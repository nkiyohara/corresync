package browser

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/session"
)

func browserFixtureContext(t *testing.T, headless bool) context.Context {
	t.Helper()
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	// testing cancels t.Context before cleanup. Keep the bounded browser owner
	// alive until Browser.close drains Chromium's profile writers, then remove
	// the temporary profile. Killing only the parent races writers on Linux.
	owner, cancelOwner := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	allocator, cancelAllocator := chromedp.NewExecAllocator(owner, allocatorOptions(executable, t.TempDir(), headless)...)
	ctx, cancelBrowser := chromedp.NewContext(allocator)
	t.Cleanup(func() {
		_ = chromedp.Cancel(ctx)
		cancelBrowser()
		cancelAllocator()
		cancelOwner()
	})
	return ctx
}

func TestRequireGraphicalSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		goos        string
		environment map[string]string
		wantError   bool
	}{
		{name: "linux without display", goos: "linux", wantError: true},
		{
			name: "linux X11", goos: "linux",
			environment: map[string]string{"DISPLAY": ":0"},
		},
		{
			name: "linux Wayland", goos: "linux",
			environment: map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
		},
		{name: "macOS without display variables", goos: "darwin"},
		{name: "Windows without display variables", goos: "windows"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lookup := func(name string) (string, bool) {
				value, exists := test.environment[name]
				return value, exists
			}
			err := requireGraphicalSession(test.goos, lookup)
			if test.wantError && !errors.Is(err, ErrGraphicalSessionUnavailable) {
				t.Fatalf("requireGraphicalSession() error = %v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("requireGraphicalSession() error = %v", err)
			}
		})
	}
}

func TestBrowserOwnedModeCannotExposeAuthorization(t *testing.T) {
	t.Parallel()

	instance := &Browser{}
	if _, err := instance.WaitForSession(context.Background()); err == nil {
		t.Fatal("WaitForSession() exposed a browser-owned session")
	}
	if _, err := instance.CurrentSession(); err == nil {
		t.Fatal("CurrentSession() exposed a browser-owned session")
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://mail.google.com/",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Apply(request); err == nil {
		t.Fatal("Apply() exposed a browser-owned session")
	}
}

func TestBrowserWaitForSessionReturnsObservationFailure(t *testing.T) {
	t.Parallel()

	manager, err := session.NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatal(err)
	}
	browserContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	instance := &Browser{
		context:       browserContext,
		sessions:      manager,
		sessionErrors: make(chan error, 1),
	}
	instance.reportSessionError(session.ErrAuthorizationScheme)
	if _, err := instance.WaitForSession(t.Context()); !errors.Is(
		err,
		session.ErrAuthorizationScheme,
	) {
		t.Fatalf("WaitForSession() error = %v", err)
	}
}

func TestBrowserCannotApplyAuthorizationAfterOwnerExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := session.NewManager("https://outlook.example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	browser := &Browser{context: ctx, sessions: manager}
	cancel()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://outlook.example.invalid/owa/service.svc",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := browser.Apply(request); !errors.Is(err, ErrBrowserSessionUnavailable) {
		t.Fatalf("Apply() after browser exit error = %v", err)
	}
}

func TestGoogleWebSnapshotsDistinguishEmptyViewsFromSelectorDrift(t *testing.T) {
	t.Parallel()

	if err := validateGoogleMailSnapshot(
		GoogleMailSnapshot{State: "empty"},
	); err != nil {
		t.Fatalf("recognized empty mail snapshot: %v", err)
	}
	if err := validateGoogleMailSnapshot(
		GoogleMailSnapshot{
			State: "rows",
			Rows:  []GoogleMailRow{{ID: "thread-1"}},
		},
	); err != nil {
		t.Fatalf("recognized mail rows: %v", err)
	}
	if err := validateGoogleMailSnapshot(
		GoogleMailSnapshot{State: "unknown"},
	); err == nil {
		t.Fatal("unknown Gmail DOM was reported as an empty mailbox")
	}

	if err := validateGoogleCalendarSnapshot(
		GoogleCalendarSnapshot{State: "empty"},
	); err != nil {
		t.Fatalf("recognized empty calendar snapshot: %v", err)
	}
	if err := validateGoogleCalendarSnapshot(
		GoogleCalendarSnapshot{
			State: "rows",
			Rows:  []GoogleCalendarRow{{ID: "event-1"}},
		},
	); err != nil {
		t.Fatalf("recognized calendar rows: %v", err)
	}
	if err := validateGoogleCalendarSnapshot(
		GoogleCalendarSnapshot{State: "unknown"},
	); err == nil {
		t.Fatal("unknown Calendar DOM was reported as an empty agenda")
	}
}

func TestValidateOptions(t *testing.T) {
	t.Parallel()

	valid := Options{
		Origin:     "https://outlook.cloud.microsoft",
		ProfileDir: filepath.Join(t.TempDir(), "profile"),
	}
	if err := validateOptions(valid); err != nil {
		t.Fatalf("validateOptions() error = %v", err)
	}
	google := valid
	google.Origin = "https://mail.google.com"
	google.AdditionalOrigins = []string{"https://calendar.google.com"}
	google.StartURL = "https://mail.google.com/mail/u/0/#inbox"
	if err := validateOptions(google); err != nil {
		t.Fatalf("validateOptions(Google) error = %v", err)
	}

	tests := []Options{
		{},
		{Origin: "http://outlook.example", ProfileDir: valid.ProfileDir},
		{Origin: valid.Origin, ProfileDir: "relative/profile"},
		{Origin: valid.Origin, ProfileDir: valid.ProfileDir, Executable: "chrome\n--flag"},
		{
			Origin: valid.Origin, ProfileDir: valid.ProfileDir,
			StartURL: "https://example.invalid/",
		},
	}
	for _, options := range tests {
		if err := validateOptions(options); err == nil {
			t.Fatalf("validateOptions(%+v) unexpectedly succeeded", options)
		}
	}
}

func TestResolveExecutableUsesExactConfiguredPath(t *testing.T) {
	t.Parallel()

	executable := filepath.Join(t.TempDir(), "test-chromium")
	// #nosec G306 -- the owner-only test fixture must be executable.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExecutable(executable)
	if err != nil {
		t.Fatalf("ResolveExecutable() error = %v", err)
	}
	if resolved != executable {
		t.Fatalf("ResolveExecutable() = %q, want %q", resolved, executable)
	}
}

func TestResolveExecutableDoesNotFallbackFromExplicitPath(t *testing.T) {
	t.Parallel()

	_, err := ResolveExecutable(filepath.Join(t.TempDir(), "missing-chromium"))
	if err == nil {
		t.Fatal("ResolveExecutable() unexpectedly accepted a missing explicit path")
	}
}
