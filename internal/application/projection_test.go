package application

import (
	"context"
	"slices"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	projectionAlpha   domain.AccountID = "acc_00000000000000000000000000000001"
	projectionOffline domain.AccountID = "acc_00000000000000000000000000000002"
	projectionZeta    domain.AccountID = "acc_00000000000000000000000000000003"
)

type projectionReaderStub struct {
	accounts      []ProjectionAccount
	mail          map[domain.AccountID][]MailSummary
	mailErrors    map[domain.AccountID]error
	mailSnapshots map[domain.AccountID]bool
	calendars     map[domain.AccountID][]CalendarEvent
	calendarError map[domain.AccountID]error
	mailCalls     []MailSearchInput
}

func (reader *projectionReaderStub) ProjectionAccounts(
	context.Context,
) ([]ProjectionAccount, error) {
	return append([]ProjectionAccount(nil), reader.accounts...), nil
}

func (reader *projectionReaderStub) SearchMail(
	_ context.Context,
	input MailSearchInput,
	_ domain.Caller,
) (MailPage, error) {
	reader.mailCalls = append(reader.mailCalls, input)
	if err := reader.mailErrors[input.Account]; err != nil {
		return MailPage{}, err
	}
	messages := reader.mail[input.Account]
	start := min(input.Offset, len(messages))
	end := min(start+input.Limit, len(messages))
	return MailPage{
		Messages:         append([]MailSummary(nil), messages[start:end]...),
		TotalItemsInView: len(messages),
		IncludesLastItem: end == len(messages) &&
			!reader.mailSnapshots[input.Account],
	}, nil
}

func (reader *projectionReaderStub) ListCalendar(
	_ context.Context,
	input CalendarListInput,
	_ domain.Caller,
) (CalendarPage, error) {
	if err := reader.calendarError[input.Account]; err != nil {
		return CalendarPage{}, err
	}
	return CalendarPage{
		Events: append(
			[]CalendarEvent(nil),
			reader.calendars[input.Account]...,
		),
		Start: input.Start,
		End:   input.End,
	}, nil
}

func TestMailProjectionHasStablePaginationProvenanceAndPartialFailure(t *testing.T) {
	reader := &projectionReaderStub{
		accounts: []ProjectionAccount{
			projectionAccount(
				projectionZeta,
				"zeta",
				domain.ProviderGoogle,
				"",
				true,
				domain.Degradation{
					Feature: "mail.search",
					Reason:  "synthetic provider query semantics differ",
					Lossy:   true,
				},
			),
			projectionAccount(
				projectionOffline,
				"offline",
				domain.ProviderMicrosoftOWA,
				"",
				false,
			),
			projectionAccount(
				projectionAlpha,
				"alpha",
				domain.ProviderMicrosoftGraph,
				"",
				true,
			),
		},
		mail: map[domain.AccountID][]MailSummary{
			projectionAlpha: {
				projectionMail(
					projectionAlpha,
					domain.ProviderMicrosoftGraph,
					"alpha-1",
					"2026-07-28T10:00:00Z",
				),
			},
			projectionZeta: {
				projectionMail(
					projectionZeta,
					domain.ProviderGoogle,
					"zeta-1",
					"2026-07-28T11:00:00Z",
				),
				projectionMail(
					projectionZeta,
					domain.ProviderGoogle,
					"zeta-2",
					"2026-07-28T09:00:00Z",
				),
			},
		},
	}
	service, err := NewProjectionService(reader)
	if err != nil {
		t.Fatal(err)
	}
	input := MailProjectionInput{
		Folder: MailFolder{
			Kind: MailFolderDistinguished,
			ID:   "inbox",
		},
		Query: "synthetic", Offset: 1, Limit: 1, TimeZone: "UTC",
	}
	first, err := service.SearchAllMail(
		t.Context(),
		input,
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("SearchAllMail() error = %v", err)
	}
	if first.Complete || len(first.Failures) != 1 ||
		first.Failures[0].Alias != "offline" ||
		first.Failures[0].Code != "not_authenticated" {
		t.Fatalf("partial failure is not explicit: %+v", first)
	}
	if aliases := projectionStatusAliases(first.Accounts); !slices.Equal(
		aliases,
		[]string{"alpha", "offline", "zeta"},
	) {
		t.Fatalf("account order = %v", aliases)
	}
	if len(first.Messages) != 1 ||
		first.Messages[0].AccountAlias != "alpha" ||
		first.Messages[0].Message.ID != "alpha-1" ||
		first.Messages[0].Message.Provenance.AccountID != projectionAlpha ||
		!first.HasMore || first.NextOffset != 2 {
		t.Fatalf("unexpected stable page: %+v", first)
	}
	if len(first.Accounts[2].Degradations) != 1 ||
		first.Accounts[2].Degradations[0].Feature != "mail.search" {
		t.Fatalf("provider degradation was not retained: %+v", first.Accounts)
	}

	input.Offset = first.NextOffset
	second, err := service.SearchAllMail(
		t.Context(),
		input,
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("second SearchAllMail() error = %v", err)
	}
	if len(second.Messages) != 1 ||
		second.Messages[0].Message.ID != "zeta-2" ||
		second.HasMore || second.NextOffset != 0 {
		t.Fatalf("unexpected terminal page: %+v", second)
	}
}

