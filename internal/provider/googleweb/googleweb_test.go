package googleweb

import (
	"context"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
)

type fakeDriver struct {
	origins         []string
	identityTarget  string
	identity        string
	mailTarget      string
	bodyTarget      string
	calendarTarget  string
	calendarTargets []string
	mailState       string
	calendarState   string
	mailRows        []browser.GoogleMailRow
	body            string
	calendarRows    []browser.GoogleCalendarRow
}

func (driver *fakeDriver) GoogleIdentity(
	_ context.Context,
	target string,
) (string, error) {
	driver.identityTarget = target
	if driver.identity == "" {
		return "reader@example.test", nil
	}
	return driver.identity, nil
}

func (driver *fakeDriver) WaitForGoogleWeb(
	_ context.Context,
	origins []string,
) error {
	driver.origins = append([]string(nil), origins...)
	return nil
}

func (driver *fakeDriver) GoogleMailRows(
	_ context.Context,
	target string,
) (browser.GoogleMailSnapshot, error) {
	driver.mailTarget = target
	state := driver.mailState
	if state == "" {
		state = "empty"
		if len(driver.mailRows) != 0 {
			state = "rows"
		}
	}
	return browser.GoogleMailSnapshot{
		State: state,
		Rows:  append([]browser.GoogleMailRow(nil), driver.mailRows...),
	}, nil
}

func (driver *fakeDriver) GoogleMailBody(
	_ context.Context,
	target string,
) (string, error) {
	driver.bodyTarget = target
	return driver.body, nil
}

func (driver *fakeDriver) GoogleCalendarRows(
	_ context.Context,
	target string,
) (browser.GoogleCalendarSnapshot, error) {
	driver.calendarTarget = target
	driver.calendarTargets = append(driver.calendarTargets, target)
	state := driver.calendarState
	if state == "" {
		state = "empty"
		if len(driver.calendarRows) != 0 {
			state = "rows"
		}
	}
	return browser.GoogleCalendarSnapshot{
		State: state,
		Rows:  append([]browser.GoogleCalendarRow(nil), driver.calendarRows...),
	}, nil
}

func TestBrowserOwnedGoogleReadsStayOnClosedSemanticDriver(t *testing.T) {
	t.Parallel()
	driver := &fakeDriver{
		mailRows: []browser.GoogleMailRow{{
			ID: "thread-1", Text: "Synthetic subject",
			FromName: "Sender", FromAddress: "sender@example.test",
			Unread: true, HasAttachments: true,
		}},
		body: "Synthetic body",
		calendarRows: []browser.GoogleCalendarRow{{
			ID: "event-1", Text: "Synthetic event",
			Start: "2026-08-01T10:00:00Z", End: "2026-08-01T11:00:00Z",
			Location: "Room",
		}},
	}
	client, err := New(t.Context(), Options{
		ExpectedAddress: "reader@example.test",
		Mail:            true, Calendar: true, Driver: driver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.origins) != 2 {
		t.Fatalf("wait origins = %#v", driver.origins)
	}
	mail, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 10,
	})
	if err != nil || len(mail.Messages) != 1 ||
		mail.Messages[0].Subject != "Synthetic subject" ||
		mail.Messages[0].IsRead ||
		!strings.HasPrefix(mail.Messages[0].ID, "ggwm1_") {
		t.Fatalf("mail = %#v error = %v", mail, err)
	}
	body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{
		MessageID: mail.Messages[0].ID,
	})
	if err != nil || body.Text != "Synthetic body" ||
		!strings.Contains(driver.bodyTarget, "#all/thread-1") {
		t.Fatalf(
			"body = %#v target = %q error = %v",
			body,
			driver.bodyTarget,
			err,
		)
	}
	calendar, err := client.ListCalendarEvents(
		t.Context(),
		application.CalendarListInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Start: "2026-08-01T00:00:00Z", End: "2026-08-02T00:00:00Z",
		},
	)
	if err != nil || len(calendar.Events) != 1 ||
		calendar.Events[0].Subject != "Synthetic event" ||
		!strings.Contains(driver.calendarTarget, "/agenda/2026/8/1") ||
		len(driver.calendarTargets) != 1 {
		t.Fatalf("calendar = %#v error = %v", calendar, err)
	}
}

func TestGoogleWebCalendarVisitsEveryUTCDateAndDeduplicatesRows(t *testing.T) {
	t.Parallel()
	driver := &fakeDriver{
		calendarRows: []browser.GoogleCalendarRow{{
			ID: "event-1", Text: "Multi-day event",
			Start: "2026-08-01T20:00:00Z",
			End:   "2026-08-03T08:00:00Z",
		}},
	}
	client, err := New(t.Context(), Options{
		Calendar: true, Driver: driver,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListCalendarEvents(
		t.Context(),
		application.CalendarListInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Start: "2026-08-01T12:00:00Z",
			End:   "2026-08-03T12:00:00Z",
		},
	)
	if err != nil || len(page.Events) != 1 ||
		len(driver.calendarTargets) != 3 ||
		!strings.Contains(driver.calendarTargets[0], "/agenda/2026/8/1") ||
		!strings.Contains(driver.calendarTargets[2], "/agenda/2026/8/3") {
		t.Fatalf(
			"multi-day page = %#v targets=%#v error=%v",
			page,
			driver.calendarTargets,
			err,
		)
	}
}

func TestGoogleWebRejectsWrongBrowserIdentity(t *testing.T) {
	t.Parallel()
	client, err := New(t.Context(), Options{
		ExpectedAddress: "reader@example.test",
		Mail:            true,
		Driver: &fakeDriver{
			identity: "different@example.test",
		},
	})
	if client != nil || err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("client = %#v error = %v", client, err)
	}
}

func TestGoogleWebWritesFailClearlyWithoutUsingTheDriver(t *testing.T) {
	t.Parallel()
	client, err := New(t.Context(), Options{
		Mail: true, Driver: &fakeDriver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendMail(
		t.Context(),
		application.MailSendInput{},
	); err == nil || !strings.Contains(err.Error(), "google") {
		t.Fatalf("SendMail() error = %v", err)
	}
}

func TestGoogleWebRejectsNonProviderOrigins(t *testing.T) {
	t.Parallel()
	if _, err := New(t.Context(), Options{
		Mail: true, MailOrigin: "https://example.test",
		Driver: &fakeDriver{},
	}); err == nil {
		t.Fatal("New() accepted a non-Google mail origin")
	}
}
