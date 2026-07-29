package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
)

const MaxMailProjectionPageSize = 50

// MailProjectionInput selects one stable global page over isolated account
// searches. Opaque account-specific folder IDs are intentionally unavailable.
type MailProjectionInput struct {
	Folder   MailFolder `json:"folder"`
	Query    string     `json:"query"`
	Offset   int        `json:"offset"`
	Limit    int        `json:"limit"`
	TimeZone string     `json:"timeZone"`
}

type ProjectedMail struct {
	AccountAlias string      `json:"accountAlias"`
	Message      MailSummary `json:"message"`
}

type MailProjectionPage struct {
	Messages   []ProjectedMail           `json:"messages"`
	Accounts   []ProjectionAccountStatus `json:"accounts"`
	Failures   []ProjectionFailure       `json:"failures"`
	Offset     int                       `json:"offset"`
	Limit      int                       `json:"limit"`
	NextOffset int                       `json:"nextOffset,omitempty"`
	HasMore    bool                      `json:"hasMore"`
	Complete   bool                      `json:"complete"`
}

type mailProjectionSource struct {
	status   ProjectionAccountStatus
	messages []ProjectedMail
}

func (service *ProjectionService) SearchAllMail(
	ctx context.Context,
	input MailProjectionInput,
	caller domain.Caller,
) (MailProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return MailProjectionPage{}, err
	}
	if err := caller.Validate(); err != nil {
		return MailProjectionPage{}, err
	}
	accounts, err := service.accounts(ctx)
	if err != nil {
		return MailProjectionPage{}, err
	}
	mailAccounts := make([]ProjectionAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.MailProvider != "" {
			mailAccounts = append(mailAccounts, account)
		}
	}
	if len(mailAccounts) == 0 {
		return MailProjectionPage{}, errors.New(
			"no configured account has a mail route",
		)
	}
	sources := make([]mailProjectionSource, len(mailAccounts))
	for index, account := range mailAccounts {
		if err := ctx.Err(); err != nil {
			return MailProjectionPage{}, err
		}
		sources[index] = service.searchProjectionAccount(
			ctx,
			account,
			input,
			caller,
		)
	}
	if err := ctx.Err(); err != nil {
		return MailProjectionPage{}, err
	}

	statuses := make([]ProjectionAccountStatus, 0, len(sources))
	messages := make([]ProjectedMail, 0, len(sources)*(input.Offset+input.Limit+1))
	sourceHasMore := false
	for _, source := range sources {
		statuses = append(statuses, source.status)
		if !source.status.Complete {
			continue
		}
		if !source.status.Exhausted {
			sourceHasMore = true
		}
		messages = append(messages, source.messages...)
	}
	sortProjectedMail(messages)
	hasMore := sourceHasMore || len(messages) > input.Offset+input.Limit
	pageMessages := projectionMailWindow(messages, input.Offset, input.Limit)
	failures := projectionFailures(statuses)
	page := MailProjectionPage{
		Messages: pageMessages, Accounts: statuses, Failures: failures,
		Offset: input.Offset, Limit: input.Limit,
		HasMore: hasMore, Complete: len(failures) == 0,
	}
	if hasMore {
		page.NextOffset = input.Offset + len(pageMessages)
	}
	if err := page.Validate(); err != nil {
		return MailProjectionPage{}, fmt.Errorf(
			"validate mail projection: %w",
			err,
		)
	}
	return page, nil
}

func (service *ProjectionService) searchProjectionAccount(
	ctx context.Context,
	account ProjectionAccount,
	input MailProjectionInput,
	caller domain.Caller,
) mailProjectionSource {
	status := newProjectionStatus(account, projectionServiceMail)
	if !account.Authenticated {
		return mailProjectionSource{
			status: projectionUnavailableStatus(
				account,
				projectionServiceMail,
			),
		}
	}
	target := input.Offset + input.Limit + 1
	messages := make([]ProjectedMail, 0, target)
	seenObjects := make(map[string]struct{}, target)
	sourceOffset := 0
	for len(messages) < target {
		limit := min(MaxMailSearchPageSize, target-len(messages))
		page, err := service.reader.SearchMail(ctx, MailSearchInput{
			Account: account.Account, Folder: input.Folder, Query: input.Query,
			Offset: sourceOffset, Limit: limit, TimeZone: input.TimeZone,
		}, caller)
		if err != nil {
			status.FetchedItems = len(messages)
			return mailProjectionSource{status: failProjectionStatus(
				status,
				"provider_error",
				"the account search did not complete; inspect account status and retry",
			)}
		}
		if err := validateMailProjectionSourcePage(
			page,
			account,
			limit,
		); err != nil {
			status.FetchedItems = len(messages)
			return mailProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned an invalid bounded search page",
			)}
		}
		for _, message := range page.Messages {
			if _, exists := seenObjects[message.Provenance.SourceObjectID]; exists {
				status.FetchedItems = len(messages)
				return mailProjectionSource{status: failProjectionStatus(
					status,
					"invalid_result",
					"the account returned duplicate message identities across pages",
				)}
			}
			seenObjects[message.Provenance.SourceObjectID] = struct{}{}
			messages = append(messages, ProjectedMail{
				AccountAlias: account.Alias,
				Message:      message,
			})
		}
		sourceOffset += len(page.Messages)
		if page.IncludesLastItem {
			status.Exhausted = true
			break
		}
		if account.MailProvider == domain.ProviderGoogleWeb &&
			len(page.Messages) < limit {
			// Google Web deliberately exposes only one bounded visible DOM
			// snapshot. A short page exhausts that available snapshot even
			// though the account degradation continues to disclose that it is
			// not proof of the remote mailbox's terminal page.
			status.Exhausted = true
			break
		}
		if len(page.Messages) == 0 {
			status.FetchedItems = len(messages)
			return mailProjectionSource{status: failProjectionStatus(
				status,
				"invalid_result",
				"the account returned an empty non-terminal search page",
			)}
		}
	}
	status.Complete = true
	status.FetchedItems = len(messages)
	return mailProjectionSource{status: status, messages: messages}
}

