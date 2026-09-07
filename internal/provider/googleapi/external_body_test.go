package googleapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestGmailReadsExternalizedTextBodiesWithoutTurningFilesIntoBody(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, mime, disposition, filename, data, want string
		size, reads                                   int
	}{
		{name: "plain", mime: "text/plain", data: "aGVsbG8=", want: "hello", size: 5, reads: 1},
		{name: "html", mime: "text/html", data: "PGI-aGVsbG88L2I-", want: "hello", size: 12, reads: 1},
		{name: "named_text_file", mime: "text/plain", filename: "file.txt", data: "aGVsbG8=", size: 5},
		{name: "attachment_disposition", mime: "text/plain", disposition: "attachment", data: "aGVsbG8=", size: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/gmail/v1/users/me/messages/message/attachments/body" {
					reads++
					writeGoogleJSON(t, w, gmailBody{Size: test.size, Data: test.data})
					return
				}
				writeGoogleJSON(t, w, gmailMessage{ID: "message", ThreadID: "thread", HistoryID: "1", Payload: gmailPart{MimeType: test.mime, Filename: test.filename, Headers: []gmailHeader{{Name: "Content-Disposition", Value: test.disposition}}, Body: gmailBody{Size: test.size, AttachmentID: "body"}}})
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
			wantAttachments := 1
			if test.reads != 0 {
				wantAttachments = 0
			}
			if err != nil || body.Text != test.want || reads != test.reads || len(body.Attachments) != wantAttachments {
				t.Fatalf("body=%+v, err=%v, external reads=%d", body, err, reads)
			}
		})
	}
}
func TestGmailExternalizedBodyRejectsOversizeBeforeFetchAndSizeMismatch(t *testing.T) {
	t.Parallel()
	for _, size := range []int{application.MaxMailBodyBytes + 1, 6} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			reads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/gmail/v1/users/me/messages/message/attachments/body" {
					reads++
					writeGoogleJSON(t, w, gmailBody{Size: 5, Data: "aGVsbG8="})
					return
				}
				writeGoogleJSON(t, w, gmailMessage{ID: "message", ThreadID: "thread", HistoryID: "1", Payload: gmailPart{MimeType: "text/plain", Body: gmailBody{Size: size, AttachmentID: "body"}}})
			}))
			defer server.Close()
			api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{api: api, mail: true}
			defer func() { _ = client.Close() }()
			id, _ := encodeMessageID("message")
			_, err = client.GetMessageBody(t.Context(), application.MailBodyInput{MessageID: id})
			if err == nil {
				t.Fatal("accepted invalid external body")
			}
			if size > application.MaxMailBodyBytes && reads != 0 {
				t.Fatalf("oversized body fetched %d times", reads)
			}
		})
	}
}
