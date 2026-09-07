package integrationlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestCommandInspectionRequiresExactArguments(t *testing.T) {
	request := lifecycleRequest(OperationSetup)
	for _, test := range []struct {
		name string
		args []string
		want State
	}{
		{"exact", request.Arguments, StateHealthy},
		{"extra", append(append([]string{}, request.Arguments...), "--help"), StateStalePath},
		{"single argument", []string{strings.Join(request.Arguments, " ")}, StateStalePath},
		{"reordered", []string{"mcp", "serve", "--config", request.Arguments[1]}, StateStalePath},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := json.Marshal(map[string]any{"command": request.Executable, "args": test.args})
			if err != nil {
				t.Fatal(err)
			}
			got := classifyCommandInspection(request, Execution{Started: true, Output: output}, false)
			if got.State != test.want {
				t.Fatalf("inspection=%+v,want=%s", got, test.want)
			}
		})
	}
}

func TestLossyTextCannotProveArgumentBoundariesOrTriggerRepair(t *testing.T) {
	request := lifecycleRequest(OperationSetup)
	// The host's args.join(" ") represents either four arguments or one argument
	// identically. Neither this text nor shell-escaped-looking text proves argv.
	for _, args := range []string{strings.Join(request.Arguments, " "), strings.Join(request.Arguments, " ") + " --help", `--config /tmp/space\ path mcp serve`} {
		output := []byte(fmt.Sprintf("corresync\n command: %s\n args: %s\n", request.Executable, args))
		executor := &scriptedExecutor{executions: []Execution{{Started: true, Output: output}}}
		engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
		plan, err := engine.Plan(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Blocked || len(plan.Actions) != 0 || plan.Previous.State != StateUnavailable {
			t.Fatalf("lossy text plan=%+v", plan)
		}
	}
	request.Host = agenthost.IDClaudeCode
	for _, scope := range []string{"User config (available in all your projects)", "Local config (private to you in this project)", "unknown", ""} {
		output := []byte(fmt.Sprintf("corresync\n Scope: %s\n command: %s\n args: %s\n", scope, request.Executable, strings.Join(request.Arguments, " ")))
		got := classifyCommandInspection(request, Execution{Started: true, Output: output}, false)
		if got.State != StateUnavailable {
			t.Fatalf("Claude text=%+v", got)
		}
	}
}

func TestUnknownInspectionFailureCannotAuthorizeSetupOrVerifyRemoval(t *testing.T) {
	for _, output := range []string{"", "not found", "internal error while loading settings", "Error: No MCP server named 'other' found.", "Error: No MCP server named 'corresync' found.\nother failure"} {
		request := lifecycleRequest(OperationSetup)
		executor := &scriptedExecutor{executions: []Execution{{Started: true, ExitCode: 1, Output: []byte(output)}}}
		engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
		plan, err := engine.Plan(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if !plan.Blocked || len(plan.Actions) != 0 {
			t.Fatalf("output=%q plan=%+v", output, plan)
		}
	}
	request := lifecycleRequest(OperationRemove)
	missing := Execution{Started: true, ExitCode: 1, Output: []byte("Error: No MCP server named 'corresync' found.")}
	if got := classifyCommandInspection(request, missing, false); got.State != StateAbsent {
		t.Fatalf("named absence=%+v", got)
	}
	executor := &scriptedExecutor{executions: []Execution{
		{Started: true, Output: healthyCommandOutput(request)},
		{Started: true, Output: healthyCommandOutput(request)},
		{Started: true},
		{Started: true, ExitCode: 1, Output: []byte("internal failure")},
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
	if result.Verified || result.Status != ResultFailedChanged {
		t.Fatalf("removal falsely verified: %+v", result)
	}
}

type partialMutationExecutor struct {
	path    string
	started bool
	runErr  error
}

func (e *partialMutationExecutor) Run(_ context.Context, command Command, _ int64) (Execution, error) {
	if command.Arguments[1] == "get" {
		return Execution{Started: true, ExitCode: 1, Output: []byte("Error: No MCP server named 'corresync' found.")}, nil
	}
	if e.started {
		if err := os.WriteFile(e.path, []byte("synthetic host wrote state"), 0o600); err != nil {
			return Execution{}, err
		}
	}
	return Execution{Started: e.started, ExitCode: 1}, e.runErr
}
func TestFailedMutationPreservesOnlyWhenItDidNotStart(t *testing.T) {
	for _, test := range []struct {
		name    string
		started bool
		err     error
	}{
		{"not started", false, nil}, {"partial nonzero", true, nil}, {"partial cancellation", true, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "host-state")
			executor := &partialMutationExecutor{path: marker, started: test.started, runErr: test.err}
			engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor}
			request := lifecycleRequest(OperationSetup)
			plan, err := engine.Plan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.Apply(t.Context(), request, plan)
			if err == nil {
				t.Fatal("expected mutation failure")
			}
			_, statErr := os.Stat(marker)
			if result.Changed != test.started || result.Verified {
				t.Fatalf("result=%+v", result)
			}
			if test.started && (result.Status != ResultFailedChanged || statErr != nil) {
				t.Fatalf("partial mutation: result=%+v, marker=%v", result, statErr)
			}
			if !test.started && (result.Status != ResultFailedPreserved || !errors.Is(statErr, os.ErrNotExist)) {
				t.Fatalf("unstarted mutation: result=%+v, marker=%v", result, statErr)
			}
		})
	}
}

func TestCodexJSONInspectionPreservesExactPathAndArguments(t *testing.T) {
	request := lifecycleRequest(OperationSetup)
	request.Arguments[1] = "/Users/synthetic/Library/Application Support/corresync/config.toml"
	entry := map[string]any{
		"name": request.ServerName, "enabled": true,
		"transport": map[string]any{"type": "stdio", "command": request.Executable, "args": request.Arguments},
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyCommandInspection(request, Execution{Started: true, Output: encoded}, false); got.State != StateHealthy {
		t.Fatalf("exact JSON=%+v", got)
	}
	add, inspect, _, _, ok, err := OfficialCommands(request)
	if err != nil || !ok || len(add.Arguments) == 0 || inspect.Arguments[len(inspect.Arguments)-1] != "--json" {
		t.Fatalf("inspect=%+v,error=%v", inspect, err)
	}
	entry["enabled"] = false
	encoded, err = json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := classifyCommandInspection(request, Execution{Started: true, Output: encoded}, false); got.State != StateDisabled {
		t.Fatalf("disabled JSON=%+v", got)
	}
}
