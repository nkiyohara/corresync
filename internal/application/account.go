package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
)

// AccountCredentialView is the only credential information exposed by account
// reads. Lookup keys, helper arguments, and credential values are omitted.
type AccountCredentialView struct {
	Configured bool   `json:"configured"`
	Backend    string `json:"backend,omitempty"`
	Consented  bool   `json:"consented"`
}

// AccountRouteView is a provider-neutral, secret-free service route summary.
type AccountRouteView struct {
	Provider   domain.ProviderID      `json:"provider"`
	Available  bool                   `json:"available"`
	Endpoints  []DiscoveredEndpoint   `json:"endpoints"`
	Identity   string                 `json:"identity,omitempty"`
	Credential *AccountCredentialView `json:"credential,omitempty"`
}

// AccountView is the secret-free account lifecycle contract shared by CLI and
// MCP. Mail, calendar, and task routes may use different providers.
type AccountView struct {
	ID        domain.AccountID  `json:"id"`
	Alias     string            `json:"alias"`
	Address   string            `json:"address,omitempty"`
	Mail      *AccountRouteView `json:"mail,omitempty"`
	Calendar  *AccountRouteView `json:"calendar,omitempty"`
	Tasks     *AccountRouteView `json:"tasks,omitempty"`
	IsDefault bool              `json:"isDefault"`
}

// AccountCatalog is a deterministic snapshot of configured accounts.
type AccountCatalog struct {
	Accounts []AccountView `json:"accounts"`
}

// AccountCredentialInput is an external lookup selected with explicit consent.
// It is accepted only as write input and never appears in AccountView.
type AccountCredentialInput struct {
	Backend string `json:"backend"`
	Key     string `json:"key"`
	Consent bool   `json:"consent"`
}

// AccountCredentialReview discloses the exact external lookup handle bound by
// an account-add approval. It appears only in the write review, never in
// AccountView or other account reads.
type AccountCredentialReview struct {
	Service  string            `json:"service"`
	Provider domain.ProviderID `json:"provider"`
	Backend  string            `json:"backend"`
	Key      string            `json:"key"`
}

// AccountCredentialBinding is the private ownership projection used to reject
// cross-account handle reuse before a route can be approved or persisted.
type AccountCredentialBinding struct {
	Account domain.AccountID
	Backend string
	Key     string
}

// AccountOutlookWebInput configures a first-party browser route.
type AccountOutlookWebInput struct {
	Origin  string `json:"origin"`
	Mailbox string `json:"mailbox,omitempty"`
}

// AccountJMAPInput configures one RFC 8620 session resource.
type AccountJMAPInput struct {
	SessionURL string                 `json:"sessionUrl"`
	Username   string                 `json:"username"`
	Credential AccountCredentialInput `json:"credential"`
}

// AccountTLSEndpointInput is one encrypted standards-protocol endpoint.
type AccountTLSEndpointInput struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
	Mode string `json:"mode"`
}

// AccountIMAPSMTPInput configures separate receive and submission transports.
type AccountIMAPSMTPInput struct {
	IMAP       AccountTLSEndpointInput `json:"imap"`
	SMTP       AccountTLSEndpointInput `json:"smtp"`
	Username   string                  `json:"username"`
	Mailbox    string                  `json:"mailbox,omitempty"`
	Credential AccountCredentialInput  `json:"credential"`
}

// AccountGoogleMailInput configures Gmail's fixed API route.
// It contains only public-client metadata and an OS-keyring grant handle.
type AccountGoogleMailInput struct {
	Username      string                 `json:"username"`
	Mailbox       string                 `json:"mailbox,omitempty"`
	ClientID      string                 `json:"clientId"`
	RedirectURI   string                 `json:"redirectUri"`
	Authorization AccountCredentialInput `json:"authorization"`
}

// AccountCalDAVInput configures authenticated principal discovery for one
// calendar. CalendarPath may remain empty to select the first VEVENT calendar.
type AccountCalDAVInput struct {
	Endpoint     string                 `json:"endpoint"`
	CalendarPath string                 `json:"calendarPath,omitempty"`
	Username     string                 `json:"username"`
	Credential   AccountCredentialInput `json:"credential"`
}

// AccountCalDAVTaskInput configures authenticated VTODO discovery for one
// task route. TaskListPath may remain empty to select the first VTODO list.
type AccountCalDAVTaskInput struct {
	Endpoint     string                 `json:"endpoint"`
	TaskListPath string                 `json:"taskListPath,omitempty"`
	Username     string                 `json:"username"`
	Credential   AccountCredentialInput `json:"credential"`
}

// AccountOAuthInput configures one BYO public client and an OS-keyring grant
// handle. It cannot represent a client secret or token.
type AccountOAuthInput struct {
	APIBase        string                 `json:"apiBase"`
	MicrosoftCloud microsoftcloud.ID      `json:"microsoftCloud,omitempty"`
	ClientID       string                 `json:"clientId"`
	RedirectURI    string                 `json:"redirectUri"`
	Authorization  AccountCredentialInput `json:"authorization"`
}

// AccountMicrosoftTaskInput selects an independent delegated Graph grant for
// Microsoft To Do. ReadOnly chooses Tasks.Read; otherwise Tasks.ReadWrite is
// requested. The grant is never inferred from a mail or calendar route.
type AccountMicrosoftTaskInput struct {
	OAuth    AccountOAuthInput `json:"oauth"`
	ReadOnly bool              `json:"readOnly,omitempty"`
}

// AccountTodoistTaskInput selects a task-only Todoist public-client grant.
// ReadOnly requests data:read; writable routes request data:read_write and the
// separately required data:delete scope.
type AccountTodoistTaskInput struct {
	OAuth    AccountOAuthInput `json:"oauth"`
	ReadOnly bool              `json:"readOnly,omitempty"`
}

// AccountWebInput identifies a provider-owned interactive browser origin.
type AccountWebInput struct {
	Origin string `json:"origin"`
}

// AccountMailRouteInput is a closed selection for shipped mail adapters.
type AccountMailRouteInput struct {
	Provider       domain.ProviderID       `json:"provider"`
	OutlookWeb     *AccountOutlookWebInput `json:"outlookWeb,omitempty"`
	JMAP           *AccountJMAPInput       `json:"jmap,omitempty"`
	IMAPSMTP       *AccountIMAPSMTPInput   `json:"imapSmtp,omitempty"`
	Google         *AccountGoogleMailInput `json:"google,omitempty"`
	GoogleWeb      *AccountWebInput        `json:"googleWeb,omitempty"`
	MicrosoftGraph *AccountOAuthInput      `json:"microsoftGraph,omitempty"`
}

// AccountCalendarRouteInput is the corresponding calendar selection.
type AccountCalendarRouteInput struct {
	Provider       domain.ProviderID       `json:"provider"`
	OutlookWeb     *AccountOutlookWebInput `json:"outlookWeb,omitempty"`
	CalDAV         *AccountCalDAVInput     `json:"caldav,omitempty"`
	Google         *AccountOAuthInput      `json:"google,omitempty"`
	GoogleWeb      *AccountWebInput        `json:"googleWeb,omitempty"`
	MicrosoftGraph *AccountOAuthInput      `json:"microsoftGraph,omitempty"`
}

