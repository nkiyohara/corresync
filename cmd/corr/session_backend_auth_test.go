package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/policy"
	caldavprovider "github.com/nkiyohara/corresync/internal/provider/caldav"
	"github.com/nkiyohara/corresync/internal/provider/googleapi"
	"github.com/nkiyohara/corresync/internal/provider/googleweb"
	"github.com/nkiyohara/corresync/internal/provider/graphapi"
	"github.com/nkiyohara/corresync/internal/provider/jmap"
	"github.com/nkiyohara/corresync/internal/session"
)

type oauthManagerStub struct {
	calls    int
	route    config.OAuthRoute
	provider oauthlocal.Provider
	client   *http.Client
	err      error
}

type routedOAuthCall struct {
	route    config.OAuthRoute
	provider oauthlocal.Provider
}

type routedOAuthManagerStub struct {
	clients map[string]*http.Client
	calls   []routedOAuthCall
}

func (stub *routedOAuthManagerStub) Client(
	_ context.Context,
	route config.OAuthRoute,
	provider oauthlocal.Provider,
) (*http.Client, error) {
	client, exists := stub.clients[route.APIBase]
	if !exists {
		return nil, errors.New("synthetic OAuth route is not registered")
	}
	stub.calls = append(stub.calls, routedOAuthCall{
		route: route, provider: provider,
	})
	return client, nil
}

type googleWebBrowserStub struct {
	waitOrigins []string
	closed      bool
}

func (*googleWebBrowserStub) WaitForSession(
	context.Context,
) (session.Credentials, error) {
	return session.Credentials{}, errors.New("authorization observation is disabled")
}

func (*googleWebBrowserStub) Apply(*http.Request) error {
	return errors.New("authorization observation is disabled")
}

func (stub *googleWebBrowserStub) Close() error {
	stub.closed = true
	return nil
}

func (stub *googleWebBrowserStub) WaitForGoogleWeb(
	_ context.Context,
	origins []string,
) error {
	stub.waitOrigins = append([]string(nil), origins...)
	return nil
}

func (*googleWebBrowserStub) GoogleIdentity(
	context.Context,
	string,
) (string, error) {
	return "reader@example.test", nil
}

func (*googleWebBrowserStub) GoogleMailRows(
	context.Context,
	string,
) (browser.GoogleMailSnapshot, error) {
	return browser.GoogleMailSnapshot{State: "empty"}, nil
}

func (*googleWebBrowserStub) GoogleMailBody(
	context.Context,
	string,
) (string, error) {
	return "", nil
}

func (*googleWebBrowserStub) GoogleCalendarRows(
	context.Context,
	string,
) (browser.GoogleCalendarSnapshot, error) {
	return browser.GoogleCalendarSnapshot{State: "empty"}, nil
}

func TestProjectionAccountsExposeOnlyContentFreePerServiceStatus(t *testing.T) {
	t.Parallel()

	const personalID domain.AccountID = "acc_00000000000000000000000000000002"
	configuration := config.OutlookDefault()
	configuration.Accounts["personal"] = config.Account{
		ID: personalID,
		Mail: &config.MailRoute{
			Provider: domain.ProviderIMAPSMTP,
		},
	}
	workID := configuration.Accounts["work"].ID
	backend := &sessionBackend{
		configuration: configuration,
		accounts: map[domain.AccountID]sessionAccount{
			workID: {
				capabilities: domain.Capabilities{
					Mail: true, Calendar: true,
				},
				degradations: []domain.Degradation{
					{
						Feature: "mail.search",
						Reason:  "synthetic mail degradation",
					},
					{
						Feature: "calendar.selection",
						Reason:  "synthetic calendar degradation",
					},
				},
			},
		},
	}
	accounts, err := backend.ProjectionAccounts(t.Context())
	if err != nil {
		t.Fatalf("ProjectionAccounts() error = %v", err)
	}
	if len(accounts) != 2 ||
		accounts[0].Alias != "personal" ||
		accounts[1].Alias != "work" {
		t.Fatalf("unexpected projection account order: %+v", accounts)
	}
	if accounts[0].Authenticated || accounts[0].Capabilities != nil ||
		len(accounts[0].MailDegradations) != 0 {
		t.Fatalf("inactive account exposed runtime state: %+v", accounts[0])
	}
	if !accounts[1].Authenticated ||
		accounts[1].Capabilities == nil ||
		len(accounts[1].MailDegradations) != 1 ||
		len(accounts[1].CalendarDegradations) != 1 {
		t.Fatalf("active service status was not retained: %+v", accounts[1])
	}
}

