// Command catalogrender renders package-manager metadata from one verified
// GoReleaser checksum manifest. It never rebuilds a release artifact.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const repositoryURL = "https://github.com/nkiyohara/corresync"

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	tagPattern     = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

type metadata struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type scoopManifest struct {
	Version      string                       `json:"version"`
	Architecture map[string]scoopArchitecture `json:"architecture"`
	Homepage     string                       `json:"homepage"`
	License      string                       `json:"license"`
	Description  string                       `json:"description"`
	Checkver     scoopCheckver                `json:"checkver"`
	Autoupdate   scoopAutoupdate              `json:"autoupdate"`
}

type scoopArchitecture struct {
	URL  string   `json:"url"`
	Bin  []string `json:"bin"`
	Hash string   `json:"hash"`
}

type scoopCheckver struct {
	GitHub string `json:"github"`
}

type scoopAutoupdate struct {
	Architecture map[string]scoopAutoupdateArchitecture `json:"architecture"`
}

type scoopAutoupdateArchitecture struct {
	URL string `json:"url"`
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	flag.Parse()

	if err := render(*dist); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "catalog rendering failed: %v\n", err)
		os.Exit(1)
	}
}

func render(dist string) error {
	release, err := readMetadata(filepath.Join(dist, "metadata.json"))
	if err != nil {
		return err
	}
	hashes, err := readChecksums(filepath.Join(dist, "checksums.txt"))
	if err != nil {
		return err
	}

	formula, err := renderFormula(release, hashes)
	if err != nil {
		return err
	}
	if err := writeCatalog(
		filepath.Join(dist, "homebrew", "Formula", "corresync.rb"),
		[]byte(formula),
	); err != nil {
		return err
	}

	scoop, err := renderScoop(release, hashes)
	if err != nil {
		return err
	}
	if err := writeCatalog(filepath.Join(dist, "scoop", "corresync.json"), scoop); err != nil {
		return err
	}

	winget, err := renderWinget(release, hashes)
	if err != nil {
		return err
	}
	root := filepath.Join(
		dist,
		"winget",
		"manifests",
		"n",
		"nkiyohara",
		"Corresync",
		release.Version,
	)
	for name, contents := range winget {
		if err := writeCatalog(filepath.Join(root, name), []byte(contents)); err != nil {
			return err
		}
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
	if !tagPattern.MatchString(release.Tag) {
		return metadata{}, fmt.Errorf("invalid release tag %q", release.Tag)
	}
	if !commitPattern.MatchString(release.Commit) {
		return metadata{}, fmt.Errorf("invalid release commit %q", release.Commit)
	}
	if _, err := time.Parse(time.RFC3339, release.Date); err != nil {
		return metadata{}, fmt.Errorf("invalid release date %q: %w", release.Date, err)
	}
	return release, nil
}

func readChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path) // #nosec G304 -- fixed file below caller-provided --dist.
	if err != nil {
		return nil, fmt.Errorf("open checksum manifest: %w", err)
	}
	defer func() { _ = file.Close() }()

	hashes := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, fmt.Errorf("checksum line %d is malformed", lineNumber)
		}
		hash, name := strings.ToLower(fields[0]), fields[1]
		if filepath.Base(name) != name {
			return nil, fmt.Errorf("checksum path %q is not a basename", name)
		}
		if len(hash) != sha256.Size*2 {
			return nil, fmt.Errorf("checksum for %q is not SHA-256", name)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("checksum for %q is not hexadecimal: %w", name, err)
		}
		if _, exists := hashes[name]; exists {
			return nil, fmt.Errorf("duplicate checksum for %q", name)
		}
		hashes[name] = hash
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	return hashes, nil
}

func requireHash(hashes map[string]string, name string) (string, error) {
	hash := hashes[name]
	if hash == "" {
		return "", fmt.Errorf("release checksum is missing %q", name)
	}
	return hash, nil
}

func renderFormula(release metadata, hashes map[string]string) (string, error) {
	name := fmt.Sprintf("corresync_%s_source.tar.gz", release.Version)
	hash, err := requireHash(hashes, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`class Corresync < Formula
  desc "Local-first guarded mail and calendar CLI and MCP server"
  homepage "%s"
  url "%s/releases/download/%s/%s"
  sha256 "%s"
  license "Apache-2.0"

  depends_on "go" => :build

  def install
    ldflags = %%W[
      -s -w -buildid=
      -X github.com/nkiyohara/corresync/internal/buildinfo.version=#{version}
      -X github.com/nkiyohara/corresync/internal/buildinfo.commit=%s
      -X github.com/nkiyohara/corresync/internal/buildinfo.buildDate=%s
    ]
    system "go", "build", "-mod=vendor",
           *std_go_args(output: bin/"corresync", ldflags: ldflags.join(" ")),
           "./cmd/corresync"

    man1.install "manpages/corresync.1"
    bash_completion.install "completions/corresync.bash" => "corresync"
    zsh_completion.install "completions/_corresync"
    fish_completion.install "completions/corresync.fish"
    pkgshare.install "plugins"
    (pkgshare/".agents").install ".agents/plugins"
    (pkgshare/".claude-plugin").install ".claude-plugin/marketplace.json"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/corresync version --json")
  end
end
`, repositoryURL, repositoryURL, release.Tag, name, hash, release.Commit, release.Date), nil
}

