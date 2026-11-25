package livetest

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repositoryRoot opens the module root so every read below stays inside it.
func repositoryRoot(t *testing.T) *os.Root {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			return root
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found above the test directory")
		}
		directory = parent
	}
}

// TestDefaultAutomationNeverRunsLiveHarnesses is the guard that keeps every
// live harness out of the default test command, the race run, task verify,
// commit hooks, and CI.
func TestDefaultAutomationNeverRunsLiveHarnesses(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	workflows, err := fs.Glob(root.FS(), ".github/workflows/*.y*ml")
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 {
		t.Fatal("no workflow found; the guard would check nothing")
	}
	files := append([]string{"Taskfile.yml", "prek.toml"}, workflows...)
	for _, name := range files {
		content, err := root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range AutomationViolations(string(content)) {
			t.Errorf("%s: %s", name, violation)
		}
	}
}

// TestEveryLiveHarnessIsTaggedAndGated checks every Go file in the module: a
// *_live_test.go file must compile only with the live tag, a live-only file
// must be a test file, every Test function in a live-only file must start
// with an opt-in check, and the opt-in variable may appear only in live-only
// files or in this package.
func TestEveryLiveHarnessIsTaggedAndGated(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	liveFiles := 0
	err := fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "dist", ".release", ".claude":
				// .claude holds local agent worktrees: full copies of other
				// revisions that are not part of this tree.
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(relative, ".go") {
			return nil
		}
		source, err := root.ReadFile(relative)
		if err != nil {
			return err
		}
		facts, err := InspectSource(relative, source)
		if err != nil {
			return err
		}
		namedLive := strings.HasSuffix(relative, "_live_test.go")
		switch {
		case namedLive && !facts.LiveOnly:
			t.Errorf("%s must start with //go:build %s", relative, LiveTag)
		case facts.LiveOnly && !strings.HasSuffix(relative, "_test.go"):
			t.Errorf("%s is live-only but not a test file", relative)
		case !facts.LiveOnly && !strings.HasPrefix(relative, "internal/livetest/") &&
			strings.Contains(string(source), EnvConfirm):
			t.Errorf("%s references %s outside a live-only file", relative, EnvConfirm)
		}
		if facts.LiveOnly {
			liveFiles++
			for _, violation := range facts.Violations {
				t.Errorf("%s: %s", relative, violation)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if liveFiles == 0 {
		t.Fatal("no live harness found; the guard would check nothing")
	}
	for _, info := range Catalog() {
		if info.Harness == "" {
			continue
		}
		source, err := root.ReadFile(info.Harness)
		if err != nil {
			t.Errorf("%s: harness %s: %v", info.ID, info.Harness, err)
			continue
		}
		if !strings.Contains(string(source), "func "+info.Test+"(") ||
			!strings.Contains(string(source), "CORRESYNC_LIVE_CONFIRM="+string(info.ID)+"\n") {
			t.Errorf("%s: %s must define %s and document its opt-in", info.ID, info.Harness, info.Test)
		}
	}
}

// TestEvidenceIndexListsEveryRoute keeps docs/evidence/README.md in step with
// the catalog.
func TestEvidenceIndexListsEveryRoute(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	content, err := root.ReadFile("docs/evidence/README.md")
	if err != nil {
		t.Fatal(err)
	}
	index := string(content)
	for _, info := range Catalog() {
		row := ""
		for _, line := range strings.Split(index, "\n") {
			if strings.HasPrefix(line, "| `"+string(info.ID)+"` |") {
				row = line
			}
		}
		switch {
		case row == "":
			t.Errorf("docs/evidence/README.md has no index row for `%s`", info.ID)
		case info.Harness != "" && !strings.Contains(row, info.Harness):
			t.Errorf("index row for `%s` must name %s", info.ID, info.Harness)
		case info.Manual != "" && !strings.Contains(row, "manual procedure"):
			t.Errorf("index row for `%s` must say manual procedure", info.ID)
		}
	}
	for _, name := range []string{EnvConfirm, EnvAccount, EnvConfig, EnvStateDir, EnvWrite,
		EnvCalendarName, EnvTaskListName, EnvRecord, EnvDeployment, EnvPlatform, EnvBrowserKeyring} {
		if !strings.Contains(index, name) {
			t.Errorf("docs/evidence/README.md does not document %s", name)
		}
	}
}

func TestAutomationViolations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line      string
		violating bool
	}{
		{"      - go test -shuffle=on ./...", false},
		{"      - go vet -tags live ./...", false},
		{"      - golangci-lint run --build-tags live", false},
		{"        run: go test -race ./...", false},
		{"      - go test -tags live ./...", true},
		{"      - go test -tags=live ./cmd/corr", true},
		{"      - go test -tags 'integration,live' ./...", true},
		{`        run: go test "-tags=live" ./...`, true},
		{"        -tags live -run TestLive", true},
		{"      GOFLAGS: -tags=live", true},
		{"      CORRESYNC_LIVE_CONFIRM: outlook-web", true},
		{"  env: { CORRESYNC_LIVE_ACCOUNT: work }", true},
		{"      - go test -tags livefixtures ./...", false},
		{"      - go vet -tags live ./... && go test -tags live ./...", true},
	}
	for _, test := range cases {
		if got := len(AutomationViolations(test.line)) != 0; got != test.violating {
			t.Errorf("AutomationViolations(%q) violating=%v, want %v", test.line, got, test.violating)
		}
	}
}

func TestInspectSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		source     string
		liveOnly   bool
		violations []string
	}{
		{
			name:     "gated by Require",
			source:   "//go:build live\n\npackage x\n\nfunc TestLiveA(t *testing.T) {\n\toptIn := livetest.Require(t, livetest.RouteJMAP)\n\t_ = optIn\n}\n",
			liveOnly: true,
		},
		{
			name:     "gated by legacy Getenv check",
			source:   "//go:build live\n\npackage x\n\nfunc TestLiveB(t *testing.T) {\n\tif os.Getenv(\"CORRESYNC_LIVE_CONFIRM\") != \"b\" {\n\t\tt.Skip(\"opt in\")\n\t}\n}\n",
			liveOnly: true,
		},
		{
			name:     "environment read then skip",
			source:   "//go:build live\n\npackage x\n\nfunc TestLiveF(t *testing.T) {\n\tpath := os.Getenv(\"X\")\n\tif path == \"\" {\n\t\tt.Skip(\"opt in\")\n\t}\n}\n",
			liveOnly: true,
		},
		{
			name:       "work before the check",
			source:     "//go:build live\n\npackage x\n\nfunc TestLiveG(t *testing.T) {\n\tconnect()\n\tif os.Getenv(\"X\") == \"\" {\n\t\tt.Skip()\n\t}\n}\n",
			liveOnly:   true,
			violations: []string{"TestLiveG does not start with an opt-in check"},
		},
		{
			name:       "ungated test",
			source:     "//go:build live\n\npackage x\n\nfunc TestLiveC(t *testing.T) {\n\tconnect()\n}\n",
			liveOnly:   true,
			violations: []string{"TestLiveC does not start with an opt-in check"},
		},
		{
			name:       "check that does not skip",
			source:     "//go:build live\n\npackage x\n\nfunc TestLiveD(t *testing.T) {\n\tif os.Getenv(\"CORRESYNC_LIVE_CONFIRM\") == \"\" {\n\t\tt.Log(\"x\")\n\t}\n}\n",
			liveOnly:   true,
			violations: []string{"TestLiveD does not start with an opt-in check"},
		},
		{
			name:     "live or another tag is not live-only",
			source:   "//go:build live || linux\n\npackage x\n",
			liveOnly: false,
		},
		{
			name:     "live and another tag is live-only",
			source:   "//go:build live && !windows\n\npackage x\n",
			liveOnly: true,
		},
		{
			name:     "untagged",
			source:   "package x\n\nfunc TestE(t *testing.T) {}\n",
			liveOnly: false, violations: []string{"TestE does not start with an opt-in check"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			facts, err := InspectSource("x_test.go", []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if facts.LiveOnly != test.liveOnly || !slices.Equal(facts.Violations, test.violations) {
				t.Fatalf("InspectSource() = %+v, want liveOnly=%v violations=%v", facts, test.liveOnly, test.violations)
			}
		})
	}
}
