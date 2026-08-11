package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
	"github.com/nkiyohara/corresync/internal/rollout"
)

type accountDiscovererStub struct {
	observation application.AccountDiscoveryObservation
	address     string
}

func (stub *accountDiscovererStub) Discover(
	_ context.Context,
	address string,
) (application.AccountDiscoveryObservation, error) {
	stub.address = address
	return stub.observation, nil
}

func newAccountCommandRuntime(
	t *testing.T,
	discoverer application.AccountDiscoverer,
) (*runtime, string, *bytes.Buffer) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CORRESYNC_STATE_DIR", state)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := newRuntime(
		context.Background(),
		path,
		&stdout,
		&stderr,
		buildinfo.Info{Version: "dev", OS: "linux", Arch: "amd64"},
	)
	app.accountDiscoverer = discoverer
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("account lifecycle unexpectedly started authentication")
		return nil, nil
	}
	return app, path, &stdout
}

func TestSetupCreatesProviderNeutralConfigThenDirectsGoogleToOfficialMCP(
	t *testing.T,
) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:                  domain.ProviderGoogle,
				Confidence:                98,
				Authentication:            application.DiscoveryExplicitOAuth,
				RequiresExplicitSelection: true,
				Evidence: []application.DiscoveryEvidence{{
					Source: "known_domain", Detail: "gmail.com",
				}},
			}},
		},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	var stdout, stderr bytes.Buffer
	app := newRuntime(
		t.Context(),
		path,
		&stdout,
		&stderr,
		buildinfo.Info{Version: "dev", OS: "linux", Arch: "amd64"},
	)
	app.accountDiscoverer = discoverer
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("setup unexpectedly started authentication")
		return nil, nil
	}

	command := setupCommand{Address: "reader@gmail.com", Alias: "personal"}
	err := command.Run(app)
	if err == nil {
		t.Fatal("setup automatically selected a Google route")
	}
	for _, expected := range []string{
		"Gmail was found",
		"awaiting approval",
		"includes the Google integration but keeps it disabled",
		"no Google sign-in was started",
		"coming soon after approval",
		"Outlook Web is available now",
		"Google's official Workspace MCP servers",
		"Developer Preview",
		googleWorkspaceMCPGuide,
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("setup error missing %q: %v", expected, err)
		}
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.DefaultAccount != "" || len(configuration.Accounts) != 0 {
		t.Fatalf("setup persisted a Google route = %+v", configuration)
	}
	for _, expected := range []string{
		"Provider-neutral configuration created",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("setup output missing %q: %q", expected, stdout.String())
		}
	}
}

