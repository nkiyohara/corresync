package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGeneratesAndChecksEveryLocale(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := run(root, false); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err != nil {
		t.Fatal(err)
	}
	for _, locale := range copies() {
		path := filepath.Join(root, locale.Prefix, "integrations.html")
		data, err := os.ReadFile(path) // #nosec G304 -- fixed generated path under a test-owned directory.
		if err != nil {
			t.Fatal(err)
		}
		page := string(data)
		for _, expected := range []string{locale.Title, `lang="` + locale.Language + `"`, "0.8.6", "0.9.0-rc.1"} {
			if !strings.Contains(page, expected) {
				t.Fatalf("%s does not contain %q", path, expected)
			}
		}
	}
	path := filepath.Join(root, "integrations.html")
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(root, true); err == nil {
		t.Fatal("check accepted a stale page")
	}
}

func TestEveryLocaleCoversCanonicalVocabulary(t *testing.T) {
	t.Parallel()
	for _, locale := range copies() {
		for _, key := range []string{"archives-and-mcpb", "mcpb", "plugin", "shared-plugin", "extension", "power", "config-only"} {
			if locale.Packages[key] == "" {
				t.Fatalf("%s is missing package %s", locale.Language, key)
			}
		}
		for _, key := range []string{"local-cli", "local-desktop", "local-ide"} {
			if locale.Surfaces[key] == "" {
				t.Fatalf("%s is missing surface %s", locale.Language, key)
			}
		}
		for _, key := range []string{"published", "source-available", "not-listed"} {
			if locale.States[key] == "" {
				t.Fatalf("%s is missing state %s", locale.Language, key)
			}
		}
	}
}
