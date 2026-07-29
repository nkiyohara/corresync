package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const CalendarMeetingUpdateModeProviderDefault = "provider_default"

// CalendarUpdateInput applies a closed patch to one exact event version.
// Nil fields remain unchanged; an empty pointed-to string clears that field.
type CalendarUpdateInput struct {
	Account           domain.AccountID    `json:"account"`
	EventID           string              `json:"eventId"`
	ChangeKey         string              `json:"changeKey"`
	Subject           *string             `json:"subject,omitempty"`
	Body              *string             `json:"body,omitempty"`
	Start             *string             `json:"start,omitempty"`
	End               *string             `json:"end,omitempty"`
	TimeZone          *string             `json:"timeZone,omitempty"`
	Location          *string             `json:"location,omitempty"`
	AllDay            *bool               `json:"allDay,omitempty"`
	Reminder          *CalendarReminder   `json:"reminder,omitempty"`
	ReplaceRecurrence bool                `json:"replaceRecurrence,omitempty"`
	Recurrence        *CalendarRecurrence `json:"recurrence,omitempty"`
	ReplaceAttendees  bool                `json:"replaceAttendees,omitempty"`
	RequiredAttendees []string            `json:"requiredAttendees,omitempty"`
	OptionalAttendees []string            `json:"optionalAttendees,omitempty"`
}

// CalendarUpdateReview displays the exact patch without exposing an unbounded
// body. MeetingUpdateMode records how the selected provider controls attendee
// notification behavior.
type CalendarUpdateReview struct {
	EventID                string              `json:"eventId"`
	ChangeKey              string              `json:"changeKey"`
	Subject                *string             `json:"subject,omitempty"`
	Body                   *CalendarBodyReview `json:"body,omitempty"`
	Start                  *string             `json:"start,omitempty"`
	End                    *string             `json:"end,omitempty"`
	TimeZone               *string             `json:"timeZone,omitempty"`
	Location               *string             `json:"location,omitempty"`
	AllDay                 *bool               `json:"allDay,omitempty"`
	Reminder               *CalendarReminder   `json:"reminder,omitempty"`
	ReplaceRecurrence      bool                `json:"replaceRecurrence"`
	Recurrence             *CalendarRecurrence `json:"recurrence,omitempty"`
	ReplaceAttendees       bool                `json:"replaceAttendees"`
	RequiredAttendees      []string            `json:"requiredAttendees,omitempty"`
	OptionalAttendees      []string            `json:"optionalAttendees,omitempty"`
	AttendeeUpdatesMaySend bool                `json:"attendeeUpdatesMaySend"`
	MeetingUpdateMode      string              `json:"meetingUpdateMode"`
}

