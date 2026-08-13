package integrationlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func skillRequest(host agenthost.ID, scope agenthost.Scope, environment Environment) Request {
	request := Request{
		Operation: OperationSetup, Host: host, Scope: scope, ServerName: "corresync",
		Executable: "/opt/corresync/bin/corr", Arguments: []string{"mcp", "serve"},
	}
	if scope != agenthost.ScopeUser {
		request.ProjectDirectory = environment.HomeDirectory
	}
	return request
}

func TestPortableSkillTargetsFollowDocumentedScopes(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	fixtures := []struct {
		host  agenthost.ID
		scope agenthost.Scope
		want  string
	}{
		{agenthost.IDVSCode, agenthost.ScopeUser, filepath.Join(environment.HomeDirectory, ".copilot", "skills", "corresync")},
		{agenthost.IDVSCode, agenthost.ScopeWorkspace, filepath.Join(environment.HomeDirectory, ".github", "skills", "corresync")},
		{agenthost.IDOpenCode, agenthost.ScopeUser, filepath.Join(environment.ConfigDirectory, "opencode", "skills", "corresync")},
		{agenthost.IDOpenCode, agenthost.ScopeProject, filepath.Join(environment.HomeDirectory, ".opencode", "skills", "corresync")},
		{agenthost.IDCline, agenthost.ScopeUser, filepath.Join(environment.HomeDirectory, ".cline", "skills", "corresync")},
		{agenthost.IDZed, agenthost.ScopeUser, filepath.Join(environment.HomeDirectory, ".agents", "skills", "corresync")},
		{agenthost.IDZed, agenthost.ScopeProject, filepath.Join(environment.HomeDirectory, ".agents", "skills", "corresync")},
	}
	for _, fixture := range fixtures {
		descriptor, ok, err := resolveSkillDescriptor(environment, skillRequest(fixture.host, fixture.scope, environment))
		if err != nil || !ok || descriptor.targetRoot != fixture.want {
			t.Errorf("resolveSkillDescriptor(%s, %s) = %q, ok %v, error %v; want %q", fixture.host, fixture.scope, descriptor.targetRoot, ok, err, fixture.want)
		}
	}
}

func TestEngineInstallsAndRemovesPortableSkillWithMCP(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	request := skillRequest(agenthost.IDVSCode, agenthost.ScopeUser, environment)
	engine := Engine{Catalog: agenthost.DefaultCatalog(), Environment: environment}
	plan, err := engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked || !slicesContainAll(plan.Components, ComponentMCP, ComponentSkill) || len(plan.Actions) != 2 {
		t.Fatalf("setup plan = %+v", plan)
	}
	result, err := engine.Apply(t.Context(), request, plan)
	if err != nil || result.Status != ResultReloadRequired || !result.Verified {
		t.Fatalf("setup result = %+v, error %v", result, err)
	}
	skill, _, err := resolveSkillDescriptor(environment, request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(skill.targetRoot, "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "name: corresync") {
		t.Fatalf("Skill = %s, error %v", data, err)
	}

	request.Operation = OperationRemove
	plan, err = engine.Plan(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err = engine.Apply(t.Context(), request, plan)
	if err != nil || !result.Verified {
		t.Fatalf("remove result = %+v, error %v", result, err)
	}
	if _, err := os.Stat(skill.targetRoot); !os.IsNotExist(err) {
		t.Fatalf("Skill target remains after remove: %v", err)
	}
}

func TestPortableSkillReferenceTrackingAndConflictSafety(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := resolveSkillDescriptor(environment, skillRequest(agenthost.IDZed, agenthost.ScopeUser, environment))
	if err != nil {
		t.Fatal(err)
	}
	store := SkillStore{}
	if err := store.Install(t.Context(), descriptor, absentFileFingerprint(descriptor.targetRoot)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Inspect(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	shared := descriptor
	shared.host = agenthost.IDVSCode
	if err := store.Install(t.Context(), shared, first.Fingerprint); err != nil {
		t.Fatal(err)
	}
	owned, err := store.Inspect(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(t.Context(), descriptor, owned.Fingerprint); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.Inspect(shared)
	if err != nil || remaining.State != StateHealthy {
		t.Fatalf("shared Skill = %+v, error %v", remaining, err)
	}
	removed, err := store.Inspect(descriptor)
	if err != nil || removed.State != StateAbsent {
		t.Fatalf("removed host Skill = %+v, error %v", removed, err)
	}

	conflictEnvironment := packageEnvironment(t)
	conflict, _, err := resolveSkillDescriptor(conflictEnvironment, skillRequest(agenthost.IDCline, agenthost.ScopeUser, conflictEnvironment))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conflict.targetRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflict.targetRoot, "SKILL.md"), []byte("user authored"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.Inspect(conflict)
	if err != nil || inspection.State != StateNameConflict {
		t.Fatalf("conflicting Skill = %+v, error %v", inspection, err)
	}
}

func TestPortableSkillFailsClosedOnPostPreviewChange(t *testing.T) {
	t.Parallel()
	environment := packageEnvironment(t)
	descriptor, _, err := resolveSkillDescriptor(environment, skillRequest(agenthost.IDOpenCode, agenthost.ScopeUser, environment))
	if err != nil {
		t.Fatal(err)
	}
	store := SkillStore{}
	if err := store.Install(t.Context(), descriptor, absentFileFingerprint(descriptor.targetRoot)); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.Inspect(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(descriptor.targetRoot, "SKILL.md"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Install(t.Context(), descriptor, inspection.Fingerprint); err == nil ||
		!strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("Install() error = %v", err)
	}
}
