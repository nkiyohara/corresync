package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/nkiyohara/corresync/internal/domain"
)

const MaxAgendaProjectionPageSize = 100
const maxAgendaSourceItems = 5000

type AgendaProjectionInput struct {
	Start           string `json:"start"`
	End             string `json:"end"`
	DisplayTimeZone string `json:"displayTimeZone"`
	Offset          int    `json:"offset"`
	Limit           int    `json:"limit"`
}

type ProjectedAgendaEvent struct {
	AccountAlias    string        `json:"accountAlias"`
	DisplayStart    string        `json:"displayStart"`
	DisplayEnd      string        `json:"displayEnd"`
	DisplayTimeZone string        `json:"displayTimeZone"`
	Event           CalendarEvent `json:"event"`
}

type AgendaProjectionPage struct {
	Events          []ProjectedAgendaEvent    `json:"events"`
	Accounts        []ProjectionAccountStatus `json:"accounts"`
	Failures        []ProjectionFailure       `json:"failures"`
	Start           string                    `json:"start"`
	End             string                    `json:"end"`
	DisplayTimeZone string                    `json:"displayTimeZone"`
	Offset          int                       `json:"offset"`
	Limit           int                       `json:"limit"`
	NextOffset      int                       `json:"nextOffset,omitempty"`
	HasMore         bool                      `json:"hasMore"`
	Complete        bool                      `json:"complete"`
}

type agendaProjectionSource struct {
	status ProjectionAccountStatus
	events []ProjectedAgendaEvent
}

func (service *ProjectionService) ListAgenda(
	ctx context.Context,
	input AgendaProjectionInput,
	caller domain.Caller,
) (AgendaProjectionPage, error) {
	location, err := input.validate()
	if err != nil {
		return AgendaProjectionPage{}, err
	}
	if err := caller.Validate(); err != nil {
		return AgendaProjectionPage{}, err
	}
	accounts, err := service.accounts(ctx)
	if err != nil {
		return AgendaProjectionPage{}, err
	}
	calendarAccounts := make([]ProjectionAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.CalendarProvider != "" {
			calendarAccounts = append(calendarAccounts, account)
		}
	}
	if len(calendarAccounts) == 0 {
		return AgendaProjectionPage{}, errors.New(
			"no configured account has a calendar route",
		)
	}
	sources := make([]agendaProjectionSource, len(calendarAccounts))
	for index, account := range calendarAccounts {
		if err := ctx.Err(); err != nil {
			return AgendaProjectionPage{}, err
		}
		sources[index] = service.listProjectionAccountAgenda(
			ctx,
			account,
			input,
			location,
			caller,
		)
	}
	if err := ctx.Err(); err != nil {
		return AgendaProjectionPage{}, err
	}
	statuses := make([]ProjectionAccountStatus, 0, len(sources))
	events := make([]ProjectedAgendaEvent, 0, 128)
	for _, source := range sources {
		statuses = append(statuses, source.status)
		if source.status.Complete {
			events = append(events, source.events...)
		}
	}
	sortProjectedAgenda(events, location)
	hasMore := len(events) > input.Offset+input.Limit
	pageEvents := projectionAgendaWindow(events, input.Offset, input.Limit)
	failures := projectionFailures(statuses)
	page := AgendaProjectionPage{
		Events: pageEvents, Accounts: statuses, Failures: failures,
		Start: input.Start, End: input.End,
		DisplayTimeZone: input.DisplayTimeZone,
		Offset:          input.Offset, Limit: input.Limit,
		HasMore: hasMore, Complete: len(failures) == 0,
	}
	if hasMore {
		page.NextOffset = input.Offset + len(pageEvents)
	}
	if err := page.Validate(); err != nil {
		return AgendaProjectionPage{}, fmt.Errorf(
			"validate agenda projection: %w",
			err,
		)
	}
	return page, nil
}

