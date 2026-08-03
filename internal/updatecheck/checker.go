// Package updatecheck performs a quiet, cached check for signed release channels.
package updatecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	// DefaultEndpoint returns the latest published non-prerelease GitHub release.
	DefaultEndpoint = "https://api.github.com/repos/nkiyohara/corresync/releases/latest"
	// DefaultPreviewEndpoint returns recent published releases so the highest
	// stable or prerelease semantic version can be selected deterministically.
	// Preview discovery fetches at most four independently bounded pages.
	DefaultPreviewEndpoint = "https://api.github.com/repos/nkiyohara/corresync/releases?per_page=5"
	cacheFormat            = 2
	cacheLifetime          = 24 * time.Hour
	maximumBody            = 1 << 20
	maximumCache           = 8 << 10
	maximumPreviewReleases = 20
)

// Channel selects a signed public release stream.
type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelPreview Channel = "preview"
)

func normalizeChannel(channel Channel) (Channel, error) {
	if channel == "" {
		return ChannelStable, nil
	}
	switch channel {
	case ChannelStable, ChannelPreview:
		return channel, nil
	default:
		return "", fmt.Errorf("unsupported update channel %q", channel)
	}
}

// Status describes the relationship between the running binary and the latest
// release in its channel without initiating an update.
type Status string

const (
	StatusCurrent     Status = "current"
	StatusAvailable   Status = "available"
	StatusDevelopment Status = "development"
	StatusUnavailable Status = "unavailable"
)

// ErrUnavailable means public release metadata could not be checked. Callers
// may report this for an explicit check, but automatic checks must ignore it.
var ErrUnavailable = errors.New("release metadata is unavailable")

// Result is safe for human or machine-readable output. It contains no account,
// tenant, mailbox, configuration, or machine identifier.
type Result struct {
	Channel         Channel `json:"channel"`
	Status          Status  `json:"status"`
	CurrentVersion  string  `json:"currentVersion"`
	LatestVersion   string  `json:"latestVersion,omitempty"`
	UpdateAvailable bool    `json:"updateAvailable"`
	ReleaseURL      string  `json:"releaseUrl,omitempty"`
	CheckedAt       string  `json:"checkedAt,omitempty"`
	Cached          bool    `json:"cached"`
}

// Checker fetches and caches the latest public release in one channel.
// Dependencies are explicit so all network, clock, and cache behavior is
// deterministic in tests.
type Checker struct {
	CurrentVersion string
	Channel        Channel
	CachePath      string
	Endpoint       string
	Client         *http.Client
	Now            func() time.Time
	Force          bool
}

type releaseResponse struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type cacheRecord struct {
	Format        int       `json:"format"`
	Channel       Channel   `json:"channel"`
	CheckedAt     time.Time `json:"checkedAt"`
	LatestVersion string    `json:"latestVersion,omitempty"`
	Unavailable   bool      `json:"unavailable,omitempty"`
}

// Check returns cached status when it is less than 24 hours old. A failed
// fetch is cached too, preventing unavailable endpoints from being retried by
// every command.
func (checker Checker) Check(ctx context.Context) (Result, error) {
	channel, err := normalizeChannel(checker.Channel)
	if err != nil {
		return Result{}, err
	}
	current, currentOK := parseVersion(checker.CurrentVersion)
	if !currentOK {
		return Result{
			Channel:        channel,
			Status:         StatusDevelopment,
			CurrentVersion: checker.CurrentVersion,
		}, nil
	}
	if checker.CachePath == "" {
		return Result{}, errors.New("update cache path is required")
	}
	now := time.Now().UTC()
	if checker.Now != nil {
		now = checker.Now().UTC()
	}
	if !checker.Force {
		if cached, ok := loadFreshCache(checker.CachePath, now, channel); ok {
			result, err := resultFromRecord(checker.CurrentVersion, current, cached, true)
			return result, err
		}
	}
	releaseLock, acquired := acquireCheckLock(checker.CachePath, now)
	if !acquired {
		return Result{
			Channel:        channel,
			Status:         StatusUnavailable,
			CurrentVersion: checker.CurrentVersion,
		}, ErrUnavailable
	}
	defer releaseLock()
	// A separate process may have populated the cache immediately before this
	// process acquired the lock. An explicit forced check intentionally skips
	// it and refreshes public metadata.
	if !checker.Force {
		if cached, ok := loadFreshCache(checker.CachePath, now, channel); ok {
			result, err := resultFromRecord(checker.CurrentVersion, current, cached, true)
			return result, err
		}
	}

	record := cacheRecord{Format: cacheFormat, Channel: channel, CheckedAt: now, Unavailable: true}
	// Publish an in-progress failure sentinel before using the network. Other
	// processes then remain quiet instead of starting a concurrent check.
	if err := writeCache(checker.CachePath, record); err != nil {
		return Result{}, fmt.Errorf("write update cache sentinel: %w", err)
	}
	latest, err := checker.fetchLatest(ctx)
	if err != nil {
		return Result{
			Channel:        channel,
			Status:         StatusUnavailable,
			CurrentVersion: checker.CurrentVersion,
			CheckedAt:      now.Format(time.RFC3339),
		}, errors.Join(ErrUnavailable, err)
	}
	record.LatestVersion = latest
	record.Unavailable = false
	if err := writeCache(checker.CachePath, record); err != nil {
		return Result{}, fmt.Errorf("write update cache: %w", err)
	}
	return resultFromRecord(checker.CurrentVersion, current, record, false)
}

