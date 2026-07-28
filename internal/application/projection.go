package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxProjectionAccounts = 32
	MaxProjectionOffset   = 400
)

const (
	projectionServiceMail     = "mail"
	projectionServiceCalendar = "calendar"
)

// ProjectionAccount is content-free routing and capability metadata for one
// isolated account. It contains no credentials, profile, cursor, or rate-limit
// state.
type ProjectionAccount struct {
	Account              domain.AccountID     `json:"account"`
	Alias                string               `json:"alias"`
	MailProvider         domain.ProviderID    `json:"mailProvider,omitempty"`
	CalendarProvider     domain.ProviderID    `json:"calendarProvider,omitempty"`
	Authenticated        bool                 `json:"authenticated"`
	Capabilities         *domain.Capabilities `json:"capabilities,omitempty"`
	MailDegradations     []domain.Degradation `json:"mailDegradations,omitempty"`
	CalendarDegradations []domain.Degradation `json:"calendarDegradations,omitempty"`
}

// ProjectionFailure is an explicit, content-free per-account failure. Raw
// provider errors are deliberately absent from the stable projection schema.
type ProjectionFailure struct {
	Account  domain.AccountID  `json:"account"`
	Alias    string            `json:"alias"`
	Provider domain.ProviderID `json:"provider"`
	Service  string            `json:"service"`
	Code     string            `json:"code"`
	Reason   string            `json:"reason"`
}

// ProjectionAccountStatus reports whether one isolated account contributed a
// complete source page and preserves its provider-specific behavior.
type ProjectionAccountStatus struct {
	Account      domain.AccountID     `json:"account"`
	Alias        string               `json:"alias"`
	Provider     domain.ProviderID    `json:"provider"`
	Service      string               `json:"service"`
	Complete     bool                 `json:"complete"`
	FetchedItems int                  `json:"fetchedItems"`
	Exhausted    bool                 `json:"exhausted"`
	Capabilities *domain.Capabilities `json:"capabilities,omitempty"`
	Degradations []domain.Degradation `json:"degradations,omitempty"`
	Failure      *ProjectionFailure   `json:"failure,omitempty"`
}

// ProjectionReader composes the existing typed, guarded per-account read use
// cases. The projection service never receives adapters or authentication
// material directly.
type ProjectionReader interface {
	ProjectionAccounts(context.Context) ([]ProjectionAccount, error)
	SearchMail(
		context.Context,
		MailSearchInput,
		domain.Caller,
	) (MailPage, error)
	ListCalendar(
		context.Context,
		CalendarListInput,
		domain.Caller,
	) (CalendarPage, error)
}

// ProjectionService provides read-only views without constructing merged
// writable storage.
type ProjectionService struct {
	reader ProjectionReader
}

func NewProjectionService(reader ProjectionReader) (*ProjectionService, error) {
	if reader == nil {
		return nil, errors.New("projection reader is required")
	}
	return &ProjectionService{reader: reader}, nil
}

func (service *ProjectionService) accounts(
	ctx context.Context,
) ([]ProjectionAccount, error) {
	accounts, err := service.reader.ProjectionAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 || len(accounts) > MaxProjectionAccounts {
		return nil, fmt.Errorf(
			"projection account count must be between 1 and %d",
			MaxProjectionAccounts,
		)
	}
	slices.SortFunc(accounts, func(left, right ProjectionAccount) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	seenAccounts := make(map[domain.AccountID]struct{}, len(accounts))
	seenAliases := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if err := account.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seenAccounts[account.Account]; exists {
			return nil, errors.New("projection contains a duplicate account")
		}
		if _, exists := seenAliases[account.Alias]; exists {
			return nil, errors.New("projection contains a duplicate account alias")
		}
		seenAccounts[account.Account] = struct{}{}
		seenAliases[account.Alias] = struct{}{}
	}
	return accounts, nil
}

