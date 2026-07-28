package main

import (
	"slices"
	"strings"
	"testing"
)

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

func TestPackageInventoryRequiresPublicChangelog(t *testing.T) {
	t.Parallel()

	destinations := []string{
		"/usr/bin/corresync",
		"/usr/share/bash-completion/completions/corresync",
		"/usr/share/zsh/site-functions/_corresync",
		"/usr/share/fish/vendor_completions.d/corresync.fish",
		"/usr/share/man/man1/corresync.1",
		"/usr/share/doc/corresync/CHANGELOG.md",
		"/usr/share/doc/corresync/third_party_licenses",
		"/usr/share/corresync/plugins/corresync",
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

	withoutChangelog := append([]any(nil), files[:5]...)
	withoutChangelog = append(withoutChangelog, files[6:]...)
	missing := packageMissingFiles(map[string]any{"Files": withoutChangelog})
	if !slices.Contains(missing, "/usr/share/doc/corresync/CHANGELOG.md") {
		t.Fatalf("package inventory missing = %v, want changelog", missing)
	}
}

func TestArchiveInventoryAcceptsChangelogAndRejectsExtras(t *testing.T) {
	t.Parallel()

	want := []string{"CHANGELOG.md", "LICENSE", "README.md"}
	got := append([]string(nil), want...)
	for range minimumLicenses {
		got = append(got, licensePrefix+"example.invalid/dependency/LICENSE")
	}
	if err := requireReleaseFiles("synthetic.zip", got, want); err != nil {
		t.Fatalf("requireReleaseFiles() error = %v", err)
	}
	if err := requireReleaseFiles("synthetic.zip", append(got, "unexpected.txt"), want); err == nil {
		t.Fatal("requireReleaseFiles() accepted an unexpected file")
	}
}
