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
	Address                   string `arg:"" help:"Bare email address for the account."`
	Alias                     string `help:"Local alias; defaults to the address local part."`
	Provider                  string `help:"Explicit primary provider override."`
	CalendarProvider          string `help:"Explicit calendar provider override; use none for mail-only."`
	Origin                    string `help:"Outlook Web HTTPS origin override."`
	Mailbox                   string `help:"Optional Outlook mailbox identity."`
	SessionURL                string `help:"JMAP HTTPS session resource."`
	Username                  string `help:"Standards mail login identity; defaults to the address."`
	CredentialBackend         string `default:"os-keyring" enum:"os-keyring,helper" help:"External standards credential backend."`
	CredentialKey             string `help:"External standards credential lookup key."`
	ApproveCredential         bool   `help:"Record explicit consent to use that external credential."`
	IMAPHost                  string `help:"IMAP server host."`
	IMAPPort                  uint16 `help:"IMAP server port."`
	IMAPTLS                   string `default:"implicit" enum:"implicit,starttls" help:"IMAP TLS mode."`
	SMTPHost                  string `help:"SMTP Submission server host."`
	SMTPPort                  uint16 `help:"SMTP Submission server port."`
	SMTPTLS                   string `default:"starttls" enum:"implicit,starttls" help:"SMTP TLS mode."`
	CalDAVEndpoint            string `help:"CalDAV HTTPS discovery endpoint."`
	CalendarPath              string `help:"Optional absolute CalDAV calendar path."`
	CalendarUsername          string `help:"CalDAV login identity; defaults to --username or the address."`
	CalendarCredentialBackend string `help:"External CalDAV credential backend; defaults to --credential-backend."`
	CalendarCredentialKey     string `help:"External CalDAV credential key; defaults to --credential-key."`
	ApproveCalendarCredential bool   `help:"Record consent for a distinct CalDAV credential."`
	Default                   bool   `help:"Make this the default account."`
	JSON                      bool   `help:"Write machine-readable JSON."`
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
	Selected application.ProviderCandidate `json:"selected"`
	Account  application.AccountView       `json:"account"`
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
	return nil
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
	}{{"Mail", account.Mail}, {"Calendar", account.Calendar}} {
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
	return nil
}

func (command *accountAddCommand) Run(app *runtime) error {
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		return err
	}
	result, err := discoverer.Discover(app.context, command.Address)
	if err != nil {
		return err
	}
	var endpointOverride string
	switch domain.ProviderID(command.Provider) {
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
	case "",
		domain.ProviderMicrosoftOWA,
		domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderPOP3:
		endpointOverride = command.Origin
	default:
		endpointOverride = command.Origin
	}
	selected, err := selectAccountCandidate(result, command.Provider, endpointOverride)
	if err != nil {
		return err
	}
	alias := command.Alias
	if alias == "" {
		alias = command.Address[:strings.LastIndexByte(command.Address, '@')]
	}
	mail, calendar, endpoint, err := command.routes(selected, result)
	if err != nil {
		return err
	}
	account, err := accounts.Add(app.context, application.AccountAddInput{
		Alias: alias, Address: result.Address, Mail: mail, Calendar: calendar,
		Default: command.Default,
	})
	if err != nil {
		return err
	}
	output := accountAddResult{Selected: selected, Account: account}
	if command.JSON {
		return writeJSON(app.stdout, output)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-14s %s\n  %-14s %d/100\n  %-14s %s\n  %-14s %s\n\n%s  %s\n",
		view.info(),
		view.strong("Selected provider route"),
		"Provider", selected.Provider,
		"Confidence", selected.Confidence,
		"Authentication", selected.Authentication,
		"Endpoint", sanitizeCell(endpoint, 2048),
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; authentication has not started"),
	)
	return err
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderPOP3:
		return nil, nil, "", fmt.Errorf(
			"provider %q has no account route builder",
			selected.Provider,
		)
	default:
		return nil, nil, "", fmt.Errorf("unknown provider %q", selected.Provider)
	}
}

func (command accountAddCommand) finishRoutes(
	mail *application.AccountMailRouteInput,
	calendar *application.AccountCalendarRouteInput,
	endpoint string,
	selected application.ProviderCandidate,
	discovery application.AccountDiscoveryResult,
) (*application.AccountMailRouteInput, *application.AccountCalendarRouteInput, string, error) {
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
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
			provider != domain.ProviderCalDAV {
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
		if endpoint.Kind == kind && strings.HasPrefix(endpoint.Value, "https://") {
			return endpoint.Value
		}
	}
	return ""
}

func accountRouteLabel(account application.AccountView) string {
	mail, calendar := "–", "–"
	if account.Mail != nil {
		mail = string(account.Mail.Provider)
	}
	if account.Calendar != nil {
		calendar = string(account.Calendar.Provider)
	}
	if mail == calendar {
		return mail
	}
	return "mail:" + mail + " · cal:" + calendar
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
			"\n  No candidate was inferred. Manual --provider and --origin remain available.\n",
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
	return nil
}
