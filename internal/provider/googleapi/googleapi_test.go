package googleapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGoogleCalendarContractUsesBoundedReadsAndConditionalWrites(
	t *testing.T,
) {
	t.Parallel()

	var recurrenceUpdated bool
	var meetRequested bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /calendar/v3/users/me/calendarList/primary":
			writeGoogleJSON(t, writer, map[string]any{
				"id": "reader@example.test", "accessRole": "owner",
				"conferenceProperties": map[string]any{
					"allowedConferenceSolutionTypes": []string{"hangoutsMeet"},
				},
			})
		case "GET /calendar/v3/users/me/calendarList":
			writeGoogleJSON(t, writer, map[string]any{
				"items": []googleCalendarListEntry{{
					ID: "reader@example.test", Summary: "Primary",
					TimeZone: "Europe/London", AccessRole: "owner", Primary: true,
				}},
			})
		case "GET /calendar/v3/calendars/primary/events":
			if request.URL.Query().Get("showDeleted") != "false" ||
				request.URL.Query().Get("maxResults") != "2500" {
				t.Errorf("calendar list query = %q", request.URL.RawQuery)
			}
			writeGoogleJSON(t, writer, map[string]any{
				"items": []googleEvent{googleTestEvent(`"etag1"`)},
			})
		case "POST /calendar/v3/calendars/primary/events":
			if request.URL.Query().Get("sendUpdates") != "all" ||
				request.URL.Query().Get("conferenceDataVersion") != "1" {
				t.Errorf("calendar create query = %q", request.URL.RawQuery)
			}
			var payload struct {
				ConferenceData struct {
					CreateRequest struct {
						RequestID             string `json:"requestId"`
						ConferenceSolutionKey struct {
							Type string `json:"type"`
						} `json:"conferenceSolutionKey"`
					} `json:"createRequest"`
				} `json:"conferenceData"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			meetRequested =
				len(payload.ConferenceData.CreateRequest.RequestID) >= 32 &&
					payload.ConferenceData.CreateRequest.
						ConferenceSolutionKey.Type == "hangoutsMeet"
			event := googleTestEvent(`"etag2"`)
			event.HangoutLink = "https://meet.google.com/abc-defg-hij"
			event.ConferenceData.CreateRequest.Status.StatusCode = "success"
			event.ConferenceData.ConferenceSolution.Key.Type = "hangoutsMeet"
			writeGoogleJSON(t, writer, event)
		case "GET /calendar/v3/calendars/primary/events/e1":
			writeGoogleJSON(t, writer, googleTestEvent(`"etag1"`))
		case "PATCH /calendar/v3/calendars/primary/events/e1":
			if request.Header.Get("If-Match") != `"etag1"` ||
				request.URL.Query().Get("sendUpdates") != "all" ||
				request.URL.Query().Get("conferenceDataVersion") != "1" {
				t.Errorf(
					"calendar update condition = %q query = %q",
					request.Header.Get("If-Match"),
					request.URL.RawQuery,
				)
			}
			var patch map[string]json.RawMessage
			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			recurrenceUpdated =
				string(patch["recurrence"]) == `["RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=4"]`
			writeGoogleJSON(t, writer, googleTestEvent(`"etag3"`))
		case "DELETE /calendar/v3/calendars/primary/events/e1":
			if request.Header.Get("If-Match") != `"etag1"` ||
				request.URL.Query().Get("sendUpdates") != "all" {
				t.Errorf(
					"calendar cancel condition = %q query = %q",
					request.Header.Get("If-Match"),
					request.URL.RawQuery,
				)
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(
				writer,
				"unexpected synthetic Google Calendar request",
				http.StatusNotFound,
			)
		}
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	if !client.MeetAvailable() {
		t.Fatal("authenticated calendar capability omitted Google Meet")
	}

	calendars, err := client.ListCalendarFolders(
		t.Context(),
		application.CalendarFolderListInput{Limit: 10},
	)
	if err != nil || len(calendars.Calendars) != 1 ||
		!calendars.Calendars[0].IsDefault ||
		!calendars.Calendars[0].CanEdit ||
		calendars.Calendars[0].ID == "" {
		t.Fatalf("calendars = %#v error = %v", calendars, err)
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
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar.Events) != 1 ||
		calendar.Events[0].Subject != "Synthetic event" ||
		calendar.Events[0].OriginalStart != "2026-08-01T10:00:00Z" ||
		calendar.Events[0].OriginalStartTimeZone != "Europe/London" {
		t.Fatalf("calendar = %#v", calendar)
	}
	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Subject: "Created", Start: "2026-08-01T10:00:00Z",
			End:               "2026-08-01T11:00:00Z",
			RequiredAttendees: []string{"person@example.test"},
			OnlineMeeting:     true,
		},
	)
	if err != nil || created.ID == "" || created.ChangeKey == "" ||
		!created.IsOnlineMeeting ||
		created.OnlineMeetingProvider != "google-meet" ||
		created.OnlineMeetingJoinURL !=
			"https://meet.google.com/abc-defg-hij" ||
		!meetRequested {
		t.Fatalf("created = %#v error = %v", created, err)
	}
	subject := "Updated"
	start := "2026-08-01T10:00:00Z"
	end := "2026-08-01T11:00:00Z"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: calendar.Events[0].ID, ChangeKey: calendar.Events[0].ChangeKey,
			Subject: &subject, Start: &start, End: &end,
			ReplaceRecurrence: true,
			Recurrence: &application.CalendarRecurrence{
				Pattern: application.CalendarRecurrenceWeekly, Interval: 1,
				DaysOfWeek: []string{"Monday"}, NumberOfOccurrences: 4,
			},
		},
	)
	if err != nil || updated.ChangeKey == calendar.Events[0].ChangeKey ||
		!recurrenceUpdated {
		t.Fatalf("updated = %#v error = %v", updated, err)
	}
	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID: calendar.Events[0].ID, ChangeKey: calendar.Events[0].ChangeKey,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleCalendarRouteBindsTheConfiguredIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/calendar/v3/users/me/calendarList/primary" {
			http.Error(writer, "unexpected request", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writeGoogleJSON(t, writer, map[string]any{
			"id": "other@example.test", "accessRole": "owner",
		})
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		HTTP: server.Client(),
	})
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("New() error = %v, want calendar identity mismatch", err)
	}
}

func TestGoogleCalendarRejectsReadOnlyPrimaryCalendar(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writeGoogleJSON(t, writer, map[string]string{
			"id": "reader@example.test", "accessRole": "reader",
		})
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, HTTP: server.Client(),
	})
	if client != nil || err == nil || !strings.Contains(err.Error(), "not editable") {
		t.Fatalf("client = %#v error = %v", client, err)
	}
}

func TestGoogleMeetStatusFailsClosedOnUntrustedOrPendingLinks(t *testing.T) {
	t.Parallel()

	success := googleTestEvent(`"etag1"`)
	success.HangoutLink = "https://meet.google.com/abc-defg-hij"
	success.ConferenceData.CreateRequest.Status.StatusCode = "success"
	success.ConferenceData.ConferenceSolution.Key.Type = "hangoutsMeet"
	link, status, err := googleMeetStatus(success)
	if err != nil || status != "success" ||
		link != "https://meet.google.com/abc-defg-hij" {
		t.Fatalf("valid Meet status = %q, %q, %v", link, status, err)
	}

	pending := success
	pending.ConferenceData.CreateRequest.Status.StatusCode = "pending"
	if link, status, err := googleMeetStatus(pending); err != nil ||
		link != "" || status != "pending" {
		t.Fatalf("pending Meet status = %q, %q, %v", link, status, err)
	}

	for _, unsafe := range []string{
		"https://attacker.invalid/abc-defg-hij",
		"https://meet.google.com:444/abc-defg-hij",
		"https://user@meet.google.com/abc-defg-hij",
	} {
		event := success
		event.HangoutLink = unsafe
		if _, _, err := googleMeetStatus(event); err == nil {
			t.Fatalf("accepted unsafe Meet link %q", unsafe)
		}
	}
}

func TestGoogleMeetPendingCreateUsesOneBoundedConfirmationRead(
	t *testing.T,
) {
	t.Parallel()

	var createCalls int
	var confirmationReads int
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /calendar/v3/users/me/calendarList/primary":
			writeGoogleJSON(t, writer, map[string]any{
				"id": "reader@example.test", "accessRole": "owner",
				"conferenceProperties": map[string]any{
					"allowedConferenceSolutionTypes": []string{"hangoutsMeet"},
				},
			})
		case "POST /calendar/v3/calendars/primary/events":
			createCalls++
			event := googleTestEvent(`"etag1"`)
			event.ConferenceData.CreateRequest.Status.StatusCode = "pending"
			writeGoogleJSON(t, writer, event)
		case "GET /calendar/v3/calendars/primary/events/e1":
			confirmationReads++
			event := googleTestEvent(`"etag2"`)
			event.HangoutLink = "https://meet.google.com/abc-defg-hij"
			event.ConferenceData.CreateRequest.Status.StatusCode = "success"
			event.ConferenceData.ConferenceSolution.Key.Type = "hangoutsMeet"
			writeGoogleJSON(t, writer, event)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Start: "2026-08-01T10:00:00Z", End: "2026-08-01T11:00:00Z",
			OnlineMeeting: true,
		},
	)
	if err != nil ||
		created.OnlineMeetingJoinURL !=
			"https://meet.google.com/abc-defg-hij" ||
		createCalls != 1 || confirmationReads != 1 {
		t.Fatalf(
			"pending create = %+v error=%v creates=%d reads=%d",
			created,
			err,
			createCalls,
			confirmationReads,
		)
	}
}

func googleTestEvent(etag string) googleEvent {
	return googleEvent{
		ID: "e1", ETag: etag, Status: "confirmed",
		Summary: "Synthetic event",
		Start: googleEventTime{
			DateTime: "2026-08-01T10:00:00Z",
			TimeZone: "Europe/London",
		},
		End: googleEventTime{
			DateTime: "2026-08-01T11:00:00Z",
			TimeZone: "Europe/London",
		},
		Organizer: googleEventPerson{
			Email: "reader@example.test", Self: true,
		},
		Attendees: []googleEventPerson{{
			Email: "person@example.test", ResponseStatus: "accepted",
		}},
	}
}

func writeGoogleJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil &&
		!errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}
