package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxSavedQueries             = 64
	MaxSavedQueryNameBytes      = 64
	MaxSavedCalendarOffsetMins  = 31 * 24 * 60
	MaxSavedCalendarWindowMins  = 31 * 24 * 60
	SavedQueryDefinitionVersion = 1
)

type SavedQueryKind string

const (
	SavedQueryMail     SavedQueryKind = "mail"
	SavedQueryCalendar SavedQueryKind = "calendar"
)

type SavedMailQuery struct {
	Folder   MailFolder `json:"folder"`
	Query    string     `json:"query"`
	Limit    int        `json:"limit"`
	TimeZone string     `json:"timeZone"`
}

// SavedCalendarQuery is relative to execution time so a private definition
// such as "the next seven days" does not silently become an obsolete absolute
// range. Provider calendar search is not invented where no typed contract
// exists.
type SavedCalendarQuery struct {
	Calendar           CalendarFolder `json:"calendar"`
	StartOffsetMinutes int            `json:"startOffsetMinutes"`
	WindowMinutes      int            `json:"windowMinutes"`
	DisplayTimeZone    string         `json:"displayTimeZone"`
}

// SavedQueryDefinition contains private query configuration but never provider
// content or authentication material. Revision is a deterministic digest of
// the complete definition and protects review/apply against concurrent edits.
type SavedQueryDefinition struct {
	Version  int                 `json:"version"`
	Account  domain.AccountID    `json:"account"`
	Name     string              `json:"name"`
	Kind     SavedQueryKind      `json:"kind"`
	Mail     *SavedMailQuery     `json:"mail,omitempty"`
	Calendar *SavedCalendarQuery `json:"calendar,omitempty"`
	Revision string              `json:"revision"`
}

type SavedQueryCatalog struct {
	Account domain.AccountID       `json:"account"`
	Queries []SavedQueryDefinition `json:"queries"`
}

type SavedQuerySaveInput struct {
	Account  domain.AccountID    `json:"account"`
	Name     string              `json:"name"`
	Kind     SavedQueryKind      `json:"kind"`
	Mail     *SavedMailQuery     `json:"mail,omitempty"`
	Calendar *SavedCalendarQuery `json:"calendar,omitempty"`
}

type SavedQueryDeleteInput struct {
	Account domain.AccountID `json:"account"`
	Name    string           `json:"name"`
}

type SavedQueryPurgeInput struct {
	Account domain.AccountID `json:"account"`
}

type SavedQueryRunInput struct {
	Account domain.AccountID `json:"account"`
	Name    string           `json:"name"`
	Offset  int              `json:"offset"`
}

type SavedQueryChangeReview struct {
	Action           string               `json:"action"`
	Account          domain.AccountID     `json:"account"`
	Name             string               `json:"name"`
	Kind             SavedQueryKind       `json:"kind"`
	Definition       SavedQueryDefinition `json:"definition"`
	PreviousRevision string               `json:"previousRevision,omitempty"`
	Replaces         bool                 `json:"replaces"`
	Private          bool                 `json:"private"`
	StoresContent    bool                 `json:"storesContent"`
}

type SavedQueryChangeAccess struct {
	Status  string                  `json:"status"`
	Review  *SavedQueryChangeReview `json:"review,omitempty"`
	Preview *approval.Preview       `json:"preview,omitempty"`
	Query   *SavedQueryDefinition   `json:"query,omitempty"`
}

// SavedQueryCatalogState is a content-free snapshot used to bind an explicit
// purge to the exact private catalog observed during review. Corrupt is true
// only for a bounded regular file that could not be decoded safely.
type SavedQueryCatalogState struct {
	Revision    string `json:"revision"`
	Definitions int    `json:"definitions"`
	Corrupt     bool   `json:"corrupt"`
}

type SavedQueryPurgeReview struct {
	Action          string           `json:"action"`
	Account         domain.AccountID `json:"account"`
	CatalogRevision string           `json:"catalogRevision"`
	Definitions     int              `json:"definitions"`
	Corrupt         bool             `json:"corrupt"`
	Private         bool             `json:"private"`
	StoresContent   bool             `json:"storesContent"`
}

type SavedQueryPurgeAccess struct {
	Status  string                 `json:"status"`
	Review  *SavedQueryPurgeReview `json:"review,omitempty"`
	Preview *approval.Preview      `json:"preview,omitempty"`
	Purged  bool                   `json:"purged"`
}

// SavedQueryExecution is always a live provider result. Corresync deliberately
// has no persistent mail/calendar content cache, so stale and cached remain
// explicit false fields instead of being inferred by consumers.
type SavedQueryExecution struct {
	Query           SavedQueryDefinition `json:"query"`
	FetchedAt       time.Time            `json:"fetchedAt"`
	Source          string               `json:"source"`
	Cached          bool                 `json:"cached"`
	Stale           bool                 `json:"stale"`
	DisplayTimeZone string               `json:"displayTimeZone,omitempty"`
	Mail            *MailPage            `json:"mail,omitempty"`
	Calendar        *CalendarPage        `json:"calendar,omitempty"`
}

