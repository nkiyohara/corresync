package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRenderManifestBindsReleaseAndLocalLauncher(t *testing.T) {
	t.Parallel()

	data, err := renderManifest("1.2.3-rc.1")
	if err != nil {
		t.Fatalf("renderManifest() error = %v", err)
	}
	text := string(data)
	for _, required := range []string{
		`"manifest_version": "0.4"`,
		`"version": "1.2.3-rc.1"`,
		`"type": "binary"`,
		`"${__dirname}/server/launch.sh"`,
		`"https://corresync.org/privacy.html"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("rendered manifest is missing %q", required)
		}
	}
}

func TestSelectBinariesRequiresOnePrimaryBinaryPerTarget(t *testing.T) {
	t.Parallel()

	dist := t.TempDir()
	artifacts := make([]artifact, 0, 6)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			name := "corr"
			if goos == "windows" {
				name = "corr.exe"
			}
			path := filepath.Join(dist, goos+"-"+goarch, name)
			writeTestFile(t, path, []byte(goos+"/"+goarch), 0o755)
			artifacts = append(artifacts, artifact{
				Name:   name,
				Path:   path,
				Type:   "Binary",
				GOOS:   goos,
				GOARCH: goarch,
				Extra:  map[string]any{"ID": "corr"},
			})
		}
	}
	binaries, err := selectBinaries(dist, artifacts)
	if err != nil {
		t.Fatalf("selectBinaries() error = %v", err)
	}
	if len(binaries) != 6 {
		t.Fatalf("selectBinaries() count = %d, want 6", len(binaries))
	}

	if _, err := selectBinaries(dist, artifacts[:5]); err == nil {
		t.Fatal("selectBinaries() accepted an incomplete target matrix")
	}
}

func TestBuildBundleIsDeterministicAndComplete(t *testing.T) {
	project := t.TempDir()
	source := "mcpb"
	writeTestFile(t, filepath.Join(project, "LICENSE"), []byte("license\n"), 0o644)
	writeTestFile(t, filepath.Join(project, "README.md"), []byte("readme\n"), 0o644)
	writeTestFile(t, filepath.Join(project, "SECURITY.md"), []byte("security\n"), 0o644)
	writeTestFile(t, filepath.Join(project, "site", "icon-512.png"), []byte("png"), 0o644)
	writeTestFile(
		t,
		filepath.Join(project, source, "server", "launch.sh"),
		[]byte("#!/bin/sh\n"),
		0o755,
	)
	writeTestFile(
		t,
		filepath.Join(project, source, "server", "launch.cmd"),
		[]byte("@echo off\r\n"),
		0o644,
	)
	writeTestFile(
		t,
		filepath.Join(project, ".release", "third_party_licenses", "example", "LICENSE"),
		[]byte("dependency\n"),
		0o644,
	)

	binaries := make(map[string]string)
	for _, target := range []string{
		"darwin/amd64",
		"darwin/arm64",
		"linux/amd64",
		"linux/arm64",
		"windows/amd64",
		"windows/arm64",
	} {
		name := "corr"
		if strings.HasPrefix(target, "windows/") {
			name = "corr.exe"
		}
		path := filepath.Join(project, "dist", target, name)
		writeTestFile(t, path, []byte(target), 0o755)
		binaries[target] = path
	}

	when := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	first := filepath.Join(project, "first.mcpb")
	second := filepath.Join(project, "second.mcpb")
	manifestData := []byte(`{"manifest_version":"0.4"}`)
	if err := buildBundle(first, project, source, manifestData, binaries, when); err != nil {
		t.Fatalf("buildBundle(first) error = %v", err)
	}
	if err := buildBundle(second, project, source, manifestData, binaries, when); err != nil {
		t.Fatalf("buildBundle(second) error = %v", err)
	}
	if hashTestFile(t, first) != hashTestFile(t, second) {
		t.Fatal("buildBundle() output is not deterministic")
	}

	reader, err := zip.OpenReader(first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	names := make([]string, 0, len(reader.File))
	modes := make(map[string]os.FileMode)
	for _, file := range reader.File {
		names = append(names, file.Name)
		modes[file.Name] = file.Mode().Perm()
		if !file.Modified.Equal(when) {
			t.Fatalf("entry %q timestamp = %s, want %s", file.Name, file.Modified, when)
		}
	}
	sort.Strings(names)
	want := []string{
		"LICENSE",
		"README.md",
		"SECURITY.md",
		"icon.png",
		"manifest.json",
		"server/darwin/amd64/corr",
		"server/darwin/arm64/corr",
		"server/launch.cmd",
		"server/launch.sh",
		"server/linux/amd64/corr",
		"server/linux/arm64/corr",
		"server/windows/amd64/corr.exe",
		"server/windows/arm64/corr.exe",
		"third_party_licenses/example/LICENSE",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("bundle entries = %q, want %q", names, want)
	}
	if modes["server/launch.sh"] != 0o755 ||
		modes["server/darwin/amd64/corr"] != 0o755 ||
		modes["manifest.json"] != 0o644 {
		t.Fatalf("bundle modes = %#v", modes)
	}
}

func TestShellLauncherSelectsCurrentTargetAndFixedMCPArgs(t *testing.T) {
	t.Parallel()

	platform := runtime.GOOS
	if platform != "darwin" && platform != "linux" {
		t.Skip("POSIX launcher is used only on macOS and Linux")
	}
	architecture := runtime.GOARCH
	if architecture != "amd64" && architecture != "arm64" {
		t.Skip("test host architecture is not in the release matrix")
	}

	root := t.TempDir()
	launcherData, err := os.ReadFile( // #nosec G304 -- repository-owned test input.
		filepath.Join("..", "..", "mcpb", "server", "launch.sh"),
	)
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(root, "server", "launch.sh")
	writeTestFile(t, launcher, launcherData, 0o755)
	selected := filepath.Join(root, "server", platform, architecture, "corr")
	writeTestFile(t, selected, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- the executable is a repository launcher copied into a test-owned directory.
	command := exec.CommandContext(ctx, launcher)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher error = %v, output = %s", err, output)
	}
	if string(output) != "mcp serve\n" {
		t.Fatalf("launcher output = %q, want fixed MCP arguments", output)
	}
}

func TestWindowsLauncherHasOnlyReviewedArchitecturesAndArguments(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile( // #nosec G304 -- repository-owned test input.
		filepath.Join("..", "..", "mcpb", "server", "launch.cmd"),
	)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		`"%corresync_arch%"=="AMD64"`,
		`"%corresync_arch%"=="ARM64"`,
		`"%~dp0windows\%corresync_arch%\corr.exe" mcp serve`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Windows launcher is missing %q", required)
		}
	}
	if strings.Contains(text, "%*") {
		t.Fatal("Windows launcher forwards unreviewed arguments")
	}
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	// #nosec G306,G703 -- every caller supplies a path below a test-owned temporary directory.
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func hashTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