func (service *ProjectionService) listProjectionAccountAgenda(
	ctx context.Context,
	account ProjectionAccount,
	input AgendaProjectionInput,
	location *time.Location,
	caller domain.Caller,
) agendaProjectionSource {
	status := newProjectionStatus(account, projectionServiceCalendar)
	if !account.ServiceAuthenticated(projectionServiceCalendar) {
		return agendaProjectionSource{
			status: projectionUnavailableStatus(
				account,
				projectionServiceCalendar,
			),
		}
	}
	page, err := service.reader.ListCalendar(ctx, CalendarListInput{
		Account: account.Account,
		Calendar: CalendarFolder{
			Kind: CalendarFolderDistinguished,
			ID:   "calendar",
		},
		Start: input.Start,
		End:   input.End,
	}, caller)
	if err != nil {
		return agendaProjectionSource{status: failProjectionCallStatus(
			status,
			err,
			"the account calendar did not complete; inspect account status and retry",
		)}
	}
	if len(page.Events) > maxAgendaSourceItems ||
		!sameCalendarBoundary(page.Start, input.Start) ||
		!sameCalendarBoundary(page.End, input.End) {
		return agendaProjectionSource{status: failProjectionStatus(
			status,
			"invalid_result",
			"the account returned an invalid bounded calendar page",
		)}
	}
	events := make([]ProjectedAgendaEvent, 0, len(page.Events))
	seenObjects := make(map[string]struct{}, len(page.Events))
	for _, event := range page.Events {
		if err := validateAgendaSourceEvent(event, account); err != nil {
			status.FetchedItems = len(events)
			return agendaProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned an invalid calendar event",
			)}
		}
		if _, exists := seenObjects[event.Provenance.SourceObjectID]; exists {
			status.FetchedItems = len(events)
			return agendaProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned duplicate calendar event identities",
			)}
		}
		seenObjects[event.Provenance.SourceObjectID] = struct{}{}
		displayStart, err := displayCalendarBoundary(
			event.Start,
			event.OriginalStart,
			event.OriginalStartFloating,
			event.IsAllDay,
			location,
		)
		if err != nil {
			status.FetchedItems = len(events)
			return agendaProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned an event with invalid time semantics",
			)}
		}
		displayEnd, err := displayCalendarBoundary(
			event.End,
			event.OriginalEnd,
			event.OriginalEndFloating,
			event.IsAllDay,
			location,
		)
		if err != nil {
			status.FetchedItems = len(events)
			return agendaProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned an event with invalid time semantics",
			)}
		}
		events = append(events, ProjectedAgendaEvent{
			AccountAlias:    account.Alias,
			DisplayStart:    displayStart,
			DisplayEnd:      displayEnd,
			DisplayTimeZone: input.DisplayTimeZone,
			Event:           event,
		})
	}
	status.Complete = true
	status.Exhausted = true
	status.FetchedItems = len(events)
	return agendaProjectionSource{status: status, events: events}
}

func sameCalendarBoundary(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr == nil && rightErr == nil && leftTime.Equal(rightTime)
}

func (input AgendaProjectionInput) validate() (*time.Location, error) {
	if err := (CalendarListInput{
		Account: "acc_00000000000000000000000000000001",
		Calendar: CalendarFolder{
			Kind: CalendarFolderDistinguished,
			ID:   "calendar",
		},
		Start: input.Start,
		End:   input.End,
	}).Validate(); err != nil {
		return nil, err
	}
	if input.Offset < 0 || input.Offset > MaxProjectionOffset {
		return nil, fmt.Errorf(
			"agenda projection offset must be between 0 and %d",
			MaxProjectionOffset,
		)
	}
	if input.Limit < 1 || input.Limit > MaxAgendaProjectionPageSize {
		return nil, fmt.Errorf(
			"agenda projection limit must be between 1 and %d",
			MaxAgendaProjectionPageSize,
		)
	}
	if input.DisplayTimeZone == "" ||
		len(input.DisplayTimeZone) > 128 ||
		strings.TrimSpace(input.DisplayTimeZone) != input.DisplayTimeZone ||
		strings.ContainsAny(input.DisplayTimeZone, "\r\n\x00") {
		return nil, errors.New("agenda display time zone is malformed")
	}
	location, err := time.LoadLocation(input.DisplayTimeZone)
	if err != nil {
		return nil, errors.New(
			"agenda display time zone must be a recognized IANA time-zone name",
		)
	}
	return location, nil
}

func (input AgendaProjectionInput) Validate() error {
	_, err := input.validate()
	return err
}

func validateAgendaSourceEvent(
	event CalendarEvent,
	account ProjectionAccount,
) error {
	if event.ID == "" || len(event.ID) > 4096 ||
		strings.ContainsAny(event.ID, "\r\n\x00") {
		return errors.New("agenda event identity is invalid")
	}
	start, err := time.Parse(time.RFC3339Nano, event.Start)
	if err != nil {
		return errors.New("agenda event start is invalid")
	}
	end, err := time.Parse(time.RFC3339Nano, event.End)
	if err != nil || end.Before(start) {
		return errors.New("agenda event end is invalid")
	}
	for _, value := range []string{
		event.OriginalStart,
		event.OriginalEnd,
		event.OriginalStartTimeZone,
		event.OriginalEndTimeZone,
	} {
		if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("agenda event original time semantics are malformed")
		}
	}
	if event.OriginalStart == "" || event.OriginalEnd == "" {
		return errors.New("agenda event lacks original time semantics")
	}
	if (event.OriginalStartFloating && event.OriginalStartTimeZone != "") ||
		(event.OriginalEndFloating && event.OriginalEndTimeZone != "") {
		return errors.New("floating agenda event cannot name an original zone")
	}
	if err := event.Provenance.Validate(); err != nil {
		return err
	}
	if event.Provenance.AccountID != account.Account ||
		event.Provenance.Provider != account.CalendarProvider ||
		event.Provenance.CalendarID == "" ||
		event.Provenance.MailboxID != "" ||
		event.Provenance.SourceObjectID != event.ID {
		return errors.New("agenda event provenance is invalid")
	}
	return nil
}

