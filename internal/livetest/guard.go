package livetest

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strings"
)

// LiveTag is the build tag that compiles the live harnesses.
const LiveTag = "live"

var (
	tagsFlagPattern  = regexp.MustCompile(`(?:^|[\s"'])-{1,2}(?:build-)?tags(?:=|\s+)["']?([A-Za-z0-9_.,\- ]+)`)
	commandSeparator = regexp.MustCompile(`&&|\|\||[;|]`)
	goflagsPattern   = regexp.MustCompile(`GOFLAGS[^\n]*tags[^\n]*\blive\b`)
)

// AutomationViolations reports every line of an automation file (a CI
// workflow, the Taskfile, or the commit-hook configuration) that could run a
// live harness. Compiling the harnesses is allowed: `go vet` and
// `golangci-lint` may name the live tag because neither executes a test.
// Setting or forwarding any live opt-in variable is never allowed.
func AutomationViolations(content string) []string {
	var violations []string
	for index, line := range strings.Split(content, "\n") {
		number := index + 1
		if strings.Contains(line, "CORRESYNC_LIVE_") {
			violations = append(violations, fmt.Sprintf("line %d references a live opt-in variable", number))
		}
		if goflagsPattern.MatchString(line) {
			violations = append(violations, fmt.Sprintf("line %d sets the live tag through GOFLAGS", number))
		}
		for _, command := range commandSeparator.Split(line, -1) {
			compileOnly := strings.Contains(command, "go vet") || strings.Contains(command, "golangci-lint")
			for _, match := range tagsFlagPattern.FindAllStringSubmatch(command, -1) {
				if hasLiveTag(match[1]) && !compileOnly {
					violations = append(violations, fmt.Sprintf("line %d passes the live tag to a command that may run tests", number))
				}
			}
		}
	}
	return violations
}

func hasLiveTag(list string) bool {
	for _, tag := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		if tag == LiveTag {
			return true
		}
	}
	return false
}

// SourceFacts describes how one Go file relates to the live gate.
type SourceFacts struct {
	// LiveOnly is true when the file is compiled only with the live tag.
	LiveOnly bool
	// Violations lists Test functions that do not start with an opt-in check.
	Violations []string
}

// InspectSource parses one Go file and reports whether it is live-only and
// whether each of its Test functions starts with an opt-in check: either
// livetest.Require or an environment check that skips the test.
func InspectSource(name string, source []byte) (SourceFacts, error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.ParseComments)
	if err != nil {
		return SourceFacts{}, err
	}
	var facts SourceFacts
	for _, group := range file.Comments {
		if group.Pos() > file.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expression, err := constraint.Parse(comment.Text)
			if err != nil {
				return SourceFacts{}, err
			}
			facts.LiveOnly = requiresLiveTag(expression)
		}
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || !strings.HasPrefix(function.Name.Name, "Test") ||
			function.Name.Name == "TestMain" {
			continue
		}
		if function.Body == nil || !startsWithOptIn(function.Body.List) {
			facts.Violations = append(facts.Violations,
				function.Name.Name+" does not start with an opt-in check")
		}
	}
	return facts, nil
}

// requiresLiveTag reports whether the file drops out of every build that
// lacks the live tag, whatever the other tags are.
func requiresLiveTag(expression constraint.Expr) bool {
	var others []string
	collectTags(expression, func(tag string) {
		if tag != LiveTag && !slices.Contains(others, tag) {
			others = append(others, tag)
		}
	})
	if len(others) > 16 {
		return false
	}
	for assignment := range 1 << len(others) {
		satisfied := expression.Eval(func(tag string) bool {
			index := slices.Index(others, tag)
			return index >= 0 && assignment&(1<<index) != 0
		})
		if satisfied {
			return false
		}
	}
	return true
}

func collectTags(expression constraint.Expr, visit func(string)) {
	switch expression := expression.(type) {
	case *constraint.TagExpr:
		visit(expression.Tag)
	case *constraint.NotExpr:
		collectTags(expression.X, visit)
	case *constraint.AndExpr:
		collectTags(expression.X, visit)
		collectTags(expression.Y, visit)
	case *constraint.OrExpr:
		collectTags(expression.X, visit)
		collectTags(expression.Y, visit)
	}
}

// startsWithOptIn accepts a test whose first statement is livetest.Require,
// or whose leading statements only read environment variables and are
// followed by an if statement that tests one of them and skips the test.
func startsWithOptIn(statements []ast.Stmt) bool {
	environment := map[string]bool{}
	for _, statement := range statements {
		switch statement := statement.(type) {
		case *ast.AssignStmt:
			if len(statement.Rhs) == 1 && isSelectorCall(statement.Rhs[0], "livetest", "Require") {
				return true
			}
			for _, value := range statement.Rhs {
				if !isSelectorCall(value, "os", "Getenv") {
					return false
				}
			}
			for _, name := range statement.Lhs {
				if identifier, ok := name.(*ast.Ident); ok {
					environment[identifier.Name] = true
				}
			}
		case *ast.ExprStmt:
			return isSelectorCall(statement.X, "livetest", "Require")
		case *ast.IfStmt:
			return statement.Init == nil && readsEnvironment(statement.Cond, environment) &&
				containsSkip(statement.Body)
		default:
			return false
		}
	}
	return false
}

func readsEnvironment(expression ast.Expr, environment map[string]bool) bool {
	found := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			found = found || isSelectorCall(node, "os", "Getenv")
		case *ast.Ident:
			found = found || environment[node.Name]
		}
		return true
	})
	return found
}

func isSelectorCall(expression ast.Expr, receiver, name string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == receiver
}

func containsSkip(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok &&
				(selector.Sel.Name == "Skip" || selector.Sel.Name == "Skipf" || selector.Sel.Name == "SkipNow") {
				found = true
			}
		}
		return true
	})
	return found
}
