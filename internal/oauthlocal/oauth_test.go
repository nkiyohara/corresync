package oauthlocal

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/rollout"
)

type tokenSourceFunc func() (*oauth2.Token, error)

func (source tokenSourceFunc) Token() (*oauth2.Token, error) {
	return source()
}

func TestClassifyingTokenSourceMapsRevokedGrantWithoutLeakingProviderBody(
	t *testing.T,
) {
	t.Parallel()

	source := classifyingTokenSource{source: tokenSourceFunc(func() (*oauth2.Token, error) {
		return nil, &oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: http.StatusBadRequest},
			Body:      []byte(`{"error":"invalid_grant","private":"secret detail"}`),
			ErrorCode: "invalid_grant",
		}
	})}
	_, err := source.Token()
	reason, ok := application.ProviderAuthenticationReason(err)
	if !ok || reason != application.AuthenticationReasonGrantRevoked ||
		strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("Token() error = %v, reason = %q", err, reason)
	}
}

func TestManagerUsesExplicitPKCEAndPersistsGrantOnlyInKeyring(t *testing.T) {
	t.Parallel()
	var challenge string
	var stored string
	apiServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Error(err)
				http.Error(writer, "bad form", http.StatusBadRequest)
				return
			}
			digest := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
			if request.Form.Get("client_id") != "synthetic-public-client" ||
				request.Form.Get("client_secret") != "synthetic-client-credential" ||
				base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
				t.Errorf("token form = %#v", request.Form)
				http.Error(writer, "bad verifier", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(
				writer,
				`{"access_token":"synthetic-access","refresh_token":"synthetic-refresh",`+
					`"token_type":"Bearer","expires_in":3600}`,
			)
		case "/protected":
			if request.Header.Get("Authorization") != "Bearer synthetic-access" {
				http.Error(writer, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, "ok")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer apiServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(apiServer.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	baseClient := &http.Client{Transport: transport}

	redirectURI := "http://127.0.0.1:0/oauth/callback"
	provider := Provider{
		ID:      domain.ProviderGoogle,
		AuthURL: apiServer.URL + "/authorize", TokenURL: apiServer.URL + "/token",
		Scopes: []string{"mail.read", "calendar.write"},
		AuthParams: map[string]string{
			"access_type": "offline",
			"hl":          "en",
		},
	}
	openCalls := 0
	manager, err := New(Options{
		HTTP:               baseClient,
		GoogleClientSecret: "synthetic-client-credential",
		Get: func(service, key string) (string, error) {
			if service != keyringService || key != "synthetic-grant" {
				t.Fatalf("keyring get = %q %q", service, key)
			}
			if stored == "" {
				return "", keyring.ErrNotFound
			}
			return stored, nil
		},
		Set: func(service, key, value string) error {
			if service != keyringService || key != "synthetic-grant" {
				t.Fatalf("keyring set = %q %q", service, key)
			}
			stored = value
			return nil
		},
		Open: func(ctx context.Context, target string) error {
			openCalls++
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			query := parsed.Query()
			challenge = query.Get("code_challenge")
			if query.Get("code_challenge_method") != "S256" ||
				query.Get("client_id") != "synthetic-public-client" ||
				query.Get("client_secret") != "" ||
				query.Get("access_type") != "offline" ||
				query.Get("hl") != "en" ||
				!strings.Contains(query.Get("scope"), "mail.read") {
				t.Fatalf("authorization query = %s", parsed.RawQuery)
			}
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil {
				return err
			}
			if callback.Port() == "" || callback.Port() == "0" {
				t.Fatalf("authorization callback did not use the bound ephemeral port: %s", callback)
			}
			values := callback.Query()
			values.Set("state", query.Get("state"))
			values.Set("code", "synthetic-code")
			callback.RawQuery = values.Encode()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer func() { _ = response.Body.Close() }()
			_, _ = io.Copy(io.Discard, response.Body)
			if response.StatusCode != http.StatusOK {
				return errorsNewStatus(response.StatusCode)
			}
			for name, expected := range map[string]string{
				"Cache-Control":           "no-store",
				"Content-Security-Policy": "default-src 'none'; base-uri 'none'; frame-ancestors 'none'",
				"Referrer-Policy":         "no-referrer",
				"X-Content-Type-Options":  "nosniff",
			} {
				if response.Header.Get(name) != expected {
					t.Fatalf(
						"authorization callback %s = %q, want %q",
						name,
						response.Header.Get(name),
						expected,
					)
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	route := config.OAuthRoute{
		APIBase: apiServer.URL, ClientID: "synthetic-public-client",
		RedirectURI: redirectURI,
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring,
			Key:     "synthetic-grant",
			Consent: true,
		},
	}
	authorization, err := manager.Authorize(t.Context(), route.Client(), provider)
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := authorization.AccessToken(t.Context())
	if err != nil || string(accessToken) != "synthetic-access" {
		t.Fatalf("AccessToken() = %q, %v", accessToken, err)
	}
	for index := range accessToken {
		accessToken[index] = 0
	}
	client := authorization.HTTPClient()
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		apiServer.URL+"/protected",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "ok" {
		t.Fatalf("protected response = %q, %v", body, err)
	}
	if openCalls != 1 || stored == "" {
		t.Fatalf("authorization calls=%d stored=%t", openCalls, stored != "")
	}
	var grant map[string]any
	if err := json.Unmarshal([]byte(stored), &grant); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "client_secret") ||
		strings.Contains(stored, "synthetic-code") {
		t.Fatalf("stored grant contains transient authorization data: %s", stored)
	}
	grant["provider"] = "google-api"
	legacyStored, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	stored = string(legacyStored)

	second, err := manager.Client(t.Context(), route, provider)
	if err != nil || second == nil {
		t.Fatalf("existing grant Client() = %v, %v", second, err)
	}
	if openCalls != 1 {
		t.Fatalf("existing grant reopened authorization: %d", openCalls)
	}

	provider.Scopes = append(provider.Scopes, "calendar.list.read")
	third, err := manager.Client(t.Context(), route, provider)
	if err != nil || third == nil {
		t.Fatalf("expanded-scope Client() = %v, %v", third, err)
	}
	if openCalls != 2 {
		t.Fatalf("expanded scopes did not start fresh explicit authorization: %d", openCalls)
	}
}

func TestManagerRequiresBoundedGoogleDesktopClientCredential(t *testing.T) {
	t.Parallel()

	provider := googleProviderProfile(true, false)
	manager, err := New(Options{
		Get: func(string, string) (string, error) {
			t.Fatal("missing Google client credential reached the keyring")
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Authorize(t.Context(), config.OAuthClient{}, provider)
	if err == nil ||
		!strings.Contains(err.Error(), "CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET") {
		t.Fatalf("Authorize() error = %v", err)
	}

	_, err = New(Options{GoogleClientSecret: strings.Repeat("x", maximumClientSecret+1)})
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("New() oversized credential error = %v", err)
	}
	for _, value := range []string{"line\rbreak", "line\nbreak", "nul\x00byte"} {
		if _, malformedErr := New(Options{GoogleClientSecret: value}); malformedErr == nil ||
			!strings.Contains(malformedErr.Error(), "malformed") {
			t.Fatalf("New() credential %q error = %v", value, malformedErr)
		}
	}
}

func TestGoogleOAuthProfileIsPresentButApprovalGated(t *testing.T) {
	t.Parallel()

	_, err := ProviderFor(domain.ProviderGoogle, Services{Mail: true, Calendar: true})
	if !errors.Is(err, rollout.ErrGoogleOAuthPending) {
		t.Fatalf("ProviderFor() error = %v", err)
	}
	provider := googleProviderProfile(true, true)
	if !slices.Contains(
		provider.Scopes,
		"https://www.googleapis.com/auth/gmail.modify",
	) || slices.Contains(provider.Scopes, "https://mail.google.com/") ||
		!slices.Contains(
			provider.Scopes,
			"https://www.googleapis.com/auth/calendar.events",
		) {
		t.Fatalf("staged Google scopes = %#v", provider.Scopes)
	}
}

func TestGoogleTaskOAuthProfileIsIndependentAndApprovalGated(t *testing.T) {
	t.Parallel()

	_, err := ProviderFor(domain.ProviderGoogleTasks, Services{Tasks: true})
	if !errors.Is(err, rollout.ErrGoogleOAuthPending) {
		t.Fatalf("ProviderFor() error = %v", err)
	}
	read := googleTaskProviderProfile(false)
	write := googleTaskProviderProfile(true)
	if !slices.Equal(read.Scopes, []string{
		"email", "openid", "https://www.googleapis.com/auth/tasks.readonly",
	}) || !slices.Equal(write.Scopes, []string{
		"email", "openid", "https://www.googleapis.com/auth/tasks",
	}) {
		t.Fatalf("Google Tasks scopes: read=%#v write=%#v", read.Scopes, write.Scopes)
	}
	for _, profile := range []Provider{read, write} {
		for _, scope := range profile.Scopes {
			if strings.Contains(scope, "gmail") || strings.Contains(scope, "calendar") {
				t.Fatalf("Google Tasks profile broadened another service: %#v", profile.Scopes)
			}
		}
	}
}

func TestGoogleDesktopClientCredentialDoesNotCrossIntoGraph(t *testing.T) {
	t.Parallel()

	provider, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{Mail: true, Calendar: true})
	if err != nil {
		t.Fatal(err)
	}
	oauth := oauthConfig(config.OAuthClient{
		ClientID:    "synthetic-graph-client",
		RedirectURI: "http://127.0.0.1:0/oauth/callback",
	}, provider, "synthetic-google-client-credential")
	if oauth.ClientSecret != "" {
		t.Fatal("Google desktop client credential crossed into Microsoft Graph")
	}
}

func TestMicrosoftTodoScopesAndNationalCloudAuthorities(t *testing.T) {
	t.Parallel()
	read, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{
		Tasks: true, MicrosoftCloud: microsoftcloud.GCCHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(read.Scopes, "Tasks.Read") ||
		slices.Contains(read.Scopes, "Tasks.ReadWrite") ||
		slices.Contains(read.Scopes, "Mail.ReadWrite") ||
		read.AuthURL != "https://login.microsoftonline.us/organizations/oauth2/v2.0/authorize" {
		t.Fatalf("GCC High task read profile = %+v", read)
	}
	write, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{
		Tasks: true, TaskWrite: true, MicrosoftCloud: microsoftcloud.DoD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(write.Scopes, "Tasks.ReadWrite") ||
		slices.Contains(write.Scopes, "Tasks.Read") ||
		write.TokenURL != "https://login.microsoftonline.us/organizations/oauth2/v2.0/token" {
		t.Fatalf("DoD task write profile = %+v", write)
	}
	if _, err := ProviderFor(domain.ProviderMicrosoftGraph, Services{
		Tasks: true, MicrosoftCloud: microsoftcloud.China,
	}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("China task profile error = %v", err)
	}
}

func TestTodoistProfileUsesPublicClientPKCEAndCommaSeparatedScopes(t *testing.T) {
	t.Parallel()
	provider, err := ProviderFor(domain.ProviderTodoist, Services{
		Tasks: true, TaskWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.AuthURL != "https://app.todoist.com/oauth/authorize" ||
		provider.TokenURL != "https://api.todoist.com/oauth/access_token" ||
		provider.ScopeSeparator != "," ||
		!slices.Equal(provider.Scopes, []string{"data:delete", "data:read_write"}) {
		t.Fatalf("Todoist profile = %+v", provider)
	}
	route := config.OAuthClient{
		ClientID: "public-client", RedirectURI: "http://127.0.0.1:8765/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "todoist", Consent: true,
		},
	}
	configuration := oauthConfig(route, provider, "")
	authorizationURL := configuration.AuthCodeURL(
		"state", authorizationOptions(provider, "verifier")...,
	)
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("scope") != "data:delete,data:read_write" ||
		parsed.Query().Get("code_challenge_method") != "S256" ||
		parsed.Query().Get("client_secret") != "" {
		t.Fatalf("Todoist authorization query = %s", parsed.RawQuery)
	}
}

func TestTickTickUsesTransientBasicCredentialWithoutPKCEOrRefreshPersistence(t *testing.T) {
	t.Parallel()
	profile, err := ProviderFor(domain.ProviderTickTick, Services{Tasks: true, TaskWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if profile.AuthURL != "https://ticktick.com/oauth/authorize" ||
		profile.TokenURL != "https://ticktick.com/oauth/token" ||
		!profile.Confidential || !profile.ExchangeScope ||
		!profile.DisablePKCE || !profile.DisableRefresh ||
		!slices.Equal(profile.Scopes, []string{"tasks:write"}) {
		t.Fatalf("TickTick profile = %+v", profile)
	}

	var stored string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			clientID, secret, ok := request.BasicAuth()
			if err := request.ParseForm(); err != nil {
				t.Error(err)
				http.Error(writer, "bad form", http.StatusBadRequest)
				return
			}
			if !ok || clientID != "ticktick-client" || secret != "transient-secret" ||
				request.Form.Get("client_id") != "" || request.Form.Get("client_secret") != "" ||
				request.Form.Get("code_verifier") != "" || request.Form.Get("scope") != "tasks:write" {
				t.Errorf("TickTick token exchange auth=%t id=%q form=%#v", ok, clientID, request.Form)
				http.Error(writer, "bad client auth", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer,
				`{"access_token":"ticktick-access","refresh_token":"must-not-persist",`+
					`"token_type":"Bearer","expires_in":3600}`,
			)
		case "/protected":
			if request.Header.Get("Authorization") != "Bearer ticktick-access" {
				http.Error(writer, "missing bearer", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, "ok")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	profile.AuthURL = server.URL + "/authorize"
	profile.TokenURL = server.URL + "/token"
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	baseClient := &http.Client{Transport: transport}
	route := config.OAuthClient{
		ClientID: "ticktick-client", RedirectURI: "http://127.0.0.1:0/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "ticktick-grant", Consent: true,
		},
	}
	manager, err := New(Options{
		HTTP: baseClient, LockDir: t.TempDir(),
		Get: func(_, _ string) (string, error) {
			if stored == "" {
				return "", keyring.ErrNotFound
			}
			return stored, nil
		},
		Set: func(_, _ string, value string) error {
			stored = value
			return nil
		},
		Open: func(ctx context.Context, target string) error {
			parsed, err := url.Parse(target)
			if err != nil {
				return err
			}
			query := parsed.Query()
			if query.Get("code_challenge") != "" || query.Get("code_challenge_method") != "" ||
				query.Get("client_secret") != "" || query.Get("scope") != "tasks:write" {
				t.Fatalf("TickTick authorization query = %s", parsed.RawQuery)
			}
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil {
				return err
			}
			values := callback.Query()
			values.Set("state", query.Get("state"))
			values.Set("code", "ticktick-code")
			callback.RawQuery = values.Encode()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
			if err != nil {
				return err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return err
			}
			defer func() { _ = response.Body.Close() }()
			_, _ = io.Copy(io.Discard, response.Body)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientCredential := []byte("transient-secret")
	authorization, err := manager.AuthorizeConfidential(
		t.Context(), route, profile,
		func(context.Context) ([]byte, error) { return clientCredential, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(clientCredential, make([]byte, len(clientCredential))) {
		t.Fatal("confidential OAuth client credential was not overwritten")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/protected", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := authorization.HTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("protected status = %d", response.StatusCode)
	}
	if strings.Contains(stored, "transient-secret") || strings.Contains(stored, "must-not-persist") {
		t.Fatalf("stored confidential grant retained a client credential or refresh token: %s", stored)
	}
	if _, err := manager.AuthorizeConfidential(
		t.Context(), route, profile,
		func(context.Context) ([]byte, error) {
			t.Fatal("valid stored grant unexpectedly resolved the client credential")
			return nil, errors.New("unexpected credential resolution")
		},
	); err != nil {
		t.Fatalf("reuse stored confidential grant: %v", err)
	}
}

func TestNonRefreshingOAuthExpiryRequiresInteractiveRecovery(t *testing.T) {
	t.Parallel()
	source := nonRefreshingTokenSource{token: oauth2.Token{
		AccessToken: "expired", Expiry: time.Now().Add(-time.Minute),
	}}
	_, err := source.Token()
	reason, ok := application.ProviderAuthenticationReason(err)
	if !ok || reason != application.AuthenticationReasonSessionExpired {
		t.Fatalf("expired non-refreshing token error = %v, reason = %q", err, reason)
	}
}

func TestConfidentialOAuthOverwritesCredentialReturnedWithAnError(t *testing.T) {
	t.Parallel()
	manager, err := New(Options{Get: func(string, string) (string, error) {
		return "", keyring.ErrNotFound
	}})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := ProviderFor(
		domain.ProviderTickTick,
		Services{Tasks: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	credential := []byte("must-be-overwritten")
	_, err = manager.AuthorizeConfidential(
		t.Context(),
		config.OAuthClient{
			ClientID:    "synthetic-client",
			RedirectURI: "http://127.0.0.1:53685/callback",
			Authorization: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     "missing-confidential-grant",
				Consent: true,
			},
		},
		profile,
		func(context.Context) ([]byte, error) {
			return credential, errors.New("synthetic credential owner failure")
		},
	)
	if err == nil || !slices.Equal(credential, make([]byte, len(credential))) {
		t.Fatalf("AuthorizeConfidential() error = %v, credential = %q", err, credential)
	}
}

func TestRefreshRotationIsAtomicAcrossManagers(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	refreshes := 0
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			http.Error(writer, "bad form", http.StatusBadRequest)
			return
		}
		if request.Form.Get("refresh_token") != "refresh-1" ||
			request.Form.Get("client_id") != "public-client" ||
			request.Form.Get("client_secret") != "" {
			t.Errorf("refresh form = %#v", request.Form)
			http.Error(writer, "bad refresh", http.StatusBadRequest)
			return
		}
		mu.Lock()
		refreshes++
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer,
			`{"access_token":"access-2","refresh_token":"refresh-2",`+
				`"token_type":"Bearer","expires_in":3600}`,
		)
	}))
	defer tokenServer.Close()

	provider := Provider{
		ID: domain.ProviderTodoist, AuthURL: tokenServer.URL + "/authorize",
		TokenURL: tokenServer.URL, Scopes: []string{"data:read_write"},
	}
	route := config.OAuthClient{
		ClientID: "public-client", RedirectURI: "http://127.0.0.1:8765/oauth/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "rotating-grant", Consent: true,
		},
	}
	initial, err := json.Marshal(storedGrant{
		Version: 1, Provider: domain.ProviderTodoist,
		ClientID: route.ClientID, RedirectURI: route.RedirectURI,
		Scopes: provider.Scopes,
		Token: oauth2.Token{
			AccessToken: "access-1", RefreshToken: "refresh-1",
			TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := string(initial)
	get := func(service, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if service != keyringService || key != route.Authorization.Key {
			t.Fatalf("keyring get = %q %q", service, key)
		}
		return stored, nil
	}
	set := func(service, key, value string) error {
		mu.Lock()
		defer mu.Unlock()
		if service != keyringService || key != route.Authorization.Key {
			t.Fatalf("keyring set = %q %q", service, key)
		}
		stored = value
		return nil
	}
	lockDir := t.TempDir()
	makeManager := func() *Manager {
		manager, err := New(Options{
			HTTP: tokenServer.Client(), Get: get, Set: set,
			LockDir: lockDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	first, err := makeManager().Authorize(t.Context(), route, provider)
	if err != nil {
		t.Fatal(err)
	}
	// Both managers intentionally load the expired, pre-rotation grant before
	// either is allowed to refresh it.
	second, err := makeManager().Authorize(t.Context(), route, provider)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	errorsFound := make(chan error, 2)
	for _, authorization := range []Authorization{first, second} {
		go func() {
			defer wait.Done()
			token, err := authorization.AccessToken(context.Background())
			if err != nil {
				errorsFound <- err
				return
			}
			if string(token) != "access-2" {
				errorsFound <- errors.New("unexpected refreshed access token")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshes != 1 || !strings.Contains(stored, `"refresh_token":"refresh-2"`) {
		t.Fatalf("refreshes=%d stored=%s", refreshes, stored)
	}
}

func TestManagerRejectsTLSVerificationBypass(t *testing.T) {
	t.Parallel()
	// #nosec G402 -- the intentionally unsafe transport proves rejection.
	unsafeTLS := &tls.Config{InsecureSkipVerify: true}
	_, err := New(Options{HTTP: &http.Client{Transport: &http.Transport{
		TLSClientConfig: unsafeTLS,
	}}})
	if err == nil || !strings.Contains(err.Error(), "cannot be disabled") {
		t.Fatalf("New() error = %v", err)
	}
}

func errorsNewStatus(status int) error {
	return &url.Error{Op: "callback", URL: "loopback", Err: &statusError{status: status}}
}

type statusError struct {
	status int
}

func (err *statusError) Error() string {
	return http.StatusText(err.status)
}
