//go:build live

package googleweb_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/provider/googleweb"
)

// TestLiveGoogleWebVisibleRead is intentionally excluded from default tests
// and CI. It uses an isolated visible browser profile, keeps browser
// authorization material inside Chromium, and performs bounded read-only
// Gmail and Calendar observations.
func TestLiveGoogleWebVisibleRead(t *testing.T) {
	if os.Getenv("CORRESYNC_LIVE_CONFIRM") != "google-web-read-only" {
		t.Skip("set CORRESYNC_LIVE_CONFIRM=google-web-read-only to opt in")
	}
	address := os.Getenv("CORRESYNC_LIVE_GOOGLE_ADDRESS")
	profile := os.Getenv("CORRESYNC_LIVE_GOOGLE_PROFILE_DIR")
	if address == "" || profile == "" {
		t.Fatal(
			"CORRESYNC_LIVE_GOOGLE_ADDRESS and " +
				"CORRESYNC_LIVE_GOOGLE_PROFILE_DIR are required",
		)
	}
	if !filepath.IsAbs(profile) {
		t.Fatal("CORRESYNC_LIVE_GOOGLE_PROFILE_DIR must be absolute")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	handle, err := browser.Launch(ctx, browser.Options{
		Origin:            "https://mail.google.com",
		AdditionalOrigins: []string{"https://calendar.google.com"},
		StartURL:          "https://mail.google.com/mail/u/0/#inbox",
		ProfileDir:        profile,
		Executable:        os.Getenv("CORRESYNC_LIVE_BROWSER_EXECUTABLE"),
		BrowserOwnedOnly:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	})
	client, err := googleweb.New(ctx, googleweb.Options{
		ExpectedAddress: address,
		Mail:            true,
		Calendar:        true,
		Driver:          handle,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.ListMessages(ctx, application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 10,
	}); err != nil {
		t.Fatalf("bounded Gmail read: %v", err)
	}
	now := time.Now().UTC()
	if _, err := client.ListCalendarEvents(
		ctx,
		application.CalendarListInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Start: now.Add(-24 * time.Hour).Format(time.RFC3339),
			End:   now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
		},
	); err != nil {
		t.Fatalf("bounded Google Calendar read: %v", err)
	}
}
