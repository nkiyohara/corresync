package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestVerifyIntegrationVersions(t *testing.T) {
	t.Parallel()
	const version = "1.2.3-rc.4"
	versionedJSON := func(extra map[string]any) []byte {
		document := map[string]any{"version": version}
		for key, value := range extra {
			document[key] = value
		}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	files := map[string][]byte{
		".claude-plugin/marketplace.json": versionedJSON(map[string]any{
			"plugins": []any{map[string]any{"version": version}},
		}),
		"integrations/config-hosts.json":                          versionedJSON(nil),
		"integrations/gemini-cli/corresync/gemini-extension.json": versionedJSON(nil),
		"plugins/corresync/.claude-plugin/plugin.json":            versionedJSON(nil),
		"plugins/corresync/.codex-plugin/plugin.json":             versionedJSON(nil),
		"docs/generated/integration-bundles.md":                   []byte("Canonical source snapshot:\n\n`" + version + "`."),
		"docs/generated/publication-channels.md":                  []byte("Canonical source snapshot: `" + version + "`."),
		"integrations/kiro/corresync/POWER.md":                    []byte("Version: " + version),
	}
	if err := verifyIntegrationVersions(files, version); err != nil {
		t.Fatal(err)
	}
	files["plugins/corresync/.codex-plugin/plugin.json"] = versionedJSON(map[string]any{"version": "9.9.9"})
	if err := verifyIntegrationVersions(files, version); err == nil {
		t.Fatal("version verifier accepted a mismatched plugin")
	}
}

func TestValidateGitHubAssetName(t *testing.T) {
	t.Parallel()

	if err := validateGitHubAssetName("corresync_0.1.0-rc.2_amd64.deb"); err != nil {
		t.Fatalf("validateGitHubAssetName() rejected a safe name: %v", err)
	}
	err := validateGitHubAssetName("corresync_0.1.0~rc.2_amd64.deb")
	if err == nil || !strings.Contains(err.Error(), "GitHub rewrites") {
		t.Fatalf("validateGitHubAssetName() error = %v, want GitHub rewrite warning", err)
	}
}

func TestVerifyCompatibilityHashesRequiresIdenticalExecutables(t *testing.T) {
	t.Parallel()

	if err := verifyCompatibilityHashes(
		map[string]string{"corr": "same", "corresync": "same"},
		"corr",
		"corresync",
	); err != nil {
		t.Fatalf("verifyCompatibilityHashes() rejected identical entries: %v", err)
	}
	for name, hashes := range map[string]map[string]string{
		"missing":    {"corr": "same"},
		"mismatched": {"corr": "primary", "corresync": "compatibility"},
	} {
		if err := verifyCompatibilityHashes(hashes, "corr", "corresync"); err == nil {
			t.Fatalf("verifyCompatibilityHashes() accepted %s entries", name)
		}
	}
}

func TestPackageInventoryRequiresPublicChangelog(t *testing.T) {
	t.Parallel()

	destinations := []string{
		"/usr/bin/corr",
		"/usr/bin/corresync",
		"/usr/share/bash-completion/completions/corr",
		"/usr/share/zsh/site-functions/_corr",
		"/usr/share/fish/vendor_completions.d/corr.fish",
		"/usr/share/man/man1/corr.1",
		"/usr/share/doc/corresync/CHANGELOG.md",
		"/usr/share/doc/corresync/third_party_licenses",
		"/usr/share/corresync/plugins/corresync",
		"/usr/share/corresync/integrations",
		"/usr/share/corresync/.agents/plugins/marketplace.json",
		"/usr/share/corresync/.claude-plugin/marketplace.json",
	}
	files := make([]any, 0, len(destinations))
	for _, destination := range destinations {
		files = append(files, map[string]any{"dst": destination})
	}
	if missing := packageMissingFiles(map[string]any{"Files": files}); len(missing) != 0 {
		t.Fatalf("complete package inventory missing = %v", missing)
	}

	withoutChangelog := append([]any(nil), files[:6]...)
	withoutChangelog = append(withoutChangelog, files[7:]...)
	missing := packageMissingFiles(map[string]any{"Files": withoutChangelog})
	if !slices.Contains(missing, "/usr/share/doc/corresync/CHANGELOG.md") {
		t.Fatalf("package inventory missing = %v, want changelog", missing)
	}
}

func TestArchiveInventoryAcceptsChangelogAndRejectsExtras(t *testing.T) {
	t.Parallel()

	want := []string{"CHANGELOG.md", "LICENSE", "README.md"}
	got := append([]string(nil), want...)
	for i := range minimumLicenses {
		got = append(got, fmt.Sprintf("%sexample.invalid/dependency-%d/LICENSE", licensePrefix, i))
	}
	if err := requireReleaseFiles("synthetic.zip", got, want); err != nil {
		t.Fatalf("requireReleaseFiles() error = %v", err)
	}
	if err := requireReleaseFiles("synthetic.zip", append(got, "unexpected.txt"), want); err == nil {
		t.Fatal("requireReleaseFiles() accepted an unexpected file")
	}
}

func TestMCPBManifestRequiresLocalLaunchersAndNoUserConfig(t *testing.T) {
	t.Parallel()

	document := `{
  "manifest_version": "0.4",
  "name": "corresync",
  "version": "1.2.3",
  "tools_generated": true,
  "privacy_policies": ["https://corresync.org/privacy.html"],
  "server": {
    "type": "binary",
    "entry_point": "server/launch.sh",
    "mcp_config": {
      "command": "${__dirname}/server/launch.sh",
      "args": [],
      "env": {},
      "platform_overrides": {
        "win32": {
          "command": "cmd.exe",
          "args": ["/d", "/s", "/c", "\"${__dirname}/server/launch.cmd\""]
        }
      }
    }
  },
  "compatibility": {"platforms": ["darwin", "linux", "win32"]}
}`
	if err := verifyMCPBManifest([]byte(document), "1.2.3"); err != nil {
		t.Fatalf("verifyMCPBManifest() error = %v", err)
	}

	withConfig := strings.Replace(
		document,
		`"tools_generated": true,`,
		`"tools_generated": true, "user_config": {},`,
		1,
	)
	if err := verifyMCPBManifest([]byte(withConfig), "1.2.3"); err == nil {
		t.Fatal("verifyMCPBManifest() accepted user configuration")
	}

	remote := strings.Replace(
		document,
		`"${__dirname}/server/launch.sh"`,
		fmt.Sprintf("%q", "https://example.invalid/mcp"),
		1,
	)
	if err := verifyMCPBManifest([]byte(remote), "1.2.3"); err == nil {
		t.Fatal("verifyMCPBManifest() accepted a remote launcher")
	}
}

func TestReleaseArchivesRejectUnsafeEntries(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"tar", "zip"} {
		for _, invalid := range []struct {
			name      string
			mode      os.FileMode
			duplicate bool
		}{
			{name: "third_party_licenses/../../escaped", mode: 0o644},
			{name: "/absolute", mode: 0o644},
			{name: `third_party_licenses/..\escaped`, mode: 0o644},
			{name: "third_party_licenses/C:escaped", mode: 0o644},
			{name: "third_party_licenses/link", mode: os.ModeSymlink | 0o777},
			{name: "corr", mode: os.ModeSymlink | 0o777},
			{name: "corr", mode: os.ModeDir | 0o755},
			{name: "corr", mode: 0o644},
			{name: "third_party_licenses/duplicate", mode: 0o644, duplicate: true},
		} {
			if format == "zip" && invalid.name == "corr" {
				if invalid.mode == 0o644 {
					continue
				} // Windows does not use Unix executable mode bits.
				invalid.name += ".exe"
			}
			t.Run(format+"/"+invalid.name+"/"+invalid.mode.String(), func(t *testing.T) {
				path, want := writeReleaseArchiveFixture(t, format, func(entries []releaseTestEntry) []releaseTestEntry {
					// Change both command entries so hash equality cannot hide a type/mode bug.
					if invalid.name == "corr" || invalid.name == "corr.exe" {
						for i := range entries {
							if strings.HasPrefix(entries[i].name, "corr") {
								entries[i].mode = invalid.mode
							}
						}
						return entries
					}
					entry := releaseTestEntry{name: invalid.name, mode: invalid.mode, content: "synthetic"}
					entries = append(entries, entry)
					if invalid.duplicate {
						entries = append(entries, entry)
					}
					return entries
				})
				var err error
				if format == "zip" {
					err = verifyZip(path, want, "1.2.3")
				} else {
					err = verifyTarGzip(path, want, "1.2.3")
				}
				if err == nil {
					t.Fatal("unsafe release archive was accepted")
				}
			})
		}
	}
}

