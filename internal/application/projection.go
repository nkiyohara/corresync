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
	projectionServiceTasks    = "tasks"
)

// ProjectionAccount is content-free routing and capability metadata for one
// isolated account. It contains no credentials, profile, cursor, or rate-limit
// state.
type ProjectionAccount struct {
	Account              domain.AccountID              `json:"account"`
	Alias                string                        `json:"alias"`
	MailProvider         domain.ProviderID             `json:"mailProvider,omitempty"`
	CalendarProvider     domain.ProviderID             `json:"calendarProvider,omitempty"`
	TaskProvider         domain.ProviderID             `json:"taskProvider,omitempty"`
	Authenticated        bool                          `json:"authenticated"`
	Services             ServiceAuthenticationStatuses `json:"services"`
	Capabilities         *domain.Capabilities          `json:"capabilities,omitempty"`
	MailDegradations     []domain.Degradation          `json:"mailDegradations,omitempty"`
	CalendarDegradations []domain.Degradation          `json:"calendarDegradations,omitempty"`
	TaskDegradations     []domain.Degradation          `json:"taskDegradations,omitempty"`
}

// ProjectionFailure is an explicit, content-free per-account failure. Raw
// provider errors are deliberately absent from the stable projection schema.
type ProjectionFailure struct {
	Account        domain.AccountID              `json:"account"`
	Alias          string                        `json:"alias"`
	Provider       domain.ProviderID             `json:"provider"`
	Service        string                        `json:"service"`
	Code           string                        `json:"code"`
	Reason         string                        `json:"reason"`
	Authentication *AuthenticationActionRequired `json:"authentication,omitempty"`
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
	ListTasks(context.Context, TaskReadInput, domain.Caller) (TaskPage, error)
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
	if account.MailProvider == "" && account.CalendarProvider == "" && account.TaskProvider == "" {
		return errors.New("projection account has no service route")
	}
	for _, provider := range []domain.ProviderID{
		account.MailProvider,
		account.CalendarProvider,
		account.TaskProvider,
	} {
		if provider != "" {
			if err := provider.Validate(); err != nil {
				return err
			}
		}
	}
	serviceStatuses := account.Services.Values()
	if len(serviceStatuses) == 0 {
		return errors.New("projection account has no service authentication status")
	}
	if (account.MailProvider != "") != (account.Services.Mail != nil) ||
		(account.CalendarProvider != "") != (account.Services.Calendar != nil) ||
		(account.TaskProvider != "") != (account.Services.Tasks != nil) {
		return errors.New("projection account service authentication statuses do not match its routes")
	}
	authenticatedServices := 0
	for _, status := range serviceStatuses {
		if err := status.Validate(account.Account, account.Alias); err != nil {
			return err
		}
		if configuredServiceProviderForProjection(account, status.Service) !=
			status.Provider {
			return errors.New(
				"projection authentication status provider does not match its route",
			)
		}
		if status.State == AuthenticationStateAuthenticated {
			authenticatedServices++
		}
	}
	if account.Authenticated != (authenticatedServices > 0) {
		return errors.New(
			"projection account compatibility authentication state is inconsistent",
		)
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
		if account.ServiceAuthenticated(projectionServiceMail) != account.Capabilities.Mail ||
			account.ServiceAuthenticated(projectionServiceCalendar) != account.Capabilities.Calendar ||
			account.ServiceAuthenticated(projectionServiceTasks) != account.Capabilities.Tasks {
			return errors.New(
				"projection account capability snapshot does not match its routes",
			)
		}
	} else if account.Capabilities != nil ||
		len(account.MailDegradations) != 0 ||
		len(account.CalendarDegradations) != 0 ||
		len(account.TaskDegradations) != 0 {
		return errors.New(
			"inactive projection account exposes runtime capability state",
		)
	}
	if len(account.MailDegradations) > 32 ||
		len(account.CalendarDegradations) > 32 ||
		len(account.TaskDegradations) > 32 {
		return errors.New("projection account has unbounded degradations")
	}
	for _, values := range [][]domain.Degradation{
		account.MailDegradations,
		account.CalendarDegradations,
		account.TaskDegradations,
	} {
		for _, degradation := range values {
			if err := degradation.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func configuredServiceProviderForProjection(
	account ProjectionAccount,
	service AuthenticationService,
) domain.ProviderID {
	switch service {
	case AuthenticationServiceMail:
		return account.MailProvider
	case AuthenticationServiceCalendar:
		return account.CalendarProvider
	case AuthenticationServiceTasks:
		return account.TaskProvider
	default:
		return ""
	}
}

func (account ProjectionAccount) ServiceAuthenticated(service string) bool {
	var status *ServiceAuthenticationStatus
	switch service {
	case projectionServiceMail:
		status = account.Services.Mail
	case projectionServiceCalendar:
		status = account.Services.Calendar
	case projectionServiceTasks:
		status = account.Services.Tasks
	}
	return status != nil && status.State == AuthenticationStateAuthenticated
}

func newProjectionStatus(
	account ProjectionAccount,
	service string,
) ProjectionAccountStatus {
	provider := account.MailProvider
	degradations := account.MailDegradations
	switch service {
	case projectionServiceCalendar:
		provider = account.CalendarProvider
		degradations = account.CalendarDegradations
	case projectionServiceTasks:
		provider = account.TaskProvider
		degradations = account.TaskDegradations
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
	authentication := accountAuthenticationStatus(account, service)
	if authentication != nil && authentication.Action != nil {
		action := *authentication.Action
		return failProjectionAuthenticationStatus(
			newProjectionStatus(account, service),
			action,
		)
	}
	return failProjectionStatus(
		newProjectionStatus(account, service),
		"not_authenticated",
		"the account is not authenticated; sign in explicitly and rerun the projection",
	)
}

func accountAuthenticationStatus(
	account ProjectionAccount,
	service string,
) *ServiceAuthenticationStatus {
	switch service {
	case projectionServiceMail:
		return account.Services.Mail
	case projectionServiceCalendar:
		return account.Services.Calendar
	case projectionServiceTasks:
		return account.Services.Tasks
	default:
		return nil
	}
}

func failProjectionAuthenticationStatus(
	status ProjectionAccountStatus,
	action AuthenticationActionRequired,
) ProjectionAccountStatus {
	status.Complete = false
	status.Exhausted = false
	status.Failure = &ProjectionFailure{
		Account:        status.Account,
		Alias:          status.Alias,
		Provider:       status.Provider,
		Service:        status.Service,
		Code:           string(action.Code),
		Reason:         string(action.Reason),
		Authentication: &action,
	}
	return status
}

func failProjectionCallStatus(
	status ProjectionAccountStatus,
	callErr error,
	reason string,
) ProjectionAccountStatus {
	if action, ok := AuthenticationActionFromError(callErr); ok {
		return failProjectionAuthenticationStatus(status, action)
	}
	return failProjectionStatus(status, "provider_error", reason)
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
		status.Service != projectionServiceCalendar &&
		status.Service != projectionServiceTasks {
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
		if failure.Authentication != nil {
			return errors.New("non-authentication projection failure includes an authentication action")
		}
	case string(AuthenticationCodeRequired),
		string(AuthenticationCodePending),
		string(AuthenticationCodeReauthenticationNeed):
		if failure.Authentication == nil {
			return errors.New("authentication projection failure omits its action")
		}
		if err := failure.Authentication.Validate(); err != nil {
			return err
		}
		if failure.Authentication.Account != failure.Account ||
			failure.Authentication.Alias != failure.Alias ||
			failure.Authentication.Provider != failure.Provider ||
			string(failure.Authentication.Service) != failure.Service ||
			string(failure.Authentication.Code) != failure.Code ||
			string(failure.Authentication.Reason) != failure.Reason {
			return errors.New("projection authentication action does not match its failure")
		}
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
