package integrationlifecycle

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type scriptedExecutor struct {
	executions []Execution
	commands   []Command
}

func (executor *scriptedExecutor) Run(_ context.Context, command Command, limit int64) (Execution, error) {
	if limit != maximumInspectionBytes {
		panic("unexpected output bound")
	}
	executor.commands = append(executor.commands, cloneCommand(command))
	if len(executor.executions) == 0 {
		panic("unexpected command")
	}
	execution := executor.executions[0]
	executor.executions = executor.executions[1:]
	return execution, nil
}

func lifecycleRequest(operation Operation) Request {
	return Request{
		Operation: operation, Host: agenthost.IDCodex, Scope: agenthost.ScopeUser,
		ServerName: "corresync", Executable: "/opt/corresync/bin/corr",
		Arguments: []string{"--config", "/home/test/.config/corresync/config.toml", "mcp", "serve"},
	}
}

func TestPlanBindsInspectionAndUsesRemoveThenAddForStalePath(t *testing.T) {
	t.Parallel()
	executor := &scriptedExecutor{executions: []Execution{{
		Started: true, Output: []byte("corresync\n command: /old/bin/corr\n args: --config /old/config.toml mcp serve\n"),
	}}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), lifecycleRequest(OperationSetup))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Previous.State != StateStalePath || len(plan.Actions) != 2 ||
		plan.Actions[0].Purpose != "remove_stale_corresync" || plan.Actions[1].Purpose != "register_corresync" ||
		plan.Previous.Fingerprint == "" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Actions[0].Command.Executable != "codex" || !reflect.DeepEqual(
		plan.Actions[0].Command.Arguments, []string{"mcp", "remove", "corresync"},
	) {
		t.Fatalf("remove action = %+v", plan.Actions[0])
	}
}

