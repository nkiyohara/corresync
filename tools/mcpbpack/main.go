// Command mcpbpack turns the six verified Corresync release binaries into one
// deterministic, platform-universal MCP Bundle and adds its SBOMs to the
// GoReleaser inventory and checksum manifest.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

const (
	bundleArtifactID = "mcpb"
	manifestVersion  = "0.4"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type metadata struct {
	Version string `json:"version"`
	Date    string `json:"date"`
}

type artifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Type   string         `json:"type"`
	GOOS   string         `json:"goos,omitempty"`
	GOARCH string         `json:"goarch,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"`
}

type manifest struct {
	ManifestVersion string `json:"manifest_version"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command           string `json:"command"`
			PlatformOverrides map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
}

type bundleEntry struct {
	name string
	path string
	data []byte
	mode os.FileMode
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	source := flag.String("source", "mcpb", "MCPB source directory")
	sbomCommand := flag.String(
		"sbom-command",
		filepath.Join(".release", "syft-reproducible"),
		"reproducible Syft wrapper",
	)
	flag.Parse()

	if err := augmentRelease(*dist, *source, *sbomCommand); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "MCPB packaging failed: %v\n", err)
		os.Exit(1)
	}
}

func augmentRelease(dist, source, sbomCommand string) error {
	release, err := readMetadata(filepath.Join(dist, "metadata.json"))
	if err != nil {
		return err
	}
	artifactsPath := filepath.Join(dist, "artifacts.json")
	artifacts, err := readArtifacts(artifactsPath)
	if err != nil {
		return err
	}
	binaries, err := selectBinaries(dist, artifacts)
	if err != nil {
		return err
	}
	manifestData, err := renderManifest(release.Version)
	if err != nil {
		return err
	}
	when, err := time.Parse(time.RFC3339, release.Date)
	if err != nil {
		return fmt.Errorf("invalid release date %q: %w", release.Date, err)
	}

	bundleName := fmt.Sprintf("corresync_%s.mcpb", release.Version)
	bundlePath := filepath.Join(dist, bundleName)
	if err := buildBundle(bundlePath, ".", source, manifestData, binaries, when.UTC()); err != nil {
		return err
	}
	bundleHash, err := hashFile(bundlePath)
	if err != nil {
		return err
	}
	registryData, err := integrationbundle.RenderRegistryManifest(release.Version, bundleHash)
	if err != nil {
		return fmt.Errorf("render MCP registry manifest: %w", err)
	}
	const registryName = "server.json"
	if err := writeAtomic(filepath.Join(dist, registryName), registryData); err != nil {
		return fmt.Errorf("write MCP registry manifest: %w", err)
	}

	sbomNames, err := createSBOMs(dist, bundleName, release.Version, sbomCommand)
	if err != nil {
		return err
	}
	generatedNames := append([]string{bundleName, registryName}, sbomNames...)
	if err := updateArtifacts(artifactsPath, dist, artifacts, bundleName, registryName, sbomNames); err != nil {
		return err
	}
	if err := updateChecksums(filepath.Join(dist, "checksums.txt"), dist, generatedNames); err != nil {
		return err
	}
	return nil
}

func readMetadata(path string) (metadata, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed file below caller-provided --dist.
	if err != nil {
		return metadata{}, fmt.Errorf("read release metadata: %w", err)
	}
	var release metadata
	if err := json.Unmarshal(data, &release); err != nil {
		return metadata{}, fmt.Errorf("decode release metadata: %w", err)
	}
	if !versionPattern.MatchString(release.Version) {
		return metadata{}, fmt.Errorf("invalid release version %q", release.Version)
	}
	if _, err := time.Parse(time.RFC3339, release.Date); err != nil {
		return metadata{}, fmt.Errorf("invalid release date %q: %w", release.Date, err)
	}
	return release, nil
}

func readArtifacts(path string) ([]artifact, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed file below caller-provided --dist.
	if err != nil {
		return nil, fmt.Errorf("read artifact inventory: %w", err)
	}
	var artifacts []artifact
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, fmt.Errorf("decode artifact inventory: %w", err)
	}
	return artifacts, nil
}

func selectBinaries(dist string, artifacts []artifact) (map[string]string, error) {
	expected := make(map[string]bool)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			expected[goos+"/"+goarch] = false
		}
	}
	binaries := make(map[string]string, len(expected))
	for _, item := range artifacts {
		if item.Type != "Binary" || stringExtra(item.Extra, "ID") != "corr" {
			continue
		}
		target := item.GOOS + "/" + item.GOARCH
		if _, wanted := expected[target]; !wanted {
			continue
		}
		if expected[target] {
			return nil, fmt.Errorf("release inventory has duplicate corr binary for %s", target)
		}
		wantName := "corr"
		if item.GOOS == "windows" {
			wantName = "corr.exe"
		}
		if item.Name != wantName {
			return nil, fmt.Errorf("release binary for %s is named %q, want %q", target, item.Name, wantName)
		}
		path, err := releaseArtifactPath(dist, item.Path)
		if err != nil {
			return nil, fmt.Errorf("release binary for %s: %w", target, err)
		}
		binaries[target] = path
		expected[target] = true
	}
	var missing []string
	for target, found := range expected {
		if !found {
			missing = append(missing, target)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("release inventory is missing corr binaries for %q", missing)
	}
	return binaries, nil
}

func stringExtra(extra map[string]any, key string) string {
	value, _ := extra[key].(string)
	return value
}

func releaseArtifactPath(dist, path string) (string, error) {
	if path == "" {
		return "", errors.New("artifact path is empty")
	}
	absoluteDist, err := filepath.Abs(dist)
	if err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(absoluteDist, absolutePath)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q escapes the dist directory", path)
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("inspect artifact %q: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", filepath.Base(path))
	}
	return absolutePath, nil
}

func renderManifest(version string) ([]byte, error) {
	if !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("invalid manifest version %q", version)
	}
	data, err := integrationbundle.RenderMCPBManifest(version)
	if err != nil {
		return nil, fmt.Errorf("render canonical MCPB manifest: %w", err)
	}
	if err := validateManifest(data, version); err != nil {
		return nil, err
	}
	return data, nil
}

func validateManifest(data []byte, version string) error {
	var document manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode MCPB manifest: %w", err)
	}
	if document.ManifestVersion != manifestVersion ||
		document.Name != "corresync" ||
		document.Version != version {
		return errors.New("MCPB manifest identity does not match the release")
	}
	if document.Server.Type != "binary" ||
		document.Server.EntryPoint != "server/launch.sh" ||
		document.Server.MCPConfig.Command != "${__dirname}/server/launch.sh" {
		return errors.New("MCPB manifest does not use the reviewed local binary launcher")
	}
	windows, ok := document.Server.MCPConfig.PlatformOverrides["win32"]
	if !ok || windows.Command != "cmd.exe" {
		return errors.New("MCPB manifest does not define the reviewed Windows launcher")
	}
	if len(document.Server.MCPConfig.PlatformOverrides) != 1 {
		return errors.New("MCPB manifest has an unexpected platform override")
	}
	wantPlatforms := []string{"darwin", "linux", "win32"}
	if !equalStrings(document.Compatibility.Platforms, wantPlatforms) {
		return fmt.Errorf(
			"MCPB manifest platforms are %q, want %q",
			document.Compatibility.Platforms,
			wantPlatforms,
		)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func buildBundle(
	output string,
	projectRoot string,
	source string,
	manifestData []byte,
	binaries map[string]string,
	when time.Time,
) error {
	entries := []bundleEntry{
		{name: "LICENSE", path: filepath.Join(projectRoot, "LICENSE"), mode: 0o644},
		{name: "README.md", path: filepath.Join(projectRoot, "README.md"), mode: 0o644},
		{name: "SECURITY.md", path: filepath.Join(projectRoot, "SECURITY.md"), mode: 0o644},
		{
			name: "icon.png",
			path: filepath.Join(projectRoot, "site", "icon-512.png"),
			mode: 0o644,
		},
		{name: "manifest.json", data: manifestData, mode: 0o644},
		{
			name: "server/launch.cmd",
			path: filepath.Join(projectRoot, source, "server", "launch.cmd"),
			mode: 0o644,
		},
		{
			name: "server/launch.sh",
			path: filepath.Join(projectRoot, source, "server", "launch.sh"),
			mode: 0o755,
		},
	}
	for target, path := range binaries {
		name := "server/" + target + "/corr"
		if strings.HasPrefix(target, "windows/") {
			name += ".exe"
		}
		entries = append(entries, bundleEntry{name: name, path: path, mode: 0o755})
	}
	licenseEntries, err := collectLicenseEntries(
		filepath.Join(projectRoot, ".release", "third_party_licenses"),
	)
	if err != nil {
		return err
	}
	entries = append(entries, licenseEntries...)
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].name < entries[right].name
	})
	if err := validateEntryNames(entries); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
		return fmt.Errorf("create MCPB output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".corresync-mcpb-*")
	if err != nil {
		return fmt.Errorf("create temporary MCPB: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	archive := zip.NewWriter(temporary)
	for _, entry := range entries {
		if err := writeBundleEntry(archive, entry, when); err != nil {
			_ = archive.Close()
			_ = temporary.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("finish MCPB archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close MCPB archive: %w", err)
	}
	// #nosec G302 -- the completed MCPB is an intentionally public release artifact.
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("set MCPB mode: %w", err)
	}
	if err := os.Rename(temporaryPath, output); err != nil {
		return fmt.Errorf("publish MCPB archive: %w", err)
	}
	return nil
}

func collectLicenseEntries(root string) ([]bundleEntry, error) {
	var entries []bundleEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("third-party license bundle contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("third-party license bundle contains non-regular file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, bundleEntry{
			name: "third_party_licenses/" + filepath.ToSlash(relative),
			path: path,
			mode: 0o644,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect third-party licenses: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("third-party license bundle is empty")
	}
	return entries, nil
}

func validateEntryNames(entries []bundleEntry) error {
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.name == "" ||
			filepath.ToSlash(filepath.Clean(entry.name)) != entry.name ||
			strings.HasPrefix(entry.name, "../") ||
			strings.HasPrefix(entry.name, "/") {
			return fmt.Errorf("unsafe MCPB entry name %q", entry.name)
		}
		if seen[entry.name] {
			return fmt.Errorf("duplicate MCPB entry %q", entry.name)
		}
		seen[entry.name] = true
	}
	return nil
}

func writeBundleEntry(archive *zip.Writer, entry bundleEntry, when time.Time) error {
	header := &zip.FileHeader{
		Name:     entry.name,
		Method:   zip.Deflate,
		Modified: when.UTC(),
	}
	header.SetMode(entry.mode)
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create MCPB entry %q: %w", entry.name, err)
	}
	if entry.data != nil {
		if _, err := writer.Write(entry.data); err != nil {
			return fmt.Errorf("write MCPB entry %q: %w", entry.name, err)
		}
		return nil
	}
	file, err := openRegularFile(entry.path)
	if err != nil {
		return fmt.Errorf("open MCPB input %q: %w", entry.name, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("write MCPB entry %q: %w", entry.name, err)
	}
	return nil
}

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("input is not a regular file")
	}
	// #nosec G304 -- every caller supplies a repository or verified release path.
	return os.Open(path)
}

func createSBOMs(dist, bundleName, version, commandPath string) ([]string, error) {
	absoluteCommand, err := filepath.Abs(commandPath)
	if err != nil {
		return nil, fmt.Errorf("resolve SBOM command: %w", err)
	}
	info, err := os.Stat(absoluteCommand)
	if err != nil {
		return nil, fmt.Errorf("inspect SBOM command: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("SBOM command is not a regular file")
	}
	absoluteDist, err := filepath.Abs(dist)
	if err != nil {
		return nil, fmt.Errorf("resolve dist directory: %w", err)
	}
	scanFile, err := os.CreateTemp(absoluteDist, ".corresync-mcpb-scan-*.zip")
	if err != nil {
		return nil, fmt.Errorf("reserve MCPB scan path: %w", err)
	}
	scanPath := scanFile.Name()
	if err := scanFile.Close(); err != nil {
		return nil, fmt.Errorf("close MCPB scan path: %w", err)
	}
	if err := os.Remove(scanPath); err != nil {
		return nil, fmt.Errorf("prepare MCPB scan path: %w", err)
	}
	if err := os.Link(filepath.Join(absoluteDist, bundleName), scanPath); err != nil {
		return nil, fmt.Errorf("link MCPB for archive scanning: %w", err)
	}
	defer func() { _ = os.Remove(scanPath) }()
	scanName := filepath.Base(scanPath)

	outputs := []struct {
		format string
		name   string
	}{
		{format: "spdx-json", name: bundleName + ".spdx.json"},
		{format: "cyclonedx-json", name: bundleName + ".cdx.json"},
	}
	names := make([]string, 0, len(outputs))
	for _, output := range outputs {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		// #nosec G204,G702 -- executable and basenames are validated local release inputs.
		command := exec.CommandContext(
			ctx,
			absoluteCommand,
			scanName,
			"--source-name",
			bundleName,
			"--source-version",
			version,
			"--output",
			output.format+"="+output.name,
			"--enrich",
			"all",
		)
		command.Dir = absoluteDist
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		err := command.Run()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("generate %s MCPB SBOM: %w", output.format, err)
		}
		names = append(names, output.name)
	}
	return names, nil
}

func updateArtifacts(
	path string,
	dist string,
	artifacts []artifact,
	bundleName string,
	registryName string,
	sbomNames []string,
) error {
	generated := map[string]bool{bundleName: true, registryName: true}
	for _, name := range sbomNames {
		generated[name] = true
	}
	filtered := artifacts[:0]
	for _, item := range artifacts {
		if !generated[item.Name] {
			filtered = append(filtered, item)
		}
	}
	filtered = append(filtered, artifact{
		Name: bundleName,
		Path: filepath.ToSlash(filepath.Join(dist, bundleName)),
		Type: "MCP Bundle",
		Extra: map[string]any{
			"Ext":    ".mcpb",
			"Format": "mcpb",
			"ID":     bundleArtifactID,
		},
	})
	filtered = append(filtered, artifact{
		Name: registryName,
		Path: filepath.ToSlash(filepath.Join(dist, registryName)),
		Type: "Registry Manifest",
		Extra: map[string]any{
			"Format": "json",
			"ID":     "mcp-registry",
		},
	})
	for _, name := range sbomNames {
		filtered = append(filtered, artifact{
			Name: name,
			Path: filepath.ToSlash(filepath.Join(dist, name)),
			Type: "SBOM",
			Extra: map[string]any{
				"ID": bundleArtifactID,
			},
		})
	}
	data, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return fmt.Errorf("encode augmented artifact inventory: %w", err)
	}
	return writeAtomic(path, append(data, '\n'))
}

func updateChecksums(path, dist string, generatedNames []string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed checksum manifest.
	if err != nil {
		return fmt.Errorf("read checksum manifest: %w", err)
	}
	hashes := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("checksum line %d is malformed", lineNumber+1)
		}
		name := fields[1]
		if filepath.Base(name) != name {
			return fmt.Errorf("checksum path %q is not a basename", name)
		}
		if _, exists := hashes[name]; exists {
			return fmt.Errorf("duplicate checksum for %q", name)
		}
		hashes[name] = strings.ToLower(fields[0])
	}
	for _, name := range generatedNames {
		hash, err := hashFile(filepath.Join(dist, name))
		if err != nil {
			return err
		}
		hashes[name] = hash
	}
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, name := range names {
		_, _ = fmt.Fprintf(&manifest, "%s  %s\n", hashes[name], name)
	}
	return writeAtomic(path, []byte(manifest.String()))
}

func hashFile(path string) (string, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("open %q for hashing: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeAtomic(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary %q: %w", filepath.Base(path), err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary %q: %w", filepath.Base(path), err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary %q: %w", filepath.Base(path), err)
	}
	// #nosec G302 -- the completed inventory is intentionally public release metadata.
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return fmt.Errorf("set mode on temporary %q: %w", filepath.Base(path), err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", filepath.Base(path), err)
	}
	return nil
}
