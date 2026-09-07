package googleapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestGmailAcceptsPaddedAndUnpaddedBodyAndAttachmentData(t *testing.T) {
	t.Parallel()
	for _, encoding := range []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding} {
		content := "hello"
		data := encoding.EncodeToString([]byte(content))
		t.Run(data, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/gmail/v1/users/me/messages/message/attachments/file" {
					writeGoogleJSON(t, w, gmailBody{Size: 5, Data: data})
					return
				}
				writeGoogleJSON(t, w, gmailMessage{ID: "message", ThreadID: "thread", HistoryID: "1", Payload: gmailPart{MimeType: "text/plain", Body: gmailBody{Size: 5, Data: data}}})
			}))
			defer server.Close()
			api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{api: api, mail: true}
			defer func() { _ = client.Close() }()
			id, _ := encodeMessageID("message")
			body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{MessageID: id})
			if err != nil || body.Text != content {
				t.Fatalf("body = %+v, %v", body, err)
			}
			aid, _ := encodeReference("gga1_", gmailAttachmentReference{MessageID: "message", HistoryID: "1", AttachmentID: "file", Size: 5, Name: "file.txt", ContentType: "text/plain"})
			attachment, err := client.GetMailAttachment(t.Context(), application.MailAttachmentInput{AttachmentID: aid})
			if err != nil || attachment.ContentBase64 != base64.StdEncoding.EncodeToString([]byte(content)) {
				t.Fatalf("attachment = %+v, %v", attachment, err)
			}
		})
	}
	for _, malformed := range []string{"aGVsbG8===", "a=GVsbG8", "aGVsbG8!", "a"} {
		if _, err := decodeGmailData(malformed); err == nil {
			t.Fatalf("accepted malformed base64url %q", malformed)
		}
	}
}
