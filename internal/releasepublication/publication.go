// Package releasepublication verifies the immutable release inputs and public
// MCP Registry result used by stable-only publication automation.
package releasepublication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

const (
	RegistryEndpoint      = "https://registry.modelcontextprotocol.io/v0.1/servers/io.github.nkiyohara%2Fcorresync/versions/latest"
	registryVersionPrefix = "https://registry.modelcontextprotocol.io/v0.1/servers/io.github.nkiyohara%2Fcorresync/versions/"
	registryMetaKey       = "io.modelcontextprotocol.registry/official"
	maxChecksumsBytes     = 4 << 20
	maxManifestBytes      = 1 << 20
	maxArtifactBytes      = 512 << 20
	maxRegistryBytes      = 1 << 20
)

var (
	stableTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	commitPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	checksumPattern  = regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._+-]{0,255})$`)
)

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type Candidate struct {
	Tag             string
	Version         string
	RegistryName    string
	PackageURL      string
	PackageSHA256   string
	ManifestSHA256  string
	ChecksumsSHA256 string
	Artifacts       []Artifact
}

type RegistryRecord struct {
	Status      string
	IsLatest    bool
	PublishedAt time.Time
}

type Evidence struct {
	SchemaVersion     int        `json:"schemaVersion"`
	SourceCommit      string     `json:"sourceCommit"`
	ReleaseTag        string     `json:"releaseTag"`
	Version           string     `json:"version"`
	PublicationTarget string     `json:"publicationTarget"`
	RegistryName      string     `json:"registryName"`
	PackageURL        string     `json:"packageUrl"`
	Artifacts         []Artifact `json:"artifacts"`
	RegistryStatus    string     `json:"registryStatus"`
	RegistryIsLatest  bool       `json:"registryIsLatest"`
	PublishedAt       string     `json:"publishedAt"`
	VerifiedAt        string     `json:"verifiedAt"`
}

type registryManifest struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Packages []struct {
		Identifier string `json:"identifier"`
		FileSHA256 string `json:"fileSha256"`
	} `json:"packages"`
}

type registryResponse struct {
	Server json.RawMessage                  `json:"server"`
	Meta   map[string]registryResponseState `json:"_meta"`
}

type registryResponseState struct {
	Status      string `json:"status"`
	IsLatest    bool   `json:"isLatest"`
	PublishedAt string `json:"publishedAt"`
}

// HTTPDoer is the minimal fixed-endpoint transport used for registry checks.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func VerifyAssets(root, tag string) (Candidate, error) {
	if len(tag) > 65 || !stableTagPattern.MatchString(tag) {
		return Candidate{}, fmt.Errorf("release tag %q is not stable vX.Y.Z", tag)
	}
	version := strings.TrimPrefix(tag, "v")
	bundleName := "corresync_" + version + ".mcpb"
	required := []string{
		bundleName,
		bundleName + ".cdx.json",
		bundleName + ".spdx.json",
		"server.json",
	}

	checksumData, err := readBoundedRegular(filepath.Join(root, "checksums.txt"), maxChecksumsBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("read checksum inventory: %w", err)
	}
	checksums, err := parseChecksums(checksumData)
	if err != nil {
		return Candidate{}, err
	}
	artifacts := make([]Artifact, 0, len(required)+1)
	for _, name := range required {
		expected, ok := checksums[name]
		if !ok {
			return Candidate{}, fmt.Errorf("checksum inventory does not contain %s", name)
		}
		actual, err := hashRegularFile(filepath.Join(root, name), maxArtifactBytes)
		if err != nil {
			return Candidate{}, fmt.Errorf("verify %s: %w", name, err)
		}
		if actual != expected {
			return Candidate{}, fmt.Errorf("%s SHA-256 is %s, want %s", name, actual, expected)
		}
		artifacts = append(artifacts, Artifact{Name: name, SHA256: actual})
	}
	checksumsHash := sha256.Sum256(checksumData)
	artifacts = append(artifacts, Artifact{Name: "checksums.txt", SHA256: hex.EncodeToString(checksumsHash[:])})
	signatureHash, err := hashRegularFile(filepath.Join(root, "checksums.txt.sigstore.json"), maxManifestBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("verify checksum signature bundle: %w", err)
	}
	artifacts = append(artifacts, Artifact{Name: "checksums.txt.sigstore.json", SHA256: signatureHash})
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })

	manifestData, err := readBoundedRegular(filepath.Join(root, "server.json"), maxManifestBytes)
	if err != nil {
		return Candidate{}, fmt.Errorf("read registry manifest: %w", err)
	}
	if err := integrationbundle.ValidateRegistryManifest(manifestData); err != nil {
		return Candidate{}, fmt.Errorf("validate registry manifest: %w", err)
	}
	var manifest registryManifest
	if err := decodeOne(manifestData, &manifest); err != nil {
		return Candidate{}, fmt.Errorf("decode registry manifest: %w", err)
	}
	if manifest.Version != version {
		return Candidate{}, fmt.Errorf("registry manifest version is %q, want %q", manifest.Version, version)
	}
	if len(manifest.Packages) != 1 || manifest.Packages[0].FileSHA256 != checksums[bundleName] {
		return Candidate{}, errors.New("registry manifest is not bound to the verified MCPB")
	}
	manifestHash := sha256.Sum256(manifestData)
	return Candidate{
		Tag:             tag,
		Version:         version,
		RegistryName:    manifest.Name,
		PackageURL:      manifest.Packages[0].Identifier,
		PackageSHA256:   manifest.Packages[0].FileSHA256,
		ManifestSHA256:  hex.EncodeToString(manifestHash[:]),
		ChecksumsSHA256: hex.EncodeToString(checksumsHash[:]),
		Artifacts:       artifacts,
	}, nil
}

func VerifyRegistry(ctx context.Context, client HTTPDoer, candidate Candidate) (RegistryRecord, error) {
	if client == nil {
		return RegistryRecord{}, errors.New("registry HTTP client is required")
	}
	data, status, err := fetchRegistry(ctx, client, RegistryEndpoint)
	if err != nil {
		return RegistryRecord{}, err
	}
	if status != http.StatusOK {
		return RegistryRecord{}, fmt.Errorf("production registry returned HTTP %d", status)
	}
	return verifyRegistryDocument(data, candidate, true)
}

// CheckExisting reports whether the candidate's immutable version already
// exists and exactly matches. Only a 404 is treated as not yet published.
func CheckExisting(ctx context.Context, client HTTPDoer, candidate Candidate) (bool, error) {
	if client == nil {
		return false, errors.New("registry HTTP client is required")
	}
	if len(candidate.Version) > 64 || !stableTagPattern.MatchString("v"+candidate.Version) {
		return false, errors.New("registry candidate version is not stable SemVer")
	}
	data, status, err := fetchRegistry(ctx, client, registryVersionPrefix+candidate.Version)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("production registry version lookup returned HTTP %d", status)
	}
	if _, err := verifyRegistryDocument(data, candidate, false); err != nil {
		return false, fmt.Errorf("immutable registry version differs from release candidate: %w", err)
	}
	return true, nil
}

func fetchRegistry(ctx context.Context, client HTTPDoer, endpoint string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create registry request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("query production registry: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, response.StatusCode, nil
	}
	data, err := readBounded(response.Body, maxRegistryBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("read production registry response: %w", err)
	}
	return data, response.StatusCode, nil
}

func verifyRegistryDocument(data []byte, candidate Candidate, requireLatest bool) (RegistryRecord, error) {
	var document registryResponse
	if err := decodeOne(data, &document); err != nil {
		return RegistryRecord{}, fmt.Errorf("decode production registry response: %w", err)
	}
	if err := integrationbundle.ValidateRegistryManifest(document.Server); err != nil {
		return RegistryRecord{}, fmt.Errorf("validate production registry record: %w", err)
	}
	var manifest registryManifest
	if err := decodeOne(document.Server, &manifest); err != nil {
		return RegistryRecord{}, fmt.Errorf("decode production registry record: %w", err)
	}
	if manifest.Name != candidate.RegistryName || manifest.Version != candidate.Version || len(manifest.Packages) != 1 ||
		manifest.Packages[0].Identifier != candidate.PackageURL || manifest.Packages[0].FileSHA256 != candidate.PackageSHA256 {
		return RegistryRecord{}, errors.New("production registry record does not match the verified release candidate")
	}
	state, ok := document.Meta[registryMetaKey]
	if !ok || state.Status != "active" || (requireLatest && !state.IsLatest) {
		return RegistryRecord{}, errors.New("production registry record has an unexpected publication state")
	}
	publishedAt, err := time.Parse(time.RFC3339Nano, state.PublishedAt)
	if err != nil {
		return RegistryRecord{}, fmt.Errorf("parse registry publication time: %w", err)
	}
	return RegistryRecord{Status: state.Status, IsLatest: state.IsLatest, PublishedAt: publishedAt.UTC()}, nil
}

func NewEvidence(candidate Candidate, record RegistryRecord, commit string, verifiedAt time.Time) (Evidence, error) {
	if !commitPattern.MatchString(commit) {
		return Evidence{}, errors.New("source commit must be a 40-character lower-case Git SHA")
	}
	if record.Status != "active" || !record.IsLatest || record.PublishedAt.IsZero() || verifiedAt.IsZero() {
		return Evidence{}, errors.New("registry record and verification time must describe an active latest publication")
	}
	return Evidence{
		SchemaVersion:     1,
		SourceCommit:      commit,
		ReleaseTag:        candidate.Tag,
		Version:           candidate.Version,
		PublicationTarget: "official-mcp-registry",
		RegistryName:      candidate.RegistryName,
		PackageURL:        candidate.PackageURL,
		Artifacts:         slices.Clone(candidate.Artifacts),
		RegistryStatus:    record.Status,
		RegistryIsLatest:  record.IsLatest,
		PublishedAt:       record.PublishedAt.UTC().Format(time.RFC3339Nano),
		VerifiedAt:        verifiedAt.UTC().Format(time.RFC3339Nano),
	}, nil
}

func WriteEvidence(path string, evidence Evidence) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evidence path is required")
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return fmt.Errorf("encode publication evidence: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".publication-evidence-*")
	if err != nil {
		return fmt.Errorf("create publication evidence: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit publication evidence: %w", err)
	}
	return nil
}

func parseChecksums(data []byte) (map[string]string, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 0 || len(lines) > 512 {
		return nil, errors.New("checksum inventory has an invalid entry count")
	}
	checksums := make(map[string]string, len(lines))
	for lineNumber, line := range lines {
		match := checksumPattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("checksum inventory line %d is malformed", lineNumber+1)
		}
		if _, duplicate := checksums[match[2]]; duplicate {
			return nil, fmt.Errorf("checksum inventory repeats %s", match[2])
		}
		checksums[match[2]] = match[1]
	}
	return checksums, nil
}

func readBoundedRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a bounded regular file", filepath.Base(path))
	}
	file, err := os.Open(path) // #nosec G304 -- caller confines names to the release asset directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readBounded(file, limit)
}

func hashRegularFile(path string, limit int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return "", fmt.Errorf("%s is not a bounded regular file", filepath.Base(path))
	}
	file, err := os.Open(path) // #nosec G304 -- caller confines names to the release asset directory.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("artifact exceeds the size bound")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds the size bound")
	}
	return data, nil
}

func decodeOne(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
