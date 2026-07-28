package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestOAuthConsentNoticeCombinesSharedRouteScopesBeforeLogin(t *testing.T) {
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
			Provider: domain.ProviderGoogleAPI, GoogleAPI: &route,
		},
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderGoogleAPI, GoogleAPI: &route,
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
	if err := writeOAuthConsentNotice(app, account); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if strings.Count(output, "google-api:") != 1 ||
		!strings.Contains(output, "gmail.modify") ||
		!strings.Contains(output, "calendar.events") ||
		!strings.Contains(output, "only when no matching valid local grant") {
		t.Fatalf("consent notice = %q", output)
	}
}