func renderScoop(release metadata, hashes map[string]string) ([]byte, error) {
	architectures := map[string]string{
		"64bit": "amd64",
		"arm64": "arm64",
	}
	manifest := scoopManifest{
		Version:      release.Version,
		Architecture: make(map[string]scoopArchitecture, len(architectures)),
		Homepage:     repositoryURL,
		License:      "Apache-2.0",
		Description:  "Local-first guarded mail and calendar CLI and MCP server",
		Checkver:     scoopCheckver{GitHub: repositoryURL},
		Autoupdate: scoopAutoupdate{
			Architecture: make(map[string]scoopAutoupdateArchitecture, len(architectures)),
		},
	}
	for scoopArch, goArch := range architectures {
		name := fmt.Sprintf("corresync_%s_windows_%s.zip", release.Version, goArch)
		hash, err := requireHash(hashes, name)
		if err != nil {
			return nil, err
		}
		manifest.Architecture[scoopArch] = scoopArchitecture{
			URL:  fmt.Sprintf("%s/releases/download/%s/%s", repositoryURL, release.Tag, name),
			Bin:  []string{"corresync.exe"},
			Hash: hash,
		}
		manifest.Autoupdate.Architecture[scoopArch] = scoopAutoupdateArchitecture{
			URL: fmt.Sprintf(
				"%s/releases/download/v$version/corresync_$version_windows_%s.zip",
				repositoryURL,
				goArch,
			),
		}
	}
	data, err := json.MarshalIndent(manifest, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("encode Scoop manifest: %w", err)
	}
	return append(data, '\n'), nil
}

func renderWinget(release metadata, hashes map[string]string) (map[string]string, error) {
	date, err := time.Parse(time.RFC3339, release.Date)
	if err != nil {
		return nil, err
	}
	type installer struct {
		wingetArch string
		goArch     string
	}
	installers := []installer{{wingetArch: "arm64", goArch: "arm64"}, {wingetArch: "x64", goArch: "amd64"}}
	var installerEntries strings.Builder
	for _, item := range installers {
		name := fmt.Sprintf("corresync_%s_windows_%s.zip", release.Version, item.goArch)
		hash, err := requireHash(hashes, name)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(&installerEntries, `  - Architecture: %s
    NestedInstallerType: portable
    NestedInstallerFiles:
      - RelativeFilePath: corresync.exe
        PortableCommandAlias: corresync
    InstallerUrl: %s/releases/download/%s/%s
    InstallerSha256: %s
    UpgradeBehavior: uninstallPrevious
`, item.wingetArch, repositoryURL, release.Tag, name, hash)
	}

	installerYAML := fmt.Sprintf(`# yaml-language-server: $schema=https://aka.ms/winget-manifest.installer.1.12.0.schema.json
PackageIdentifier: nkiyohara.Corresync
PackageVersion: %s
InstallerLocale: en-US
InstallerType: zip
ReleaseDate: "%s"
Installers:
%sManifestType: installer
ManifestVersion: 1.12.0
`, release.Version, date.Format(time.DateOnly), installerEntries.String())

	localeYAML := fmt.Sprintf(`# yaml-language-server: $schema=https://aka.ms/winget-manifest.defaultLocale.1.12.0.schema.json
PackageIdentifier: nkiyohara.Corresync
PackageVersion: %s
PackageLocale: en-US
Publisher: nkiyohara
PublisherUrl: https://github.com/nkiyohara
PublisherSupportUrl: %s/issues
PackageName: Corresync
PackageUrl: %s
License: Apache-2.0
LicenseUrl: %s/blob/main/LICENSE
ShortDescription: Local-first Outlook Web CLI and MCP server
Description: Manage an authorized Outlook Web mail and calendar session through a guarded CLI or local MCP server without a Microsoft Graph application.
Moniker: corresync
Tags:
  - outlook
  - email
  - calendar
  - cli
  - mcp
ReleaseNotesUrl: %s/releases/tag/%s
InstallationNotes: Run `+"`corresync auth login`"+` interactively, then register your agent with `+"`corresync mcp setup <client>`"+`. See the MCP guide for all supported clients.
ManifestType: defaultLocale
ManifestVersion: 1.12.0
`, release.Version, repositoryURL, repositoryURL, repositoryURL, repositoryURL, release.Tag)

	versionYAML := fmt.Sprintf(`# yaml-language-server: $schema=https://aka.ms/winget-manifest.version.1.12.0.schema.json
PackageIdentifier: nkiyohara.Corresync
PackageVersion: %s
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.12.0
`, release.Version)

	prefix := "nkiyohara.Corresync"
	return map[string]string{
		prefix + ".installer.yaml":    installerYAML,
		prefix + ".locale.en-US.yaml": localeYAML,
		prefix + ".yaml":              versionYAML,
	}, nil
}

func writeCatalog(path string, contents []byte) error {
	if filepath.Clean(path) != path {
		return errors.New("catalog destination is not clean")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create catalog directory: %w", err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil { // #nosec G306 -- public manifest.
		return fmt.Errorf("write catalog %q: %w", filepath.Base(path), err)
	}
	return nil
}
