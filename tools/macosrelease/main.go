// Command macosrelease repacks the Darwin release archives from the exact
// Developer ID-signed binaries, refreshes their SBOMs, and updates checksums.
package main

import (
	"archive/tar"
	"compress/gzip"
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
	"sort"
	"strings"
	"time"
)

const (
	primaryBuildID       = "corr"
	compatibilityBuildID = "corresync-compat"
	releaseArchiveID     = "release"
)

var expectedDarwinArchitectures = []string{"amd64", "arm64"}

type artifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	Type   string         `json:"type"`
	GOOS   string         `json:"goos"`
	GOARCH string         `json:"goarch"`
	Extra  map[string]any `json:"extra"`
}

type darwinRelease struct {
	archivePath string
	binaries    map[string]string
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser output directory")
	sbomCommand := flag.String(
		"sbom-command",
		filepath.Join(".release", "syft-reproducible"),
		"reproducible Syft wrapper",
	)
	flag.Parse()

	updated, err := repack(*dist, *sbomCommand)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "macOS release repack failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"macOS release repack passed: %d signed archives and %d refreshed SBOMs\n",
		len(updated)/3,
		len(updated)-len(updated)/3,
	)
}

func repack(dist, sbomCommand string) ([]string, error) {
	artifacts, err := readArtifacts(filepath.Join(dist, "artifacts.json"))
	if err != nil {
		return nil, err
	}
	releases, err := collectDarwinReleases(dist, artifacts)
	if err != nil {
		return nil, err
	}

	var updated []string
	for _, architecture := range expectedDarwinArchitectures {
		release := releases[architecture]
		if err := rewriteTarGzip(release.archivePath, release.binaries); err != nil {
			return nil, fmt.Errorf("repack Darwin %s archive: %w", architecture, err)
		}
		archiveName := filepath.Base(release.archivePath)
		sbomNames, err := refreshSBOMs(dist, sbomCommand, archiveName)
		if err != nil {
			return nil, fmt.Errorf("refresh Darwin %s SBOMs: %w", architecture, err)
		}
		updated = append(updated, archiveName)
		updated = append(updated, sbomNames...)
	}
	sort.Strings(updated)
	if err := refreshChecksums(filepath.Join(dist, "checksums.txt"), dist, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func readArtifacts(path string) ([]artifact, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- caller-selected release directory.
	if err != nil {
		return nil, fmt.Errorf("read artifact inventory: %w", err)
	}
	var artifacts []artifact
	if err := json.Unmarshal(data, &artifacts); err != nil {
		return nil, fmt.Errorf("decode artifact inventory: %w", err)
	}
	return artifacts, nil
}

func collectDarwinReleases(dist string, artifacts []artifact) (map[string]darwinRelease, error) {
	releases := make(map[string]darwinRelease, len(expectedDarwinArchitectures))
	for _, architecture := range expectedDarwinArchitectures {
		releases[architecture] = darwinRelease{binaries: make(map[string]string)}
	}

	for _, item := range artifacts {
		if item.GOOS != "darwin" {
			continue
		}
		release, expectedArchitecture := releases[item.GOARCH]
		if !expectedArchitecture {
			return nil, fmt.Errorf("unexpected Darwin architecture %q", item.GOARCH)
		}
		switch item.Type {
		case "Binary":
			var binaryName string
			switch stringExtra(item.Extra, "ID") {
			case primaryBuildID:
				binaryName = "corr"
			case compatibilityBuildID:
				binaryName = "corresync"
			default:
				continue
			}
			if item.Name != binaryName {
				return nil, fmt.Errorf(
					"darwin %s binary for build %q is named %q, want %q",
					item.GOARCH,
					stringExtra(item.Extra, "ID"),
					item.Name,
					binaryName,
				)
			}
			if release.binaries[binaryName] != "" {
				return nil, fmt.Errorf("duplicate Darwin %s binary %q", item.GOARCH, binaryName)
			}
			path, err := releaseArtifactPath(dist, item.Path)
			if err != nil {
				return nil, fmt.Errorf("darwin %s binary %q: %w", item.GOARCH, binaryName, err)
			}
			release.binaries[binaryName] = path
		case "Archive":
			if stringExtra(item.Extra, "ID") != releaseArchiveID {
				continue
			}
			if release.archivePath != "" {
				return nil, fmt.Errorf("duplicate Darwin %s release archive", item.GOARCH)
			}
			path, err := releaseArtifactPath(dist, item.Path)
			if err != nil {
				return nil, fmt.Errorf("darwin %s archive: %w", item.GOARCH, err)
			}
			release.archivePath = path
		}
		releases[item.GOARCH] = release
	}

	for _, architecture := range expectedDarwinArchitectures {
		release := releases[architecture]
		if release.archivePath == "" {
			return nil, fmt.Errorf("missing Darwin %s release archive", architecture)
		}
		for _, binaryName := range []string{"corr", "corresync"} {
			if release.binaries[binaryName] == "" {
				return nil, fmt.Errorf("missing Darwin %s binary %q", architecture, binaryName)
			}
		}
	}
	return releases, nil
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
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact %q is not a regular file", filepath.Base(path))
	}
	return absolutePath, nil
}

func rewriteTarGzip(path string, replacements map[string]string) error {
	source, err := os.Open(path) // #nosec G304 -- validated release artifact path.
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	info, err := source.Stat()
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keepTemp := false
	defer func() {
		_ = temp.Close()
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		return err
	}

	gzipWriter := gzip.NewWriter(temp)
	gzipWriter.Header = gzipReader.Header
	tarReader := tar.NewReader(gzipReader)
	tarWriter := tar.NewWriter(gzipWriter)
	replaced := make(map[string]bool, len(replacements))

	for {
		header, readErr := tarReader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read tar entry: %w", readErr)
		}
		headerCopy := *header
		replacementPath, replace := replacements[header.Name]
		if replace {
			if replaced[header.Name] {
				return fmt.Errorf("archive contains duplicate binary %q", header.Name)
			}
			if !header.FileInfo().Mode().IsRegular() {
				return fmt.Errorf("archive binary %q is not a regular file", header.Name)
			}
			data, readReplacementErr := os.ReadFile(replacementPath) // #nosec G304 -- validated artifact.
			if readReplacementErr != nil {
				return fmt.Errorf("read signed binary %q: %w", header.Name, readReplacementErr)
			}
			headerCopy.Size = int64(len(data))
			if err := tarWriter.WriteHeader(&headerCopy); err != nil {
				return fmt.Errorf("write tar header %q: %w", header.Name, err)
			}
			if _, err := tarWriter.Write(data); err != nil {
				return fmt.Errorf("write signed binary %q: %w", header.Name, err)
			}
			replaced[header.Name] = true
			continue
		}
		if err := tarWriter.WriteHeader(&headerCopy); err != nil {
			return fmt.Errorf("write tar header %q: %w", header.Name, err)
		}
		if _, err := io.CopyN(tarWriter, tarReader, header.Size); err != nil {
			return fmt.Errorf("copy tar entry %q: %w", header.Name, err)
		}
	}
	for binaryName := range replacements {
		if !replaced[binaryName] {
			return fmt.Errorf("archive is missing binary %q", binaryName)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar stream: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("close gzip stream: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func refreshSBOMs(dist, command, archiveName string) ([]string, error) {
	absoluteCommand, err := filepath.Abs(command)
	if err != nil {
		return nil, fmt.Errorf("resolve SBOM command: %w", err)
	}
	outputs := []struct {
		name   string
		format string
	}{
		{name: archiveName + ".spdx.json", format: "spdx-json"},
		{name: archiveName + ".cdx.json", format: "cyclonedx-json"},
	}
	for _, output := range outputs {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		process := exec.CommandContext( // #nosec G204 -- workflow selects the fixed tool path.
			ctx,
			absoluteCommand,
			archiveName,
			"--output",
			output.format+"="+output.name,
			"--enrich",
			"all",
		)
		process.Dir = dist
		process.Stdout = os.Stdout
		process.Stderr = os.Stderr
		err := process.Run()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", output.name, err)
		}
	}
	return []string{outputs[0].name, outputs[1].name}, nil
}

func refreshChecksums(path, dist string, names []string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed release manifest path.
	if err != nil {
		return fmt.Errorf("read checksum manifest: %w", err)
	}
	hashes := make(map[string]string)
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("checksum line %d is malformed", lineNumber+1)
		}
		if filepath.Base(fields[1]) != fields[1] {
			return fmt.Errorf("checksum path %q is not a basename", fields[1])
		}
		hashes[fields[1]] = strings.ToLower(fields[0])
	}
	for _, name := range names {
		if _, exists := hashes[name]; !exists {
			return fmt.Errorf("checksum manifest is missing %q", name)
		}
		hash, err := hashFile(filepath.Join(dist, name))
		if err != nil {
			return err
		}
		hashes[name] = hash
	}
	sortedNames := make([]string, 0, len(hashes))
	for name := range hashes {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	var output strings.Builder
	for _, name := range sortedNames {
		fmt.Fprintf(&output, "%s  %s\n", hashes[name], name)
	}
	// #nosec G306 -- the public checksum manifest intentionally uses mode 0644.
	if err := os.WriteFile(path, []byte(output.String()), 0o644); err != nil {
		return fmt.Errorf("write checksum manifest: %w", err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- validated release asset path.
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
