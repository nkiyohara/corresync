// Package application coordinates domain operations through explicit ports.
package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

var ErrDenied = errors.New("operation denied by policy")

// Preparation tells an adapter whether an operation is ready or needs approval.
type Preparation struct {
	Decision policy.Decision   `json:"decision"`
	Preview  *approval.Preview `json:"preview,omitempty"`
}

// Guard is the only path from an adapter to consequential execution.
type Guard struct {
	rules     policy.Rules
	approvals *approval.Store
	audit     AuditRecorder
}

// NewGuard validates policy and constructs the shared safety boundary.
func NewGuard(rules policy.Rules, approvals *approval.Store, audit AuditRecorder) (*Guard, error) {
	if err := rules.Validate(); err != nil {
		return nil, fmt.Errorf("validate policy rules: %w", err)
	}
	if approvals == nil {
		return nil, errors.New("approval store is required")
	}
	if audit == nil {
		return nil, errors.New("audit recorder is required")
	}
	return &Guard{rules: rules, approvals: approvals, audit: audit}, nil
}

// Prepare evaluates an operation and issues an exact preview when required.
func (guard *Guard) Prepare(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
) (Preparation, error) {
	if err := caller.Validate(); err != nil {
		return Preparation{}, fmt.Errorf("validate caller: %w", err)
	}
	decision := guard.rules.Evaluate(operation)
	if err := guard.audit.Record(ctx, AuditEvent{
		Phase:     AuditPhasePrepared,
		Outcome:   auditOutcome(decision.Verdict),
		Reason:    decision.Reason,
		Caller:    caller,
		Operation: operation.View(),
	}); err != nil {
		return Preparation{}, fmt.Errorf("record policy decision: %w", err)
	}
	switch decision.Verdict {
	case policy.VerdictAllow:
		return Preparation{Decision: decision}, nil
	case policy.VerdictPreview:
		preview, err := guard.approvals.Issue(operation, caller)
		if err != nil {
			return Preparation{}, fmt.Errorf("issue operation preview: %w", err)
		}
		return Preparation{Decision: decision, Preview: &preview}, nil
	case policy.VerdictDeny:
		return Preparation{Decision: decision}, fmt.Errorf("%w: %s", ErrDenied, decision.Reason)
	default:
		return Preparation{}, errors.New("policy returned an unknown verdict")
	}
}

// requirePreviewPreparation checks that policy permits constructing a
// content-derived preview before an adapter performs the read needed to build
// its exact payload. It records that content-free decision but never issues an
// approval token. Allowed callers must still pass Prepare with the final
// immutable operation.
func (guard *Guard) requirePreviewPreparation(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
) error {
	if err := caller.Validate(); err != nil {
		return fmt.Errorf("validate caller: %w", err)
	}
	decision := guard.rules.Evaluate(operation)
	switch decision.Verdict {
	case policy.VerdictPreview:
		if err := guard.audit.Record(ctx, AuditEvent{
			Phase: AuditPhasePrepared, Outcome: AuditOutcomePreview,
			Reason: decision.Reason, Caller: caller, Operation: operation.View(),
		}); err != nil {
			return fmt.Errorf("record preview preparation: %w", err)
		}
		return nil
	case policy.VerdictDeny:
		if err := guard.audit.Record(ctx, AuditEvent{
			Phase: AuditPhasePrepared, Outcome: AuditOutcomeDenied,
			Reason: decision.Reason, Caller: caller, Operation: operation.View(),
		}); err != nil {
			return fmt.Errorf("record policy decision: %w", err)
		}
		return fmt.Errorf("%w: %s", ErrDenied, decision.Reason)
	case policy.VerdictAllow:
		return errors.New("policy attempted to bypass mandatory preview preparation")
	default:
		return errors.New("policy returned an unknown preview preparation verdict")
	}
}

// Commit consumes an exact, single-use preview token.
func (guard *Guard) Commit(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (domain.Operation, error) {
	operation, err := guard.approvals.Consume(token, caller)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("consume operation preview: %w", err)
	}
	if err := guard.audit.Record(ctx, AuditEvent{
		Phase:     AuditPhaseCommitted,
		Outcome:   AuditOutcomeAllowed,
		Reason:    "approval_consumed",
		Caller:    caller,
		Operation: operation.View(),
	}); err != nil {
		return domain.Operation{}, fmt.Errorf("record operation commit: %w", err)
	}
	return operation, nil
}

// CommitFor consumes a token only when its immutable operation matches the
// expected application use case.
func (guard *Guard) CommitFor(
	ctx context.Context,
	token string,
	caller domain.Caller,
	name string,
	effect domain.Effect,
) (domain.Operation, error) {
	operation, err := guard.approvals.ConsumeFor(token, caller, name, effect)
	if err != nil {
		return domain.Operation{}, fmt.Errorf("consume operation preview: %w", err)
	}
	if err := guard.audit.Record(ctx, AuditEvent{
		Phase: AuditPhaseCommitted, Outcome: AuditOutcomeAllowed,
		Reason: "approval_consumed", Caller: caller, Operation: operation.View(),
	}); err != nil {
		return domain.Operation{}, fmt.Errorf("record operation commit: %w", err)
	}
	return operation, nil
}

// RecordExecution closes the audit lifecycle for an operation that already
// passed Prepare and, when required, Commit. Adapters use this after the typed
// application mutation returns so success and failure remain content-free.
func (guard *Guard) RecordExecution(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	executionErr error,
) error {
	if err := caller.Validate(); err != nil {
		return fmt.Errorf("validate caller: %w", err)
	}
	outcome := AuditOutcomeSuccess
	reason := "executed"
	if executionErr != nil {
		outcome = AuditOutcomeFailure
		reason = "execution_failed"
	}
	if err := guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase:     AuditPhaseExecuted,
		Outcome:   outcome,
		Reason:    reason,
		Caller:    caller,
		Operation: operation.View(),
	}); err != nil {
		return fmt.Errorf("record operation execution: %w", err)
	}
	return nil
}

func auditOutcome(verdict policy.Verdict) AuditOutcome {
	switch verdict {
	case policy.VerdictAllow:
		return AuditOutcomeAllowed
	case policy.VerdictPreview:
		return AuditOutcomePreview
	case policy.VerdictDeny:
		return AuditOutcomeDenied
	default:
		return AuditOutcomeUnknown
	}
}
