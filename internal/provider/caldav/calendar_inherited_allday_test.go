package caldav

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestCalDAVInheritedAllDayRejectsNonmidnightBeforeWrite(t *testing.T) {
	server, backend, httpClient := newFixtureServer(t)
	client, err := New(t.Context(), Options{Endpoint: server.URL, Username: "reader@example.invalid", Password: []byte("synthetic-secret"), Client: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	created, err := client.CreateCalendarEvent(t.Context(), application.CalendarCreateInput{Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"}, Subject: "Synthetic", Start: "2026-07-20T00:00:00Z", End: "2026-07-21T00:00:00Z", AllDay: true})
	if err != nil {
		t.Fatal(err)
	}
	start, end := "2026-07-20T10:00:00Z", "2026-07-20T11:00:00Z"
	input := application.CalendarUpdateInput{Account: "work", EventID: created.ID, ChangeKey: created.ChangeKey, Start: &start, End: &end}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	before := len(backend.putConditions)
	backend.mu.Unlock()
	_, err = client.UpdateCalendarEvent(t.Context(), input)
	backend.mu.Lock()
	after := len(backend.putConditions)
	backend.mu.Unlock()
	if err == nil || after != before {
		t.Fatalf("nonmidnight inherited allDay writes before=%d after=%d error=%v", before, after, err)
	}
	allDay := false
	input.AllDay = &allDay
	if _, err := client.UpdateCalendarEvent(t.Context(), input); err != nil {
		t.Fatalf("explicit conversion to timed event failed: %v", err)
	}
}