type SavedQueryRepository interface {
	ListSavedQueries(context.Context, domain.AccountID) ([]SavedQueryDefinition, error)
	PutSavedQuery(context.Context, SavedQueryDefinition, string) error
	DeleteSavedQuery(context.Context, domain.AccountID, string, string) error
	InspectSavedQueryCatalog(context.Context, domain.AccountID) (SavedQueryCatalogState, error)
	PurgeSavedQueryCatalog(context.Context, domain.AccountID, string) error
}

type SavedQueryReader interface {
	SearchMail(context.Context, MailSearchInput, domain.Caller) (MailPage, error)
	ListCalendar(context.Context, CalendarListInput, domain.Caller) (CalendarPage, error)
}

type SavedQueryService struct {
	repository SavedQueryRepository
	reader     SavedQueryReader
	now        func() time.Time
}

func NewSavedQueryService(
	repository SavedQueryRepository,
	reader SavedQueryReader,
) (*SavedQueryService, error) {
	if repository == nil {
		return nil, errors.New("saved query repository is required")
	}
	return &SavedQueryService{
		repository: repository,
		reader:     reader,
		now:        time.Now,
	}, nil
}

func (service *SavedQueryService) List(
	ctx context.Context,
	account domain.AccountID,
) (SavedQueryCatalog, error) {
	if err := account.ValidateOpaque(); err != nil {
		return SavedQueryCatalog{}, err
	}
	queries, err := service.repository.ListSavedQueries(ctx, account)
	if err != nil {
		return SavedQueryCatalog{}, err
	}
	if len(queries) > MaxSavedQueries {
		return SavedQueryCatalog{}, errors.New("saved query repository exceeded its bound")
	}
	for _, query := range queries {
		if query.Account != account {
			return SavedQueryCatalog{}, errors.New("saved query crossed its account boundary")
		}
		if err := query.Validate(); err != nil {
			return SavedQueryCatalog{}, fmt.Errorf("invalid saved query repository result: %w", err)
		}
	}
	return SavedQueryCatalog{Account: account, Queries: queries}, nil
}

func (service *SavedQueryService) Get(
	ctx context.Context,
	input SavedQueryDeleteInput,
) (SavedQueryDefinition, error) {
	if err := input.Validate(); err != nil {
		return SavedQueryDefinition{}, err
	}
	catalog, err := service.List(ctx, input.Account)
	if err != nil {
		return SavedQueryDefinition{}, err
	}
	for _, query := range catalog.Queries {
		if query.Name == input.Name {
			return query, nil
		}
	}
	return SavedQueryDefinition{}, errors.New("saved query was not found in this account")
}

func (service *SavedQueryService) ReviewSave(
	ctx context.Context,
	input SavedQuerySaveInput,
) (SavedQueryChangeReview, error) {
	definition := input.Definition()
	if err := definition.Validate(); err != nil {
		return SavedQueryChangeReview{}, err
	}
	revision, err := savedQueryRevision(definition)
	if err != nil {
		return SavedQueryChangeReview{}, err
	}
	definition.Revision = revision
	catalog, err := service.List(ctx, input.Account)
	if err != nil {
		return SavedQueryChangeReview{}, err
	}
	review := SavedQueryChangeReview{
		Action: "save", Account: input.Account, Name: input.Name, Kind: input.Kind,
		Definition: definition, Private: true, StoresContent: false,
	}
	for _, existing := range catalog.Queries {
		if existing.Name == input.Name {
			review.PreviousRevision = existing.Revision
			review.Replaces = true
			return review, nil
		}
	}
	if len(catalog.Queries) >= MaxSavedQueries {
		return SavedQueryChangeReview{}, fmt.Errorf(
			"at most %d saved queries are supported per account",
			MaxSavedQueries,
		)
	}
	return review, nil
}

func (service *SavedQueryService) ApplySave(
	ctx context.Context,
	review SavedQueryChangeReview,
) (SavedQueryDefinition, error) {
	if err := review.Validate("save"); err != nil {
		return SavedQueryDefinition{}, err
	}
	if err := service.repository.PutSavedQuery(
		ctx,
		review.Definition,
		review.PreviousRevision,
	); err != nil {
		return SavedQueryDefinition{}, err
	}
	return review.Definition, nil
}