// AccountTaskRouteInput is the closed task-provider selection. Adapter issues
// may add typed, secret-free payloads; an arbitrary options map is forbidden.
type AccountTaskRouteInput struct {
	Provider       domain.ProviderID          `json:"provider"`
	MicrosoftGraph *AccountMicrosoftTaskInput `json:"microsoftGraph,omitempty"`
	Todoist        *AccountTodoistTaskInput   `json:"todoist,omitempty"`
	CalDAV         *AccountCalDAVTaskInput    `json:"caldav,omitempty"`
}

// AccountAddInput explicitly selects service routes to persist. Discovery
// never writes configuration or starts authentication.
type AccountAddInput struct {
	Alias    string                     `json:"alias"`
	Address  string                     `json:"address,omitempty"`
	Mail     *AccountMailRouteInput     `json:"mail,omitempty"`
	Calendar *AccountCalendarRouteInput `json:"calendar,omitempty"`
	Tasks    *AccountTaskRouteInput     `json:"tasks,omitempty"`
	Default  bool                       `json:"default"`
}

// AccountRegistration is the validated write contract passed only to the local
// configuration repository. It is never serialized as an account read result.
type AccountRegistration struct {
	ID        domain.AccountID           `json:"-"`
	Alias     string                     `json:"-"`
	Address   string                     `json:"-"`
	Mail      *AccountMailRouteInput     `json:"-"`
	Calendar  *AccountCalendarRouteInput `json:"-"`
	Tasks     *AccountTaskRouteInput     `json:"-"`
	IsDefault bool                       `json:"-"`
}

// AccountRenameInput changes only the human-facing alias.
type AccountRenameInput struct {
	Account  string `json:"account"`
	NewAlias string `json:"newAlias"`
}

// AccountRemoveInput selects a replacement default when necessary.
type AccountRemoveInput struct {
	Account            string `json:"account"`
	ReplacementDefault string `json:"replacementDefault,omitempty"`
}

// AccountChangeReview is the bounded, secret-free account lifecycle summary
// returned before an MCP configuration mutation.
type AccountChangeReview struct {
	Action              string                    `json:"action"`
	Account             domain.AccountID          `json:"account,omitempty"`
	Alias               string                    `json:"alias"`
	NewAlias            string                    `json:"newAlias,omitempty"`
	Address             string                    `json:"address,omitempty"`
	MailProvider        domain.ProviderID         `json:"mailProvider,omitempty"`
	CalendarProvider    domain.ProviderID         `json:"calendarProvider,omitempty"`
	TaskProvider        domain.ProviderID         `json:"taskProvider,omitempty"`
	Mail                *AccountRouteView         `json:"mail,omitempty"`
	Calendar            *AccountRouteView         `json:"calendar,omitempty"`
	Tasks               *AccountRouteView         `json:"tasks,omitempty"`
	Credentials         []AccountCredentialReview `json:"credentials,omitempty"`
	MakesDefault        bool                      `json:"makesDefault"`
	ReplacementDefault  string                    `json:"replacementDefault,omitempty"`
	ReplacementAccount  domain.AccountID          `json:"replacementAccount,omitempty"`
	PurgesLocalState    bool                      `json:"purgesLocalState"`
	MayDeleteOwnedOAuth bool                      `json:"mayDeleteOwnedOAuth"`
	Authentication      string                    `json:"authentication,omitempty"`
	RestartsSessions    bool                      `json:"restartsSessions"`
}

// AccountChangeAccess is either an approval-bound preview or a completed
// secret-free account view.
type AccountChangeAccess struct {
	Status  string               `json:"status"`
	Review  *AccountChangeReview `json:"review,omitempty"`
	Preview *approval.Preview    `json:"preview,omitempty"`
	Account *AccountView         `json:"account,omitempty"`
}

// AccountRepository persists secret-free account definitions. Implementations
// must perform each mutation atomically against the latest complete config.
type AccountRepository interface {
	ListAccounts(context.Context) (AccountCatalog, error)
	ListCredentialBindings(context.Context) ([]AccountCredentialBinding, error)
	AddAccount(context.Context, AccountRegistration) error
	RenameAccount(context.Context, domain.AccountID, string) error
	RemoveAccount(context.Context, domain.AccountID, domain.AccountID) error
}

// AccountStatePurger removes Corresync-owned per-account state. It never reads
// or removes credentials owned by another application.
type AccountStatePurger interface {
	PurgeAccountState(context.Context, domain.AccountID) error
}

type accountIDGenerator func() (domain.AccountID, error)

// AccountService owns account lifecycle validation and isolation semantics.
type AccountService struct {
	repository    AccountRepository
	purger        AccountStatePurger
	available     map[domain.ProviderID]struct{}
	taskAvailable map[domain.ProviderID]struct{}
	newID         accountIDGenerator
}

// NewAccountService creates the shared lifecycle use case.
func NewAccountService(
	repository AccountRepository,
	purger AccountStatePurger,
	available []domain.ProviderID,
	taskAvailable []domain.ProviderID,
) (*AccountService, error) {
	if repository == nil {
		return nil, errors.New("account repository is required")
	}
	if purger == nil {
		return nil, errors.New("account state purger is required")
	}
	providers, err := providerSet(available)
	if err != nil {
		return nil, err
	}
	taskProviders, err := providerSet(taskAvailable)
	if err != nil {
		return nil, err
	}
	return &AccountService{
		repository:    repository,
		purger:        purger,
		available:     providers,
		taskAvailable: taskProviders,
		newID:         domain.NewAccountID,
	}, nil
}

func providerSet(available []domain.ProviderID) (map[domain.ProviderID]struct{}, error) {
	providers := make(map[domain.ProviderID]struct{}, len(available))
	for _, provider := range available {
		if err := provider.Validate(); err != nil {
			return nil, err
		}
		if _, exists := providers[provider]; exists {
			return nil, fmt.Errorf("provider %q is duplicated in route availability", provider)
		}
		providers[provider] = struct{}{}
	}
	return providers, nil
}

