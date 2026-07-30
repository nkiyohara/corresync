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
	if err := writeOAuthConsentNotice(app, account); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if strings.Count(output, "google:") != 1 ||
		!strings.Contains(output, "mail.google.com") ||
		!strings.Contains(output, "calendar.events") ||
		!strings.Contains(output, "only when no matching valid local grant") ||
		!strings.Contains(output, privacyPolicyURL) ||
		!strings.Contains(output, termsOfUseURL) {
		t.Fatalf("consent notice = %q", output)
	}
}