func displayCalendarBoundary(
	normalized, original string,
	floating, allDay bool,
	location *time.Location,
) (string, error) {
	if allDay {
		if len(original) >= len("2006-01-02") {
			if date, err := time.Parse(
				"2006-01-02",
				original[:len("2006-01-02")],
			); err == nil {
				return date.Format("2006-01-02"), nil
			}
		}
		parsed, err := time.Parse(time.RFC3339Nano, normalized)
		if err != nil {
			return "", err
		}
		return parsed.Format("2006-01-02"), nil
	}
	if floating {
		for _, layout := range []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05",
			"20060102T150405",
		} {
			if parsed, err := time.ParseInLocation(
				layout,
				original,
				location,
			); err == nil {
				return parsed.Format(time.RFC3339), nil
			}
		}
		return "", errors.New("floating calendar time is malformed")
	}
	parsed, err := time.Parse(time.RFC3339Nano, normalized)
	if err != nil {
		return "", err
	}
	return parsed.In(location).Format(time.RFC3339), nil
}

func sortProjectedAgenda(
	events []ProjectedAgendaEvent,
	location *time.Location,
) {
	slices.SortStableFunc(events, func(left, right ProjectedAgendaEvent) int {
		return compareProjectedAgenda(left, right, location)
	})
}

func compareProjectedAgenda(
	left, right ProjectedAgendaEvent,
	location *time.Location,
) int {
	leftTime := projectedAgendaDisplayTime(left, true, location)
	rightTime := projectedAgendaDisplayTime(right, true, location)
	if compared := leftTime.Compare(rightTime); compared != 0 {
		return compared
	}
	leftEnd := projectedAgendaDisplayTime(left, false, location)
	rightEnd := projectedAgendaDisplayTime(right, false, location)
	if compared := leftEnd.Compare(rightEnd); compared != 0 {
		return compared
	}
	if compared := strings.Compare(
		left.AccountAlias,
		right.AccountAlias,
	); compared != 0 {
		return compared
	}
	return strings.Compare(
		left.Event.Provenance.SourceObjectID,
		right.Event.Provenance.SourceObjectID,
	)
}

func projectedAgendaDisplayTime(
	event ProjectedAgendaEvent,
	start bool,
	location *time.Location,
) time.Time {
	value := event.DisplayStart
	if !start {
		value = event.DisplayEnd
	}
	if event.Event.IsAllDay {
		parsed, _ := time.ParseInLocation("2006-01-02", value, location)
		return parsed
	}
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func projectionAgendaWindow(
	events []ProjectedAgendaEvent,
	offset, limit int,
) []ProjectedAgendaEvent {
	if offset >= len(events) {
		return []ProjectedAgendaEvent{}
	}
	end := min(len(events), offset+limit)
	return append([]ProjectedAgendaEvent(nil), events[offset:end]...)
}

func (page AgendaProjectionPage) Validate() error {
	location, err := (AgendaProjectionInput{
		Start: page.Start, End: page.End,
		DisplayTimeZone: page.DisplayTimeZone,
		Offset:          page.Offset, Limit: page.Limit,
	}).validate()
	if err != nil {
		return err
	}
	if len(page.Events) > page.Limit {
		return errors.New("agenda projection page is unbounded")
	}
	if err := validateProjectionEnvelope(
		page.Accounts,
		page.Failures,
		page.Complete,
	); err != nil {
		return err
	}
	accounts := make(map[domain.AccountID]ProjectionAccountStatus, len(page.Accounts))
	for _, account := range page.Accounts {
		accounts[account.Account] = account
	}
	for _, event := range page.Events {
		status, exists := accounts[event.Event.Provenance.AccountID]
		if !exists || !status.Complete ||
			status.Alias != event.AccountAlias ||
			status.Provider != event.Event.Provenance.Provider ||
			event.DisplayTimeZone != page.DisplayTimeZone {
			return errors.New("projected agenda event has inconsistent provenance")
		}
		if event.Event.IsAllDay {
			if _, err := time.Parse(
				"2006-01-02",
				event.DisplayStart,
			); err != nil {
				return errors.New("projected all-day display start is invalid")
			}
			if _, err := time.Parse(
				"2006-01-02",
				event.DisplayEnd,
			); err != nil {
				return errors.New("projected all-day display end is invalid")
			}
		} else {
			if _, err := time.Parse(
				time.RFC3339,
				event.DisplayStart,
			); err != nil {
				return errors.New("projected agenda display start is invalid")
			}
			if _, err := time.Parse(
				time.RFC3339,
				event.DisplayEnd,
			); err != nil {
				return errors.New("projected agenda display end is invalid")
			}
		}
		if err := validateAgendaSourceEvent(
			event.Event,
			ProjectionAccount{
				Account:          status.Account,
				Alias:            status.Alias,
				CalendarProvider: status.Provider,
			},
		); err != nil {
			return err
		}
	}
	if !slices.IsSortedFunc(
		page.Events,
		func(left, right ProjectedAgendaEvent) int {
			return compareProjectedAgenda(left, right, location)
		},
	) {
		return errors.New("projected agenda is not stably ordered")
	}
	if page.HasMore {
		if page.NextOffset != page.Offset+len(page.Events) {
			return errors.New("agenda projection next offset is invalid")
		}
	} else if page.NextOffset != 0 {
		return errors.New("terminal agenda projection has a next offset")
	}
	return nil
}
