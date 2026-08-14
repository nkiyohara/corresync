// Command releaseverify validates the complete local GoReleaser output.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

const (
	expectedArchives   = 6
	expectedBinaries   = 12
	expectedMCPBundles = 1
	expectedRegistries = 1
	expectedPackages   = 6
	expectedSBOMs      = 28
	expectedSources    = 1
	licensePrefix      = "third_party_licenses/"
	minimumLicenses    = 24
)

var mcpbNamePattern = regexp.MustCompile(
	`^corresync_([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)\.mcpb$`,
)

var archiveNamePattern = regexp.MustCompile(
	`^corresync_([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)_(?:darwin|linux|windows)_(?:amd64|arm64)\.(?:tar\.gz|zip)$`,
)

type artifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Type   string         `json:"type"`
	GOOS   string         `json:"goos"`
	GOARCH string         `json:"goarch"`
	Extra  map[string]any `json:"extra"`
}

type mcpbManifest struct {
	ManifestVersion string   `json:"manifest_version"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	ToolsGenerated  bool     `json:"tools_generated"`
	PrivacyPolicies []string `json:"privacy_policies"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command           string            `json:"command"`
			Args              []string          `json:"args"`
			Env               map[string]string `json:"env"`
			PlatformOverrides map[string]struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"platform_overrides"`
		} `json:"mcp_config"`
	} `json:"server"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
}

type registryManifest struct {
	Version  string `json:"version"`
	Packages []struct {
		FileSHA256 string `json:"fileSha256"`
	} `json:"packages"`
}

type scoopManifest struct {
	Architecture map[string]struct {
		URL  string   `json:"url"`
		Bin  []string `json:"bin"`
		Hash string   `json:"hash"`
	} `json:"architecture"`
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	flag.Parse()

	if err := verify(*dist); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "release verification failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"release verification passed: %d archives, %d MCP bundles, %d packages, %d SBOMs\n",
		expectedArchives,
		expectedMCPBundles,
		expectedPackages,
		expectedSBOMs,
	)
}

func verify(dist string) error {
	if err := verifyLicenseBundle(filepath.Join(".release", "third_party_licenses")); err != nil {
		return err
	}
	artifacts, err := readArtifacts(filepath.Join(dist, "artifacts.json"))
	if err != nil {
		return err
	}
	hashes, err := verifyChecksums(dist)
	if err != nil {
		return err
	}
	if err := verifyInventory(dist, artifacts, hashes); err != nil {
		return err
	}
	if err := verifyCatalogs(dist, hashes); err != nil {
		return err
	}
	return nil
}

func readArtifacts(path string) ([]artifact, error) {
	data, err := readLocalFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact inventory: %w", err)
	}
	var artifacts []artifact
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, fmt.Errorf("decode artifact inventory: %w", err)
	}
	return artifacts, nil
}

func verifyChecksums(dist string) (map[string]string, error) {
	manifest, err := readLocalFile(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	hashes := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("checksum line %d is malformed", lineNumber+1)
		}
		want, name := strings.ToLower(fields[0]), fields[1]
		if len(want) != sha256.Size*2 {
			return nil, fmt.Errorf("checksum for %q is not SHA-256", name)
		}
		if _, err := hex.DecodeString(want); err != nil {
			return nil, fmt.Errorf("checksum for %q is not hexadecimal: %w", name, err)
		}
		if filepath.Base(name) != name {
			return nil, fmt.Errorf("checksum path %q is not a release basename", name)
		}
		if err := validateGitHubAssetName(name); err != nil {
			return nil, err
		}
		got, err := hashFile(filepath.Join(dist, name))
		if err != nil {
			return nil, err
		}
		if got != want {
			return nil, fmt.Errorf("checksum mismatch for %q", name)
		}
		if _, exists := hashes[name]; exists {
			return nil, fmt.Errorf("duplicate checksum for %q", name)
		}
		hashes[name] = want
	}
	expectedChecksums := expectedArchives +
		expectedMCPBundles +
		expectedRegistries +
		expectedPackages +
		expectedSBOMs +
		expectedSources
	if len(hashes) != expectedChecksums {
		return nil, fmt.Errorf("checksum count is %d, want %d", len(hashes), expectedChecksums)
	}
	return hashes, nil
}

func validateGitHubAssetName(name string) error {
	if strings.Contains(name, "~") {
		return fmt.Errorf(
			"release asset %q contains '~', which GitHub rewrites and would invalidate checksums",
			name,
		)
	}
	return nil
}

