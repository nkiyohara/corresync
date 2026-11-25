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
)

type accountCommand struct {
	Discover accountDiscoverCommand `cmd:"" help:"Show how Corresync could connect to an address, without signing in."`
	List     accountListCommand     `cmd:"" help:"List your accounts."`
	Show     accountShowCommand     `cmd:"" help:"Show how one account connects."`
	Add      accountAddCommand      `cmd:"" help:"Add an account with the connection you choose, without signing in."`
	Rename   accountRenameCommand   `cmd:"" help:"Rename an account; its sign-in and data stay."`
	Remove   accountRemoveCommand   `cmd:"" help:"Remove an account and what Corresync keeps for it on this computer."`
}

type accountDiscoverCommand struct {
	Address string `arg:"" help:"Email address to look up. Nothing signs in."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountListCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type accountShowCommand struct {
	Account string `arg:"" help:"Account name or ID."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountAddCommand struct {
	Address                          string `arg:"" optional:"" help:"Email address; task-only accounts may leave it out."`
	Alias                            string `help:"Name for the account; defaults to the part of the address before @."`
	Default                          bool   `help:"Make this the default account."`
	JSON                             bool   `help:"Write machine-readable JSON."`
	Provider                         string `group:"Choose the connection:" help:"Older name for --mail-provider."`
	MailProvider                     string `group:"Choose the connection:" help:"Mail connection: microsoft-owa, imap-smtp, jmap, google, microsoft-graph, or none."`
	CalendarProvider                 string `group:"Choose the connection:" help:"Calendar connection: microsoft-owa, caldav, google, microsoft-graph, or none."`
	TaskProvider                     string `group:"Choose the connection:" help:"Task service to add, or none. See the Tasks flags below."`
	Mailbox                          string `group:"Choose the connection:" help:"Other mailbox to read or send as, if your provider allows it."`
	Origin                           string `group:"Outlook Web:" help:"Outlook Web address, such as https://outlook.cloud.microsoft."`
	SessionURL                       string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"JMAP session URL (https://)."`
	Username                         string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Mail sign-in name; defaults to the address. Google requires the address."`
	CredentialBackend                string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" default:"os-keyring" enum:"os-keyring,helper" help:"Where the password is saved: os-keyring or a credential helper."`
	CredentialKey                    string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Name of the saved password entry."`
	ApproveCredential                bool   `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Allow this account to use that saved password."`
	IMAPHost                         string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"IMAP server name."`
	IMAPPort                         uint16 `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"IMAP server port."`
	IMAPTLS                          string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" name:"imap-tls" default:"implicit" enum:"implicit,starttls" help:"IMAP encryption: implicit or starttls."`
	SMTPHost                         string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"SMTP server name for sending."`
	SMTPPort                         uint16 `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"SMTP server port."`
	SMTPTLS                          string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" name:"smtp-tls" default:"starttls" enum:"implicit,starttls" help:"SMTP encryption: implicit or starttls."`
	CalDAVEndpoint                   string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" name:"caldav-endpoint" help:"CalDAV server URL (https://)."`
	CalendarPath                     string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"CalDAV calendar path, if the server needs one."`
	CalendarUsername                 string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"CalDAV sign-in name; defaults to --username or the address."`
	CalendarCredentialBackend        string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Where the CalDAV password is saved; defaults to --credential-backend."`
	CalendarCredentialKey            string `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Name of the saved CalDAV password; defaults to --credential-key."`
	ApproveCalendarCredential        bool   `group:"IMAP/SMTP, JMAP, and CalDAV (password saved outside Corresync):" help:"Allow this account to use a separate CalDAV password."`
	MicrosoftCloud                   string `group:"Microsoft Graph and Google (your own OAuth client):" default:"global" enum:"global,gcc-high,dod,china" help:"Microsoft cloud for every Microsoft service on this account."`
	APIBase                          string `group:"Microsoft Graph and Google (your own OAuth client):" help:"Different API address for Google Calendar or Microsoft Graph (https://)."`
	OAuthClientID                    string `group:"Microsoft Graph and Google (your own OAuth client):" name:"oauth-client-id" help:"Client ID of your own app registration or OAuth client."`
	OAuthRedirectURI                 string `group:"Microsoft Graph and Google (your own OAuth client):" name:"oauth-redirect-uri" help:"Redirect URI registered for that client, on http://127.0.0.1."`
	AuthorizationKey                 string `group:"Microsoft Graph and Google (your own OAuth client):" help:"Keyring name to save the sign-in under."`
	ApproveOAuth                     bool   `group:"Microsoft Graph and Google (your own OAuth client):" name:"approve-oauth" help:"Allow sign-in with that client when no saved sign-in exists."`
	OAuthClientSecretBackend         string `group:"Microsoft Graph and Google (your own OAuth client):" name:"oauth-client-secret-backend" default:"os-keyring" enum:"os-keyring,helper" help:"Where the Google client secret is saved."`
	OAuthClientSecretKey             string `group:"Microsoft Graph and Google (your own OAuth client):" name:"oauth-client-secret-key" help:"Name of the saved Google client secret."`
	ApproveOAuthClientSecret         bool   `group:"Microsoft Graph and Google (your own OAuth client):" name:"approve-oauth-client-secret" help:"Allow this account to use the saved Google client secret."`
	CalendarAPIBase                  string `group:"Calendar with its own OAuth sign-in:" name:"calendar-api-base" help:"Different calendar API address (https://)."`
	CalendarOAuthClientID            string `group:"Calendar with its own OAuth sign-in:" name:"calendar-oauth-client-id" help:"Calendar client ID; defaults to --oauth-client-id."`
	CalendarOAuthRedirectURI         string `group:"Calendar with its own OAuth sign-in:" name:"calendar-oauth-redirect-uri" help:"Calendar redirect URI; defaults to --oauth-redirect-uri."`
	CalendarAuthorizationKey         string `group:"Calendar with its own OAuth sign-in:" name:"calendar-authorization-key" help:"Keyring name for the calendar sign-in; defaults to --authorization-key."`
	ApproveCalendarOAuth             bool   `group:"Calendar with its own OAuth sign-in:" name:"approve-calendar-oauth" help:"Allow a separate calendar sign-in."`
	CalendarOAuthClientSecretBackend string `group:"Calendar with its own OAuth sign-in:" name:"calendar-oauth-client-secret-backend" help:"Where the calendar Google client secret is saved (os-keyring or helper); defaults to --oauth-client-secret-backend."`
	CalendarOAuthClientSecretKey     string `group:"Calendar with its own OAuth sign-in:" name:"calendar-oauth-client-secret-key" help:"Name of the saved calendar Google client secret; defaults to --oauth-client-secret-key."`
	ApproveCalendarOAuthClientSecret bool   `group:"Calendar with its own OAuth sign-in:" name:"approve-calendar-oauth-client-secret" help:"Allow a separate calendar Google client secret."`
	TaskAPIBase                      string `group:"Tasks:" name:"task-api-base" help:"Different task API address (https://)."`
	TaskOAuthClientID                string `group:"Tasks:" name:"task-oauth-client-id" help:"Task OAuth client ID; defaults to --oauth-client-id."`
	TaskOAuthRedirectURI             string `group:"Tasks:" name:"task-oauth-redirect-uri" help:"Task redirect URI; defaults to --oauth-redirect-uri."`
	TaskAuthorizationKey             string `group:"Tasks:" name:"task-authorization-key" help:"Keyring name for the task sign-in; defaults to --authorization-key."`
	ApproveTaskOAuth                 bool   `group:"Tasks:" name:"approve-task-oauth" help:"Allow the task sign-in and its task-only access."`
	TaskOAuthSecretBackend           string `group:"Tasks:" name:"task-oauth-secret-backend" help:"Where the task client secret is saved (os-keyring or helper); defaults to --oauth-client-secret-backend."`
	TaskOAuthSecretKey               string `group:"Tasks:" name:"task-oauth-secret-key" help:"Name of the saved task client secret."`
	ApproveTaskOAuthSecret           bool   `group:"Tasks:" name:"approve-task-oauth-secret" help:"Allow this account to use the saved task client secret."`
	TaskReadOnly                     bool   `group:"Tasks:" name:"task-read-only" help:"Ask only for read access to tasks."`
	TaskCalDAVEndpoint               string `group:"Tasks:" name:"task-caldav-endpoint" help:"CalDAV server URL for tasks; defaults to --caldav-endpoint."`
	TaskListPath                     string `group:"Tasks:" name:"task-list-path" help:"CalDAV task list path, if the server needs one."`
	TaskUsername                     string `group:"Tasks:" name:"task-username" help:"CalDAV task sign-in name; defaults to the calendar or mail sign-in."`
	TaskCredentialBackend            string `group:"Tasks:" name:"task-credential-backend" help:"Where the CalDAV task password is saved; defaults to --credential-backend."`
	TaskCredentialKey                string `group:"Tasks:" name:"task-credential-key" help:"Name of the saved CalDAV task password."`
	ApproveTaskCredential            bool   `group:"Tasks:" name:"approve-task-credential" help:"Allow this account to use the saved CalDAV task password."`

	// onboardingOAuthProvider records which guided setup branch populated the
	// shared OAuth fields. It is ephemeral provenance, never a CLI flag or
	// persisted account field.
	onboardingOAuthProvider    domain.ProviderID
	onboardingGoogleClientPath string
}

func (command *accountAddCommand) Help() string {
	return `Most people use ` + "`corr setup`" + `, which asks questions instead. This command is for scripts and for choosing the connection yourself. It never signs in: run ` + "`corr auth login --account NAME`" + ` afterwards.

Without --mail-provider, Corresync uses a connection it found for the address, but only one that is safe to pick for you; Microsoft Graph and Google are never picked automatically. Choose them, or any other connection, with the flags below. Corresync never takes a password or token on the command line; it reads only the name of an entry saved in the keyring or a credential helper.

Examples:

  # Microsoft 365 through Outlook Web (browser sign-in)
  corr account add ana@example.com --alias work \
    --mail-provider microsoft-owa --origin https://outlook.cloud.microsoft

  # IMAP/SMTP mail and CalDAV calendar, password saved as ana-mail
  corr account add ana@example.com --alias home \
    --mail-provider imap-smtp --calendar-provider caldav \
    --imap-host imap.example.com --imap-port 993 \
    --smtp-host smtp.example.com --smtp-port 587 \
    --caldav-endpoint https://dav.example.com/ \
    --credential-key ana-mail --approve-credential

  # Microsoft Graph with your own app registration
  corr account add ana@outlook.com --alias personal \
    --mail-provider microsoft-graph --calendar-provider microsoft-graph \
    --oauth-client-id YOUR_CLIENT_ID \
    --oauth-redirect-uri http://127.0.0.1:0/callback \
    --authorization-key personal-graph --approve-oauth

  # Gmail and Google Calendar with your own Desktop OAuth client
  corr auth google-client import ~/Downloads/client_secret.json --key ana-google-client
  corr account add ana@gmail.com --alias gmail \
    --mail-provider google --calendar-provider google \
    --oauth-client-id YOUR_CLIENT_ID.apps.googleusercontent.com \
    --oauth-redirect-uri http://127.0.0.1:0 \
    --authorization-key ana-google --approve-oauth \
    --oauth-client-secret-key ana-google-client --approve-oauth-client-secret

Mail and calendar can use different connections. Calendar OAuth flags default to the shared --oauth-* values. Task services (microsoft-graph, google-tasks, todoist, ticktick, caldav) use the Tasks flags and may be added without an email address. More examples: docs/cli.md.`
}

type accountRenameCommand struct {
	Account string `arg:"" help:"Account name or ID."`
	Alias   string `arg:"" help:"New name."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountRemoveCommand struct {
	Account    string `arg:"" help:"Account name or ID."`
	NewDefault string `help:"Account to make the default when you remove the current default."`
	Approve    bool   `help:"Confirm that Corresync should delete what it keeps for this account."`
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
	if account.Messages != nil {
		if _, err := view.printf(
			"\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n",
			"Messages", account.Messages.Provider,
			"Route", account.Messages.Route,
			"Workspace", sanitizeCell(account.Messages.WorkspaceID, 4096),
			"Available", yesNo(account.Messages.Available),
		); err != nil {
			return err
		}
		for _, endpoint := range account.Messages.Endpoints {
			if _, err := view.printf("  %-10s %s\n", endpoint.Kind, sanitizeCell(endpoint.Value, 2048)); err != nil {
				return err
			}
		}
		if account.Messages.Credential != nil {
			if _, err := view.printf(
				"  %-10s %s · consent %s\n", "Credential",
				account.Messages.Credential.Backend, yesNo(account.Messages.Credential.Consented),
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
		"%s  %s\n\n  %-12s %s\n  %-12s %s\n  %-12s %s\n\n%s  %s\n   %s\n",
		view.info(),
		view.strong("Connection"),
		"Provider", providerDisplayName(selected.Provider),
		"Sign-in", onboardingSignInSummary(&selected),
		"Server", sanitizeCell(endpoint, 2048),
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; not signed in yet"),
		view.nextStep(shellCommand("corr", "auth", "login", "--account", account.Alias)),
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
		"%s  %s\n\n  %-12s %s\n\n%s  %s\n   %s\n",
		view.info(), view.strong("Task connection"),
		"Provider", providerDisplayName(provider),
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; not signed in yet"),
		view.nextStep(shellCommand("corr", "auth", "login", "--account", account.Alias)),
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
		distinctClientCredential := command.TaskOAuthSecretBackend != "" ||
			command.TaskOAuthSecretKey != ""
		secretBackend := command.TaskOAuthSecretBackend
		if secretBackend == "" {
			secretBackend = command.OAuthClientSecretBackend
		}
		if secretBackend == "" {
			secretBackend = "os-keyring"
		}
		secretKey := command.TaskOAuthSecretKey
		if secretKey == "" {
			secretKey = command.OAuthClientSecretKey
		}
		secretApproved := command.ApproveTaskOAuthSecret
		if !distinctClientCredential {
			secretApproved = secretApproved || command.ApproveOAuthClientSecret
		}
		if secretBackend == "" || secretKey == "" || !secretApproved {
			return nil, errors.New(
				"google-tasks requires an external Desktop client-credential handle and --approve-task-oauth-secret",
			)
		}
		apiBase := command.TaskAPIBase
		if apiBase == "" {
			apiBase = "https://tasks.googleapis.com"
		}
		result.GoogleTasks = &application.AccountGoogleTaskInput{
			ReadOnly: command.TaskReadOnly,
			OAuth: application.AccountGoogleOAuthInput{
				APIBase: apiBase, ClientID: clientID, RedirectURI: redirectURI,
				Authorization: application.AccountCredentialInput{
					Backend: "os-keyring", Key: authorizationKey, Consent: true,
				},
				ClientSecret: application.AccountCredentialInput{
					Backend: secretBackend, Key: secretKey, Consent: true,
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
		oauth, err := command.googleOAuthRoute(
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
					Authorization: oauth.Authorization, ClientSecret: oauth.ClientSecret,
				},
			},
			&application.AccountCalendarRouteInput{
				Provider: domain.ProviderGoogle,
				Google:   oauth,
			},
			"Gmail API · Google Calendar API "+oauth.APIBase,
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
					APIBase: oauth.APIBase, MicrosoftCloud: oauth.MicrosoftCloud, ClientID: oauth.ClientID,
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
		distinctAuthorization := command.CalendarOAuthClientID != "" ||
			command.CalendarOAuthRedirectURI != "" ||
			command.CalendarAuthorizationKey != ""
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
		if distinctAuthorization && !command.ApproveCalendarOAuth {
			approved = false
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

func (command accountAddCommand) googleOAuthRoute(
	selected application.ProviderCandidate,
	defaultBase string,
	calendar bool,
) (*application.AccountGoogleOAuthInput, error) {
	base, err := command.oauthRoute(
		domain.ProviderGoogle,
		selected,
		defaultBase,
		calendar,
	)
	if err != nil {
		return nil, err
	}
	backend := command.OAuthClientSecretBackend
	if backend == "" {
		backend = "os-keyring"
	}
	key := command.OAuthClientSecretKey
	approved := command.ApproveOAuthClientSecret
	if calendar {
		distinctClientCredential := command.CalendarOAuthClientSecretBackend != "" ||
			command.CalendarOAuthClientSecretKey != ""
		if command.CalendarOAuthClientSecretBackend != "" {
			backend = command.CalendarOAuthClientSecretBackend
		}
		if command.CalendarOAuthClientSecretKey != "" {
			key = command.CalendarOAuthClientSecretKey
		}
		if command.ApproveCalendarOAuthClientSecret {
			approved = true
		}
		if distinctClientCredential && !command.ApproveCalendarOAuthClientSecret {
			approved = false
		}
	}
	if backend == "" || key == "" || !approved {
		return nil, errors.New(
			"google requires an external Desktop client-credential handle and explicit client-credential approval",
		)
	}
	return &application.AccountGoogleOAuthInput{
		APIBase: base.APIBase, ClientID: base.ClientID,
		RedirectURI: base.RedirectURI, Authorization: base.Authorization,
		ClientSecret: application.AccountCredentialInput{
			Backend: backend, Key: key, Consent: true,
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
		if calendar == nil ||
			calendar.Provider != domain.ProviderID(provider) ||
			command.hasDistinctCalendarOAuth() {
			candidate := candidateForProvider(discovery, domain.ProviderID(provider))
			defaultBase := "https://www.googleapis.com"
			if provider == string(domain.ProviderMicrosoftGraph) {
				defaultBase = "https://graph.microsoft.com/v1.0"
			}
			calendar = &application.AccountCalendarRouteInput{
				Provider: domain.ProviderID(provider),
			}
			if provider == string(domain.ProviderGoogle) {
				oauth, err := command.googleOAuthRoute(candidate, defaultBase, true)
				if err != nil {
					return nil, nil, "", err
				}
				calendar.Google = oauth
				if endpoint == "" {
					endpoint = oauth.APIBase
				} else {
					endpoint += " · " + provider + " " + oauth.APIBase
				}
			} else {
				oauth, err := command.oauthRoute(
					domain.ProviderMicrosoftGraph,
					candidate,
					defaultBase,
					true,
				)
				if err != nil {
					return nil, nil, "", err
				}
				calendar.MicrosoftGraph = oauth
				if endpoint == "" {
					endpoint = oauth.APIBase
				} else {
					endpoint += " · " + provider + " " + oauth.APIBase
				}
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
		command.ApproveCalendarOAuth ||
		command.CalendarOAuthClientSecretBackend != "" ||
		command.CalendarOAuthClientSecretKey != "" ||
		command.ApproveCalendarOAuthClientSecret
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
		return withHint(
			errors.New("removing an account deletes what Corresync keeps for it on this computer; add --approve to confirm"),
			shellCommand("corr", "account", "remove", command.Account, "--approve"),
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
	return application.ProviderCandidate{}, withHint(
		fmt.Errorf(
			"no connection for %s can be picked automatically; choose one with --mail-provider or --calendar-provider",
			result.Address,
		),
		shellCommand("corr", "account", "add", result.Address, "--help"),
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
	mail, calendar, tasks, messages := "–", "–", "–", "–"
	if account.Mail != nil {
		mail = string(account.Mail.Provider)
	}
	if account.Calendar != nil {
		calendar = string(account.Calendar.Provider)
	}
	if account.Tasks != nil {
		tasks = string(account.Tasks.Provider)
	}
	if account.Messages != nil {
		messages = string(account.Messages.Provider)
	}
	if mail == calendar && calendar == tasks && tasks == messages {
		return mail
	}
	return "mail:" + mail + " · cal:" + calendar + " · tasks:" + tasks + " · msg:" + messages
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
		view.strong("Ways to connect "+sanitizeCell(result.Address, 254)),
		view.muted("Found without signing in. You'll confirm a connection works when you sign in."),
	); err != nil {
		return err
	}
	if len(result.Candidates) == 0 {
		_, err := view.printf(
			"\n  Corresync couldn't find how to connect. You can still choose a connection yourself.\n"+
				"  %s\n",
			view.nextStep(shellCommand("corr", "setup")),
		)
		return err
	}
	for index, candidate := range result.Candidates {
		rank := "Also possible"
		if index == 0 {
			rank = "Recommended"
		}
		if !candidate.Available {
			rank = "Not available in this version"
		}
		copied := candidate
		if _, err := view.printf(
			"\n  %-14s %s (%s) · %s\n",
			rank,
			providerDisplayName(candidate.Provider),
			candidate.Provider,
			onboardingSignInSummary(&copied),
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
		"\n  Nothing was changed and nothing signed in.\n"+
			"  %s\n",
		view.nextStep(shellCommand("corr", "setup")),
	)
	return err
}
