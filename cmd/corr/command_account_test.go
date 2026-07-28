package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
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
	if err := config.Save(path, config.Default()); err != nil {
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
			Provider: domain.ProviderGoogleAPI, Confidence: 98,
			Authentication:            application.DiscoveryExplicitOAuth,
			RequiresExplicitSelection: true,
			Available:                 true,
		}},
	}
	if _, err := selectAccountCandidate(result, "", ""); err == nil {
		t.Fatal("explicit OAuth candidate was automatically selected")
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
