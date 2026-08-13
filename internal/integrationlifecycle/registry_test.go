package integrationlifecycle

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestOfficialCommandsUseTypedExecutableAndArgv(t *testing.T) {
	t.Parallel()
	executable := filepath.Join(t.TempDir(), "corr binary")
	configPath := testAbsolutePath("config", "config.toml")
	projectPath := testAbsolutePath("work", "project")
	request := Request{
		Operation: OperationSetup, Host: agenthost.IDClaudeCode,
		Scope: agenthost.ScopeProject, ServerName: "work_mail", Executable: executable,
		Arguments:        []string{"--config", configPath, "mcp", "serve"},
		ProjectDirectory: projectPath,
	}
	add, inspect, remove, list, ok, err := OfficialCommands(request)
	if err != nil || !ok || list {
		t.Fatalf("OfficialCommands() = ok %v list %v error %v", ok, list, err)
	}
	wantAdd := []string{"mcp", "add", "--scope", "project", "work_mail", "--", executable, "--config", configPath, "mcp", "serve"}
	if add.Executable != "claude" || !reflect.DeepEqual(add.Arguments, wantAdd) {
		t.Fatalf("add = %+v, want claude %#v", add, wantAdd)
	}
	if add.WorkingDirectory != projectPath || inspect.WorkingDirectory != projectPath || remove.WorkingDirectory != projectPath {
		t.Fatalf("commands do not bind the project directory: %+v %+v %+v", add, inspect, remove)
	}
	if !reflect.DeepEqual(inspect.Arguments, []string{"mcp", "get", "work_mail"}) ||
		!reflect.DeepEqual(remove.Arguments, []string{"mcp", "remove", "--scope", "project", "work_mail"}) {
		t.Fatalf("inspect/remove = %+v / %+v", inspect, remove)
	}
}

func TestOfficialCommandsCoverEveryPhaseAHost(t *testing.T) {
	t.Parallel()
	fixtures := map[agenthost.ID]agenthost.Scope{
		agenthost.IDCodex: agenthost.ScopeUser, agenthost.IDClaudeCode: agenthost.ScopeUser,
		agenthost.IDGitHubCopilot: agenthost.ScopeUser, agenthost.IDGeminiCLI: agenthost.ScopeUser,
		agenthost.IDQwenCode: agenthost.ScopeUser, agenthost.IDQoder: agenthost.ScopeUser,
		agenthost.IDKimiCode: agenthost.ScopeUser,
	}
	for host, scope := range fixtures {
		request := Request{Operation: OperationSetup, Host: host, Scope: scope, ServerName: "corresync", Executable: testAbsolutePath("bin", "corr")}
		add, inspect, remove, _, ok, err := OfficialCommands(request)
		if err != nil || !ok || add.Executable == "" || inspect.Executable == "" || remove.Executable == "" {
			t.Errorf("host %s commands = %+v %+v %+v ok %v error %v", host, add, inspect, remove, ok, err)
		}
	}
}

func TestRequestRejectsRelativeExecutableAndControlArguments(t *testing.T) {
	t.Parallel()
	base := Request{Operation: OperationSetup, Host: agenthost.IDCodex, Scope: agenthost.ScopeUser, ServerName: "corresync", Executable: "corr"}
	if err := base.Validate(); err == nil {
		t.Fatal("Validate() accepted a relative executable")
	}
	base.Executable = testAbsolutePath("bin", "corr")
	base.Arguments = []string{"mcp\nserve"}
	if err := base.Validate(); err == nil {
		t.Fatal("Validate() accepted a control character")
	}
}
