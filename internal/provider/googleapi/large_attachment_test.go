package googleapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestUnsupportedLargeAttachmentsFailBeforeAnyRemoteCall(t *testing.T) {
	client := &Client{} // No transport: any attempt at a remote call is a bug.
	attachments := []application.MailFileAttachment{{Name: "large.bin", Content: make([]byte, (2<<20)+1)}}
	_, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{Attachments: attachments})
	if err == nil || !strings.Contains(err.Error(), "this mail route") {
		t.Fatalf("draft: %v", err)
	}
	_, err = client.SendMail(t.Context(), application.MailSendInput{Attachments: attachments})
	if err == nil || !strings.Contains(err.Error(), "this mail route") {
		t.Fatalf("send: %v", err)
	}
}

func TestGmailSmallAttachmentsAndBodyFitEncodedTransport(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n <= 8<<20 {
			t.Errorf("encoded bytes=%d, err=%v", n, err)
		}
		_, _ = io.WriteString(w, `{"id":"d1","message":{"id":"m2","historyId":"102"}}`)
	}))
	defer server.Close()
	api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = api.Close() }()
	client := &Client{api: api, address: "sender@example.invalid", mail: true}
	input := application.MailDraftInput{Account: "work", To: []string{"reader@example.invalid"}, Body: strings.Repeat("あ", (1<<20)/3), Attachments: []application.MailFileAttachment{
		{Name: "one.bin", Content: make([]byte, 2<<20)},
		{Name: "two.bin", Content: make([]byte, 1<<20)},
	}}
	if err := input.Validate(application.MaxMailRecipients); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateMailDraft(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("write calls=%d", calls)
	}
}
