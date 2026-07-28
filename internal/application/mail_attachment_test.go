package application

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestMailAttachmentSensitiveReadUsesPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, recorder := testMailService(t, reader)
	access, err := service.GetAttachment(t.Context(), MailAttachmentInput{
		Account: "work", AttachmentID: "attachment-1",
	}, domain.Caller{Surface: "mcp", Instance: "session-1"})
	if err != nil {
		t.Fatalf("GetAttachment() error = %v", err)
	}
	if access.Status != "completed" || access.Attachment == nil ||
		access.Attachment.ContentBase64 != "c3ludGhldGljIGF0dGFjaG1lbnQ=" || reader.calls != 1 {
		t.Fatalf("unexpected access: %+v calls=%d", access, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Name != "mail.get_attachment" ||
		recorder.events[0].Operation.Effect != domain.EffectSensitiveRead {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailAttachmentInputValidation(t *testing.T) {
	t.Parallel()

	for _, input := range []MailAttachmentInput{
		{},
		{Account: "work"},
		{Account: "work", AttachmentID: " bad "},
	} {
		if err := input.Validate(); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", input)
		}
	}
}
