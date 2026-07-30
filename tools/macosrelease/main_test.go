package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRewriteTarGzipUsesSignedBinaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archivePath := filepath.Join(root, "corresync_darwin_arm64.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{
		"README.md": "documentation",
		"corr":      "unsigned corr",
		"corresync": "unsigned compatibility binary",
	})
	signedCorr := filepath.Join(root, "signed-corr")
	signedCompatibility := filepath.Join(root, "signed-corresync")
	writeTestFile(t, signedCorr, "signed corr")
	writeTestFile(t, signedCompatibility, "signed compatibility binary")

	err := rewriteTarGzip(archivePath, map[string]string{
		"corr":      signedCorr,
		"corresync": signedCompatibility,
	})
	if err != nil {
		t.Fatalf("rewrite archive: %v", err)
	}

	entries := readTestArchive(t, archivePath)
	if entries["corr"] != "signed corr" {
		t.Fatalf("corr = %q, want signed binary", entries["corr"])
	}
	if entries["corresync"] != "signed compatibility binary" {
		t.Fatalf("corresync = %q, want signed compatibility binary", entries["corresync"])
	}
	if entries["README.md"] != "documentation" {
		t.Fatalf("README.md = %q, want unchanged documentation", entries["README.md"])
	}
}

func TestRewriteTarGzipRejectsMissingBinary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	archivePath := filepath.Join(root, "corresync_darwin_amd64.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{"corr": "unsigned corr"})
	signedCorr := filepath.Join(root, "signed-corr")
	signedCompatibility := filepath.Join(root, "signed-corresync")
	writeTestFile(t, signedCorr, "signed corr")
	writeTestFile(t, signedCompatibility, "signed compatibility binary")

	err := rewriteTarGzip(archivePath, map[string]string{
		"corr":      signedCorr,
		"corresync": signedCompatibility,
	})
	if err == nil || !strings.Contains(err.Error(), `archive is missing binary "corresync"`) {
		t.Fatalf("error = %v, want missing compatibility binary", err)
	}
}

func TestRefreshChecksumsChangesOnlySelectedAssets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "darwin.tar.gz"), "new archive")
	writeTestFile(t, filepath.Join(root, "darwin.tar.gz.spdx.json"), "new SBOM")
	writeTestFile(t, filepath.Join(root, "linux.tar.gz"), "unchanged")
	checksumsPath := filepath.Join(root, "checksums.txt")
	writeTestFile(t, checksumsPath, strings.Join([]string{
		strings.Repeat("0", 64) + "  darwin.tar.gz",
		strings.Repeat("1", 64) + "  darwin.tar.gz.spdx.json",
		strings.Repeat("2", 64) + "  linux.tar.gz",
		"",
	}, "\n"))

	err := refreshChecksums(
		checksumsPath,
		root,
		[]string{"darwin.tar.gz", "darwin.tar.gz.spdx.json"},
	)
	if err != nil {
		t.Fatalf("refresh checksums: %v", err)
	}
	data, err := os.ReadFile(checksumsPath) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, testHash("new archive")+"  darwin.tar.gz\n") {
		t.Fatalf("checksums do not contain refreshed archive hash:\n%s", got)
	}
	if !strings.Contains(got, testHash("new SBOM")+"  darwin.tar.gz.spdx.json\n") {
		t.Fatalf("checksums do not contain refreshed SBOM hash:\n%s", got)
	}
	if !strings.Contains(got, strings.Repeat("2", 64)+"  linux.tar.gz\n") {
		t.Fatalf("checksums changed unrelated asset:\n%s", got)
	}
}

func writeTestArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()

	file, err := os.Create(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.ModTime = time.Unix(1_700_000_000, 0).UTC()
	tarWriter := tar.NewWriter(gzipWriter)
	for _, name := range []string{"README.md", "corresync", "corr"} {
		content, exists := entries[name]
		if !exists {
			continue
		}
		mode := int64(0o644)
		if name == "corr" || name == "corresync" {
			mode = 0o755
		}
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:    name,
			Mode:    mode,
			Size:    int64(len(content)),
			ModTime: time.Unix(1_700_000_000, 0).UTC(),
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := io.WriteString(tarWriter, content); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func readTestArchive(t *testing.T, path string) map[string]string {
	t.Helper()

	file, err := os.Open(path) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	entries := make(map[string]string)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		entries[header.Name] = string(data)
	}
	return entries
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

func testHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
