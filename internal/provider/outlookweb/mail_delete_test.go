package outlookweb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestDeleteMailBuildsHardDeleteAndNeverRetries(t *testing.T) {
	t.Parallel()

	input := application.MailDeleteInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1",
	}
	payload := buildMailDeleteEnvelope(input)
	if payload.Body.DeleteType != "HardDelete" || payload.Body.SendMeetingCancellations != "SendToNone" ||
		len(payload.Body.ItemIDs) != 1 || payload.Body.ItemIDs[0].ChangeKey != "change-1" {
		t.Fatalf("unexpected delete payload: %+v", payload)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Query().Get("action") != "DeleteItem" {
			t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
		}
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{
			"ResponseClass":"Success","ResponseCode":"NoError"
		}]}}}`))
	}))
	defer server.Close()
	if err := testClient(t, server, nil).DeleteMail(context.Background(), input); err != nil {
		t.Fatalf("DeleteMail() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("DeleteItem calls = %d, want 1", calls.Load())
	}
}

func TestDeleteMailMarksMalformedSuccessUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[]}}}`))
	}))
	defer server.Close()
	err := testClient(t, server, nil).DeleteMail(t.Context(), application.MailDeleteInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1",
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("DeleteMail() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
