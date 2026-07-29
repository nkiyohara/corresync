package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const (
	MaxCalendarFolderPageSize = 100
	MaxCalendarFolderOffset   = 10_000
)

// CalendarFolderListInput selects a bounded page of calendars available to one
// already authenticated account route.
type CalendarFolderListInput struct {
	Account domain.AccountID `json:"account"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

// CalendarFolderSummary is a provider-neutral selectable calendar.
type CalendarFolderSummary struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName"`
	IsDefault   bool              `json:"isDefault"`
	CanEdit     bool              `json:"canEdit"`
	AccessRole  string            `json:"accessRole"`
	TimeZone    string            `json:"timeZone,omitempty"`
	Provenance  domain.Provenance `json:"provenance,omitempty"`
}

// CalendarFolderPage is stable across CLI, daemon, and MCP transports.
type CalendarFolderPage struct {
	Calendars        []CalendarFolderSummary `json:"calendars"`
	TotalCalendars   int                     `json:"totalCalendars"`
	IncludesLastItem bool                    `json:"includesLastItem"`
}

// CalendarFolderReader discovers selectable calendars without exposing a
// provider-specific generic collection API.
type CalendarFolderReader interface {
	ListCalendarFolders(context.Context, CalendarFolderListInput) (CalendarFolderPage, error)
}

// Validate rejects unbounded folder enumeration before policy or network use.
func (input CalendarFolderListInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if input.Offset < 0 || input.Offset > MaxCalendarFolderOffset {
		return fmt.Errorf(
			"calendar offset must be between 0 and %d",
			MaxCalendarFolderOffset,
		)
	}
	if input.Limit < 1 || input.Limit > MaxCalendarFolderPageSize {
		return fmt.Errorf(
			"calendar limit must be between 1 and %d",
			MaxCalendarFolderPageSize,
		)
	}
	return nil
}

// ListFolders returns calendar identities through the shared read policy.
func (service *CalendarService) ListFolders(
	ctx context.Context,
	input CalendarFolderListInput,
	caller domain.Caller,
) (CalendarFolderPage, error) {
	if err := input.Validate(); err != nil {
		return CalendarFolderPage{}, err
	}
	operation, err := domain.NewOperation(
		"calendar.folders",
		domain.EffectRead,
		input.Account,
		input,
	)
	if err != nil {
		return CalendarFolderPage{}, fmt.Errorf(
			"create calendar folder operation: %w",
			err,
		)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return CalendarFolderPage{}, err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return CalendarFolderPage{}, errors.New(
			"calendar folder operation was not allowed for immediate execution",
		)
	}

	page, callErr := service.folderReader.ListCalendarFolders(ctx, input)
	if callErr == nil {
		callErr = validateCalendarFolderPage(page, input)
	}
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return CalendarFolderPage{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		for index := range page.Calendars {
			calendar := &page.Calendars[index]
			calendar.Provenance = service.provenance
			calendar.Provenance.CalendarID = calendar.ID
			calendar.Provenance.SourceObjectID = calendar.ID
		}
	}
	return page, nil
}

func validateCalendarFolderPage(
	page CalendarFolderPage,
	input CalendarFolderListInput,
) error {
	if len(page.Calendars) > input.Limit ||
		page.TotalCalendars < len(page.Calendars) ||
		page.TotalCalendars > MaxCalendarFolderOffset+MaxCalendarFolderPageSize {
		return errors.New("provider returned invalid calendar folder counts")
	}
	seen := make(map[string]struct{}, len(page.Calendars))
	defaults := 0
	for _, calendar := range page.Calendars {
		if err := validateOpaqueValue("calendar folder ID", calendar.ID); err != nil {
			return err
		}
		if calendar.DisplayName == "" ||
			len(calendar.DisplayName) > 512 ||
			!utf8.ValidString(calendar.DisplayName) ||
			strings.ContainsAny(calendar.DisplayName, "\r\n\x00") {
			return errors.New("provider returned an invalid calendar display name")
		}
		if len(calendar.TimeZone) > 128 ||
			strings.ContainsAny(calendar.TimeZone, "\r\n\x00") {
			return errors.New("provider returned an invalid calendar time zone")
		}
		switch calendar.AccessRole {
		case "owner", "writer":
			if !calendar.CanEdit {
				return errors.New("provider calendar access role disagrees with editability")
			}
		case "reader", "free_busy", "unknown":
			if calendar.CanEdit {
				return errors.New("provider calendar access role disagrees with editability")
			}
		default:
			return errors.New("provider returned an invalid calendar access role")
		}
		if calendar.IsDefault {
			defaults++
		}
		if _, exists := seen[calendar.ID]; exists {
			return errors.New("provider returned a duplicate calendar identity")
		}
		seen[calendar.ID] = struct{}{}
	}
	if defaults > 1 {
		return errors.New("provider returned multiple default calendars")
	}
	return nil
}
