package updatecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerVerifiesAndReplacesDirectRelease(t *testing.T) {
	target := filepath.Join(secureTempDir(t), "owa")
	oldBinary := []byte("#!/bin/sh\nprintf old\n")
	if err := os.WriteFile(target, oldBinary, 0o755); err != nil { // #nosec G306 -- synthetic executable fixture.
		t.Fatal(err)
	}
	candidate := syntheticCandidate()
	release := newSyntheticUpdateRelease(t, candidate, "")
	defer release.server.Close()

	var verifiedIdentity string
	var progress []InstallStage
	installer := Installer{
		CurrentVersion: "1.0.0",
		Executable:     target,
		TrustCachePath: filepath.Join(t.TempDir(), "trust"),
		Endpoint:       release.server.URL + "/latest",
		Client:         release.server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		Progress: func(item InstallProgress) {
			progress = append(progress, item.Stage)
		},
		VerifyProvenance: func(
			_ context.Context,
			manifestPath, bundlePath, identity, trustCachePath string,
		) error {
			manifest, err := os.ReadFile(manifestPath) // #nosec G304 -- installer-provided private test path.
			if err != nil {
				return err
			}
			bundle, err := os.ReadFile(bundlePath) // #nosec G304 -- installer-provided private test path.
			if err != nil {
				return err
			}
			if !bytes.Equal(manifest, release.manifest) ||
				!bytes.Equal(bundle, release.bundle) ||
				trustCachePath == "" {
				return errors.New("unexpected provenance inputs")
			}
			verifiedIdentity = identity
			return nil
		},
	}
	result, err := installer.Install(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != InstallStatusUpdated ||
		result.PreviousVersion != "1.0.0" ||
		result.CurrentVersion != "1.1.0" ||
		result.LatestVersion != "v1.1.0" ||
		result.Archive != "owa-bridge_1.1.0_linux_amd64.tar.gz" {
		t.Fatalf("Install() = %+v", result)
	}
	if verifiedIdentity != "https://github.com/nkiyohara/owa-bridge/.github/workflows/release.yml@refs/tags/v1.1.0" {
		t.Fatalf("verified identity = %q", verifiedIdentity)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, candidate) { // #nosec G304 -- test-controlled path.
		t.Fatalf("updated target = %q, %v", got, err)
	}
	if got, err := os.ReadFile(result.BackupPath); err != nil || !bytes.Equal(got, oldBinary) {
		t.Fatalf("rollback copy = %q, %v", got, err)
	}
	wantProgress := []InstallStage{
		InstallStageRelease,
		InstallStageDownload,
		InstallStageProvenance,
		InstallStageChecksum,
		InstallStageCandidate,
		InstallStageReplace,
	}
	if fmt.Sprint(progress) != fmt.Sprint(wantProgress) {
		t.Fatalf("progress = %v, want %v", progress, wantProgress)
	}
}

func TestInstallerFailsClosedBeforeReplacement(t *testing.T) {
	tests := []struct {
		name      string
		checksum  string
		verifyErr error
		want      string
	}{
		{
			name:      "provenance",
			verifyErr: errors.New("synthetic signature failure"),
			want:      "verify release provenance",
		},
		{
			name:     "checksum",
			checksum: strings.Repeat("0", sha256.Size*2),
			want:     "checksum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := filepath.Join(secureTempDir(t), "owa")
			oldBinary := []byte("#!/bin/sh\nprintf old\n")
			if err := os.WriteFile(target, oldBinary, 0o755); err != nil { // #nosec G306 -- synthetic executable fixture.
				t.Fatal(err)
			}
			release := newSyntheticUpdateRelease(
				t,
				syntheticCandidate(),
				test.checksum,
			)
			defer release.server.Close()
			installer := Installer{
				CurrentVersion: "1.0.0",
				Executable:     target,
				Endpoint:       release.server.URL + "/latest",
				Client:         release.server.Client(),
				GOOS:           "linux",
				GOARCH:         "amd64",
				VerifyProvenance: func(
					context.Context,
					string, string, string, string,
				) error {
					return test.verifyErr
				},
			}
			_, err := installer.Install(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Install() error = %v, want %q", err, test.want)
			}
			if got, readErr := os.ReadFile(target); readErr != nil || !bytes.Equal(got, oldBinary) { // #nosec G304 -- test-controlled path.
				t.Fatalf("target changed after failed verification: %q, %v", got, readErr)
			}
			if _, statErr := os.Stat(target + ".backup-1.0.0"); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rollback copy created before verification: %v", statErr)
			}
		})
	}
}

func TestInstallerAcceptsExactCorresyncWorkflowIdentity(t *testing.T) {
	target := filepath.Join(secureTempDir(t), "owa")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- synthetic executable fixture.
		t.Fatal(err)
	}
	release := newSyntheticUpdateRelease(
		t,
		syntheticCandidate(),
		"",
	)
	defer release.server.Close()

	var identities []string
	installer := Installer{
		CurrentVersion: "1.0.0",
		Executable:     target,
		Endpoint:       release.server.URL + "/latest",
		Client:         release.server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		VerifyProvenance: func(
			_ context.Context,
			_, _, identity, _ string,
		) error {
			identities = append(identities, identity)
			if strings.Contains(identity, "/nkiyohara/corresync/") {
				return nil
			}
			return errors.New("certificate identity mismatch")
		},
	}
	if _, err := installer.Install(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://github.com/nkiyohara/owa-bridge/.github/workflows/release.yml@refs/tags/v1.1.0",
		"https://github.com/nkiyohara/corresync/.github/workflows/release.yml@refs/tags/v1.1.0",
	}
	if fmt.Sprint(identities) != fmt.Sprint(want) {
		t.Fatalf("verified identities = %v, want %v", identities, want)
	}
}

