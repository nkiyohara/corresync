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

func validReadStateInput() application.MailReadStateInput {
	return application.MailReadStateInput{
		Account: "work", MessageID: "synthetic-message-1", ChangeKey: "synthetic-change-1",
		State: application.MailReadStateUnread,
	}
}

func TestUpdateReadStateRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload := buildUpdateReadStateEnvelope(validReadStateInput())
	actual, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, actual, readFixture(t, "update_read_state_request.json"))
}

func TestSetMailReadStateNormalizesResponse(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "update_read_state_response.json")
	expectedRequest := readFixture(t, "update_read_state_request.json")
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
	result, err := client.SetMailReadState(context.Background(), validReadStateInput())
	if err != nil {
		t.Fatalf("SetMailReadState() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if result.ID != "synthetic-message-1" || result.ChangeKey != "synthetic-change-2" ||
		result.State != application.MailReadStateUnread {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSetMailReadStateAllowsEmptySuccessItems(t *testing.T) {
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
	result, err := client.SetMailReadState(context.Background(), validReadStateInput())
	if err != nil || result.State != application.MailReadStateUnread || result.ID != "" {
		t.Fatalf("SetMailReadState() = %+v, %v", result, err)
	}
}

func TestSetMailReadStateMarksMalformedSuccessResponseUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[]}}}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.SetMailReadState(t.Context(), validReadStateInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("SetMailReadState() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