func (service *SavedQueryService) ReviewDelete(
	ctx context.Context,
	input SavedQueryDeleteInput,
) (SavedQueryChangeReview, error) {
	query, err := service.Get(ctx, input)
	if err != nil {
		return SavedQueryChangeReview{}, err
	}
	return SavedQueryChangeReview{
		Action: "delete", Account: input.Account, Name: input.Name,
		Kind: query.Kind, Definition: query,
		PreviousRevision: query.Revision, Replaces: true,
		Private: true, StoresContent: false,
	}, nil
}

func (service *SavedQueryService) ApplyDelete(
	ctx context.Context,
	review SavedQueryChangeReview,
) error {
	if err := review.Validate("delete"); err != nil {
		return err
	}
	return service.repository.DeleteSavedQuery(
		ctx,
		review.Account,
		review.Name,
		review.PreviousRevision,
	)
}

func (service *SavedQueryService) ReviewPurge(
	ctx context.Context,
	input SavedQueryPurgeInput,
) (SavedQueryPurgeReview, error) {
	if err := input.Account.ValidateOpaque(); err != nil {
		return SavedQueryPurgeReview{}, err
	}
	state, err := service.repository.InspectSavedQueryCatalog(ctx, input.Account)
	if err != nil {
		return SavedQueryPurgeReview{}, err
	}
	if state.Revision == "" {
		return SavedQueryPurgeReview{}, errors.New("saved query catalog is already absent")
	}
	return SavedQueryPurgeReview{
		Action: "purge", Account: input.Account,
		CatalogRevision: state.Revision, Definitions: state.Definitions,
		Corrupt: state.Corrupt, Private: true, StoresContent: false,
	}, nil
}

func (service *SavedQueryService) ApplyPurge(
	ctx context.Context,
	review SavedQueryPurgeReview,
) error {
	if err := review.Validate(); err != nil {
		return err
	}
	return service.repository.PurgeSavedQueryCatalog(
		ctx,
		review.Account,
		review.CatalogRevision,
	)
}

func (service *SavedQueryService) Run(
	ctx context.Context,
	input SavedQueryRunInput,
	caller domain.Caller,
) (SavedQueryExecution, error) {
	if err := input.Validate(); err != nil {
		return SavedQueryExecution{}, err
	}
	if err := caller.Validate(); err != nil {
		return SavedQueryExecution{}, err
	}
	if service.reader == nil {
		return SavedQueryExecution{}, errors.New("saved query execution is unavailable")
	}
	query, err := service.Get(ctx, SavedQueryDeleteInput{
		Account: input.Account,
		Name:    input.Name,
	})
	if err != nil {
		return SavedQueryExecution{}, err
	}
	now := service.now().UTC()
	result := SavedQueryExecution{
		Query: query, FetchedAt: now, Source: "live_provider",
		Cached: false, Stale: false,
	}
	switch query.Kind {
	case SavedQueryMail:
		page, err := service.reader.SearchMail(ctx, MailSearchInput{
			Account: input.Account, Folder: query.Mail.Folder,
			Query: query.Mail.Query, Offset: input.Offset,
			Limit: query.Mail.Limit, TimeZone: query.Mail.TimeZone,
		}, caller)
		if err != nil {
			return SavedQueryExecution{}, err
		}
		result.Mail = &page
		result.DisplayTimeZone = query.Mail.TimeZone
	case SavedQueryCalendar:
		start := now.Add(time.Duration(query.Calendar.StartOffsetMinutes) * time.Minute)
		end := start.Add(time.Duration(query.Calendar.WindowMinutes) * time.Minute)
		page, err := service.reader.ListCalendar(ctx, CalendarListInput{
			Account: input.Account, Calendar: query.Calendar.Calendar,
			Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339),
		}, caller)
		if err != nil {
			return SavedQueryExecution{}, err
		}
		result.Calendar = &page
		result.DisplayTimeZone = query.Calendar.DisplayTimeZone
	default:
		return SavedQueryExecution{}, errors.New("saved query kind is unsupported")
	}
	return result, nil
}

func (input SavedQuerySaveInput) Definition() SavedQueryDefinition {
	return SavedQueryDefinition{
		Version:  SavedQueryDefinitionVersion,
		Account:  input.Account,
		Name:     input.Name,
		Kind:     input.Kind,
		Mail:     cloneSavedMailQuery(input.Mail),
		Calendar: cloneSavedCalendarQuery(input.Calendar),
	}
}

func (input SavedQueryDeleteInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	return validateSavedQueryName(input.Name)
}

func (input SavedQueryRunInput) Validate() error {
	if err := (SavedQueryDeleteInput{Account: input.Account, Name: input.Name}).Validate(); err != nil {
		return err
	}
	if input.Offset < 0 || input.Offset > MaxMailOffset {
		return fmt.Errorf("saved query offset must be between 0 and %d", MaxMailOffset)
	}
	return nil
}

