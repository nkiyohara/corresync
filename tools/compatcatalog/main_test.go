package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/compatcatalog"
)

func defaultCatalog(t *testing.T) compatcatalog.Catalog {
	t.Helper()
	catalog, err := compatcatalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

// TestCheckedInDocumentationIsCurrent lets plain `go test ./...` catch a
// stale table even where the task runner is not used.
func TestCheckedInDocumentationIsCurrent(t *testing.T) {
	t.Parallel()

	stale, err := apply(defaultCatalog(t), "../..", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) > 0 {
		t.Fatalf("stale generated tables in %v; run task docs:generate", stale)
	}
}

func TestRenderListsEveryEntryOnce(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	evidence := renderEvidence(catalog, "../")
	tasks := renderTasks(catalog, "../")
	rowPrefix := func(entry compatcatalog.Entry) string {
		if entry.Service == compatcatalog.ServiceBoundary {
			return "| " + entry.ProviderName + " | `" + string(entry.Availability) + "` |"
		}
		return "| " + entry.ProviderName + " (`" + entry.Provider + "`) | `" + entry.Route +
			"` | `" + string(entry.Availability) + "` |"
	}
	// One provider route can serve several services, so each row prefix
	// must appear exactly as often as the catalog lists it.
	expected := make(map[string]int)
	for _, entry := range catalog.Entries {
		expected[rowPrefix(entry)]++
	}
	for _, entry := range catalog.Entries {
		row := rowPrefix(entry)
		if count := strings.Count(evidence, row); count != expected[row] {
			t.Errorf("%s appears %d times in the evidence tables, want %d", entry.Key(), count, expected[row])
		}
		if entry.Service == compatcatalog.ServiceTasks {
			row := "| `" + entry.Provider + "` | `" + string(entry.Availability) + "` |"
			if count := strings.Count(tasks, row); count != 1 {
				t.Errorf("%s appears %d times in the task table", entry.Key(), count)
			}
		}
	}
	for _, want := range []string{
		"| Slack (`slack`) | `slack_api` | `disabled` |",
		"| Mattermost (`mattermost`) | `mattermost_api` | `preview` |",
		"[`internal/provider/outlookweb`](../internal/provider/outlookweb)",
		"[#108](https://github.com/nkiyohara/corresync/issues/108)",
	} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence tables do not contain %q", want)
		}
	}
}

func TestLiveObservedEntryLinksItsRecord(t *testing.T) {
	t.Parallel()

	entry := compatcatalog.Entry{Live: compatcatalog.LiveEvidence{
		State: compatcatalog.LiveObserved, Summary: "Read path", Record: "docs/evidence/x.md",
	}}
	if got, want := live(entry, "../"), "`live-observed`: Read path ([record](../docs/evidence/x.md))"; got != want {
		t.Fatalf("live() = %q, want %q", got, want)
	}
}

func TestReplaceRegionKeepsSurroundingProse(t *testing.T) {
	t.Parallel()

	document := "before\n<!-- compatcatalog:begin x -->\nold\n<!-- compatcatalog:end x -->\nafter\n"
	got, err := replaceRegion([]byte(document), "x", "new\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "before\n<!-- compatcatalog:begin x -->\nnew\n<!-- compatcatalog:end x -->\nafter\n"
	if string(got) != want {
		t.Fatalf("replaceRegion() = %q", got)
	}
	for _, invalid := range []string{
		"no markers\n",
		"<!-- compatcatalog:end x -->\n<!-- compatcatalog:begin x -->\n",
		"<!-- compatcatalog:begin x -->\n<!-- compatcatalog:begin x -->\n<!-- compatcatalog:end x -->\n",
	} {
		if _, err := replaceRegion([]byte(invalid), "x", "new\n"); err == nil {
			t.Errorf("replaceRegion(%q) succeeded", invalid)
		}
	}
}

func TestCheckDetectsStaleDocumentWithoutWriting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, document := range []string{"docs/compatibility.md", "docs/tasks.md"} {
		name := strings.TrimSuffix(filepath.Base(document), ".md")
		region := "evidence"
		if name == "tasks" {
			region = "tasks"
		}
		path := filepath.Join(root, filepath.FromSlash(document))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		content := "# Title\n\n" + beginMarker(region) + "\nstale\n" + endMarker(region) + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := defaultCatalog(t)
	stale, err := apply(catalog, root, false)
	if err != nil || len(stale) != 2 {
		t.Fatalf("check stale = %v, %v", stale, err)
	}
	if stale, err = apply(catalog, root, true); err != nil || len(stale) != 2 {
		t.Fatalf("write stale = %v, %v", stale, err)
	}
	if stale, err = apply(catalog, root, false); err != nil || len(stale) != 0 {
		t.Fatalf("after write stale = %v, %v", stale, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "docs", "tasks.md")) // #nosec G304 -- test temporary directory.
	if err != nil || !strings.HasPrefix(string(content), "# Title\n\n") {
		t.Fatalf("surrounding prose changed: %q, %v", content, err)
	}
}