// List returns a stable alias-ordered account snapshot.
func (service *AccountService) List(ctx context.Context) (AccountCatalog, error) {
	catalog, err := service.repository.ListAccounts(ctx)
	if err != nil {
		return AccountCatalog{}, fmt.Errorf("list accounts: %w", err)
	}
	if err := validateAccountCatalog(catalog); err != nil {
		return AccountCatalog{}, fmt.Errorf("validate account repository: %w", err)
	}
	for index := range catalog.Accounts {
		service.markRouteAvailability(&catalog.Accounts[index])
	}
	slices.SortFunc(catalog.Accounts, func(left, right AccountView) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	return catalog, nil
}

// Show resolves either a mutable alias or stable opaque ID.
func (service *AccountService) Show(
	ctx context.Context,
	reference string,
) (AccountView, error) {
	if reference == "" {
		return AccountView{}, errors.New("account reference is required")
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, account := range catalog.Accounts {
		if account.Alias == reference || string(account.ID) == reference {
			return account, nil
		}
	}
	return AccountView{}, fmt.Errorf("account %q is not configured", reference)
}

// Add persists one explicit provider selection without authenticating.
func (service *AccountService) Add(
	ctx context.Context,
	input AccountAddInput,
) (AccountView, error) {
	normalizedAddress, catalog, err := service.reviewAdd(ctx, input)
	if err != nil {
		return AccountView{}, err
	}
	accountID, err := service.newID()
	if err != nil {
		return AccountView{}, fmt.Errorf("generate account ID: %w", err)
	}
	registration := AccountRegistration{
		ID: accountID, Alias: input.Alias, Address: normalizedAddress,
		Mail: cloneMailRoute(input.Mail), Calendar: cloneCalendarRoute(input.Calendar),
		Tasks:     cloneTaskRoute(input.Tasks),
		IsDefault: input.Default || len(catalog.Accounts) == 0,
	}
	account := registration.view()
	service.markRouteAvailability(&account)
	if err := validateAccountView(account); err != nil {
		return AccountView{}, err
	}
	if err := service.repository.AddAccount(ctx, registration); err != nil {
		return AccountView{}, fmt.Errorf("add account: %w", err)
	}
	return account, nil
}

// ReviewAdd validates one exact account addition without generating an ID,
// authenticating, resolving a credential, or changing configuration.
func (service *AccountService) ReviewAdd(
	ctx context.Context,
	input AccountAddInput,
) (AccountChangeReview, error) {
	address, catalog, err := service.reviewAdd(ctx, input)
	if err != nil {
		return AccountChangeReview{}, err
	}
	review := AccountChangeReview{
		Action: "add", Alias: input.Alias, Address: address,
		Mail: mailRouteView(input.Mail), Calendar: calendarRouteView(input.Calendar),
		Tasks:          taskRouteInputView(input.Tasks),
		Credentials:    accountCredentialReviews(input),
		MakesDefault:   input.Default || len(catalog.Accounts) == 0,
		Authentication: "explicit_cli_required",
	}
	if input.Mail != nil {
		review.MailProvider = input.Mail.Provider
	}
	if input.Calendar != nil {
		review.CalendarProvider = input.Calendar.Provider
	}
	if input.Tasks != nil {
		review.TaskProvider = input.Tasks.Provider
	}
	return review, nil
}

func (service *AccountService) reviewAdd(
	ctx context.Context,
	input AccountAddInput,
) (string, AccountCatalog, error) {
	if err := domain.AccountAlias(input.Alias).Validate(); err != nil {
		return "", AccountCatalog{}, err
	}
	if input.Mail == nil && input.Calendar == nil && input.Tasks == nil {
		return "", AccountCatalog{}, errors.New(
			"at least one mail, calendar, or task route is required",
		)
	}
	normalizedAddress := ""
	if input.Address != "" {
		var err error
		normalizedAddress, _, err = normalizeDiscoveryAddress(input.Address)
		if err != nil {
			return "", AccountCatalog{}, err
		}
	} else if input.Mail != nil || input.Calendar != nil {
		return "", AccountCatalog{}, errors.New(
			"mail and calendar routes require an account address",
		)
	}
	if input.Mail != nil {
		if err := service.validateMailRoute(*input.Mail); err != nil {
			return "", AccountCatalog{}, fmt.Errorf("mail route: %w", err)
		}
		if input.Mail.Provider == domain.ProviderGoogle &&
			input.Mail.Google != nil &&
			!strings.EqualFold(
				input.Mail.Google.Username,
				normalizedAddress,
			) {
			return "", AccountCatalog{}, errors.New(
				"google mail username must match the account email address",
			)
		}
	}
	if input.Calendar != nil {
		if err := service.validateCalendarRoute(*input.Calendar); err != nil {
			return "", AccountCatalog{}, fmt.Errorf("calendar route: %w", err)
		}
	}
	if input.Tasks != nil {
		if err := service.validateTaskRoute(*input.Tasks); err != nil {
			return "", AccountCatalog{}, fmt.Errorf("task route: %w", err)
		}
		if (input.Tasks.Provider == domain.ProviderMicrosoftGraph ||
			input.Tasks.Provider == domain.ProviderTodoist) && normalizedAddress == "" {
			return "", AccountCatalog{}, errors.New(
				"an OAuth task route requires an account address",
			)
		}
	}
	if err := validateAccountOAuthGrantSharing(input); err != nil {
		return "", AccountCatalog{}, err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return "", AccountCatalog{}, err
	}
	bindings, err := service.repository.ListCredentialBindings(ctx)
	if err != nil {
		return "", AccountCatalog{}, fmt.Errorf(
			"list credential ownership: %w",
			err,
		)
	}
	for _, requested := range accountCredentialReviews(input) {
		for _, existing := range bindings {
			if requested.Backend == existing.Backend &&
				requested.Key == existing.Key {
				return "", AccountCatalog{}, fmt.Errorf(
					"credential handle %q in backend %q already belongs to account %q",
					requested.Key,
					requested.Backend,
					existing.Account,
				)
			}
		}
	}
	for _, existing := range catalog.Accounts {
		if existing.Alias == input.Alias {
			return "", AccountCatalog{}, fmt.Errorf(
				"account alias %q already exists",
				input.Alias,
			)
		}
	}
	return normalizedAddress, catalog, nil
}

type accountOAuthGrantBinding struct {
	provider      domain.ProviderID
	clientID      string
	redirectURI   string
	authorization AccountCredentialInput
	cloud         microsoftcloud.ID
}

func validateAccountOAuthGrantSharing(input AccountAddInput) error {
	bindings := make([]accountOAuthGrantBinding, 0, 3)
	add := func(
		provider domain.ProviderID,
		clientID, redirectURI string,
		authorization AccountCredentialInput,
		cloud microsoftcloud.ID,
	) {
		bindings = append(bindings, accountOAuthGrantBinding{
			provider: provider, clientID: clientID, redirectURI: redirectURI,
			authorization: authorization, cloud: cloud,
		})
	}
	if input.Mail != nil {
		if input.Mail.Provider == domain.ProviderGoogle && input.Mail.Google != nil {
			route := input.Mail.Google
			add(domain.ProviderGoogle, route.ClientID, route.RedirectURI, route.Authorization, "")
		}
		if input.Mail.Provider == domain.ProviderMicrosoftGraph && input.Mail.MicrosoftGraph != nil {
			route := input.Mail.MicrosoftGraph
			add(domain.ProviderMicrosoftGraph, route.ClientID, route.RedirectURI, route.Authorization, route.MicrosoftCloud)
		}
	}
	if input.Calendar != nil {
		if input.Calendar.Provider == domain.ProviderGoogle && input.Calendar.Google != nil {
			route := input.Calendar.Google
			add(domain.ProviderGoogle, route.ClientID, route.RedirectURI, route.Authorization, "")
		}
		if input.Calendar.Provider == domain.ProviderMicrosoftGraph && input.Calendar.MicrosoftGraph != nil {
			route := input.Calendar.MicrosoftGraph
			add(domain.ProviderMicrosoftGraph, route.ClientID, route.RedirectURI, route.Authorization, route.MicrosoftCloud)
		}
	}
	if input.Tasks != nil && input.Tasks.Provider == domain.ProviderMicrosoftGraph &&
		input.Tasks.MicrosoftGraph != nil {
		route := input.Tasks.MicrosoftGraph.OAuth
		add(domain.ProviderMicrosoftGraph, route.ClientID, route.RedirectURI, route.Authorization, route.MicrosoftCloud)
	}
	if input.Tasks != nil && input.Tasks.Provider == domain.ProviderTodoist &&
		input.Tasks.Todoist != nil {
		route := input.Tasks.Todoist.OAuth
		add(domain.ProviderTodoist, route.ClientID, route.RedirectURI, route.Authorization, "")
	}
	for left := range bindings {
		for right := left + 1; right < len(bindings); right++ {
			if bindings[left].authorization.Backend == bindings[right].authorization.Backend &&
				bindings[left].authorization.Key == bindings[right].authorization.Key &&
				!sameAccountOAuthGrant(bindings[left], bindings[right]) {
				return errors.New("one OAuth authorization handle cannot identify different provider or public-client grants")
			}
		}
	}
	return nil
}

func sameAccountOAuthGrant(left, right accountOAuthGrantBinding) bool {
	if left.provider != right.provider || left.clientID != right.clientID ||
		left.redirectURI != right.redirectURI || left.authorization != right.authorization {
		return false
	}
	return left.provider != domain.ProviderMicrosoftGraph ||
		microsoftcloud.Equivalent(left.cloud, right.cloud)
}

func accountCredentialReviews(input AccountAddInput) []AccountCredentialReview {
	reviews := make([]AccountCredentialReview, 0, 3)
	if input.Mail != nil {
		var credential *AccountCredentialInput
		switch input.Mail.Provider {
		case domain.ProviderJMAP:
			if input.Mail.JMAP != nil {
				credential = &input.Mail.JMAP.Credential
			}
		case domain.ProviderIMAPSMTP:
			if input.Mail.IMAPSMTP != nil {
				credential = &input.Mail.IMAPSMTP.Credential
			}
		case domain.ProviderGoogle:
			if input.Mail.Google != nil {
				credential = &input.Mail.Google.Authorization
			}
		case domain.ProviderMicrosoftGraph:
			if input.Mail.MicrosoftGraph != nil {
				credential = &input.Mail.MicrosoftGraph.Authorization
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderCalDAV, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
		if credential != nil {
			reviews = append(reviews, AccountCredentialReview{
				Service: "mail", Provider: input.Mail.Provider,
				Backend: credential.Backend, Key: credential.Key,
			})
		}
	}
	if input.Calendar != nil {
		var credential *AccountCredentialInput
		switch input.Calendar.Provider {
		case domain.ProviderCalDAV:
			if input.Calendar.CalDAV != nil {
				credential = &input.Calendar.CalDAV.Credential
			}
		case domain.ProviderGoogle:
			if input.Calendar.Google != nil {
				credential = &input.Calendar.Google.Authorization
			}
		case domain.ProviderMicrosoftGraph:
			if input.Calendar.MicrosoftGraph != nil {
				credential = &input.Calendar.MicrosoftGraph.Authorization
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
		if credential != nil {
			reviews = append(reviews, AccountCredentialReview{
				Service: "calendar", Provider: input.Calendar.Provider,
				Backend: credential.Backend, Key: credential.Key,
			})
		}
	}
	if input.Tasks != nil && input.Tasks.Provider == domain.ProviderMicrosoftGraph &&
		input.Tasks.MicrosoftGraph != nil {
		credential := input.Tasks.MicrosoftGraph.OAuth.Authorization
		reviews = append(reviews, AccountCredentialReview{
			Service: "tasks", Provider: input.Tasks.Provider,
			Backend: credential.Backend, Key: credential.Key,
		})
	}
	if input.Tasks != nil && input.Tasks.Provider == domain.ProviderTodoist &&
		input.Tasks.Todoist != nil {
		credential := input.Tasks.Todoist.OAuth.Authorization
		reviews = append(reviews, AccountCredentialReview{
			Service: "tasks", Provider: input.Tasks.Provider,
			Backend: credential.Backend, Key: credential.Key,
		})
	}
	if input.Tasks != nil && input.Tasks.Provider == domain.ProviderCalDAV &&
		input.Tasks.CalDAV != nil {
		credential := input.Tasks.CalDAV.Credential
		reviews = append(reviews, AccountCredentialReview{
			Service: "tasks", Provider: input.Tasks.Provider,
			Backend: credential.Backend, Key: credential.Key,
		})
	}
	return reviews
}

// Rename preserves the opaque account identity and all account-owned state.
func (service *AccountService) Rename(
	ctx context.Context,
	input AccountRenameInput,
) (AccountView, error) {
	account, err := service.reviewRename(ctx, input)
	if err != nil {
		return AccountView{}, err
	}
	if err := service.repository.RenameAccount(ctx, account.ID, input.NewAlias); err != nil {
		return AccountView{}, fmt.Errorf("rename account: %w", err)
	}
	account.Alias = input.NewAlias
	return account, nil
}

// ReviewRename validates an alias change without mutating configuration.
func (service *AccountService) ReviewRename(
	ctx context.Context,
	input AccountRenameInput,
) (AccountChangeReview, error) {
	account, err := service.reviewRename(ctx, input)
	if err != nil {
		return AccountChangeReview{}, err
	}
	return AccountChangeReview{
		Action: "rename", Account: account.ID, Alias: account.Alias,
		NewAlias: input.NewAlias,
	}, nil
}

func (service *AccountService) reviewRename(
	ctx context.Context,
	input AccountRenameInput,
) (AccountView, error) {
	if err := domain.AccountAlias(input.NewAlias).Validate(); err != nil {
		return AccountView{}, err
	}
	account, err := service.Show(ctx, input.Account)
	if err != nil {
		return AccountView{}, err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, existing := range catalog.Accounts {
		if existing.Alias == input.NewAlias && existing.ID != account.ID {
			return AccountView{}, fmt.Errorf("account alias %q already exists", input.NewAlias)
		}
	}
	return account, nil
}

// Remove purges Corresync-owned state before deleting the configuration route.
// If the final config write fails, the account remains configured but requires
// a fresh authentication; readable session copies are never retained.
func (service *AccountService) Remove(
	ctx context.Context,
	input AccountRemoveInput,
) (AccountView, error) {
	account, _, replacementAccount, err := service.reviewRemove(ctx, input)
	if err != nil {
		return AccountView{}, err
	}
	if err := service.purger.PurgeAccountState(ctx, account.ID); err != nil {
		return AccountView{}, fmt.Errorf("purge account state: %w", err)
	}
	if err := service.repository.RemoveAccount(
		ctx,
		account.ID,
		replacementAccount,
	); err != nil {
		return AccountView{}, fmt.Errorf("remove account: %w", err)
	}
	return account, nil
}

// ReviewRemove validates the selected account and replacement default without
// closing sessions, purging local state, or changing configuration.
func (service *AccountService) ReviewRemove(
	ctx context.Context,
	input AccountRemoveInput,
) (AccountChangeReview, error) {
	account, replacement, replacementAccount, err := service.reviewRemove(ctx, input)
	if err != nil {
		return AccountChangeReview{}, err
	}
	return AccountChangeReview{
		Action: "remove", Account: account.ID, Alias: account.Alias,
		ReplacementDefault: replacement, ReplacementAccount: replacementAccount,
		PurgesLocalState:    true,
		MayDeleteOwnedOAuth: accountUsesOAuth(account),
	}, nil
}

func accountUsesOAuth(account AccountView) bool {
	for _, route := range []*AccountRouteView{account.Mail, account.Calendar, account.Tasks} {
		if route == nil {
			continue
		}
		switch route.Provider {
		case domain.ProviderGoogle, domain.ProviderMicrosoftGraph, domain.ProviderTodoist:
			return true
		case
			domain.ProviderMicrosoftOWA,
			domain.ProviderGoogleWeb,
			domain.ProviderJMAP,
			domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV,
			domain.ProviderMicrosoftTasks,
			domain.ProviderGoogleTasks,
			domain.ProviderAppleReminders,
			domain.ProviderTickTick,
			domain.ProviderAnyDoMCP,
			domain.ProviderThings,
			domain.ProviderOmniFocus,
			domain.ProviderPOP3:
		}
	}
	return false
}

func (service *AccountService) reviewRemove(
	ctx context.Context,
	input AccountRemoveInput,
) (AccountView, string, domain.AccountID, error) {
	account, err := service.Show(ctx, input.Account)
	if err != nil {
		return AccountView{}, "", "", err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, "", "", err
	}
	if len(catalog.Accounts) == 1 {
		return AccountView{}, "", "", errors.New("cannot remove the only configured account")
	}
	replacement := input.ReplacementDefault
	var replacementAccountID domain.AccountID
	if account.IsDefault {
		if replacement == "" {
			return AccountView{}, "", "", errors.New(
				"removing the default account requires --new-default",
			)
		}
		replacementAccount, resolveErr := service.Show(ctx, replacement)
		if resolveErr != nil {
			return AccountView{}, "", "", resolveErr
		}
		if replacementAccount.ID == account.ID {
			return AccountView{}, "", "", errors.New(
				"replacement default must be a different account",
			)
		}
		replacementAccountID = replacementAccount.ID
	} else if replacement != "" {
		return AccountView{}, "", "", errors.New(
			"--new-default is valid only when removing the default account",
		)
	}
	return account, replacement, replacementAccountID, nil
}

func validateAccountCatalog(catalog AccountCatalog) error {
	if len(catalog.Accounts) > 32 {
		return errors.New("account catalog size is invalid")
	}
	ids := make(map[domain.AccountID]struct{}, len(catalog.Accounts))
	aliases := make(map[string]struct{}, len(catalog.Accounts))
	defaults := 0
	for _, account := range catalog.Accounts {
		if err := validateAccountView(account); err != nil {
			return err
		}
		if _, exists := ids[account.ID]; exists {
			return errors.New("account catalog contains a duplicate ID")
		}
		if _, exists := aliases[account.Alias]; exists {
			return errors.New("account catalog contains a duplicate alias")
		}
		ids[account.ID] = struct{}{}
		aliases[account.Alias] = struct{}{}
		if account.IsDefault {
			defaults++
		}
	}
	expectedDefaults := 1
	if len(catalog.Accounts) == 0 {
		expectedDefaults = 0
	}
	if defaults != expectedDefaults {
		return errors.New("account catalog must contain exactly one default")
	}
	return nil
}

func validateAccountView(account AccountView) error {
	if err := account.ID.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(account.Alias).Validate(); err != nil {
		return err
	}
	if account.Address != "" {
		if _, _, err := normalizeDiscoveryAddress(account.Address); err != nil {
			return err
		}
	}
	if account.Mail == nil && account.Calendar == nil && account.Tasks == nil {
		return errors.New("account has no service routes")
	}
	if account.Mail != nil {
		if err := validateAccountRouteView(*account.Mail); err != nil {
			return fmt.Errorf("mail route: %w", err)
		}
	}
	if account.Calendar != nil {
		if err := validateAccountRouteView(*account.Calendar); err != nil {
			return fmt.Errorf("calendar route: %w", err)
		}
	}
	if account.Tasks != nil {
		if err := validateAccountRouteView(*account.Tasks); err != nil {
			return fmt.Errorf("task route: %w", err)
		}
	}
	return nil
}

func (service *AccountService) markRouteAvailability(account *AccountView) {
	for _, route := range []*AccountRouteView{account.Mail, account.Calendar} {
		if route != nil {
			_, route.Available = service.available[route.Provider]
		}
	}
	if account.Tasks != nil {
		_, account.Tasks.Available = service.taskAvailable[account.Tasks.Provider]
	}
}

func (service *AccountService) requireAvailable(provider domain.ProviderID) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	if _, available := service.available[provider]; !available {
		return fmt.Errorf("provider %q is not available in this build", provider)
	}
	return nil
}

// ValidateTaskProviderSelection checks the closed, service-scoped build
// catalog without authenticating, discovering an endpoint, or touching config.
func (service *AccountService) ValidateTaskProviderSelection(provider domain.ProviderID) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	switch provider {
	case domain.ProviderMicrosoftGraph,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderCalDAV,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		if _, available := service.taskAvailable[provider]; !available {
			return fmt.Errorf("task provider %q is not available in this build", provider)
		}
		return nil
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogle,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderPOP3:
		return fmt.Errorf("provider %q cannot supply a task route", provider)
	default:
		return fmt.Errorf("provider %q cannot supply a task route", provider)
	}
}

func (service *AccountService) validateMailRoute(route AccountMailRouteInput) error {
	if err := service.requireAvailable(route.Provider); err != nil {
		return err
	}
	present := 0
	if route.OutlookWeb != nil {
		present++
	}
	if route.JMAP != nil {
		present++
	}
	if route.IMAPSMTP != nil {
		present++
	}
	if route.Google != nil {
		present++
	}
	if route.GoogleWeb != nil {
		present++
	}
	if route.MicrosoftGraph != nil {
		present++
	}
	if present != 1 {
		return errors.New("exactly one provider-specific mail route is required")
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return errors.New("microsoft-owa requires outlookWeb settings")
		}
		return validateOutlookWebInput(*route.OutlookWeb)
	case domain.ProviderJMAP:
		if route.JMAP == nil {
			return errors.New("jmap requires JMAP settings")
		}
		return validateJMAPInput(*route.JMAP)
	case domain.ProviderIMAPSMTP:
		if route.IMAPSMTP == nil {
			return errors.New("imap-smtp requires IMAP/SMTP settings")
		}
		return validateIMAPSMTPInput(*route.IMAPSMTP)
	case domain.ProviderGoogle:
		if route.Google == nil {
			return errors.New("google requires Google settings")
		}
		return validateGoogleMailInput(*route.Google)
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires Google Web settings")
		}
		return validateGoogleWebOrigin(route.GoogleWeb.Origin, "mail.google.com")
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires Microsoft Graph settings")
		}
		return validateOAuthInput(
			domain.ProviderMicrosoftGraph,
			*route.MicrosoftGraph,
		)
	case
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return fmt.Errorf("provider %q cannot supply a configured mail route", route.Provider)
	default:
		return fmt.Errorf("unknown mail provider %q", route.Provider)
	}
}

func validateIMAPSMTPInput(route AccountIMAPSMTPInput) error {
	if err := validateAccountTLSEndpoint("IMAP", route.IMAP); err != nil {
		return err
	}
	if err := validateAccountTLSEndpoint("SMTP", route.SMTP); err != nil {
		return err
	}
	if route.Username == "" || len(route.Username) > 320 ||
		strings.TrimSpace(route.Username) != route.Username ||
		strings.ContainsAny(route.Username, "\r\n\x00") {
		return errors.New("IMAP/SMTP username is malformed")
	}
	if err := validateOptionalMailbox(route.Mailbox); err != nil {
		return err
	}
	return validateAccountCredential(route.Credential)
}

func validateGoogleMailInput(route AccountGoogleMailInput) error {
	if route.Username == "" || len(route.Username) > 320 ||
		strings.TrimSpace(route.Username) != route.Username ||
		strings.ContainsAny(route.Username, "\r\n\x00") {
		return errors.New("google mail username is malformed")
	}
	if err := validateOptionalMailbox(route.Mailbox); err != nil {
		return err
	}
	return validateOAuthClientInput(
		route.ClientID,
		route.RedirectURI,
		route.Authorization,
	)
}

func validateAccountTLSEndpoint(name string, endpoint AccountTLSEndpointInput) error {
	if endpoint.Host == "" || len(endpoint.Host) > 253 ||
		strings.TrimSpace(endpoint.Host) != endpoint.Host ||
		strings.ContainsAny(endpoint.Host, "\r\n\x00/@") {
		return fmt.Errorf("%s host is malformed", name)
	}
	if endpoint.Port == 0 {
		return fmt.Errorf("%s port is required", name)
	}
	switch endpoint.Mode {
	case "implicit", "starttls":
		return nil
	default:
		return fmt.Errorf("%s TLS mode must be implicit or starttls", name)
	}
}

func (service *AccountService) validateCalendarRoute(route AccountCalendarRouteInput) error {
	if err := service.requireAvailable(route.Provider); err != nil {
		return err
	}
	present := 0
	if route.OutlookWeb != nil {
		present++
	}
	if route.CalDAV != nil {
		present++
	}
	if route.Google != nil {
		present++
	}
	if route.GoogleWeb != nil {
		present++
	}
	if route.MicrosoftGraph != nil {
		present++
	}
	if present != 1 {
		return errors.New("exactly one provider-specific calendar route is required")
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return errors.New("microsoft-owa requires Outlook Web settings")
		}
		return validateOutlookWebInput(*route.OutlookWeb)
	case domain.ProviderCalDAV:
		if route.CalDAV == nil {
			return errors.New("caldav requires CalDAV settings")
		}
		return validateCalDAVInput(*route.CalDAV)
	case domain.ProviderGoogle:
		if route.Google == nil {
			return errors.New("google requires Google settings")
		}
		return validateOAuthInput(domain.ProviderGoogle, *route.Google)
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires Google Web settings")
		}
		return validateGoogleWebOrigin(
			route.GoogleWeb.Origin,
			"calendar.google.com",
		)
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires Microsoft Graph settings")
		}
		return validateOAuthInput(
			domain.ProviderMicrosoftGraph,
			*route.MicrosoftGraph,
		)
	case
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return fmt.Errorf(
			"provider %q cannot supply a configured calendar route",
			route.Provider,
		)
	default:
		return fmt.Errorf("unknown calendar provider %q", route.Provider)
	}
}