func (input MailProjectionInput) Validate() error {
	if input.Folder.Kind != MailFolderDistinguished {
		return errors.New(
			"cross-account mail search requires one well-known folder",
		)
	}
	if err := validateMessageFolder(input.Folder); err != nil {
		return err
	}
	if input.Query == "" ||
		strings.TrimSpace(input.Query) != input.Query ||
		len(input.Query) > MaxMailSearchQueryBytes ||
		!utf8.ValidString(input.Query) ||
		strings.ContainsAny(input.Query, "\r\n\x00") {
		return fmt.Errorf(
			"mail search query must be valid UTF-8 without CR, LF, or NUL and at most %d bytes",
			MaxMailSearchQueryBytes,
		)
	}
	if input.Offset < 0 || input.Offset > MaxProjectionOffset {
		return fmt.Errorf(
			"mail projection offset must be between 0 and %d",
			MaxProjectionOffset,
		)
	}
	if input.Limit < 1 || input.Limit > MaxMailProjectionPageSize {
		return fmt.Errorf(
			"mail projection limit must be between 1 and %d",
			MaxMailProjectionPageSize,
		)
	}
	if input.TimeZone != "UTC" {
		return errors.New(
			"cross-account mail search uses UTC to avoid provider-specific time-zone semantics",
		)
	}
	return nil
}

func validateMailProjectionSourcePage(
	page MailPage,
	account ProjectionAccount,
	limit int,
) error {
	if len(page.Messages) > limit ||
		page.TotalItemsInView < 0 {
		return errors.New("mail projection source page is unbounded")
	}
	for _, message := range page.Messages {
		if message.ID == "" || len(message.ID) > 4096 ||
			strings.ContainsAny(message.ID, "\r\n\x00") {
			return errors.New("mail projection source identity is invalid")
		}
		if message.ReceivedAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, message.ReceivedAt); err != nil {
				return errors.New("mail projection source date is invalid")
			}
		}
		if err := message.Provenance.Validate(); err != nil {
			return err
		}
		if message.Provenance.AccountID != account.Account ||
			message.Provenance.Provider != account.MailProvider ||
			message.Provenance.MailboxID == "" ||
			message.Provenance.CalendarID != "" ||
			message.Provenance.SourceObjectID != message.ID {
			return errors.New("mail projection source provenance is invalid")
		}
	}
	return nil
}

func sortProjectedMail(messages []ProjectedMail) {
	slices.SortStableFunc(messages, compareProjectedMail)
}

func compareProjectedMail(left, right ProjectedMail) int {
	leftTime := projectionMailTime(left.Message.ReceivedAt)
	rightTime := projectionMailTime(right.Message.ReceivedAt)
	if compared := rightTime.Compare(leftTime); compared != 0 {
		return compared
	}
	if compared := strings.Compare(
		left.AccountAlias,
		right.AccountAlias,
	); compared != 0 {
		return compared
	}
	if compared := strings.Compare(
		string(left.Message.Provenance.Provider),
		string(right.Message.Provenance.Provider),
	); compared != 0 {
		return compared
	}
	return strings.Compare(
		left.Message.Provenance.SourceObjectID,
		right.Message.Provenance.SourceObjectID,
	)
}

func projectionMailTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func projectionMailWindow(
	messages []ProjectedMail,
	offset, limit int,
) []ProjectedMail {
	if offset >= len(messages) {
		return []ProjectedMail{}
	}
	end := min(len(messages), offset+limit)
	return append([]ProjectedMail(nil), messages[offset:end]...)
}

func (page MailProjectionPage) Validate() error {
	if page.Offset < 0 || page.Offset > MaxProjectionOffset ||
		page.Limit < 1 || page.Limit > MaxMailProjectionPageSize ||
		len(page.Messages) > page.Limit {
		return errors.New("mail projection page bounds are invalid")
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
	for _, message := range page.Messages {
		status, exists := accounts[message.Message.Provenance.AccountID]
		if !exists || !status.Complete ||
			status.Alias != message.AccountAlias ||
			status.Provider != message.Message.Provenance.Provider {
			return errors.New("projected mail has inconsistent account provenance")
		}
		if err := validateMailProjectionSourcePage(
			MailPage{
				Messages:         []MailSummary{message.Message},
				TotalItemsInView: 1,
				IncludesLastItem: true,
			},
			ProjectionAccount{
				Account:      status.Account,
				Alias:        status.Alias,
				MailProvider: status.Provider,
			},
			1,
		); err != nil {
			return err
		}
	}
	if !slices.IsSortedFunc(page.Messages, compareProjectedMail) {
		return errors.New("projected mail is not stably ordered")
	}
	if page.HasMore {
		if page.NextOffset != page.Offset+len(page.Messages) {
			return errors.New("mail projection next offset is invalid")
		}
	} else if page.NextOffset != 0 {
		return errors.New("terminal mail projection has a next offset")
	}
	return nil
}
