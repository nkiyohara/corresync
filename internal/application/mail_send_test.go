package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validSendInput() MailSendInput {
	return MailSendInput{
		Account: "work", To: []string{"alice@example.invalid"},
		Subject: "Synthetic send", Body: "Send body",
	}
}

func TestMailSendAlwaysPreviewsThenCommitsExactContent(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.DefaultRules(), store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	port := &fakeMailReader{}
	service, err := NewMailService(guard, port, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.Send(t.Context(), validSendInput(), caller)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || port.calls != 0 {
		t.Fatalf("unexpected preview: %+v calls=%d", access, port.calls)
	}
	if access.Preview.Operation.Effect != domain.EffectExternalWrite ||
		access.Preview.Operation.Name != "mail.send" || access.Review.BodySHA256 == "" {
		t.Fatalf("unsafe preview: %+v", access)
	}
	committed, err := service.CommitSend(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitSend() error = %v", err)
	}
	if committed.Status != "sent" || committed.Sent == nil || committed.Sent.ID != "sent-1" || port.calls != 1 {
		t.Fatalf("unexpected commit: %+v calls=%d", committed, port.calls)
	}
	if _, err := service.CommitSend(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitSend() replay unexpectedly succeeded")
	}
	if len(recorder.events) != 3 || recorder.events[1].Phase != AuditPhaseCommitted ||
		recorder.events[2].Phase != AuditPhaseExecuted {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailSendRejectsMissingRecipientAndWrongCaller(t *testing.T) {
	t.Parallel()

	invalid := validSendInput()
	invalid.To = nil
	if err := invalid.Validate(20); err == nil {
		t.Fatal("Validate() accepted a send without recipients")
	}

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	guard, err := NewGuard(policy.DefaultRules(), store, &memoryAudit{})
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	port := &fakeMailReader{}
	service, err := NewMailService(guard, port, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.Send(context.Background(), validSendInput(), caller)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	wrongCaller := domain.Caller{Surface: "cli", Instance: "process-2"}
	if _, err := service.CommitSend(t.Context(), access.Preview.Token, wrongCaller); err == nil {
		t.Fatal("CommitSend() accepted a different caller")
	}
	if port.calls != 0 {
		t.Fatalf("send calls = %d, want 0", port.calls)
	}
}

func TestMailSendReviewBindsResponseAndAttachment(t *testing.T) {
	t.Parallel()

	input := MailSendInput{
		Account: "work", To: []string{"alice@example.invalid"},
		ComposeMode: MailComposeForward, ReferenceMessageID: "message-1",
		ReferenceChangeKey: "change-1", BodyFormat: MailBodyHTML, Body: "<p>FYI</p>",
		Attachments: []MailFileAttachment{{Name: "fixture.txt", Content: []byte("fixture")}},
	}
	if err := input.Validate(20); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	review := input.Review()
	if review.ComposeMode != MailComposeForward || review.BodyFormat != MailBodyHTML ||
		len(review.Attachments) != 1 || review.Attachments[0].SHA256 == "" {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestMailSendAuditsAmbiguousOutcome(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.DefaultRules(), store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	port := &fakeMailReader{err: ErrWriteOutcomeUnknown}
	service, err := NewMailService(guard, port, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	preview, err := service.Send(t.Context(), validSendInput(), caller)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	_, err = service.CommitSend(t.Context(), preview.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitSend() error = %v, want ErrWriteOutcomeUnknown", err)
	}
	last := recorder.events[len(recorder.events)-1]
	if last.Outcome != AuditOutcomeUnknown || last.Reason != "outcome_unknown" {
		t.Fatalf("unexpected audit event: %+v", last)
	}
}

func TestMailSendApprovalCannotBeConsumedByDraftTool(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	guard, err := NewGuard(policy.DefaultRules(), store, &memoryAudit{})
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	port := &fakeMailReader{}
	service, err := NewMailService(guard, port, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	preview, err := service.Send(t.Context(), validSendInput(), caller)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if _, err := service.CommitDraft(t.Context(), preview.Preview.Token, caller); err == nil {
		t.Fatal("CommitDraft() unexpectedly accepted a send approval")
	}
	if _, err := service.CommitSend(t.Context(), preview.Preview.Token, caller); err != nil {
		t.Fatalf("CommitSend() error after mismatched tool = %v", err)
	}
}