func (service *AccountService) validateTaskRoute(route AccountTaskRouteInput) error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	switch route.Provider {
	case domain.ProviderMicrosoftGraph:
		if _, available := service.taskAvailable[route.Provider]; !available {
			return fmt.Errorf("task provider %q is not available in this build", route.Provider)
		}
		if route.MicrosoftGraph == nil || route.Todoist != nil || route.CalDAV != nil {
			return errors.New("microsoft-graph tasks require independent OAuth settings")
		}
		if err := validateOAuthInput(domain.ProviderMicrosoftGraph, route.MicrosoftGraph.OAuth); err != nil {
			return err
		}
		profile, err := microsoftcloud.Resolve(route.MicrosoftGraph.OAuth.MicrosoftCloud)
		if err != nil {
			return err
		}
		if !profile.TasksAvailable {
			return errors.New("the Microsoft To Do API is unavailable in the selected Microsoft cloud")
		}
		return nil
	case domain.ProviderTodoist:
		if _, available := service.taskAvailable[route.Provider]; !available {
			return fmt.Errorf("task provider %q is not available in this build", route.Provider)
		}
		if route.Todoist == nil || route.MicrosoftGraph != nil || route.CalDAV != nil {
			return errors.New("todoist tasks require independent OAuth settings")
		}
		return validateOAuthInput(domain.ProviderTodoist, route.Todoist.OAuth)
	case domain.ProviderCalDAV:
		if _, available := service.taskAvailable[route.Provider]; !available {
			return fmt.Errorf("task provider %q is not available in this build", route.Provider)
		}
		if route.CalDAV == nil || route.MicrosoftGraph != nil || route.Todoist != nil {
			return errors.New("caldav tasks require CalDAV VTODO settings")
		}
		return validateCalDAVTaskInput(*route.CalDAV)
	case domain.ProviderMicrosoftTasks,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		if _, available := service.taskAvailable[route.Provider]; !available {
			return fmt.Errorf("task provider %q is not available in this build", route.Provider)
		}
		if route.MicrosoftGraph != nil || route.Todoist != nil || route.CalDAV != nil {
			return errors.New("task route contains settings for another provider")
		}
		return nil
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogle,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderPOP3:
		return fmt.Errorf("provider %q cannot supply a task route", route.Provider)
	default:
		return fmt.Errorf("provider %q cannot supply a task route", route.Provider)
	}
}