func (query SavedQueryDefinition) Validate() error {
	if query.Version != SavedQueryDefinitionVersion {
		return errors.New("saved query definition version is unsupported")
	}
	if err := query.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateSavedQueryName(query.Name); err != nil {
		return err
	}
	switch query.Kind {
	case SavedQueryMail:
		if query.Mail == nil || query.Calendar != nil {
			return errors.New("mail saved query requires exactly one mail definition")
		}
		if err := (MailSearchInput{
			Account: query.Account, Folder: query.Mail.Folder,
			Query: query.Mail.Query, Limit: query.Mail.Limit,
			TimeZone: query.Mail.TimeZone,
		}).Validate(); err != nil {
			return fmt.Errorf("mail saved query: %w", err)
		}
	case SavedQueryCalendar:
		if query.Calendar == nil || query.Mail != nil {
			return errors.New("calendar saved query requires exactly one calendar definition")
		}
		if err := validateCalendarFolder(query.Calendar.Calendar); err != nil {
			return fmt.Errorf("calendar saved query: %w", err)
		}
		if query.Calendar.StartOffsetMinutes < -MaxSavedCalendarOffsetMins ||
			query.Calendar.StartOffsetMinutes > MaxSavedCalendarOffsetMins {
			return fmt.Errorf(
				"calendar start offset must be between -%d and %d minutes",
				MaxSavedCalendarOffsetMins,
				MaxSavedCalendarOffsetMins,
			)
		}
		if query.Calendar.WindowMinutes < 1 ||
			query.Calendar.WindowMinutes > MaxSavedCalendarWindowMins {
			return fmt.Errorf(
				"calendar window must be between 1 and %d minutes",
				MaxSavedCalendarWindowMins,
			)
		}
		if err := validateSavedQueryTimeZone(query.Calendar.DisplayTimeZone); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported saved query kind %q", query.Kind)
	}
	if query.Revision != "" {
		if len(query.Revision) != sha256.Size*2 {
			return errors.New("saved query revision is invalid")
		}
		if _, err := hex.DecodeString(query.Revision); err != nil {
			return errors.New("saved query revision is invalid")
		}
		revision, err := savedQueryRevision(query)
		if err != nil {
			return err
		}
		if query.Revision != revision {
			return errors.New("saved query revision does not match its definition")
		}
	}
	return nil
}

func (review SavedQueryChangeReview) Validate(action string) error {
	if review.Action != action || review.Account != review.Definition.Account ||
		review.Name != review.Definition.Name || review.Kind != review.Definition.Kind ||
		!review.Private || review.StoresContent || review.Definition.Revision == "" {
		return errors.New("saved query review is inconsistent")
	}
	if err := review.Definition.Validate(); err != nil {
		return err
	}
	if action == "save" && review.Replaces != (review.PreviousRevision != "") ||
		action == "delete" && (!review.Replaces || review.PreviousRevision != review.Definition.Revision) {
		return errors.New("saved query review revision binding is inconsistent")
	}
	return nil
}

func (review SavedQueryPurgeReview) Validate() error {
	if review.Action != "purge" || review.CatalogRevision == "" ||
		review.Definitions < 0 || review.Definitions > MaxSavedQueries ||
		!review.Private || review.StoresContent {
		return errors.New("saved query purge review is inconsistent")
	}
	if err := review.Account.ValidateOpaque(); err != nil {
		return err
	}
	if len(review.CatalogRevision) != sha256.Size*2 {
		return errors.New("saved query catalog revision is invalid")
	}
	if _, err := hex.DecodeString(review.CatalogRevision); err != nil {
		return errors.New("saved query catalog revision is invalid")
	}
	if review.Corrupt && review.Definitions != 0 {
		return errors.New("corrupt saved query purge review cannot claim definitions")
	}
	return nil
}

func validateSavedQueryName(value string) error {
	if value == "" || len(value) > MaxSavedQueryNameBytes ||
		!utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return errors.New("saved query name must contain 1 through 64 bounded characters")
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return errors.New("saved query name must use letters, digits, dots, dashes, or underscores")
	}
	return nil
}

func validateSavedQueryTimeZone(value string) error {
	if value == "" {
		return errors.New("saved query display time zone is required")
	}
	if len(value) > 128 || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("saved query display time zone is invalid")
	}
	if _, err := time.LoadLocation(value); err != nil {
		return errors.New("saved query display time zone is unknown")
	}
	return nil
}

func savedQueryRevision(query SavedQueryDefinition) (string, error) {
	query.Revision = ""
	encoded, err := json.Marshal(query)
	if err != nil {
		return "", fmt.Errorf("encode saved query revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneSavedMailQuery(query *SavedMailQuery) *SavedMailQuery {
	if query == nil {
		return nil
	}
	copy := *query
	return &copy
}

func cloneSavedCalendarQuery(query *SavedCalendarQuery) *SavedCalendarQuery {
	if query == nil {
		return nil
	}
	copy := *query
	return &copy
}
