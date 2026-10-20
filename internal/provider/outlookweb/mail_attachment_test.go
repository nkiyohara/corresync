package outlookweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestGetMailAttachmentReadsAboveTheSendBound(t *testing.T) {
	t.Parallel()

	// 13 MiB of content encodes to about 17.3 MiB of base64, which exceeds both
	// the 2 MiB send bound and the previous 16 MiB OWA response bound.
	content := bytes.Repeat([]byte("a"), 13<<20)
	encoded := base64.StdEncoding.EncodeToString(content)
	response := fmt.Sprintf(`{"Body":{"ResponseMessages":{"Items":[{
		"ResponseClass":"Success","ResponseCode":"NoError","Attachments":[{
			"__type":"FileAttachment:#Exchange","AttachmentId":{"Id":"attachment-1"},
			"Name":"fixture.pdf","ContentType":"application/pdf","Size":%d,
			"IsInline":false,"Content":%q
		}]
	}]}}}`, len(content), encoded)
	if len(response) <= 16<<20 {
		t.Fatalf("fixture response is %d bytes; it must exceed the previous 16 MiB bound", len(response))
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(response))
	}))
	defer server.Close()

	attachment, err := testClient(t, server, nil).GetMailAttachment(t.Context(), application.MailAttachmentInput{
		Account: "work", AttachmentID: "attachment-1",
	})
	if err != nil {
		t.Fatalf("GetMailAttachment() error = %v", err)
	}
	if attachment.Size != len(content) || attachment.ContentBase64 != encoded {
		t.Fatalf("unexpected attachment: size=%d content=%d bytes", attachment.Size, len(attachment.ContentBase64))
	}
}

func TestGetMailAttachmentRejectsAboveTheReadBound(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(writer, `{"Body":{"ResponseMessages":{"Items":[{
			"ResponseClass":"Success","ResponseCode":"NoError","Attachments":[{
				"__type":"FileAttachment:#Exchange","AttachmentId":{"Id":"attachment-1"},
				"Name":"fixture.bin","ContentType":"application/octet-stream","Size":%d,
				"IsInline":false,"Content":"Zml4dHVyZQ=="
			}]
		}]}}}`, application.MaxMailAttachmentReadBytes+1)
	}))
	defer server.Close()

	_, err := testClient(t, server, nil).GetMailAttachment(t.Context(), application.MailAttachmentInput{
		Account: "work", AttachmentID: "attachment-1",
	})
	if err == nil || !strings.Contains(err.Error(), "mail attachment exceeds") {
		t.Fatalf("GetMailAttachment() error = %v, want the read bound", err)
	}
}