func validateOAuthInput(
	provider domain.ProviderID,
	route AccountOAuthInput,
) error {
	if err := validateAccountHTTPSURL("API base", route.APIBase); err != nil {
		return err
	}
	apiBase, _ := url.Parse(route.APIBase)
	switch provider {
	case domain.ProviderGoogle:
		if route.MicrosoftCloud != "" {
			return errors.New("google OAuth route cannot select a Microsoft cloud")
		}
		if apiBase.Host != "www.googleapis.com" || apiBase.RawQuery != "" ||
			apiBase.EscapedPath() != "" && apiBase.EscapedPath() != "/" {
			return errors.New("google API base must be https://www.googleapis.com")
		}
	case domain.ProviderMicrosoftGraph:
		if err := microsoftcloud.ValidateAPIBase(route.MicrosoftCloud, route.APIBase); err != nil {
			return err
		}
	case domain.ProviderTodoist:
		if route.MicrosoftCloud != "" || apiBase.Host != "api.todoist.com" ||
			apiBase.RawQuery != "" || apiBase.EscapedPath() != "/api/v1" {
			return errors.New("todoist API base must be https://api.todoist.com/api/v1")
		}
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return fmt.Errorf("provider %q has no OAuth API base policy", provider)
	default:
		return fmt.Errorf("unknown OAuth API provider %q", provider)
	}
	if err := validateOAuthClientInput(
		route.ClientID,
		route.RedirectURI,
		route.Authorization,
	); err != nil {
		return err
	}
	if provider == domain.ProviderTodoist {
		redirect, _ := url.Parse(route.RedirectURI)
		if redirect.Port() == "0" {
			return errors.New("todoist OAuth requires the fixed loopback port registered for the public client")
		}
	}
	return nil
}