func verifyInventory(dist string, artifacts []artifact, hashes map[string]string) error {
	counts := make(map[string]int)
	targets := make(map[string]bool)
	binaries := make(map[string]map[string]int)
	packageFormats := make(map[string]int)
	sbomFormats := make(map[string]int)
	bundleBinaries, err := expectedMCPBBinaries(dist, artifacts)
	if err != nil {
		return err
	}
	for _, item := range artifacts {
		counts[item.Type]++
		switch item.Type {
		case "Binary":
			target := item.GOOS + "/" + item.GOARCH
			if binaries[target] == nil {
				binaries[target] = make(map[string]int)
			}
			binaries[target][item.Name]++
		case "Archive":
			if filepath.Base(item.Name) != item.Name {
				return fmt.Errorf("archive name %q is not a basename", item.Name)
			}
			targets[item.GOOS+"/"+item.GOARCH] = true
			if hashes[item.Name] == "" {
				return fmt.Errorf("archive %q is absent from checksums", item.Name)
			}
			if err := verifyArchive(filepath.Join(dist, item.Name), item.GOOS); err != nil {
				return err
			}
		case "MCP Bundle":
			if filepath.Base(item.Name) != item.Name {
				return fmt.Errorf("MCP bundle name %q is not a basename", item.Name)
			}
			if hashes[item.Name] == "" {
				return fmt.Errorf("MCP bundle %q is absent from checksums", item.Name)
			}
			if err := verifyMCPBundle(
				filepath.Join(dist, item.Name),
				bundleBinaries,
			); err != nil {
				return err
			}
		case "Registry Manifest":
			if item.Name != "server.json" {
				return fmt.Errorf("registry manifest name is %q, want server.json", item.Name)
			}
			if hashes[item.Name] == "" {
				return fmt.Errorf("registry manifest %q is absent from checksums", item.Name)
			}
			if err := verifyRegistryManifest(filepath.Join(dist, item.Name), hashes); err != nil {
				return err
			}
		case "Linux Package":
			extension := filepath.Ext(item.Name)
			packageFormats[extension]++
			if hashes[item.Name] == "" {
				return fmt.Errorf("package %q is absent from checksums", item.Name)
			}
			if missing := packageMissingFiles(item.Extra); len(missing) > 0 {
				return fmt.Errorf("package %q does not declare required files %q", item.Name, missing)
			}
		case "SBOM":
			if hashes[item.Name] == "" {
				return fmt.Errorf("SBOM %q is absent from checksums", item.Name)
			}
			format, err := verifySBOM(filepath.Join(dist, item.Name))
			if err != nil {
				return err
			}
			sbomFormats[format]++
		case "Source":
			if hashes[item.Name] == "" {
				return fmt.Errorf("source archive %q is absent from checksums", item.Name)
			}
			if err := verifySourceArchive(filepath.Join(dist, item.Name)); err != nil {
				return err
			}
		}
	}

	wantCounts := map[string]int{
		"Archive":           expectedArchives,
		"Binary":            expectedBinaries,
		"Checksum":          1,
		"Linux Package":     expectedPackages,
		"MCP Bundle":        expectedMCPBundles,
		"Metadata":          1,
		"Registry Manifest": expectedRegistries,
		"SBOM":              expectedSBOMs,
		"Source":            expectedSources,
	}
	for kind, want := range wantCounts {
		if counts[kind] != want {
			return fmt.Errorf("%s count is %d, want %d", kind, counts[kind], want)
		}
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			target := goos + "/" + goarch
			if !targets[target] {
				return fmt.Errorf("release target %s is missing", target)
			}
			wantNames := []string{"corr", "corresync"}
			if goos == "windows" {
				wantNames = []string{"corr.exe", "corresync.exe"}
			}
			if len(binaries[target]) != len(wantNames) {
				return fmt.Errorf("release target %s has binaries %#v", target, binaries[target])
			}
			for _, name := range wantNames {
				if binaries[target][name] != 1 {
					return fmt.Errorf(
						"release target %s has %d copies of %s, want one",
						target,
						binaries[target][name],
						name,
					)
				}
			}
		}
	}
	for _, extension := range []string{".apk", ".deb", ".rpm"} {
		if packageFormats[extension] != 2 {
			return fmt.Errorf("%s package count is %d, want 2", extension, packageFormats[extension])
		}
	}
	expectedPerSBOMFormat := expectedSBOMs / 2
	if sbomFormats["CycloneDX"] != expectedPerSBOMFormat ||
		sbomFormats["SPDX"] != expectedPerSBOMFormat {
		return fmt.Errorf(
			"SBOM formats are %#v, want %d CycloneDX and %d SPDX",
			sbomFormats,
			expectedPerSBOMFormat,
			expectedPerSBOMFormat,
		)
	}
	return nil
}

