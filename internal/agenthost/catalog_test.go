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

func TestCatalogResolvesCompatibilityAliasesAndOfficialCLI(t *testing.T) {
	t.Parallel()

	catalog := DefaultCatalog()
	claude, ok := catalog.Lookup("  CLAUDE ")
	if !ok || claude.ID != IDClaudeCode {
		t.Fatalf("Lookup(claude) = %+v, %t", claude, ok)
	}
	command, arguments, ok := claude.OfficialCLI("work_mail")
	if !ok || command != "claude" || strings.Join(arguments, " ") != "mcp get work_mail" {
		t.Fatalf("OfficialCLI() = %q %#v, %t", command, arguments, ok)
	}
	arguments[2] = "mutated"
	_, freshArguments, _ := claude.OfficialCLI("personal")
	if strings.Join(freshArguments, " ") != "mcp get personal" {
		t.Fatalf("OfficialCLI() retained caller mutation: %#v", freshArguments)
	}

	kimi, ok := catalog.Lookup("kimi-cli")
	if !ok || kimi.ID != IDKimiCode || kimi.Support != SupportConfigOnly {
		t.Fatalf("Lookup(kimi-cli) = %+v, %t", kimi, ok)
	}
	if _, _, ok := kimi.OfficialCLI("corresync"); ok {
		t.Fatal("config-only host unexpectedly exposed an official setup command")
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
