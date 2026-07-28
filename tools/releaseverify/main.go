// Command releaseverify validates the complete local GoReleaser output.
package main

import (
	"archive/tar"
	"archive/zip"
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
	"sort"
	"strings"
	"time"
)

const (
	expectedArchives = 6
	expectedPackages = 6
	expectedSBOMs    = 26
	expectedSources  = 1
	licensePrefix    = "third_party_licenses/"
	minimumLicenses  = 24
)

type artifact struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	GOOS   string         `json:"goos"`
	GOARCH string         `json:"goarch"`
	Extra  map[string]any `json:"extra"`
}

type scoopManifest struct {
	Architecture map[string]struct {
		URL  string `json:"url"`
		Hash string `json:"hash"`
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
		"release verification passed: %d archives, %d packages, %d SBOMs\n",
		expectedArchives,
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
	expectedChecksums := expectedArchives + expectedPackages + expectedSBOMs + expectedSources
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
	packageFormats := make(map[string]int)
	sbomFormats := make(map[string]int)
	for _, item := range artifacts {
		counts[item.Type]++
		switch item.Type {
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
		"Archive":       expectedArchives,
		"Binary":        expectedArchives,
		"Checksum":      1,
		"Linux Package": expectedPackages,
		"Metadata":      1,
		"SBOM":          expectedSBOMs,
		"Source":        expectedSources,
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

func packageMissingFiles(extra map[string]any) []string {
	required := map[string]bool{
		"/usr/bin/corresync": false,
		"/usr/share/bash-completion/completions/corresync":      false,
		"/usr/share/zsh/site-functions/_corresync":              false,
		"/usr/share/fish/vendor_completions.d/corresync.fish":   false,
		"/usr/share/man/man1/corresync.1":                       false,
		"/usr/share/doc/corresync/CHANGELOG.md":                 false,
		"/usr/share/doc/corresync/third_party_licenses":         false,
		"/usr/share/corresync/plugins/corresync":                false,
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
	want := []string{
		".agents/plugins/marketplace.json",
		".claude-plugin/marketplace.json",
		"CHANGELOG.md",
		"LICENSE",
		"README.md",
		"SECURITY.md",
		"completions/_corresync",
		"completions/corresync.bash",
		"completions/corresync.fish",
		"docs/install.md",
		"docs/mcp.md",
		"manpages/corresync.1",
		"plugins/corresync/.claude-plugin/plugin.json",
		"plugins/corresync/.codex-plugin/plugin.json",
		"plugins/corresync/README.md",
		"plugins/corresync/assets/icon.svg",
		"plugins/corresync/skills/corresync/SKILL.md",
		"plugins/corresync/skills/corresync/agents/openai.yaml",
		licensePrefix + "github.com/alecthomas/kong/COPYING",
		licensePrefix + "github.com/hashicorp/go-multierror/LICENSE",
		licensePrefix + "github.com/hashicorp/go-multierror/multierror.go",
	}
	if goos == "windows" {
		want = append(want, "corresync.exe")
		return verifyZip(path, want)
	}
	want = append(want, "corresync")
	return verifyTarGzip(path, want)
}

func verifyZip(path string, want []string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip %q: %w", filepath.Base(path), err)
	}
	defer func() { _ = archive.Close() }()
	names := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	return requireReleaseFiles(filepath.Base(path), names, want)
}

func verifyTarGzip(path string, want []string) error {
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
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tarball %q: %w", filepath.Base(path), err)
		}
		names = append(names, header.Name)
	}
	return requireReleaseFiles(filepath.Base(path), names, want)
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
		`std_go_args(output: bin/"corresync"`,
		`bash_completion.install "completions/corresync.bash" => "corresync"`,
		`zsh_completion.install "completions/_corresync"`,
		`fish_completion.install "completions/corresync.fish"`,
		`man1.install "manpages/corresync.1"`,
		`pkgshare.install "plugins"`,
		`(pkgshare/".agents").install ".agents/plugins"`,
		`(pkgshare/".claude-plugin").install ".claude-plugin/marketplace.json"`,
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
	if !strings.Contains(installer, "PortableCommandAlias: corresync") {
		return errors.New("WinGet manifest does not install the corresync command")
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
		"cmd/corresync/main.go":                       false,
		"internal/buildinfo/buildinfo.go":             false,
		"manpages/corresync.1":                        false,
		"vendor/modules.txt":                          false,
		"completions/corresync.bash":                  false,
		"completions/_corresync":                      false,
		"completions/corresync.fish":                  false,
		"plugins/corresync/.codex-plugin/plugin.json": false,
		"plugins/corresync/skills/corresync/SKILL.md": false,
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
