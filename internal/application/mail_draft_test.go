package application

import (
	"context"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validDraftInput() MailDraftInput {
	return MailDraftInput{
		Account: "work",
		To:      []string{"alice@example.invalid"},
		Subject: "Synthetic draft",
		Body:    "Draft body",
	}
}

func TestMailDraftIsSaveOnlyReversibleWrite(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, recorder := testMailService(t, reader)
	access, err := service.CreateDraft(
		context.Background(), validDraftInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if access.Status != "completed" || access.Draft == nil || access.Draft.ID != "draft-1" {
		t.Fatalf("unexpected access: %+v", access)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Effect != domain.EffectReversibleWrite {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailDraftValidationBoundsRecipientsAndContent(t *testing.T) {
	t.Parallel()

	tests := []MailDraftInput{
		{},
		{Account: "work"},
		{Account: "work", To: []string{"Display Name <alice@example.invalid>"}},
		{Account: "work", To: []string{"alice@example.invalid\nBCC: injected@example.invalid"}},
		{Account: "work", Subject: "line one\nline two"},
		{Account: "work", Subject: string([]byte{0xff})},
		{Account: "work", Body: string([]byte{0xff})},
		{Account: "work", Body: strings.Repeat("x", MaxMailDraftBodyBytes+1)},
		{Account: "work", To: []string{"a@example.invalid", "b@example.invalid"}},
	}
	for index, input := range tests {
		limit := 20
		if index == len(tests)-1 {
			limit = 1
		}
		if err := input.Validate(limit); err == nil {
			t.Fatalf("Validate(%+v, %d) unexpectedly succeeded", input, limit)
		}
	}
}

func TestMailDraftReviewBoundsPreviewAndBindsFullBody(t *testing.T) {
	t.Parallel()

	input := validDraftInput()
	input.Body = strings.Repeat("界", 600)
	review := input.Review()
	if !strings.HasSuffix(review.BodyPreview, "…") || review.BodyBytes != len(input.Body) ||
		len(review.BodySHA256) != 64 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestMailDraftSupportsReviewedResponseHTMLAndAttachments(t *testing.T) {
	t.Parallel()

	input := MailDraftInput{
		Account: "work", Body: "<p>Thanks</p>", BodyFormat: MailBodyHTML,
		ComposeMode:        MailComposeReplyAll,
		ReferenceMessageID: "message-1", ReferenceChangeKey: "change-1",
		Attachments: []MailFileAttachment{{
			Name: "fixture.txt", ContentType: "text/plain", Content: []byte("fixture"),
		}},
	}
	if err := input.Validate(20); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	review := input.Review()
	if review.ComposeMode != MailComposeReplyAll || review.BodyFormat != MailBodyHTML ||
		len(review.Attachments) != 1 || review.Attachments[0].Bytes != 7 ||
		len(review.Attachments[0].SHA256) != 64 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestMailDraftRejectsAmbiguousResponseModesAndAttachments(t *testing.T) {
	t.Parallel()

	tests := []MailDraftInput{
		{Account: "work", ComposeMode: MailComposeReply, ReferenceMessageID: "message-1"},
		{Account: "work", ComposeMode: MailComposeReply, ReferenceMessageID: "message-1", ReferenceChangeKey: "change-1", To: []string{"a@example.invalid"}},
		{Account: "work", ComposeMode: MailComposeForward, ReferenceMessageID: "message-1", ReferenceChangeKey: "change-1"},
		{Account: "work", To: []string{"a@example.invalid"}, BodyFormat: "markdown"},
		{Account: "work", To: []string{"a@example.invalid"}, Attachments: []MailFileAttachment{{Name: "../fixture.txt", Content: []byte("x")}}},
		{Account: "work", To: []string{"a@example.invalid"}, Attachments: []MailFileAttachment{{Name: "fixture", Content: make([]byte, MaxMailAttachmentBytes+1)}}},
	}
	for _, input := range tests {
		if err := input.Validate(20); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", input)
		}
	}
}

func TestMailDraftApprovalCannotBeConsumedByBodyTool(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	guard, err := NewGuard(policy.Rules{
		Mode: policy.ModeGuarded, PreviewReversibleWrites: true,
	}, store, &memoryAudit{})
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	reader := &fakeMailReader{}
	service, err := NewMailService(guard, reader, testMailOptions())
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	preview, err := service.CreateDraft(t.Context(), validDraftInput(), caller)
	if err != nil || preview.Preview == nil {
		t.Fatalf("CreateDraft() = %+v, %v", preview, err)
	}
	if _, err := service.CommitBody(t.Context(), preview.Preview.Token, caller); err == nil {
		t.Fatal("CommitBody() unexpectedly accepted a draft approval")
	}
	committed, err := service.CommitDraft(t.Context(), preview.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitDraft() error = %v", err)
	}
	if committed.Draft == nil || committed.Draft.ID != "draft-1" || reader.calls != 1 {
		t.Fatalf("unexpected draft commit: %+v calls=%d", committed, reader.calls)
	}
}
