package releasepublication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

func TestVerifyAssetsAndRegistry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	candidate := writeCandidate(t, root, "1.2.3")
	if candidate.Version != "1.2.3" || candidate.RegistryName != "io.github.nkiyohara/corresync" {
		t.Fatalf("candidate = %#v", candidate)
	}
	manifest, err := os.ReadFile( // #nosec G304 -- fixed fixture path under a test-owned directory.
		filepath.Join(root, "server.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := registryFixture(t, manifest, "active", true)
	record, err := VerifyRegistry(context.Background(), staticClient{body: response}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewEvidence(candidate, record, strings.Repeat("a", 40), time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "evidence", "registry.json")
	if err := WriteEvidence(path, evidence); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed fixture path under a test-owned directory.
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) || bytes.Contains(data, []byte("token")) || !bytes.Contains(data, []byte(`"registryIsLatest": true`)) {
		t.Fatalf("unexpected evidence: %s", data)
	}
	present, err := CheckExisting(context.Background(), staticClient{body: response}, candidate)
	if err != nil || !present {
		t.Fatalf("existing version = %t, %v", present, err)
	}
	present, err = CheckExisting(context.Background(), staticClient{status: http.StatusNotFound}, candidate)
	if err != nil || present {
		t.Fatalf("missing version = %t, %v", present, err)
	}
}

func TestVerifyAssetsRejectsPreviewAndTampering(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCandidate(t, root, "1.2.3")
	if _, err := VerifyAssets(root, "v1.2.3-rc.1"); err == nil {
		t.Fatal("preview tag was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "corresync_1.2.3.mcpb"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAssets(root, "v1.2.3"); err == nil {
		t.Fatal("tampered bundle was accepted")
	}
}

func TestVerifyRegistryRejectsWrongStateAndOversize(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	candidate := writeCandidate(t, root, "1.2.3")
	manifest, err := os.ReadFile( // #nosec G304 -- fixed fixture path under a test-owned directory.
		filepath.Join(root, "server.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range [][]byte{
		registryFixture(t, manifest, "deprecated", true),
		registryFixture(t, manifest, "active", false),
		bytes.Repeat([]byte(" "), maxRegistryBytes+1),
	} {
		if _, err := VerifyRegistry(context.Background(), staticClient{body: fixture}, candidate); err == nil {
			t.Fatal("invalid registry response was accepted")
		}
	}
}

func writeCandidate(t *testing.T, root, version string) Candidate {
	t.Helper()
	bundleName := "corresync_" + version + ".mcpb"
	files := map[string][]byte{
		bundleName:                []byte("synthetic MCPB"),
		bundleName + ".cdx.json":  []byte("{\"bomFormat\":\"CycloneDX\"}\n"),
		bundleName + ".spdx.json": []byte("{\"spdxVersion\":\"SPDX-2.3\"}\n"),
	}
	bundleHash := digest(files[bundleName])
	manifest, err := integrationbundle.RenderRegistryManifest(version, bundleHash)
	if err != nil {
		t.Fatal(err)
	}
	files["server.json"] = manifest
	var checksums strings.Builder
	names := []string{bundleName, bundleName + ".cdx.json", bundleName + ".spdx.json", "server.json"}
	for _, name := range names {
		checksums.WriteString(digest(files[name]) + "  " + name + "\n")
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "checksums.txt"), []byte(checksums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "checksums.txt.sigstore.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := VerifyAssets(root, "v"+version)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func registryFixture(t *testing.T, manifest []byte, status string, latest bool) []byte {
	t.Helper()
	var server any
	if err := json.Unmarshal(manifest, &server); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"server": server,
		"_meta": map[string]any{registryMetaKey: map[string]any{
			"status": status, "isLatest": latest, "publishedAt": "2026-08-14T02:59:00Z",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type staticClient struct {
	body   []byte
	status int
}

func (client staticClient) Do(request *http.Request) (*http.Response, error) {
	validURL := request.URL.String() == RegistryEndpoint || strings.HasPrefix(request.URL.String(), registryVersionPrefix)
	if request.Method != http.MethodGet || !validURL || request.Header.Get("Accept") != "application/json" {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("bad request"))}, nil
	}
	status := client.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(client.body))}, nil
}
