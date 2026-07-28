package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const CalendarCancellationModeAll = "all_attendees_and_save_copy"

// CalendarCancelInput cancels one exact event version using the selected
// provider's reviewed disposition and attendee-notification semantics.
type CalendarCancelInput struct {
	Account   domain.AccountID `json:"account"`
	EventID   string           `json:"eventId"`
	ChangeKey string           `json:"changeKey"`
}

// CalendarCancelReview is the immutable destructive-operation review.
type CalendarCancelReview struct {
	EventID                      string `json:"eventId"`
	ChangeKey                    string `json:"changeKey"`
	CancellationMode             string `json:"cancellationMode"`
	DeleteType                   string `json:"deleteType"`
	AttendeeNotificationsMaySend bool   `json:"attendeeNotificationsMaySend"`
}

// CalendarCancelResult identifies the event requested for cancellation.
type CalendarCancelResult struct {
	ID         string            `json:"id"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// CalendarCancelAccess is a destructive preview or completed cancellation.
type CalendarCancelAccess struct {
	Status    string                `json:"status"`
	Cancelled *CalendarCancelResult `json:"cancelled,omitempty"`
	Review    CalendarCancelReview  `json:"review"`
	Preview   *approval.Preview     `json:"preview,omitempty"`
}

// CalendarCanceller is the narrow provider-neutral port for one cancellation.
type CalendarCanceller interface {
	CancelCalendarEvent(context.Context, CalendarCancelInput) error
}

// Cancel always prepares a destructive preview and never deletes directly.
func (service *CalendarService) Cancel(
	ctx context.Context,
	input CalendarCancelInput,
	caller domain.Caller,
) (CalendarCancelAccess, error) {
	if err := input.Validate(); err != nil {
		return CalendarCancelAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"calendar.cancel",
		domain.EffectDestructiveWrite,
		input.Account,
		calendarEventTarget(input.EventID),
		input,
	)
	if err != nil {
		return CalendarCancelAccess{}, fmt.Errorf("create calendar cancel operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return CalendarCancelAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return CalendarCancelAccess{
			Status: "approval_required",
			Review: input.reviewWithEffects(service.effects), Preview: prepared.Preview,
		}, nil
	case policy.VerdictDeny:
		return CalendarCancelAccess{}, errors.New("calendar cancel operation was denied")
	case policy.VerdictAllow:
		return CalendarCancelAccess{}, errors.New("calendar cancel policy attempted to bypass mandatory preview")
	default:
		return CalendarCancelAccess{}, errors.New("calendar cancel operation received an unknown policy verdict")
	}
}

// CommitCancel consumes a caller-bound preview and submits once.
func (service *CalendarService) CommitCancel(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (CalendarCancelAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "calendar.cancel", domain.EffectDestructiveWrite,
	)
	if err != nil {
		return CalendarCancelAccess{}, err
	}
	var input CalendarCancelInput
	if err := operation.DecodePayload(&input); err != nil {
		return CalendarCancelAccess{}, err
	}
	if err := input.Validate(); err != nil {
		return CalendarCancelAccess{}, err
	}
	if err := service.executeCancel(ctx, input, caller, operation); err != nil {
		return CalendarCancelAccess{}, err
	}
	return CalendarCancelAccess{
		Status: "cancelled",
		Cancelled: &CalendarCancelResult{
			ID: input.EventID, Provenance: service.calendarProvenance(input.EventID),
		},
		Review: input.reviewWithEffects(service.effects),
	}, nil
}

func (service *CalendarService) executeCancel(
	ctx context.Context,
	input CalendarCancelInput,
	caller domain.Caller,
	operation domain.Operation,
) error {
	if err := service.validateExecutionAccount(operation); err != nil {
		return err
	}
	callErr := service.canceller.CancelCalendarEvent(ctx, input)
	outcome, reason := calendarWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	return errors.Join(callErr, auditErr)
}

// Validate requires the exact event version returned by calendar list.
func (input CalendarCancelInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("calendar event ID", input.EventID); err != nil {
		return err
	}
	return validateOpaqueValue("calendar event change key", input.ChangeKey)
}

// Review describes the exact cancellation and deletion behavior.
func (input CalendarCancelInput) Review() CalendarCancelReview {
	return input.reviewWithEffects(CalendarEffects{
		CancelAttendeeNotifications: true,
		CancellationMode:            CalendarCancellationProviderManaged,
		CancellationDisposition:     CalendarDispositionRemoteDelete,
	})
}

func (input CalendarCancelInput) reviewWithEffects(
	effects CalendarEffects,
) CalendarCancelReview {
	return CalendarCancelReview{
		EventID: input.EventID, ChangeKey: input.ChangeKey,
		CancellationMode:             effects.CancellationMode,
		DeleteType:                   effects.CancellationDisposition,
		AttendeeNotificationsMaySend: effects.CancelAttendeeNotifications,
	}
}
