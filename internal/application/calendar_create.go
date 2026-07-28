package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const (
	MaxCalendarAttendees       = 1000
	MaxCalendarSubjectBytes    = 998
	MaxCalendarBodyBytes       = 1 << 20
	MaxCalendarLocationBytes   = 4096
	MaxCalendarEventDuration   = 31 * 24 * time.Hour
	MaxCalendarReminderMinutes = 28 * 24 * 60
	MaxCalendarRecurrenceCount = 999
	calendarBodyPreviewRunes   = 500
)

type CalendarReminder struct {
	Enabled            bool `json:"enabled"`
	MinutesBeforeStart int  `json:"minutesBeforeStart"`
}

type CalendarRecurrencePattern string

const (
	CalendarRecurrenceDaily           CalendarRecurrencePattern = "daily"
	CalendarRecurrenceWeekly          CalendarRecurrencePattern = "weekly"
	CalendarRecurrenceAbsoluteMonthly CalendarRecurrencePattern = "absolute_monthly"
	CalendarRecurrenceAbsoluteYearly  CalendarRecurrencePattern = "absolute_yearly"
)

type CalendarRecurrence struct {
	Pattern             CalendarRecurrencePattern `json:"pattern"`
	Interval            int                       `json:"interval,omitempty"`
	DaysOfWeek          []string                  `json:"daysOfWeek,omitempty"`
	DayOfMonth          int                       `json:"dayOfMonth,omitempty"`
	Month               string                    `json:"month,omitempty"`
	EndDate             string                    `json:"endDate,omitempty"`
	NumberOfOccurrences int                       `json:"numberOfOccurrences,omitempty"`
}

// CalendarCreateInput creates one bounded calendar item. Required and optional
// attendees turn it into a meeting invitation.
type CalendarCreateInput struct {
	Account           domain.AccountID    `json:"account"`
	Calendar          CalendarFolder      `json:"calendar"`
	Subject           string              `json:"subject,omitempty"`
	Body              string              `json:"body,omitempty"`
	Start             string              `json:"start"`
	End               string              `json:"end"`
	Location          string              `json:"location,omitempty"`
	RequiredAttendees []string            `json:"requiredAttendees,omitempty"`
	OptionalAttendees []string            `json:"optionalAttendees,omitempty"`
	TeamsMeeting      bool                `json:"teamsMeeting,omitempty"`
	AllDay            bool                `json:"allDay,omitempty"`
	TimeZone          string              `json:"timeZone,omitempty"`
	Reminder          *CalendarReminder   `json:"reminder,omitempty"`
	Recurrence        *CalendarRecurrence `json:"recurrence,omitempty"`
}

// CalendarCreateReview is the exact bounded review shown before creation.
type CalendarCreateReview struct {
	Calendar              CalendarFolder      `json:"calendar"`
	Subject               string              `json:"subject,omitempty"`
	BodyPreview           string              `json:"bodyPreview,omitempty"`
	BodyBytes             int                 `json:"bodyBytes"`
	BodySHA256            string              `json:"bodySha256"`
	Start                 string              `json:"start"`
	End                   string              `json:"end"`
	Location              string              `json:"location,omitempty"`
	RequiredAttendees     []string            `json:"requiredAttendees,omitempty"`
	OptionalAttendees     []string            `json:"optionalAttendees,omitempty"`
	InvitationsWillBeSent bool                `json:"invitationsWillBeSent"`
	TeamsMeeting          bool                `json:"teamsMeeting"`
	AllDay                bool                `json:"allDay"`
	TimeZone              string              `json:"timeZone"`
	Reminder              *CalendarReminder   `json:"reminder,omitempty"`
	Recurrence            *CalendarRecurrence `json:"recurrence,omitempty"`
}

