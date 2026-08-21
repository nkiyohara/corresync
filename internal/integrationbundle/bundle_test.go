package integrationbundle

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

var testSkill = []byte(`---
name: corresync
description: Use guarded local Corresync tools.
---

# Corresync

Never auto-approve writes.
`)

func TestSpecification(t *testing.T) {
	t.Parallel()
	spec, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if spec.SourceVersion != "0.9.0-rc.3" {
		t.Fatalf("source version = %q", spec.SourceVersion)
	}
	if spec.Effects.AutoApproval {
		t.Fatal("automatic approval must remain disabled")
	}
	if utf8.RuneCountInString(spec.Description) > 100 {
		t.Fatalf("registry description has %d characters", utf8.RuneCountInString(spec.Description))
	}
	if len(spec.PublicationChannels) != 13 {
		t.Fatalf("publication channels = %d, want 13", len(spec.PublicationChannels))
	}
	if got := PortableLaunch(); got.Command != "corr" || !slices.Equal(got.Args, []string{"mcp", "serve"}) || got.Transport != "stdio" {
		t.Fatalf("portable launch = %#v", got)
	}
}

func TestRenderIsDeterministicAndVersioned(t *testing.T) {
	t.Parallel()
	inputs := Inputs{Skill: testSkill, Icon: []byte("<svg/>\n")}
	first, err := Render("1.2.3-rc.4", inputs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render("1.2.3-rc.4", inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 13 || len(second) != len(first) {
		t.Fatalf("outputs = %d and %d, want 13", len(first), len(second))
	}
	for index := range first {
		if first[index].Path != second[index].Path || !bytes.Equal(first[index].Data, second[index].Data) {
			t.Fatalf("render differs at index %d", index)
		}
		if index > 0 && first[index-1].Path >= first[index].Path {
			t.Fatalf("outputs are not path-sorted: %q then %q", first[index-1].Path, first[index].Path)
		}
		text := string(first[index].Data)
		for _, forbidden := range []string{"0.7.0", "oauth_token", "refresh_token", "password", "autoApprove\": true"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden value %q", first[index].Path, forbidden)
			}
		}
	}
	assertOutputContains(t, first, "plugins/corresync/.mcp.json", `"command": "corr"`)
	assertOutputContains(t, first, "plugins/corresync/.codex-plugin/plugin.json", `"version": "1.2.3-rc.4"`)
	assertOutputContains(t, first, "integrations/kiro/corresync/POWER.md", "Kiro Web")
	assertOutputContains(t, first, "docs/generated/integration-bundles.md", "hosted ChatGPT")
	assertOutputContains(t, first, "docs/generated/publication-channels.md", "`1.2.3-rc.4`")
	assertOutputContains(t, first, "docs/generated/publication-channels.md", "`not-listed`")
}

func TestCodexManifestStarterPromptsMeetDirectoryLimits(t *testing.T) {
	t.Parallel()
	manifest := codexManifest(MustLoad(), "1.2.3")
	pluginInterface, ok := manifest["interface"].(map[string]any)
	if !ok {
		t.Fatal("Codex manifest interface is missing")
	}
	prompts, ok := pluginInterface["defaultPrompt"].([]string)
	if !ok || len(prompts) == 0 || len(prompts) > 3 {
		t.Fatalf("Codex starter prompts = %#v, want one to three", pluginInterface["defaultPrompt"])
	}
	for index, prompt := range prompts {
		if strings.TrimSpace(prompt) == "" || utf8.RuneCountInString(prompt) > 128 {
			t.Fatalf("Codex starter prompt %d is empty or exceeds 128 characters", index)
		}
	}
}

func TestPublicationSnapshotSeparatesSourceAndExternalVersions(t *testing.T) {
	t.Parallel()
	spec := MustLoad()
	channels, err := spec.PublicationSnapshot("2.0.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	versions := make(map[string]string, len(channels))
	for _, channel := range channels {
		versions[channel.ID] = channel.ObservedVersion
	}
	if versions["mcp-registry"] != "0.8.6" || versions["github-releases"] != "0.8.6" {
		t.Fatalf("published versions changed with source snapshot: %#v", versions)
	}
	if versions["claude-marketplace"] != "2.0.0-rc.1" {
		t.Fatalf("source version = %q", versions["claude-marketplace"])
	}
	if versions["openai-plugin-directory"] != "" {
		t.Fatalf("unlisted channel claims version %q", versions["openai-plugin-directory"])
	}
}

func TestRenderStableAndPreviewVersions(t *testing.T) {
	t.Parallel()
	inputs := Inputs{Skill: testSkill, Icon: []byte("<svg/>\n")}
	for _, version := range []string{"1.2.3", "1.2.3-rc.1", "1.2.3+build.9"} {
		outputs, err := Render(version, inputs)
		if err != nil {
			t.Fatalf("Render(%q): %v", version, err)
		}
		assertOutputContains(t, outputs, ".claude-plugin/marketplace.json", `"version": "`+version+`"`)
	}
	for _, version := range []string{"v1.2.3", "1.2", "latest", "1.2.3/../../x"} {
		if _, err := Render(version, inputs); err == nil {
			t.Fatalf("Render(%q) succeeded", version)
		}
	}
}

func TestRenderMCPBAndRegistryShareIdentity(t *testing.T) {
	t.Parallel()
	mcpbData, err := RenderMCPBManifest("2.3.4-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	registryData, err := RenderRegistryManifest("2.3.4-rc.1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	var mcpb, registry map[string]any
	if err := json.Unmarshal(mcpbData, &mcpb); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(registryData, &registry); err != nil {
		t.Fatal(err)
	}
	if mcpb["name"] != "corresync" || registry["name"] != "io.github.nkiyohara/corresync" {
		t.Fatalf("identities = %v and %v", mcpb["name"], registry["name"])
	}
	if mcpb["version"] != registry["version"] {
		t.Fatalf("versions = %v and %v", mcpb["version"], registry["version"])
	}
	if err := ValidateRegistryManifest(registryData); err != nil {
		t.Fatal(err)
	}
	registryData = bytes.Replace(registryData, []byte("stdio"), []byte("http"), 1)
	if err := ValidateRegistryManifest(registryData); err == nil {
		t.Fatal("registry validator accepted a remote transport")
	}
}

func TestSkillFrontmatterRequiresExactTopLevelKeys(t *testing.T) {
	t.Parallel()
	for _, skill := range []string{
		"---\nothername: corresync\ndescription: Test.\n---\n# Test\n",
		"---\nname: corresync\notherdescription: Test.\n---\n# Test\n",
		"---\nname: corresync\nname: corresync\ndescription: Test.\n---\n# Test\n",
	} {
		if err := validateSkill([]byte(skill), "corresync"); err == nil {
			t.Fatalf("validateSkill accepted invalid frontmatter:\n%s", skill)
		}
	}
	if err := validateSkill(testSkill, "corresync"); err != nil {
		t.Fatal(err)
	}
}

func TestKiroSteeringStripsCRLFSkillFrontmatter(t *testing.T) {
	t.Parallel()
	crlf := bytes.ReplaceAll(testSkill, []byte("\n"), []byte("\r\n"))
	steering := renderKiroSteering(crlf)
	if bytes.Count(steering, []byte("---\n")) != 2 {
		t.Fatalf("steering retained nested frontmatter:\n%s", steering)
	}
	if bytes.Contains(steering, []byte("name: corresync")) || bytes.Contains(steering, []byte("\r")) {
		t.Fatalf("steering retained source frontmatter or CRLF:\n%s", steering)
	}
}

func assertOutputContains(t *testing.T, outputs []Output, path, needle string) {
	t.Helper()
	for _, output := range outputs {
		if output.Path == path {
			if !bytes.Contains(output.Data, []byte(needle)) {
				t.Fatalf("%s does not contain %q", path, needle)
			}
			return
		}
	}
	t.Fatalf("output %s is missing", path)
}
