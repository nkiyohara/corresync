package outlookweb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func testSendInput() application.MailSendInput {
	return application.MailSendInput{
		Account: "work", To: []string{"alice@example.invalid"},
		Subject: "Synthetic send", Body: "Send body",
	}
}

func TestSendRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	actual := marshalJSON(t, buildSendEnvelope(testSendInput()))
	want := readFixture(t, "send_mail_request.json")
	assertJSONEqual(t, actual, want)
}

func TestSendMailAcceptsEmptySuccessItemsAndNeverRetries(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "send_mail_response.json")
	expectedRequest := readFixture(t, "send_mail_request.json")
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
	sent, err := client.SendMail(context.Background(), testSendInput())
	if err != nil {
		t.Fatalf("SendMail() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if sent.ID != "" || sent.ChangeKey != "" {
		t.Fatalf("unexpected sent copy: %+v", sent)
	}
	if calls.Load() != 1 {
		t.Fatalf("CreateItem send calls = %d, want exactly 1", calls.Load())
	}
}

func TestSendMailMarksMalformedSuccessResponseUnknown(t *testing.T) {
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
	_, err := client.SendMail(t.Context(), testSendInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("SendMail() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}

func TestSendMailAcceptsSuccessItemWithoutSentCopyIdentity(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError",
				"Items":[{"ItemId":{"Id":"","ChangeKey":""}}]
			}]}}
		}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	sent, err := client.SendMail(t.Context(), testSendInput())
	if err != nil {
		t.Fatalf("SendMail() error = %v", err)
	}
	if sent.ID != "" || sent.ChangeKey != "" {
		t.Fatalf("unexpected sent copy: %+v", sent)
	}
}

func TestSendMailWithAttachmentUsesDraftAttachSendPipeline(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		switch request.URL.Query().Get("action") {
		case "CreateItem":
			if call != 1 {
				t.Errorf("CreateItem call = %d", call)
			}
			_, _ = writer.Write(readFixture(t, "create_draft_response.json"))
		case "CreateAttachment":
			if call != 2 {
				t.Errorf("CreateAttachment call = %d", call)
			}
			_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError",
				"Attachments":[{"AttachmentId":{"Id":"attachment-1"}}],
				"RootItemId":"synthetic-draft-1","RootItemChangeKey":"synthetic-change-2"
			}]}}}`))
		case "SendItem":
			if call != 3 {
				t.Errorf("SendItem call = %d", call)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("ReadAll() error = %v", err)
			}
			if !containsBytes(body, []byte(`"Id":"synthetic-draft-1"`), []byte(`"ChangeKey":"synthetic-change-2"`)) {
				t.Errorf("SendItem did not use refreshed draft identity: %s", body)
			}
			_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError"
			}]}}}`))
		default:
			t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
		}
	}))
	defer server.Close()

	input := testSendInput()
	input.Attachments = []application.MailFileAttachment{{Name: "fixture.txt", Content: []byte("fixture")}}
	if _, err := testClient(t, server, nil).SendMail(t.Context(), input); err != nil {
		t.Fatalf("SendMail() error = %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("write calls = %d, want 3", calls.Load())
	}
}

func containsBytes(data []byte, values ...[]byte) bool {
	for _, value := range values {
		if !bytes.Contains(data, value) {
			return false
		}
	}
	return true
}

func TestSendMailRejectsChangeKeyWithoutSentCopyID(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError",
				"Items":[{"ItemId":{"Id":"","ChangeKey":"synthetic-change"}}]
			}]}}
		}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.SendMail(t.Context(), testSendInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("SendMail() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
