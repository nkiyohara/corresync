package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type fakeCalendarReader struct {
	page         CalendarPage
	folderPage   CalendarFolderPage
	err          error
	calls        int
	createInput  CalendarCreateInput
	createResult CalendarCreateResult
	createErr    error
	createCalls  int
	updateInput  CalendarUpdateInput
	updateResult CalendarUpdateResult
	updateErr    error
	updateCalls  int
	cancelInput  CalendarCancelInput
	cancelErr    error
	cancelCalls  int
}

func (reader *fakeCalendarReader) ListCalendarFolders(
	context.Context,
	CalendarFolderListInput,
) (CalendarFolderPage, error) {
	return reader.folderPage, reader.err
}

func (reader *fakeCalendarReader) UpdateCalendarEvent(
	_ context.Context,
	input CalendarUpdateInput,
) (CalendarUpdateResult, error) {
	reader.updateCalls++
	reader.updateInput = input
	if reader.updateResult.ID == "" {
		reader.updateResult = CalendarUpdateResult{ID: input.EventID, ChangeKey: "change-updated"}
	}
	return reader.updateResult, reader.updateErr
}

func (reader *fakeCalendarReader) CancelCalendarEvent(
	_ context.Context,
	input CalendarCancelInput,
) error {
	reader.cancelCalls++
	reader.cancelInput = input
	return reader.cancelErr
}

func (reader *fakeCalendarReader) ListCalendarEvents(
	context.Context,
	CalendarListInput,
) (CalendarPage, error) {
	reader.calls++
	return reader.page, reader.err
}

func (reader *fakeCalendarReader) CreateCalendarEvent(
	_ context.Context,
	input CalendarCreateInput,
) (CalendarCreateResult, error) {
	reader.createCalls++
	reader.createInput = input
	if reader.createResult.ID == "" {
		reader.createResult = CalendarCreateResult{ID: "event-created", ChangeKey: "change-created"}
		if input.OnlineMeeting || input.TeamsMeeting {
			reader.createResult.IsOnlineMeeting = true
			reader.createResult.OnlineMeetingProvider = "teams"
			reader.createResult.OnlineMeetingJoinURL =
				"https://teams.example.invalid/l/meetup-join/synthetic"
		}
	}
	return reader.createResult, reader.createErr
}

func testCalendarService(t *testing.T, reader CalendarPort) (*CalendarService, *memoryAudit) {
	t.Helper()
	guard, recorder := newTestGuard(t, policy.DefaultRules())
	service, err := NewCalendarService(guard, reader, CalendarOptions{
		MaxAttendees:          50,
		OnlineMeetingProvider: "teams",
		Provenance: domain.Provenance{
			AccountID:  "work",
			Provider:   domain.ProviderCalDAV,
			CalendarID: "synthetic-calendar",
		},
		Effects: CalendarEffects{
			CreateAttendeeNotifications: true,
			UpdateAttendeeNotifications: true,
			CancelAttendeeNotifications: true,
			CancellationMode:            CalendarCancellationModeAll,
			CancellationDisposition:     CalendarDispositionDeletedItems,
		},
	})
	if err != nil {
		t.Fatalf("NewCalendarService() error = %v", err)
	}
	return service, recorder
}

func validCalendarListInput() CalendarListInput {
	return CalendarListInput{
		Account:  "work",
		Calendar: CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"},
		Start:    "2026-07-17T00:00:00Z",
		End:      "2026-07-18T00:00:00Z",
	}
}

func TestCalendarServiceListsThroughPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeCalendarReader{page: CalendarPage{Events: []CalendarEvent{{ID: "event-1"}}}}
	service, recorder := testCalendarService(t, reader)
	page, err := service.List(
		context.Background(),
		validCalendarListInput(),
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Events) != 1 || reader.calls != 1 {
		t.Fatalf("unexpected result: page=%+v calls=%d", page, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Phase != AuditPhasePrepared ||
		recorder.events[1].Phase != AuditPhaseExecuted ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestCalendarServiceFailsClosedWithoutRoutedProvenance(t *testing.T) {
	t.Parallel()
	operation, err := domain.NewOperation(
		"calendar.create",
		domain.EffectExternalWrite,
		"work",
		struct{}{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&CalendarService{}).validateExecutionAccount(operation); err == nil {
		t.Fatal("calendar execution accepted missing routed provenance")
	}
}

func TestCalendarServiceAuditsTransportFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeCalendarReader{err: errors.New("synthetic transport failure")}
	service, recorder := testCalendarService(t, reader)
	_, err := service.List(
		context.Background(),
		validCalendarListInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil {
		t.Fatal("List() unexpectedly succeeded")
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeFailure ||
		recorder.events[1].Reason != "transport_error" {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestCalendarListInputValidationPreventsReaderCall(t *testing.T) {
	t.Parallel()

	reader := &fakeCalendarReader{}
	service, _ := testCalendarService(t, reader)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	tests := []CalendarListInput{
		{},
		{Account: "work", Calendar: CalendarFolder{Kind: CalendarFolderDistinguished, ID: "inbox"}, Start: "2026-07-17T00:00:00Z", End: "2026-07-18T00:00:00Z"},
		{Account: "work", Calendar: CalendarFolder{Kind: CalendarFolderOpaque}, Start: "2026-07-17T00:00:00Z", End: "2026-07-18T00:00:00Z"},
		{Account: "work", Calendar: CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"}, Start: "not-a-time", End: "2026-07-18T00:00:00Z"},
		{Account: "work", Calendar: CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"}, Start: "2026-07-18T00:00:00Z", End: "2026-07-17T00:00:00Z"},
		{Account: "work", Calendar: CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"}, Start: "2026-07-01T00:00:00Z", End: "2026-08-02T00:00:00Z"},
	}
	for _, input := range tests {
		if _, err := service.List(context.Background(), input, caller); err == nil {
			t.Fatalf("List(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

func TestNewCalendarServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	reader := &fakeCalendarReader{}
	if _, err := NewCalendarService(nil, reader, CalendarOptions{MaxAttendees: 50}); err == nil {
		t.Fatal("NewCalendarService() unexpectedly accepted nil guard")
	}
	guard, _ := newTestGuard(t, policy.DefaultRules())
	if _, err := NewCalendarService(guard, nil, CalendarOptions{MaxAttendees: 50}); err == nil {
		t.Fatal("NewCalendarService() unexpectedly accepted nil reader")
	}
	if _, err := NewCalendarService(guard, reader, CalendarOptions{}); err == nil {
		t.Fatal("NewCalendarService() unexpectedly accepted an invalid attendee limit")
	}
	if _, err := NewCalendarService(
		guard,
		reader,
		CalendarOptions{MaxAttendees: 50},
	); err == nil {
		t.Fatal("NewCalendarService() unexpectedly accepted missing effect semantics")
	}
	if _, err := NewCalendarService(
		guard,
		reader,
		CalendarOptions{
			MaxAttendees: 50,
			Effects: CalendarEffects{
				CancellationMode:        CalendarCancellationNoScheduling,
				CancellationDisposition: CalendarDispositionDeletedItems,
			},
		},
	); err == nil {
		t.Fatal("NewCalendarService() unexpectedly accepted inconsistent effects")
	}
}

func TestCalendarEffectsAcceptTruthfulCalDAVObjectMutations(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{
		CalendarCancellationNoScheduling,
		CalendarCancellationProviderManaged,
	} {
		effects := CalendarEffects{
			CancellationMode:        mode,
			CancellationDisposition: CalendarDispositionCalendarObject,
		}
		if mode == CalendarCancellationProviderManaged {
			effects.CancelAttendeeNotifications = true
		}
		if err := effects.validate(); err != nil {
			t.Fatalf("CalDAV effects for %q: %v", mode, err)
		}
	}
}