func TestReleaseArchivesAcceptRegularExecutablesAndDistinctLicenses(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"tar", "zip"} {
		t.Run(format, func(t *testing.T) {
			path, want := writeReleaseArchiveFixture(t, format, nil)
			var err error
			if format == "zip" {
				err = verifyZip(path, want, "1.2.3")
			} else {
				err = verifyTarGzip(path, want, "1.2.3")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type releaseTestEntry struct {
	typeflag byte
	name     string
	mode     os.FileMode
	content  string
}

func writeReleaseArchiveFixture(t *testing.T, format string, mutate func([]releaseTestEntry) []releaseTestEntry) (string, []string) {
	t.Helper()
	entries := []releaseTestEntry{
		{name: ".claude-plugin/marketplace.json", mode: 0o644, content: `{"version":"1.2.3","plugins":[{"version":"1.2.3"}]}`},
		{name: "docs/generated/integration-bundles.md", mode: 0o644, content: "Canonical source snapshot:\n\n`1.2.3`."},
		{name: "docs/generated/publication-channels.md", mode: 0o644, content: "Canonical source snapshot: `1.2.3`."},
		{name: "integrations/kiro/corresync/POWER.md", mode: 0o644, content: "Version: 1.2.3"},
	}
	for _, name := range []string{"integrations/config-hosts.json", "integrations/gemini-cli/corresync/gemini-extension.json", "plugins/corresync/.claude-plugin/plugin.json", "plugins/corresync/.codex-plugin/plugin.json"} {
		entries = append(entries, releaseTestEntry{name: name, mode: 0o644, content: `{"version":"1.2.3"}`})
	}
	for _, name := range []string{"corr", "corresync"} {
		if format == "zip" {
			name += ".exe"
		}
		entries = append(entries, releaseTestEntry{name: name, mode: 0o755, content: "synthetic identical executable"})
	}
	want := make([]string, 0, len(entries))
	for _, entry := range entries {
		want = append(want, entry.name)
	}
	for i := range minimumLicenses {
		entries = append(entries, releaseTestEntry{name: fmt.Sprintf("%sexample.invalid/dep-%d/LICENSE", licensePrefix, i), mode: 0o644, content: "synthetic license"})
	}
	if mutate != nil {
		entries = mutate(entries)
	}
	path := filepath.Join(t.TempDir(), "fixture."+format)
	out, err := os.Create(path) // #nosec G304 -- Synthetic archive path under the test's private temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if format == "zip" {
		writer := zip.NewWriter(out)
		for _, entry := range entries {
			header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
			header.SetMode(entry.mode)
			sink, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sink.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		compressed := gzip.NewWriter(out)
		writer := tar.NewWriter(compressed)
		for _, entry := range entries {
			header := &tar.Header{Name: entry.name, Mode: int64(entry.mode.Perm()), Typeflag: tar.TypeReg, Size: int64(len(entry.content))}
			if entry.typeflag != 0 {
				header.Typeflag = entry.typeflag
				header.Linkname = "/nonexistent"
				header.Size = 0
			}
			if entry.mode&os.ModeSymlink != 0 {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/nonexistent"
				header.Size = 0
			}
			if entry.mode.IsDir() {
				header.Typeflag = tar.TypeDir
				header.Size = 0
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Typeflag == tar.TypeReg {
				if _, err := writer.Write([]byte(entry.content)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path, want
}

func TestReleaseTarRejectsHardlinkedBinary(t *testing.T) {
	path, want := writeReleaseArchiveFixture(t, "tar", func(entries []releaseTestEntry) []releaseTestEntry {
		for i := range entries {
			if entries[i].name == "corr" || entries[i].name == "corresync" {
				entries[i].typeflag = tar.TypeLink
			}
		}
		return entries
	})
	if err := verifyTarGzip(path, want, "1.2.3"); err == nil {
		t.Fatal("hard-linked executables were accepted")
	}
}

func TestReleaseArchivesRejectEmptyBinary(t *testing.T) {
	for _, format := range []string{"tar", "zip"} {
		path, want := writeReleaseArchiveFixture(t, format, func(entries []releaseTestEntry) []releaseTestEntry {
			for i := range entries {
				if strings.HasPrefix(entries[i].name, "corr") {
					entries[i].content = ""
				}
			}
			return entries
		})
		var err error
		if format == "zip" {
			err = verifyZip(path, want, "1.2.3")
		} else {
			err = verifyTarGzip(path, want, "1.2.3")
		}
		if err == nil {
			t.Fatalf("%s accepted empty executables", format)
		}
	}
}
