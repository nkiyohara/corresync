package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// MailDeleteInput permanently deletes one exact message version. It is kept
// separate from reversible moves to Deleted Items.
type MailDeleteInput struct {
	Account   domain.AccountID `json:"account"`
	MessageID string           `json:"messageId"`
	ChangeKey string           `json:"changeKey"`
}

// MailDeleteReview makes the irreversible disposal mode explicit.
type MailDeleteReview struct {
	MessageID  string `json:"messageId"`
	ChangeKey  string `json:"changeKey"`
	DeleteType string `json:"deleteType"`
}

type MailDeleteResult struct {
	ID         string            `json:"id"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

type MailDeleteAccess struct {
	Status  string            `json:"status"`
	Deleted *MailDeleteResult `json:"deleted,omitempty"`
	Review  MailDeleteReview  `json:"review"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

// MailDeleter is the narrow transport port for irreversible deletion.
type MailDeleter interface {
	DeleteMail(context.Context, MailDeleteInput) error
}

// Delete always requires an exact destructive preview.
func (service *MailService) Delete(
	ctx context.Context,
	input MailDeleteInput,
	caller domain.Caller,
) (MailDeleteAccess, error) {
	if err := input.Validate(); err != nil {
		return MailDeleteAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"mail.delete",
		domain.EffectDestructiveWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailDeleteAccess{}, fmt.Errorf("create mail delete operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailDeleteAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return MailDeleteAccess{Status: "approval_required", Review: input.Review(), Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailDeleteAccess{}, errors.New("mail delete operation was denied")
	case policy.VerdictAllow:
		return MailDeleteAccess{}, errors.New("mail delete policy attempted to bypass mandatory preview")
	default:
		return MailDeleteAccess{}, errors.New("mail delete operation received an unknown policy verdict")
	}
}

func (service *MailService) CommitDelete(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailDeleteAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.delete", domain.EffectDestructiveWrite,
	)
	if err != nil {
		return MailDeleteAccess{}, err
	}
	var input MailDeleteInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailDeleteAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return MailDeleteAccess{}, err
	}
	callErr := service.deleter.DeleteMail(ctx, input)
	outcome, reason := mailWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailDeleteAccess{}, errors.Join(callErr, auditErr)
	}
	return MailDeleteAccess{
		Status: "deleted",
		Deleted: &MailDeleteResult{
			ID: input.MessageID, Provenance: service.mailProvenance(input.MessageID),
		},
		Review: input.Review(),
	}, nil
}

func (input MailDeleteInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("message ID", input.MessageID); err != nil {
		return err
	}
	return validateOpaqueValue("message change key", input.ChangeKey)
}

func (input MailDeleteInput) Review() MailDeleteReview {
	return MailDeleteReview{
		MessageID: input.MessageID, ChangeKey: input.ChangeKey, DeleteType: "hard_delete",
	}
}

func mailWriteAuditOutcome(callErr error) (AuditOutcome, string) {
	if callErr == nil {
		return AuditOutcomeSuccess, "completed"
	}
	if errors.Is(callErr, ErrWriteOutcomeUnknown) {
		return AuditOutcomeUnknown, "outcome_unknown"
	}
	return AuditOutcomeFailure, "transport_error"
}
