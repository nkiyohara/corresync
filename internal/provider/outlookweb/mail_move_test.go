package outlookweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func validMoveInput() application.MailMoveInput {
	return application.MailMoveInput{
		Account: "work", MessageID: "synthetic-message-1", ChangeKey: "synthetic-change-1",
		Destination: application.MailFolder{Kind: application.MailFolderOpaque, ID: "synthetic-folder-1"},
	}
}

func TestMoveItemRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload := buildMoveItemEnvelope(validMoveInput())
	actual, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, actual, readFixture(t, "move_item_request.json"))
}

func TestMoveMailNormalizesReturnedIdentity(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "move_item_response.json")
	expectedRequest := readFixture(t, "move_item_request.json")
	requestBodies := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
	result, err := client.MoveMail(context.Background(), validMoveInput())
	if err != nil {
		t.Fatalf("MoveMail() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if result.ID != "synthetic-moved-message-1" || result.ChangeKey != "synthetic-change-2" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestMoveMailMakesOneAttemptAndReportsUnknownOutcome(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := testClient(t, server, func(options *Options) { options.ReadAttempts = 5 })
	_, err := client.MoveMail(context.Background(), validMoveInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("MoveMail() error = %v, want unknown outcome", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("MoveItem calls = %d, want 1", calls.Load())
	}
}

func TestMoveMailValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid move input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := validMoveInput()
	input.ChangeKey = ""
	if _, err := client.MoveMail(context.Background(), input); err == nil {
		t.Fatal("MoveMail() unexpectedly accepted an empty change key")
	}
}

func TestMoveMailRejectsMultipleReturnedItems(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError",
            "Items":[{"ItemId":{"Id":"one"}},{"ItemId":{"Id":"two"}}]
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.MoveMail(context.Background(), validMoveInput())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("too many")) {
		t.Fatalf("MoveMail() error = %v, want too many moved items", err)
	}
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("MoveMail() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