func (account ProjectionAccount) Validate() error {
	if err := account.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(account.Alias).Validate(); err != nil {
		return err
	}
	if account.MailProvider == "" && account.CalendarProvider == "" {
		return errors.New("projection account has no service route")
	}
	for _, provider := range []domain.ProviderID{
		account.MailProvider,
		account.CalendarProvider,
	} {
		if provider != "" {
			if err := provider.Validate(); err != nil {
				return err
			}
		}
	}
	if account.Authenticated {
		if account.Capabilities == nil {
			return errors.New(
				"authenticated projection account has no capability snapshot",
			)
		}
		if err := account.Capabilities.Validate(); err != nil {
			return err
		}
		if (account.MailProvider != "" && !account.Capabilities.Mail) ||
			(account.CalendarProvider != "" && !account.Capabilities.Calendar) {
			return errors.New(
				"projection account capability snapshot does not match its routes",
			)
		}
	} else if account.Capabilities != nil ||
		len(account.MailDegradations) != 0 ||
		len(account.CalendarDegradations) != 0 {
		return errors.New(
			"inactive projection account exposes runtime capability state",
		)
	}
	if len(account.MailDegradations) > 32 ||
		len(account.CalendarDegradations) > 32 {
		return errors.New("projection account has unbounded degradations")
	}
	for _, values := range [][]domain.Degradation{
		account.MailDegradations,
		account.CalendarDegradations,
	} {
		for _, degradation := range values {
			if err := degradation.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func newProjectionStatus(
	account ProjectionAccount,
	service string,
) ProjectionAccountStatus {
	provider := account.MailProvider
	degradations := account.MailDegradations
	if service == projectionServiceCalendar {
		provider = account.CalendarProvider
		degradations = account.CalendarDegradations
	}
	var capabilities *domain.Capabilities
	if account.Capabilities != nil {
		copy := *account.Capabilities
		capabilities = &copy
	}
	return ProjectionAccountStatus{
		Account: account.Account, Alias: account.Alias,
		Provider: provider, Service: service,
		Capabilities: capabilities,
		Degradations: append([]domain.Degradation(nil), degradations...),
	}
}

func failProjectionStatus(
	status ProjectionAccountStatus,
	code, reason string,
) ProjectionAccountStatus {
	status.Complete = false
	status.Exhausted = false
	status.Failure = &ProjectionFailure{
		Account: status.Account, Alias: status.Alias,
		Provider: status.Provider, Service: status.Service,
		Code: code, Reason: reason,
	}
	return status
}

func projectionUnavailableStatus(
	account ProjectionAccount,
	service string,
) ProjectionAccountStatus {
	return failProjectionStatus(
		newProjectionStatus(account, service),
		"not_authenticated",
		"the account is not authenticated; sign in explicitly and rerun the projection",
	)
}

func validateProjectionStatus(status ProjectionAccountStatus) error {
	if err := status.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(status.Alias).Validate(); err != nil {
		return err
	}
	if err := status.Provider.Validate(); err != nil {
		return err
	}
	if status.Service != projectionServiceMail &&
		status.Service != projectionServiceCalendar {
		return errors.New("projection account service is invalid")
	}
	if status.FetchedItems < 0 || status.FetchedItems > 5000 {
		return errors.New("projection fetched item count is invalid")
	}
	if len(status.Degradations) > 32 {
		return errors.New("projection account degradations are unbounded")
	}
	for _, degradation := range status.Degradations {
		if err := degradation.Validate(); err != nil {
			return err
		}
	}
	if status.Capabilities != nil {
		if err := status.Capabilities.Validate(); err != nil {
			return err
		}
	}
	if status.Complete {
		if status.Failure != nil || status.Capabilities == nil {
			return errors.New(
				"complete projection account lacks valid runtime status",
			)
		}
		return nil
	}
	if status.Failure == nil {
		return errors.New("incomplete projection account has no failure")
	}
	return validateProjectionFailure(*status.Failure, status)
}

func validateProjectionFailure(
	failure ProjectionFailure,
	status ProjectionAccountStatus,
) error {
	if failure.Account != status.Account ||
		failure.Alias != status.Alias ||
		failure.Provider != status.Provider ||
		failure.Service != status.Service {
		return errors.New("projection failure does not match its account")
	}
	switch failure.Code {
	case "not_authenticated", "provider_error", "invalid_result":
	default:
		return errors.New("projection failure code is invalid")
	}
	if failure.Reason == "" || len(failure.Reason) > 512 ||
		strings.TrimSpace(failure.Reason) != failure.Reason ||
		strings.ContainsAny(failure.Reason, "\r\n\x00") {
		return errors.New("projection failure reason is malformed")
	}
	return nil
}

func projectionFailures(
	statuses []ProjectionAccountStatus,
) []ProjectionFailure {
	failures := make([]ProjectionFailure, 0, len(statuses))
	for _, status := range statuses {
		if status.Failure != nil {
			failures = append(failures, *status.Failure)
		}
	}
	return failures
}

func validateProjectionEnvelope(
	statuses []ProjectionAccountStatus,
	failures []ProjectionFailure,
	complete bool,
) error {
	if len(statuses) == 0 || len(statuses) > MaxProjectionAccounts {
		return errors.New("projection account statuses are unbounded")
	}
	failureCount := 0
	for index, status := range statuses {
		if err := validateProjectionStatus(status); err != nil {
			return err
		}
		if index > 0 && statuses[index-1].Alias >= status.Alias {
			return errors.New("projection account statuses are not stably ordered")
		}
		if status.Failure != nil {
			failureCount++
		}
	}
	if failureCount != len(failures) ||
		complete != (failureCount == 0) {
		return errors.New("projection completeness is inconsistent")
	}
	expectedFailures := projectionFailures(statuses)
	if !slices.Equal(failures, expectedFailures) {
		return errors.New("projection failures are inconsistent")
	}
	return nil
}
