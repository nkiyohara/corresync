package jmap

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
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
