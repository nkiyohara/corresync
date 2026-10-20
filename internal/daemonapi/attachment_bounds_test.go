package daemonapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestAttachmentReadFitsDaemonResponseAtMaximumSize(t *testing.T) {
	server := newTestServer(t, &fakeBackend{}, syntheticCredential("a"))
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", application.MaxMailAttachmentReadBytes)))
	result := application.MailAttachmentAccess{
		Status: "completed",
		Attachment: &application.MailAttachment{
			MailAttachmentMetadata: application.MailAttachmentMetadata{ID: "synthetic-attachment", Size: application.MaxMailAttachmentReadBytes},
			ContentBase64:          encoded,
		},
	}
	recorder := httptest.NewRecorder()
	server.writeResult(recorder, "synthetic-request", result)
	if recorder.Code != http.StatusOK {
		t.Fatalf("maximum attachment response status = %d", recorder.Code)
	}
	if recorder.Body.Len() <= 16<<20 || recorder.Body.Len() > maxResponseBytes {
		t.Fatalf("unexpected response size: %d", recorder.Body.Len())
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil {
		t.Fatal("maximum attachment was replaced with an error")
	}
	var decoded application.MailAttachmentAccess
	if err := json.Unmarshal(envelope.Result, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Attachment == nil || decoded.Attachment.ContentBase64 != encoded {
		t.Fatal("attachment content did not survive the response envelope")
	}
}