func TestAccountAddCreatesDistinctStableIsolationKeysWithoutAuthentication(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:       domain.ProviderMicrosoftOWA,
				Confidence:     98,
				Authentication: application.DiscoveryBrowserFirstParty,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "origin", Value: "https://outlook.example.invalid",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "known_domain", Detail: "example.invalid",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)

	for _, input := range []struct {
		address string
		alias   string
	}{
		{"first@example.invalid", "first"},
		{"second@example.invalid", "second"},
	} {
		command := accountAddCommand{Address: input.address, Alias: input.alias}
		if err := command.Run(app); err != nil {
			t.Fatalf("add %s: %v", input.alias, err)
		}
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	first := configuration.Accounts["first"]
	second := configuration.Accounts["second"]
	if first.ID == second.ID || first.ID.ValidateOpaque() != nil || second.ID.ValidateOpaque() != nil {
		t.Fatalf("account IDs are not distinct opaque values: %q %q", first.ID, second.ID)
	}
	if first.Monitor != nil || second.Monitor != nil {
		t.Fatal("account add unexpectedly enabled monitoring")
	}
	firstProfile, err := paths.ProfileDir(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondProfile, err := paths.ProfileDir(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstProfile == secondProfile {
		t.Fatalf("profile paths collided: %s", firstProfile)
	}
	if !strings.Contains(stdout.String(), "authentication has not started") {
		t.Fatalf("output did not state consent boundary: %q", stdout.String())
	}
}

func TestAccountManualOverrideAndLifecycle(t *testing.T) {
	discoverer := &accountDiscovererStub{}
	app, path, _ := newAccountCommandRuntime(t, discoverer)
	add := accountAddCommand{
		Address: "reader@example.invalid", Alias: "reader",
		Provider: string(domain.ProviderMicrosoftOWA),
		Origin:   "https://outlook.example.invalid",
	}
	if err := add.Run(app); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	stableID := before.Accounts["reader"].ID

	if err := (&accountRenameCommand{Account: "reader", Alias: "home"}).Run(app); err != nil {
		t.Fatal(err)
	}
	renamed, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Accounts["home"].ID != stableID {
		t.Fatal("rename changed the stable ID")
	}
	if err := (&accountRemoveCommand{Account: "home"}).Run(app); err == nil {
		t.Fatal("remove succeeded without explicit approval")
	}
	if err := (&accountRemoveCommand{Account: "home", Approve: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	removed, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := removed.Accounts["home"]; exists {
		t.Fatal("removed account remained configured")
	}
}

func TestSelectAccountCandidateDoesNotAutoSelectExplicitConsent(t *testing.T) {
	t.Parallel()
	result := application.AccountDiscoveryResult{
		Domain: "gmail.com",
		Candidates: []application.ProviderCandidate{{
			Provider: domain.ProviderGoogle, Confidence: 98,
			Authentication:            application.DiscoveryExplicitOAuth,
			RequiresExplicitSelection: true,
			Available:                 true,
		}},
	}
	if _, err := selectAccountCandidate(result, "", ""); err == nil {
		t.Fatal("explicit OAuth candidate was automatically selected")
	}
}

func TestAccountAddRejectsGoogleWebEvenWhenDiscovered(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{
				{
					Provider:                  domain.ProviderGoogle,
					Confidence:                98,
					Authentication:            application.DiscoveryExplicitOAuth,
					RequiresExplicitSelection: true,
					Evidence: []application.DiscoveryEvidence{{
						Source: "known_domain", Detail: "gmail.com",
					}},
				},
				{
					Provider:       domain.ProviderGoogleWeb,
					Confidence:     90,
					Authentication: application.DiscoveryBrowserFirstParty,
					Endpoints: []application.DiscoveredEndpoint{{
						Kind: "origin", Value: "https://mail.google.com",
					}},
					Evidence: []application.DiscoveryEvidence{{
						Source: "known_domain", Detail: "gmail.com",
					}},
				},
			},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	err := (&accountAddCommand{
		Address:  "reader@gmail.com",
		Alias:    "google-web",
		Provider: string(domain.ProviderGoogleWeb),
	}).Run(app)
	if err == nil || !strings.Contains(
		err.Error(),
		`provider "google-web" is not available in this build`,
	) {
		t.Fatalf("google-web error = %v", err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["google-web"]; exists {
		t.Fatalf("google-web route was persisted: %#v", configuration)
	}
	if stdout.Len() != 0 {
		t.Fatalf("rejected Google output = %q", stdout.String())
	}
}

func TestAccountAddRejectsGoogleWebOnOneServiceOfMixedRoute(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:       domain.ProviderMicrosoftOWA,
				Confidence:     98,
				Authentication: application.DiscoveryBrowserFirstParty,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "origin", Value: "https://outlook.cloud.microsoft",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "known_domain", Detail: "example.test",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	err := (&accountAddCommand{
		Address:          "reader@example.test",
		Alias:            "mixed-web",
		Provider:         string(domain.ProviderMicrosoftOWA),
		Origin:           "https://outlook.cloud.microsoft",
		CalendarProvider: string(domain.ProviderGoogleWeb),
	}).Run(app)
	if err == nil || !strings.Contains(
		err.Error(),
		`calendar provider "google-web" is not available in this build`,
	) {
		t.Fatalf("mixed google-web error = %v", err)
	}
	configuration, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, exists := configuration.Accounts["mixed-web"]; exists {
		t.Fatalf("mixed google-web route was persisted: %#v", configuration)
	}
	if stdout.Len() != 0 {
		t.Fatalf("rejected mixed route output = %q", stdout.String())
	}
}

func TestAccountAddPersistsJMAPRouteWithoutAuthenticating(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:                  domain.ProviderJMAP,
				Confidence:                85,
				Authentication:            application.DiscoveryExternalCredential,
				RequiresExplicitSelection: true,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "jmap", Value: "https://jmap.example.invalid/.well-known/jmap",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "well_known_jmap", Detail: "example.invalid",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "reader@example.invalid", Alias: "standards",
		Provider:          string(domain.ProviderJMAP),
		SessionURL:        "https://jmap.example.invalid/session",
		Username:          "reader@example.invalid",
		CredentialBackend: "os-keyring",
		CredentialKey:     "jmap-standards",
		ApproveCredential: true,
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["standards"]
	if account.Mail == nil || account.Mail.JMAP == nil ||
		account.Mail.JMAP.SessionURL != command.SessionURL ||
		account.Mail.JMAP.Credential.Key != command.CredentialKey ||
		account.Calendar != nil {
		t.Fatalf("persisted JMAP account = %#v", account)
	}
	if !strings.Contains(stdout.String(), "authentication has not started") {
		t.Fatalf("output did not preserve authentication boundary: %q", stdout.String())
	}
}

func TestAccountAddPersistsMixedIMAPAndCalDAVRoutes(t *testing.T) {
	discoverer := &accountDiscovererStub{}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "reader@example.invalid", Alias: "standards",
		Provider:          string(domain.ProviderIMAPSMTP),
		CalendarProvider:  string(domain.ProviderCalDAV),
		Username:          "reader@example.invalid",
		CredentialBackend: "os-keyring",
		CredentialKey:     "standards-account",
		ApproveCredential: true,
		IMAPHost:          "imap.example.invalid",
		IMAPPort:          993,
		IMAPTLS:           "implicit",
		SMTPHost:          "smtp.example.invalid",
		SMTPPort:          587,
		SMTPTLS:           "starttls",
		CalDAVEndpoint:    "https://dav.example.invalid/",
		CalendarPath:      "/calendars/reader/main/",
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["standards"]
	if account.Mail == nil || account.Mail.IMAPSMTP == nil ||
		account.Calendar == nil || account.Calendar.CalDAV == nil {
		t.Fatalf("mixed standards account = %#v", account)
	}
	if account.Mail.IMAPSMTP.Credential.Key != "standards-account" ||
		account.Calendar.CalDAV.Credential.Key != "standards-account" ||
		account.Calendar.CalDAV.CalendarPath != "/calendars/reader/main/" {
		t.Fatalf("mixed standards routes = %#v", account)
	}
	if !strings.Contains(stdout.String(), "IMAP") ||
		!strings.Contains(stdout.String(), "CalDAV") ||
		!strings.Contains(stdout.String(), "authentication has not started") {
		t.Fatalf("account output = %q", stdout.String())
	}
}

func TestAccountAddRejectsExplicitGoogleWhileOAuthApprovalIsPending(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:                  domain.ProviderGoogle,
				Confidence:                98,
				Authentication:            application.DiscoveryExplicitOAuth,
				RequiresExplicitSelection: true,
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "reader@gmail.com", Alias: "google",
		Provider:         string(domain.ProviderGoogle),
		OAuthClientID:    "synthetic-public-client.apps.googleusercontent.com",
		OAuthRedirectURI: "http://127.0.0.1:53682/oauth/callback",
		AuthorizationKey: "google-reader",
		ApproveOAuth:     true,
	}
	err := command.Run(app)
	if !errors.Is(err, rollout.ErrGoogleOAuthPending) {
		t.Fatalf("account add error = %v", err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["google"]; exists {
		t.Fatalf("pending Google route was persisted: %#v", configuration)
	}
	if stdout.Len() != 0 {
		t.Fatalf("pending Google output = %q", stdout.String())
	}
}

func TestAccountAddPersistsExplicitGraphPublicClientWithoutAuthorizing(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:                  domain.ProviderMicrosoftGraph,
				Confidence:                0,
				Authentication:            application.DiscoveryExplicitOAuth,
				RequiresExplicitSelection: true,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "api", Value: "https://graph.microsoft.com/v1.0",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "reader@example.test", Alias: "graph",
		Provider:         string(domain.ProviderMicrosoftGraph),
		OAuthClientID:    "synthetic-public-client",
		OAuthRedirectURI: "http://127.0.0.1:53683/oauth/callback",
		AuthorizationKey: "graph-reader",
		ApproveOAuth:     true,
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["graph"]
	if account.Mail == nil || account.Mail.MicrosoftGraph == nil ||
		account.Calendar == nil || account.Calendar.MicrosoftGraph == nil ||
		account.Mail.MicrosoftGraph.ClientID != command.OAuthClientID ||
		account.Mail.MicrosoftGraph.Authorization.Key != command.AuthorizationKey ||
		account.Mail.MicrosoftGraph.Authorization.Backend != config.CredentialOSKeyring {
		t.Fatalf("Graph account = %#v", account)
	}
	if !strings.Contains(stdout.String(), "authentication has not started") {
		t.Fatalf("account output = %q", stdout.String())
	}
}

func TestAccountAddRejectsCalendarOnlyGoogleWhileApprovalIsPending(t *testing.T) {
	discoverer := &accountDiscovererStub{
		observation: application.AccountDiscoveryObservation{
			Candidates: []application.ProviderCandidate{{
				Provider:                  domain.ProviderGoogle,
				Confidence:                90,
				Authentication:            application.DiscoveryExplicitOAuth,
				RequiresExplicitSelection: true,
				Available:                 true,
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic",
				}},
			}},
		},
	}
	app, path, stdout := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "calendar@example.test", Alias: "calendar-only",
		MailProvider:             "none",
		CalendarProvider:         string(domain.ProviderGoogle),
		CalendarOAuthClientID:    "synthetic-calendar-client",
		CalendarOAuthRedirectURI: "http://127.0.0.1:0/oauth/callback",
		CalendarAuthorizationKey: "calendar-only-google",
		ApproveCalendarOAuth:     true,
	}
	err := command.Run(app)
	if !errors.Is(err, rollout.ErrGoogleOAuthPending) {
		t.Fatalf("account add error = %v", err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["calendar-only"]; exists {
		t.Fatalf("pending Google route was persisted: %#v", configuration)
	}
	if stdout.Len() != 0 {
		t.Fatalf("pending Google output = %q", stdout.String())
	}
}

func TestAccountAddPersistsIndependentIMAPAndGraphCalendarRoutes(t *testing.T) {
	discoverer := &accountDiscovererStub{}
	app, path, _ := newAccountCommandRuntime(t, discoverer)
	command := accountAddCommand{
		Address: "mixed@example.test", Alias: "mixed-api",
		MailProvider:             string(domain.ProviderIMAPSMTP),
		CalendarProvider:         string(domain.ProviderMicrosoftGraph),
		Username:                 "mixed@example.test",
		CredentialBackend:        "os-keyring",
		CredentialKey:            "mixed-imap",
		ApproveCredential:        true,
		IMAPHost:                 "imap.example.test",
		IMAPPort:                 993,
		IMAPTLS:                  "implicit",
		SMTPHost:                 "smtp.example.test",
		SMTPPort:                 587,
		SMTPTLS:                  "starttls",
		CalendarOAuthClientID:    "synthetic-graph-client",
		CalendarOAuthRedirectURI: "http://127.0.0.1:0/oauth/callback",
		CalendarAuthorizationKey: "mixed-graph-calendar",
		ApproveCalendarOAuth:     true,
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["mixed-api"]
	if account.Mail == nil || account.Mail.IMAPSMTP == nil ||
		account.Calendar == nil ||
		account.Calendar.MicrosoftGraph == nil ||
		account.Calendar.MicrosoftGraph.Authorization.Key !=
			command.CalendarAuthorizationKey {
		t.Fatalf("mixed IMAP/Graph account = %#v", account)
	}
}

func TestAccountAddRejectsConflictingProviderAliases(t *testing.T) {
	t.Parallel()
	command := accountAddCommand{
		Provider:     string(domain.ProviderJMAP),
		MailProvider: string(domain.ProviderIMAPSMTP),
	}
	if _, err := command.effectiveMailProvider(); err == nil {
		t.Fatal("effectiveMailProvider() accepted conflicting aliases")
	}
}
