package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/rollout"
)

func TestOAuthConsentNoticeStopsBeforePendingGoogleLogin(t *testing.T) {
	t.Parallel()
	route := config.OAuthRoute{
		APIBase:     "https://www.googleapis.com",
		ClientID:    "synthetic-client",
		RedirectURI: "http://127.0.0.1:0/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring,
			Key:     "synthetic-google",
			Consent: true,
		},
	}
	account := config.Account{
		Mail: &config.MailRoute{
			Provider: domain.ProviderGoogle,
			Google: &config.GoogleMailRoute{
				Username: "reader@example.test",
				ClientID: route.ClientID, RedirectURI: route.RedirectURI,
				Authorization: route.Authorization,
			},
		},
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderGoogle, Google: &route,
		},
	}
	var stderr bytes.Buffer
	app := newRuntime(
		t.Context(),
		"",
		&bytes.Buffer{},
		&stderr,
		buildinfo.Info{Version: "dev"},
	)
	err := writeOAuthConsentNotice(app, account)
	if !errors.Is(err, rollout.ErrGoogleOAuthPending) {
		t.Fatalf("writeOAuthConsentNotice() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("pending Google consent notice = %q", stderr.String())
	}
}

func TestOAuthConsentsMergeOnlyAnExplicitlySharedGraphGrant(t *testing.T) {
	t.Parallel()
	shared := config.OAuthRoute{
		APIBase: "https://graph.microsoft.us/v1.0", MicrosoftCloud: microsoftcloud.GCCHigh,
		ClientID: "synthetic-client", RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "shared", Consent: true,
		},
	}
	account := config.Account{
		Mail: &config.MailRoute{
			Provider: domain.ProviderMicrosoftGraph, MicrosoftGraph: &shared,
		},
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &config.MicrosoftGraphTaskRoute{
				OAuth: shared, ReadOnly: true,
			},
		},
	}
	routes, err := accountOAuthConsents(account)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || !routes[0].services.Mail || !routes[0].services.Tasks ||
		routes[0].services.TaskWrite {
		t.Fatalf("shared Graph consent routes = %+v", routes)
	}
	provider, err := oauthlocal.ProviderFor(routes[0].provider, routes[0].services)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(provider.Scopes, "Mail.ReadWrite") ||
		!slices.Contains(provider.Scopes, "Tasks.Read") ||
		slices.Contains(provider.Scopes, "Tasks.ReadWrite") {
		t.Fatalf("shared Graph scopes = %#v", provider.Scopes)
	}
	label, err := oauthConsentLabel(routes[0])
	if err != nil || !strings.Contains(label, "gcc-high") ||
		!strings.Contains(label, "https://graph.microsoft.us/v1.0") {
		t.Fatalf("Graph consent label = %q, %v", label, err)
	}

	distinct := shared
	distinct.Authorization.Key = "task-only"
	account.Tasks.MicrosoftGraph.OAuth = distinct
	routes, err = accountOAuthConsents(account)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("distinct Graph consent routes = %+v", routes)
	}
	for _, route := range routes {
		if route.route.Authorization.Key == "task-only" &&
			(route.services.Mail || !route.services.Tasks || route.services.TaskWrite) {
			t.Fatalf("independent task consent = %+v", route)
		}
	}

	legacyGlobal := shared
	legacyGlobal.APIBase = "https://graph.microsoft.com/v1.0"
	legacyGlobal.MicrosoftCloud = ""
	explicitGlobal := legacyGlobal
	explicitGlobal.MicrosoftCloud = microsoftcloud.Global
	account.Mail.MicrosoftGraph = &legacyGlobal
	account.Tasks.MicrosoftGraph.OAuth = explicitGlobal
	routes, err = accountOAuthConsents(account)
	if err != nil || len(routes) != 1 || !routes[0].services.Mail || !routes[0].services.Tasks {
		t.Fatalf("canonical Global consent routes = %+v, %v", routes, err)
	}
}