func validateOAuthClientInput(
	clientID string,
	redirectURI string,
	authorization AccountCredentialInput,
) error {
	if clientID == "" || len(clientID) > 512 ||
		strings.TrimSpace(clientID) != clientID ||
		strings.ContainsAny(clientID, "\r\n\x00") {
		return errors.New("OAuth client ID is malformed")
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil || redirect.Scheme != "http" ||
		redirect.Hostname() != "127.0.0.1" ||
		redirect.User != nil || redirect.Fragment != "" ||
		redirect.RawQuery != "" || redirect.Port() == "" {
		return errors.New("OAuth redirect URI must use an explicit loopback port")
	}
	if authorization.Backend != "os-keyring" {
		return errors.New("OAuth authorization must use the OS keyring")
	}
	return validateAccountCredential(authorization)
}

func validateGoogleWebOrigin(raw, host string) error {
	if err := validateAccountOrigin(raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.Host != host {
		return fmt.Errorf("google Web origin must be https://%s", host)
	}
	return nil
}

func validateCalDAVInput(route AccountCalDAVInput) error {
	return validateCalDAVRouteInput(
		route.Endpoint, route.CalendarPath, "calendar", route.Username,
		route.Credential,
	)
}

func validateCalDAVTaskInput(route AccountCalDAVTaskInput) error {
	return validateCalDAVRouteInput(
		route.Endpoint, route.TaskListPath, "task list", route.Username,
		route.Credential,
	)
}

func validateCalDAVRouteInput(
	endpoint, collectionPath, collectionName, username string,
	credential AccountCredentialInput,
) error {
	if err := validateAccountHTTPSURL("CalDAV endpoint", endpoint); err != nil {
		return err
	}
	if collectionPath != "" &&
		(!strings.HasPrefix(collectionPath, "/") || len(collectionPath) > 2048 ||
			strings.ContainsAny(collectionPath, "\r\n\x00?#")) {
		return fmt.Errorf("CalDAV %s path must be a bounded absolute DAV path", collectionName)
	}
	if username == "" || len(username) > 320 ||
		strings.TrimSpace(username) != username ||
		strings.ContainsAny(username, "\r\n\x00") {
		return errors.New("CalDAV username is malformed")
	}
	return validateAccountCredential(credential)
}

func validateOutlookWebInput(route AccountOutlookWebInput) error {
	if err := validateAccountOrigin(route.Origin); err != nil {
		return err
	}
	return validateOptionalMailbox(route.Mailbox)
}

func validateJMAPInput(route AccountJMAPInput) error {
	if err := validateAccountHTTPSURL("JMAP session URL", route.SessionURL); err != nil {
		return err
	}
	if route.Username == "" || len(route.Username) > 320 ||
		strings.TrimSpace(route.Username) != route.Username ||
		strings.ContainsAny(route.Username, "\r\n\x00") {
		return errors.New("JMAP username is malformed")
	}
	return validateAccountCredential(route.Credential)
}

func validateAccountCredential(reference AccountCredentialInput) error {
	switch reference.Backend {
	case "os-keyring", "helper":
	default:
		return errors.New("credential backend must be os-keyring or helper")
	}
	if !reference.Consent {
		return errors.New("credential use requires explicit consent")
	}
	if reference.Key == "" || len(reference.Key) > 256 ||
		strings.ContainsAny(reference.Key, "\r\n\x00") {
		return errors.New("credential key is malformed")
	}
	return nil
}

func validateOptionalMailbox(mailbox string) error {
	if mailbox == "" {
		return nil
	}
	normalized, _, err := normalizeDiscoveryAddress(mailbox)
	if err != nil || normalized != mailbox {
		return errors.New("mailbox must be one normalized bare email address")
	}
	return nil
}

func validateAccountHTTPSURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute credential-free HTTPS URL", name)
	}
	return nil
}

