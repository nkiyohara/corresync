package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

const savedQueryTestAccount domain.AccountID = "acc_00000000000000000000000000000051"

type savedQueryRepositoryStub struct {
	queries []SavedQueryDefinition
}

func (repository *savedQueryRepositoryStub) ListSavedQueries(
	context.Context,
	domain.AccountID,
) ([]SavedQueryDefinition, error) {
	return append([]SavedQueryDefinition(nil), repository.queries...), nil
}

func (repository *savedQueryRepositoryStub) PutSavedQuery(
	_ context.Context,
	query SavedQueryDefinition,
	expected string,
) error {
	for index := range repository.queries {
		if repository.queries[index].Name != query.Name {
			continue
		}
		if repository.queries[index].Revision != expected {
			return errors.New("stale")
		}
		repository.queries[index] = query
		return nil
	}
	if expected != "" {
		return errors.New("stale")
	}
	repository.queries = append(repository.queries, query)
	return nil
}

func (repository *savedQueryRepositoryStub) DeleteSavedQuery(
	_ context.Context,
	_ domain.AccountID,
	name string,
	expected string,
) error {
	for index := range repository.queries {
		if repository.queries[index].Name == name &&
			repository.queries[index].Revision == expected {
			repository.queries = append(
				repository.queries[:index],
				repository.queries[index+1:]...,
			)
			return nil
		}
	}
	return errors.New("stale")
}

func (repository *savedQueryRepositoryStub) InspectSavedQueryCatalog(
	context.Context,
	domain.AccountID,
) (SavedQueryCatalogState, error) {
	if len(repository.queries) == 0 {
		return SavedQueryCatalogState{}, nil
	}
	return SavedQueryCatalogState{
		Revision: strings.Repeat("a", 64), Definitions: len(repository.queries),
	}, nil
}

func (repository *savedQueryRepositoryStub) PurgeSavedQueryCatalog(
	_ context.Context,
	_ domain.AccountID,
	expected string,
) error {
	if expected != strings.Repeat("a", 64) {
		return errors.New("stale")
	}
	repository.queries = nil
	return nil
}

type savedQueryReaderStub struct {
	mailInput     MailSearchInput
	calendarInput CalendarListInput
}

func (reader *savedQueryReaderStub) SearchMail(
	_ context.Context,
	input MailSearchInput,
	_ domain.Caller,
) (MailPage, error) {
	reader.mailInput = input
	return MailPage{Messages: []MailSummary{{ID: "message-1"}}}, nil
}

func (reader *savedQueryReaderStub) ListCalendar(
	_ context.Context,
	input CalendarListInput,
	_ domain.Caller,
) (CalendarPage, error) {
	reader.calendarInput = input
	return CalendarPage{Start: input.Start, End: input.End}, nil
}

