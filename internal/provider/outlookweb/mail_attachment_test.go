package outlookweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestCreateMailDraftAddsBoundedAttachmentAndRefreshesChangeKey(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		call := calls.Add(1)
		switch httpRequest.URL.Query().Get("action") {
		case "CreateItem":
			if call != 1 {
				t.Errorf("CreateItem call = %d", call)
			}
			_, _ = writer.Write(readFixture(t, "create_draft_response.json"))
		case "CreateAttachment":
			if call != 2 {
				t.Errorf("CreateAttachment call = %d", call)
			}
			var payload createAttachmentEnvelope
			if err := json.NewDecoder(httpRequest.Body).Decode(&payload); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if len(payload.Body.Attachments) != 1 || payload.Body.Attachments[0].Name != "fixture.txt" ||
				string(payload.Body.Attachments[0].Content) != "fixture" {
				t.Errorf("unexpected attachments: %+v", payload.Body.Attachments)
			}
			_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{
				"ResponseClass":"Success","ResponseCode":"NoError",
				"Attachments":[{"AttachmentId":{"Id":"attachment-1"}}],
				"RootItemId":"synthetic-draft-1","RootItemChangeKey":"synthetic-change-2"
			}]}}}`))
		default:
			t.Errorf("unexpected action %q", httpRequest.URL.Query().Get("action"))
		}
	}))
	defer server.Close()

	input := testDraftInput()
	input.Attachments = []application.MailFileAttachment{{
		Name: "fixture.txt", ContentType: "text/plain", Content: []byte("fixture"),
	}}
	draft, err := testClient(t, server, nil).CreateMailDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateMailDraft() error = %v", err)
	}
	if draft.ID != "synthetic-draft-1" || draft.ChangeKey != "synthetic-change-2" || calls.Load() != 2 {
		t.Fatalf("unexpected result: draft=%+v calls=%d", draft, calls.Load())
	}
}

func TestGetMailAttachmentReturnsBoundedDecodedContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("action") != "GetAttachment" {
			t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
		}
		var payload getAttachmentEnvelope
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(payload.Body.AttachmentIDs) != 1 ||
			payload.Body.AttachmentIDs[0].Type != "AttachmentIdType:#Exchange" ||
			payload.Body.AttachmentIDs[0].ID != "attachment-1" {
			t.Errorf("unexpected attachment IDs: %+v", payload.Body.AttachmentIDs)
		}
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{
			"ResponseClass":"Success","ResponseCode":"NoError","Attachments":[{
				"__type":"FileAttachment:#Exchange","AttachmentId":{"Id":"attachment-1"},
				"Name":"fixture.txt","ContentType":"text/plain","Size":7,
				"IsInline":false,"Content":"Zml4dHVyZQ=="
			}]
		}]}}}`))
	}))
	defer server.Close()

	attachment, err := testClient(t, server, nil).GetMailAttachment(t.Context(), application.MailAttachmentInput{
		Account: "work", AttachmentID: "attachment-1",
	})
	if err != nil {
		t.Fatalf("GetMailAttachment() error = %v", err)
	}
	if attachment.Name != "fixture.txt" || attachment.ContentBase64 != "Zml4dHVyZQ==" || attachment.Size != 7 {
		t.Fatalf("unexpected attachment: %+v", attachment)
	}
}
