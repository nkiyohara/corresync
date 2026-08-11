package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
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
