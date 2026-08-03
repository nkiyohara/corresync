package updatecheck

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	binaryupdate "github.com/minio/selfupdate"
)

const (
	maximumChecksumManifest = 64 << 10
	maximumSigstoreBundle   = 1 << 20
	maximumReleaseArchive   = 64 << 20
	maximumReleaseBinary    = 64 << 20
	maximumArchiveEntries   = 4096
	sigstoreOIDCIssuer      = "https://token.actions.githubusercontent.com"
	legacyRepository        = "nkiyohara/owa-bridge"
	currentRepository       = "nkiyohara/corresync"
)

// InstallStatus describes the result of a consented direct self-update.
type InstallStatus string

const (
	InstallStatusCurrent  InstallStatus = "current"
	InstallStatusRepaired InstallStatus = "repaired"
	InstallStatusUpdated  InstallStatus = "updated"
)

// InstallResult contains no provider or machine identity. BackupPath is
// included because installation is a consented local filesystem operation.
type InstallResult struct {
	Channel         Channel       `json:"channel"`
	Status          InstallStatus `json:"status"`
	PreviousVersion string        `json:"previousVersion,omitempty"`
	CurrentVersion  string        `json:"currentVersion"`
	LatestVersion   string        `json:"latestVersion"`
	ReleaseURL      string        `json:"releaseUrl"`
	Archive         string        `json:"archive,omitempty"`
	BackupPath      string        `json:"backupPath,omitempty"`
	CanonicalPath   string        `json:"canonicalPath,omitempty"`
}

// InstallStage identifies a completed self-update stage for human-facing
// progress output. Machine-readable callers receive only InstallResult.
type InstallStage string

const (
	InstallStageRelease    InstallStage = "release"
	InstallStageDownload   InstallStage = "download"
	InstallStageProvenance InstallStage = "provenance"
	InstallStageChecksum   InstallStage = "checksum"
	InstallStageCandidate  InstallStage = "candidate"
	InstallStageReplace    InstallStage = "replace"
)

// InstallProgress reports a completed stage without exposing temporary paths.
type InstallProgress struct {
	Stage  InstallStage
	Detail string
}

// Installer performs a consented self-update of one direct installation.
// Network, platform, and verification seams are injectable for deterministic
// synthetic tests.
type Installer struct {
	CurrentVersion   string
	Channel          Channel
	Executable       string
	TrustCachePath   string
	Endpoint         string
	Client           *http.Client
	GOOS             string
	GOARCH           string
	Progress         func(InstallProgress)
	VerifyProvenance func(
		context.Context,
		string,
		string,
		string,
		string,
	) error
}

type installReleaseResponse struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type releaseArtifact struct {
	Archive    string
	Executable string
}