// CalendarBodyReview exposes bounded text while binding the complete body.
type CalendarBodyReview struct {
	Preview string `json:"preview,omitempty"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// CalendarCreateResult identifies the created event returned by OWA.
type CalendarCreateResult struct {
	ID                    string            `json:"id"`
	ChangeKey             string            `json:"changeKey,omitempty"`
	IsOnlineMeeting       bool              `json:"isOnlineMeeting"`
	OnlineMeetingProvider string            `json:"onlineMeetingProvider,omitempty"`
	OnlineMeetingJoinURL  string            `json:"onlineMeetingJoinUrl,omitempty"`
	Provenance            domain.Provenance `json:"provenance,omitempty"`
}

// CalendarCreateAccess is either an immutable preview or a created event.
type CalendarCreateAccess struct {
	Status  string                `json:"status"`
	Created *CalendarCreateResult `json:"created,omitempty"`
	Review  CalendarCreateReview  `json:"review"`
	Preview *approval.Preview     `json:"preview,omitempty"`
}

// CalendarCreator is the narrow OWA port for one new event.
type CalendarCreator interface {
	CreateCalendarEvent(context.Context, CalendarCreateInput) (CalendarCreateResult, error)
}

// Create prepares a calendar write. It always requires an exact preview,
// including when no attendees are present.
func (service *CalendarService) Create(
	ctx context.Context,
	input CalendarCreateInput,
	caller domain.Caller,
) (CalendarCreateAccess, error) {
	if err := input.Validate(service.maxAttendees); err != nil {
		return CalendarCreateAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"calendar.create",
		domain.EffectExternalWrite,
		input.Account,
		calendarFolderTarget(input.Calendar),
		input,
	)
	if err != nil {
		return CalendarCreateAccess{}, fmt.Errorf("create calendar operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return CalendarCreateAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return CalendarCreateAccess{
			Status: "approval_required", Review: input.Review(), Preview: prepared.Preview,
		}, nil
	case policy.VerdictDeny:
		return CalendarCreateAccess{}, errors.New("calendar create operation was denied")
	case policy.VerdictAllow:
		return CalendarCreateAccess{}, errors.New("calendar create policy attempted to bypass mandatory preview")
	default:
		return CalendarCreateAccess{}, errors.New("calendar create operation received an unknown policy verdict")
	}
}

// CommitCreate consumes a caller-bound preview and submits its immutable event
// exactly once.
func (service *CalendarService) CommitCreate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (CalendarCreateAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "calendar.create", domain.EffectExternalWrite,
	)
	if err != nil {
		return CalendarCreateAccess{}, err
	}
	var input CalendarCreateInput
	if err := operation.DecodePayload(&input); err != nil {
		return CalendarCreateAccess{}, err
	}
	if err := input.Validate(service.maxAttendees); err != nil {
		return CalendarCreateAccess{}, err
	}
	created, err := service.executeCreate(ctx, input, caller, operation)
	if err != nil {
		return CalendarCreateAccess{}, err
	}
	return CalendarCreateAccess{
		Status: "created", Created: &created, Review: input.Review(),
	}, nil
}

func (service *CalendarService) executeCreate(
	ctx context.Context,
	input CalendarCreateInput,
	caller domain.Caller,
	operation domain.Operation,
) (CalendarCreateResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return CalendarCreateResult{}, err
	}
	created, callErr := service.creator.CreateCalendarEvent(ctx, input)
	outcome, reason := calendarWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return CalendarCreateResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		created.Provenance = service.calendarProvenance(created.ID)
	}
	return created, nil
}

// Validate bounds all content, addresses, and absolute times before policy or
// network use.
func (input CalendarCreateInput) Validate(maxAttendees int) error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateCalendarFolder(input.Calendar); err != nil {
		return err
	}
	if maxAttendees < 1 || maxAttendees > MaxCalendarAttendees {
		return errors.New("invalid calendar attendee limit")
	}
	if !utf8.ValidString(input.Subject) || len(input.Subject) > MaxCalendarSubjectBytes ||
		strings.ContainsAny(input.Subject, "\r\n\x00") {
		return errors.New("calendar subject is malformed or too large")
	}
	if !utf8.ValidString(input.Body) || len(input.Body) > MaxCalendarBodyBytes ||
		strings.ContainsRune(input.Body, '\x00') {
		return errors.New("calendar body is malformed or too large")
	}
	if !utf8.ValidString(input.Location) || len(input.Location) > MaxCalendarLocationBytes ||
		strings.ContainsAny(input.Location, "\r\n\x00") {
		return errors.New("calendar location is malformed or too large")
	}
	start, err := time.Parse(time.RFC3339, input.Start)
	if err != nil {
		return errors.New("calendar start must be RFC3339")
	}
	end, err := time.Parse(time.RFC3339, input.End)
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
	if len(input.TimeZone) > 128 || strings.TrimSpace(input.TimeZone) != input.TimeZone || strings.ContainsAny(input.TimeZone, "\r\n\x00") {
		return errors.New("calendar time zone is malformed")
	}
	if input.AllDay {
		boundaryStart := calendarBoundaryForTimeZone(start, input.TimeZone)
		boundaryEnd := calendarBoundaryForTimeZone(end, input.TimeZone)
		if !isCalendarMidnight(boundaryStart) || !isCalendarMidnight(boundaryEnd) {
			return errors.New("all-day calendar start and end must be midnight boundaries in the reviewed time zone")
		}
	}
	if input.Reminder != nil {
		if input.Reminder.MinutesBeforeStart < 0 || input.Reminder.MinutesBeforeStart > MaxCalendarReminderMinutes {
			return fmt.Errorf("calendar reminder must be between 0 and %d minutes", MaxCalendarReminderMinutes)
		}
		if !input.Reminder.Enabled && input.Reminder.MinutesBeforeStart != 0 {
			return errors.New("disabled calendar reminder must use zero minutes")
		}
	}
	if input.Recurrence != nil {
		if err := input.Recurrence.Validate(calendarBoundaryForTimeZone(start, input.TimeZone)); err != nil {
			return err
		}
	}
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
	return nil
}

// Review binds the complete body while limiting visible preview text.
func (input CalendarCreateInput) Review() CalendarCreateReview {
	body := reviewCalendarBody(input.Body)
	return CalendarCreateReview{
		Calendar: input.Calendar, Subject: input.Subject,
		BodyPreview: body.Preview, BodyBytes: body.Bytes, BodySHA256: body.SHA256,
		Start: input.Start, End: input.End, Location: input.Location,
		RequiredAttendees:     append([]string(nil), input.RequiredAttendees...),
		OptionalAttendees:     append([]string(nil), input.OptionalAttendees...),
		InvitationsWillBeSent: len(input.RequiredAttendees)+len(input.OptionalAttendees) > 0,
		TeamsMeeting:          input.TeamsMeeting,
		AllDay:                input.AllDay,
		TimeZone:              effectiveCalendarTimeZone(input.TimeZone),
		Reminder:              cloneCalendarReminder(input.Reminder),
		Recurrence:            cloneCalendarRecurrence(input.Recurrence),
	}
}

func (recurrence CalendarRecurrence) Validate(start time.Time) error {
	if recurrence.Interval < 1 || recurrence.Interval > 999 {
		return errors.New("calendar recurrence interval must be between 1 and 999")
	}
	if (recurrence.EndDate == "") == (recurrence.NumberOfOccurrences == 0) {
		return errors.New("calendar recurrence requires exactly one end date or occurrence count")
	}
	if recurrence.NumberOfOccurrences < 0 || recurrence.NumberOfOccurrences > MaxCalendarRecurrenceCount {
		return fmt.Errorf("calendar recurrence count must be between 1 and %d", MaxCalendarRecurrenceCount)
	}
	if recurrence.EndDate != "" {
		end, err := time.Parse("2006-01-02", recurrence.EndDate)
		if err != nil || end.Before(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)) {
			return errors.New("calendar recurrence end date must be YYYY-MM-DD on or after the start date")
		}
	}
	validDay := func(day string) bool {
		switch day {
		case "Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday":
			return true
		default:
			return false
		}
	}
	switch recurrence.Pattern {
	case CalendarRecurrenceDaily:
		if len(recurrence.DaysOfWeek) != 0 || recurrence.DayOfMonth != 0 || recurrence.Month != "" {
			return errors.New("daily recurrence accepts only interval and range")
		}
	case CalendarRecurrenceWeekly:
		if len(recurrence.DaysOfWeek) == 0 || recurrence.DayOfMonth != 0 || recurrence.Month != "" {
			return errors.New("weekly recurrence requires daysOfWeek")
		}
		seen := make(map[string]struct{}, len(recurrence.DaysOfWeek))
		for _, day := range recurrence.DaysOfWeek {
			if !validDay(day) {
				return fmt.Errorf("unsupported recurrence weekday %q", day)
			}
			if _, exists := seen[day]; exists {
				return fmt.Errorf("recurrence weekday %q appears more than once", day)
			}
			seen[day] = struct{}{}
		}
	case CalendarRecurrenceAbsoluteMonthly:
		if recurrence.DayOfMonth < 1 || recurrence.DayOfMonth > 31 || len(recurrence.DaysOfWeek) != 0 || recurrence.Month != "" {
			return errors.New("absolute monthly recurrence requires dayOfMonth from 1 to 31")
		}
	case CalendarRecurrenceAbsoluteYearly:
		if recurrence.Interval != 1 || recurrence.DayOfMonth < 1 || recurrence.DayOfMonth > 31 ||
			len(recurrence.DaysOfWeek) != 0 || !validCalendarMonth(recurrence.Month) {
			return errors.New("absolute yearly recurrence requires interval 1, month, and dayOfMonth")
		}
	default:
		return fmt.Errorf("unsupported calendar recurrence pattern %q", recurrence.Pattern)
	}
	return nil
}

func validCalendarMonth(month string) bool {
	switch month {
	case "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December":
		return true
	default:
		return false
	}
}

func effectiveCalendarTimeZone(zone string) string {
	if zone == "" {
		return "UTC"
	}
	return zone
}

func calendarBoundaryForTimeZone(value time.Time, zone string) time.Time {
	if effectiveCalendarTimeZone(zone) == "UTC" {
		return value.UTC()
	}
	return value
}

func isCalendarMidnight(value time.Time) bool {
	return value.Hour() == 0 && value.Minute() == 0 && value.Second() == 0 && value.Nanosecond() == 0
}

func cloneCalendarReminder(value *CalendarReminder) *CalendarReminder {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneCalendarRecurrence(value *CalendarRecurrence) *CalendarRecurrence {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.DaysOfWeek = append([]string(nil), value.DaysOfWeek...)
	return &cloned
}

func reviewCalendarBody(value string) CalendarBodyReview {
	preview := value
	if utf8.RuneCountInString(preview) > calendarBodyPreviewRunes {
		runes := []rune(preview)
		preview = string(runes[:calendarBodyPreviewRunes-1]) + "…"
	}
	digest := sha256.Sum256([]byte(value))
	return CalendarBodyReview{
		Preview: preview, Bytes: len(value), SHA256: hex.EncodeToString(digest[:]),
	}
}
