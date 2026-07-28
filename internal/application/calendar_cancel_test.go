package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validCalendarCancelInput() CalendarCancelInput {
	return CalendarCancelInput{Account: "work", EventID: "event-1", ChangeKey: "change-1"}
}

func TestCalendarCancelAlwaysPreviewsThenCommitsExactVersion(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, recorder := testCalendarService(t, port)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.Cancel(t.Context(), validCalendarCancelInput(), caller)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || port.cancelCalls != 0 ||
		access.Preview.Operation.Name != "calendar.cancel" ||
		access.Preview.Operation.Effect != domain.EffectDestructiveWrite ||
		access.Review.CancellationMode != CalendarCancellationModeAll ||
		!access.Review.AttendeeNotificationsMaySend {
		t.Fatalf("unsafe preview: %+v calls=%d", access, port.cancelCalls)
	}
	committed, err := service.CommitCancel(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitCancel() error = %v", err)
	}
	if committed.Status != "cancelled" || committed.Cancelled == nil ||
		committed.Cancelled.ID != "event-1" || port.cancelCalls != 1 ||
		port.cancelInput.ChangeKey != "change-1" {
		t.Fatalf("unexpected commit: %+v input=%+v calls=%d", committed, port.cancelInput, port.cancelCalls)
	}
	if len(recorder.events) != 3 || recorder.events[2].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestCalendarReviewsUseConfiguredProviderEffects(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	guard, _ := newTestGuard(t, policy.DefaultRules())
	service, err := NewCalendarService(guard, port, CalendarOptions{
		MaxAttendees: 50,
		Effects: CalendarEffects{
			CancellationMode:        CalendarCancellationNoScheduling,
			CancellationDisposition: CalendarDispositionCalendarObject,
		},
	})
	if err != nil {
		t.Fatalf("NewCalendarService() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	create, err := service.Create(t.Context(), validCalendarCreateInput(), caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if create.Review.InvitationsWillBeSent {
		t.Fatalf("create review claimed attendee notification: %+v", create.Review)
	}
	updateInput := validCalendarUpdateInput()
	updateInput.ReplaceAttendees = true
	updateInput.RequiredAttendees = []string{"reader@example.invalid"}
	update, err := service.Update(t.Context(), updateInput, caller)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if update.Review.AttendeeUpdatesMaySend {
		t.Fatalf("update review claimed attendee notification: %+v", update.Review)
	}
	cancel, err := service.Cancel(t.Context(), validCalendarCancelInput(), caller)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if cancel.Review.AttendeeNotificationsMaySend ||
		cancel.Review.CancellationMode != CalendarCancellationNoScheduling ||
		cancel.Review.DeleteType != CalendarDispositionCalendarObject {
		t.Fatalf("cancel review does not reflect configured effects: %+v", cancel.Review)
	}
}

func TestCalendarCancelValidationPreventsNetworkUse(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	for _, input := range []CalendarCancelInput{
		{},
		{Account: "work", EventID: "event-1"},
		{Account: "work", EventID: "bad\nevent", ChangeKey: "change-1"},
	} {
		if _, err := service.Cancel(context.Background(), input, caller); err == nil {
			t.Fatalf("Cancel(%+v) unexpectedly succeeded", input)
		}
	}
	if port.cancelCalls != 0 {
		t.Fatalf("cancel calls = %d, want 0", port.cancelCalls)
	}
}

func TestCalendarCancelRejectsWrongCallerWithoutConsumingPreview(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{}
	service, _ := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.Cancel(t.Context(), validCalendarCancelInput(), caller)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if _, err := service.CommitCancel(t.Context(), access.Preview.Token, domain.Caller{
		Surface: "cli", Instance: "process-2",
	}); err == nil {
		t.Fatal("CommitCancel() accepted a different caller")
	}
	if _, err := service.CommitCancel(t.Context(), access.Preview.Token, caller); err != nil {
		t.Fatalf("CommitCancel() error after caller mismatch = %v", err)
	}
}

func TestCalendarCancelAuditsAmbiguousOutcome(t *testing.T) {
	t.Parallel()

	port := &fakeCalendarReader{cancelErr: errors.Join(
		ErrWriteOutcomeUnknown, errors.New("synthetic disconnect"),
	)}
	service, recorder := testCalendarService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.Cancel(t.Context(), validCalendarCancelInput(), caller)
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	_, err = service.CommitCancel(t.Context(), access.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitCancel() error = %v, want ErrWriteOutcomeUnknown", err)
	}
	last := recorder.events[len(recorder.events)-1]
	if last.Outcome != AuditOutcomeUnknown || last.Reason != "outcome_unknown" {
		t.Fatalf("unexpected audit event: %+v", last)
	}
}
