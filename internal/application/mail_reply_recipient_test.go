package application

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestMailDirectRepliesRequireSavedDraftRecipientReview(t *testing.T) {
	for _, mode := range []MailComposeMode{MailComposeReply, MailComposeReplyAll} {
		t.Run(string(mode), func(t *testing.T) {
			port := &fakeMailReader{}
			service, recorder := testMailService(t, port)
			service.maxRecipients = 1
			input := validSendInput()
			input.To = nil
			input.ComposeMode = mode
			input.ReferenceMessageID = "reference"
			input.ReferenceChangeKey = "version"
			caller := domain.Caller{Surface: "cli", Instance: "derived-recipients"}
			access, err := service.Send(t.Context(), input, caller)
			if err == nil || !strings.Contains(err.Error(), "saved draft") || access.Preview != nil || port.calls != 0 || len(recorder.events) != 0 {
				t.Fatalf("access=%+v calls=%d error=%v", access, port.calls, err)
			}
			// A token prepared with the former direct-send policy must also fail closed.
			operation, err := domain.NewTargetedOperation("mail.send", domain.EffectExternalWrite, input.Account, configuredMailboxTarget(), input)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := service.guard.Prepare(t.Context(), operation, caller)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CommitSend(t.Context(), prepared.Preview.Token, caller); err == nil || port.calls != 0 {
				t.Fatalf("commit calls=%d error=%v", port.calls, err)
			}
			draft, err := service.CreateDraft(t.Context(), input.asDraftInput(), caller)
			if err != nil || draft.Draft == nil || port.calls != 1 {
				t.Fatalf("reply draft=%+v calls=%d error=%v", draft, port.calls, err)
			}
		})
	}
}
func TestMailExplicitRecipientSendsRemainAvailable(t *testing.T) {
	for _, mode := range []MailComposeMode{MailComposeNew, MailComposeForward} {
		t.Run(string(mode), func(t *testing.T) {
			port := &fakeMailReader{}
			service, _ := testMailService(t, port)
			service.maxRecipients = 1
			input := validSendInput()
			input.ComposeMode = mode
			if mode == MailComposeForward {
				input.ReferenceMessageID = "reference"
				input.ReferenceChangeKey = "version"
			}
			caller := domain.Caller{Surface: "cli", Instance: "explicit-recipients"}
			access, err := service.Send(t.Context(), input, caller)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CommitSend(t.Context(), access.Preview.Token, caller); err != nil || port.calls != 1 {
				t.Fatalf("calls=%d error=%v", port.calls, err)
			}
		})
	}
}
func TestMailSavedReplySnapshotEnforcesConfiguredRecipientLimit(t *testing.T) {
	port := &fakeMailReader{draft: validDraftSnapshot()}
	port.draft.CC = []string{"second@example.invalid"}
	service, _ := testMailService(t, port)
	service.maxRecipients = 1
	caller := domain.Caller{Surface: "cli", Instance: "saved-reply"}
	rejected, err := service.SendDraft(t.Context(), validDraftSendInput(), caller)
	if err == nil || !strings.Contains(err.Error(), "2 recipients; maximum is 1") || rejected.Preview != nil || port.draftSends != 0 {
		t.Fatalf("access=%+v sends=%d error=%v", rejected, port.draftSends, err)
	}
	service.maxRecipients = 2
	access, err := service.SendDraft(t.Context(), validDraftSendInput(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Review.To) != 1 || len(access.Review.CC) != 1 {
		t.Fatalf("review=%+v", access.Review)
	}
	if _, err := service.CommitSendDraft(t.Context(), access.Preview.Token, caller); err != nil || port.draftSends != 1 {
		t.Fatalf("sends=%d error=%v", port.draftSends, err)
	}
}
