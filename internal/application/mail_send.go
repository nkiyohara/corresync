package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// MailSendInput is one new message or response. Sending always requires an
// exact preview, regardless of configurable read/reversible-write preferences.
type MailSendInput struct {
	Account            domain.AccountID     `json:"account"`
	To                 []string             `json:"to,omitempty"`
	CC                 []string             `json:"cc,omitempty"`
	BCC                []string             `json:"bcc,omitempty"`
	Subject            string               `json:"subject,omitempty"`
	Body               string               `json:"body,omitempty"`
	BodyFormat         MailBodyFormat       `json:"bodyFormat,omitempty"`
	ComposeMode        MailComposeMode      `json:"composeMode,omitempty"`
	ReferenceMessageID string               `json:"referenceMessageId,omitempty"`
	ReferenceChangeKey string               `json:"referenceChangeKey,omitempty"`
	Attachments        []MailFileAttachment `json:"attachments,omitempty"`
}

// MailSendResult identifies a sent copy only when the provider returns one.
// A successful SendAndSaveCopy response is allowed to omit it.
type MailSendResult struct {
	ID         string            `json:"id,omitempty"`
	ChangeKey  string            `json:"changeKey,omitempty"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// MailSendAccess is either an immutable approval preview or a completed send.
type MailSendAccess struct {
	Status  string            `json:"status"`
	Sent    *MailSendResult   `json:"sent,omitempty"`
	Review  MailReview        `json:"review"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

// MailSender is the narrow provider-neutral port for one external message.
type MailSender interface {
	SendMail(context.Context, MailSendInput) (MailSendResult, error)
}

// Send prepares an external write. Current policy always returns a preview and
// never executes from this method.
func (service *MailService) Send(
	ctx context.Context,
	input MailSendInput,
	caller domain.Caller,
) (MailSendAccess, error) {
	if err := input.Validate(service.maxRecipients); err != nil {
		return MailSendAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"mail.send",
		domain.EffectExternalWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailSendAccess{}, fmt.Errorf("create mail send operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailSendAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return MailSendAccess{
			Status: "approval_required", Review: input.Review(), Preview: prepared.Preview,
		}, nil
	case policy.VerdictDeny:
		return MailSendAccess{}, errors.New("mail send operation was denied")
	case policy.VerdictAllow:
		return MailSendAccess{}, errors.New("mail send policy attempted to bypass mandatory preview")
	default:
		return MailSendAccess{}, errors.New("mail send operation received an unknown policy verdict")
	}
}

// CommitSend consumes a caller-bound preview and sends its immutable content.
func (service *MailService) CommitSend(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailSendAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.send", domain.EffectExternalWrite,
	)
	if err != nil {
		return MailSendAccess{}, err
	}
	var input MailSendInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailSendAccess{}, err
	}
	if err := input.Validate(service.maxRecipients); err != nil {
		return MailSendAccess{}, err
	}
	sent, err := service.executeSend(ctx, input, caller, operation)
	if err != nil {
		return MailSendAccess{}, err
	}
	return MailSendAccess{
		Status: "sent", Sent: &sent, Review: input.Review(),
	}, nil
}

func (service *MailService) executeSend(
	ctx context.Context,
	input MailSendInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailSendResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailSendResult{}, err
	}
	sent, callErr := service.sender.SendMail(ctx, input)
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
		if errors.Is(callErr, ErrWriteOutcomeUnknown) {
			outcome = AuditOutcomeUnknown
			reason = "outcome_unknown"
		}
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailSendResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		sent.Provenance = service.mailProvenance(sent.ID)
	}
	return sent, nil
}

// Validate applies the same injection and size bounds as drafts, and requires
// at least one recipient before a send preview can be issued.
func (input MailSendInput) Validate(maxRecipients int) error {
	composition := input.asDraftInput()
	if err := composition.Validate(maxRecipients); err != nil {
		return err
	}
	if composition.EffectiveComposeMode() == MailComposeNew && len(input.To)+len(input.CC)+len(input.BCC) == 0 {
		return errors.New("mail send requires at least one recipient")
	}
	return nil
}

// Review binds the complete body while bounding visible preview text.
func (input MailSendInput) Review() MailReview { return input.asDraftInput().Review() }

func (input MailSendInput) asDraftInput() MailDraftInput {
	return MailDraftInput{
		Account: input.Account, To: input.To, CC: input.CC, BCC: input.BCC,
		Subject: input.Subject, Body: input.Body, BodyFormat: input.BodyFormat,
		ComposeMode: input.ComposeMode, ReferenceMessageID: input.ReferenceMessageID,
		ReferenceChangeKey: input.ReferenceChangeKey,
		Attachments:        append([]MailFileAttachment(nil), input.Attachments...),
	}
}
