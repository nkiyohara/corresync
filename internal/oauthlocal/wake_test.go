package oauthlocal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

type wakeTokenEndpoint int32

const (
	wakeTokenOffline wakeTokenEndpoint = iota
	wakeTokenOutage
	wakeTokenRevoked
	wakeTokenAvailable
)

// TestAccessTokenThatExpiredDuringSleepRecoversWhenTheNetworkReturns models a
// daemon whose stored access token expired while the machine slept. The first
// requests after wake run while the network is still unavailable or the token
// endpoint is briefly failing; neither may look like a sign-out or consume the
// stored refresh grant. When the network returns, the same authorization
// refreshes once and the API request succeeds without a new login.
func TestAccessTokenThatExpiredDuringSleepRecoversWhenTheNetworkReturns(t *testing.T) {
	t.Parallel()
	harness := newWakeOAuthHarness(t)
	authorization := harness.authorize(t)

	for _, mode := range []wakeTokenEndpoint{wakeTokenOffline, wakeTokenOutage} {
		harness.mode.Store(int32(mode))
		err := harness.callAPI(t, authorization)
		if err == nil {
			t.Fatalf("mode %d: API call succeeded without a usable token", mode)
		}
		if _, rejected := application.ProviderAuthenticationReason(err); rejected {
			t.Fatalf("mode %d: transient token failure looked like sign-out: %v", mode, err)
		}
		if strings.Contains(err.Error(), "refresh-1") ||
			strings.Contains(err.Error(), "private") {
			t.Fatalf("mode %d: error exposed grant detail: %v", mode, err)
		}
	}
	if harness.apiCalls.Load() != 0 {
		t.Fatalf("API received %d requests without a refreshed token", harness.apiCalls.Load())
	}
	if !strings.Contains(harness.storedGrant(), `"refresh_token":"refresh-1"`) {
		t.Fatal("a transient refresh failure changed the stored grant")
	}

	harness.mode.Store(int32(wakeTokenAvailable))
	if err := harness.callAPI(t, authorization); err != nil {
		t.Fatalf("API call after the network returned: %v", err)
	}
	if err := harness.callAPI(t, authorization); err != nil {
		t.Fatalf("second API call after recovery: %v", err)
	}
	if harness.successfulRefreshes.Load() != 1 || harness.apiCalls.Load() != 2 {
		t.Fatalf(
			"successful refreshes = %d, API calls = %d",
			harness.successfulRefreshes.Load(), harness.apiCalls.Load(),
		)
	}
}

// TestRefreshRejectedAfterSleepRequiresInteractiveLogin proves that a grant
// revoked while the machine slept is reported as an authentication action,
// not as a transient failure, and never reaches the API.
func TestRefreshRejectedAfterSleepRequiresInteractiveLogin(t *testing.T) {
	t.Parallel()
	harness := newWakeOAuthHarness(t)
	authorization := harness.authorize(t)
	harness.mode.Store(int32(wakeTokenRevoked))

	err := harness.callAPI(t, authorization)
	reason, rejected := application.ProviderAuthenticationReason(err)
	if !rejected || reason != application.AuthenticationReasonGrantRevoked ||
		strings.Contains(err.Error(), "private") {
		t.Fatalf("revoked refresh error = %v, reason = %q", err, reason)
	}
	if harness.apiCalls.Load() != 0 || harness.tokenRequests.Load() != 1 {
		t.Fatalf(
			"API calls = %d, token requests = %d",
			harness.apiCalls.Load(), harness.tokenRequests.Load(),
		)
	}
}

type wakeOAuthHarness struct {
	server              *httptest.Server
	manager             *Manager
	route               config.OAuthClient
	provider            Provider
	mode                atomic.Int32
	tokenRequests       atomic.Int32
	successfulRefreshes atomic.Int32
	apiCalls            atomic.Int32
	mu                  sync.Mutex
	stored              string
}

func newWakeOAuthHarness(t *testing.T) *wakeOAuthHarness {
	t.Helper()
	harness := &wakeOAuthHarness{}
	harness.server = httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/token":
			harness.tokenRequests.Add(1)
			switch wakeTokenEndpoint(harness.mode.Load()) {
			case wakeTokenOutage:
				http.Error(writer, `{"error":"temporarily_unavailable","private":"x"}`, http.StatusServiceUnavailable)
			case wakeTokenRevoked:
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"private"}`)
			case wakeTokenOffline, wakeTokenAvailable:
				harness.successfulRefreshes.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer,
					`{"access_token":"access-2","refresh_token":"refresh-2",`+
						`"token_type":"Bearer","expires_in":3600}`,
				)
			}
		case "/api/messages":
			if request.Header.Get("Authorization") != "Bearer access-2" {
				http.Error(writer, "stale token", http.StatusUnauthorized)
				return
			}
			harness.apiCalls.Add(1)
			_, _ = io.WriteString(writer, `{"ok":true}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(harness.server.Close)

	harness.provider = Provider{
		ID: domain.ProviderTodoist, AuthURL: harness.server.URL + "/authorize",
		TokenURL: harness.server.URL + "/token", Scopes: []string{"data:read"},
	}
	harness.route = config.OAuthClient{
		ClientID: "public-client", RedirectURI: "http://127.0.0.1:8765/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "wake-grant", Consent: true,
		},
	}
	initial, err := json.Marshal(storedGrant{
		Version: storedGrantVersion, Provider: harness.provider.ID,
		Profile:  providerGrantProfile(harness.provider),
		ClientID: harness.route.ClientID, RedirectURI: harness.route.RedirectURI,
		Scopes: harness.provider.Scopes,
		Token: oauth2.Token{
			AccessToken: "access-1", RefreshToken: "refresh-1", TokenType: "Bearer",
			// The access token expired while the machine was asleep.
			Expiry: time.Now().Add(-8 * time.Hour),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.stored = string(initial)

	// The base transport dials the synthetic server only while the network is
	// up. Keep-alives are disabled so a pooled connection cannot outlive the
	// simulated loss of the network.
	base := harness.server.Client().Transport.(*http.Transport).Clone()
	base.DisableKeepAlives = true
	address := harness.server.Listener.Addr().String()
	base.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if wakeTokenEndpoint(harness.mode.Load()) == wakeTokenOffline {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ENETUNREACH}
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	base.TLSClientConfig = base.TLSClientConfig.Clone()
	base.TLSClientConfig.MinVersion = tls.VersionTLS12
	harness.manager, err = New(Options{
		HTTP: &http.Client{Transport: base},
		Get: func(string, string) (string, error) {
			harness.mu.Lock()
			defer harness.mu.Unlock()
			return harness.stored, nil
		},
		Set: func(_, _, value string) error {
			harness.mu.Lock()
			defer harness.mu.Unlock()
			harness.stored = value
			return nil
		},
		LockDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func (harness *wakeOAuthHarness) authorize(t *testing.T) Authorization {
	t.Helper()
	authorization, err := harness.manager.Authorize(
		t.Context(), harness.route, harness.provider,
	)
	if err != nil {
		t.Fatal(err)
	}
	return authorization
}

func (harness *wakeOAuthHarness) callAPI(t *testing.T, authorization Authorization) error {
	t.Helper()
	request, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, harness.server.URL+"/api/messages", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := authorization.HTTPClient().Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("synthetic API rejected the request")
	}
	return nil
}

func (harness *wakeOAuthHarness) storedGrant() string {
	harness.mu.Lock()
	defer harness.mu.Unlock()
	return harness.stored
}
