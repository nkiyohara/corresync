package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// MailReadState is a closed message state, not an arbitrary property name.
type MailReadState string

const (
	MailReadStateRead   MailReadState = "read"
	MailReadStateUnread MailReadState = "unread"
)

// MailReadStateInput updates one exact version of one message.
type MailReadStateInput struct {
	Account   domain.AccountID `json:"account"`
	MessageID string           `json:"messageId"`
	ChangeKey string           `json:"changeKey"`
	State     MailReadState    `json:"state"`
}

// MailReadStateReview is the immutable policy preview.
type MailReadStateReview struct {
	MessageID string        `json:"messageId"`
	ChangeKey string        `json:"changeKey"`
	State     MailReadState `json:"state"`
}

// MailReadStateResult contains a refreshed identity when OWA returns one.
type MailReadStateResult struct {
	ID         string            `json:"id,omitempty"`
	ChangeKey  string            `json:"changeKey,omitempty"`
	State      MailReadState     `json:"state"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// MailReadStateAccess is a completed update or a caller-bound preview.
type MailReadStateAccess struct {
	Status  string               `json:"status"`
	Updated *MailReadStateResult `json:"updated,omitempty"`
	Review  MailReadStateReview  `json:"review"`
	Preview *approval.Preview    `json:"preview,omitempty"`
}

// MailReadStateWriter is the narrow OWA port for the IsRead field.
type MailReadStateWriter interface {
	SetMailReadState(context.Context, MailReadStateInput) (MailReadStateResult, error)
}

// SetReadState prepares and, when policy permits, performs one update.
func (service *MailService) SetReadState(
	ctx context.Context,
	input MailReadStateInput,
	caller domain.Caller,
) (MailReadStateAccess, error) {
	if err := input.Validate(); err != nil {
		return MailReadStateAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"mail.set_read_state",
		domain.EffectReversibleWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailReadStateAccess{}, fmt.Errorf("create mail read-state operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailReadStateAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictAllow:
		updated, err := service.executeReadState(ctx, input, caller, operation)
		if err != nil {
			return MailReadStateAccess{}, err
		}
		return MailReadStateAccess{Status: "completed", Updated: &updated, Review: input.Review()}, nil
	case policy.VerdictPreview:
		return MailReadStateAccess{Status: "approval_required", Review: input.Review(), Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailReadStateAccess{}, errors.New("mail read-state operation was denied")
	default:
		return MailReadStateAccess{}, errors.New("mail read-state operation received an unknown policy verdict")
	}
}

// CommitReadState consumes one exact preview and performs its update once.
func (service *MailService) CommitReadState(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailReadStateAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.set_read_state", domain.EffectReversibleWrite,
	)
	if err != nil {
		return MailReadStateAccess{}, err
	}
	var input MailReadStateInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailReadStateAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return MailReadStateAccess{}, err
	}
	updated, err := service.executeReadState(ctx, input, caller, operation)
	if err != nil {
		return MailReadStateAccess{}, err
	}
	return MailReadStateAccess{Status: "completed", Updated: &updated, Review: input.Review()}, nil
}

func (service *MailService) executeReadState(
	ctx context.Context,
	input MailReadStateInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailReadStateResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailReadStateResult{}, err
	}
	updated, callErr := service.readState.SetMailReadState(ctx, input)
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
		return MailReadStateResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		updated.Provenance = service.mailProvenance(updated.ID)
	}
	return updated, nil
}

// Validate requires the exact version returned by list or search.
func (input MailReadStateInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("message ID", input.MessageID); err != nil {
		return err
	}
	if err := validateOpaqueValue("message change key", input.ChangeKey); err != nil {
		return err
	}
	switch input.State {
	case MailReadStateRead, MailReadStateUnread:
		return nil
	default:
		return fmt.Errorf("unsupported mail read state %q", input.State)
	}
}

// Review returns the exact version and requested state.
func (input MailReadStateInput) Review() MailReadStateReview {
	return MailReadStateReview{MessageID: input.MessageID, ChangeKey: input.ChangeKey, State: input.State}
}
