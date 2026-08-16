package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/rollout"
)

type accountCommand struct {
	Discover accountDiscoverCommand `cmd:"" help:"Find explainable provider candidates without authenticating."`
	List     accountListCommand     `cmd:"" help:"List configured account routes."`
	Show     accountShowCommand     `cmd:"" help:"Show one configured account route."`
	Add      accountAddCommand      `cmd:"" help:"Add an explicitly selected route without authenticating."`
	Rename   accountRenameCommand   `cmd:"" help:"Rename an account while preserving its stable identity."`
	Remove   accountRemoveCommand   `cmd:"" help:"Remove an account and its Corresync-owned local state."`
}

type accountDiscoverCommand struct {
	Address string `arg:"" help:"Bare email address to inspect without credentials."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountListCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type accountShowCommand struct {
	Account string `arg:"" help:"Account alias or stable opaque ID."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountAddCommand struct {
	Address                   string `arg:"" optional:"" help:"Bare email address; task-only routes may omit it."`
	Alias                     string `help:"Local alias; defaults to the address local part."`
	Provider                  string `help:"Compatibility alias for --mail-provider."`
	MailProvider              string `help:"Explicit mail provider override; use none for calendar-only."`
	CalendarProvider          string `help:"Explicit calendar provider override; use none for mail-only."`
	TaskProvider              string `help:"Explicit task provider override; use none to omit tasks."`
	MicrosoftCloud            string `default:"global" enum:"global,gcc-high,dod,china" help:"Microsoft Graph deployment shared by selected Microsoft services."`
	Origin                    string `help:"Outlook Web HTTPS origin override."`
	Mailbox                   string `help:"Optional routed or sender mailbox identity."`
	SessionURL                string `help:"JMAP HTTPS session resource."`
	APIBase                   string `help:"Google Calendar or Microsoft Graph API HTTPS base override."`
	OAuthClientID             string `name:"oauth-client-id" help:"BYO OAuth public-client ID."`
	OAuthRedirectURI          string `name:"oauth-redirect-uri" help:"Registered http://127.0.0.1 loopback redirect URI."`
	AuthorizationKey          string `help:"OS-keyring handle for the OAuth grant."`
	ApproveOAuth              bool   `name:"approve-oauth" help:"Confirm explicit OAuth authorization when no valid grant exists."`
	CalendarAPIBase           string `name:"calendar-api-base" help:"Calendar OAuth API HTTPS base override."`
	CalendarOAuthClientID     string `name:"calendar-oauth-client-id" help:"Calendar BYO OAuth public-client ID; defaults to --oauth-client-id."`
	CalendarOAuthRedirectURI  string `name:"calendar-oauth-redirect-uri" help:"Calendar loopback redirect; defaults to --oauth-redirect-uri."`
	CalendarAuthorizationKey  string `name:"calendar-authorization-key" help:"Calendar OAuth grant key; defaults to --authorization-key."`
	ApproveCalendarOAuth      bool   `name:"approve-calendar-oauth" help:"Confirm a distinct calendar OAuth authorization."`
	TaskAPIBase               string `name:"task-api-base" help:"Pinned task API base override."`
	TaskOAuthClientID         string `name:"task-oauth-client-id" help:"Task BYO OAuth client ID; defaults to --oauth-client-id."`
	TaskOAuthRedirectURI      string `name:"task-oauth-redirect-uri" help:"Task loopback redirect; defaults to --oauth-redirect-uri."`
	TaskAuthorizationKey      string `name:"task-authorization-key" help:"Independent OS-keyring grant handle for tasks; defaults to --authorization-key."`
	ApproveTaskOAuth          bool   `name:"approve-task-oauth" help:"Confirm the task OAuth grant and its task-only scope."`
	TaskOAuthSecretBackend    string `name:"task-oauth-secret-backend" default:"os-keyring" enum:"os-keyring,helper" help:"External backend for a confidential task OAuth client secret."`
	TaskOAuthSecretKey        string `name:"task-oauth-secret-key" help:"External lookup handle for a confidential task OAuth client secret."`
	ApproveTaskOAuthSecret    bool   `name:"approve-task-oauth-secret" help:"Record explicit consent to resolve the confidential task OAuth client secret."`
	TaskReadOnly              bool   `name:"task-read-only" help:"Request the selected provider's read-only task scope."`
	TaskCalDAVEndpoint        string `name:"task-caldav-endpoint" help:"CalDAV HTTPS discovery endpoint for VTODO tasks; defaults to --caldav-endpoint."`
	TaskListPath              string `name:"task-list-path" help:"Optional absolute CalDAV VTODO collection path."`
	TaskUsername              string `name:"task-username" help:"CalDAV task login identity; defaults to calendar, mail, or account identity."`
	TaskCredentialBackend     string `name:"task-credential-backend" help:"External CalDAV task credential backend; defaults to --credential-backend."`
	TaskCredentialKey         string `name:"task-credential-key" help:"Independent external CalDAV task credential handle."`
	ApproveTaskCredential     bool   `name:"approve-task-credential" help:"Record explicit consent for the CalDAV task credential."`
	Username                  string `help:"Mail login identity; defaults to the address and must match it for Google."`
	CredentialBackend         string `default:"os-keyring" enum:"os-keyring,helper" help:"External standards credential backend."`
	CredentialKey             string `help:"External standards credential lookup key."`
	ApproveCredential         bool   `help:"Record explicit consent to use that external credential."`
	IMAPHost                  string `help:"IMAP server host."`
	IMAPPort                  uint16 `help:"IMAP server port."`
	IMAPTLS                   string `name:"imap-tls" default:"implicit" enum:"implicit,starttls" help:"IMAP TLS mode."`
	SMTPHost                  string `help:"SMTP Submission server host."`
	SMTPPort                  uint16 `help:"SMTP Submission server port."`
	SMTPTLS                   string `name:"smtp-tls" default:"starttls" enum:"implicit,starttls" help:"SMTP TLS mode."`
	CalDAVEndpoint            string `name:"caldav-endpoint" help:"CalDAV HTTPS discovery endpoint."`
	CalendarPath              string `help:"Optional absolute CalDAV calendar path."`
	CalendarUsername          string `help:"CalDAV login identity; defaults to --username or the address."`
	CalendarCredentialBackend string `help:"External CalDAV credential backend; defaults to --credential-backend."`
	CalendarCredentialKey     string `help:"External CalDAV credential key; defaults to --credential-key."`
	ApproveCalendarCredential bool   `help:"Record consent for a distinct CalDAV credential."`
	Default                   bool   `help:"Make this the default account."`
	JSON                      bool   `help:"Write machine-readable JSON."`

	// onboardingOAuthProvider records which guided setup branch populated the
	// shared OAuth fields. It is ephemeral provenance, never a CLI flag or
	// persisted account field.
	onboardingOAuthProvider domain.ProviderID
}

type accountRenameCommand struct {
	Account string `arg:"" help:"Account alias or stable opaque ID."`
	Alias   string `arg:"" help:"New local alias."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountRemoveCommand struct {
	Account    string `arg:"" help:"Account alias or stable opaque ID."`
	NewDefault string `help:"Replacement alias when removing the default account."`
	Approve    bool   `help:"Confirm deletion of Corresync-owned local state."`
	JSON       bool   `help:"Write machine-readable JSON."`
}

type accountAddResult struct {
	Selected             *application.ProviderCandidate `json:"selected,omitempty"`
	SelectedTaskProvider domain.ProviderID              `json:"selectedTaskProvider,omitempty"`
	Account              application.AccountView        `json:"account"`
}

var errUnsupportedLegacyGoogleRoute = errors.New(
	"this account uses an unsupported legacy Google route; remove it and " +
		"add it again with provider google",
)

const googleWorkspaceMCPGuide = "https://developers.google.com/workspace/guides/configure-mcp-servers"

func googleOAuthPendingError(prefix string) error {
	return fmt.Errorf(
		"%s %w. Outlook Web is available now; JMAP, IMAP/SMTP, CalDAV, and "+
			"Microsoft Graph can also be configured when your provider supports "+
			"them. Until then, Google's official Workspace MCP servers are an "+
			"independent Developer Preview: %s",
		prefix,
		rollout.ErrGoogleOAuthPending,
		googleWorkspaceMCPGuide,
	)
}

func (command *accountDiscoverCommand) Run(app *runtime) error {
	_, discoverer, err := app.accountServices()
	if err != nil {
		return err
	}
	result, err := discoverer.Discover(app.context, command.Address)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	return writeDiscoveryResult(app, result)
}

func (command *accountListCommand) Run(app *runtime) error {
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, catalog)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf("%s  %s\n\n", view.info(), view.strong("Accounts")); err != nil {
		return err
	}
	if len(catalog.Accounts) == 0 {
		_, err := view.printf(
			"  No accounts configured.\n\n  %s\n",
			view.command("Next: corr setup <email-address>"),
		)
		return err
	}
	for _, account := range catalog.Accounts {
		marker := " "
		if account.IsDefault {
			marker = "*"
		}
		address := account.Address
		if address == "" {
			address = "address not set"
		}
		routes := accountRouteLabel(account)
		if _, err := view.printf(
			"  %s %-16s %-28s %s\n",
			marker,
			sanitizeCell(account.Alias, 64),
			sanitizeCell(routes, 64),
			view.muted(sanitizeCell(address, 254)),
		); err != nil {
			return err
		}
	}
	_, err = view.printf(
		"\n  %s\n  %s\n",
		view.command("Manage interactively: corr settings"),
		view.command("Rename directly: corr account rename <current-name> <new-name>"),
	)
	return err
}

func (command *accountShowCommand) Run(app *runtime) error {
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Show(app.context, command.Account)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, account)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err = view.printf(
		"%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %t\n",
		view.info(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)),
		"ID", account.ID,
		"Address", sanitizeCell(account.Address, 254),
		"Default", account.IsDefault,
	); err != nil {
		return err
	}
	for _, service := range []struct {
		name  string
		route *application.AccountRouteView
	}{{"Mail", account.Mail}, {"Calendar", account.Calendar}, {"Tasks", account.Tasks}} {
		if service.route == nil {
			continue
		}
		if _, err := view.printf(
			"\n  %-10s %s\n  %-10s %s\n",
			service.name,
			service.route.Provider,
			"Available",
			yesNo(service.route.Available),
		); err != nil {
			return err
		}
		for _, endpoint := range service.route.Endpoints {
			if _, err := view.printf(
				"  %-10s %s\n",
				endpoint.Kind,
				sanitizeCell(endpoint.Value, 2048),
			); err != nil {
				return err
			}
		}
		if service.route.Identity != "" {
			if _, err := view.printf(
				"  %-10s %s\n",
				"Identity",
				sanitizeCell(service.route.Identity, 320),
			); err != nil {
				return err
			}
		}
		if service.route.Credential != nil {
			if _, err := view.printf(
				"  %-10s %s · consent %s\n",
				"Credential",
				service.route.Credential.Backend,
				yesNo(service.route.Credential.Consented),
			); err != nil {
				return err
			}
		}
	}
	_, err = view.printf(
		"\n  %s\n",
		view.command(
			"Rename: corr account rename "+shellSingleQuote(account.Alias)+" <new-name>",
		),
	)
	return err
}

func (command *accountAddCommand) Run(app *runtime) error {
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		return err
	}
	mailProvider, err := command.effectiveMailProvider()
	if err != nil {
		return err
	}
	if mailProvider == "none" &&
		(command.CalendarProvider == "" || command.CalendarProvider == "none") &&
		(command.TaskProvider == "" || command.TaskProvider == "none") {
		return errors.New(
			"at least one mail, calendar, or task provider is required",
		)
	}
	if command.TaskProvider != "" && command.TaskProvider != "none" {
		if domain.ProviderID(command.TaskProvider) == domain.ProviderGoogleTasks &&
			!rollout.GoogleOAuthApproved {
			return googleOAuthPendingError("Google Tasks was selected.")
		}
		if err := accounts.ValidateTaskProviderSelection(domain.ProviderID(command.TaskProvider)); err != nil {
			return err
		}
	}
	if (mailProvider == "" || mailProvider == "none") &&
		(command.CalendarProvider == "" || command.CalendarProvider == "none") {
		if command.TaskProvider != "" && command.TaskProvider != "none" {
			return command.addTaskOnly(app, accounts)
		}
	}
	result, err := discoverer.Discover(app.context, command.Address)
	if err != nil {
		return err
	}
	selectionProvider := mailProvider
	if selectionProvider == "none" {
		selectionProvider = command.CalendarProvider
	}
	if selectionProvider == "" || selectionProvider == "none" {
		selectionProvider = command.TaskProvider
	}
	var endpointOverride string
	switch domain.ProviderID(selectionProvider) {
	case domain.ProviderJMAP:
		endpointOverride = command.SessionURL
	case domain.ProviderIMAPSMTP:
		if command.IMAPHost != "" && command.IMAPPort != 0 {
			endpointOverride = fmt.Sprintf(
				"%s://%s",
				command.IMAPTLS,
				net.JoinHostPort(command.IMAPHost, strconv.Itoa(int(command.IMAPPort))),
			)
		}
	case domain.ProviderCalDAV:
		endpointOverride = command.CalDAVEndpoint
	case domain.ProviderGoogle:
		endpointOverride = command.APIBase
		if mailProvider == "none" && command.CalendarAPIBase != "" {
			endpointOverride = command.CalendarAPIBase
		}
		if endpointOverride == "" {
			endpointOverride = "https://www.googleapis.com"
		}
	case domain.ProviderMicrosoftGraph:
		endpointOverride = command.APIBase
		if mailProvider == "none" && command.CalendarAPIBase != "" {
			endpointOverride = command.CalendarAPIBase
		}
		if endpointOverride == "" {
			profile, profileErr := microsoftcloud.Resolve(
				microsoftcloud.ID(command.MicrosoftCloud),
			)
			if profileErr != nil {
				return profileErr
			}
			endpointOverride = profile.APIBase
		}
	case "",
		domain.ProviderMicrosoftOWA,
		domain.ProviderPOP3:
		endpointOverride = command.Origin
	case domain.ProviderGoogleWeb:
		endpointOverride = command.Origin
		if endpointOverride == "" {
			endpointOverride = "https://mail.google.com"
		}
	case domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		return errors.New("a task provider cannot select a mail or calendar discovery route")
	default:
		endpointOverride = command.Origin
	}
	selected, err := selectAccountCandidate(
		result,
		selectionProvider,
		endpointOverride,
	)
	if err != nil {
		return err
	}
	alias := command.Alias
	if alias == "" {
		alias = command.Address[:strings.LastIndexByte(command.Address, '@')]
	}
	routing := *command
	routing.Provider = mailProvider
	routing.MailProvider = ""
	mail, calendar, endpoint, err := routing.routes(selected, result)
	if err != nil {
		return err
	}
	tasks, err := command.taskRoute()
	if err != nil {
		return err
	}
	account, err := accounts.Add(app.context, application.AccountAddInput{
		Alias: alias, Address: result.Address, Mail: mail, Calendar: calendar,
		Tasks: tasks, Default: command.Default,
	})
	if err != nil {
		return err
	}
	output := accountAddResult{Selected: &selected, Account: account}
	if command.JSON {
		return writeJSON(app.stdout, output)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-14s %s\n  %-14s %d/100\n  %-14s %s\n  %-14s %s\n\n%s  %s\n   %s\n",
		view.info(),
		view.strong("Selected provider route"),
		"Provider", selected.Provider,
		"Confidence", selected.Confidence,
		"Authentication", selected.Authentication,
		"Endpoint", sanitizeCell(endpoint, 2048),
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; authentication has not started"),
		view.command("Next: corr auth login --account "+shellSingleQuote(account.Alias)),
	)
	return err
}

func (command *accountAddCommand) addTaskOnly(
	app *runtime,
	accounts *application.AccountService,
) error {
	alias := command.Alias
	if alias == "" {
		if command.Address == "" {
			return errors.New("task-only account without an address requires --alias")
		}
		separator := strings.LastIndexByte(command.Address, '@')
		if separator < 1 {
			return errors.New("task-only account address must be a bare email address")
		}
		alias = command.Address[:separator]
	}
	provider := domain.ProviderID(command.TaskProvider)
	tasks, err := command.taskRoute()
	if err != nil {
		return err
	}
	account, err := accounts.Add(app.context, application.AccountAddInput{
		Alias: alias, Address: command.Address,
		Tasks:   tasks,
		Default: command.Default,
	})
	if err != nil {
		return err
	}
	output := accountAddResult{SelectedTaskProvider: provider, Account: account}
	if command.JSON {
		return writeJSON(app.stdout, output)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-14s %s\n  %-14s %s\n\n%s  %s\n   %s\n",
		view.info(), view.strong("Selected task route"),
		"Provider", provider,
		"Discovery", "not needed for an explicit task route",
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; authentication has not started"),
		view.command("Next: corr auth login --account "+shellSingleQuote(account.Alias)),
	)
	return err
}

func (command accountAddCommand) taskRoute() (*application.AccountTaskRouteInput, error) {
	if command.TaskProvider == "" || command.TaskProvider == "none" {
		return nil, nil
	}
	provider := domain.ProviderID(command.TaskProvider)
	result := &application.AccountTaskRouteInput{Provider: provider}
	if provider == domain.ProviderCalDAV {
		endpoint := command.TaskCalDAVEndpoint
		if endpoint == "" {
			endpoint = command.CalDAVEndpoint
		}
		username := command.TaskUsername
		if username == "" {
			username = command.CalendarUsername
		}
		if username == "" {
			username = command.Username
		}
		if username == "" {
			username = command.Address
		}
		backend := command.TaskCredentialBackend
		if backend == "" {
			backend = command.CredentialBackend
		}
		if endpoint == "" || username == "" || command.TaskCredentialKey == "" ||
			!command.ApproveTaskCredential {
			return nil, errors.New(
				"caldav tasks require an HTTPS endpoint, login identity, independent external credential handle, and --approve-task-credential",
			)
		}
		result.CalDAV = &application.AccountCalDAVTaskInput{
			Endpoint: endpoint, TaskListPath: command.TaskListPath,
			Username: username,
			Credential: application.AccountCredentialInput{
				Backend: backend, Key: command.TaskCredentialKey, Consent: true,
			},
		}
		return result, nil
	}
	if provider != domain.ProviderMicrosoftGraph && provider != domain.ProviderTodoist &&
		provider != domain.ProviderGoogleTasks && provider != domain.ProviderTickTick {
		return result, nil
	}
	clientID := command.TaskOAuthClientID
	if clientID == "" {
		clientID = command.OAuthClientID
	}
	redirectURI := command.TaskOAuthRedirectURI
	if redirectURI == "" {
		redirectURI = command.OAuthRedirectURI
	}
	authorizationKey := command.TaskAuthorizationKey
	if authorizationKey == "" {
		authorizationKey = command.AuthorizationKey
	}
	if clientID == "" || redirectURI == "" || authorizationKey == "" || !command.ApproveTaskOAuth {
		if provider == domain.ProviderTodoist {
			return nil, errors.New(
				"todoist tasks require a BYO public-client ID, loopback redirect, keyring handle, and --approve-task-oauth",
			)
		}
		if provider == domain.ProviderGoogleTasks {
			return nil, errors.New(
				"google-tasks requires a BYO desktop client ID, loopback redirect, independent keyring handle, and --approve-task-oauth",
			)
		}
		if provider == domain.ProviderTickTick {
			return nil, errors.New(
				"ticktick tasks require a BYO confidential-client ID, fixed loopback redirect, independent keyring grant handle, and --approve-task-oauth",
			)
		}
		return nil, errors.New(
			"microsoft-graph tasks require task OAuth flags or shared public-client defaults, plus --approve-task-oauth",
		)
	}
	if provider == domain.ProviderTodoist {
		apiBase := command.TaskAPIBase
		if apiBase == "" {
			apiBase = "https://api.todoist.com/api/v1"
		}
		result.Todoist = &application.AccountTodoistTaskInput{
			ReadOnly: command.TaskReadOnly,
			OAuth: application.AccountOAuthInput{
				APIBase: apiBase, ClientID: clientID, RedirectURI: redirectURI,
				Authorization: application.AccountCredentialInput{
					Backend: "os-keyring", Key: authorizationKey, Consent: true,
				},
			},
		}
		return result, nil
	}
	if provider == domain.ProviderGoogleTasks {
		apiBase := command.TaskAPIBase
		if apiBase == "" {
			apiBase = "https://tasks.googleapis.com"
		}
		result.GoogleTasks = &application.AccountGoogleTaskInput{
			ReadOnly: command.TaskReadOnly,
			OAuth: application.AccountOAuthInput{
				APIBase: apiBase, ClientID: clientID, RedirectURI: redirectURI,
				Authorization: application.AccountCredentialInput{
					Backend: "os-keyring", Key: authorizationKey, Consent: true,
				},
			},
		}
		return result, nil
	}
	if provider == domain.ProviderTickTick {
		secretBackend := command.TaskOAuthSecretBackend
		if secretBackend == "" {
			secretBackend = command.CredentialBackend
		}
		if command.TaskOAuthSecretKey == "" || !command.ApproveTaskOAuthSecret {
			return nil, errors.New(
				"ticktick tasks require an external client-secret handle and --approve-task-oauth-secret",
			)
		}
		apiBase := command.TaskAPIBase
		if apiBase == "" {
			apiBase = "https://api.ticktick.com"
		}
		result.TickTick = &application.AccountTickTickTaskInput{
			ReadOnly: command.TaskReadOnly,
			OAuth: application.AccountTickTickOAuthInput{
				APIBase: apiBase, ClientID: clientID, RedirectURI: redirectURI,
				Authorization: application.AccountCredentialInput{
					Backend: "os-keyring", Key: authorizationKey, Consent: true,
				},
				ClientSecret: application.AccountCredentialInput{
					Backend: secretBackend, Key: command.TaskOAuthSecretKey, Consent: true,
				},
			},
		}
		return result, nil
	}
	cloud, err := microsoftcloud.Resolve(microsoftcloud.ID(command.MicrosoftCloud))
	if err != nil {
		return nil, err
	}
	if !cloud.TasksAvailable {
		return nil, errors.New("the Microsoft To Do API is unavailable in Microsoft Graph China operated by 21Vianet")
	}
	apiBase := command.TaskAPIBase
	if apiBase == "" {
		apiBase = cloud.APIBase
	}
	result.MicrosoftGraph = &application.AccountMicrosoftTaskInput{
		ReadOnly: command.TaskReadOnly,
		OAuth: application.AccountOAuthInput{
			APIBase: apiBase, MicrosoftCloud: cloud.ID,
			ClientID: clientID, RedirectURI: redirectURI,
			Authorization: application.AccountCredentialInput{
				Backend: "os-keyring", Key: authorizationKey, Consent: true,
			},
		},
	}
	return result, nil
}

func (command accountAddCommand) effectiveMailProvider() (string, error) {
	if command.Provider != "" && command.MailProvider != "" &&
		command.Provider != command.MailProvider {
		return "", errors.New(
			"--provider and --mail-provider must select the same provider",
		)
	}
	if command.MailProvider != "" {
		return command.MailProvider, nil
	}
	return command.Provider, nil
}

func (command accountAddCommand) routes(
	selected application.ProviderCandidate,
	discovery application.AccountDiscoveryResult,
) (*application.AccountMailRouteInput, *application.AccountCalendarRouteInput, string, error) {
	address := discovery.Address
	switch selected.Provider {
	case domain.ProviderMicrosoftOWA:
		origin := command.Origin
		if origin == "" {
			origin = candidateEndpoint(selected, "origin")
		}
		if origin == "" {
			return nil, nil, "", errors.New(
				"selected provider has no origin; pass --origin with an explicit HTTPS origin",
			)
		}
		web := &application.AccountOutlookWebInput{
			Origin: origin, Mailbox: command.Mailbox,
		}
		return command.finishRoutes(&application.AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA, OutlookWeb: web,
		}, &application.AccountCalendarRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &application.AccountOutlookWebInput{
				Origin: origin, Mailbox: command.Mailbox,
			},
		}, origin, selected, discovery)
	case domain.ProviderJMAP:
		sessionURL := command.SessionURL
		if sessionURL == "" {
			sessionURL = candidateHTTPSEndpoint(selected, "jmap")
		}
		if sessionURL == "" {
			return nil, nil, "", errors.New(
				"selected JMAP provider needs --session-url with an explicit HTTPS session resource",
			)
		}
		username := command.Username
		if username == "" {
			username = address
		}
		if command.CredentialBackend == "" || command.CredentialKey == "" ||
			!command.ApproveCredential {
			return nil, nil, "", errors.New(
				"JMAP requires --credential-backend, --credential-key, and --approve-credential",
			)
		}
		return command.finishRoutes(&application.AccountMailRouteInput{
			Provider: domain.ProviderJMAP,
			JMAP: &application.AccountJMAPInput{
				SessionURL: sessionURL,
				Username:   username,
				Credential: application.AccountCredentialInput{
					Backend: command.CredentialBackend,
					Key:     command.CredentialKey,
					Consent: command.ApproveCredential,
				},
			},
		}, nil, sessionURL, selected, discovery)
	case domain.ProviderIMAPSMTP:
		imapEndpoint, err := accountTLSEndpoint(
			command.IMAPHost,
			command.IMAPPort,
			command.IMAPTLS,
			candidateEndpoint(selected, "imap"),
			"implicit",
		)
		if err != nil {
			return nil, nil, "", fmt.Errorf("IMAP endpoint: %w", err)
		}
		smtpEndpoint, err := accountTLSEndpoint(
			command.SMTPHost,
			command.SMTPPort,
			command.SMTPTLS,
			candidateEndpoint(selected, "smtp"),
			"starttls",
		)
		if err != nil {
			return nil, nil, "", fmt.Errorf("SMTP endpoint: %w", err)
		}
		username := command.Username
		if username == "" {
			username = address
		}
		if command.CredentialKey == "" || !command.ApproveCredential {
			return nil, nil, "", errors.New(
				"IMAP/SMTP requires --credential-key and --approve-credential",
			)
		}
		return command.finishRoutes(&application.AccountMailRouteInput{
			Provider: domain.ProviderIMAPSMTP,
			IMAPSMTP: &application.AccountIMAPSMTPInput{
				IMAP: imapEndpoint, SMTP: smtpEndpoint,
				Username: username, Mailbox: command.Mailbox,
				Credential: application.AccountCredentialInput{
					Backend: command.CredentialBackend,
					Key:     command.CredentialKey,
					Consent: command.ApproveCredential,
				},
			},
		}, nil, fmt.Sprintf(
			"IMAP %s:%d · SMTP %s:%d",
			imapEndpoint.Host,
			imapEndpoint.Port,
			smtpEndpoint.Host,
			smtpEndpoint.Port,
		), selected, discovery)
	case domain.ProviderCalDAV:
		return command.finishRoutes(nil, nil, "", selected, discovery)
	case domain.ProviderGoogle:
		if !rollout.GoogleOAuthApproved {
			return nil, nil, "", googleOAuthPendingError("A Google route was selected.")
		}
		oauth, err := command.oauthRoute(
			domain.ProviderGoogle,
			selected,
			"https://www.googleapis.com",
			command.Provider == "none",
		)
		if err != nil {
			return nil, nil, "", err
		}
		username := command.Username
		if username == "" {
			username = address
		}
		return command.finishRoutes(
			&application.AccountMailRouteInput{
				Provider: domain.ProviderGoogle,
				Google: &application.AccountGoogleMailInput{
					Username: username, Mailbox: command.Mailbox,
					ClientID: oauth.ClientID, RedirectURI: oauth.RedirectURI,
					Authorization: oauth.Authorization,
				},
			},
			&application.AccountCalendarRouteInput{
				Provider: domain.ProviderGoogle,
				Google: &application.AccountOAuthInput{
					APIBase: oauth.APIBase, ClientID: oauth.ClientID,
					RedirectURI: oauth.RedirectURI, Authorization: oauth.Authorization,
				},
			},
			"IMAP imap.gmail.com:993 · SMTP smtp.gmail.com:587 · Calendar "+
				oauth.APIBase,
			selected,
			discovery,
		)
	case domain.ProviderMicrosoftGraph:
		oauth, err := command.oauthRoute(
			domain.ProviderMicrosoftGraph,
			selected,
			"https://graph.microsoft.com/v1.0",
			command.Provider == "none",
		)
		if err != nil {
			return nil, nil, "", err
		}
		return command.finishRoutes(
			&application.AccountMailRouteInput{
				Provider: domain.ProviderMicrosoftGraph, MicrosoftGraph: oauth,
			},
			&application.AccountCalendarRouteInput{
				Provider: domain.ProviderMicrosoftGraph,
				MicrosoftGraph: &application.AccountOAuthInput{
					APIBase: oauth.APIBase, ClientID: oauth.ClientID,
					RedirectURI: oauth.RedirectURI, Authorization: oauth.Authorization,
				},
			},
			oauth.APIBase,
			selected,
			discovery,
		)
	case domain.ProviderGoogleWeb:
		return nil, nil, "", fmt.Errorf(
			"provider %q is not available in this build",
			selected.Provider,
		)
	case domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		return nil, nil, "", fmt.Errorf(
			"task provider %q cannot supply a discovered mail or calendar route",
			selected.Provider,
		)
	case domain.ProviderPOP3:
		return nil, nil, "", fmt.Errorf(
			"provider %q has no account route builder",
			selected.Provider,
		)
	default:
		return nil, nil, "", fmt.Errorf("unknown provider %q", selected.Provider)
	}
}

func (command accountAddCommand) oauthRoute(
	provider domain.ProviderID,
	selected application.ProviderCandidate,
	defaultBase string,
	calendar bool,
) (*application.AccountOAuthInput, error) {
	apiBase := command.APIBase
	explicitAPIBase := apiBase != ""
	clientID := command.OAuthClientID
	redirectURI := command.OAuthRedirectURI
	authorizationKey := command.AuthorizationKey
	approved := command.ApproveOAuth
	cloudID := microsoftcloud.ID("")
	if calendar {
		if command.CalendarAPIBase != "" {
			apiBase = command.CalendarAPIBase
			explicitAPIBase = true
		}
		if command.CalendarOAuthClientID != "" {
			clientID = command.CalendarOAuthClientID
		}
		if command.CalendarOAuthRedirectURI != "" {
			redirectURI = command.CalendarOAuthRedirectURI
		}
		if command.CalendarAuthorizationKey != "" {
			authorizationKey = command.CalendarAuthorizationKey
		}
		if command.ApproveCalendarOAuth {
			approved = true
		}
	}
	if apiBase == "" {
		apiBase = candidateHTTPSEndpoint(selected, "api")
	}
	if provider == domain.ProviderMicrosoftGraph {
		profile, err := microsoftcloud.Resolve(microsoftcloud.ID(command.MicrosoftCloud))
		if err != nil {
			return nil, err
		}
		cloudID = profile.ID
		if !explicitAPIBase &&
			(apiBase == "" || apiBase == "https://graph.microsoft.com/v1.0") {
			apiBase = profile.APIBase
		}
	}
	if apiBase == "" {
		apiBase = defaultBase
	}
	if clientID == "" ||
		redirectURI == "" ||
		authorizationKey == "" ||
		!approved {
		flags := "--oauth-client-id, --oauth-redirect-uri, " +
			"--authorization-key, and --approve-oauth"
		if calendar {
			flags = "calendar OAuth flags or their shared OAuth defaults, " +
				"plus explicit OAuth approval"
		}
		return nil, fmt.Errorf("%s requires %s", provider, flags)
	}
	return &application.AccountOAuthInput{
		APIBase: apiBase, MicrosoftCloud: cloudID, ClientID: clientID,
		RedirectURI: redirectURI,
		Authorization: application.AccountCredentialInput{
			Backend: "os-keyring", Key: authorizationKey, Consent: true,
		},
	}, nil
}

func (command accountAddCommand) finishRoutes(
	mail *application.AccountMailRouteInput,
	calendar *application.AccountCalendarRouteInput,
	endpoint string,
	selected application.ProviderCandidate,
	discovery application.AccountDiscoveryResult,
) (*application.AccountMailRouteInput, *application.AccountCalendarRouteInput, string, error) {
	if command.Provider == "none" {
		mail = nil
	}
	provider := command.CalendarProvider
	if provider == "" && selected.Provider == domain.ProviderCalDAV {
		provider = string(domain.ProviderCalDAV)
	}
	switch domain.ProviderID(provider) {
	case "":
		return mail, calendar, endpoint, nil
	case "none":
		if mail == nil {
			return nil, nil, "", errors.New("calendar-only selection cannot use --calendar-provider none")
		}
		return mail, nil, endpoint, nil
	case domain.ProviderMicrosoftOWA:
		if selected.Provider != domain.ProviderMicrosoftOWA || calendar == nil {
			return nil, nil, "", errors.New(
				"microsoft-owa calendar requires a selected Outlook Web route",
			)
		}
		return mail, calendar, endpoint, nil
	case domain.ProviderCalDAV:
	case domain.ProviderGoogleWeb:
		return nil, nil, "", fmt.Errorf(
			"calendar provider %q is not available in this build",
			provider,
		)
	case domain.ProviderGoogle, domain.ProviderMicrosoftGraph:
		if provider == string(domain.ProviderGoogle) &&
			!rollout.GoogleOAuthApproved {
			return nil, nil, "", googleOAuthPendingError(
				"A Google Calendar route was selected.",
			)
		}
		if calendar == nil ||
			calendar.Provider != domain.ProviderID(provider) ||
			command.hasDistinctCalendarOAuth() {
			candidate := candidateForProvider(discovery, domain.ProviderID(provider))
			defaultBase := "https://www.googleapis.com"
			if provider == string(domain.ProviderMicrosoftGraph) {
				defaultBase = "https://graph.microsoft.com/v1.0"
			}
			oauth, err := command.oauthRoute(
				domain.ProviderID(provider),
				candidate,
				defaultBase,
				true,
			)
			if err != nil {
				return nil, nil, "", err
			}
			calendar = &application.AccountCalendarRouteInput{
				Provider: domain.ProviderID(provider),
			}
			if provider == string(domain.ProviderGoogle) {
				calendar.Google = oauth
			} else {
				calendar.MicrosoftGraph = oauth
			}
			if endpoint == "" {
				endpoint = oauth.APIBase
			} else {
				endpoint += " · " + provider + " " + oauth.APIBase
			}
		}
		return mail, calendar, endpoint, nil
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
		return nil, nil, "", fmt.Errorf(
			"provider %q cannot supply a configured calendar route",
			provider,
		)
	default:
		return nil, nil, "", fmt.Errorf(
			"calendar provider %q is not available in this build",
			provider,
		)
	}

	calendarEndpoint := command.CalDAVEndpoint
	if calendarEndpoint == "" {
		for _, candidate := range discovery.Candidates {
			if candidate.Provider == domain.ProviderCalDAV && candidate.Available {
				calendarEndpoint = candidateHTTPSEndpoint(candidate, "caldav")
				if calendarEndpoint != "" {
					break
				}
			}
		}
	}
	if calendarEndpoint == "" && selected.Provider == domain.ProviderCalDAV {
		calendarEndpoint = candidateHTTPSEndpoint(selected, "caldav")
	}
	if calendarEndpoint == "" {
		return nil, nil, "", errors.New(
			"CalDAV requires --caldav-endpoint with an explicit HTTPS discovery endpoint",
		)
	}
	username := command.CalendarUsername
	if username == "" {
		username = command.Username
	}
	if username == "" {
		username = discovery.Address
	}
	backend := command.CalendarCredentialBackend
	if backend == "" {
		backend = command.CredentialBackend
	}
	key := command.CalendarCredentialKey
	consent := command.ApproveCalendarCredential
	if key == "" {
		key = command.CredentialKey
		consent = command.ApproveCredential
	}
	if backend == "" || key == "" || !consent {
		return nil, nil, "", errors.New(
			"CalDAV requires an external credential key and explicit credential approval",
		)
	}
	calendar = &application.AccountCalendarRouteInput{
		Provider: domain.ProviderCalDAV,
		CalDAV: &application.AccountCalDAVInput{
			Endpoint: calendarEndpoint, CalendarPath: command.CalendarPath,
			Username: username,
			Credential: application.AccountCredentialInput{
				Backend: backend, Key: key, Consent: consent,
			},
		},
	}
	if endpoint == "" {
		endpoint = calendarEndpoint
	} else {
		endpoint += " · CalDAV " + calendarEndpoint
	}
	return mail, calendar, endpoint, nil
}

func candidateForProvider(
	discovery application.AccountDiscoveryResult,
	provider domain.ProviderID,
) application.ProviderCandidate {
	for _, candidate := range discovery.Candidates {
		if candidate.Provider == provider {
			return candidate
		}
	}
	return application.ProviderCandidate{Provider: provider}
}

func (command accountAddCommand) hasDistinctCalendarOAuth() bool {
	return command.CalendarAPIBase != "" ||
		command.CalendarOAuthClientID != "" ||
		command.CalendarOAuthRedirectURI != "" ||
		command.CalendarAuthorizationKey != "" ||
		command.ApproveCalendarOAuth
}

func (command *accountRenameCommand) Run(app *runtime) error {
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Rename(app.context, application.AccountRenameInput{
		Account: command.Account, NewAlias: command.Alias,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, account)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Account renamed to "+sanitizeCell(account.Alias, 64)),
		view.muted(string(account.ID)+" remains unchanged"),
	)
	return err
}

func (command *accountRemoveCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New(
			"account removal deletes Corresync-owned local state; review the account and rerun with --approve",
		)
	}
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Remove(app.context, application.AccountRemoveInput{
		Account: command.Account, ReplacementDefault: command.NewDefault,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"removed": true, "account": account})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n",
		view.success(),
		view.strong("Removed account "+sanitizeCell(account.Alias, 64)+" and its local state"),
	)
	return err
}

func selectAccountCandidate(
	result application.AccountDiscoveryResult,
	providerOverride string,
	originOverride string,
) (application.ProviderCandidate, error) {
	if providerOverride != "" {
		provider := domain.ProviderID(providerOverride)
		if err := provider.Validate(); err != nil {
			return application.ProviderCandidate{}, err
		}
		if provider == domain.ProviderGoogleWeb {
			return application.ProviderCandidate{}, fmt.Errorf(
				"provider %q is not available in this build",
				provider,
			)
		}
		if provider == domain.ProviderGoogle && !rollout.GoogleOAuthApproved {
			return application.ProviderCandidate{}, googleOAuthPendingError(
				"A Google route was selected.",
			)
		}
		for _, candidate := range result.Candidates {
			if candidate.Provider == provider {
				if !candidate.Available {
					return application.ProviderCandidate{}, fmt.Errorf(
						"provider %q was discovered but is not available in this build",
						provider,
					)
				}
				return candidate, nil
			}
		}
		if provider != domain.ProviderMicrosoftOWA &&
			provider != domain.ProviderJMAP &&
			provider != domain.ProviderIMAPSMTP &&
			provider != domain.ProviderCalDAV &&
			provider != domain.ProviderGoogle &&
			provider != domain.ProviderMicrosoftGraph {
			return application.ProviderCandidate{}, fmt.Errorf(
				"provider %q is not available in this build",
				provider,
			)
		}
		if originOverride == "" {
			return application.ProviderCandidate{}, errors.New(
				"manual provider selection requires an explicit provider endpoint",
			)
		}
		if _, err := url.ParseRequestURI(originOverride); err != nil {
			return application.ProviderCandidate{}, errors.New(
				"manual provider endpoint is not a valid URI",
			)
		}
		authentication := application.DiscoveryBrowserFirstParty
		kind := "origin"
		standards := map[domain.ProviderID]string{
			domain.ProviderJMAP:     "jmap",
			domain.ProviderIMAPSMTP: "imap",
			domain.ProviderCalDAV:   "caldav",
		}
		if standardsKind, isStandards := standards[provider]; isStandards {
			authentication, kind = application.DiscoveryExternalCredential, standardsKind
		} else if provider == domain.ProviderGoogle ||
			provider == domain.ProviderMicrosoftGraph {
			authentication, kind = application.DiscoveryExplicitOAuth, "api"
		}
		return application.ProviderCandidate{
			Provider: provider, Confidence: 0,
			Authentication:            authentication,
			RequiresExplicitSelection: true,
			Available:                 true,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: kind, Value: originOverride},
			},
			Evidence: []application.DiscoveryEvidence{
				{Source: "manual_override", Detail: result.Domain},
			},
		}, nil
	}
	for _, candidate := range result.Candidates {
		if candidate.Available && !candidate.RequiresExplicitSelection {
			return candidate, nil
		}
	}
	for _, candidate := range result.Candidates {
		if candidate.Provider == domain.ProviderGoogle &&
			!rollout.GoogleOAuthApproved {
			return application.ProviderCandidate{}, googleOAuthPendingError(
				"Gmail was found.",
			)
		}
	}
	return application.ProviderCandidate{}, errors.New(
		"no automatically selectable provider is available; inspect `corr account discover` and pass --provider with an explicit endpoint",
	)
}

func accountTLSEndpoint(
	host string,
	port uint16,
	mode string,
	discovered string,
	discoveredMode string,
) (application.AccountTLSEndpointInput, error) {
	if host == "" && port == 0 && discovered != "" {
		discoveredHost, discoveredPort, err := net.SplitHostPort(discovered)
		if err != nil {
			return application.AccountTLSEndpointInput{}, errors.New(
				"discovered endpoint is not host:port; pass explicit host and port",
			)
		}
		parsedPort, err := strconv.ParseUint(discoveredPort, 10, 16)
		if err != nil || parsedPort == 0 {
			return application.AccountTLSEndpointInput{}, errors.New(
				"discovered endpoint port is invalid",
			)
		}
		host, port, mode = discoveredHost, uint16(parsedPort), discoveredMode
	}
	if host == "" || port == 0 {
		return application.AccountTLSEndpointInput{}, errors.New(
			"host and port are required",
		)
	}
	return application.AccountTLSEndpointInput{
		Host: host, Port: port, Mode: mode,
	}, nil
}

func candidateEndpoint(candidate application.ProviderCandidate, kind string) string {
	for _, endpoint := range candidate.Endpoints {
		if endpoint.Kind == kind {
			return endpoint.Value
		}
	}
	return ""
}

func candidateHTTPSEndpoint(
	candidate application.ProviderCandidate,
	kind string,
) string {
	for _, endpoint := range candidate.Endpoints {
		if endpoint.Kind != kind {
			continue
		}
		parsed, err := url.Parse(endpoint.Value)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
			parsed.Fragment != "" || strings.HasSuffix(parsed.Host, ":") {
			continue
		}
		if port := parsed.Port(); port != "" {
			value, err := strconv.ParseUint(port, 10, 16)
			if err != nil || value == 0 {
				continue
			}
		}
		return parsed.String()
	}
	return ""
}

func accountRouteLabel(account application.AccountView) string {
	mail, calendar, tasks := "–", "–", "–"
	if account.Mail != nil {
		mail = string(account.Mail.Provider)
	}
	if account.Calendar != nil {
		calendar = string(account.Calendar.Provider)
	}
	if account.Tasks != nil {
		tasks = string(account.Tasks.Provider)
	}
	if mail == calendar && calendar == tasks {
		return mail
	}
	return "mail:" + mail + " · cal:" + calendar + " · tasks:" + tasks
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func writeDiscoveryResult(app *runtime, result application.AccountDiscoveryResult) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n   %s\n",
		view.info(),
		view.strong("Provider candidates"),
		view.muted(result.Address+" · evidence only; no authentication performed"),
	); err != nil {
		return err
	}
	if len(result.Candidates) == 0 {
		_, err := view.printf(
			"\n  No candidate was inferred. Manual --provider and endpoint flags remain available.\n"+
				"  %s\n",
			view.command("Next: corr account add "+shellSingleQuote(result.Address)+" --help"),
		)
		return err
	}
	for _, candidate := range result.Candidates {
		availability := "planned"
		if candidate.Available {
			availability = "available"
		}
		if _, err := view.printf(
			"\n  %-18s %3d/100 · %s · %s\n",
			candidate.Provider,
			candidate.Confidence,
			candidate.Authentication,
			availability,
		); err != nil {
			return err
		}
		for _, evidence := range candidate.Evidence {
			if _, err := view.printf(
				"    %-20s %s\n",
				evidence.Source,
				sanitizeCell(evidence.Detail, 512),
			); err != nil {
				return err
			}
		}
		for _, endpoint := range candidate.Endpoints {
			if _, err := view.printf(
				"    %-20s %s\n",
				endpoint.Kind,
				sanitizeCell(endpoint.Value, 2048),
			); err != nil {
				return err
			}
		}
	}
	_, err := view.printf(
		"\n  Discovery changed nothing and opened no sign-in page.\n"+
			"  %s\n",
		view.command("Next: corr account add "+shellSingleQuote(result.Address)+" --help"),
	)
	return err
}
