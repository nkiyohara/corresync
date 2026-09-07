package oauthlocal

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
)

func TestStoredGrantBindsMicrosoftCloudOnReuseAndRefresh(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]microsoftcloud.ID{
		{microsoftcloud.Global, microsoftcloud.China},
		{microsoftcloud.GCCHigh, microsoftcloud.DoD},
	} {
		t.Run(string(pair[0])+"-to-"+string(pair[1]), func(t *testing.T) {
			t.Parallel()
			original, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{Mail: true, MicrosoftCloud: pair[0]})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{Mail: true, MicrosoftCloud: pair[1]})
			if err != nil {
				t.Fatal(err)
			}
			route := securityTestRoute()
			var stored string
			opens := 0
			interrupted := errors.New("synthetic browser did not authorize")
			manager, err := New(Options{
				LockDir: t.TempDir(),
				Get:     func(string, string) (string, error) { return stored, nil },
				Set:     func(_, _, value string) error { stored = value; return nil },
				Open:    func(context.Context, string) error { opens++; return interrupted },
			})
			if err != nil {
				t.Fatal(err)
			}
			token := oauth2.Token{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Expiry: time.Now().Add(time.Hour)}
			if err := manager.save(route, original, token, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.load(route, original); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.load(route, selected); !errors.Is(err, errStoredGrantMismatch) {
				t.Fatalf("cross-cloud load = %v", err)
			}
			if _, err := manager.Authorize(t.Context(), route, selected); !errors.Is(err, interrupted) || opens != 1 {
				t.Fatalf("cross-cloud login = %v; browser opens = %d", err, opens)
			}
			// Refresh cannot create an interactive authorization or send the old
			// refresh token to the newly configured authority.
			source := classifyingTokenSource{source: &persistingTokenSource{
				ctx: t.Context(), manager: manager, route: route, provider: selected,
				observed: newObservedScopeSet(nil),
			}}
			if _, err := source.Token(); !errors.Is(err, errStoredGrantMismatch) {
				t.Fatalf("cross-cloud refresh = %v", err)
			} else if reason, ok := application.ProviderAuthenticationReason(err); !ok || reason != application.AuthenticationReasonInteractionRequired {
				t.Fatalf("cross-cloud refresh reason = %q, %t", reason, ok)
			}
			if opens != 1 {
				t.Fatal("refresh opened authorization")
			}
		})
	}
}

func TestStoredGrantRejectsLegacyAndChangedProviderProfiles(t *testing.T) {
	t.Parallel()
	provider, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{Mail: true})
	if err != nil {
		t.Fatal(err)
	}
	route := securityTestRoute()
	base := storedGrant{
		Version: storedGrantVersion, Provider: provider.ID, Profile: providerGrantProfile(provider),
		ClientID: route.ClientID, RedirectURI: route.RedirectURI, Scopes: provider.Scopes,
		Token: oauth2.Token{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", Expiry: time.Now().Add(time.Hour)},
	}
	cases := map[string]func(*storedGrant){
		"legacy": func(grant *storedGrant) { grant.Version = 1; grant.Profile = grantProviderProfile{} },
		"legacy-google": func(grant *storedGrant) {
			grant.Version = 1
			grant.Provider = "google-api"
			grant.Profile = grantProviderProfile{}
		},
		"missing-profile":        func(grant *storedGrant) { grant.Profile = grantProviderProfile{} },
		"authorization-endpoint": func(grant *storedGrant) { grant.Profile.AuthURL = "https://other.example/authorize" },
		"token-endpoint":         func(grant *storedGrant) { grant.Profile.TokenURL = "https://other.example/token" },
		"protocol":               func(grant *storedGrant) { grant.Profile.DisablePKCE = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			grant := base
			mutate(&grant)
			encoded, err := json.Marshal(grant)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := New(Options{LockDir: t.TempDir(), Get: func(string, string) (string, error) { return string(encoded), nil }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.load(route, provider); !errors.Is(err, errStoredGrantMismatch) {
				t.Fatalf("unbound grant load = %v", err)
			}
		})
	}
	// Narrower selected scopes preserve a correctly bound superset grant.
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{LockDir: t.TempDir(), Get: func(string, string) (string, error) { return string(encoded), nil }})
	if err != nil {
		t.Fatal(err)
	}
	provider.Scopes = []string{"User.Read"}
	if _, err := manager.load(route, provider); err != nil {
		t.Fatalf("narrowed selection = %v", err)
	}
}

