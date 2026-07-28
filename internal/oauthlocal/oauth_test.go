package oauthlocal

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

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
				request.Form.Get("client_secret") != "" ||
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

	redirectURI := unusedLoopbackURI(t)
	provider := Provider{
		ID:      domain.ProviderGoogleAPI,
		AuthURL: apiServer.URL + "/authorize", TokenURL: apiServer.URL + "/token",
		Scopes: []string{"mail.read", "calendar.write"},
		AuthParams: map[string]string{
			"access_type": "offline",
		},
	}
	openCalls := 0
	manager, err := New(Options{
		HTTP: baseClient,
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
				!strings.Contains(query.Get("scope"), "mail.read") {
				t.Fatalf("authorization query = %s", parsed.RawQuery)
			}
			callback, err := url.Parse(query.Get("redirect_uri"))
			if err != nil {
				return err
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
	client, err := manager.Client(t.Context(), route, provider)
	if err != nil {
		t.Fatal(err)
	}
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

	second, err := manager.Client(t.Context(), route, provider)
	if err != nil || second == nil {
		t.Fatalf("existing grant Client() = %v, %v", second, err)
	}
	if openCalls != 1 {
		t.Fatalf("existing grant reopened authorization: %d", openCalls)
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

func unusedLoopbackURI(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + address + "/oauth/callback"
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