func TestInstallerDoesNotDownloadWhenCurrent(t *testing.T) {
	target := filepath.Join(secureTempDir(t), "owa")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- synthetic executable fixture.
		t.Fatal(err)
	}
	release := newSyntheticUpdateRelease(
		t,
		syntheticCandidate(),
		"",
	)
	defer release.server.Close()
	installer := Installer{
		CurrentVersion: "1.1.0",
		Executable:     target,
		Endpoint:       release.server.URL + "/latest",
		Client:         release.server.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		VerifyProvenance: func(
			context.Context,
			string, string, string, string,
		) error {
			t.Fatal("current release attempted provenance verification")
			return nil
		},
	}
	result, err := installer.Install(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != InstallStatusCurrent || result.CurrentVersion != "1.1.0" ||
		result.BackupPath != "" || release.assetRequests != 0 {
		t.Fatalf("Install() = %+v; asset requests=%d", result, release.assetRequests)
	}
}

func TestInstallerRejectsSymlinkedExecutable(t *testing.T) {
	directory := secureTempDir(t)
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil { // #nosec G306 -- synthetic executable fixture.
		t.Fatal(err)
	}
	link := filepath.Join(directory, "owa")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := (Installer{
		CurrentVersion: "1.0.0",
		Executable:     link,
	}).Install(t.Context())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Install() error = %v", err)
	}
}

func secureTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- self-update fixtures require a private executable directory.
		t.Fatal(err)
	}
	return directory
}

func TestReleaseArtifactNamesCoverPublishedPlatforms(t *testing.T) {
	version, ok := parseVersion("v1.2.3")
	if !ok {
		t.Fatal("version did not parse")
	}
	tests := []struct {
		goos, goarch string
		archive      string
		executable   string
	}{
		{"linux", "amd64", "owa-bridge_1.2.3_linux_amd64.tar.gz", "owa"},
		{"darwin", "arm64", "owa-bridge_1.2.3_darwin_arm64.tar.gz", "owa"},
		{"windows", "amd64", "owa-bridge_1.2.3_windows_amd64.zip", "owa.exe"},
	}
	for _, test := range tests {
		archive, executable, err := releaseArtifactNames(version, test.goos, test.goarch)
		if err != nil || archive != test.archive || executable != test.executable {
			t.Errorf(
				"releaseArtifactNames(%q, %q) = %q, %q, %v",
				test.goos,
				test.goarch,
				archive,
				executable,
				err,
			)
		}
	}
}

type syntheticUpdateRelease struct {
	server        *httptest.Server
	manifest      []byte
	bundle        []byte
	archive       []byte
	assetRequests int
}

func newSyntheticUpdateRelease(
	t *testing.T,
	candidate []byte,
	checksumOverride string,
) *syntheticUpdateRelease {
	t.Helper()
	archive := tarCandidate(t, candidate)
	checksum := sha256.Sum256(archive)
	checksumText := hex.EncodeToString(checksum[:])
	if checksumOverride != "" {
		checksumText = checksumOverride
	}
	result := &syntheticUpdateRelease{
		archive:  archive,
		manifest: []byte(checksumText + "  owa-bridge_1.1.0_linux_amd64.tar.gz\n"),
		bundle:   []byte(`{"synthetic":"bundle"}`),
	}
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			assets := []releaseAsset{
				{
					Name:               "checksums.txt",
					BrowserDownloadURL: server.URL + "/assets/checksums.txt",
					Size:               int64(len(result.manifest)),
				},
				{
					Name:               "checksums.txt.sigstore.json",
					BrowserDownloadURL: server.URL + "/assets/checksums.txt.sigstore.json",
					Size:               int64(len(result.bundle)),
				},
				{
					Name:               "owa-bridge_1.1.0_linux_amd64.tar.gz",
					BrowserDownloadURL: server.URL + "/assets/archive",
					Size:               int64(len(result.archive)),
				},
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"tag_name":   "v1.1.0",
				"draft":      false,
				"prerelease": false,
				"assets":     assets,
			})
		case "/assets/checksums.txt":
			result.assetRequests++
			_, _ = writer.Write(result.manifest)
		case "/assets/checksums.txt.sigstore.json":
			result.assetRequests++
			_, _ = writer.Write(result.bundle)
		case "/assets/archive":
			result.assetRequests++
			_, _ = writer.Write(result.archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	result.server = server
	return result
}

func tarCandidate(t *testing.T, candidate []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(compressed)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "owa",
		Mode: 0o755,
		Size: int64(len(candidate)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(candidate); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func syntheticCandidate() []byte {
	payload := fmt.Sprintf(
		`{"version":%q,"os":%q,"arch":%q}`,
		"1.1.0",
		"linux",
		"amd64",
	)
	return []byte("#!/bin/sh\nif [ \"$1\" = version ]; then\nprintf '%s\\n' '" +
		payload + "'\nfi\n")
}