func expectedMCPBBinaries(dist string, artifacts []artifact) (map[string]string, error) {
	expected := make(map[string]bool)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			expected[goos+"/"+goarch] = false
		}
	}
	hashes := make(map[string]string, len(expected))
	for _, item := range artifacts {
		id, _ := item.Extra["ID"].(string)
		if item.Type != "Binary" || id != "corr" {
			continue
		}
		target := item.GOOS + "/" + item.GOARCH
		if _, wanted := expected[target]; !wanted {
			continue
		}
		if expected[target] {
			return nil, fmt.Errorf("duplicate primary binary for MCPB target %s", target)
		}
		path, err := releaseArtifactPath(dist, item.Path)
		if err != nil {
			return nil, fmt.Errorf("resolve MCPB target %s: %w", target, err)
		}
		hash, err := hashFile(path)
		if err != nil {
			return nil, err
		}
		hashes["server/"+target+"/corr"] = hash
		if item.GOOS == "windows" {
			delete(hashes, "server/"+target+"/corr")
			hashes["server/"+target+"/corr.exe"] = hash
		}
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
		return nil, fmt.Errorf("MCPB source binaries are missing targets %q", missing)
	}
	return hashes, nil
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
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", filepath.Base(path))
	}
	return absolutePath, nil
}

func packageMissingFiles(extra map[string]any) []string {
	required := map[string]bool{
		"/usr/bin/corr":      false,
		"/usr/bin/corresync": false,
		"/usr/share/bash-completion/completions/corr":           false,
		"/usr/share/zsh/site-functions/_corr":                   false,
		"/usr/share/fish/vendor_completions.d/corr.fish":        false,
		"/usr/share/man/man1/corr.1":                            false,
		"/usr/share/doc/corresync/CHANGELOG.md":                 false,
		"/usr/share/doc/corresync/third_party_licenses":         false,
		"/usr/share/corresync/plugins/corresync":                false,
		"/usr/share/corresync/integrations":                     false,
		"/usr/share/corresync/.agents/plugins/marketplace.json": false,
		"/usr/share/corresync/.claude-plugin/marketplace.json":  false,
	}
	files, ok := extra["Files"].([]any)
	if !ok {
		return sortedMissingFiles(required)
	}
	for _, value := range files {
		file, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if destination, ok := file["dst"].(string); ok {
			if _, requiredDestination := required[destination]; requiredDestination {
				required[destination] = true
			}
		}
	}
	return sortedMissingFiles(required)
}

