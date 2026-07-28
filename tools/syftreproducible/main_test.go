package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutputDocument(t *testing.T) {
	t.Parallel()

	format, document, err := outputDocument([]string{
		"corresync.tar.gz", "--output", "spdx-json=corresync.tar.gz.spdx.json", "--enrich", "all",
	})
	if err != nil {
		t.Fatalf("outputDocument() error = %v", err)
	}
	if format != "spdx-json" || document != "corresync.tar.gz.spdx.json" {
		t.Fatalf("outputDocument() = %q, %q", format, document)
	}
	for _, arguments := range [][]string{
		{"../corresync.tar.gz", "--output", "spdx-json=sbom.json"},
		{"corresync.tar.gz", "--output", "xml=sbom.xml"},
		{"corresync.tar.gz", "--output", "spdx-json=../sbom.json"},
		{"corresync.tar.gz"},
	} {
		if _, _, err := outputDocument(arguments); err == nil {
			t.Errorf("outputDocument(%q) unexpectedly succeeded", arguments)
		}
	}
}

func TestCanonicalizeSPDXIsStable(t *testing.T) {
	t.Parallel()

	first := canonicalizeFixture(t, "spdx-json", `{
		"name":"corresync","documentNamespace":"https://random/one",
		"creationInfo":{"created":"2026-01-01T00:00:00Z"},"packages":[]}`)
	second := canonicalizeFixture(t, "spdx-json", `{
		"packages":[],"creationInfo":{"created":"2027-01-01T00:00:00Z"},
		"documentNamespace":"https://random/two","name":"corresync"}`)
	if first != second {
		t.Fatalf("canonical SPDX differs:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, `"created":"2026-07-17T20:37:49Z"`) ||
		!strings.Contains(first, `"documentNamespace":"https://github.com/nkiyohara/corresync/sbom/spdx/`) {
		t.Fatalf("canonical SPDX metadata is invalid: %s", first)
	}
}

func TestCanonicalizeCycloneDXIsStable(t *testing.T) {
	t.Parallel()

	first := canonicalizeFixture(t, "cyclonedx-json", `{
		"bomFormat":"CycloneDX","serialNumber":"urn:uuid:random-one",
		"metadata":{"timestamp":"2026-01-01T00:00:00Z"},
		"components":[{"name":"/tmp/syft-archive-contents-1234/corresync"}]}`)
	second := canonicalizeFixture(t, "cyclonedx-json", `{
		"components":[{"name":"/tmp/syft-archive-contents-9876/corresync"}],
		"metadata":{"timestamp":"2027-01-01T00:00:00Z"},
		"serialNumber":"urn:uuid:random-two","bomFormat":"CycloneDX"}`)
	if first != second {
		t.Fatalf("canonical CycloneDX differs:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, `"timestamp":"2026-07-17T20:37:49Z"`) ||
		!strings.Contains(first, `"serialNumber":"urn:uuid:`) ||
		!strings.Contains(first, `"name":"corresync"`) {
		t.Fatalf("canonical CycloneDX metadata is invalid: %s", first)
	}
}

func canonicalizeFixture(t *testing.T, format, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sbom.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, time.July, 17, 20, 37, 49, 0, time.UTC)
	if err := canonicalize(path, format, when); err != nil {
		t.Fatalf("canonicalize() error = %v", err)
	}
	// #nosec G304 -- path is created below this test's temporary directory.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
