package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/kong"
)

// The rules these tests enforce are described in CONTRIBUTING.md, "Writing
// for the command line".

const maximumSummaryLength = 70

// helpStyleAllowlist exempts top-level commands whose help has not been
// rewritten yet. Remove an entry when its files are converted;
// TestHelpStyleAllowlistHasNoCleanEntries fails once an entry is no longer
// needed. Keys are top-level command names; the value names the work package
// that owns the files.
var helpStyleAllowlist = map[string]string{
	"account":  "B. setup, settings, accounts",
	"agenda":   "A. everyday commands",
	"calendar": "A. everyday commands",
	"config":   "B. setup, settings, accounts",
	"mail":     "A. everyday commands",
	"queries":  "A. everyday commands",
	"setup":    "B. setup, settings, accounts",
	"tasks":    "A. everyday commands",
}

// bannedHelpWords are review vocabulary that must not reach a reader. Text in
// backticks (a literal flag value or protocol term) is exempt.
var bannedHelpWords = []string{
	"exact",
	"explicit",
	"explicitly",
	"versioned",
	"opaque",
	"bounded",
	"durable",
	"deterministic",
	"deterministically",
	"typed",
	"guarded",
	"provider-neutral",
	"canonical",
	"normalized",
	"session owner",
	"secret-free",
	"credential-free",
	"content-free",
	"isolated",
}

var (
	bannedHelpPattern = regexp.MustCompile(
		`(?i)\b(` + strings.Join(quoteAll(bannedHelpWords), "|") + `)\b`,
	)
	backtickSpan = regexp.MustCompile("`[^`]*`")
)

func quoteAll(words []string) []string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, strings.ReplaceAll(regexp.QuoteMeta(word), " ", `\s+`))
	}
	return quoted
}

type helpStyleViolation struct {
	root    string
	path    string
	element string
	text    string
	problem string
}

func (violation helpStyleViolation) String() string {
	return fmt.Sprintf("%s %s: %s\n    %q", violation.path, violation.element, violation.problem, violation.text)
}

func TestHelpTextFollowsWritingRules(t *testing.T) {
	t.Parallel()

	var report []string
	for _, violation := range helpStyleViolations(t) {
		if _, exempt := helpStyleAllowlist[violation.root]; exempt {
			continue
		}
		report = append(report, violation.String())
	}
	if len(report) > 0 {
		t.Fatalf(
			"help text breaks the CONTRIBUTING.md writing rules in %d places:\n  %s",
			len(report),
			strings.Join(report, "\n  "),
		)
	}
}

func TestHelpStyleAllowlistHasNoCleanEntries(t *testing.T) {
	t.Parallel()

	dirty := map[string]bool{}
	for _, violation := range helpStyleViolations(t) {
		dirty[violation.root] = true
	}
	visible := map[string]bool{}
	for _, command := range helpStyleModel(t).Children {
		if !command.Hidden {
			visible[command.Name] = true
		}
	}
	for root := range helpStyleAllowlist {
		switch {
		case !visible[root]:
			t.Errorf("helpStyleAllowlist names %q, which is not a visible top-level command", root)
		case !dirty[root]:
			t.Errorf("help for %q now follows the writing rules; remove it from helpStyleAllowlist", root)
		}
	}
}

func TestHelpStyleCheckFindsViolations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		text string
		want string
	}{
		{"Move one versioned message.", "versioned"},
		{"Run the local Session  Owner.", "Session  Owner"},
		{"Return Provider-Neutral tasks.", "Provider-Neutral"},
		{"Read an exact-version draft.", "exact"},
	} {
		if got := bannedHelpPattern.FindString(test.text); got != test.want {
			t.Errorf("banned word in %q = %q, want %q", test.text, got, test.want)
		}
	}
	for _, clean := range []string{
		"Show exactly one account.",
		"Use `exact` matching.",
		"Read text you retyped.",
	} {
		if got := bannedHelpPattern.FindString(stripLiterals(clean)); got != "" {
			t.Errorf("banned word in %q = %q, want none", clean, got)
		}
	}
}

