package integrationlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestClaudeInspectionReadsExactScopeAndPreservesSpaceBoundaries(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project with spaces")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := Environment{HomeDirectory: home, ConfigDirectory: home}
	request := lifecycleRequest(OperationSetup)
	request.Host = agenthost.IDClaudeCode
	request.ProjectDirectory = project
	request.Arguments[1] = filepath.Join(home, "Application Support", "config.toml")
	healthy := map[string]any{"command": request.Executable, "args": request.Arguments, "type": "stdio"}
	wrong := map[string]any{"command": "/other/server", "args": []string{}}
	userDocument := map[string]any{"mcpServers": map[string]any{"corresync": healthy}, "projects": map[string]any{
		project:                              map[string]any{"mcpServers": map[string]any{"corresync": wrong}},
		filepath.Join(home, "other project"): map[string]any{"mcpServers": map[string]any{"corresync": healthy}},
	}}
	write := func(path string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude.json"), userDocument)
	write(filepath.Join(project, ".mcp.json"), map[string]any{"mcpServers": map[string]any{"corresync": healthy}})
	for _, test := range []struct {
		scope agenthost.Scope
		want  State
		file  string
	}{
		{agenthost.ScopeUser, StateHealthy, filepath.Join(home, ".claude.json")},
		{agenthost.ScopeLocal, StateNameConflict, filepath.Join(home, ".claude.json")},
		{agenthost.ScopeProject, StateHealthy, filepath.Join(project, ".mcp.json")},
	} {
		t.Run(string(test.scope), func(t *testing.T) {
			scoped := request
			scoped.Scope = test.scope
			engine := Engine{Catalog: agenthost.DefaultCatalog(), Environment: environment}
			got, err := engine.Inspect(t.Context(), scoped)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.want || got.Scope != test.scope || got.Path != test.file {
				t.Fatalf("inspection=%+v", got)
			}
		})
	}
	// A user-scoped installation must not mask absence in a different local project.
	request.Scope = agenthost.ScopeLocal
	request.ProjectDirectory = filepath.Join(home, "unconfigured project")
	got, err := inspectClaudeRegistration(environment, request)
	if err != nil || got.State != StateAbsent {
		t.Fatalf("other project=%+v,error=%v", got, err)
	}
	// A single array element containing all launch words is structurally different.
	healthy["args"] = []string{"--config " + request.Arguments[1] + " mcp serve"}
	write(filepath.Join(home, ".claude.json"), userDocument)
	request.Scope = agenthost.ScopeUser
	got, err = inspectClaudeRegistration(environment, request)
	if err != nil || got.State != StateStalePath {
		t.Fatalf("single argument=%+v,error=%v", got, err)
	}
}

func TestClaudeInspectionRejectsUnsafeOrAmbiguousScopeSources(t *testing.T) {
	home := t.TempDir()
	environment := Environment{HomeDirectory: home, ConfigDirectory: home}
	request := lifecycleRequest(OperationSetup)
	request.Host = agenthost.IDClaudeCode
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := inspectClaudeRegistration(environment, request)
	if err != nil || got.State != StateMalformed {
		t.Fatalf("malformed=%+v,error=%v", got, err)
	}
	environment.ClaudeConfigOverride = true
	got, err = inspectClaudeRegistration(environment, request)
	if err != nil || got.State != StateUnavailable {
		t.Fatalf("override=%+v,error=%v", got, err)
	}
}
