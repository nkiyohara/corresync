package main

import (
	"context"
	"errors"
	"net/http"
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
) ([]browser.GoogleMailRow, error) {
	return nil, nil
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
) ([]browser.GoogleCalendarRow, error) {
	return nil, nil
}

func TestProjectionAccountsExposeOnlyContentFreePerServiceStatus(t *testing.T) {
	t.Parallel()

	const personalID domain.AccountID = "acc_00000000000000000000000000000002"
	configuration := config.Default()
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
	configuration := config.Default()
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
	configuration := config.Default()
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
	configuration := config.Default()
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
	configuration := config.Default()
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
	configuration := config.Default()
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
