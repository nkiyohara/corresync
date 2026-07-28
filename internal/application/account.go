package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
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
// MCP. Mail and calendar routes may use different providers.
type AccountView struct {
	ID        domain.AccountID  `json:"id"`
	Alias     string            `json:"alias"`
	Address   string            `json:"address,omitempty"`
	Mail      *AccountRouteView `json:"mail,omitempty"`
	Calendar  *AccountRouteView `json:"calendar,omitempty"`
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

// AccountCalDAVInput configures authenticated principal discovery for one
// calendar. CalendarPath may remain empty to select the first VEVENT calendar.
type AccountCalDAVInput struct {
	Endpoint     string                 `json:"endpoint"`
	CalendarPath string                 `json:"calendarPath,omitempty"`
	Username     string                 `json:"username"`
	Credential   AccountCredentialInput `json:"credential"`
}

// AccountMailRouteInput is a closed selection for shipped mail adapters.
type AccountMailRouteInput struct {
	Provider   domain.ProviderID       `json:"provider"`
	OutlookWeb *AccountOutlookWebInput `json:"outlookWeb,omitempty"`
	JMAP       *AccountJMAPInput       `json:"jmap,omitempty"`
	IMAPSMTP   *AccountIMAPSMTPInput   `json:"imapSmtp,omitempty"`
}

// AccountCalendarRouteInput is the corresponding calendar selection.
type AccountCalendarRouteInput struct {
	Provider   domain.ProviderID       `json:"provider"`
	OutlookWeb *AccountOutlookWebInput `json:"outlookWeb,omitempty"`
	CalDAV     *AccountCalDAVInput     `json:"caldav,omitempty"`
}

// AccountAddInput explicitly selects service routes to persist. Discovery
// never writes configuration or starts authentication.
type AccountAddInput struct {
	Alias    string                     `json:"alias"`
	Address  string                     `json:"address"`
	Mail     *AccountMailRouteInput     `json:"mail,omitempty"`
	Calendar *AccountCalendarRouteInput `json:"calendar,omitempty"`
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

// AccountRepository persists secret-free account definitions. Implementations
// must perform each mutation atomically against the latest complete config.
type AccountRepository interface {
	ListAccounts(context.Context) (AccountCatalog, error)
	AddAccount(context.Context, AccountRegistration) error
	RenameAccount(context.Context, domain.AccountID, string) error
	RemoveAccount(context.Context, domain.AccountID, string) error
}

// AccountStatePurger removes Corresync-owned per-account state. It never reads
// or removes credentials owned by another application.
type AccountStatePurger interface {
	PurgeAccountState(context.Context, domain.AccountID) error
}

type accountIDGenerator func() (domain.AccountID, error)

// AccountService owns account lifecycle validation and isolation semantics.
type AccountService struct {
	repository AccountRepository
	purger     AccountStatePurger
	available  map[domain.ProviderID]struct{}
	newID      accountIDGenerator
}

// NewAccountService creates the shared lifecycle use case.
func NewAccountService(
	repository AccountRepository,
	purger AccountStatePurger,
	available []domain.ProviderID,
) (*AccountService, error) {
	if repository == nil {
		return nil, errors.New("account repository is required")
	}
	if purger == nil {
		return nil, errors.New("account state purger is required")
	}
	providers := make(map[domain.ProviderID]struct{}, len(available))
	for _, provider := range available {
		if err := provider.Validate(); err != nil {
			return nil, err
		}
		providers[provider] = struct{}{}
	}
	return &AccountService{
		repository: repository,
		purger:     purger,
		available:  providers,
		newID:      domain.NewAccountID,
	}, nil
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
	if err := domain.AccountAlias(input.Alias).Validate(); err != nil {
		return AccountView{}, err
	}
	normalizedAddress, _, err := normalizeDiscoveryAddress(input.Address)
	if err != nil {
		return AccountView{}, err
	}
	if input.Mail == nil && input.Calendar == nil {
		return AccountView{}, errors.New("at least one mail or calendar route is required")
	}
	if input.Mail != nil {
		if err := service.validateMailRoute(*input.Mail); err != nil {
			return AccountView{}, fmt.Errorf("mail route: %w", err)
		}
	}
	if input.Calendar != nil {
		if err := service.validateCalendarRoute(*input.Calendar); err != nil {
			return AccountView{}, fmt.Errorf("calendar route: %w", err)
		}
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, existing := range catalog.Accounts {
		if existing.Alias == input.Alias {
			return AccountView{}, fmt.Errorf("account alias %q already exists", input.Alias)
		}
	}
	accountID, err := service.newID()
	if err != nil {
		return AccountView{}, fmt.Errorf("generate account ID: %w", err)
	}
	registration := AccountRegistration{
		ID: accountID, Alias: input.Alias, Address: normalizedAddress,
		Mail: cloneMailRoute(input.Mail), Calendar: cloneCalendarRoute(input.Calendar),
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

// Rename preserves the opaque account identity and all account-owned state.
func (service *AccountService) Rename(
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
	if err := service.repository.RenameAccount(ctx, account.ID, input.NewAlias); err != nil {
		return AccountView{}, fmt.Errorf("rename account: %w", err)
	}
	account.Alias = input.NewAlias
	return account, nil
}

// Remove purges Corresync-owned state before deleting the configuration route.
// If the final config write fails, the account remains configured but requires
// a fresh authentication; readable session copies are never retained.
func (service *AccountService) Remove(
	ctx context.Context,
	input AccountRemoveInput,
) (AccountView, error) {
	account, err := service.Show(ctx, input.Account)
	if err != nil {
		return AccountView{}, err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	if len(catalog.Accounts) == 1 {
		return AccountView{}, errors.New("cannot remove the only configured account")
	}
	replacement := input.ReplacementDefault
	if account.IsDefault {
		if replacement == "" {
			return AccountView{}, errors.New(
				"removing the default account requires --new-default",
			)
		}
		replacementAccount, resolveErr := service.Show(ctx, replacement)
		if resolveErr != nil {
			return AccountView{}, resolveErr
		}
		if replacementAccount.ID == account.ID {
			return AccountView{}, errors.New("replacement default must be a different account")
		}
		replacement = replacementAccount.Alias
	} else if replacement != "" {
		return AccountView{}, errors.New("--new-default is valid only when removing the default account")
	}
	if err := service.purger.PurgeAccountState(ctx, account.ID); err != nil {
		return AccountView{}, fmt.Errorf("purge account state: %w", err)
	}
	if err := service.repository.RemoveAccount(ctx, account.ID, replacement); err != nil {
		return AccountView{}, fmt.Errorf("remove account: %w", err)
	}
	return account, nil
}

func validateAccountCatalog(catalog AccountCatalog) error {
	if len(catalog.Accounts) == 0 || len(catalog.Accounts) > 32 {
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
	if defaults != 1 {
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
	if account.Mail == nil && account.Calendar == nil {
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
	return nil
}

func (service *AccountService) markRouteAvailability(account *AccountView) {
	for _, route := range []*AccountRouteView{account.Mail, account.Calendar} {
		if route != nil {
			_, route.Available = service.available[route.Provider]
		}
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderCalDAV,
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderPOP3:
		return fmt.Errorf(
			"provider %q cannot supply a configured calendar route",
			route.Provider,
		)
	default:
		return fmt.Errorf("unknown calendar provider %q", route.Provider)
	}
}

func validateCalDAVInput(route AccountCalDAVInput) error {
	if err := validateAccountHTTPSURL("CalDAV endpoint", route.Endpoint, true); err != nil {
		return err
	}
	if route.CalendarPath != "" &&
		(!strings.HasPrefix(route.CalendarPath, "/") ||
			len(route.CalendarPath) > 2048 ||
			strings.ContainsAny(route.CalendarPath, "\r\n\x00?#")) {
		return errors.New("CalDAV calendar path must be a bounded absolute DAV path")
	}
	if route.Username == "" || len(route.Username) > 320 ||
		strings.TrimSpace(route.Username) != route.Username ||
		strings.ContainsAny(route.Username, "\r\n\x00") {
		return errors.New("CalDAV username is malformed")
	}
	return validateAccountCredential(route.Credential)
}

func validateOutlookWebInput(route AccountOutlookWebInput) error {
	if err := validateAccountOrigin(route.Origin); err != nil {
		return err
	}
	return validateOptionalMailbox(route.Mailbox)
}

func validateJMAPInput(route AccountJMAPInput) error {
	if err := validateAccountHTTPSURL("JMAP session URL", route.SessionURL, true); err != nil {
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

func validateAccountHTTPSURL(name, raw string, allowPath bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an absolute credential-free HTTPS URL", name)
	}
	if !allowPath && (parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "") {
		return fmt.Errorf("%s must not contain a path or query", name)
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
		IsDefault: registration.IsDefault,
	}
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderCalDAV,
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderPOP3:
		return &AccountRouteView{Provider: route.Provider}
	default:
		return &AccountRouteView{Provider: route.Provider}
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