func TestSavedQueryServiceReviewsAndAppliesPrivateDefinitions(t *testing.T) {
	repository := &savedQueryRepositoryStub{}
	service, err := NewSavedQueryService(repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := savedMailQueryInput("priority")
	review, err := service.ReviewSave(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if review.Replaces || !review.Private || review.StoresContent ||
		review.Definition.Revision == "" {
		t.Fatalf("new saved query review = %+v", review)
	}
	saved, err := service.ApplySave(t.Context(), review)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != review.Definition.Revision || len(repository.queries) != 1 {
		t.Fatalf("saved query = %+v, repository = %+v", saved, repository.queries)
	}

	input.Mail.Limit = 12
	replacement, err := service.ReviewSave(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replacement.Replaces || replacement.PreviousRevision != saved.Revision ||
		replacement.Definition.Revision == saved.Revision {
		t.Fatalf("replacement review = %+v", replacement)
	}
	if _, err := service.ApplySave(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	deletion, err := service.ReviewDelete(t.Context(), SavedQueryDeleteInput{
		Account: savedQueryTestAccount, Name: "priority",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyDelete(t.Context(), deletion); err != nil {
		t.Fatal(err)
	}
	if len(repository.queries) != 0 {
		t.Fatalf("deleted saved query remains: %+v", repository.queries)
	}
}

func TestSavedQueryRunAlwaysReportsLiveNonCachedMail(t *testing.T) {
	repository := &savedQueryRepositoryStub{}
	reader := &savedQueryReaderStub{}
	service, err := NewSavedQueryService(repository, reader)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewSave(t.Context(), savedMailQueryInput("priority"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplySave(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	result, err := service.Run(t.Context(), SavedQueryRunInput{
		Account: savedQueryTestAccount, Name: "priority", Offset: 7,
	}, domain.Caller{Surface: "cli", Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "live_provider" || result.Cached || result.Stale ||
		!result.FetchedAt.Equal(now) || result.Mail == nil || result.Calendar != nil {
		t.Fatalf("mail saved query result = %+v", result)
	}
	if reader.mailInput.Account != savedQueryTestAccount ||
		reader.mailInput.Offset != 7 || reader.mailInput.Limit != 25 ||
		reader.mailInput.Query != "is:unread importance:high" {
		t.Fatalf("mail execution input = %+v", reader.mailInput)
	}
}

func TestSavedQueryRunResolvesCalendarWindowAtExecution(t *testing.T) {
	repository := &savedQueryRepositoryStub{}
	reader := &savedQueryReaderStub{}
	service, err := NewSavedQueryService(repository, reader)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewSave(t.Context(), SavedQuerySaveInput{
		Account: savedQueryTestAccount, Name: "week", Kind: SavedQueryCalendar,
		Calendar: &SavedCalendarQuery{
			Calendar:           CalendarFolder{Kind: CalendarFolderDistinguished, ID: "calendar"},
			StartOffsetMinutes: 0, WindowMinutes: 7 * 24 * 60,
			DisplayTimeZone: "Europe/London",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplySave(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	result, err := service.Run(t.Context(), SavedQueryRunInput{
		Account: savedQueryTestAccount, Name: "week",
	}, domain.Caller{Surface: "mcp", Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Calendar == nil || result.Mail != nil || result.Cached || result.Stale ||
		result.DisplayTimeZone != "Europe/London" ||
		reader.calendarInput.Start != "2026-08-14T20:00:00Z" ||
		reader.calendarInput.End != "2026-08-21T20:00:00Z" {
		t.Fatalf("calendar saved query result = %+v, input = %+v", result, reader.calendarInput)
	}
}

func TestSavedQueryValidationRejectsCrossKindAndUnsafeNames(t *testing.T) {
	definition := savedMailQueryInput("unsafe/name").Definition()
	if err := definition.Validate(); err == nil {
		t.Fatal("saved query accepted an unsafe name")
	}
	definition = savedMailQueryInput("safe").Definition()
	definition.Calendar = &SavedCalendarQuery{}
	if err := definition.Validate(); err == nil {
		t.Fatal("saved query accepted two query kinds")
	}
}

func TestSavedQueryServiceBindsCatalogPurge(t *testing.T) {
	repository := &savedQueryRepositoryStub{}
	service, err := NewSavedQueryService(repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewSave(t.Context(), savedMailQueryInput("priority"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplySave(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	purge, err := service.ReviewPurge(t.Context(), SavedQueryPurgeInput{
		Account: savedQueryTestAccount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if purge.Definitions != 1 || purge.Corrupt || !purge.Private || purge.StoresContent {
		t.Fatalf("purge review = %+v", purge)
	}
	if err := service.ApplyPurge(t.Context(), purge); err != nil {
		t.Fatal(err)
	}
	if len(repository.queries) != 0 {
		t.Fatalf("purged repository = %+v", repository.queries)
	}
}

func TestSavedQueryServiceRejectsCatalogAbovePerAccountBound(t *testing.T) {
	repository := &savedQueryRepositoryStub{
		queries: make([]SavedQueryDefinition, MaxSavedQueries+1),
	}
	service, err := NewSavedQueryService(repository, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.List(t.Context(), savedQueryTestAccount); err == nil {
		t.Fatal("saved query service accepted an oversized account catalog")
	}
}

func BenchmarkSavedQueryCatalogValidationAtBound(b *testing.B) {
	repository := &savedQueryRepositoryStub{}
	service, err := NewSavedQueryService(repository, nil)
	if err != nil {
		b.Fatal(err)
	}
	for index := 0; index < MaxSavedQueries; index++ {
		input := savedMailQueryInput(fmt.Sprintf("query%02d", index))
		review, reviewErr := service.ReviewSave(context.Background(), input)
		if reviewErr != nil {
			b.Fatal(reviewErr)
		}
		if _, applyErr := service.ApplySave(context.Background(), review); applyErr != nil {
			b.Fatal(applyErr)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := service.List(context.Background(), savedQueryTestAccount); err != nil {
			b.Fatal(err)
		}
	}
}

func savedMailQueryInput(name string) SavedQuerySaveInput {
	return SavedQuerySaveInput{
		Account: savedQueryTestAccount, Name: name, Kind: SavedQueryMail,
		Mail: &SavedMailQuery{
			Folder: MailFolder{Kind: MailFolderDistinguished, ID: "inbox"},
			Query:  "is:unread importance:high", Limit: 25, TimeZone: "UTC",
		},
	}
}
