package outlookweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func owaStringPointer(value string) *string { return &value }

func testCalendarUpdateInput() application.CalendarUpdateInput {
	return application.CalendarUpdateInput{
		Account: "work", EventID: "synthetic-event-1", ChangeKey: "synthetic-change-1",
		Subject:  owaStringPointer("Updated synthetic event"),
		Body:     owaStringPointer("Updated fixture body."),
		Start:    owaStringPointer("2026-07-20T09:00:00+01:00"),
		End:      owaStringPointer("2026-07-20T10:00:00+01:00"),
		Location: owaStringPointer("Room Updated"),
	}
}

func TestCalendarUpdateUsesOWASpecializedContract(t *testing.T) {
	t.Parallel()

	payload, err := buildCalendarUpdateEnvelope(testCalendarUpdateInput())
	if err != nil {
		t.Fatalf("buildCalendarUpdateEnvelope() error = %v", err)
	}
	got := make([]string, 0, len(payload.Body.ItemChange.Updates))
	for _, update := range payload.Body.ItemChange.Updates {
		got = append(got, update.Path.FieldURI)
	}
	want := []string{
		"Subject", "Body", "Start", "End", "StartTimeZoneId", "EndTimeZoneId", "Locations",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("calendar update field URIs = %v, want %v", got, want)
	}
	if payload.Type != "UpdateCalendarEventJsonRequest:#Exchange" ||
		payload.Body.Type != "UpdateCalendarEventRequest:#Exchange" || payload.Body.EventScope != 0 {
		t.Fatalf("unexpected calendar update action contract: %+v", payload)
	}
	if payload.Body.EventID != payload.Body.ItemChange.ItemID {
		t.Fatalf("event ID and item-change ID differ: %+v", payload.Body)
	}
}

func TestCalendarUpdateRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildCalendarUpdateEnvelope(testCalendarUpdateInput())
	if err != nil {
		t.Fatalf("buildCalendarUpdateEnvelope() error = %v", err)
	}
	assertJSONEqual(t, marshalJSON(t, payload), readFixture(t, "update_calendar_event_request.json"))
}

func TestUpdateCalendarEventNormalizesResponseAndNeverRetries(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "update_calendar_event_response.json")
	expectedRequest := readFixture(t, "update_calendar_event_request.json")
	requestBodies := make(chan []byte, 1)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if got := request.URL.Query().Get("action"); got != "UpdateCalendarEvent" {
			t.Errorf("action query = %q, want UpdateCalendarEvent", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requestBodies <- body
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	updated, err := client.UpdateCalendarEvent(context.Background(), testCalendarUpdateInput())
	if err != nil {
		t.Fatalf("UpdateCalendarEvent() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if updated.ID != "synthetic-event-1" || updated.ChangeKey != "synthetic-change-2" {
		t.Fatalf("unexpected updated event: %+v", updated)
	}
	if calls.Load() != 1 {
		t.Fatalf("UpdateCalendarEvent calls = %d, want exactly 1", calls.Load())
	}
}

func TestCalendarUpdateCanClearClosedTextFields(t *testing.T) {
	t.Parallel()

	input := application.CalendarUpdateInput{
		Account: "work", EventID: "event-1", ChangeKey: "change-1",
		Subject: owaStringPointer(""), Body: owaStringPointer(""), Location: owaStringPointer(""),
	}
	payload, err := buildCalendarUpdateEnvelope(input)
	if err != nil {
		t.Fatalf("buildCalendarUpdateEnvelope() error = %v", err)
	}
	updates := payload.Body.ItemChange.Updates
	if len(updates) != 3 || updates[0].Item.Subject == nil || *updates[0].Item.Subject != "" ||
		updates[1].Item.Body == nil || updates[1].Item.Body.Value != "" ||
		updates[2].Item.Locations == nil || len(*updates[2].Item.Locations) != 0 {
		t.Fatalf("clear fields were omitted or changed: %+v", updates)
	}
}

func TestCalendarUpdateBuildsReminderAllDayAndAttendeeReplacement(t *testing.T) {
	t.Parallel()

	allDay := true
	input := application.CalendarUpdateInput{
		Account: "work", EventID: "event-1", ChangeKey: "change-1",
		Start:    owaStringPointer("2026-07-20T00:00:00+01:00"),
		End:      owaStringPointer("2026-07-21T00:00:00+01:00"),
		TimeZone: owaStringPointer("GMT Standard Time"), AllDay: &allDay,
		Reminder:          &application.CalendarReminder{Enabled: true, MinutesBeforeStart: 15},
		ReplaceAttendees:  true,
		RequiredAttendees: []string{"alice@example.invalid"},
	}
	payload, err := buildCalendarUpdateEnvelope(input)
	if err != nil {
		t.Fatalf("buildCalendarUpdateEnvelope() error = %v", err)
	}
	got := make([]string, 0, len(payload.Body.ItemChange.Updates))
	for _, update := range payload.Body.ItemChange.Updates {
		got = append(got, update.Path.FieldURI)
	}
	want := []string{
		"Start", "End", "StartTimeZoneId", "EndTimeZoneId", "IsAllDayEvent",
		"ReminderIsSet", "ReminderMinutesBeforeStart", "RequiredAttendees", "OptionalAttendees",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("field URIs = %v, want %v", got, want)
	}
	if payload.Header.TimeZoneContext.TimeZoneDefinition.ID != "GMT Standard Time" ||
		payload.Body.ItemChange.Updates[0].Item.Start == nil ||
		*payload.Body.ItemChange.Updates[0].Item.Start != "2026-07-20T00:00:00.000" {
		t.Fatalf("unexpected time-zone request: %+v", payload)
	}
	optional := payload.Body.ItemChange.Updates[len(payload.Body.ItemChange.Updates)-1].Item.OptionalAttendees
	if optional == nil || *optional == nil || len(*optional) != 0 {
		t.Fatalf("empty optional attendee list was not explicit: %+v", optional)
	}
}

func TestCalendarUpdateValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := testCalendarUpdateInput()
	input.End = nil
	if _, err := client.UpdateCalendarEvent(context.Background(), input); err == nil {
		t.Fatal("UpdateCalendarEvent() unexpectedly accepted an incomplete time range")
	}
}

func TestCalendarUpdateMarksMalformedSuccessResponseUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[]}}}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.UpdateCalendarEvent(t.Context(), testCalendarUpdateInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("UpdateCalendarEvent() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}

func TestCalendarUpdateMarksSuccessWithoutUpdatedItemUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError","Items":[]
			}]}}
		}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.UpdateCalendarEvent(t.Context(), testCalendarUpdateInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("UpdateCalendarEvent() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