func acquireCheckLock(cachePath string, now time.Time) (func(), bool) {
	directory := filepath.Dir(cachePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return func() {}, false
	}
	lockPath := cachePath + ".lock"
	open := func() (*os.File, error) {
		return os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- fixed private cache lock.
	}
	lock, err := open()
	if errors.Is(err, os.ErrExist) {
		if info, statErr := os.Lstat(lockPath); statErr == nil && info.Mode().IsRegular() &&
			now.Sub(info.ModTime()) > time.Minute {
			_ = os.Remove(lockPath)
			lock, err = open()
		}
	}
	if err != nil {
		return func() {}, false
	}
	return func() {
		_ = lock.Close()
		_ = os.Remove(lockPath)
	}, true
}

func (checker Checker) fetchLatest(ctx context.Context) (string, error) {
	channel, err := normalizeChannel(checker.Channel)
	if err != nil {
		return "", err
	}
	endpoint := checker.Endpoint
	if endpoint == "" {
		if channel == ChannelPreview {
			endpoint = DefaultPreviewEndpoint
		} else {
			endpoint = DefaultEndpoint
		}
	}
	client := checker.Client
	if client == nil {
		client = http.DefaultClient
	}
	if channel == ChannelStable {
		data, err := fetchReleaseMetadata(
			ctx, client, endpoint, checker.CurrentVersion, checker.Endpoint == "",
		)
		if err != nil {
			return "", err
		}
		var release releaseResponse
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&release); err != nil {
			return "", fmt.Errorf("decode release metadata: %w", err)
		}
		latest, ok := eligibleRelease(release, channel)
		if !ok {
			return "", errors.New("latest release is not a stable semantic version")
		}
		return latest.String(), nil
	}
	pageEndpoints, pageSize, err := previewMetadataEndpoints(endpoint)
	if err != nil {
		return "", err
	}
	var latest semanticVersion
	found := false
	for _, pageEndpoint := range pageEndpoints {
		data, fetchErr := fetchReleaseMetadata(
			ctx, client, pageEndpoint, checker.CurrentVersion, checker.Endpoint == "",
		)
		if fetchErr != nil {
			return "", fetchErr
		}
		var releases []releaseResponse
		decoder := json.NewDecoder(bytes.NewReader(data))
		if decodeErr := decoder.Decode(&releases); decodeErr != nil {
			return "", fmt.Errorf("decode preview release metadata: %w", decodeErr)
		}
		candidate, ok := selectLatestRelease(releases, channel)
		if ok && (!found || candidate.Compare(latest) > 0) {
			latest = candidate
			found = true
		}
		if pageSize == 0 || len(releases) < pageSize {
			break
		}
	}
	if !found {
		return "", errors.New("no eligible preview release was found")
	}
	return latest.String(), nil
}

func previewMetadataEndpoints(endpoint string) ([]string, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("parse preview release endpoint: %w", err)
	}
	pageSizeText := parsed.Query().Get("per_page")
	if pageSizeText == "" {
		return []string{endpoint}, 0, nil
	}
	pageSize, err := strconv.Atoi(pageSizeText)
	if err != nil || pageSize < 1 || pageSize > maximumPreviewReleases {
		return nil, 0, errors.New("preview release endpoint has an invalid per_page value")
	}
	pageCount := (maximumPreviewReleases + pageSize - 1) / pageSize
	endpoints := make([]string, 0, pageCount)
	for page := 1; page <= pageCount; page++ {
		candidate := *parsed
		query := candidate.Query()
		query.Set("page", strconv.Itoa(page))
		candidate.RawQuery = query.Encode()
		endpoints = append(endpoints, candidate.String())
	}
	return endpoints, pageSize, nil
}

func fetchReleaseMetadata(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	currentVersion string,
	requireHTTPS bool,
) ([]byte, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil || endpointURL.Host == "" ||
		(requireHTTPS && endpointURL.Scheme != "https") {
		return nil, errors.New("release endpoint must be an absolute HTTPS URL")
	}
	effectiveClient := client
	if endpointURL.Scheme == "https" {
		effectiveClient = restrictedHTTPClient(client, endpointURL)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "corresync/"+currentVersion)
	response, err := effectiveClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch release metadata: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumBody))
		return nil, fmt.Errorf("release endpoint returned HTTP %d", response.StatusCode)
	}
	if endpointURL.Scheme == "https" &&
		(response.Request == nil || response.Request.URL == nil ||
			response.Request.URL.Scheme != "https" ||
			!allowedAssetHost(response.Request.URL, endpointURL)) {
		return nil, errors.New("release metadata redirected to an untrusted URL")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumBody+1))
	if err != nil {
		return nil, fmt.Errorf("read release metadata: %w", err)
	}
	if len(data) > maximumBody {
		return nil, errors.New("release metadata exceeds size limit")
	}
	return data, nil
}