func TestAgendaProjectionNormalizesDisplayAndRetainsOriginalSemantics(t *testing.T) {
	reader := &projectionReaderStub{
		accounts: []ProjectionAccount{
			projectionAccount(
				projectionAlpha,
				"alpha",
				"",
				domain.ProviderCalDAV,
				true,
			),
		},
		calendars: map[domain.AccountID][]CalendarEvent{
			projectionAlpha: {
				projectionEvent(
					"timed",
					"2026-07-28T09:00:00Z",
					"2026-07-28T10:00:00Z",
					"2026-07-28T10:00:00",
					"2026-07-28T11:00:00",
					"Europe/London",
					false,
					false,
				),
				projectionEvent(
					"floating",
					"2026-07-28T10:00:00Z",
					"2026-07-28T11:00:00Z",
					"2026-07-28T10:00:00",
					"2026-07-28T11:00:00",
					"",
					true,
					false,
				),
				projectionEvent(
					"all-day",
					"2026-07-29T00:00:00Z",
					"2026-07-30T00:00:00Z",
					"2026-07-29",
					"2026-07-30",
					"",
					false,
					true,
				),
			},
		},
	}
	service, err := NewProjectionService(reader)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListAgenda(
		t.Context(),
		AgendaProjectionInput{
			Start:           "2026-07-28T00:00:00Z",
			End:             "2026-07-31T00:00:00Z",
			DisplayTimeZone: "America/New_York",
			Limit:           100,
		},
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatalf("ListAgenda() error = %v", err)
	}
	if !page.Complete || len(page.Events) != 3 {
		t.Fatalf("unexpected agenda page: %+v", page)
	}
	if page.Events[0].DisplayStart != "2026-07-28T05:00:00-04:00" ||
		page.Events[0].Event.OriginalStartTimeZone != "Europe/London" ||
		page.Events[0].Event.OriginalStart != "2026-07-28T10:00:00" {
		t.Fatalf("timed event semantics changed: %+v", page.Events[0])
	}
	if page.Events[1].DisplayStart != "2026-07-28T10:00:00-04:00" ||
		!page.Events[1].Event.OriginalStartFloating {
		t.Fatalf("floating event wall clock changed: %+v", page.Events[1])
	}
	if page.Events[2].DisplayStart != "2026-07-29" ||
		page.Events[2].DisplayEnd != "2026-07-30" ||
		!page.Events[2].Event.IsAllDay {
		t.Fatalf("all-day event date semantics changed: %+v", page.Events[2])
	}
}