func (stub *oauthManagerStub) Client(
	_ context.Context,
	route config.OAuthRoute,
	provider oauthlocal.Provider,
) (*http.Client, error) {
	stub.calls++
	stub.route = route
	stub.provider = provider
	return stub.client, stub.err
}

func TestSessionBackendOnlyResolvesJMAPCredentialForExplicitCLILogin(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	keyringReads := 0
	resolver, err := credential.New(credential.Options{
		Keyring: func(service, key string) (string, error) {
			keyringReads++
			if service != "corresync" || key != "jmap-work" {
				t.Fatalf("keyring request = %q, %q", service, key)
			}
			return "synthetic-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Accounts["work"] = config.Account{
		ID: accountID,
		Mail: &config.MailRoute{
			Provider: domain.ProviderJMAP,
			JMAP: &config.JMAPRoute{
				SessionURL: "https://jmap.example.invalid/session",
				Username:   "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialOSKeyring,
					Key:     "jmap-work",
					Consent: true,
				},
			},
		},
	}
	factoryCalls := 0
	var observedPassword []byte
	factoryError := errors.New("synthetic factory stop")
	backend := &sessionBackend{
		configuration: configuration,
		credentials:   resolver,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		newJMAP: func(_ context.Context, options jmap.Options) (*jmap.Client, error) {
			factoryCalls++
			observedPassword = options.Password
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	_, err = backend.ListMail(t.Context(), application.MailListInput{
		Account: accountID,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 25,
	}, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "corr auth login") {
		t.Fatalf("ListMail() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"ordinary MCP read touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "explicit local CLI") {
		t.Fatalf("MCP Login() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"MCP login touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, cliCaller)
	if !errors.Is(err, factoryError) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if keyringReads != 1 || factoryCalls != 1 {
		t.Fatalf(
			"explicit CLI login did not resolve once: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}
	for index, value := range observedPassword {
		if value != 0 {
			t.Fatalf("temporary credential byte %d was not zeroed", index)
		}
	}
}

func TestJMAPCapabilityReportPreservesReadAccessAndNamesWriteDegradation(
	t *testing.T,
) {
	t.Parallel()

	capabilities, degradations := jmapCapabilityReport(
		jmap.ObservedCapabilities{ReadOnly: true},
	)
	if !capabilities.Mail || !capabilities.Folders ||
		!capabilities.AttachmentReads || !capabilities.IncrementalSync ||
		capabilities.AttachmentWrites {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if len(degradations) != 2 ||
		degradations[0].Feature != "mail.write" ||
		degradations[1].Feature != "mail.send" {
		t.Fatalf("degradations = %+v", degradations)
	}
}

func TestSessionBackendOnlyResolvesCalDAVCredentialForExplicitCLILogin(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	keyringReads := 0
	resolver, err := credential.New(credential.Options{
		Keyring: func(service, key string) (string, error) {
			keyringReads++
			if service != "corresync" || key != "caldav-work" {
				t.Fatalf("keyring request = %q, %q", service, key)
			}
			return "synthetic-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Accounts["work"] = config.Account{
		ID: accountID,
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &config.CalDAVRoute{
				Endpoint: "https://dav.example.invalid/",
				Username: "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialOSKeyring,
					Key:     "caldav-work",
					Consent: true,
				},
			},
		},
	}
	factoryCalls := 0
	var observedPassword []byte
	factoryError := errors.New("synthetic factory stop")
	backend := &sessionBackend{
		configuration: configuration,
		credentials:   resolver,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		newCalDAV: func(
			_ context.Context,
			options caldavprovider.Options,
		) (*caldavprovider.Client, error) {
			factoryCalls++
			observedPassword = options.Password
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	_, err = backend.ListCalendar(t.Context(), application.CalendarListInput{
		Account: accountID,
		Calendar: application.CalendarFolder{
			Kind: application.CalendarFolderDistinguished,
			ID:   "calendar",
		},
		Start: "2026-07-28T00:00:00Z",
		End:   "2026-07-29T00:00:00Z",
	}, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "corr auth login") {
		t.Fatalf("ListCalendar() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"ordinary MCP read touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "explicit local CLI") {
		t.Fatalf("MCP Login() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"MCP login touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, cliCaller)
	if !errors.Is(err, factoryError) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if keyringReads != 1 || factoryCalls != 1 {
		t.Fatalf(
			"explicit CLI login did not resolve once: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}
	for index, value := range observedPassword {
		if value != 0 {
			t.Fatalf("temporary credential byte %d was not zeroed", index)
		}
	}
}

func TestSessionBackendOnlyAuthorizesSharedGoogleRouteForExplicitCLILogin(
	t *testing.T,
) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	route := config.OAuthRoute{
		APIBase:     "https://www.googleapis.com",
		ClientID:    "synthetic.apps.googleusercontent.com",
		RedirectURI: "http://127.0.0.1:53682/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring,
			Key:     "google-work",
			Consent: true,
		},
	}
	configuration := config.OutlookDefault()
	configuration.Accounts["work"] = config.Account{
		ID: accountID, Address: "reader@example.test",
		Mail: &config.MailRoute{
			Provider: domain.ProviderGoogleAPI, GoogleAPI: &route,
		},
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderGoogleAPI, GoogleAPI: &route,
		},
	}
	manager := &oauthManagerStub{client: http.DefaultClient}
	factoryError := errors.New("synthetic Google factory stop")
	factoryCalls := 0
	backend := &sessionBackend{
		configuration: configuration,
		oauth:         manager,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		newGoogle: func(
			_ context.Context,
			options googleapi.Options,
		) (*googleapi.Client, error) {
			factoryCalls++
			if !options.Mail || !options.Calendar ||
				options.Address != "reader@example.test" ||
				options.APIBase != route.APIBase {
				t.Fatalf("Google options = %#v", options)
			}
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	_, err := backend.ListMail(t.Context(), application.MailListInput{
		Account: accountID,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	}, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "corr auth login") {
		t.Fatalf("ListMail() error = %v", err)
	}
	if manager.calls != 0 || factoryCalls != 0 {
		t.Fatalf(
			"ordinary MCP read touched OAuth: manager=%d factory=%d",
			manager.calls,
			factoryCalls,
		)
	}
	if _, err := backend.Login(t.Context(), accountID, mcpCaller); err == nil {
		t.Fatal("MCP Login() unexpectedly authorized Google")
	}
	if manager.calls != 0 || factoryCalls != 0 {
		t.Fatalf(
			"MCP login touched OAuth: manager=%d factory=%d",
			manager.calls,
			factoryCalls,
		)
	}
	if _, err := backend.Login(t.Context(), accountID, cliCaller); !errors.Is(
		err,
		factoryError,
	) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if manager.calls != 1 || factoryCalls != 1 || manager.route != route ||
		manager.provider.ID != domain.ProviderGoogleAPI ||
		!slices.Contains(manager.provider.Scopes, "https://mail.google.com/") ||
		!slices.Contains(manager.provider.Scopes, "https://www.googleapis.com/auth/calendar.events") {
		t.Fatalf(
			"explicit Google OAuth = calls %d factory %d route %#v provider %#v",
			manager.calls,
			factoryCalls,
			manager.route,
			manager.provider,
		)
	}
}

func TestSessionBackendOnlyAuthorizesGraphForExplicitCLILogin(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	route := config.OAuthRoute{
		APIBase:     "https://graph.microsoft.com/v1.0",
		ClientID:    "synthetic-public-client",
		RedirectURI: "http://127.0.0.1:53683/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring,
			Key:     "graph-work",
			Consent: true,
		},
	}
	configuration := config.OutlookDefault()
	configuration.Accounts["work"] = config.Account{
		ID: accountID, Address: "reader@example.test",
		Mail: &config.MailRoute{
			Provider:       domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &route,
		},
	}
	manager := &oauthManagerStub{client: http.DefaultClient}
	factoryError := errors.New("synthetic Graph factory stop")
	factoryCalls := 0
	backend := &sessionBackend{
		configuration: configuration,
		oauth:         manager,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		newGraph: func(
			_ context.Context,
			options graphapi.Options,
		) (*graphapi.Client, error) {
			factoryCalls++
			if !options.Mail || options.Calendar ||
				options.Address != "reader@example.test" ||
				options.APIBase != route.APIBase {
				t.Fatalf("Graph options = %#v", options)
			}
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	if _, err := backend.Login(
		t.Context(),
		accountID,
		mcpCaller,
	); err == nil {
		t.Fatal("MCP Login() unexpectedly authorized Graph")
	}
	if manager.calls != 0 || factoryCalls != 0 {
		t.Fatalf(
			"MCP login touched Graph OAuth: manager=%d factory=%d",
			manager.calls,
			factoryCalls,
		)
	}
	if _, err := backend.Login(
		t.Context(),
		accountID,
		cliCaller,
	); !errors.Is(err, factoryError) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if manager.calls != 1 || factoryCalls != 1 ||
		manager.provider.ID != domain.ProviderMicrosoftGraph ||
		!slices.Contains(manager.provider.Scopes, "Mail.ReadWrite") ||
		!slices.Contains(manager.provider.Scopes, "Mail.Send") ||
		!slices.Contains(manager.provider.Scopes, "User.Read") ||
		slices.Contains(manager.provider.Scopes, "Calendars.ReadWrite") {
		t.Fatalf(
			"explicit Graph OAuth = calls %d factory %d provider %#v",
			manager.calls,
			factoryCalls,
			manager.provider,
		)
	}
}

func TestSessionBackendOnlyOpensBrowserOwnedGoogleForExplicitCLILogin(
	t *testing.T,
) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	configuration := config.OutlookDefault()
	configuration.Accounts["work"] = config.Account{
		ID: accountID, Address: "reader@example.test",
		Mail: &config.MailRoute{
			Provider: domain.ProviderGoogleWeb,
			GoogleWeb: &config.WebRoute{
				Origin: "https://mail.google.com",
			},
		},
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderGoogleWeb,
			GoogleWeb: &config.WebRoute{
				Origin: "https://calendar.google.com",
			},
		},
	}
	factoryError := errors.New("synthetic Google Web factory stop")
	factoryCalls := 0
	launchCalls := 0
	handle := &googleWebBrowserStub{}
	app := &runtime{
		context: t.Context(),
		stderr:  &strings.Builder{},
		launch: func(
			_ context.Context,
			options browser.Options,
		) (browserHandle, error) {
			launchCalls++
			if !options.BrowserOwnedOnly ||
				options.Origin != "https://mail.google.com" ||
				!slices.Equal(
					options.AdditionalOrigins,
					[]string{"https://calendar.google.com"},
				) {
				t.Fatalf("browser options = %#v", options)
			}
			return handle, nil
		},
	}
	backend := &sessionBackend{
		app:           app,
		configuration: configuration,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		lifecycle:     t.Context(),
		newGoogleWeb: func(
			_ context.Context,
			options googleweb.Options,
		) (*googleweb.Client, error) {
			factoryCalls++
			if !options.Mail || !options.Calendar || options.Driver != handle ||
				options.ExpectedAddress != "reader@example.test" {
				t.Fatalf("Google Web options = %#v", options)
			}
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	if _, err := backend.Login(t.Context(), accountID, mcpCaller); err == nil {
		t.Fatal("MCP Login() unexpectedly opened Google Web")
	}
	if launchCalls != 0 || factoryCalls != 0 {
		t.Fatalf("MCP touched browser: launch=%d factory=%d", launchCalls, factoryCalls)
	}
	if _, err := backend.Login(t.Context(), accountID, cliCaller); !errors.Is(
		err,
		factoryError,
	) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if launchCalls != 1 || factoryCalls != 1 || !handle.closed {
		t.Fatalf(
			"explicit Google Web login = launch %d factory %d closed %t",
			launchCalls,
			factoryCalls,
			handle.closed,
		)
	}
}

func TestMergeSessionAccountsPreservesServiceSpecificCapabilities(t *testing.T) {
	t.Parallel()
	mail := &application.MailService{}
	calendar := &application.CalendarService{}
	merged := mergeSessionAccounts(
		sessionAccount{
			mail: mail,
			capabilities: domain.Capabilities{
				Mail: true, Folders: true, AttachmentReads: true,
			},
			captured: time.Unix(1, 0),
		},
		sessionAccount{
			calendar: calendar,
			capabilities: domain.Capabilities{
				Calendar: true,
			},
			captured: time.Unix(2, 0),
		},
	)
	if merged.mail != mail || merged.calendar != calendar ||
		!merged.capabilities.Mail || !merged.capabilities.Calendar ||
		!merged.capabilities.Folders || !merged.capabilities.AttachmentReads ||
		!merged.captured.Equal(time.Unix(2, 0)) {
		t.Fatalf("mergeSessionAccounts() = %#v", merged)
	}
}

func TestSessionBackendKeepsHybridProvidersAndAccountsIsolated(t *testing.T) {
	t.Parallel()

	const (
		alphaID domain.AccountID = "acc_00000000000000000000000000000001"
		betaID  domain.AccountID = "acc_00000000000000000000000000000002"
	)
	googleAlpha := newSessionGoogleServer(
		t,
		"alpha@example.test",
		"google-alpha",
		"unused-google-alpha-event",
	)
	defer googleAlpha.Close()
	graphAlpha := newSessionGraphServer(
		t,
		"alpha@example.test",
		"unused-graph-alpha-message",
		"graph-alpha",
	)
	defer graphAlpha.Close()
	graphBeta := newSessionGraphServer(
		t,
		"beta@example.test",
		"graph-beta",
		"unused-graph-beta-event",
	)
	defer graphBeta.Close()
	googleBeta := newSessionGoogleServer(
		t,
		"beta@example.test",
		"unused-google-beta-message",
		"google-beta",
	)
	defer googleBeta.Close()

	googleAlphaRoute := sessionOAuthRoute(googleAlpha.URL, "google-alpha")
	graphAlphaRoute := sessionOAuthRoute(graphAlpha.URL, "graph-alpha")
	graphBetaRoute := sessionOAuthRoute(graphBeta.URL, "graph-beta")
	googleBetaRoute := sessionOAuthRoute(googleBeta.URL, "google-beta")
	configuration := config.OutlookDefault()
	configuration.DefaultAccount = "alpha"
	configuration.Accounts = map[string]config.Account{
		"alpha": {
			ID: alphaID, Address: "alpha@example.test",
			Mail: &config.MailRoute{
				Provider:  domain.ProviderGoogleAPI,
				GoogleAPI: &googleAlphaRoute,
			},
			Calendar: &config.CalendarRoute{
				Provider:       domain.ProviderMicrosoftGraph,
				MicrosoftGraph: &graphAlphaRoute,
			},
		},
		"beta": {
			ID: betaID, Address: "beta@example.test",
			Mail: &config.MailRoute{
				Provider:       domain.ProviderMicrosoftGraph,
				MicrosoftGraph: &graphBetaRoute,
			},
			Calendar: &config.CalendarRoute{
				Provider:  domain.ProviderGoogleAPI,
				GoogleAPI: &googleBetaRoute,
			},
		},
	}
	manager := &routedOAuthManagerStub{clients: map[string]*http.Client{
		googleAlpha.URL: googleAlpha.Client(),
		graphAlpha.URL:  graphAlpha.Client(),
		graphBeta.URL:   graphBeta.Client(),
		googleBeta.URL:  googleBeta.Client(),
	}}
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &sessionBackend{
		configuration: configuration,
		guard: daemonMCPGuard(
			t,
			policy.DefaultRules(),
			&daemonMCPAudit{},
		),
		oauth:          manager,
		newGoogle:      googleapi.New,
		newGraph:       graphapi.New,
		accounts:       make(map[domain.AccountID]sessionAccount),
		previews:       make(map[string]sessionPreview),
		lifecycle:      lifecycle,
		cancel:         cancel,
		monitorStarted: make(map[domain.AccountID]bool),
		monitorCancel:  make(map[domain.AccountID]context.CancelFunc),
		monitorDone:    make(map[domain.AccountID]chan struct{}),
	}
	caller := domain.Caller{Surface: "cli", Instance: "hybrid-provider-test"}
	for _, accountID := range []domain.AccountID{alphaID, betaID} {
		result, err := backend.Login(t.Context(), accountID, caller)
		if err != nil || !result.Authenticated || result.Account != accountID {
			t.Fatalf("Login(%s) = %+v, %v", accountID, result, err)
		}
	}
	requireHybridOAuthScopes(t, manager.calls)

	assertSessionMailProvider(
		t,
		backend,
		caller,
		alphaID,
		domain.ProviderGoogleAPI,
		"google-alpha",
	)
	assertSessionCalendarProvider(
		t,
		backend,
		caller,
		alphaID,
		domain.ProviderMicrosoftGraph,
		"graph-alpha",
	)
	assertSessionMailProvider(
		t,
		backend,
		caller,
		betaID,
		domain.ProviderMicrosoftGraph,
		"graph-beta",
	)
	assertSessionCalendarProvider(
		t,
		backend,
		caller,
		betaID,
		domain.ProviderGoogleAPI,
		"google-beta",
	)

	status, err := backend.SessionStatus(t.Context(), caller)
	if err != nil || len(status.Accounts) != 2 {
		t.Fatalf("SessionStatus() = %+v, %v", status, err)
	}
	for _, account := range status.Accounts {
		if !account.Authenticated || account.Capabilities == nil ||
			!account.Capabilities.Mail || !account.Capabilities.Calendar {
			t.Fatalf("hybrid account status = %+v", account)
		}
	}

	if _, err := backend.Logout(t.Context(), alphaID, caller); err != nil {
		t.Fatalf("Logout(alpha) error = %v", err)
	}
	if _, err := backend.ListMail(
		t.Context(),
		sessionMailInput(alphaID),
		caller,
	); err == nil || !strings.Contains(err.Error(), "corr auth login") {
		t.Fatalf("logged-out alpha mail error = %v", err)
	}
	assertSessionMailProvider(
		t,
		backend,
		caller,
		betaID,
		domain.ProviderMicrosoftGraph,
		"graph-beta",
	)
	if _, err := backend.Logout(t.Context(), betaID, caller); err != nil {
		t.Fatalf("Logout(beta) error = %v", err)
	}
}

func sessionOAuthRoute(base, key string) config.OAuthRoute {
	return config.OAuthRoute{
		APIBase:     base,
		ClientID:    "synthetic-public-client-" + key,
		RedirectURI: "http://127.0.0.1:53682/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring,
			Key:     key,
			Consent: true,
		},
	}
}

func requireHybridOAuthScopes(t *testing.T, calls []routedOAuthCall) {
	t.Helper()
	if len(calls) != 4 {
		t.Fatalf("OAuth calls = %+v", calls)
	}
	for _, call := range calls {
		hasMail := slices.Contains(
			call.provider.Scopes,
			"https://mail.google.com/",
		) || slices.Contains(call.provider.Scopes, "Mail.ReadWrite")
		hasCalendar := slices.Contains(
			call.provider.Scopes,
			"https://www.googleapis.com/auth/calendar.events",
		) || slices.Contains(call.provider.Scopes, "Calendars.ReadWrite")
		if hasMail == hasCalendar {
			t.Fatalf(
				"hybrid service grant was not least-privilege: %+v",
				call,
			)
		}
	}
}

func sessionMailInput(account domain.AccountID) application.MailListInput {
	return application.MailListInput{
		Account: account,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 10,
	}
}

func assertSessionMailProvider(
	t *testing.T,
	backend *sessionBackend,
	caller domain.Caller,
	account domain.AccountID,
	provider domain.ProviderID,
	subject string,
) {
	t.Helper()
	page, err := backend.ListMail(
		t.Context(),
		sessionMailInput(account),
		caller,
	)
	if err != nil || len(page.Messages) != 1 ||
		page.Messages[0].Subject != subject ||
		page.Messages[0].Provenance.AccountID != account ||
		page.Messages[0].Provenance.Provider != provider {
		t.Fatalf(
			"ListMail(%s) = %+v, %v",
			account,
			page,
			err,
		)
	}
}

func assertSessionCalendarProvider(
	t *testing.T,
	backend *sessionBackend,
	caller domain.Caller,
	account domain.AccountID,
	provider domain.ProviderID,
	subject string,
) {
	t.Helper()
	page, err := backend.ListCalendar(
		t.Context(),
		application.CalendarListInput{
			Account: account,
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Start: "2026-08-01T00:00:00Z",
			End:   "2026-08-02T00:00:00Z",
		},
		caller,
	)
	if err != nil || len(page.Events) != 1 ||
		page.Events[0].Subject != subject ||
		page.Events[0].Provenance.AccountID != account ||
		page.Events[0].Provenance.Provider != provider {
		t.Fatalf(
			"ListCalendar(%s) = %+v, %v",
			account,
			page,
			err,
		)
	}
}

func newSessionGoogleServer(
	t *testing.T,
	address, mailSubject, calendarSubject string,
) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gmail/v1/users/me/profile":
			writeSessionJSON(t, writer, map[string]string{
				"emailAddress": address,
			})
		case "/gmail/v1/users/me/messages":
			writeSessionJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "google-message"}},
				"resultSizeEstimate": 1,
			})
		case "/gmail/v1/users/me/messages/google-message":
			writeSessionJSON(t, writer, map[string]any{
				"id":           "google-message",
				"historyId":    "101",
				"internalDate": "1785578400000",
				"labelIds":     []string{"INBOX"},
				"payload": map[string]any{
					"headers": []map[string]string{
						{"name": "Subject", "value": mailSubject},
						{
							"name":  "From",
							"value": "Sender <sender@example.test>",
						},
					},
				},
			})
		case "/calendar/v3/users/me/calendarList/primary":
			writeSessionJSON(t, writer, map[string]any{
				"id":         address,
				"accessRole": "owner",
				"conferenceProperties": map[string]any{
					"allowedConferenceSolutionTypes": []string{"hangoutsMeet"},
				},
			})
		case "/calendar/v3/calendars/primary/events":
			writeSessionJSON(t, writer, map[string]any{
				"items": []map[string]any{{
					"id":      "google-event",
					"etag":    `"google-event-etag"`,
					"status":  "confirmed",
					"summary": calendarSubject,
					"start": map[string]string{
						"dateTime": "2026-08-01T10:00:00Z",
						"timeZone": "UTC",
					},
					"end": map[string]string{
						"dateTime": "2026-08-01T11:00:00Z",
						"timeZone": "UTC",
					},
					"organizer": map[string]any{
						"email": address,
						"self":  true,
					},
				}},
			})
		default:
			http.Error(
				writer,
				"unexpected synthetic Google request",
				http.StatusNotFound,
			)
		}
	}))
}

func newSessionGraphServer(
	t *testing.T,
	address, mailSubject, calendarSubject string,
) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeSessionJSON(t, writer, map[string]string{
				"id": "graph-user", "mail": address,
			})
		case "/me/mailFolders/inbox":
			writeSessionJSON(t, writer, map[string]string{"id": "graph-inbox"})
		case "/me/mailFolders/inbox/messages":
			writeSessionJSON(t, writer, map[string]any{
				"@odata.count": 1,
				"value": []map[string]any{{
					"@odata.etag":      `W/"graph-message-etag"`,
					"id":               "graph-message",
					"subject":          mailSubject,
					"receivedDateTime": "2026-08-01T10:00:00Z",
					"importance":       "normal",
					"from": map[string]any{
						"emailAddress": map[string]string{
							"name": "Sender", "address": "sender@example.test",
						},
					},
				}},
			})
		case "/me/calendar":
			writeSessionJSON(t, writer, map[string]any{
				"id": "graph-calendar", "canEdit": true,
			})
		case "/me/calendarView":
			writeSessionJSON(t, writer, map[string]any{
				"value": []map[string]any{{
					"@odata.etag": `W/"graph-event-etag"`,
					"id":          "graph-event",
					"subject":     calendarSubject,
					"start": map[string]string{
						"dateTime": "2026-08-01T10:00:00",
						"timeZone": "UTC",
					},
					"end": map[string]string{
						"dateTime": "2026-08-01T11:00:00",
						"timeZone": "UTC",
					},
					"organizer": map[string]any{
						"emailAddress": map[string]string{
							"name": "Reader", "address": address,
						},
					},
					"isOrganizer": true,
					"showAs":      "busy",
				}},
			})
		default:
			http.Error(
				writer,
				"unexpected synthetic Graph request",
				http.StatusNotFound,
			)
		}
	}))
}

func writeSessionJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