func TestOAuthFailuresKeepOnlyBoundedStatusAndAuthenticationClassification(t *testing.T) {
	t.Parallel()
	private := "synthetic-private-provider-detail"
	cases := []struct {
		name     string
		failure  error
		reason   application.AuthenticationReason
		sentinel error
		text     string
	}{
		{name: "body", failure: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusInternalServerError}, Body: []byte(private)}, text: "OAuth token request failed (HTTP 500)"},
		{name: "description", failure: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusBadRequest}, ErrorCode: "invalid_scope", ErrorDescription: private}, text: "OAuth token request failed (HTTP 400)"},
		{name: "revoked", failure: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusBadRequest}, ErrorCode: "invalid_grant", ErrorDescription: private}, reason: application.AuthenticationReasonGrantRevoked},
		{name: "rejected", failure: &oauth2.RetrieveError{Response: &http.Response{StatusCode: http.StatusUnauthorized}, Body: []byte(private)}, reason: application.AuthenticationReasonCredentialRejected},
		{name: "transport", failure: errors.New(private), text: "OAuth token request failed"},
		{name: "cancelled", failure: fmt.Errorf("%s: %w", private, context.Canceled), sentinel: context.Canceled},
		{name: "deadline", failure: fmt.Errorf("%s: %w", private, context.DeadlineExceeded), sentinel: context.DeadlineExceeded},
		{name: "typed", failure: fmt.Errorf("%s: %w", private, application.NewProviderAuthenticationFailure(application.AuthenticationReasonSessionExpired, errors.New(private))), reason: application.AuthenticationReasonSessionExpired},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := classifyingTokenSource{source: tokenSourceFunc(func() (*oauth2.Token, error) { return nil, test.failure })}
			_, err := source.Token()
			if err == nil || strings.Contains(err.Error(), private) {
				t.Fatalf("unsafe OAuth failure = %v", err)
			}
			if test.text != "" && err.Error() != test.text {
				t.Fatalf("OAuth failure = %v", err)
			}
			if test.sentinel != nil && !errors.Is(err, test.sentinel) {
				t.Fatalf("lost cancellation = %v", err)
			}
			if test.reason != "" {
				reason, ok := application.ProviderAuthenticationReason(err)
				if !ok || reason != test.reason {
					t.Fatalf("reason = %q, %t", reason, ok)
				}
			}
		})
	}
}

func TestOAuthExchangeHidesProviderDescription(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"synthetic-private-authentication-detail"}`)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	manager, err := New(Options{
		HTTP: &http.Client{Transport: transport}, LockDir: t.TempDir(),
		Open: func(ctx context.Context, target string) error {
			authorizationURL, err := url.Parse(target)
			if err != nil {
				return err
			}
			callback, err := url.Parse(authorizationURL.Query().Get("redirect_uri"))
			if err != nil {
				return err
			}
			callback.RawQuery = url.Values{"state": {authorizationURL.Query().Get("state")}, "code": {"synthetic-code"}}.Encode()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			return response.Body.Close()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.authorize(t.Context(), securityTestRoute(), Provider{
		ID: domain.ProviderMicrosoftGraph, AuthURL: "https://synthetic.example/authorize", TokenURL: server.URL, Scopes: []string{"User.Read"},
	}, "")
	if err == nil || strings.Contains(err.Error(), "synthetic-private-authentication-detail") {
		t.Fatalf("exchange error = %v", err)
	}
	if reason, ok := application.ProviderAuthenticationReason(err); !ok || reason != application.AuthenticationReasonGrantRevoked {
		t.Fatalf("exchange reason = %q, %t", reason, ok)
	}
}

func securityTestRoute() config.OAuthClient {
	return config.OAuthClient{
		ClientID: "synthetic-client", RedirectURI: "http://127.0.0.1:0/callback",
		Authorization: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "synthetic-grant", Consent: true},
	}
}