func TestMailProjectionKeepsShortGoogleWebSnapshot(t *testing.T) {
	t.Parallel()

	reader := &projectionReaderStub{
		accounts: []ProjectionAccount{
			projectionAccount(
				projectionAlpha,
				"google",
				domain.ProviderGoogleWeb,
				"",
				true,
				domain.Degradation{
					Feature: "mail.pagination",
					Reason:  "synthetic bounded DOM snapshot",
					Lossy:   true,
				},
			),
		},
		mail: map[domain.AccountID][]MailSummary{
			projectionAlpha: {
				projectionMail(
					projectionAlpha,
					domain.ProviderGoogleWeb,
					"visible-1",
					"2026-07-28T10:00:00Z",
				),
				projectionMail(
					projectionAlpha,
					domain.ProviderGoogleWeb,
					"visible-2",
					"2026-07-28T09:00:00Z",
				),
			},
		},
		mailSnapshots: map[domain.AccountID]bool{projectionAlpha: true},
	}
	service, err := NewProjectionService(reader)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.SearchAllMail(
		t.Context(),
		MailProjectionInput{
			Folder: MailFolder{
				Kind: MailFolderDistinguished,
				ID:   "inbox",
			},
			Query: "synthetic", Limit: 25, TimeZone: "UTC",
		},
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Complete || page.HasMore || len(page.Failures) != 0 ||
		len(page.Messages) != 2 ||
		!page.Accounts[0].Complete ||
		!page.Accounts[0].Exhausted {
		t.Fatalf("Google Web snapshot projection = %#v", page)
	}
}

func TestProjectionInputsRejectAccountSpecificOrUnboundedReads(t *testing.T) {
	t.Parallel()
	mail := MailProjectionInput{
		Folder: MailFolder{Kind: MailFolderOpaque, ID: "folder-1"},
		Query:  "synthetic", Limit: 25, TimeZone: "UTC",
	}
	if err := mail.Validate(); err == nil {
		t.Fatal("cross-account opaque folder was accepted")
	}
	mail.Folder = MailFolder{Kind: MailFolderDistinguished, ID: "inbox"}
	mail.Offset = MaxProjectionOffset + 1
	if err := mail.Validate(); err == nil {
		t.Fatal("unbounded mail projection offset was accepted")
	}
	if _, err := (AgendaProjectionInput{
		Start:           "2026-07-28T00:00:00Z",
		End:             "2026-07-29T00:00:00Z",
		DisplayTimeZone: "GMT Standard Time",
		Limit:           25,
	}).validate(); err == nil {
		t.Fatal("provider-specific display time zone was accepted")
	}
}

func projectionAccount(
	account domain.AccountID,
	alias string,
	mail, calendar domain.ProviderID,
	authenticated bool,
	degradations ...domain.Degradation,
) ProjectionAccount {
	result := ProjectionAccount{
		Account: account, Alias: alias,
		MailProvider: mail, CalendarProvider: calendar,
		Authenticated: authenticated,
	}
	if authenticated {
		result.Capabilities = &domain.Capabilities{
			Mail: mail != "", Calendar: calendar != "",
		}
		if mail != "" {
			result.MailDegradations = degradations
		} else {
			result.CalendarDegradations = degradations
		}
	}
	return result
}

func projectionMail(
	account domain.AccountID,
	provider domain.ProviderID,
	id, received string,
) MailSummary {
	return MailSummary{
		ID: id, Subject: id, ReceivedAt: received,
		Provenance: domain.Provenance{
			AccountID: account, Provider: provider,
			MailboxID: "synthetic-mailbox", SourceObjectID: id,
		},
	}
}

func projectionEvent(
	id, start, end, originalStart, originalEnd, zone string,
	floating, allDay bool,
) CalendarEvent {
	return CalendarEvent{
		ID: id, Subject: id, Start: start, End: end,
		OriginalStart: originalStart, OriginalEnd: originalEnd,
		OriginalStartTimeZone: zone, OriginalEndTimeZone: zone,
		OriginalStartFloating: floating, OriginalEndFloating: floating,
		IsAllDay: allDay,
		Provenance: domain.Provenance{
			AccountID: projectionAlpha, Provider: domain.ProviderCalDAV,
			CalendarID: "synthetic-calendar", SourceObjectID: id,
		},
	}
}

func projectionStatusAliases(
	statuses []ProjectionAccountStatus,
) []string {
	result := make([]string, 0, len(statuses))
	for _, status := range statuses {
		result = append(result, status.Alias)
	}
	return result
}

var _ ProjectionReader = (*projectionReaderStub)(nil)