// CalendarUpdateResult contains a refreshed identity when the provider returns
// one.
type CalendarUpdateResult struct {
	ID         string            `json:"id,omitempty"`
	ChangeKey  string            `json:"changeKey,omitempty"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// CalendarUpdateAccess is an immutable preview or a completed update.
type CalendarUpdateAccess struct {
	Status  string                `json:"status"`
	Updated *CalendarUpdateResult `json:"updated,omitempty"`
	Review  CalendarUpdateReview  `json:"review"`
	Preview *approval.Preview     `json:"preview,omitempty"`
}

// CalendarUpdater is the narrow provider-neutral port for supported fields.
type CalendarUpdater interface {
	UpdateCalendarEvent(context.Context, CalendarUpdateInput) (CalendarUpdateResult, error)
}

// Update always prepares an exact external-write preview.
func (service *CalendarService) Update(
	ctx context.Context,
	input CalendarUpdateInput,
	caller domain.Caller,
) (CalendarUpdateAccess, error) {
	if err := input.ValidateWithAttendeeLimit(service.maxAttendees); err != nil {
		return CalendarUpdateAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"calendar.update",
		domain.EffectExternalWrite,
		input.Account,
		calendarEventTarget(input.EventID),
		input,
	)
	if err != nil {
		return CalendarUpdateAccess{}, fmt.Errorf("create calendar update operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return CalendarUpdateAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return CalendarUpdateAccess{
			Status: "approval_required",
			Review: input.reviewWithEffects(service.effects), Preview: prepared.Preview,
		}, nil
	case policy.VerdictDeny:
		return CalendarUpdateAccess{}, errors.New("calendar update operation was denied")
	case policy.VerdictAllow:
		return CalendarUpdateAccess{}, errors.New("calendar update policy attempted to bypass mandatory preview")
	default:
		return CalendarUpdateAccess{}, errors.New("calendar update operation received an unknown policy verdict")
	}
}

// CommitUpdate consumes one caller-bound preview and submits its exact patch.
func (service *CalendarService) CommitUpdate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (CalendarUpdateAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "calendar.update", domain.EffectExternalWrite,
	)
	if err != nil {
		return CalendarUpdateAccess{}, err
	}
	var input CalendarUpdateInput
	if err := operation.DecodePayload(&input); err != nil {
		return CalendarUpdateAccess{}, err
	}
	if err := input.ValidateWithAttendeeLimit(service.maxAttendees); err != nil {
		return CalendarUpdateAccess{}, err
	}
	updated, err := service.executeUpdate(ctx, input, caller, operation)
	if err != nil {
		return CalendarUpdateAccess{}, err
	}
	return CalendarUpdateAccess{
		Status: "updated", Updated: &updated,
		Review: input.reviewWithEffects(service.effects),
	}, nil
}

func (service *CalendarService) executeUpdate(
	ctx context.Context,
	input CalendarUpdateInput,
	caller domain.Caller,
	operation domain.Operation,
) (CalendarUpdateResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return CalendarUpdateResult{}, err
	}
	updated, callErr := service.updater.UpdateCalendarEvent(ctx, input)
	outcome, reason := calendarWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return CalendarUpdateResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		updated.Provenance = service.calendarProvenance(updated.ID)
	}
	return updated, nil
}

// Validate rejects empty patches, stale-unsafe identities, and unsupported
// independent start/end changes before policy or network use.
func (input CalendarUpdateInput) Validate() error {
	return input.ValidateWithAttendeeLimit(MaxCalendarAttendees)
}

// ValidateWithAttendeeLimit applies the configured attendee bound in addition
// to the absolute protocol limit.
func (input CalendarUpdateInput) ValidateWithAttendeeLimit(maxAttendees int) error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if maxAttendees < 1 || maxAttendees > MaxCalendarAttendees {
		return errors.New("invalid calendar attendee limit")
	}
	if err := validateOpaqueValue("calendar event ID", input.EventID); err != nil {
		return err
	}
	if err := validateOpaqueValue("calendar event change key", input.ChangeKey); err != nil {
		return err
	}
	if input.Subject == nil && input.Body == nil && input.Start == nil &&
		input.End == nil && input.TimeZone == nil && input.Location == nil &&
		input.AllDay == nil && input.Reminder == nil &&
		!input.ReplaceRecurrence && !input.ReplaceAttendees {
		return errors.New("calendar update must change at least one supported field")
	}
	if input.Subject != nil && (!utf8.ValidString(*input.Subject) ||
		len(*input.Subject) > MaxCalendarSubjectBytes || strings.ContainsAny(*input.Subject, "\r\n\x00")) {
		return errors.New("calendar subject is malformed or too large")
	}
	if input.Body != nil && (!utf8.ValidString(*input.Body) ||
		len(*input.Body) > MaxCalendarBodyBytes || strings.ContainsRune(*input.Body, '\x00')) {
		return errors.New("calendar body is malformed or too large")
	}
	if input.Location != nil && (!utf8.ValidString(*input.Location) ||
		len(*input.Location) > MaxCalendarLocationBytes || strings.ContainsAny(*input.Location, "\r\n\x00")) {
		return errors.New("calendar location is malformed or too large")
	}
	if (input.Start == nil) != (input.End == nil) {
		return errors.New("calendar start and end must be updated together")
	}
	if input.TimeZone != nil {
		if input.Start == nil {
			return errors.New("calendar time zone can only be updated with start and end")
		}
		if len(*input.TimeZone) > 128 || *input.TimeZone == "" || strings.TrimSpace(*input.TimeZone) != *input.TimeZone ||
			strings.ContainsAny(*input.TimeZone, "\r\n\x00") {
			return errors.New("calendar time zone is malformed")
		}
	}
	if input.Start != nil {
		start, err := time.Parse(time.RFC3339, *input.Start)
		if err != nil {
			return errors.New("calendar start must be RFC3339")
		}
		end, err := time.Parse(time.RFC3339, *input.End)
		if err != nil {
			return errors.New("calendar end must be RFC3339")
		}
		duration := end.Sub(start)
		if duration <= 0 {
			return errors.New("calendar end must be after start")
		}
		if duration > MaxCalendarEventDuration {
			return fmt.Errorf("calendar event duration must not exceed %s", MaxCalendarEventDuration)
		}
		if input.AllDay != nil && *input.AllDay {
			zone := ""
			if input.TimeZone != nil {
				zone = *input.TimeZone
			}
			if !isCalendarMidnight(calendarBoundaryForTimeZone(start, zone)) ||
				!isCalendarMidnight(calendarBoundaryForTimeZone(end, zone)) {
				return errors.New("all-day calendar start and end must be midnight boundaries in the reviewed time zone")
			}
		}
	} else if input.AllDay != nil && *input.AllDay {
		return errors.New("enabling all-day requires start and end midnight boundaries")
	}
	if input.Reminder != nil {
		if input.Reminder.MinutesBeforeStart < 0 || input.Reminder.MinutesBeforeStart > MaxCalendarReminderMinutes {
			return fmt.Errorf("calendar reminder must be between 0 and %d minutes", MaxCalendarReminderMinutes)
		}
		if !input.Reminder.Enabled && input.Reminder.MinutesBeforeStart != 0 {
			return errors.New("disabled calendar reminder must use zero minutes")
		}
	}
	if input.Recurrence != nil && !input.ReplaceRecurrence {
		return errors.New(
			"calendar recurrence requires replaceRecurrence=true",
		)
	}
	if input.ReplaceRecurrence {
		if input.Start == nil {
			return errors.New(
				"calendar recurrence replacement requires start and end",
			)
		}
		if input.Recurrence != nil {
			start, _ := time.Parse(time.RFC3339, *input.Start)
			zone := ""
			if input.TimeZone != nil {
				zone = *input.TimeZone
			}
			if err := input.Recurrence.Validate(
				calendarBoundaryForTimeZone(start, zone),
			); err != nil {
				return err
			}
		}
	}
	if !input.ReplaceAttendees && (len(input.RequiredAttendees) != 0 || len(input.OptionalAttendees) != 0) {
		return errors.New("calendar attendee lists require replaceAttendees=true")
	}
	if input.ReplaceAttendees {
		attendees := append(append([]string(nil), input.RequiredAttendees...), input.OptionalAttendees...)
		if len(attendees) > maxAttendees {
			return fmt.Errorf("calendar event has %d attendees; maximum is %d", len(attendees), maxAttendees)
		}
		seen := make(map[string]struct{}, len(attendees))
		for _, attendee := range attendees {
			if err := validateMailAddress(attendee); err != nil {
				return fmt.Errorf("validate calendar attendee: %w", err)
			}
			normalized := strings.ToLower(attendee)
			if _, exists := seen[normalized]; exists {
				return fmt.Errorf("calendar attendee %q appears more than once", attendee)
			}
			seen[normalized] = struct{}{}
		}
	}
	return nil
}

// Review returns an immutable bounded representation of the exact patch.
func (input CalendarUpdateInput) Review() CalendarUpdateReview {
	return input.reviewWithEffects(CalendarEffects{
		UpdateAttendeeNotifications: true,
		CancellationMode:            CalendarCancellationProviderManaged,
		CancellationDisposition:     CalendarDispositionRemoteDelete,
	})
}

func (input CalendarUpdateInput) reviewWithEffects(
	effects CalendarEffects,
) CalendarUpdateReview {
	review := CalendarUpdateReview{
		EventID: input.EventID, ChangeKey: input.ChangeKey,
		Subject: cloneString(input.Subject), Start: cloneString(input.Start),
		End: cloneString(input.End), TimeZone: cloneString(input.TimeZone), Location: cloneString(input.Location),
		AllDay: cloneBool(input.AllDay), Reminder: cloneCalendarReminder(input.Reminder),
		ReplaceRecurrence:      input.ReplaceRecurrence,
		Recurrence:             cloneCalendarRecurrence(input.Recurrence),
		ReplaceAttendees:       input.ReplaceAttendees,
		RequiredAttendees:      append([]string(nil), input.RequiredAttendees...),
		OptionalAttendees:      append([]string(nil), input.OptionalAttendees...),
		AttendeeUpdatesMaySend: effects.UpdateAttendeeNotifications,
		MeetingUpdateMode:      CalendarMeetingUpdateModeProviderDefault,
	}
	if input.Body != nil {
		body := reviewCalendarBody(*input.Body)
		review.Body = &body
	}
	return review
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func calendarWriteAuditOutcome(callErr error) (AuditOutcome, string) {
	if callErr == nil {
		return AuditOutcomeSuccess, "completed"
	}
	if errors.Is(callErr, ErrWriteOutcomeUnknown) {
		return AuditOutcomeUnknown, "outcome_unknown"
	}
	return AuditOutcomeFailure, "transport_error"
}