func verifyLicenseBundle(root string) error {
	required := map[string]bool{
		"github.com/alecthomas/kong/COPYING":               false,
		"github.com/hashicorp/go-multierror/LICENSE":       false,
		"github.com/hashicorp/go-multierror/multierror.go": false,
		"github.com/modelcontextprotocol/go-sdk/LICENSE":   false,
		"golang.org/x/sys/LICENSE":                         false,
	}
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("license bundle contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("license bundle contains non-regular file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files++
		relative = filepath.ToSlash(relative)
		if _, expected := required[relative]; expected {
			required[relative] = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect third-party license bundle: %w", err)
	}
	if missing := sortedMissingFiles(required); len(missing) > 0 {
		return fmt.Errorf("third-party license bundle is missing %q", missing)
	}
	if files < minimumLicenses {
		return fmt.Errorf(
			"third-party license bundle contains %d files, want at least %d",
			files, minimumLicenses,
		)
	}
	return nil
}

func sortedMissingFiles(required map[string]bool) []string {
	missing := make([]string, 0, len(required))
	for path, found := range required {
		if !found {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	return missing
}

func verifySBOM(path string) (string, error) {
	data, err := readLocalFile(path)
	if err != nil {
		return "", fmt.Errorf("read SBOM %q: %w", filepath.Base(path), err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("decode SBOM %q: %w", filepath.Base(path), err)
	}
	if strings.Contains(string(data), "syft-archive-contents-") {
		return "", fmt.Errorf("SBOM %q exposes a generated Syft temp path", filepath.Base(path))
	}
	if document["bomFormat"] == "CycloneDX" {
		metadata, ok := document["metadata"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("CycloneDX SBOM %q has no metadata", filepath.Base(path))
		}
		if err := verifyCanonicalTimestamp(metadata["timestamp"]); err != nil {
			return "", fmt.Errorf("CycloneDX SBOM %q: %w", filepath.Base(path), err)
		}
		serial, _ := document["serialNumber"].(string)
		if !strings.HasPrefix(serial, "urn:uuid:") || len(serial) != len("urn:uuid:")+36 {
			return "", fmt.Errorf("CycloneDX SBOM %q has a non-canonical serial number", filepath.Base(path))
		}
		return "CycloneDX", nil
	}
	if version, ok := document["spdxVersion"].(string); ok && strings.HasPrefix(version, "SPDX-") {
		creation, ok := document["creationInfo"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("SPDX SBOM %q has no creation info", filepath.Base(path))
		}
		if err := verifyCanonicalTimestamp(creation["created"]); err != nil {
			return "", fmt.Errorf("SPDX SBOM %q: %w", filepath.Base(path), err)
		}
		namespace, _ := document["documentNamespace"].(string)
		if !strings.HasPrefix(namespace, "https://github.com/nkiyohara/corresync/sbom/spdx/") {
			return "", fmt.Errorf("SPDX SBOM %q has a non-canonical namespace", filepath.Base(path))
		}
		return "SPDX", nil
	}
	return "", fmt.Errorf("SBOM %q has an unknown format", filepath.Base(path))
}

func verifyCanonicalTimestamp(value any) error {
	timestamp, ok := value.(string)
	if !ok {
		return errors.New("timestamp is missing")
	}
	parsed, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return fmt.Errorf("timestamp is not RFC3339: %w", err)
	}
	if timestamp != parsed.UTC().Format(time.RFC3339) {
		return errors.New("timestamp is not canonical UTC")
	}
	return nil
}

func verifyArchive(path, goos string) error {
	match := archiveNamePattern.FindStringSubmatch(filepath.Base(path))
	if len(match) != 2 {
		return fmt.Errorf("archive name %q does not contain one SemVer version", filepath.Base(path))
	}
	version := match[1]
	want := []string{
		".agents/plugins/marketplace.json",
		".claude-plugin/marketplace.json",
		"CHANGELOG.md",
		"LICENSE",
		"README.md",
		"SECURITY.md",
		"completions/_corr",
		"completions/corr.bash",
		"completions/corr.fish",
		"docs/install.md",
		"docs/mcp.md",
		"docs/generated/integration-bundles.md",
		"docs/generated/publication-channels.md",
		"integrations/config-hosts.json",
		"integrations/gemini-cli/corresync/gemini-extension.json",
		"integrations/gemini-cli/corresync/skills/corresync/SKILL.md",
		"integrations/kiro/corresync/POWER.md",
		"integrations/kiro/corresync/mcp.json",
		"integrations/kiro/corresync/steering/corresync.md",
		"manpages/corr.1",
		"plugins/corresync/.claude-plugin/plugin.json",
		"plugins/corresync/.codex-plugin/plugin.json",
		"plugins/corresync/.mcp.json",
		"plugins/corresync/README.md",
		"plugins/corresync/assets/icon.svg",
		"plugins/corresync/skills/corresync/SKILL.md",
		"plugins/corresync/skills/corresync/agents/openai.yaml",
		licensePrefix + "github.com/alecthomas/kong/COPYING",
		licensePrefix + "github.com/hashicorp/go-multierror/LICENSE",
		licensePrefix + "github.com/hashicorp/go-multierror/multierror.go",
	}
	if goos == "windows" {
		want = append(want, "corr.exe", "corresync.exe")
		return verifyZip(path, want, version)
	}
	want = append(want, "corr", "corresync")
	return verifyTarGzip(path, want, version)
}

func verifyMCPBundle(bundlePath string, binaryHashes map[string]string) error {
	name := filepath.Base(bundlePath)
	match := mcpbNamePattern.FindStringSubmatch(name)
	if len(match) != 2 {
		return fmt.Errorf("MCP bundle name %q does not contain one SemVer version", name)
	}
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		return fmt.Errorf("open MCP bundle %q: %w", name, err)
	}
	defer func() { _ = archive.Close() }()

	requiredModes := map[string]os.FileMode{
		"LICENSE":           0o644,
		"README.md":         0o644,
		"SECURITY.md":       0o644,
		"icon.png":          0o644,
		"manifest.json":     0o644,
		"server/launch.cmd": 0o644,
		"server/launch.sh":  0o755,
	}
	for binary := range binaryHashes {
		requiredModes[binary] = 0o755
	}
	found := make(map[string]bool, len(requiredModes))
	seen := make(map[string]bool, len(archive.File))
	var manifestData []byte
	licenseFiles := 0
	for _, file := range archive.File {
		if err := validateMCPBEntry(file); err != nil {
			return fmt.Errorf("MCP bundle %q: %w", name, err)
		}
		if seen[file.Name] {
			return fmt.Errorf("MCP bundle %q has duplicate entry %q", name, file.Name)
		}
		seen[file.Name] = true
		if strings.HasPrefix(file.Name, licensePrefix) {
			licenseFiles++
			if file.Mode().Perm() != 0o644 {
				return fmt.Errorf(
					"MCP bundle %q license %q has mode %#o, want 0644",
					name,
					file.Name,
					file.Mode().Perm(),
				)
			}
			continue
		}
		wantMode, expected := requiredModes[file.Name]
		if !expected {
			return fmt.Errorf("MCP bundle %q contains unexpected entry %q", name, file.Name)
		}
		if file.Mode().Perm() != wantMode {
			return fmt.Errorf(
				"MCP bundle %q entry %q has mode %#o, want %#o",
				name,
				file.Name,
				file.Mode().Perm(),
				wantMode,
			)
		}
		found[file.Name] = true
		if wantHash := binaryHashes[file.Name]; wantHash != "" {
			gotHash, err := hashZipEntry(file)
			if err != nil {
				return fmt.Errorf("hash MCP bundle binary %q: %w", file.Name, err)
			}
			if gotHash != wantHash {
				return fmt.Errorf(
					"MCP bundle binary %q differs from the verified release binary",
					file.Name,
				)
			}
		}
		if file.Name == "manifest.json" {
			manifestData, err = readZipEntry(file)
			if err != nil {
				return fmt.Errorf("read MCPB manifest: %w", err)
			}
		}
	}
	var missing []string
	for entry := range requiredModes {
		if !found[entry] {
			missing = append(missing, entry)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("MCP bundle %q is missing %q", name, missing)
	}
	if licenseFiles < minimumLicenses {
		return fmt.Errorf(
			"MCP bundle %q contains %d third-party license files, want at least %d",
			name,
			licenseFiles,
			minimumLicenses,
		)
	}
	if err := verifyMCPBManifest(manifestData, match[1]); err != nil {
		return fmt.Errorf("MCP bundle %q: %w", name, err)
	}
	return nil
}

func verifyRegistryManifest(path string, hashes map[string]string) error {
	data, err := readLocalFile(path)
	if err != nil {
		return fmt.Errorf("read MCP registry manifest: %w", err)
	}
	if err := integrationbundle.ValidateRegistryManifest(data); err != nil {
		return fmt.Errorf("validate MCP registry manifest: %w", err)
	}
	var document registryManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode MCP registry manifest: %w", err)
	}
	item := document.Packages[0]
	bundleName := "corresync_" + document.Version + ".mcpb"
	if item.FileSHA256 != hashes[bundleName] {
		return errors.New("MCP registry package does not bind the release MCPB over stdio")
	}
	return nil
}

func validateMCPBEntry(file *zip.File) error {
	name := file.Name
	clean := pathpkg.Clean(name)
	if name == "" ||
		clean != name ||
		pathpkg.IsAbs(clean) ||
		clean == ".." ||
		strings.HasPrefix(clean, "../") ||
		strings.Contains(name, `\`) {
		return fmt.Errorf("unsafe entry path %q", name)
	}
	if file.FileInfo().IsDir() {
		return fmt.Errorf("unexpected directory entry %q", name)
	}
	if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
		return fmt.Errorf("entry %q is not a regular file", name)
	}
	return nil
}

func hashZipEntry(file *zip.File) (string, error) {
	const maximumBinaryBytes = 256 * 1024 * 1024

	if file.UncompressedSize64 > maximumBinaryBytes {
		return "", fmt.Errorf("entry %q exceeds %d bytes", file.Name, maximumBinaryBytes)
	}
	reader, err := file.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()
	hash := sha256.New()
	// #nosec G110 -- the declared and streamed size are both bounded above.
	written, err := io.Copy(hash, io.LimitReader(reader, maximumBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if written != int64(file.UncompressedSize64) { // #nosec G115 -- bounded above.
		return "", fmt.Errorf(
			"entry %q size is %d, want %d",
			file.Name,
			written,
			file.UncompressedSize64,
		)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readZipEntry(file *zip.File) ([]byte, error) {
	const maximum = 256 * 1024

	if file.UncompressedSize64 > maximum {
		return nil, fmt.Errorf("entry %q exceeds %d bytes", file.Name, maximum)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("entry %q exceeds %d bytes", file.Name, maximum)
	}
	return data, nil
}

func verifyMCPBManifest(data []byte, version string) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode manifest object: %w", err)
	}
	if _, exists := raw["user_config"]; exists {
		return errors.New("manifest must not collect credentials or configuration")
	}
	var document mcpbManifest
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("decode manifest fields: %w", err)
	}
	if document.ManifestVersion != "0.4" ||
		document.Name != "corresync" ||
		document.Version != version {
		return errors.New("manifest identity does not match the release")
	}
	if !document.ToolsGenerated {
		return errors.New("manifest must discover the live tool catalog")
	}
	if !equalStringLists(
		document.PrivacyPolicies,
		[]string{"https://corresync.org/privacy.html"},
	) {
		return errors.New("manifest does not bind the Corresync Privacy Policy")
	}
	if document.Server.Type != "binary" ||
		document.Server.EntryPoint != "server/launch.sh" ||
		document.Server.MCPConfig.Command != "${__dirname}/server/launch.sh" ||
		len(document.Server.MCPConfig.Args) != 0 ||
		len(document.Server.MCPConfig.Env) != 0 {
		return errors.New("manifest does not use the reviewed local stdio launcher")
	}
	windows, exists := document.Server.MCPConfig.PlatformOverrides["win32"]
	wantWindowsArgs := []string{
		"/d",
		"/s",
		"/c",
		`"${__dirname}/server/launch.cmd"`,
	}
	if !exists ||
		len(document.Server.MCPConfig.PlatformOverrides) != 1 ||
		windows.Command != "cmd.exe" ||
		!equalStringLists(windows.Args, wantWindowsArgs) ||
		len(windows.Env) != 0 {
		return errors.New("manifest does not use the reviewed Windows launcher")
	}
	if !equalStringLists(
		document.Compatibility.Platforms,
		[]string{"darwin", "linux", "win32"},
	) {
		return errors.New("manifest platform inventory is incomplete")
	}
	return nil
}

func equalStringLists(left, right []string) bool {
	return slices.Equal(left, right)
}

func verifyZip(path string, want []string, version string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip %q: %w", filepath.Base(path), err)
	}
	defer func() { _ = archive.Close() }()
	names := make([]string, 0, len(archive.File))
	versioned := make(map[string][]byte)
	for _, file := range archive.File {
		names = append(names, file.Name)
		if isVersionedIntegration(file.Name) {
			data, readErr := readZipEntry(file)
			if readErr != nil {
				return fmt.Errorf("read versioned integration %q: %w", file.Name, readErr)
			}
			versioned[file.Name] = data
		}
	}
	if err := verifyIntegrationVersions(versioned, version); err != nil {
		return fmt.Errorf("archive %q: %w", filepath.Base(path), err)
	}
	return requireReleaseFiles(filepath.Base(path), names, want)
}

func verifyTarGzip(path string, want []string, version string) error {
	file, err := openLocalFile(path)
	if err != nil {
		return fmt.Errorf("open tarball %q: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream %q: %w", filepath.Base(path), err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	var names []string
	versioned := make(map[string][]byte)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball %q: %w", filepath.Base(path), err)
		}
		names = append(names, header.Name)
		if isVersionedIntegration(header.Name) {
			const maximum = 256 * 1024
			if header.Size < 0 || header.Size > maximum {
				return fmt.Errorf("versioned integration %q exceeds %d bytes", header.Name, maximum)
			}
			data, readErr := io.ReadAll(io.LimitReader(tarReader, maximum+1))
			if readErr != nil {
				return fmt.Errorf("read versioned integration %q: %w", header.Name, readErr)
			}
			if int64(len(data)) != header.Size {
				return fmt.Errorf("versioned integration %q size is %d, want %d", header.Name, len(data), header.Size)
			}
			versioned[header.Name] = data
		}
	}
	if err := verifyIntegrationVersions(versioned, version); err != nil {
		return fmt.Errorf("archive %q: %w", filepath.Base(path), err)
	}
	return requireReleaseFiles(filepath.Base(path), names, want)
}

func isVersionedIntegration(path string) bool {
	switch path {
	case ".claude-plugin/marketplace.json",
		"docs/generated/integration-bundles.md",
		"docs/generated/publication-channels.md",
		"integrations/config-hosts.json",
		"integrations/gemini-cli/corresync/gemini-extension.json",
		"integrations/kiro/corresync/POWER.md",
		"plugins/corresync/.claude-plugin/plugin.json",
		"plugins/corresync/.codex-plugin/plugin.json":
		return true
	default:
		return false
	}
}

func verifyIntegrationVersions(files map[string][]byte, version string) error {
	const claudeMarketplace = ".claude-plugin/marketplace.json"
	for _, path := range []string{
		claudeMarketplace,
		"integrations/config-hosts.json",
		"integrations/gemini-cli/corresync/gemini-extension.json",
		"plugins/corresync/.claude-plugin/plugin.json",
		"plugins/corresync/.codex-plugin/plugin.json",
	} {
		data := files[path]
		if len(data) == 0 {
			return fmt.Errorf("versioned integration %q is missing", path)
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("decode versioned integration %q: %w", path, err)
		}
		if document["version"] != version {
			return fmt.Errorf("integration %q version is %v, want %q", path, document["version"], version)
		}
		if path == claudeMarketplace {
			plugins, ok := document["plugins"].([]any)
			if !ok || len(plugins) != 1 {
				return errors.New("claude marketplace must contain one plugin")
			}
			plugin, ok := plugins[0].(map[string]any)
			if !ok || plugin["version"] != version {
				return errors.New("claude marketplace plugin version does not match the release")
			}
		}
	}
	for path, marker := range map[string]string{
		"docs/generated/integration-bundles.md":  "Canonical source snapshot:\n\n`" + version + "`.",
		"docs/generated/publication-channels.md": "Canonical source snapshot: `" + version + "`.",
		"integrations/kiro/corresync/POWER.md":   "Version: " + version,
	} {
		if !bytes.Contains(files[path], []byte(marker)) {
			return fmt.Errorf("integration %q does not contain release marker %q", path, marker)
		}
	}
	return nil
}

func requireReleaseFiles(archive string, got, want []string) error {
	required := make(map[string]bool, len(want))
	for _, name := range want {
		required[name] = false
	}
	licenseFiles := 0
	var unexpected []string
	for _, name := range got {
		if _, exists := required[name]; exists {
			required[name] = true
		}
		if strings.HasPrefix(name, licensePrefix) {
			if !strings.HasSuffix(name, "/") {
				licenseFiles++
			}
			continue
		}
		if _, exists := required[name]; !exists {
			unexpected = append(unexpected, name)
		}
	}
	if missing := sortedMissingFiles(required); len(missing) > 0 {
		return fmt.Errorf("archive %q is missing %q", archive, missing)
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("archive %q contains unexpected files %q", archive, unexpected)
	}
	if licenseFiles < minimumLicenses {
		return fmt.Errorf(
			"archive %q contains %d third-party license files, want at least %d",
			archive, licenseFiles, minimumLicenses,
		)
	}
	return nil
}

func verifyCatalogs(dist string, hashes map[string]string) error {
	formula, err := readLocalFile(filepath.Join(dist, "homebrew", "Formula", "corresync.rb"))
	if err != nil {
		return fmt.Errorf("read Homebrew Formula: %w", err)
	}
	for _, snippet := range []string{
		`depends_on "go" => :build`,
		`std_go_args(output: bin/"corr"`,
		`bin.install_symlink "corr" => "corresync"`,
		`bash_completion.install "completions/corr.bash" => "corr"`,
		`zsh_completion.install "completions/_corr"`,
		`fish_completion.install "completions/corr.fish"`,
		`man1.install "manpages/corr.1"`,
		`pkgshare.install "plugins"`,
		`(pkgshare/".agents").install ".agents/plugins"`,
		`(pkgshare/".claude-plugin").install ".claude-plugin/marketplace.json"`,
		`shell_output("#{bin}/corr version --json")`,
		`shell_output("#{bin}/corresync version --json")`,
	} {
		if !strings.Contains(string(formula), snippet) {
			return fmt.Errorf("homebrew Formula is missing %q", snippet)
		}
	}
	for name, hash := range hashes {
		if strings.HasSuffix(name, "_source.tar.gz") &&
			(!strings.Contains(string(formula), name) || !strings.Contains(string(formula), hash)) {
			return fmt.Errorf("homebrew Formula does not bind %q to its hash", name)
		}
	}

	manifestData, err := readLocalFile(filepath.Join(dist, "scoop", "corresync.json"))
	if err != nil {
		return fmt.Errorf("read Scoop manifest: %w", err)
	}
	var manifest scoopManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("decode Scoop manifest: %w", err)
	}
	if len(manifest.Architecture) != 2 {
		return fmt.Errorf("scoop architecture count is %d, want 2", len(manifest.Architecture))
	}
	for architecture, item := range manifest.Architecture {
		name := filepath.Base(item.URL)
		if hashes[name] != item.Hash {
			return fmt.Errorf("scoop %s hash does not match %q", architecture, name)
		}
		if len(item.Bin) != 2 || item.Bin[0] != "corr.exe" || item.Bin[1] != "corresync.exe" {
			return fmt.Errorf("scoop %s commands are %#v", architecture, item.Bin)
		}
	}

	wingetFiles, err := filepath.Glob(filepath.Join(dist, "winget", "manifests", "*", "*", "*", "*", "*"))
	if err != nil {
		return fmt.Errorf("find WinGet manifests: %w", err)
	}
	if len(wingetFiles) != 3 {
		return fmt.Errorf("WinGet manifest count is %d, want 3", len(wingetFiles))
	}
	var installer string
	for _, path := range wingetFiles {
		if strings.HasSuffix(path, ".installer.yaml") {
			data, err := readLocalFile(path)
			if err != nil {
				return fmt.Errorf("read WinGet installer manifest: %w", err)
			}
			installer = string(data)
		}
	}
	if !strings.Contains(installer, "PortableCommandAlias: corr") ||
		!strings.Contains(installer, "PortableCommandAlias: corresync") {
		return errors.New("WinGet manifest does not install corr and its compatibility command")
	}
	for name, hash := range hashes {
		if strings.Contains(name, "_windows_") && strings.HasSuffix(name, ".zip") {
			if !strings.Contains(installer, name) || !strings.Contains(installer, hash) {
				return fmt.Errorf("WinGet manifest does not bind %q to its hash", name)
			}
		}
	}
	return nil
}

func verifySourceArchive(archivePath string) error {
	file, err := openLocalFile(archivePath)
	if err != nil {
		return fmt.Errorf("open source archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read source archive compression: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	required := map[string]bool{
		".agents/plugins/marketplace.json":            false,
		".claude-plugin/marketplace.json":             false,
		"LICENSE":                                     false,
		"go.mod":                                      false,
		"go.sum":                                      false,
		"cmd/corr/main.go":                            false,
		"internal/buildinfo/buildinfo.go":             false,
		"manpages/corr.1":                             false,
		"vendor/modules.txt":                          false,
		"completions/corr.bash":                       false,
		"completions/_corr":                           false,
		"completions/corr.fish":                       false,
		"plugins/corresync/.codex-plugin/plugin.json": false,
		"plugins/corresync/.mcp.json":                 false,
		"plugins/corresync/skills/corresync/SKILL.md": false,
		"integrations/config-hosts.json":              false,
		"integrations/gemini-cli/corresync/gemini-extension.json": false,
		"integrations/kiro/corresync/POWER.md":                    false,
	}
	var prefix string
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read source archive: %w", err)
		}
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		clean := pathpkg.Clean(header.Name)
		if clean == "." || pathpkg.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("source archive contains unsafe path %q", header.Name)
		}
		parts := strings.SplitN(clean, "/", 2)
		if len(parts) == 1 {
			if header.FileInfo().IsDir() {
				if prefix == "" {
					prefix = parts[0]
				}
				continue
			}
			return fmt.Errorf("source archive file %q has no root directory", header.Name)
		}
		if prefix == "" {
			prefix = parts[0]
		}
		if parts[0] != prefix {
			return fmt.Errorf("source archive has multiple roots %q and %q", prefix, parts[0])
		}
		if _, exists := required[parts[1]]; exists && !header.FileInfo().IsDir() {
			required[parts[1]] = true
		}
	}
	if missing := sortedMissingFiles(required); len(missing) > 0 {
		return fmt.Errorf("source archive is missing %q", missing)
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := openLocalFile(path)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", filepath.Base(path), err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// The verifier is a local release-engineering command. Every caller either
// constructs a fixed path below --dist or first requires an artifact basename.
func readLocalFile(path string) ([]byte, error) {
	// #nosec G304 -- constrained local release output, never a network path.
	return os.ReadFile(path)
}

func openLocalFile(path string) (*os.File, error) {
	// #nosec G304 -- constrained local release output, never a network path.
	return os.Open(path)
}
