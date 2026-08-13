package integrationlifecycle

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func fileEngine(t *testing.T) (Engine, Environment) {
	t.Helper()
	home := t.TempDir()
	environment := Environment{
		HomeDirectory: home, ConfigDirectory: filepath.Join(home, ".config"), GOOS: "linux",
	}
	return Engine{Catalog: agenthost.DefaultCatalog(), Environment: environment}, environment
}

func cursorRequest(environment Environment, operation Operation) Request {
	return Request{
		Operation: operation, Host: agenthost.IDCursor, Scope: agenthost.ScopeUser,
		ServerName: "corresync", Executable: testAbsolutePath("bin", "corr"),
		Arguments: []string{"--config", filepath.Join(environment.HomeDirectory, ".config", "corresync", "config.toml"), "mcp", "serve"},
	}
}

func TestJSONAdapterSetupIsAtomicPrivateAndIdempotent(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	request := cursorRequest(environment, OperationSetup)
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(environment.HomeDirectory, ".cursor", "mcp.json")
	if plan.Previous.State != StateAbsent || len(plan.Actions) != 1 ||
		plan.Actions[0].File == nil || plan.Actions[0].File.Path != path ||
		!plan.Actions[0].File.Normalization {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResultReloadRequired || !result.Verified {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	second, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Previous.State != StateHealthy || len(second.Actions) != 0 {
		t.Fatalf("second plan = %+v", second)
	}
}

func TestJSONAdapterPreservesExistingHostDirectoryMode(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	directory := filepath.Join(environment.HomeDirectory, ".cursor")
	if err := os.MkdirAll(directory, 0o755); err != nil { // #nosec G301 -- host-owned public directory fixture.
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil { // #nosec G302 -- verifies that integration setup preserves this mode.
		t.Fatal(err)
	}
	request := cursorRequest(environment, OperationSetup)
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(t.Context(), request, plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("host directory mode = %o", info.Mode().Perm())
	}
}

func TestJSONAdapterRepairPreservesUnrelatedStateAndRecoveryCopy(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	request := cursorRequest(environment, OperationRepair)
	path := filepath.Join(environment.HomeDirectory, ".cursor", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{
  // user-owned server and settings must survive
  "schemaVersion": 1,
  "theme": "dark",
  "largeId": 9007199254740993,
  "mcpServers": {
    "personal": {"command": "/usr/bin/personal", "env": {"TOKEN": "private"}},
    "corresync": {"command": "/old/bin/corr", "args": ["mcp", "serve"], "alwaysAllow": ["*"], "note": "keep"},
  },
}
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Previous.State != StateStalePath || len(plan.Actions) != 1 || plan.Actions[0].Purpose != "repair_corresync" {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified {
		t.Fatalf("result = %+v", result)
	}
	recovery, err := os.ReadFile(path + ".corresync.bak") // #nosec G304 -- path is below this test's temporary root.
	if err != nil || string(recovery) != string(original) {
		t.Fatalf("recovery = %q, error %v", recovery, err)
	}
	updated, err := os.ReadFile(path) // #nosec G304 -- path is below this test's temporary root.
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(updated, &document); err != nil {
		t.Fatal(err)
	}
	if document["theme"] != "dark" {
		t.Fatalf("updated document lost unrelated settings: %s", updated)
	}
	if !bytes.Contains(updated, []byte(`"largeId": 9007199254740993`)) {
		t.Fatalf("updated document changed an unrelated large integer: %s", updated)
	}
	servers := document["mcpServers"].(map[string]any)
	personal := servers["personal"].(map[string]any)
	if personal["command"] != "/usr/bin/personal" {
		t.Fatalf("updated document lost unrelated server: %s", updated)
	}
	corresync := servers["corresync"].(map[string]any)
	if corresync["command"] != request.Executable || corresync["note"] != "keep" {
		t.Fatalf("updated Corresync entry = %+v", corresync)
	}
	if _, exists := corresync["alwaysAllow"]; exists {
		t.Fatalf("updated Corresync entry retained host auto-approval: %+v", corresync)
	}
}

func TestJSONAdapterRemoveDeletesOnlyOwnedEntry(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	setup := cursorRequest(environment, OperationSetup)
	plan, err := engine.Plan(t.Context(), setup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply(t.Context(), setup, plan); err != nil {
		t.Fatal(err)
	}
	remove := cursorRequest(environment, OperationRemove)
	plan, err = engine.Plan(t.Context(), remove)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(t.Context(), remove, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Status != ResultReloadRequired {
		t.Fatalf("result = %+v", result)
	}
	inspection, err := engine.Inspect(t.Context(), remove)
	if err != nil || inspection.State != StateAbsent {
		t.Fatalf("inspection = %+v, error %v", inspection, err)
	}
}

func TestJSONAdapterRefusesUnsafeRecoveryFileWithoutChangingTarget(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	request := cursorRequest(environment, OperationRepair)
	path := filepath.Join(environment.HomeDirectory, ".cursor", "mcp.json")
	original := []byte(`{"mcpServers":{"corresync":{"command":"/old/bin/corr","args":["mcp","serve"]}}}`)
	writeHostFixture(t, path, original, 0o600)
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	recovery := path + ".corresync.bak"
	writeHostFixture(t, recovery, []byte("do not replace"), 0o622)
	if _, err := engine.Apply(t.Context(), request, plan); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("Apply() error = %v", err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is below this test's temporary root.
	if err != nil || string(data) != string(original) {
		t.Fatalf("target changed: %q, error %v", data, err)
	}
	data, err = os.ReadFile(recovery) // #nosec G304 -- path is below this test's temporary root.
	if err != nil || string(data) != "do not replace" {
		t.Fatalf("recovery changed: %q, error %v", data, err)
	}
}

func TestJSONAdapterFailsClosedOnConflictMalformedModeAndSymlink(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name  string
		setup func(*testing.T, string)
		state State
	}{
		{
			name: "name conflict", state: StateNameConflict,
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeHostFixture(t, path, []byte(`{"mcpServers":{"corresync":{"command":"/usr/bin/other","disabled":true}}}`), 0o600)
			},
		},
		{
			name: "malformed", state: StateMalformed,
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeHostFixture(t, path, []byte(`{"mcpServers":`), 0o600)
			},
		},
		{
			name: "unsafe mode", state: StateUnreadable,
			setup: func(t *testing.T, path string) {
				t.Helper()
				writeHostFixture(t, path, []byte(`{}`), 0o622)
			},
		},
		{
			name: "symlink target", state: StateUnreadable,
			setup: func(t *testing.T, path string) {
				t.Helper()
				outside := filepath.Join(t.TempDir(), "outside.json")
				writeHostFixture(t, outside, []byte(`{}`), 0o600)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			engine, environment := fileEngine(t)
			request := cursorRequest(environment, OperationSetup)
			path := filepath.Join(environment.HomeDirectory, ".cursor", "mcp.json")
			fixture.setup(t, path)
			plan, err := engine.Plan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Previous.State != fixture.state || !plan.Blocked || len(plan.Actions) != 0 {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

func writeHostFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestJSONAdapterRequiresExplicitProjectRoot(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	request := cursorRequest(environment, OperationSetup)
	request.Scope = agenthost.ScopeProject
	if _, err := engine.Plan(t.Context(), request); err == nil {
		t.Fatal("Plan() accepted an implicit project root")
	}
}

func TestJSONAdaptersCoverDocumentedPhaseBPathsAndShapes(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name  string
		host  agenthost.ID
		scope agenthost.Scope
		path  func(Environment, string) string
	}{
		{"vscode user", agenthost.IDVSCode, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.ConfigDirectory, "Code", "User", "mcp.json")
		}},
		{"vscode workspace", agenthost.IDVSCode, agenthost.ScopeWorkspace, func(_ Environment, project string) string {
			return filepath.Join(project, ".vscode", "mcp.json")
		}},
		{"cursor user", agenthost.IDCursor, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.HomeDirectory, ".cursor", "mcp.json")
		}},
		{"cursor project", agenthost.IDCursor, agenthost.ScopeProject, func(_ Environment, project string) string {
			return filepath.Join(project, ".cursor", "mcp.json")
		}},
		{"windsurf user", agenthost.IDWindsurf, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.HomeDirectory, ".codeium", "windsurf", "mcp_config.json")
		}},
		{"opencode user", agenthost.IDOpenCode, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.ConfigDirectory, "opencode", "opencode.json")
		}},
		{"opencode project", agenthost.IDOpenCode, agenthost.ScopeProject, func(_ Environment, project string) string {
			return filepath.Join(project, ".opencode", "opencode.json")
		}},
		{"cline user", agenthost.IDCline, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.HomeDirectory, ".cline", "data", "settings", "cline_mcp_settings.json")
		}},
		{"roo project", agenthost.IDRooCode, agenthost.ScopeProject, func(_ Environment, project string) string {
			return filepath.Join(project, ".roo", "mcp.json")
		}},
		{"zed user", agenthost.IDZed, agenthost.ScopeUser, func(environment Environment, _ string) string {
			return filepath.Join(environment.ConfigDirectory, "zed", "settings.json")
		}},
		{"zed project", agenthost.IDZed, agenthost.ScopeProject, func(_ Environment, project string) string {
			return filepath.Join(project, ".zed", "settings.json")
		}},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			engine, environment := fileEngine(t)
			project := filepath.Join(environment.HomeDirectory, "project")
			if err := os.MkdirAll(project, 0o700); err != nil {
				t.Fatal(err)
			}
			request := Request{
				Operation: OperationSetup, Host: fixture.host, Scope: fixture.scope,
				ServerName: "corresync", Executable: testAbsolutePath("bin", "corr"),
				Arguments: []string{"--config", filepath.Join(environment.ConfigDirectory, "corresync", "config.toml"), "mcp", "serve"},
			}
			if fixture.scope != agenthost.ScopeUser {
				request.ProjectDirectory = project
			}
			plan, err := engine.Plan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			wantPath := fixture.path(environment, project)
			if plan.Blocked || len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionJSON ||
				plan.Actions[0].File == nil || plan.Actions[0].File.Path != wantPath {
				t.Fatalf("plan = %+v, want path %s", plan, wantPath)
			}
			result, err := engine.Apply(t.Context(), request, plan)
			if err != nil || !result.Verified {
				t.Fatalf("result = %+v, error %v", result, err)
			}
			inspection, err := engine.Inspect(t.Context(), request)
			if err != nil || inspection.State != StateHealthy {
				t.Fatalf("inspection = %+v, error %v", inspection, err)
			}
		})
	}
}