// Install fetches the latest release in the selected channel, verifies its
// signed checksum inventory and candidate metadata, then replaces a direct
// executable with rollback support. It never installs a downgrade.
func (installer Installer) Install(ctx context.Context) (InstallResult, error) {
	channel, err := normalizeChannel(installer.Channel)
	if err != nil {
		return InstallResult{}, err
	}
	current, ok := parseVersion(installer.CurrentVersion)
	if !ok {
		return InstallResult{}, errors.New("development builds cannot self-update")
	}
	executable, info, err := installer.validateExecutable(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	goos, goarch := installer.platform()
	canonicalPath, installCanonical, err := canonicalCommandTarget(executable, goos)
	if err != nil {
		return InstallResult{}, err
	}
	release, latest, endpointURL, err := installer.fetchRelease(ctx)
	if err != nil {
		return InstallResult{}, err
	}
	releaseURL := canonicalReleaseURL(latest)
	comparison := current.Compare(latest)
	repairOnly := comparison == 0 && installCanonical
	if comparison > 0 || (comparison == 0 && !repairOnly) {
		return InstallResult{
			Channel:        channel,
			Status:         InstallStatusCurrent,
			CurrentVersion: strings.TrimPrefix(current.String(), "v"),
			LatestVersion:  latest.String(),
			ReleaseURL:     releaseURL,
		}, nil
	}
	installer.progress(InstallStageRelease, "Found "+string(channel)+" release "+latest.String())

	artifactCandidates, err := releaseArtifactCandidates(latest, goos, goarch)
	if err != nil {
		return InstallResult{}, err
	}
	artifact, err := selectReleaseArtifact(release.Assets, artifactCandidates)
	if err != nil {
		return InstallResult{}, err
	}
	archiveName := artifact.Archive
	executableName := artifact.Executable
	assets, err := exactReleaseAssets(
		release.Assets,
		archiveName,
		"checksums.txt",
		"checksums.txt.sigstore.json",
	)
	if err != nil {
		return InstallResult{}, err
	}

	temporaryDirectory, err := os.MkdirTemp("", "corresync-update-*")
	if err != nil {
		return InstallResult{}, fmt.Errorf("create private update directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporaryDirectory) }()
	if err := os.Chmod(temporaryDirectory, 0o700); err != nil { // #nosec G302 -- private directory requires owner execute.
		return InstallResult{}, fmt.Errorf("protect private update directory: %w", err)
	}
	manifestPath := filepath.Join(temporaryDirectory, "checksums.txt")
	bundlePath := filepath.Join(temporaryDirectory, "checksums.txt.sigstore.json")
	archivePath := filepath.Join(temporaryDirectory, filepath.Base(archiveName))

	client := installer.Client
	if client == nil {
		client = &http.Client{}
	}
	downloads := []struct {
		asset releaseAsset
		path  string
		limit int64
	}{
		{assets["checksums.txt"], manifestPath, maximumChecksumManifest},
		{assets["checksums.txt.sigstore.json"], bundlePath, maximumSigstoreBundle},
		{assets[archiveName], archivePath, maximumReleaseArchive},
	}
	for _, download := range downloads {
		if err := downloadAsset(ctx, client, endpointURL, download.asset, download.path, download.limit); err != nil {
			return InstallResult{}, err
		}
	}
	installer.progress(InstallStageDownload, "Downloaded the signed release inventory and "+archiveName)

	verifyProvenance := installer.VerifyProvenance
	if verifyProvenance == nil {
		verifyProvenance = VerifyProvenance
	}
	if err := verifyReleaseProvenance(
		ctx,
		manifestPath,
		bundlePath,
		installer.TrustCachePath,
		latest,
		verifyProvenance,
	); err != nil {
		return InstallResult{}, fmt.Errorf("verify release provenance: %w", err)
	}
	installer.progress(InstallStageProvenance, "Verified the GitHub Actions Sigstore identity")

	expectedArchiveChecksum, err := checksumFromManifest(manifestPath, archiveName)
	if err != nil {
		return InstallResult{}, err
	}
	if err := verifyFileChecksum(archivePath, expectedArchiveChecksum); err != nil {
		return InstallResult{}, fmt.Errorf("verify release archive checksum: %w", err)
	}
	installer.progress(InstallStageChecksum, "Verified the archive SHA-256 checksum")

	candidatePath := filepath.Join(temporaryDirectory, executableName)
	if err := extractReleaseBinary(archivePath, candidatePath, executableName, goos); err != nil {
		return InstallResult{}, err
	}
	if err := validateCandidate(ctx, candidatePath, latest, goos, goarch); err != nil {
		return InstallResult{}, err
	}
	installer.progress(InstallStageCandidate, "Validated candidate version, operating system, and architecture")

	backupPath := ""
	stagingPath := ""
	if !repairOnly {
		backupPath = executable + ".backup-" + strings.TrimPrefix(current.String(), "v")
		if _, err := os.Lstat(backupPath); err == nil {
			return InstallResult{}, fmt.Errorf("refusing to replace existing rollback copy %q", backupPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return InstallResult{}, fmt.Errorf("inspect rollback path: %w", err)
		}
		stagingPath = filepath.Join(filepath.Dir(executable), "."+filepath.Base(executable)+".new")
		if _, err := os.Lstat(stagingPath); err == nil {
			return InstallResult{}, fmt.Errorf("refusing to replace existing update staging file %q", stagingPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return InstallResult{}, fmt.Errorf("inspect update staging path: %w", err)
		}
	}
	canonicalStagingPath := ""
	if installCanonical {
		canonicalStagingPath = filepath.Join(
			filepath.Dir(canonicalPath),
			"."+filepath.Base(canonicalPath)+".new",
		)
		if canonicalStagingPath != stagingPath {
			if _, err := os.Lstat(canonicalStagingPath); err == nil {
				return InstallResult{}, fmt.Errorf(
					"refusing to replace existing canonical command staging file %q",
					canonicalStagingPath,
				)
			} else if !errors.Is(err, os.ErrNotExist) {
				return InstallResult{}, fmt.Errorf(
					"inspect canonical command staging path: %w",
					err,
				)
			}
		}
	}
	candidate, err := os.Open(candidatePath) // #nosec G304 -- private verified update artifact.
	if err != nil {
		return InstallResult{}, fmt.Errorf("open verified candidate: %w", err)
	}
	defer func() { _ = candidate.Close() }()
	candidateChecksum, err := fileChecksum(candidate)
	if err != nil {
		return InstallResult{}, fmt.Errorf("hash verified candidate: %w", err)
	}
	if _, err := candidate.Seek(0, io.SeekStart); err != nil {
		return InstallResult{}, fmt.Errorf("rewind verified candidate: %w", err)
	}
	options := binaryupdate.Options{
		TargetPath:  executable,
		TargetMode:  info.Mode().Perm(),
		Checksum:    candidateChecksum,
		OldSavePath: backupPath,
	}
	if err := checkUpdatePermissions(executable, info.Mode().Perm()); err != nil {
		return InstallResult{}, fmt.Errorf("self-update requires write access to %q: %w", filepath.Dir(executable), err)
	}
	if installCanonical {
		if err := installCanonicalCommand(
			candidatePath,
			canonicalPath,
			canonicalStagingPath,
			info.Mode().Perm(),
		); err != nil {
			return InstallResult{}, fmt.Errorf("install primary corr command: %w", err)
		}
		if repairOnly {
			installer.progress(
				InstallStageReplace,
				"Installed the missing primary corr command for "+latest.String(),
			)
			return InstallResult{
				Channel:        channel,
				Status:         InstallStatusRepaired,
				CurrentVersion: strings.TrimPrefix(latest.String(), "v"),
				LatestVersion:  latest.String(),
				ReleaseURL:     releaseURL,
				Archive:        archiveName,
				CanonicalPath:  canonicalPath,
			}, nil
		}
	}
	if err := binaryupdate.Apply(candidate, options); err != nil {
		if rollbackErr := binaryupdate.RollbackError(err); rollbackErr != nil {
			return InstallResult{}, fmt.Errorf(
				"replace executable: %w; rollback also failed: %w; recover from %q",
				err,
				rollbackErr,
				backupPath,
			)
		}
		if installCanonical {
			if removeErr := os.Remove(canonicalPath); removeErr != nil {
				return InstallResult{}, fmt.Errorf(
					"replace executable; original was restored: %w; remove newly installed corr command: %w",
					err,
					removeErr,
				)
			}
		}
		return InstallResult{}, fmt.Errorf("replace executable; original was restored: %w", err)
	}
	installer.progress(InstallStageReplace, "Installed "+latest.String()+" and preserved a rollback copy")

	return InstallResult{
		Channel:         channel,
		Status:          InstallStatusUpdated,
		PreviousVersion: strings.TrimPrefix(current.String(), "v"),
		CurrentVersion:  strings.TrimPrefix(latest.String(), "v"),
		LatestVersion:   latest.String(),
		ReleaseURL:      releaseURL,
		Archive:         archiveName,
		BackupPath:      backupPath,
		CanonicalPath:   canonicalPath,
	}, nil
}

func canonicalCommandTarget(executable, goos string) (string, bool, error) {
	canonicalName := "corr"
	legacyNames := []string{"corresync", "owa"}
	if goos == "windows" {
		canonicalName += ".exe"
		for index := range legacyNames {
			legacyNames[index] += ".exe"
		}
	}
	base := filepath.Base(executable)
	equalName := func(left, right string) bool {
		if goos == "windows" {
			return strings.EqualFold(left, right)
		}
		return left == right
	}
	if equalName(base, canonicalName) {
		return "", false, nil
	}
	legacy := false
	for _, name := range legacyNames {
		if equalName(base, name) {
			legacy = true
			break
		}
	}
	if !legacy {
		return "", false, nil
	}
	target := filepath.Join(filepath.Dir(executable), canonicalName)
	if _, err := os.Lstat(target); err == nil {
		return "", false, fmt.Errorf(
			"primary corr command already exists at %q; run %q instead",
			target,
			target+" update",
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect primary corr command path: %w", err)
	}
	return target, true, nil
}

func installCanonicalCommand(candidatePath, target, stagingPath string, mode os.FileMode) error {
	source, err := os.Open(candidatePath) // #nosec G304 -- private verified release candidate.
	if err != nil {
		return fmt.Errorf("open verified candidate: %w", err)
	}
	defer func() { _ = source.Close() }()

	staging, err := os.OpenFile(
		stagingPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		mode,
	) // #nosec G304 -- validated user-owned executable directory and fixed sibling filename.
	if err != nil {
		return fmt.Errorf("create primary command staging file: %w", err)
	}
	keepStaging := true
	defer func() {
		_ = staging.Close()
		if keepStaging {
			_ = os.Remove(stagingPath)
		}
	}()
	written, err := io.Copy(staging, io.LimitReader(source, maximumReleaseBinary+1))
	if err != nil {
		return fmt.Errorf("copy verified primary command: %w", err)
	}
	if written > maximumReleaseBinary {
		return errors.New("verified primary command exceeds size limit")
	}
	if err := staging.Sync(); err != nil {
		return fmt.Errorf("sync primary command staging file: %w", err)
	}
	if err := staging.Close(); err != nil {
		return fmt.Errorf("close primary command staging file: %w", err)
	}
	if err := os.Chmod(stagingPath, mode); err != nil {
		return fmt.Errorf("set primary command permissions: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("refusing to replace primary corr command created concurrently at %q", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect primary corr command before install: %w", err)
	}
	if err := os.Rename(stagingPath, target); err != nil {
		return fmt.Errorf("install primary corr command: %w", err)
	}
	keepStaging = false
	return nil
}

func verifyReleaseProvenance(
	ctx context.Context,
	manifestPath, bundlePath, trustCachePath string,
	version semanticVersion,
	verify func(context.Context, string, string, string, string) error,
) error {
	repositories := []string{currentRepository, legacyRepository}
	var verificationErrors []error
	for _, repository := range repositories {
		identity := "https://github.com/" + repository +
			"/.github/workflows/release.yml@refs/tags/" + version.String()
		err := verify(
			ctx,
			manifestPath,
			bundlePath,
			identity,
			trustCachePath,
		)
		if err != nil {
			verificationErrors = append(
				verificationErrors,
				fmt.Errorf("%s: %w", repository, err),
			)
			continue
		}
		return nil
	}
	return errors.Join(verificationErrors...)
}

func (installer Installer) validateExecutable(ctx context.Context) (string, os.FileInfo, error) {
	executable := installer.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return "", nil, fmt.Errorf("resolve running executable: %w", err)
		}
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "", nil, fmt.Errorf("resolve executable path: %w", err)
	}
	executable = filepath.Clean(absolute)
	if DetectInstallationContext(ctx, executable) != InstallDirect {
		return "", nil, errors.New("self-update is restricted to direct installations")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return "", nil, fmt.Errorf("inspect running executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("direct self-update refuses a symlinked executable")
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("running executable is not a regular file")
	}
	directoryInfo, err := os.Stat(filepath.Dir(executable))
	if err != nil {
		return "", nil, fmt.Errorf("inspect executable directory: %w", err)
	}
	goos, _ := installer.platform()
	if !directoryInfo.IsDir() ||
		(goos != "windows" && directoryInfo.Mode().Perm()&0o022 != 0) {
		return "", nil, errors.New("direct self-update requires an executable directory that is not group- or world-writable")
	}
	return executable, info, nil
}

func (installer Installer) fetchRelease(
	ctx context.Context,
) (installReleaseResponse, semanticVersion, *url.URL, error) {
	endpoint := installer.Endpoint
	if endpoint == "" {
		channel, channelErr := normalizeChannel(installer.Channel)
		if channelErr != nil {
			return installReleaseResponse{}, semanticVersion{}, nil, channelErr
		}
		if channel == ChannelPreview {
			endpoint = DefaultPreviewEndpoint
		} else {
			endpoint = DefaultEndpoint
		}
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Scheme != "https" || endpointURL.Host == "" {
		return installReleaseResponse{}, semanticVersion{}, nil, errors.New("release endpoint must be an absolute HTTPS URL")
	}
	client := installer.Client
	if client == nil {
		client = http.DefaultClient
	}
	channel, err := normalizeChannel(installer.Channel)
	if err != nil {
		return installReleaseResponse{}, semanticVersion{}, nil, err
	}
	if channel == ChannelStable {
		data, fetchErr := fetchReleaseMetadata(
			ctx, client, endpoint, installer.CurrentVersion, true,
		)
		if fetchErr != nil {
			return installReleaseResponse{}, semanticVersion{}, nil, fetchErr
		}
		var release installReleaseResponse
		if err := json.Unmarshal(data, &release); err != nil {
			return installReleaseResponse{}, semanticVersion{}, nil, fmt.Errorf("decode release metadata: %w", err)
		}
		latest, ok := eligibleInstallRelease(release, channel)
		if !ok {
			return installReleaseResponse{}, semanticVersion{}, nil, errors.New("latest release is not a stable semantic version")
		}
		return release, latest, endpointURL, nil
	}
	pageEndpoints, pageSize, err := previewMetadataEndpoints(endpoint)
	if err != nil {
		return installReleaseResponse{}, semanticVersion{}, nil, err
	}
	var selected installReleaseResponse
	var latest semanticVersion
	found := false
	for _, pageEndpoint := range pageEndpoints {
		data, fetchErr := fetchReleaseMetadata(
			ctx, client, pageEndpoint, installer.CurrentVersion, true,
		)
		if fetchErr != nil {
			return installReleaseResponse{}, semanticVersion{}, nil, fetchErr
		}
		var releases []installReleaseResponse
		if decodeErr := json.Unmarshal(data, &releases); decodeErr != nil {
			return installReleaseResponse{}, semanticVersion{}, nil, fmt.Errorf(
				"decode preview release metadata: %w",
				decodeErr,
			)
		}
		candidateRelease, candidateVersion, ok := selectLatestInstallRelease(releases, channel)
		if ok && (!found || candidateVersion.Compare(latest) > 0) {
			selected = candidateRelease
			latest = candidateVersion
			found = true
		}
		if pageSize == 0 || len(releases) < pageSize {
			break
		}
	}
	if !found {
		return installReleaseResponse{}, semanticVersion{}, nil, errors.New("no eligible preview release was found")
	}
	return selected, latest, endpointURL, nil
}

func eligibleInstallRelease(
	release installReleaseResponse,
	channel Channel,
) (semanticVersion, bool) {
	return eligibleVersionTag(release.TagName, release.Draft, release.Prerelease, channel)
}

func selectLatestInstallRelease(
	releases []installReleaseResponse,
	channel Channel,
) (installReleaseResponse, semanticVersion, bool) {
	var selected installReleaseResponse
	var latest semanticVersion
	found := false
	for _, release := range releases {
		candidate, ok := eligibleInstallRelease(release, channel)
		if !ok {
			continue
		}
		if !found || candidate.Compare(latest) > 0 {
			selected = release
			latest = candidate
			found = true
		}
	}
	return selected, latest, found
}

func (installer Installer) platform() (string, string) {
	goos := installer.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := installer.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (installer Installer) progress(stage InstallStage, detail string) {
	if installer.Progress != nil {
		installer.Progress(InstallProgress{Stage: stage, Detail: detail})
	}
}

func releaseArtifactCandidates(
	version semanticVersion,
	goos, goarch string,
) ([]releaseArtifact, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return nil, fmt.Errorf("direct self-update does not support architecture %q", goarch)
	}
	versionText := strings.TrimPrefix(version.String(), "v")
	switch goos {
	case "linux", "darwin":
		return []releaseArtifact{
			{
				Archive:    fmt.Sprintf("corresync_%s_%s_%s.tar.gz", versionText, goos, goarch),
				Executable: "corr",
			},
			{
				Archive:    fmt.Sprintf("owa-bridge_%s_%s_%s.tar.gz", versionText, goos, goarch),
				Executable: "owa",
			},
		}, nil
	case "windows":
		return []releaseArtifact{
			{
				Archive:    fmt.Sprintf("corresync_%s_windows_%s.zip", versionText, goarch),
				Executable: "corr.exe",
			},
			{
				Archive:    fmt.Sprintf("owa-bridge_%s_windows_%s.zip", versionText, goarch),
				Executable: "owa.exe",
			},
		}, nil
	default:
		return nil, fmt.Errorf("direct self-update does not support operating system %q", goos)
	}
}

func selectReleaseArtifact(
	assets []releaseAsset,
	candidates []releaseArtifact,
) (releaseArtifact, error) {
	counts := make(map[string]int, len(candidates))
	for _, asset := range assets {
		counts[asset.Name]++
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.Archive)
		switch counts[candidate.Archive] {
		case 1:
			return candidate, nil
		case 0:
			continue
		default:
			return releaseArtifact{}, fmt.Errorf(
				"release contains duplicate asset %q",
				candidate.Archive,
			)
		}
	}
	return releaseArtifact{}, fmt.Errorf(
		"release is missing a supported archive; expected one of %q",
		names,
	)
}

func exactReleaseAssets(assets []releaseAsset, names ...string) (map[string]releaseAsset, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	found := make(map[string]releaseAsset, len(names))
	for _, asset := range assets {
		if _, ok := wanted[asset.Name]; !ok {
			continue
		}
		if _, duplicate := found[asset.Name]; duplicate {
			return nil, fmt.Errorf("release contains duplicate asset %q", asset.Name)
		}
		if asset.Size < 1 || asset.BrowserDownloadURL == "" {
			return nil, fmt.Errorf("release asset %q has incomplete metadata", asset.Name)
		}
		found[asset.Name] = asset
	}
	for _, name := range names {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("release is missing required asset %q", name)
		}
	}
	return found, nil
}

func downloadAsset(
	ctx context.Context,
	client *http.Client,
	endpoint *url.URL,
	asset releaseAsset,
	destination string,
	limit int64,
) error {
	if asset.Size > limit {
		return fmt.Errorf("release asset %q exceeds size limit", asset.Name)
	}
	downloadURL, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || downloadURL.Scheme != "https" || downloadURL.Host == "" {
		return fmt.Errorf("release asset %q does not use an absolute HTTPS URL", asset.Name)
	}
	if !allowedAssetHost(downloadURL, endpoint) {
		return fmt.Errorf("release asset %q uses untrusted host %q", asset.Name, downloadURL.Hostname())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create release asset request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "corresync-self-update")
	response, err := restrictedHTTPClient(client, endpoint).Do(request)
	if err != nil {
		return fmt.Errorf("download release asset %q: %w", asset.Name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
		return fmt.Errorf("download release asset %q: HTTP %d", asset.Name, response.StatusCode)
	}
	if response.Request == nil || response.Request.URL == nil ||
		response.Request.URL.Scheme != "https" ||
		!allowedAssetHost(response.Request.URL, endpoint) {
		return fmt.Errorf("release asset %q redirected to an untrusted URL", asset.Name)
	}
	if response.ContentLength > limit {
		return fmt.Errorf("release asset %q exceeds size limit", asset.Name)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- destination is a fixed name inside the private update directory.
	if err != nil {
		return fmt.Errorf("create private release asset %q: %w", asset.Name, err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download release asset %q: %w", asset.Name, copyErr)
	}
	if written > limit {
		return fmt.Errorf("release asset %q exceeds size limit", asset.Name)
	}
	if written != asset.Size {
		return fmt.Errorf(
			"release asset %q size mismatch: metadata=%d downloaded=%d",
			asset.Name,
			asset.Size,
			written,
		)
	}
	if syncErr != nil {
		return fmt.Errorf("sync release asset %q: %w", asset.Name, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close release asset %q: %w", asset.Name, closeErr)
	}
	return nil
}

func restrictedHTTPClient(client *http.Client, endpoint *url.URL) *http.Client {
	restricted := *client
	originalRedirect := client.CheckRedirect
	restricted.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL == nil || request.URL.Scheme != "https" ||
			!allowedAssetHost(request.URL, endpoint) {
			return errors.New("release request redirected to an untrusted URL")
		}
		if originalRedirect != nil {
			return originalRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many release redirects")
		}
		return nil
	}
	return &restricted
}

func allowedAssetHost(candidate, endpoint *url.URL) bool {
	host := strings.ToLower(candidate.Hostname())
	switch host {
	case strings.ToLower(endpoint.Hostname()),
		"github.com",
		"release-assets.githubusercontent.com",
		"objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func checksumFromManifest(path, assetName string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- private bounded release asset.
	if err != nil {
		return nil, fmt.Errorf("read checksum manifest: %w", err)
	}
	if len(data) > maximumChecksumManifest {
		return nil, errors.New("checksum manifest exceeds size limit")
	}
	var checksum []byte
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("checksum manifest contains a malformed line")
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != assetName {
			continue
		}
		if checksum != nil {
			return nil, fmt.Errorf("checksum manifest contains duplicate entry for %q", assetName)
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("checksum manifest has an invalid SHA-256 for %q", assetName)
		}
		checksum = decoded
	}
	if checksum == nil {
		return nil, fmt.Errorf("checksum manifest is missing %q", assetName)
	}
	return checksum, nil
}

func verifyFileChecksum(path string, expected []byte) error {
	file, err := os.Open(path) // #nosec G304 -- private bounded release asset.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	actual, err := fileChecksum(file)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("SHA-256 checksum does not match the signed manifest")
	}
	return nil
}

func fileChecksum(reader io.Reader) ([]byte, error) {
	digest := sha256.New()
	if _, err := io.Copy(digest, reader); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}

func checkUpdatePermissions(executable string, mode os.FileMode) error {
	probe, err := os.CreateTemp(filepath.Dir(executable), "."+filepath.Base(executable)+".permission-*")
	if err != nil {
		return err
	}
	path := probe.Name()
	defer func() { _ = os.Remove(path) }()
	if err := probe.Chmod(mode); err != nil {
		_ = probe.Close()
		return err
	}
	if err := probe.Close(); err != nil {
		return err
	}
	return nil
}

func extractReleaseBinary(archivePath, destination, executableName, goos string) error {
	switch goos {
	case "windows":
		return extractZIPBinary(archivePath, destination, executableName)
	case "linux", "darwin":
		return extractTarBinary(archivePath, destination, executableName)
	default:
		return fmt.Errorf("extract release for unsupported operating system %q", goos)
	}
}

func extractTarBinary(archivePath, destination, executableName string) error {
	archive, err := os.Open(archivePath) // #nosec G304 -- private verified release asset.
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer func() { _ = archive.Close() }()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open release gzip stream: %w", err)
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	found := false
	for entries := 0; ; entries++ {
		if entries >= maximumArchiveEntries {
			return errors.New("release archive contains too many entries")
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != executableName {
			continue
		}
		if found {
			return fmt.Errorf("release archive contains duplicate %q", executableName)
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maximumReleaseBinary {
			return fmt.Errorf("release archive %q is not a bounded regular file", executableName)
		}
		if err := writeCandidate(destination, io.LimitReader(reader, header.Size), header.Size); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("release archive is missing %q", executableName)
	}
	return nil
}

func extractZIPBinary(archivePath, destination, executableName string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open release zip: %w", err)
	}
	defer func() { _ = archive.Close() }()
	if len(archive.File) > maximumArchiveEntries {
		return errors.New("release archive contains too many entries")
	}
	found := false
	for _, item := range archive.File {
		if item.Name != executableName {
			continue
		}
		if found {
			return fmt.Errorf("release archive contains duplicate %q", executableName)
		}
		if !item.Mode().IsRegular() || item.UncompressedSize64 < 1 ||
			item.UncompressedSize64 > maximumReleaseBinary {
			return fmt.Errorf("release archive %q is not a bounded regular file", executableName)
		}
		file, err := item.Open()
		if err != nil {
			return fmt.Errorf("open release archive candidate: %w", err)
		}
		writeErr := writeCandidate(destination, file, int64(item.UncompressedSize64))
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return fmt.Errorf("close release archive candidate: %w", closeErr)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("release archive is missing %q", executableName)
	}
	return nil
}

func writeCandidate(destination string, source io.Reader, size int64) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700) // #nosec G302,G304 -- verified candidate must be executable inside the private update directory.
	if err != nil {
		return fmt.Errorf("create private update candidate: %w", err)
	}
	written, copyErr := io.Copy(file, source)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("extract update candidate: %w", copyErr)
	}
	if written != size {
		return fmt.Errorf("update candidate size mismatch: archive=%d extracted=%d", size, written)
	}
	if syncErr != nil {
		return fmt.Errorf("sync update candidate: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close update candidate: %w", closeErr)
	}
	return nil
}

func validateCandidate(
	ctx context.Context,
	path string,
	expected semanticVersion,
	goos, goarch string,
) error {
	command := exec.CommandContext(ctx, path, "version", "--json") // #nosec G204 -- exact binary extracted from the verified release.
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("run verified candidate version check: %w", err)
	}
	if len(output) > 8<<10 {
		return errors.New("candidate version output exceeds size limit")
	}
	var information struct {
		Version string `json:"version"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&information); err != nil {
		return fmt.Errorf("decode candidate version output: %w", err)
	}
	candidate, ok := parseVersion(information.Version)
	if !ok || candidate.Compare(expected) != 0 {
		return fmt.Errorf(
			"candidate version %q does not match release %q",
			information.Version,
			expected.String(),
		)
	}
	if information.OS != goos || information.Arch != goarch {
		return fmt.Errorf(
			"candidate platform %s/%s does not match running platform %s/%s",
			information.OS,
			information.Arch,
			goos,
			goarch,
		)
	}
	return nil
}

func canonicalReleaseURL(version semanticVersion) string {
	return "https://github.com/nkiyohara/corresync/releases/tag/" +
		url.PathEscape(version.String())
}
