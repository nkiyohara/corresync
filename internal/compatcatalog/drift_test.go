package compatcatalog

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/rollout"
)

const repositoryRoot = "../.."

func defaultCatalog(t *testing.T) Catalog {
	t.Helper()
	catalog, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	return catalog
}

// TestMessagingAvailabilityMatchesReleaseGate keeps documentation from calling
// a closed route a preview, or a preview available, as happened with Slack.
func TestMessagingAvailabilityMatchesReleaseGate(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	routes := declaredConstants(t, "MessagingRouteKind")
	if len(routes) == 0 {
		t.Fatal("no messaging routes declared in the domain package")
	}
	for _, route := range routes {
		var matches []Entry
		for _, entry := range catalog.Entries {
			if entry.Service == ServiceMessages && entry.Route == route {
				matches = append(matches, entry)
			}
		}
		if len(matches) != 1 {
			t.Errorf("messaging route %q has %d catalog entries, want 1", route, len(matches))
			continue
		}
		entry := matches[0]
		status, err := rollout.MessagingStatus(
			domain.MessagingProviderID(entry.Provider),
			domain.MessagingRouteKind(entry.Route),
		)
		if err != nil {
			t.Errorf("MessagingStatus(%s, %s) error = %v", entry.Provider, entry.Route, err)
			continue
		}
		var want []Availability
		switch {
		case status.Enabled && status.Preview:
			want = []Availability{AvailabilityPreview}
		case status.Enabled:
			want = []Availability{AvailabilityAvailable, AvailabilityAvailableWithSetup}
		default:
			want = []Availability{AvailabilityDisabled}
		}
		if !slices.Contains(want, entry.Availability) {
			t.Errorf("catalog says %s is %q; release gate %+v requires one of %v",
				entry.Key(), entry.Availability, status, want)
		}
	}
	for _, entry := range catalog.Entries {
		if entry.Service == ServiceMessages && !slices.Contains(routes, entry.Route) {
			t.Errorf("catalog messaging entry %s names an undeclared route", entry.Key())
		}
	}
}

func TestGoogleAvailabilityMatchesReleaseGate(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	byo, managed := 0, 0
	for _, entry := range catalog.Entries {
		if entry.Provider != string(domain.ProviderGoogle) &&
			entry.Provider != string(domain.ProviderGoogleTasks) {
			continue
		}
		enabled := rollout.GoogleBYOOAuthEnabled
		if entry.Route == "managed_oauth" {
			enabled = rollout.GoogleManagedOAuthEnabled
			managed++
		} else {
			byo++
		}
		if enabled != entry.Availability.Reachable() {
			t.Errorf("catalog says %s is %q; release gate enabled = %t",
				entry.Key(), entry.Availability, enabled)
		}
		if !enabled && entry.Availability != AvailabilityDisabled {
			t.Errorf("closed Google route %s must be %q", entry.Key(), AvailabilityDisabled)
		}
	}
	if byo == 0 || managed == 0 {
		t.Fatalf("catalog lacks Google entries: user-owned %d, managed %d", byo, managed)
	}
}

// TestEveryDeclaredProviderIsCataloged makes a newly reserved identifier show
// up in the public matrix instead of disappearing from it.
func TestEveryDeclaredProviderIsCataloged(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	providers := make(map[string][]Entry)
	for _, entry := range catalog.Entries {
		providers[entry.Provider] = append(providers[entry.Provider], entry)
	}
	declared := append(declaredConstants(t, "ProviderID"), declaredConstants(t, "MessagingProviderID")...)
	if len(declared) < 10 {
		t.Fatalf("found only %d declared providers", len(declared))
	}
	for _, provider := range declared {
		entries, ok := providers[provider]
		if !ok {
			t.Errorf("domain provider %q is missing from the compatibility catalog", provider)
			continue
		}
		for _, entry := range entries {
			if entry.Availability == AvailabilityPlanned && len(entry.Deterministic.Tests) != 0 {
				t.Errorf("planned entry %s claims deterministic tests", entry.Key())
			}
		}
	}
}

func TestEveryNamedTestExists(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	for _, entry := range catalog.Entries {
		for _, reference := range entry.Deterministic.Tests {
			target := filepath.Join(repositoryRoot, filepath.FromSlash(reference))
			info, err := os.Stat(target)
			if err != nil {
				t.Errorf("%s names missing test %q: %v", entry.Key(), reference, err)
				continue
			}
			if !info.IsDir() {
				continue
			}
			files, err := os.ReadDir(target)
			if err != nil {
				t.Errorf("%s: read %q: %v", entry.Key(), reference, err)
				continue
			}
			if !slices.ContainsFunc(files, func(file os.DirEntry) bool {
				return !file.IsDir() && IsTestFileName(file.Name())
			}) {
				t.Errorf("%s names package %q, which has no test file", entry.Key(), reference)
			}
		}
		if entry.Live.State == LiveObserved {
			if _, err := os.Stat(filepath.Join(repositoryRoot, filepath.FromSlash(entry.Live.Record))); err != nil {
				t.Errorf("%s links missing evidence record %q: %v", entry.Key(), entry.Live.Record, err)
			}
		}
	}
}

// declaredConstants returns the string values of every exported constant of
// the named type in the domain package's non-test sources.
func declaredConstants(t *testing.T, typeName string) []string {
	t.Helper()
	directory := filepath.Join(repositoryRoot, "internal", "domain")
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	var values []string
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, filepath.Join(directory, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				identifier, ok := value.Type.(*ast.Ident)
				if !ok || identifier.Name != typeName {
					continue
				}
				for _, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("%s constant is not a string literal", typeName)
					}
					unquoted, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					values = append(values, unquoted)
				}
			}
		}
	}
	return values
}
