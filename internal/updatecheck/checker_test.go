package updatecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckerCachesSuccessfulResultForTwentyFourHours(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Accept") != "application/vnd.github+json" ||
			request.Header.Get("User-Agent") != "corresync/1.0.0" || request.URL.RawQuery != "" {
			t.Errorf("unexpected release request: headers=%v URL=%s", request.Header, request.URL)
		}
		version := "v1.1.0"
		if requests > 1 {
			version = "v1.2.0"
		}
		_, _ = writer.Write([]byte(`{"tag_name":"` + version + `","draft":false,"prerelease":false}`))
	}))
	defer server.Close()

	checker := Checker{
		CurrentVersion: "1.0.0",
		CachePath:      filepath.Join(t.TempDir(), "updates", "latest.json"),
		Endpoint:       server.URL,
		Client:         server.Client(),
		Now:            func() time.Time { return now },
	}
	first, err := checker.Check(t.Context())
	if err != nil || first.Status != StatusAvailable || first.LatestVersion != "v1.1.0" || first.Cached {
		t.Fatalf("first Check() = %+v, %v", first, err)
	}
	now = now.Add(23 * time.Hour)
	second, err := checker.Check(t.Context())
	if err != nil || !second.Cached || second.LatestVersion != "v1.1.0" || requests != 1 {
		t.Fatalf("cached Check() = %+v, %v; requests=%d", second, err, requests)
	}
	now = now.Add(2 * time.Hour)
	third, err := checker.Check(t.Context())
	if err != nil || third.Cached || third.LatestVersion != "v1.2.0" || requests != 2 {
		t.Fatalf("expired Check() = %+v, %v; requests=%d", third, err, requests)
	}
}

func TestCheckerAcceptsLargeGitHubReleaseMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"tag_name":"v1.1.0","draft":false,"prerelease":false,"assets":"` +
			strings.Repeat("a", 128<<10) + `"}`))
	}))
	defer server.Close()

	checker := Checker{
		CurrentVersion: "1.0.0",
		CachePath:      filepath.Join(t.TempDir(), "latest.json"),
		Endpoint:       server.URL,
		Client:         server.Client(),
	}
	result, err := checker.Check(t.Context())
	if err != nil || result.Status != StatusAvailable || result.LatestVersion != "v1.1.0" {
		t.Fatalf("Check() = %+v, %v", result, err)
	}
}

func TestForcedCheckerRefreshesFreshCache(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write([]byte(`{"tag_name":"v1.1.0","draft":false,"prerelease":false}`))
	}))
	defer server.Close()
	cachePath := filepath.Join(t.TempDir(), "latest.json")
	if err := writeCache(cachePath, cacheRecord{
		Format:        cacheFormat,
		CheckedAt:     now,
		LatestVersion: "v1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := (Checker{
		CurrentVersion: "1.0.0",
		CachePath:      cachePath,
		Endpoint:       server.URL,
		Client:         server.Client(),
		Now:            func() time.Time { return now.Add(time.Hour) },
		Force:          true,
	}).Check(t.Context())
	if err != nil || result.Status != StatusAvailable || result.LatestVersion != "v1.1.0" ||
		result.Cached || requests != 1 {
		t.Fatalf("forced Check() = %+v, %v; requests=%d", result, err, requests)
	}
}

func TestCheckerInvalidatesLegacyFailureCache(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write([]byte(`{"tag_name":"v1.0.0","draft":false,"prerelease":false}`))
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "latest.json")
	legacy := `{"checkedAt":"2026-07-22T10:00:00Z","unavailable":true}` + "\n"
	if err := os.WriteFile(cachePath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	checker := Checker{
		CurrentVersion: "1.0.0",
		CachePath:      cachePath,
		Endpoint:       server.URL,
		Client:         server.Client(),
		Now:            func() time.Time { return time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC) },
	}
	result, err := checker.Check(t.Context())
	if err != nil || result.Status != StatusCurrent || result.Cached || requests != 1 {
		t.Fatalf("Check() = %+v, %v; requests=%d", result, err, requests)
	}
}

func TestCheckerSerializesConcurrentProcesses(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		close(entered)
		<-release
		_, _ = writer.Write([]byte(`{"tag_name":"v1.1.0","draft":false,"prerelease":false}`))
	}))
	defer server.Close()
	checker := Checker{
		CurrentVersion: "1.0.0",
		CachePath:      filepath.Join(t.TempDir(), "latest.json"),
		Endpoint:       server.URL,
		Client:         server.Client(),
	}
	finished := make(chan error, 1)
	go func() {
		_, err := checker.Check(context.Background())
		finished <- err
	}()
	<-entered
	result, err := checker.Check(t.Context())
	if !errors.Is(err, ErrUnavailable) || result.Status != StatusUnavailable || requests.Load() != 1 {
		t.Fatalf("concurrent Check() = %+v, %v; requests=%d", result, err, requests.Load())
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("first Check() error = %v", err)
	}
	result, err = checker.Check(t.Context())
	if err != nil || result.Status != StatusAvailable || !result.Cached || requests.Load() != 1 {
		t.Fatalf("final Check() = %+v, %v; requests=%d", result, err, requests.Load())
	}
}

