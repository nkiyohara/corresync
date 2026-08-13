package agenthost

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultCatalogIsValidUniqueAndDetached(t *testing.T) {
	t.Parallel()

	catalog := DefaultCatalog()
	hosts := catalog.Hosts()
	if len(hosts) < 20 {
		t.Fatalf("default catalog has %d hosts, want a broad discovery roster", len(hosts))
	}
	for _, host := range hosts {
		if !strings.HasPrefix(host.DocumentationURL, "https://") {
			t.Errorf("host %q documentation = %q", host.ID, host.DocumentationURL)
		}
	}

	hosts[0].DisplayName = "mutated"
	hosts[0].Detection.Commands[0] = "mutated"
	fresh, ok := catalog.Lookup("codex")
	if !ok || fresh.DisplayName != "Codex" || fresh.Detection.Commands[0] != "codex" ||
		!slices.Contains(fresh.Detection.EvidenceKinds, EvidenceExecutable) {
		t.Fatalf("catalog aliases internal state through Hosts(): %+v, %t", fresh, ok)
	}
}

func TestCatalogResolvesCompatibilityAliasesAndLifecycle(t *testing.T) {
	t.Parallel()

	catalog := DefaultCatalog()
	claude, ok := catalog.Lookup("  CLAUDE ")
	if !ok || claude.ID != IDClaudeCode {
		t.Fatalf("Lookup(claude) = %+v, %t", claude, ok)
	}
	if !claude.Lifecycle.Setup || !claude.Lifecycle.Inspect || !claude.Lifecycle.Verify ||
		!claude.Lifecycle.Repair || !claude.Lifecycle.Remove {
		t.Fatalf("Claude lifecycle = %+v", claude.Lifecycle)
	}

	kimi, ok := catalog.Lookup("kimi-cli")
	if !ok || kimi.ID != IDKimiCode || kimi.Support != SupportConfigOnly {
		t.Fatalf("Lookup(kimi-cli) = %+v, %t", kimi, ok)
	}
	if !kimi.Lifecycle.Setup || kimi.Lifecycle.AdapterID != "kimi-code-cli" {
		t.Fatalf("Kimi lifecycle = %+v", kimi.Lifecycle)
	}
}

func TestCatalogRejectsAmbiguousOrUnsafeDeclarations(t *testing.T) {
	t.Parallel()

	base := Host{
		ID: "synthetic", DisplayName: "Synthetic",
		DocumentationURL: "https://example.invalid/docs",
		Surfaces:         []Surface{SurfaceCLI}, Platforms: []string{"linux"},
		Support:   SupportCatalogOnly,
		Detection: DetectionSpec{Commands: []string{"synthetic"}},
	}
	for _, fixture := range []struct {
		name  string
		hosts []Host
	}{
		{name: "duplicate alias", hosts: []Host{base, {
			ID: "another", DisplayName: "Another", Aliases: []string{"synthetic"},
			DocumentationURL: "https://example.invalid/another",
			Surfaces:         []Surface{SurfaceCLI}, Platforms: []string{"linux"}, Support: SupportCatalogOnly,
		}}},
		{name: "credential URL", hosts: []Host{func() Host {
			host := base
			host.DocumentationURL = "https://user@example.invalid/docs"
			return host
		}()}},
		{name: "unsafe command", hosts: []Host{func() Host {
			host := base
			host.Detection.Commands = []string{"sh -c something"}
			return host
		}()}},
		{name: "path traversal", hosts: []Host{func() Host {
			host := base
			host.Detection.paths = []knownPath{known("linux", rootHome, EvidenceConfig, "..", "secret")}
			return host
		}()}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewCatalog(fixture.hosts); err == nil {
				t.Fatal("NewCatalog() unexpectedly accepted unsafe declaration")
			}
		})
	}
}

func TestCatalogOnlyHostsDoNotClaimIntegrationSupport(t *testing.T) {
	t.Parallel()

	for _, host := range DefaultCatalog().Hosts() {
		if host.Support != SupportCatalogOnly {
			continue
		}
		if host.Capabilities.LocalStdioMCP || host.Capabilities.AgentSkill ||
			host.Capabilities.NativePackage != "" || host.Capabilities.Published || host.Lifecycle.AdapterID != "" {
			t.Errorf("catalog-only host %q claims an integration: %+v %+v", host.ID, host.Capabilities, host.Lifecycle)
		}
	}
}

func TestPhaseBScopesAndPortableSkillClaimsAreExact(t *testing.T) {
	t.Parallel()
	fixtures := map[ID]struct {
		scopes []Scope
		skill  bool
	}{
		IDVSCode:   {[]Scope{ScopeUser, ScopeWorkspace}, true},
		IDCursor:   {[]Scope{ScopeUser, ScopeProject}, false},
		IDWindsurf: {[]Scope{ScopeUser}, false},
		IDOpenCode: {[]Scope{ScopeUser, ScopeProject}, true},
		IDCline:    {[]Scope{ScopeUser}, true},
		IDRooCode:  {[]Scope{ScopeProject}, false},
		IDZed:      {[]Scope{ScopeUser, ScopeProject}, true},
		IDGoose:    {[]Scope{ScopeUser}, false},
	}
	catalog := DefaultCatalog()
	for id, want := range fixtures {
		host, ok := catalog.Lookup(string(id))
		if !ok || !slices.Equal(host.Scopes, want.scopes) || host.Capabilities.AgentSkill != want.skill ||
			!host.Lifecycle.Setup || !host.Lifecycle.Remove {
			t.Errorf("host %s = %+v, found %v; want scopes %v skill %v", id, host, ok, want.scopes, want.skill)
		}
	}
}