func validateAccountRouteView(route AccountRouteView) error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	if len(route.Endpoints) == 0 || len(route.Endpoints) > 4 {
		return errors.New("route endpoint count is invalid")
	}
	for _, endpoint := range route.Endpoints {
		if endpoint.Kind == "" || len(endpoint.Kind) > 32 ||
			endpoint.Value == "" || len(endpoint.Value) > 2048 ||
			strings.ContainsAny(endpoint.Kind+endpoint.Value, "\r\n\x00") {
			return errors.New("route endpoint is malformed")
		}
	}
	if route.Identity != "" && (len(route.Identity) > 320 ||
		strings.ContainsAny(route.Identity, "\r\n\x00")) {
		return errors.New("route identity is malformed")
	}
	if route.Credential != nil {
		switch route.Credential.Backend {
		case "os-keyring", "helper":
		default:
			return errors.New("route credential backend is malformed")
		}
		if !route.Credential.Configured {
			return errors.New("route credential summary is inconsistent")
		}
	}
	return nil
}

func (registration AccountRegistration) view() AccountView {
	return AccountView{
		ID: registration.ID, Alias: registration.Alias, Address: registration.Address,
		Mail: mailRouteView(registration.Mail), Calendar: calendarRouteView(registration.Calendar),
		Tasks:     taskRouteInputView(registration.Tasks),
		IsDefault: registration.IsDefault,
	}
}

// TaskRouteView returns the deliberately minimal, secret-free task route.
// Provider adapters enrich capabilities only after an explicit sign-in.
func TaskRouteView(provider domain.ProviderID) *AccountRouteView {
	return &AccountRouteView{
		Provider: provider,
		Endpoints: []DiscoveredEndpoint{
			{Kind: "configured", Value: "provider-specific"},
		},
	}
}

func taskRouteInputView(route *AccountTaskRouteInput) *AccountRouteView {
	if route == nil {
		return nil
	}
	if route.Provider == domain.ProviderMicrosoftGraph && route.MicrosoftGraph != nil {
		return oauthRouteView(route.Provider, &route.MicrosoftGraph.OAuth)
	}
	if route.Provider == domain.ProviderTodoist && route.Todoist != nil {
		return oauthRouteView(route.Provider, &route.Todoist.OAuth)
	}
	if route.Provider == domain.ProviderCalDAV && route.CalDAV != nil {
		endpoints := []DiscoveredEndpoint{{Kind: "endpoint", Value: route.CalDAV.Endpoint}}
		if route.CalDAV.TaskListPath != "" {
			endpoints = append(endpoints, DiscoveredEndpoint{
				Kind: "task-list", Value: route.CalDAV.TaskListPath,
			})
		}
		return &AccountRouteView{
			Provider: route.Provider, Endpoints: endpoints,
			Identity: route.CalDAV.Username,
			Credential: &AccountCredentialView{
				Configured: true, Backend: route.CalDAV.Credential.Backend,
				Consented: route.CalDAV.Credential.Consent,
			},
		}
	}
	return TaskRouteView(route.Provider)
}

