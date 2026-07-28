package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// MailMoveInput moves exactly one versioned message to one folder.
type MailMoveInput struct {
	Account     domain.AccountID `json:"account"`
	MessageID   string           `json:"messageId"`
	ChangeKey   string           `json:"changeKey"`
	Destination MailFolder       `json:"destination"`
}

// MailMoveReview is the exact metadata shown before a policy-gated move.
type MailMoveReview struct {
	MessageID   string     `json:"messageId"`
	ChangeKey   string     `json:"changeKey"`
	Destination MailFolder `json:"destination"`
}

// MailMoveResult identifies the moved item when OWA returns its new identity.
type MailMoveResult struct {
	ID         string            `json:"id,omitempty"`
	ChangeKey  string            `json:"changeKey,omitempty"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// MailMoveAccess is either a completed move or an exact approval preview.
type MailMoveAccess struct {
	Status  string            `json:"status"`
	Moved   *MailMoveResult   `json:"moved,omitempty"`
	Review  MailMoveReview    `json:"review"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

// MailMover is the narrow OWA port for one move in the selected account.
type MailMover interface {
	MoveMail(context.Context, MailMoveInput) (MailMoveResult, error)
}

// Move prepares and, when policy allows, executes one exact move.
func (service *MailService) Move(
	ctx context.Context,
	input MailMoveInput,
	caller domain.Caller,
) (MailMoveAccess, error) {
	if err := input.Validate(); err != nil {
		return MailMoveAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"mail.move",
		domain.EffectReversibleWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailMoveAccess{}, fmt.Errorf("create mail move operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailMoveAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictAllow:
		moved, err := service.executeMove(ctx, input, caller, operation)
		if err != nil {
			return MailMoveAccess{}, err
		}
		return MailMoveAccess{Status: "completed", Moved: &moved, Review: input.Review()}, nil
	case policy.VerdictPreview:
		return MailMoveAccess{Status: "approval_required", Review: input.Review(), Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailMoveAccess{}, errors.New("mail move operation was denied")
	default:
		return MailMoveAccess{}, errors.New("mail move operation received an unknown policy verdict")
	}
}

// CommitMove consumes a caller-bound preview and moves its immutable item.
func (service *MailService) CommitMove(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailMoveAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.move", domain.EffectReversibleWrite,
	)
	if err != nil {
		return MailMoveAccess{}, err
	}
	var input MailMoveInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailMoveAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return MailMoveAccess{}, err
	}
	moved, err := service.executeMove(ctx, input, caller, operation)
	if err != nil {
		return MailMoveAccess{}, err
	}
	return MailMoveAccess{Status: "completed", Moved: &moved, Review: input.Review()}, nil
}

func (service *MailService) executeMove(
	ctx context.Context,
	input MailMoveInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailMoveResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailMoveResult{}, err
	}
	moved, callErr := service.mover.MoveMail(ctx, input)
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
		return MailMoveResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		moved.Provenance = service.mailProvenance(moved.ID)
	}
	return moved, nil
}

// Validate requires the versioned item identity returned by list or search.
func (input MailMoveInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("message ID", input.MessageID); err != nil {
		return err
	}
	if err := validateOpaqueValue("message change key", input.ChangeKey); err != nil {
		return err
	}
	return validateMessageFolder(input.Destination)
}

// Review returns an immutable display contract for adapters.
func (input MailMoveInput) Review() MailMoveReview {
	return MailMoveReview{
		MessageID: input.MessageID, ChangeKey: input.ChangeKey, Destination: input.Destination,
	}
}
