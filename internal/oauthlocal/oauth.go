// Package oauthlocal owns explicit OAuth public-client authorization for API
// adapters. Grants remain in the OS credential store and never enter config,
// discovery, MCP, or audit output.
package oauthlocal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	keyringService        = "corresync/oauth"
	maximumGrantBytes     = 64 << 10
	authorizationTimeout  = 5 * time.Minute
	defaultRequestTimeout = 30 * time.Second
)

var errStoredGrantMismatch = errors.New("stored OAuth grant needs fresh authorization")

// Provider is a closed OAuth public-client profile with service-derived least
// privilege scopes.
type Provider struct {
	ID         domain.ProviderID
	AuthURL    string
	TokenURL   string
	Scopes     []string
	AuthParams map[string]string
}

// ProviderFor returns the accepted endpoint and scope set for one explicit API
// route. It never performs discovery or authorization.
func ProviderFor(
	provider domain.ProviderID,
	mailEnabled, calendarEnabled bool,
) (Provider, error) {
	var result Provider
	switch provider {
	case domain.ProviderGoogleAPI:
		// #nosec G101 -- these are public OAuth endpoint URLs, not credentials.
		result = Provider{
			ID:       provider,
			AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
			AuthParams: map[string]string{
				"access_type": "offline",
				"prompt":      "consent",
			},
		}
		if mailEnabled {
			result.Scopes = append(
				result.Scopes,
				"https://mail.google.com/",
			)
		}
		if calendarEnabled {
			result.Scopes = append(
				result.Scopes,
				"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
				"https://www.googleapis.com/auth/calendar.events",
			)
		}
	case domain.ProviderMicrosoftGraph:
		// #nosec G101 -- these are public OAuth endpoint URLs, not credentials.
		result = Provider{
			ID:       provider,
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:   []string{"offline_access", "User.Read"},
			AuthParams: map[string]string{
				"prompt": "select_account",
			},
		}
		if mailEnabled {
			result.Scopes = append(result.Scopes, "Mail.ReadWrite", "Mail.Send")
		}
		if calendarEnabled {
			result.Scopes = append(result.Scopes, "Calendars.ReadWrite")
		}
	case domain.ProviderMicrosoftOWA,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderGoogleWeb,
		domain.ProviderPOP3:
		return Provider{}, fmt.Errorf("provider %q has no OAuth API profile", provider)
	default:
		return Provider{}, fmt.Errorf("unknown OAuth provider %q", provider)
	}
	if !mailEnabled && !calendarEnabled {
		return Provider{}, errors.New("OAuth profile requires a mail or calendar service")
	}
	slices.Sort(result.Scopes)
	result.Scopes = slices.Compact(result.Scopes)
	return result, nil
}

type keyringGetter func(service, key string) (string, error)
type keyringSetter func(service, key, value string) error
type browserOpener func(context.Context, string) error

// Options supplies deterministic outer adapters for synthetic tests.
type Options struct {
	HTTP       *http.Client
	Get        keyringGetter
	Set        keyringSetter
	Open       browserOpener
	BeforeOpen func(Provider)
}

// Manager loads, refreshes, and explicitly creates account-scoped grants.
type Manager struct {
	http       *http.Client
	get        keyringGetter
	set        keyringSetter
	open       browserOpener
	beforeOpen func(Provider)
}

// New creates a manager without touching the keyring or network.
func New(options Options) (*Manager, error) {
	client, err := secureHTTPClient(options.HTTP)
	if err != nil {
		return nil, err
	}
	get := options.Get
	if get == nil {
		get = keyring.Get
	}
	set := options.Set
	if set == nil {
		set = keyring.Set
	}
	open := options.Open
	if open == nil {
		open = openBrowser
	}
	return &Manager{
		http: client, get: get, set: set, open: open,
		beforeOpen: options.BeforeOpen,
	}, nil
}

type storedGrant struct {
	Version     int               `json:"version"`
	Provider    domain.ProviderID `json:"provider"`
	ClientID    string            `json:"clientId"`
	RedirectURI string            `json:"redirectUri"`
	Scopes      []string          `json:"scopes"`
	Token       oauth2.Token      `json:"token"`
}