func TestPlanBlocksNameConflictWithoutRemoval(t *testing.T) {
	t.Parallel()
	executor := &scriptedExecutor{executions: []Execution{{
		Started: true, Output: []byte("corresync\n command: /usr/bin/other-server\n enabled: false\n"),
	}}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), lifecycleRequest(OperationRemove))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.Previous.State != StateNameConflict || len(plan.Actions) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestUnsafePackageSourceBecomesBlockedHostState(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	if err := os.RemoveAll(environment.BundleDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environment.BundleDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &scriptedExecutor{executions: []Execution{{
		Started: true, ExitCode: 1, Output: []byte("not found"),
	}}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor, Environment: environment}
	plan, err := engine.Plan(t.Context(), lifecycleRequest(OperationSetup))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.Previous.State != StateUnreadable || len(plan.Actions) != 0 ||
		!containsAny(plan.Reason, "unsafe", "unreadable") {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestUnsafePortableSkillPathBecomesBlockedHostState(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	unsafeParent := filepath.Join(environment.HomeDirectory, ".copilot")
	if err := os.WriteFile(unsafeParent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Operation: OperationSetup, Host: agenthost.IDVSCode, Scope: agenthost.ScopeUser,
		ServerName: "corresync", Executable: "/opt/corresync/bin/corr",
		Arguments: []string{"mcp", "serve"},
	}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Environment: environment}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Blocked || plan.Previous.State != StateUnreadable || len(plan.Actions) != 0 ||
		!containsAny(plan.Reason, "unsafe", "unreadable") {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestApplyFailsClosedWhenStateChangesAfterPreview(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	absent := Execution{Started: true, ExitCode: 1, Output: []byte("not found")}
	executor := &scriptedExecutor{executions: []Execution{
		absent,
		{Started: true, Output: []byte("corresync\n command: /usr/bin/other-server\n")},
	}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResultFailedPreserved || result.Changed || len(executor.commands) != 2 {
		t.Fatalf("result = %+v, commands = %+v", result, executor.commands)
	}
}

func TestApplyRechecksNoopPlanAfterPreview(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	healthy := Execution{Started: true, Output: []byte("corresync\n command: /opt/corresync/bin/corr\n args: --config /home/test/.config/corresync/config.toml mcp serve\n")}
	absent := Execution{Started: true, ExitCode: 1, Output: []byte("not found")}
	executor := &scriptedExecutor{executions: []Execution{healthy, absent}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("plan actions = %+v", plan.Actions)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResultFailedPreserved || result.Verified {
		t.Fatalf("result = %+v", result)
	}
}

func TestApplyReportsIndependentReloadAfterVerification(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	absent := Execution{Started: true, ExitCode: 1, Output: []byte("not found")}
	healthy := Execution{Started: true, Output: []byte("corresync\n command: /opt/corresync/bin/corr\n args: --config /home/test/.config/corresync/config.toml mcp serve\n")}
	executor := &scriptedExecutor{executions: []Execution{absent, absent, {Started: true}, healthy}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResultReloadRequired || !result.Changed || !result.Verified {
		t.Fatalf("result = %+v", result)
	}
}

func TestCommandInspectionNeverCopiesHostOutputIntoDetail(t *testing.T) {
	t.Parallel()
	secret := "PRIVATE_TOKEN=do-not-copy"
	inspection := classifyCommandInspection(lifecycleRequest(OperationSetup), Execution{
		Started: true, Output: []byte("corresync " + secret),
	}, false)
	if inspection.Detail == secret || containsAny(inspection.Detail, secret, "do-not-copy") {
		t.Fatalf("inspection leaked host output: %+v", inspection)
	}
}

func TestApplyRejectsTamperedPlanAction(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	executor := &scriptedExecutor{executions: []Execution{{Started: true, ExitCode: 1, Output: []byte("not found")}}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions[0].Command.Executable = "sh"
	if _, err := engine.Apply(t.Context(), request, plan); err == nil ||
		!containsAny(err.Error(), "actions do not match") {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(executor.commands) != 1 {
		t.Fatalf("tampered plan executed commands: %+v", executor.commands)
	}
}

func TestInspectionFailsClosedWhenHostInventoryIsTruncated(t *testing.T) {
	t.Parallel()
	inspection := classifyCommandInspection(lifecycleRequest(OperationSetup), Execution{
		Started: true, Truncated: true,
		Output: []byte("corresync /opt/corresync/bin/corr mcp serve"),
	}, false)
	if inspection.State != StateMalformed || !containsAny(inspection.Detail, "bounded parser limit") {
		t.Fatalf("inspection = %+v", inspection)
	}
}

func TestListInspectionDoesNotCombineDifferentHostRecords(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	inspection := classifyCommandInspection(request, Execution{
		Started: true,
		Output:  []byte("corresync /usr/bin/other-server\nother /opt/corresync/bin/corr --config /home/test/.config/corresync/config.toml mcp serve\n"),
	}, true)
	if inspection.State == StateHealthy {
		t.Fatalf("inspection combined separate records: %+v", inspection)
	}
}

func TestListInspectionParsesOneExactJSONEntry(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	inspection := classifyCommandInspection(request, Execution{
		Started: true,
		Output:  []byte(`{"mcpServers":{"other":{"command":"/usr/bin/other"},"corresync":{"command":"/opt/corresync/bin/corr","args":["--config","/home/test/.config/corresync/config.toml","mcp","serve"],"disabledTools":[]}}}`),
	}, true)
	if inspection.State != StateHealthy {
		t.Fatalf("inspection = %+v", inspection)
	}
}

func TestListInspectionAcceptsShellEscapedArgument(t *testing.T) {
	t.Parallel()
	request := lifecycleRequest(OperationSetup)
	request.Arguments[1] = "/Users/test/Library/Application Support/corresync/config.toml"
	inspection := classifyCommandInspection(request, Execution{
		Started: true,
		Output:  []byte("corresync /opt/corresync/bin/corr --config /Users/test/Library/Application\\ Support/corresync/config.toml mcp serve\n"),
	}, true)
	if inspection.State != StateHealthy {
		t.Fatalf("inspection = %+v", inspection)
	}
}
