package outlookweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func testCalendarCancelInput() application.CalendarCancelInput {
	return application.CalendarCancelInput{
		Account: "work", EventID: "synthetic-event-1", ChangeKey: "synthetic-change-1",
	}
}

func TestCalendarCancelRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	actual := marshalJSON(t, buildCalendarCancelEnvelope(testCalendarCancelInput()))
	assertJSONEqual(t, actual, readFixture(t, "cancel_calendar_event_request.json"))
}

func TestCancelCalendarEventAcceptsOneResponseAndNeverRetries(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "cancel_calendar_event_response.json")
	expectedRequest := readFixture(t, "cancel_calendar_event_request.json")
	requestBodies := make(chan []byte, 1)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
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
	if err := client.CancelCalendarEvent(context.Background(), testCalendarCancelInput()); err != nil {
		t.Fatalf("CancelCalendarEvent() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if calls.Load() != 1 {
		t.Fatalf("DeleteItem calls = %d, want exactly 1", calls.Load())
	}
}

func TestCalendarCancelValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := testCalendarCancelInput()
	input.ChangeKey = ""
	if err := client.CancelCalendarEvent(context.Background(), input); err == nil {
		t.Fatal("CancelCalendarEvent() unexpectedly accepted an unversioned event")
	}
}

func TestCalendarCancelMarksMalformedSuccessResponseUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[]}}}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	err := client.CancelCalendarEvent(t.Context(), testCalendarCancelInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CancelCalendarEvent() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