// Client loads an existing valid grant or starts explicit PKCE authorization.
// Callers must invoke it only from the local CLI login boundary.
func (manager *Manager) Client(
	ctx context.Context,
	route config.OAuthRoute,
	provider Provider,
) (*http.Client, error) {
	if route.Authorization.Backend != config.CredentialOSKeyring ||
		!route.Authorization.Consent {
		return nil, errors.New("OAuth authorization handle is not explicitly consented")
	}
	grant, err := manager.load(route, provider)
	if errors.Is(err, keyring.ErrNotFound) ||
		errors.Is(err, errStoredGrantMismatch) {
		grant, err = manager.authorize(ctx, route, provider)
	}
	if err != nil {
		return nil, err
	}
	oauthConfig := oauthConfig(route, provider)
	// Authorization obeys the interactive login context above. The resulting
	// token source must outlive that one RPC so later account-scoped requests
	// can refresh without inheriting an already-cancelled context.
	baseContext := context.WithValue(
		context.WithoutCancel(ctx),
		oauth2.HTTPClient,
		manager.http,
	)
	source := oauthConfig.TokenSource(baseContext, &grant.Token)
	persistedProvider := provider
	persistedProvider.Scopes = append([]string(nil), grant.Scopes...)
	persisting := &persistingTokenSource{
		source: source, manager: manager, route: route,
		provider: persistedProvider, current: grant.Token,
	}
	return oauth2.NewClient(baseContext, oauth2.ReuseTokenSource(&grant.Token, persisting)), nil
}

func (manager *Manager) load(
	route config.OAuthRoute,
	provider Provider,
) (storedGrant, error) {
	raw, err := manager.get(keyringService, route.Authorization.Key)
	if err != nil {
		return storedGrant{}, err
	}
	if raw == "" || len(raw) > maximumGrantBytes {
		return storedGrant{}, errors.New("stored OAuth grant is empty or too large")
	}
	var grant storedGrant
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grant); err != nil {
		return storedGrant{}, errors.New("stored OAuth grant is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return storedGrant{}, errors.New("stored OAuth grant has trailing content")
	}
	if grant.Version != 1 ||
		grant.Provider != provider.ID ||
		grant.ClientID != route.ClientID ||
		grant.RedirectURI != route.RedirectURI ||
		grant.Token.AccessToken == "" ||
		!hasScopes(grant.Scopes, provider.Scopes) {
		return storedGrant{}, fmt.Errorf(
			"%w: %s",
			errStoredGrantMismatch,
			"stored OAuth grant does not match the configured public client and scopes",
		)
	}
	if !grant.Token.Valid() && grant.Token.RefreshToken == "" {
		return storedGrant{}, fmt.Errorf(
			"%w: stored OAuth grant cannot be refreshed",
			errStoredGrantMismatch,
		)
	}
	return grant, nil
}

func hasScopes(granted, required []string) bool {
	set := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		set[scope] = struct{}{}
	}
	for _, scope := range required {
		if _, exists := set[scope]; !exists {
			return false
		}
	}
	return true
}

func oauthConfig(route config.OAuthRoute, provider Provider) oauth2.Config {
	return oauth2.Config{
		ClientID: route.ClientID, RedirectURL: route.RedirectURI,
		Scopes: append([]string(nil), provider.Scopes...),
		Endpoint: oauth2.Endpoint{
			AuthURL: provider.AuthURL, TokenURL: provider.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

type persistingTokenSource struct {
	mu       sync.Mutex
	source   oauth2.TokenSource
	manager  *Manager
	route    config.OAuthRoute
	provider Provider
	current  oauth2.Token
}

func (source *persistingTokenSource) Token() (*oauth2.Token, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	token, err := source.source.Token()
	if err != nil {
		return nil, err
	}
	if token.AccessToken != source.current.AccessToken ||
		token.RefreshToken != source.current.RefreshToken ||
		!token.Expiry.Equal(source.current.Expiry) {
		if err := source.manager.save(source.route, source.provider, *token); err != nil {
			return nil, fmt.Errorf("persist refreshed OAuth grant: %w", err)
		}
		source.current = *token
	}
	return token, nil
}

func (manager *Manager) save(
	route config.OAuthRoute,
	provider Provider,
	token oauth2.Token,
) error {
	grant := storedGrant{
		Version: 1, Provider: provider.ID, ClientID: route.ClientID,
		RedirectURI: route.RedirectURI,
		Scopes:      append([]string(nil), provider.Scopes...),
		Token:       token,
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	if len(encoded) > maximumGrantBytes {
		return errors.New("OAuth grant exceeds the storage limit")
	}
	return manager.set(keyringService, route.Authorization.Key, string(encoded))
}

// DeleteAuthorization removes one Corresync-owned OAuth grant. External
// password credentials use a different owner and are never handled here.
func DeleteAuthorization(key string) error {
	if key == "" || len(key) > 256 ||
		strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("OAuth authorization key is malformed")
	}
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func secureHTTPClient(input *http.Client) (*http.Client, error) {
	var client http.Client
	if input != nil {
		client = *input
	}
	if client.Timeout == 0 {
		client.Timeout = defaultRequestTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("OAuth and API redirects are not accepted")
	}
	var transport *http.Transport
	switch configured := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = configured.Clone()
	default:
		return nil, errors.New("OAuth requires an inspectable HTTP transport")
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("TLS certificate verification cannot be disabled")
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	client.Transport = transport
	return &client, nil
}
