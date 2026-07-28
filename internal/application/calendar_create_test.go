package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func validCalendarCreateInput() CalendarCreateInput {
	return CalendarCreateInput{
		Account:           "work",
		Calendar:          CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"},
		Subject:           "Synthetic design review",
		Body:              "Discuss synthetic fixture data only.",
		Start:             "2026-07-20T09:00:00+01:00",
		End:               "2026-07-20T09:45:00+01:00",
		Location:          "Room Example",
		RequiredAttendees: []string{"alice@example.invalid"},
		OptionalAttendees: []string{"bob@example.invalid"},
		TeamsMeeting:      true,
	}
}

func TestCalendarCreateAlwaysPreviewsThenCommitsExactEvent(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{
		createResult: CalendarCreateResult{
			ID: "event-1", ChangeKey: "change-1",
			IsOnlineMeeting: true, OnlineMeetingProvider: "TeamsForBusiness",
			OnlineMeetingJoinURL: "https://teams.example.invalid/l/meetup-join/synthetic",
		},
	}
	service, recorder := testCalendarService(t, port)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	input := validCalendarCreateInput()

	access, err := service.Create(t.Context(), input, caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || port.createCalls != 0 {
		t.Fatalf("unexpected preview: %+v calls=%d", access, port.createCalls)
	}
	if access.Preview.Operation.Name != "calendar.create" ||
		access.Preview.Operation.Effect != domain.EffectExternalWrite ||
		!access.Review.InvitationsWillBeSent || !access.Review.TeamsMeeting ||
		access.Review.BodySHA256 == "" {
		t.Fatalf("unsafe preview: %+v", access)
	}

	committed, err := service.CommitCreate(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitCreate() error = %v", err)
	}
	if committed.Status != "created" || committed.Created == nil ||
		committed.Created.ID != "event-1" || port.createCalls != 1 ||
		!committed.Created.IsOnlineMeeting || committed.Created.OnlineMeetingProvider != "TeamsForBusiness" ||
		committed.Created.OnlineMeetingJoinURL == "" ||
		port.createInput.Subject != input.Subject || port.createInput.Body != input.Body ||
		!port.createInput.TeamsMeeting {
		t.Fatalf("unexpected commit: %+v input=%+v calls=%d", committed, port.createInput, port.createCalls)
	}
	if _, err := service.CommitCreate(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitCreate() replay unexpectedly succeeded")
	}
	if len(recorder.events) != 3 || recorder.events[1].Phase != AuditPhaseCommitted ||
		recorder.events[2].Phase != AuditPhaseExecuted ||
		recorder.events[2].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestCalendarCreateWithoutAttendeesStillRequiresPreview(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	input := validCalendarCreateInput()
	input.RequiredAttendees = nil
	input.OptionalAttendees = nil

	access, err := service.Create(
		t.Context(), input, domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil ||
		access.Review.InvitationsWillBeSent || port.createCalls != 0 {
		t.Fatalf("unexpected access: %+v calls=%d", access, port.createCalls)
	}
}

func TestCalendarCreateRejectsWrongCallerWithoutConsumingPreview(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.Create(t.Context(), validCalendarCreateInput(), caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.CommitCreate(
		t.Context(), access.Preview.Token,
		domain.Caller{Surface: "cli", Instance: "process-2"},
	); err == nil {
		t.Fatal("CommitCreate() accepted a different caller")
	}
	if _, err := service.CommitCreate(t.Context(), access.Preview.Token, caller); err != nil {
		t.Fatalf("CommitCreate() error after caller mismatch = %v", err)
	}
	if port.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", port.createCalls)
	}
}

func TestCalendarCreateRejectsCommitRoutedToAnotherAccountService(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	service.provenance.AccountID = "personal"
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.Create(t.Context(), validCalendarCreateInput(), caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := service.CommitCreate(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitCreate() accepted a preview routed to another account service")
	}
	if port.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", port.createCalls)
	}
}

func TestCalendarCreateAuditsAmbiguousOutcome(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{createErr: errors.Join(
		ErrWriteOutcomeUnknown, errors.New("synthetic disconnect"),
	)}
	service, recorder := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.Create(t.Context(), validCalendarCreateInput(), caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	_, err = service.CommitCreate(t.Context(), access.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitCreate() error = %v, want ErrWriteOutcomeUnknown", err)
	}
	last := recorder.events[len(recorder.events)-1]
	if last.Outcome != AuditOutcomeUnknown || last.Reason != "outcome_unknown" {
		t.Fatalf("unexpected audit event: %+v", last)
	}
}

func TestCalendarCreateValidationPreventsNetworkUse(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	valid := validCalendarCreateInput()
	tests := []CalendarCreateInput{
		{},
		func() CalendarCreateInput { value := valid; value.Calendar.ID = "inbox"; return value }(),
		func() CalendarCreateInput { value := valid; value.Subject = "bad\nsubject"; return value }(),
		func() CalendarCreateInput { value := valid; value.Body = "bad\x00body"; return value }(),
		func() CalendarCreateInput { value := valid; value.Location = "bad\rlocation"; return value }(),
		func() CalendarCreateInput { value := valid; value.Start = "not-a-time"; return value }(),
		func() CalendarCreateInput { value := valid; value.End = value.Start; return value }(),
		func() CalendarCreateInput { value := valid; value.End = "2026-09-01T09:00:00+01:00"; return value }(),
		func() CalendarCreateInput {
			value := valid
			value.RequiredAttendees = []string{"Not an address"}
			return value
		}(),
		func() CalendarCreateInput {
			value := valid
			value.OptionalAttendees = []string{"ALICE@example.invalid"}
			return value
		}(),
		func() CalendarCreateInput { value := valid; value.RequiredAttendees = make([]string, 51); return value }(),
		func() CalendarCreateInput {
			value := valid
			value.Subject = strings.Repeat("x", MaxCalendarSubjectBytes+1)
			return value
		}(),
	}
	for _, input := range tests {
		if _, err := service.Create(context.Background(), input, caller); err == nil {
			t.Fatalf("Create(%+v) unexpectedly succeeded", input)
		}
	}
	if port.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", port.createCalls)
	}
}

func TestCalendarCreateReviewTruncatesVisibleBodyAndBindsFullBody(t *testing.T) {
	t.Parallel()

	input := validCalendarCreateInput()
	input.Body = strings.Repeat("界", calendarBodyPreviewRunes+10)
	review := input.Review()
	if !strings.HasSuffix(review.BodyPreview, "…") ||
		len([]rune(review.BodyPreview)) != calendarBodyPreviewRunes ||
		review.BodyBytes != len(input.Body) || len(review.BodySHA256) != 64 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestCalendarCreateSupportsAllDayReminderAndRecurrence(t *testing.T) {
	t.Parallel()

	input := validCalendarCreateInput()
	input.Start = "2026-07-20T00:00:00+01:00"
	input.End = "2026-07-21T00:00:00+01:00"
	input.AllDay = true
	input.TimeZone = "GMT Standard Time"
	input.Reminder = &CalendarReminder{Enabled: true, MinutesBeforeStart: 30}
	input.Recurrence = &CalendarRecurrence{
		Pattern: CalendarRecurrenceWeekly, Interval: 2,
		DaysOfWeek: []string{"Monday", "Wednesday"}, NumberOfOccurrences: 8,
	}
	if err := input.Validate(50); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	review := input.Review()
	if !review.AllDay || review.TimeZone != "GMT Standard Time" || review.Reminder == input.Reminder ||
		review.Recurrence == input.Recurrence || review.Recurrence.NumberOfOccurrences != 8 {
		t.Fatalf("unexpected review: %+v", review)
	}
}

func TestCalendarCreateAllDayUsesReviewedTimeZoneBoundaries(t *testing.T) {
	t.Parallel()

	input := validCalendarCreateInput()
	input.Start = "2026-07-20T00:00:00+01:00"
	input.End = "2026-07-21T00:00:00+01:00"
	input.AllDay = true
	if err := input.Validate(50); err == nil {
		t.Fatal("Validate() accepted non-UTC all-day boundaries with the UTC default")
	}
	input.TimeZone = "GMT Standard Time"
	if err := input.Validate(50); err != nil {
		t.Fatalf("Validate() rejected boundaries in the reviewed time zone: %v", err)
	}
}

func TestCalendarCreateRejectsMalformedReminderAndRecurrence(t *testing.T) {
	t.Parallel()

	valid := validCalendarCreateInput()
	tests := []CalendarCreateInput{
		func() CalendarCreateInput { value := valid; value.AllDay = true; return value }(),
		func() CalendarCreateInput {
			value := valid
			value.Reminder = &CalendarReminder{MinutesBeforeStart: 5}
			return value
		}(),
		func() CalendarCreateInput {
			value := valid
			value.Recurrence = &CalendarRecurrence{Pattern: CalendarRecurrenceDaily, Interval: 1}
			return value
		}(),
		func() CalendarCreateInput {
			value := valid
			value.Recurrence = &CalendarRecurrence{Pattern: CalendarRecurrenceWeekly, Interval: 1, DaysOfWeek: []string{"Funday"}, NumberOfOccurrences: 2}
			return value
		}(),
		func() CalendarCreateInput { value := valid; value.TimeZone = " bad "; return value }(),
	}
	for _, input := range tests {
		if err := input.Validate(50); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", input)
		}
	}
}