func eligibleRelease(release releaseResponse, channel Channel) (semanticVersion, bool) {
	return eligibleVersionTag(release.TagName, release.Draft, release.Prerelease, channel)
}

func eligibleVersionTag(
	tag string,
	draft bool,
	prerelease bool,
	channel Channel,
) (semanticVersion, bool) {
	version, ok := parseVersion(tag)
	if !ok || tag != version.String() || draft || prerelease != (version.prerelease != "") {
		return semanticVersion{}, false
	}
	if version.prerelease == "" {
		return version, true
	}
	if channel != ChannelPreview || !supportedPreviewPrerelease(version.prerelease) {
		return semanticVersion{}, false
	}
	return version, true
}

func supportedPreviewPrerelease(prerelease string) bool {
	parts := strings.Split(prerelease, ".")
	if len(parts) != 2 {
		return false
	}
	switch parts[0] {
	case "alpha", "beta", "rc":
	default:
		return false
	}
	_, err := strconv.ParseUint(parts[1], 10, 64)
	return err == nil
}

func selectLatestRelease(releases []releaseResponse, channel Channel) (semanticVersion, bool) {
	var latest semanticVersion
	found := false
	for _, release := range releases {
		candidate, ok := eligibleRelease(release, channel)
		if !ok {
			continue
		}
		if !found || candidate.Compare(latest) > 0 {
			latest = candidate
			found = true
		}
	}
	return latest, found
}

func resultFromRecord(currentRaw string, current semanticVersion, record cacheRecord, cached bool) (Result, error) {
	channel, err := normalizeChannel(record.Channel)
	if err != nil {
		return Result{}, ErrUnavailable
	}
	result := Result{
		Channel:        channel,
		Status:         StatusUnavailable,
		CurrentVersion: currentRaw,
		CheckedAt:      record.CheckedAt.Format(time.RFC3339),
		Cached:         cached,
	}
	if record.Unavailable {
		return result, ErrUnavailable
	}
	latest, ok := parseVersion(record.LatestVersion)
	if !ok || (channel == ChannelStable && latest.prerelease != "") {
		return result, ErrUnavailable
	}
	result.LatestVersion = latest.String()
	result.ReleaseURL = "https://github.com/nkiyohara/corresync/releases/tag/" + url.PathEscape(latest.String())
	comparison := current.Compare(latest)
	if comparison < 0 {
		result.Status = StatusAvailable
		result.UpdateAvailable = true
	} else {
		result.Status = StatusCurrent
	}
	return result, nil
}

func loadFreshCache(path string, now time.Time, channel Channel) (cacheRecord, bool) {
	file, err := os.Open(path) // #nosec G304 -- path is the fixed private application cache.
	if err != nil {
		return cacheRecord{}, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return cacheRecord{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumCache+1))
	if err != nil || len(data) > maximumCache {
		return cacheRecord{}, false
	}
	var record cacheRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return cacheRecord{}, false
	}
	if record.Format != cacheFormat || record.Channel != channel {
		return cacheRecord{}, false
	}
	age := now.Sub(record.CheckedAt)
	if record.CheckedAt.IsZero() || age < 0 || age >= cacheLifetime {
		return cacheRecord{}, false
	}
	return record, true
}

func writeCache(path string, record cacheRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- private cache directories require owner execute.
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("update cache path is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".update-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	prerelease string
}

func parseVersion(value string) (semanticVersion, bool) {
	value = strings.TrimPrefix(value, "v")
	if !semver.IsValid("v" + value) {
		return semanticVersion{}, false
	}
	core, prerelease, _ := strings.Cut(value, "-")
	if buildIndex := strings.IndexByte(prerelease, '+'); buildIndex >= 0 {
		prerelease = prerelease[:buildIndex]
	}
	if buildIndex := strings.IndexByte(core, '+'); buildIndex >= 0 {
		core = core[:buildIndex]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	values := make([]uint64, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semanticVersion{}, false
		}
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, false
		}
		values[index] = parsed
	}
	return semanticVersion{
		major: values[0], minor: values[1], patch: values[2], prerelease: prerelease,
	}, true
}

func (version semanticVersion) String() string {
	value := fmt.Sprintf("v%d.%d.%d", version.major, version.minor, version.patch)
	if version.prerelease != "" {
		value += "-" + version.prerelease
	}
	return value
}

func (version semanticVersion) Compare(other semanticVersion) int {
	return semver.Compare(version.String(), other.String())
}
