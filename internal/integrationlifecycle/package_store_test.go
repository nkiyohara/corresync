package integrationlifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func packageEnvironment(t *testing.T) Environment {
	t.Helper()
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	config := filepath.Join(root, "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"plugins/corresync/README.md":                                 "# Corresync\n",
		"plugins/corresync/assets/icon.svg":                           "<svg/>\n",
		"plugins/corresync/skills/corresync/SKILL.md":                 "---\nname: corresync\ndescription: Test.\n---\n",
		"plugins/corresync/skills/corresync/agents/openai.yaml":       "interface: {}\n",
		"plugins/corresync/.codex-plugin/plugin.json":                 `{"name":"corresync","version":"1.2.3","mcpServers":"./.mcp.json"}`,
		"plugins/corresync/.claude-plugin/plugin.json":                `{"name":"corresync","version":"1.2.3","mcpServers":"./.mcp.json"}`,
		".agents/plugins/marketplace.json":                            `{"name":"corresync","plugins":[]}`,
		".claude-plugin/marketplace.json":                             `{"name":"corresync","version":"1.2.3","plugins":[]}`,
		"integrations/gemini-cli/corresync/gemini-extension.json":     `{"name":"corresync","version":"1.2.3","mcpServers":{"corresync":{}}}`,
		"integrations/gemini-cli/corresync/skills/corresync/SKILL.md": "---\nname: corresync\ndescription: Test.\n---\n",
	}
	for relative, data := range fixtures {
		path := filepath.Join(bundle, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Environment{
		HomeDirectory: root, ConfigDirectory: config, BundleDirectory: bundle,
		ManagedDirectory: filepath.Join(config, "corresync", "integration-packages"), GOOS: "linux",
	}
}

func TestPackageDescriptorsStripDuplicateMCPAndKeepVersion(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	fixtures := map[agenthost.ID]Component{
		agenthost.IDCodex: ComponentPlugin, agenthost.IDClaudeCode: ComponentPlugin,
		agenthost.IDGitHubCopilot: ComponentPlugin, agenthost.IDGeminiCLI: ComponentExtension,
	}
	for host, wantKind := range fixtures {
		descriptor, ok, err := (PackageStore{}).Describe(environment, host)
		if err != nil || !ok {
			t.Fatalf("Describe(%s) = ok %v error %v", host, ok, err)
		}
		if descriptor.kind != wantKind || descriptor.version != "1.2.3" || descriptor.sourceFingerprint == "" {
			t.Errorf("descriptor %s = %+v", host, descriptor)
		}
		for name, data := range descriptor.files {
			if strings.HasSuffix(name, ".json") {
				var document map[string]any
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatal(err)
				}
				if _, exists := document["mcpServers"]; exists {
					t.Errorf("%s package manifest %s retains duplicate MCP declaration", host, name)
				}
			}
		}
	}
}

func TestPackageStoreStagesUpdatesAndRemovesOnlyManagedTree(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, ok, err := (PackageStore{}).Describe(environment, agenthost.IDGeminiCLI)
	if err != nil || !ok {
		t.Fatalf("Describe() = ok %v error %v", ok, err)
	}
	store := PackageStore{}
	absentFingerprint := absentFileFingerprint(descriptor.targetRoot)
	if err := store.Stage(t.Context(), descriptor, descriptor.sourceFingerprint, absentFingerprint); err != nil {
		t.Fatal(err)
	}
	state, version, fingerprint, err := store.InspectTarget(descriptor)
	if err != nil || state != StateHealthy || version != "1.2.3" {
		t.Fatalf("InspectTarget() = %s %q, error %v", state, version, err)
	}
	manifest, err := os.ReadFile(filepath.Join(descriptor.targetRoot, "gemini-extension.json"))
	if err != nil || strings.Contains(string(manifest), "mcpServers") {
		t.Fatalf("staged manifest = %s, error %v", manifest, err)
	}
	markerInfo, err := os.Stat(filepath.Join(descriptor.targetRoot, packageMarkerName))
	if err != nil || markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %v, error %v", markerInfo, err)
	}

	if err := store.Stage(t.Context(), descriptor, descriptor.sourceFingerprint, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(descriptor.targetRoot + ".corresync.bak"); err != nil {
		t.Fatalf("recovery tree is absent: %v", err)
	}
	_, _, fingerprint, err = store.InspectTarget(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(t.Context(), descriptor, fingerprint); err != nil {
		t.Fatal(err)
	}
	state, _, _, err = store.InspectTarget(descriptor)
	if err != nil || state != StateAbsent {
		t.Fatalf("InspectTarget() after remove = %s, error %v", state, err)
	}
}

func TestPackageStoreRefusesUnmanagedAndChangedSources(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := (PackageStore{}).Describe(environment, agenthost.IDCodex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(descriptor.targetRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(descriptor.targetRoot, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (PackageStore{}).Stage(t.Context(), descriptor, descriptor.sourceFingerprint, absentFileFingerprint(descriptor.targetRoot)); err == nil {
		t.Fatalf("Stage() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(descriptor.targetRoot, "user.txt"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("unmanaged target changed: %q, error %v", data, err)
	}
	if err := (PackageStore{}).Stage(t.Context(), descriptor, strings.Repeat("0", 64), absentFileFingerprint(descriptor.targetRoot)); err == nil ||
		!strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("Stage() changed-source error = %v", err)
	}
}

func TestPackageStoreRefusesTargetChangedAfterPreview(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := (PackageStore{}).Describe(environment, agenthost.IDGeminiCLI)
	if err != nil {
		t.Fatal(err)
	}
	store := PackageStore{}
	if err := store.Stage(t.Context(), descriptor, descriptor.sourceFingerprint, absentFileFingerprint(descriptor.targetRoot)); err != nil {
		t.Fatal(err)
	}
	_, _, fingerprint, err := store.InspectTarget(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(descriptor.targetRoot, "gemini-extension.json")
	if err := os.WriteFile(manifestPath, []byte(`{"name":"tampered"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(t.Context(), descriptor, descriptor.sourceFingerprint, fingerprint); err == nil ||
		!strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("Stage() error = %v", err)
	}
	data, err := os.ReadFile(manifestPath) // #nosec G304 -- path is below this test's temporary root.
	if err != nil || string(data) != `{"name":"tampered"}` {
		t.Fatalf("tampered target changed: %q, error %v", data, err)
	}
}

func TestPackageStoreRefusesUnexpectedManagedTreeDirectories(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := (PackageStore{}).Describe(environment, agenthost.IDGeminiCLI)
	if err != nil {
		t.Fatal(err)
	}
	store := PackageStore{}
	if err := store.Stage(t.Context(), descriptor, descriptor.sourceFingerprint, absentFileFingerprint(descriptor.targetRoot)); err != nil {
		t.Fatal(err)
	}
	_, _, fingerprint, err := store.InspectTarget(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(descriptor.targetRoot, "user-directory")
	if err := os.Mkdir(unexpected, 0o700); err != nil {
		t.Fatal(err)
	}
	state, _, _, err := store.InspectTarget(descriptor)
	if err == nil || state != StateNameConflict {
		t.Fatalf("InspectTarget() = %s, error %v", state, err)
	}
	if err := store.Remove(t.Context(), descriptor, fingerprint); err == nil {
		t.Fatal("Remove() accepted a managed tree with an unexpected directory")
	}
	if _, err := os.Stat(unexpected); err != nil {
		t.Fatalf("unexpected directory was removed: %v", err)
	}
}

func TestNativePackageClassificationRequiresManagedOwnership(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := (PackageStore{}).Describe(environment, agenthost.IDCodex)
	if err != nil {
		t.Fatal(err)
	}
	adapter, _, ok, err := resolveNativeAdapter(environment, lifecycleRequest(OperationSetup))
	if err != nil || !ok {
		t.Fatalf("resolveNativeAdapter() = ok %v error %v", ok, err)
	}
	execution := Execution{Started: true, Output: []byte(`{"installed":[{"pluginId":"corresync@corresync-local","version":"1.2.3","enabled":true}]}`)}
	component := classifyNativePackage(adapter, descriptor, StateAbsent, "", execution)
	if component.State != StateNameConflict {
		t.Fatalf("unowned component = %+v", component)
	}
	component = classifyNativePackage(adapter, descriptor, StateHealthy, "1.2.3", execution)
	if component.State != StateHealthy {
		t.Fatalf("owned component = %+v", component)
	}
}

func TestEngineAppliesAndVerifiesMCPWithNativeSkillPackage(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	request := lifecycleRequest(OperationSetup)
	descriptor, _, err := (PackageStore{}).Describe(environment, request.Host)
	if err != nil {
		t.Fatal(err)
	}
	absentMCP := Execution{Started: true, ExitCode: 1, Output: []byte("not found")}
	absentPackage := Execution{Started: true, Output: []byte(`{"installed":[]}`)}
	absentSource := Execution{Started: true, Output: []byte(`{"marketplaces":[]}`)}
	healthyMCP := Execution{Started: true, Output: []byte(
		"corresync\n command: /opt/corresync/bin/corr\n args: --config /home/test/.config/corresync/config.toml mcp serve\n",
	)}
	healthyPackage := Execution{Started: true, Output: []byte(
		`{"installed":[{"pluginId":"corresync@corresync-local","version":"1.2.3","enabled":true}]}`,
	)}
	healthySource := Execution{Started: true, Output: []byte(
		`{"marketplaces":[{"name":"corresync-local","root":` + string(mustJSON(t, descriptor.targetRoot)) + `}]}`,
	)}
	executor := &scriptedExecutor{executions: []Execution{
		absentMCP, absentPackage, absentSource,
		absentMCP, absentPackage, absentSource,
		{Started: true}, {Started: true}, {Started: true},
		healthyMCP, healthyPackage, healthySource,
	}}
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Executor: executor, Environment: environment}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked || !slicesContainAll(plan.Components, ComponentMCP, ComponentPlugin, ComponentStage, ComponentSource) {
		t.Fatalf("plan components = %+v, blocked %v: %s", plan.Components, plan.Blocked, plan.Reason)
	}
	if len(plan.Actions) != 4 || plan.Actions[0].Purpose != "register_corresync" ||
		plan.Actions[1].Kind != ActionPackage || plan.Actions[2].Purpose != "register_private_package_source" ||
		plan.Actions[3].Purpose != "install_native_skill_package" {
		t.Fatalf("plan actions = %+v", plan.Actions)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil || result.Status != ResultReloadRequired || !result.Verified {
		t.Fatalf("result = %+v, error %v", result, err)
	}
	manifest, err := os.ReadFile(filepath.Join(descriptor.targetRoot, "plugins", "corresync", ".codex-plugin", "plugin.json"))
	if err != nil || strings.Contains(string(manifest), "mcpServers") {
		t.Fatalf("staged Codex manifest = %s, error %v", manifest, err)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func slicesContainAll(values []Component, expected ...Component) bool {
	for _, want := range expected {
		found := false
		for _, value := range values {
			found = found || value == want
		}
		if !found {
			return false
		}
	}
	return true
}
