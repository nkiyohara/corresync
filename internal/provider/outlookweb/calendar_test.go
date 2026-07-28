package outlookweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func validCalendarInput() application.CalendarListInput {
	return application.CalendarListInput{
		Account:  "work",
		Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Start:    "2026-07-17T01:00:00+01:00",
		End:      "2026-07-18T01:00:00+01:00",
	}
}

func TestCalendarViewRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildCalendarViewEnvelope(validCalendarInput())
	if err != nil {
		t.Fatalf("buildCalendarViewEnvelope() error = %v", err)
	}
	actual := marshalJSON(t, payload)
	want := readFixture(t, "get_calendar_view_request.json")
	assertJSONEqual(t, actual, want)
}

func TestListCalendarEventsNormalizesGoldenResponse(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "get_calendar_view_response.json")
	expectedRequest := readFixture(t, "get_calendar_view_request.json")
	requestBodies := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requestBodies <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	page, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	if err != nil {
		t.Fatalf("ListCalendarEvents() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if len(page.Events) != 2 || page.Start != validCalendarInput().Start || page.End != validCalendarInput().End {
		t.Fatalf("unexpected page: %+v", page)
	}
	first := page.Events[0]
	if first.ID != "synthetic-event-1" || first.Start != "2026-07-17T09:00:00Z" ||
		first.OriginalStart != "2026-07-17T09:00:00.000" ||
		first.OriginalStartTimeZone != "UTC" ||
		first.Location != "Room 1" || first.Organizer.Address != "alice@example.invalid" ||
		!first.IsOnlineMeeting || first.IsCancelled {
		t.Fatalf("unexpected first event: %+v", first)
	}
	if page.Events[1].Location != "Home office" {
		t.Fatalf("string calendar location was not normalized: %+v", page.Events[1])
	}
}

func TestListCalendarEventsAcceptsResponseMessageVariant(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError",
            "CalendarView":{"Items":[]}
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	page, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	if err != nil {
		t.Fatalf("ListCalendarEvents() error = %v", err)
	}
	if page.Events == nil || len(page.Events) != 0 {
		t.Fatalf("unexpected empty page: %+v", page)
	}
}

func TestListCalendarEventsReturnsSanitizedProtocolError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Error","ResponseCode":"ErrorAccessDenied",
            "MessageText":"private server detail"
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.ResponseCode != "ErrorAccessDenied" {
		t.Fatalf("ListCalendarEvents() error = %v, want ErrorAccessDenied", err)
	}
}

func TestListCalendarEventsValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := validCalendarInput()
	input.End = input.Start
	if _, err := client.ListCalendarEvents(context.Background(), input); err == nil {
		t.Fatal("ListCalendarEvents() unexpectedly accepted an empty window")
	}
}

func TestCalendarViewPreservesOpaqueCalendarID(t *testing.T) {
	t.Parallel()

	input := validCalendarInput()
	input.Calendar = application.CalendarFolder{
		Kind: application.CalendarFolderOpaque,
		ID:   "AAMkCaseSensitiveCalendarID==",
	}
	payload, err := buildCalendarViewEnvelope(input)
	if err != nil {
		t.Fatalf("buildCalendarViewEnvelope() error = %v", err)
	}
	folder := payload.Body.CalendarID.BaseFolderID
	if folder.ID != input.Calendar.ID || folder.Type != "FolderId:#Exchange" {
		t.Fatalf("opaque calendar ID changed: %+v", folder)
	}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return data
}
