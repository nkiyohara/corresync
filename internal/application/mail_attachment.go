package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const MaxMailAttachmentMetadata = 100

// MailAttachmentMetadata is bounded attachment metadata returned with an
// explicitly requested message body.
type MailAttachmentMetadata struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name,omitempty"`
	ContentType string            `json:"contentType,omitempty"`
	Size        int               `json:"size"`
	IsInline    bool              `json:"isInline"`
	ContentID   string            `json:"contentId,omitempty"`
	Provenance  domain.Provenance `json:"provenance,omitempty"`
}

// MailAttachmentInput names one exact attachment returned with a body read.
type MailAttachmentInput struct {
	Account      domain.AccountID `json:"account"`
	AttachmentID string           `json:"attachmentId"`
}

// MailAttachment contains one bounded file attachment with an explicit base64
// JSON contract shared by CLI, daemon IPC, and MCP.
type MailAttachment struct {
	MailAttachmentMetadata
	ContentBase64 string `json:"contentBase64"`
}

// MailAttachmentAccess represents completed content or a sensitive-read
// approval preview.
type MailAttachmentAccess struct {
	Status     string            `json:"status"`
	Attachment *MailAttachment   `json:"attachment,omitempty"`
	Preview    *approval.Preview `json:"preview,omitempty"`
}

// MailAttachmentReader fetches one explicit bounded attachment.
type MailAttachmentReader interface {
	GetMailAttachment(context.Context, MailAttachmentInput) (MailAttachment, error)
}

// GetAttachment applies the sensitive-read policy to one explicit attachment.
func (service *MailService) GetAttachment(
	ctx context.Context,
	input MailAttachmentInput,
	caller domain.Caller,
) (MailAttachmentAccess, error) {
	if err := input.Validate(); err != nil {
		return MailAttachmentAccess{}, err
	}
	operation, err := domain.NewOperation(
		"mail.get_attachment", domain.EffectSensitiveRead, input.Account, input,
	)
	if err != nil {
		return MailAttachmentAccess{}, fmt.Errorf("create mail attachment operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailAttachmentAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictAllow:
		attachment, err := service.executeAttachment(ctx, input, caller, operation)
		if err != nil {
			return MailAttachmentAccess{}, err
		}
		return MailAttachmentAccess{Status: "completed", Attachment: &attachment}, nil
	case policy.VerdictPreview:
		return MailAttachmentAccess{Status: "approval_required", Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailAttachmentAccess{}, errors.New("mail attachment operation was denied")
	default:
		return MailAttachmentAccess{}, errors.New("mail attachment operation received an unknown policy verdict")
	}
}

// CommitAttachment consumes a caller-bound preview for one immutable input.
func (service *MailService) CommitAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailAttachmentAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.get_attachment", domain.EffectSensitiveRead,
	)
	if err != nil {
		return MailAttachmentAccess{}, err
	}
	var input MailAttachmentInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailAttachmentAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return MailAttachmentAccess{}, err
	}
	attachment, err := service.executeAttachment(ctx, input, caller, operation)
	if err != nil {
		return MailAttachmentAccess{}, err
	}
	return MailAttachmentAccess{Status: "completed", Attachment: &attachment}, nil
}

func (service *MailService) executeAttachment(
	ctx context.Context,
	input MailAttachmentInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailAttachment, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailAttachment{}, err
	}
	attachment, callErr := service.attachmentReader.GetMailAttachment(ctx, input)
	outcome, reason := AuditOutcomeSuccess, "completed"
	if callErr != nil {
		outcome, reason = AuditOutcomeFailure, "transport_error"
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailAttachment{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		attachment.Provenance = service.mailProvenance(attachment.ID)
	}
	return attachment, nil
}

// Validate bounds the caller-controlled selector.
func (input MailAttachmentInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	return validateOpaqueValue("mail attachment ID", input.AttachmentID)
}