func TestRootHelpGroupsCoverEveryVisibleCommand(t *testing.T) {
	t.Parallel()

	known := map[string]bool{}
	for _, group := range rootHelpGroups {
		if known[group.Key] {
			t.Errorf("rootHelpGroups lists %q twice", group.Key)
		}
		known[group.Key] = true
	}
	used := map[string]bool{}
	for _, command := range helpStyleModel(t).Children {
		key := rootHelpGroupKey(command)
		switch {
		case command.Hidden && key != "":
			t.Errorf("hidden command %q has helpgroup %q; hidden commands stay out of the root help", command.Name, key)
		case command.Hidden:
		case key == "":
			t.Errorf("top-level command %q has no helpgroup tag; add one of the rootHelpGroups keys", command.Name)
		case !known[key]:
			t.Errorf("top-level command %q has unknown helpgroup %q", command.Name, key)
		default:
			used[key] = true
		}
	}
	for key := range known {
		if !used[key] {
			t.Errorf("root help group %q has no commands", key)
		}
	}
}

func TestRootHelpListsCommandsInGroups(t *testing.T) {
	t.Parallel()

	model := helpStyleModel(t)
	help := rootHelp(model, consoleView{})
	want := []string{
		"Everyday:", "  mail ", "  calendar ", "  agenda ", "  tasks ", "  queries ",
		"Accounts and sign-in:", "  setup ", "  settings ", "  account ", "  auth ",
		"AI agents:", "  mcp ", "  integrations ",
		"Maintenance:", "  doctor ", "  update ", "  config ", "  completion ", "  feedback ", "  version ",
		"Advanced:", "  monitor ", "  events ", "  import ", "  daemon ",
		"Flags:", "Start:", "  corr setup ",
	}
	position := 0
	for _, fragment := range want {
		index := strings.Index(help[position:], fragment)
		if index < 0 {
			t.Fatalf("root help is missing %q after byte %d:\n%s", fragment, position, help)
		}
		position += index + len(fragment)
	}
	for _, hidden := range []string{"login", "messages", "Other:"} {
		if strings.Contains(help, "  "+hidden+" ") || strings.Contains(help, hidden+"\n") {
			t.Errorf("root help shows %q:\n%s", hidden, help)
		}
	}
	for line := range strings.Lines(help) {
		if width := utf8.RuneCountInString(strings.TrimSuffix(line, "\n")); width > 80 {
			t.Errorf("root help line is %d characters wide: %q", width, line)
		}
	}
}

func helpStyleModel(t *testing.T) *kong.Application {
	t.Helper()

	var commandLine cli
	parser, err := kong.New(
		&commandLine,
		kong.Name(commandName),
		kong.Description(productTagline),
		kong.Exit(func(int) { t.Fatal("building the help model exited") }),
	)
	if err != nil {
		t.Fatalf("build command model: %v", err)
	}
	return parser.Model
}

func helpStyleViolations(t *testing.T) []helpStyleViolation {
	t.Helper()

	model := helpStyleModel(t)
	var violations []helpStyleViolation
	check := func(root, path, element, text string, summary bool) {
		add := func(problem string) {
			violations = append(violations, helpStyleViolation{
				root: root, path: path, element: element, text: text, problem: problem,
			})
		}
		for _, word := range bannedHelpPattern.FindAllString(stripLiterals(text), -1) {
			add(fmt.Sprintf("uses %q", word))
		}
		if !summary {
			return
		}
		if strings.HasPrefix(text, "Review and ") {
			add(`starts with "Review and"`)
		}
		if length := utf8.RuneCountInString(text); length > maximumSummaryLength {
			add(fmt.Sprintf("summary is %d characters, more than %d", length, maximumSummaryLength))
		}
		if text == "" {
			add("has no summary")
		} else if !strings.HasSuffix(text, ".") {
			add("summary does not end with a period")
		}
	}
	checkFlags := func(root, path string, node *kong.Node) {
		for _, flag := range node.Flags {
			if !flag.Hidden {
				check(root, path, "--"+flag.Name, flag.Help, false)
			}
		}
		for _, argument := range node.Positional {
			check(root, path, "<"+argument.Name+">", argument.Help, false)
		}
	}
	var walk func(root string, node *kong.Node)
	walk = func(root string, node *kong.Node) {
		path := node.FullPath()
		check(root, path, "summary", node.Help, true)
		if node.Detail != "" {
			check(root, path, "details", node.Detail, false)
		}
		checkFlags(root, path, node)
		for _, child := range node.Children {
			if !child.Hidden {
				walk(root, child)
			}
		}
	}
	checkFlags("", commandName, model.Node)
	check("", commandName, "description", model.Help, false)
	for _, command := range model.Children {
		if !command.Hidden {
			walk(command.Name, command)
		}
	}
	slices.SortStableFunc(violations, func(left, right helpStyleViolation) int {
		return strings.Compare(left.path, right.path)
	})
	return violations
}

func stripLiterals(text string) string {
	return backtickSpan.ReplaceAllString(text, "``")
}
