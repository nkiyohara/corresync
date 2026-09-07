package googleapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGoogleInheritedAllDayRejectsNonmidnightBeforeWrite(t *testing.T) {
	writes := 0
	event := googleTestEvent(`"etag1"`)
	event.Start = googleEventTime{Date: "2026-07-20"}
	event.End = googleEventTime{Date: "2026-07-21"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /calendar/v3/users/me/calendarList/primary":
			writeGoogleJSON(t, w, map[string]any{"id": "reader@example.test", "accessRole": "owner"})
		case "GET /calendar/v3/calendars/primary/events/e1":
			writeGoogleJSON(t, w, event)
		case "PATCH /calendar/v3/calendars/primary/events/e1":
			writes++
			changed := event
			changed.ETag = `"etag2"`
			writeGoogleJSON(t, w, changed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{APIBase: server.URL, Address: "reader@example.test", Calendar: true, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	id, err := encodeEventID("primary", "e1")
	if err != nil {
		t.Fatal(err)
	}
	start, end := "2026-07-20T10:00:00Z", "2026-07-20T11:00:00Z"
	input := application.CalendarUpdateInput{Account: "work", EventID: id, ChangeKey: encodeETag(`"etag1"`), Start: &start, End: &end}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateCalendarEvent(t.Context(), input); err == nil || writes != 0 {
		t.Fatalf("nonmidnight inherited allDay writes=%d error=%v", writes, err)
	}
	allDay := false
	input.AllDay = &allDay
	if _, err := client.UpdateCalendarEvent(t.Context(), input); err != nil || writes != 1 {
		t.Fatalf("explicit timed conversion writes=%d error=%v", writes, err)
	}
}
