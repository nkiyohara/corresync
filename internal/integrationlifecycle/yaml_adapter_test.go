package integrationlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func gooseRequest(environment Environment, operation Operation) Request {
	return Request{
		Operation: operation, Host: agenthost.IDGoose, Scope: agenthost.ScopeUser,
		ServerName: "corresync", Executable: "/opt/corresync/bin/corr",
		Arguments: []string{"--config", filepath.Join(environment.HomeDirectory, ".config", "corresync", "config.toml"), "mcp", "serve"},
	}
}

func TestGooseYAMLSetupPreservesOtherExtensionsAndComments(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	request := gooseRequest(environment, OperationSetup)
	path := filepath.Join(environment.ConfigDirectory, "goose", "config.yaml")
	original := []byte(`# provider choice belongs to the user
provider: local
extensions:
  filesystem: # keep this extension
    type: stdio
    name: filesystem
    enabled: true
    cmd: npx
    args: [-y, server-filesystem]
`)
	writeHostFixture(t, path, original, 0o600)
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Previous.State != StateAbsent || len(plan.Actions) != 1 || plan.Actions[0].Kind != ActionYAML {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ResultReloadRequired || !result.Verified {
		t.Fatalf("result = %+v", result)
	}
	updated, err := os.ReadFile(path) // #nosec G304 -- path is below this test's temporary root.
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# provider choice belongs to the user", "filesystem:", "# keep this extension", "corresync:", "cmd: /opt/corresync/bin/corr", "enabled: true"} {
		if !strings.Contains(string(updated), want) {
			t.Errorf("updated YAML does not contain %q:\n%s", want, updated)
		}
	}
	for _, forbidden := range []string{"token", "password", "auto_approve", "always_allow"} {
		if strings.Contains(strings.ToLower(string(updated)), forbidden) {
			t.Errorf("updated YAML contains forbidden field %q:\n%s", forbidden, updated)
		}
	}
	recovery, err := os.ReadFile(path + ".corresync.bak") // #nosec G304 -- path is below this test's temporary root.
	if err != nil || string(recovery) != string(original) {
		t.Fatalf("recovery = %q, error %v", recovery, err)
	}
}

func TestGooseYAMLRepairAndRemoveTouchOnlyCorresync(t *testing.T) {
	t.Parallel()
	engine, environment := fileEngine(t)
	path := filepath.Join(environment.ConfigDirectory, "goose", "config.yaml")
	writeHostFixture(t, path, []byte(`extensions:
  other:
    type: stdio
    name: other
    enabled: true
    cmd: /usr/bin/other
    args: []
  corresync:
    type: stdio
    name: corresync
    enabled: false
    cmd: /old/bin/corr
    args: [mcp, serve]
    auto_approve: ["*"]
    envs:
      SHOULD_DISAPPEAR: private
`), 0o600)
	repair := gooseRequest(environment, OperationRepair)
	plan, err := engine.Plan(t.Context(), repair)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Previous.State != StateDisabled {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := engine.Apply(t.Context(), repair, plan); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.ReadFile(path) // #nosec G304 -- path is below this test's temporary root.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repaired), "auto_approve") {
		t.Fatalf("repaired YAML retained host auto-approval: %s", repaired)
	}
	remove := gooseRequest(environment, OperationRemove)
	plan, err = engine.Plan(t.Context(), remove)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Apply(t.Context(), remove, plan)
	if err != nil || !result.Verified {
		t.Fatalf("result = %+v, error %v", result, err)
	}
	updated, err := os.ReadFile(path) // #nosec G304 -- path is below this test's temporary root.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "other:") || strings.Contains(string(updated), "corresync:") ||
		strings.Contains(string(updated), "auto_approve") {
		t.Fatalf("updated YAML = %s", updated)
	}
}

func TestGooseYAMLFailsClosedOnAliasesAndNameConflict(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct {
		name  string
		data  string
		state State
	}{
		{name: "alias", data: "base: &base {type: stdio}\nextensions: {corresync: *base}\n", state: StateMalformed},
		{name: "conflict", data: "extensions:\n  corresync:\n    type: stdio\n    enabled: false\n    cmd: /usr/bin/other\n    args: []\n", state: StateNameConflict},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			engine, environment := fileEngine(t)
			request := gooseRequest(environment, OperationSetup)
			path := filepath.Join(environment.ConfigDirectory, "goose", "config.yaml")
			writeHostFixture(t, path, []byte(fixture.data), 0o600)
			plan, err := engine.Plan(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Previous.State != fixture.state || !plan.Blocked {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}
