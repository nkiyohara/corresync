package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

func TestRunGenerateAndCheck(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixture(t, root, integrationbundle.SkillPath, []byte("---\nname: corresync\ndescription: Test.\n---\n\n# Test\n"))
	writeFixture(t, root, integrationbundle.IconPath, []byte("<svg/>\n"))
	writeRegistryFixture(t, root)
	if err := run(root, root, "1.2.3-rc.1", false, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, root, "1.2.3-rc.1", true, false); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "plugins", "corresync", ".mcp.json")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(root, root, "1.2.3-rc.1", true, false); err == nil {
		t.Fatal("check succeeded with drift")
	}
}

func TestRunReleaseTreeCopiesCanonicalAssets(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	output := t.TempDir()
	fixtures := map[string][]byte{
		integrationbundle.SkillPath:                             []byte("---\nname: corresync\ndescription: Test.\n---\n\n# Test\n"),
		integrationbundle.IconPath:                              []byte("<svg/>\n"),
		"plugins/corresync/README.md":                           []byte("# Plugin\n"),
		"plugins/corresync/skills/corresync/agents/openai.yaml": []byte("interface: {}\n"),
	}
	for path, data := range fixtures {
		writeFixture(t, source, path, data)
	}
	writeRegistryFixture(t, source)
	if err := run(source, output, "2.3.4", false, false); err != nil {
		t.Fatal(err)
	}
	for path, want := range fixtures {
		got, err := os.ReadFile( // #nosec G304 -- path is from the fixed test fixture map under a test-owned directory.
			filepath.Join(output, filepath.FromSlash(path)),
		)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("copied %s differs", path)
		}
	}
	manifest, err := os.ReadFile( // #nosec G304 -- fixed path under a test-owned directory.
		filepath.Join(output, "plugins", "corresync", ".codex-plugin", "plugin.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"version": "2.3.4"`) {
		t.Fatal("release manifest does not use requested version")
	}
}

func TestCleanOutputIsRestrictedToReleaseStaging(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	staging := filepath.Join(root, ".release", "integrationbundle")
	writeFixture(t, root, ".release/integrationbundle/stale.txt", []byte("stale\n"))
	if err := cleanReleaseOutput(root, staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging still exists or returned unexpected error: %v", err)
	}
	for _, unsafe := range []string{root, filepath.Join(root, ".release"), t.TempDir()} {
		if err := cleanReleaseOutput(root, unsafe); err == nil {
			t.Fatalf("cleanReleaseOutput accepted %q", unsafe)
		}
	}
	symlinkRoot := t.TempDir()
	symlink := filepath.Join(symlinkRoot, ".release")
	if err := os.Symlink(t.TempDir(), symlink); err != nil {
		t.Fatal(err)
	}
	if err := cleanReleaseOutput(symlinkRoot, filepath.Join(symlink, "integrationbundle")); err == nil {
		t.Fatal("cleanReleaseOutput accepted a symlinked .release root")
	}
}

func writeRegistryFixture(t *testing.T, root string) {
	t.Helper()
	data, err := integrationbundle.RenderRegistryManifest("1.2.3", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "server.json", data)
}

func TestOutputPathRejectsEscapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{"", "../escape", "/absolute", "a/../escape"} {
		if _, err := outputPath(root, path); err == nil {
			t.Fatalf("outputPath(%q) succeeded", path)
		}
	}
}

func writeFixture(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
