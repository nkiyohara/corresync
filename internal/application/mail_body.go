package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const MaxMailBodyBytes = 1 << 20

// MailBodyInput names exactly one message. Bulk body reads are not supported.
type MailBodyInput struct {
	Account   domain.AccountID `json:"account"`
	MessageID string           `json:"messageId"`
}

// MailBody is deliberately plain text and exposes only bounded attachment
// metadata, never attachment content or message headers.
type MailBody struct {
	ID          string                   `json:"id"`
	ChangeKey   string                   `json:"changeKey,omitempty"`
	Text        string                   `json:"text"`
	Attachments []MailAttachmentMetadata `json:"attachments,omitempty"`
	Provenance  domain.Provenance        `json:"provenance,omitempty"`
}

// MailBodyAccess represents either completed content or an approval preview.
type MailBodyAccess struct {
	Status  string            `json:"status"`
	Body    *MailBody         `json:"body,omitempty"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

// MailBodyReader is the narrow provider-neutral port for an explicit body read.
type MailBodyReader interface {
	GetMessageBody(context.Context, MailBodyInput) (MailBody, error)
}

// GetBody prepares an explicit sensitive read and executes it immediately only
// when policy allows. A preview remains usable by CommitBody in the same
// service process.
func (service *MailService) GetBody(
	ctx context.Context,
	input MailBodyInput,
	caller domain.Caller,
) (MailBodyAccess, error) {
	if err := input.Validate(); err != nil {
		return MailBodyAccess{}, err
	}
	operation, err := domain.NewOperation("mail.get_body", domain.EffectSensitiveRead, input.Account, input)
	if err != nil {
		return MailBodyAccess{}, fmt.Errorf("create mail body operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailBodyAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictAllow:
		body, err := service.executeBody(ctx, input, caller, operation)
		if err != nil {
			return MailBodyAccess{}, err
		}
		return MailBodyAccess{Status: "completed", Body: &body}, nil
	case policy.VerdictPreview:
		return MailBodyAccess{Status: "approval_required", Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailBodyAccess{}, errors.New("mail body operation was denied")
	default:
		return MailBodyAccess{}, errors.New("mail body operation received an unknown policy verdict")
	}
}

// CommitBody consumes a caller-bound preview and executes its immutable input.
func (service *MailService) CommitBody(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailBodyAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.get_body", domain.EffectSensitiveRead,
	)
	if err != nil {
		return MailBodyAccess{}, err
	}
	var input MailBodyInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailBodyAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return MailBodyAccess{}, err
	}
	body, err := service.executeBody(ctx, input, caller, operation)
	if err != nil {
		return MailBodyAccess{}, err
	}
	return MailBodyAccess{Status: "completed", Body: &body}, nil
}

func (service *MailService) executeBody(
	ctx context.Context,
	input MailBodyInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailBody, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailBody{}, err
	}
	body, callErr := service.bodyReader.GetMessageBody(ctx, input)
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase:     AuditPhaseExecuted,
		Outcome:   outcome,
		Reason:    reason,
		Caller:    caller,
		Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailBody{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		body.Provenance = service.mailProvenance(body.ID)
		for index := range body.Attachments {
			body.Attachments[index].Provenance = service.mailProvenance(
				body.Attachments[index].ID,
			)
		}
	}
	return body, nil
}

// Validate bounds the only caller-controlled selector.
func (input MailBodyInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	return validateOpaqueValue("message ID", input.MessageID)
}