func mailRouteView(route *AccountMailRouteInput) *AccountRouteView {
	if route == nil {
		return nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		return outlookWebRouteView(route.Provider, route.OutlookWeb)
	case domain.ProviderJMAP:
		if route.JMAP == nil {
			return &AccountRouteView{Provider: route.Provider}
		}
		return &AccountRouteView{
			Provider: route.Provider,
			Endpoints: []DiscoveredEndpoint{
				{Kind: "session", Value: route.JMAP.SessionURL},
			},
			Identity: route.JMAP.Username,
			Credential: &AccountCredentialView{
				Configured: true, Backend: route.JMAP.Credential.Backend,
				Consented: route.JMAP.Credential.Consent,
			},
		}
	case domain.ProviderIMAPSMTP:
		if route.IMAPSMTP == nil {
			return &AccountRouteView{Provider: route.Provider}
		}
		return &AccountRouteView{
			Provider: route.Provider,
			Endpoints: []DiscoveredEndpoint{
				{
					Kind:  "imap",
					Value: accountTLSEndpointView(route.IMAPSMTP.IMAP),
				},
				{
					Kind:  "smtp",
					Value: accountTLSEndpointView(route.IMAPSMTP.SMTP),
				},
			},
			Identity: route.IMAPSMTP.Username,
			Credential: &AccountCredentialView{
				Configured: true, Backend: route.IMAPSMTP.Credential.Backend,
				Consented: route.IMAPSMTP.Credential.Consent,
			},
		}
	case domain.ProviderGoogle:
		if route.Google == nil {
			return &AccountRouteView{Provider: route.Provider}
		}
		return &AccountRouteView{
			Provider: route.Provider,
			Endpoints: []DiscoveredEndpoint{
				{Kind: "imap", Value: "implicit://imap.gmail.com:993"},
				{Kind: "smtp", Value: "starttls://smtp.gmail.com:587"},
			},
			Identity: route.Google.Username,
			Credential: &AccountCredentialView{
				Configured: true, Backend: route.Google.Authorization.Backend,
				Consented: route.Google.Authorization.Consent,
			},
		}
	case domain.ProviderGoogleWeb:
		return webRouteView(route.Provider, route.GoogleWeb)
	case domain.ProviderMicrosoftGraph:
		return oauthRouteView(route.Provider, route.MicrosoftGraph)
	case
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return &AccountRouteView{Provider: route.Provider}
	default:
		return &AccountRouteView{Provider: route.Provider}
	}
}

func accountTLSEndpointView(endpoint AccountTLSEndpointInput) string {
	return fmt.Sprintf("%s://%s:%d", endpoint.Mode, endpoint.Host, endpoint.Port)
}

func calendarRouteView(route *AccountCalendarRouteInput) *AccountRouteView {
	if route == nil {
		return nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		return outlookWebRouteView(route.Provider, route.OutlookWeb)
	case domain.ProviderCalDAV:
		if route.CalDAV == nil {
			return &AccountRouteView{Provider: route.Provider}
		}
		endpoints := []DiscoveredEndpoint{
			{Kind: "endpoint", Value: route.CalDAV.Endpoint},
		}
		if route.CalDAV.CalendarPath != "" {
			endpoints = append(endpoints, DiscoveredEndpoint{
				Kind: "calendar", Value: route.CalDAV.CalendarPath,
			})
		}
		return &AccountRouteView{
			Provider: route.Provider, Endpoints: endpoints,
			Identity: route.CalDAV.Username,
			Credential: &AccountCredentialView{
				Configured: true, Backend: route.CalDAV.Credential.Backend,
				Consented: route.CalDAV.Credential.Consent,
			},
		}
	case domain.ProviderGoogle:
		return oauthRouteView(route.Provider, route.Google)
	case domain.ProviderGoogleWeb:
		return webRouteView(route.Provider, route.GoogleWeb)
	case domain.ProviderMicrosoftGraph:
		return oauthRouteView(route.Provider, route.MicrosoftGraph)
	case
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return &AccountRouteView{Provider: route.Provider}
	default:
		return &AccountRouteView{Provider: route.Provider}
	}
}

func oauthRouteView(
	provider domain.ProviderID,
	route *AccountOAuthInput,
) *AccountRouteView {
	if route == nil {
		return &AccountRouteView{Provider: provider}
	}
	return &AccountRouteView{
		Provider:  provider,
		Endpoints: []DiscoveredEndpoint{{Kind: "api", Value: route.APIBase}},
		Credential: &AccountCredentialView{
			Configured: true, Backend: route.Authorization.Backend,
			Consented: route.Authorization.Consent,
		},
	}
}

func webRouteView(
	provider domain.ProviderID,
	route *AccountWebInput,
) *AccountRouteView {
	if route == nil {
		return &AccountRouteView{Provider: provider}
	}
	return &AccountRouteView{
		Provider:  provider,
		Endpoints: []DiscoveredEndpoint{{Kind: "origin", Value: route.Origin}},
	}
}

func outlookWebRouteView(
	provider domain.ProviderID,
	route *AccountOutlookWebInput,
) *AccountRouteView {
	if route == nil {
		return &AccountRouteView{Provider: provider}
	}
	return &AccountRouteView{
		Provider:  provider,
		Endpoints: []DiscoveredEndpoint{{Kind: "origin", Value: route.Origin}},
		Identity:  route.Mailbox,
	}
}

func cloneMailRoute(route *AccountMailRouteInput) *AccountMailRouteInput {
	if route == nil {
		return nil
	}
	cloned := *route
	if route.OutlookWeb != nil {
		value := *route.OutlookWeb
		cloned.OutlookWeb = &value
	}
	if route.JMAP != nil {
		value := *route.JMAP
		cloned.JMAP = &value
	}
	if route.IMAPSMTP != nil {
		value := *route.IMAPSMTP
		cloned.IMAPSMTP = &value
	}
	if route.Google != nil {
		value := *route.Google
		cloned.Google = &value
	}
	if route.GoogleWeb != nil {
		value := *route.GoogleWeb
		cloned.GoogleWeb = &value
	}
	if route.MicrosoftGraph != nil {
		value := *route.MicrosoftGraph
		cloned.MicrosoftGraph = &value
	}
	return &cloned
}

func cloneCalendarRoute(route *AccountCalendarRouteInput) *AccountCalendarRouteInput {
	if route == nil {
		return nil
	}
	cloned := *route
	if route.OutlookWeb != nil {
		value := *route.OutlookWeb
		cloned.OutlookWeb = &value
	}
	if route.CalDAV != nil {
		value := *route.CalDAV
		cloned.CalDAV = &value
	}
	if route.Google != nil {
		value := *route.Google
		cloned.Google = &value
	}
	if route.GoogleWeb != nil {
		value := *route.GoogleWeb
		cloned.GoogleWeb = &value
	}
	if route.MicrosoftGraph != nil {
		value := *route.MicrosoftGraph
		cloned.MicrosoftGraph = &value
	}
	return &cloned
}

func cloneTaskRoute(route *AccountTaskRouteInput) *AccountTaskRouteInput {
	if route == nil {
		return nil
	}
	cloned := *route
	if route.MicrosoftGraph != nil {
		value := *route.MicrosoftGraph
		cloned.MicrosoftGraph = &value
	}
	if route.Todoist != nil {
		value := *route.Todoist
		cloned.Todoist = &value
	}
	if route.CalDAV != nil {
		value := *route.CalDAV
		cloned.CalDAV = &value
	}
	return &cloned
}

func validateAccountOrigin(raw string) error {
	origin, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse origin: %w", err)
	}
	if origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" ||
		origin.Path != "" && origin.Path != "/" {
		return errors.New("origin must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return nil
}
