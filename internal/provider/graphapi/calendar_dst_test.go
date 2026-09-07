package graphapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestGraphCalendarRejectsBothOccurrencesOfNamedZoneFoldBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, zone := range []string{"Europe/London", "GMT Standard Time"} {
		for _, start := range []string{"2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"} {
			t.Run(zone+"/"+start, func(t *testing.T) {
				calls := 0
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
				defer server.Close()
				api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				client := &Client{api: api, calendar: true}
				defer func() { _ = client.Close() }()
				input := application.CalendarCreateInput{Account: graphTaskAccount, Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"}, Subject: "Synthetic", Start: start, End: "2026-10-25T03:00:00Z", TimeZone: zone}
				if err := input.Validate(10); err != nil {
					t.Fatal(err)
				}
				_, err = client.CreateCalendarEvent(t.Context(), input)
				if err == nil || !strings.Contains(err.Error(), "ambiguous") || calls != 0 {
					t.Fatalf("fold create err=%v, HTTP calls=%d", err, calls)
				}
				utc, err := graphWriteTime(start, "UTC")
				if err != nil {
					t.Fatal(err)
				}
				parsed, err := time.Parse(time.RFC3339, utc["dateTime"]+"Z")
				if err != nil {
					t.Fatal(err)
				}
				original, err := time.Parse(time.RFC3339, start)
				if err != nil {
					t.Fatal(err)
				}
				if !parsed.Equal(original) {
					t.Fatalf("UTC fallback changed instant: %v", utc)
				}
			})
		}
	}
	for _, start := range []string{"2026-10-24T23:59:59Z", "2026-10-25T02:00:00Z"} {
		if _, err := graphWriteTime(start, "Europe/London"); err != nil {
			t.Fatalf("non-fold boundary %s rejected: %v", start, err)
		}
	}
}

func TestGraphTaskRejectsBothFoldOccurrencesForEveryTemporalWriteBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"2026-10-25T01:30:00+01:00", "2026-10-25T01:30:00Z"} {
		for _, field := range []string{"start", "due", "reminder"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				calls := 0
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusInternalServerError) }))
				defer server.Close()
				api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				client := &Client{api: api, tasks: true, taskWrite: true}
				defer func() { _ = client.Close() }()
				listID, err := encodeTaskListID("list")
				if err != nil {
					t.Fatal(err)
				}
				input := application.TaskCreateInput{Account: graphTaskAccount, ListID: listID, Title: "Synthetic", Priority: application.TaskPriorityNone}
				temporal := &application.TaskTemporal{Kind: application.TaskTemporalZoned, Value: value, TimeZone: "Europe/London"}
				switch field {
				case "start":
					input.Start = temporal
				case "due":
					input.Due = temporal
				case "reminder":
					input.Reminders = []application.TaskReminder{{Kind: application.TaskReminderAbsolute, At: temporal}}
				}
				if err := input.Validate(); err != nil {
					t.Fatal(err)
				}
				_, err = client.CreateTask(t.Context(), input)
				if err == nil || !strings.Contains(err.Error(), "ambiguous") || calls != 0 {
					t.Fatalf("task %s fold error=%v, HTTP calls=%d", field, err, calls)
				}
				temporal.TimeZone = "UTC"
				utc, err := graphWriteTaskTime(temporal)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := time.Parse(time.RFC3339, utc.DateTime+"Z")
				if err != nil {
					t.Fatal(err)
				}
				expected, err := time.Parse(time.RFC3339, value)
				if err != nil {
					t.Fatal(err)
				}
				if !actual.Equal(expected) {
					t.Fatalf("UTC task fallback changed instant: %+v", utc)
				}
			})
		}
	}
}
