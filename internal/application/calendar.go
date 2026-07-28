package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const MaxCalendarWindow = 31 * 24 * time.Hour

// CalendarFolderKind distinguishes the default calendar from an opaque folder.
type CalendarFolderKind string

const (
	CalendarFolderDistinguished CalendarFolderKind = "distinguished"
	CalendarFolderOpaque        CalendarFolderKind = "opaque"
)

// CalendarFolder is a protocol-independent calendar selection.
type CalendarFolder struct {
	Kind CalendarFolderKind `json:"kind"`
	ID   string             `json:"id"`
}

// CalendarListInput selects a bounded absolute time window. RFC3339 inputs are
// normalized to UTC at the OWA boundary.
type CalendarListInput struct {
	Account  domain.AccountID `json:"account"`
	Calendar CalendarFolder   `json:"calendar"`
	Start    string           `json:"start"`
	End      string           `json:"end"`
}

// CalendarEvent is metadata only. It excludes body, attendee list, attachment
// content, and online-meeting join URLs.
type CalendarEvent struct {
	ID              string            `json:"id"`
	ChangeKey       string            `json:"changeKey,omitempty"`
	Subject         string            `json:"subject,omitempty"`
	Start           string            `json:"start"`
	End             string            `json:"end"`
	Location        string            `json:"location,omitempty"`
	Organizer       MailAddress       `json:"organizer,omitempty"`
	IsAllDay        bool              `json:"isAllDay"`
	IsOnlineMeeting bool              `json:"isOnlineMeeting"`
	IsOrganizer     bool              `json:"isOrganizer"`
	IsCancelled     bool              `json:"isCancelled"`
	MyResponse      string            `json:"myResponse,omitempty"`
	FreeBusy        string            `json:"freeBusy,omitempty"`
	Provenance      domain.Provenance `json:"provenance,omitempty"`
}

// CalendarPage is the stable output contract shared by CLI and MCP.
type CalendarPage struct {
	Events []CalendarEvent `json:"events"`
	Start  string          `json:"start"`
	End    string          `json:"end"`
}

// CalendarReader is the application port implemented by the OWA adapter.
type CalendarReader interface {
	ListCalendarEvents(context.Context, CalendarListInput) (CalendarPage, error)
}

// CalendarPort is the complete typed calendar boundary required by the
// application service. It intentionally exposes no generic item mutation.
type CalendarPort interface {
	CalendarReader
	CalendarCreator
	CalendarUpdater
	CalendarCanceller
}

// CalendarOptions applies configured limits at the application boundary.
type CalendarOptions struct {
	MaxAttendees int
	Provenance   domain.Provenance
}

// CalendarService applies policy and audit around calendar use cases.
type CalendarService struct {
	guard        *Guard
	reader       CalendarReader
	creator      CalendarCreator
	updater      CalendarUpdater
	canceller    CalendarCanceller
	maxAttendees int
	provenance   domain.Provenance
}

// NewCalendarService requires the shared guard and a transport port.
func NewCalendarService(
	guard *Guard,
	port CalendarPort,
	options CalendarOptions,
) (*CalendarService, error) {
	if guard == nil {
		return nil, errors.New("calendar guard is required")
	}
	if port == nil {
		return nil, errors.New("calendar reader is required")
	}
	if options.MaxAttendees < 1 || options.MaxAttendees > MaxCalendarAttendees {
		return nil, fmt.Errorf("max calendar attendees must be between 1 and %d", MaxCalendarAttendees)
	}
	return &CalendarService{
		guard: guard, reader: port, creator: port, updater: port, canceller: port,
		maxAttendees: options.MaxAttendees, provenance: options.Provenance,
	}, nil
}

// List returns event metadata through the shared policy and audit boundary.
func (service *CalendarService) List(
	ctx context.Context,
	input CalendarListInput,
	caller domain.Caller,
) (CalendarPage, error) {
	if err := input.Validate(); err != nil {
		return CalendarPage{}, err
	}
	operation, err := domain.NewOperation("calendar.list", domain.EffectRead, input.Account, input)
	if err != nil {
		return CalendarPage{}, fmt.Errorf("create calendar list operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return CalendarPage{}, err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return CalendarPage{}, errors.New("calendar list operation was not allowed for immediate execution")
	}

	page, callErr := service.reader.ListCalendarEvents(ctx, input)
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase:     AuditPhaseExecuted,
		Outcome:   outcome,
		Reason:    reason,
		Caller:    caller,
		Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return CalendarPage{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		for index := range page.Events {
			page.Events[index].Provenance = service.calendarProvenance(page.Events[index].ID)
		}
	}
	return page, nil
}

// Validate rejects unbounded or ambiguous calendar reads before network use.
func (input CalendarListInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateCalendarFolder(input.Calendar); err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339, input.Start)
	if err != nil {
		return errors.New("calendar start must be RFC3339")
	}
	end, err := time.Parse(time.RFC3339, input.End)
	if err != nil {
		return errors.New("calendar end must be RFC3339")
	}
	window := end.Sub(start)
	if window <= 0 {
		return errors.New("calendar end must be after start")
	}
	if window > MaxCalendarWindow {
		return fmt.Errorf("calendar window must not exceed %s", MaxCalendarWindow)
	}
	return nil
}

func validateCalendarFolder(calendar CalendarFolder) error {
	switch calendar.Kind {
	case CalendarFolderDistinguished:
		if calendar.ID != "calendar" {
			return fmt.Errorf("unsupported distinguished calendar %q", calendar.ID)
		}
	case CalendarFolderOpaque:
		if err := validateOpaqueValue("calendar ID", calendar.ID); err != nil {
			return err
		}
	default:
		return errors.New("calendar folder kind is required")
	}
	return nil
}

func calendarFolderTarget(calendar CalendarFolder) domain.TargetRef {
	return domain.TargetRef{
		Kind: domain.TargetCalendar,
		ID:   string(calendar.Kind) + ":" + calendar.ID,
	}
}

func calendarEventTarget(eventID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetCalendar, ID: "event:" + eventID}
}

func (service *CalendarService) calendarProvenance(sourceObjectID string) domain.Provenance {
	provenance := service.provenance
	if provenance.AccountID == "" {
		return domain.Provenance{}
	}
	provenance.SourceObjectID = sourceObjectID
	return provenance
}