func TestVersionRelationshipNeverSuggestsDowngrade(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		latest    string
		status    Status
		available bool
	}{
		{name: "newer", current: "0.3.2", latest: "v0.4.0", status: StatusAvailable, available: true},
		{name: "equal", current: "0.4.0", latest: "v0.4.0", status: StatusCurrent},
		{name: "older release metadata", current: "0.5.0", latest: "v0.4.0", status: StatusCurrent},
		{name: "prerelease current", current: "0.4.0-rc.1", latest: "v0.4.0", status: StatusAvailable, available: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, ok := parseVersion(test.current)
			if !ok {
				t.Fatal("current version did not parse")
			}
			result, err := resultFromRecord(test.current, current, cacheRecord{
				CheckedAt: time.Now().UTC(), LatestVersion: test.latest,
			}, false)
			if err != nil || result.Status != test.status || result.UpdateAvailable != test.available {
				t.Fatalf("result = %+v, %v", result, err)
			}
		})
	}
}

func TestCheckerRejectsAndCachesPrereleaseMetadata(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write([]byte(`{"tag_name":"v1.1.0-rc.1","draft":false,"prerelease":true}`))
	}))
	defer server.Close()

	checker := Checker{
		CurrentVersion: "1.0.0",
		CachePath:      filepath.Join(t.TempDir(), "latest.json"),
		Endpoint:       server.URL,
		Client:         server.Client(),
	}
	for range 2 {
		result, err := checker.Check(t.Context())
		if !errors.Is(err, ErrUnavailable) || result.Status != StatusUnavailable {
			t.Fatalf("Check() = %+v, %v", result, err)
		}
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one cached attempt", requests)
	}
}

func TestPreviewCheckerSelectsHighestStableOrPrerelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`[
{"tag_name":"v1.1.0-rc.2","draft":false,"prerelease":true},
{"tag_name":"v1.0.0","draft":false,"prerelease":false},
{"tag_name":"v1.1.0-rc.10","draft":false,"prerelease":true},
{"tag_name":"v9.0.0","draft":true,"prerelease":false},
{"tag_name":"v2.0.0-rc.1","draft":false,"prerelease":false},
{"tag_name":"v3.0.0-dev.1","draft":false,"prerelease":true},
{"tag_name":"v4.0.0+rebuilt","draft":false,"prerelease":false}
]`))
	}))
	defer server.Close()

	result, err := (Checker{
		CurrentVersion: "1.1.0-rc.2",
		Channel:        ChannelPreview,
		CachePath:      filepath.Join(t.TempDir(), "latest-preview.json"),
		Endpoint:       server.URL,
		Client:         server.Client(),
	}).Check(t.Context())
	if err != nil || result.Channel != ChannelPreview ||
		result.Status != StatusAvailable || result.LatestVersion != "v1.1.0-rc.10" {
		t.Fatalf("preview Check() = %+v, %v", result, err)
	}
}

func TestPreviewChannelNeverDowngradesToOlderStable(t *testing.T) {
	current, ok := parseVersion("v1.1.0-rc.1")
	if !ok {
		t.Fatal("current version did not parse")
	}
	result, err := resultFromRecord("v1.1.0-rc.1", current, cacheRecord{
		Channel: ChannelPreview, CheckedAt: time.Now().UTC(), LatestVersion: "v1.0.0",
	}, false)
	if err != nil || result.Status != StatusCurrent || result.UpdateAvailable {
		t.Fatalf("preview downgrade result = %+v, %v", result, err)
	}
}

func TestCheckerCachesNetworkFailureAndDevelopmentBuildSkipsNetwork(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("synthetic network failure")
	})}
	cachePath := filepath.Join(t.TempDir(), "latest.json")
	checker := Checker{CurrentVersion: "1.0.0", CachePath: cachePath, Client: client}
	for range 2 {
		result, err := checker.Check(context.Background())
		if !errors.Is(err, ErrUnavailable) || result.Status != StatusUnavailable {
			t.Fatalf("Check() = %+v, %v", result, err)
		}
	}
	if requests != 1 {
		t.Fatalf("network requests = %d, want one cached attempt", requests)
	}

	development := Checker{CurrentVersion: "dev", CachePath: cachePath, Client: client}
	result, err := development.Check(context.Background())
	if err != nil || result.Status != StatusDevelopment || requests != 1 {
		t.Fatalf("development Check() = %+v, %v; requests=%d", result, err, requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
