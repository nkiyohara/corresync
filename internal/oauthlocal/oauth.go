// Package oauthlocal owns explicit OAuth public-client authorization for API
// adapters. Grants remain in the OS credential store and never enter config,
// discovery, MCP, or audit output.
package oauthlocal

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/filelock"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/paths"
	"github.com/nkiyohara/corresync/internal/rollout"
)

const (
	keyringService        = "corresync/oauth"
	maximumGrantBytes     = 64 << 10
	maximumClientSecret   = 4 << 10
	authorizationTimeout  = 5 * time.Minute
	defaultRequestTimeout = 30 * time.Second
	refreshLockTimeout    = 30 * time.Second
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
	// ScopeSeparator overrides oauth2's space-separated authorization query.
	// Stored grants retain the individual scope strings.
	ScopeSeparator string
	// Confidential selects HTTP Basic client authentication for the token
	// exchange. ExchangeScope is set when that endpoint explicitly requires the
	// reviewed scope. DisablePKCE and DisableRefresh are set only when the
	// provider's primary contract does not document those mechanisms.
	Confidential   bool
	ExchangeScope  bool
	DisablePKCE    bool
	DisableRefresh bool
}

// Services is the exact service set selected for one public-client grant.
// TaskWrite has meaning only when Tasks is true.
type Services struct {
	Mail           bool
	Calendar       bool
	Tasks          bool
	TaskWrite      bool
	Messages       bool
	MessageWrite   bool
	MicrosoftCloud microsoftcloud.ID
}

// ProviderFor returns the accepted endpoint and scope set for one explicit
// OAuth route. It never performs discovery or authorization.
func ProviderFor(
	provider domain.ProviderID,
	services Services,
) (Provider, error) {
	if services.TaskWrite && !services.Tasks {
		return Provider{}, errors.New("task write scope requires the task service")
	}
	if services.MessageWrite && !services.Messages {
		return Provider{}, errors.New("messaging write scope requires the messaging service")
	}
	var result Provider
	switch provider {
	case domain.ProviderGoogle:
		if services.Tasks || services.TaskWrite || services.Messages || services.MessageWrite ||
			services.MicrosoftCloud != "" {
			return Provider{}, errors.New("google OAuth profile has invalid service options")
		}
		if !rollout.GoogleOAuthApproved {
			return Provider{}, rollout.ErrGoogleOAuthPending
		}
		result = googleProviderProfile(services.Mail, services.Calendar)
	case domain.ProviderGoogleTasks:
		if services.Mail || services.Calendar || services.Messages ||
			services.MessageWrite || !services.Tasks ||
			services.MicrosoftCloud != "" {
			return Provider{}, errors.New("google Tasks OAuth profile has invalid service options")
		}
		if !rollout.GoogleOAuthApproved {
			return Provider{}, rollout.ErrGoogleOAuthPending
		}
		result = googleTaskProviderProfile(services.TaskWrite)
	case domain.ProviderMicrosoftGraph:
		cloud, err := microsoftcloud.Resolve(services.MicrosoftCloud)
		if err != nil {
			return Provider{}, err
		}
		if services.Tasks && !cloud.TasksAvailable {
			return Provider{}, errors.New("the Microsoft To Do API is unavailable in the selected Microsoft cloud")
		}
		result = Provider{
			ID:       provider,
			AuthURL:  cloud.AuthorizationURL,
			TokenURL: cloud.TokenURL,
			Scopes:   []string{"offline_access", "User.Read"},
			AuthParams: map[string]string{
				"prompt": "select_account",
			},
		}
		if services.Mail {
			result.Scopes = append(result.Scopes, "Mail.ReadWrite", "Mail.Send")
		}
		if services.Calendar {
			result.Scopes = append(result.Scopes, "Calendars.ReadWrite")
		}
		if services.Tasks {
			scope := "Tasks.Read"
			if services.TaskWrite {
				scope = "Tasks.ReadWrite"
			}
			result.Scopes = append(result.Scopes, scope)
		}
		if services.Messages {
			result.Scopes = append(result.Scopes,
				"Channel.ReadBasic.All",
				"ChannelMessage.Read.All",
				"Team.ReadBasic.All",
			)
			chatReadScope := "Chat.Read"
			if services.MessageWrite {
				chatReadScope = "Chat.ReadWrite"
				result.Scopes = append(result.Scopes,
					"ChannelMember.ReadWrite.All",
					"ChannelMessage.ReadWrite",
					"ChannelMessage.Send",
					"ChatMember.ReadWrite",
					"ChatMessage.Send",
				)
			}
			result.Scopes = append(result.Scopes, chatReadScope)
		}
	case domain.ProviderTodoist:
		if services.Mail || services.Calendar || services.Messages ||
			services.MessageWrite || services.MicrosoftCloud != "" {
			return Provider{}, errors.New("todoist OAuth profile has invalid service options")
		}
		scope := "data:read"
		if services.TaskWrite {
			scope = "data:read_write"
		}
		result = Provider{ // #nosec G101 -- fixed public OAuth endpoints and scope names, not credentials.
			ID:             provider,
			AuthURL:        "https://app.todoist.com/oauth/authorize",
			TokenURL:       "https://api.todoist.com/oauth/access_token",
			Scopes:         []string{scope},
			ScopeSeparator: ",",
		}
		if services.TaskWrite {
			result.Scopes = append(result.Scopes, "data:delete")
		}
	case domain.ProviderTickTick:
		if services.Mail || services.Calendar || services.Messages ||
			services.MessageWrite || !services.Tasks ||
			services.MicrosoftCloud != "" {
			return Provider{}, errors.New("ticktick OAuth profile has invalid service options")
		}
		scope := "tasks:read"
		if services.TaskWrite {
			scope = "tasks:write"
		}
		result = Provider{ // #nosec G101 -- fixed OAuth endpoints and scope names, not credentials.
			ID:           provider,
			AuthURL:      "https://ticktick.com/oauth/authorize",
			TokenURL:     "https://ticktick.com/oauth/token",
			Scopes:       []string{scope},
			Confidential: true, ExchangeScope: true,
			DisablePKCE: true, DisableRefresh: true,
		}
	case domain.ProviderMicrosoftOWA,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderGoogleWeb,
		domain.ProviderMicrosoftTasks,
		domain.ProviderAppleReminders,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return Provider{}, fmt.Errorf("provider %q has no OAuth API profile", provider)
	default:
		return Provider{}, fmt.Errorf("unknown OAuth provider %q", provider)
	}
	if !services.Mail && !services.Calendar && !services.Tasks && !services.Messages {
		return Provider{}, errors.New("OAuth profile requires a mail, calendar, task, or messaging service")
	}
	slices.Sort(result.Scopes)
	result.Scopes = slices.Compact(result.Scopes)
	return result, nil
}

func googleProviderProfile(mailEnabled, calendarEnabled bool) Provider {
	// #nosec G101 -- these are public OAuth endpoint and scope URLs, not credentials.
	result := Provider{
		ID:       domain.ProviderGoogle,
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		AuthParams: map[string]string{
			"access_type": "offline",
			"hl":          "en",
			"prompt":      "consent",
		},
	}
	if mailEnabled {
		result.Scopes = append(
			result.Scopes,
			"https://www.googleapis.com/auth/gmail.modify",
		)
	}
	if calendarEnabled {
		result.Scopes = append(
			result.Scopes,
			"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
			"https://www.googleapis.com/auth/calendar.events",
		)
	}
	slices.Sort(result.Scopes)
	result.Scopes = slices.Compact(result.Scopes)
	return result
}

func googleTaskProviderProfile(write bool) Provider {
	scope := "https://www.googleapis.com/auth/tasks.readonly"
	if write {
		scope = "https://www.googleapis.com/auth/tasks"
	}
	return Provider{ // #nosec G101 -- fixed public OAuth endpoints and scope URLs, not credentials.
		ID:       domain.ProviderGoogleTasks,
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   []string{"email", "openid", scope},
		AuthParams: map[string]string{
			"access_type": "offline",
			"hl":          "en",
			"prompt":      "consent",
		},
	}
}

type keyringGetter func(service, key string) (string, error)
type keyringSetter func(service, key, value string) error
type browserOpener func(context.Context, string) error

// Options supplies deterministic outer adapters for synthetic tests.
type Options struct {
	HTTP               *http.Client
	Get                keyringGetter
	Set                keyringSetter
	Open               browserOpener
	BeforeOpen         func(Provider)
	GoogleClientSecret string
	// LockDir permits deterministic tests. Production defaults to a private
	// application-state directory and contains only hashed grant handles.
	LockDir string
}

// Manager loads, refreshes, and explicitly creates account-scoped grants.
type Manager struct {
	http         *http.Client
	get          keyringGetter
	set          keyringSetter
	open         browserOpener
	beforeOpen   func(Provider)
	googleSecret string
	lockDir      string
}

// New creates a manager without touching the keyring or network.
func New(options Options) (*Manager, error) {
	if len(options.GoogleClientSecret) > maximumClientSecret ||
		strings.ContainsAny(options.GoogleClientSecret, "\r\n\x00") {
		return nil, errors.New("google OAuth desktop client credential is malformed")
	}
	client, err := secureHTTPClient(options.HTTP)
	if err != nil {
		return nil, err
	}
	lockDir := options.LockDir
	if lockDir == "" {
		stateDir, stateErr := paths.StateDir()
		if stateErr != nil {
			return nil, stateErr
		}
		lockDir = filepath.Join(stateDir, "oauth-locks")
	}
	if !filepath.IsAbs(lockDir) || filepath.Clean(lockDir) != lockDir {
		return nil, errors.New("OAuth lock directory must be clean and absolute")
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
		beforeOpen:   options.BeforeOpen,
		googleSecret: options.GoogleClientSecret,
		lockDir:      lockDir,
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

// Authorization is one account-scoped, refreshable grant projection. The
// refresh token remains encapsulated in the manager-owned token source.
type Authorization interface {
	HTTPClient() *http.Client
	AccessToken(context.Context) ([]byte, error)
}

// ClientCredentialResolver returns a newly allocated confidential-client
// credential only when an authorization-code exchange is required. The OAuth
// manager owns and overwrites the returned bytes.
type ClientCredentialResolver func(context.Context) ([]byte, error)

type authorization struct {
	http   *http.Client
	source oauth2.TokenSource
}

type classifyingTokenSource struct {
	source oauth2.TokenSource
}

func (source classifyingTokenSource) Token() (*oauth2.Token, error) {
	token, err := source.source.Token()
	if err == nil {
		return token, nil
	}
	var retrieval *oauth2.RetrieveError
	if errors.As(err, &retrieval) {
		switch retrieval.ErrorCode {
		case "invalid_grant", "invalid_token", "unauthorized_client":
			return nil, application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonGrantRevoked,
				err,
			)
		}
		if retrieval.Response != nil &&
			retrieval.Response.StatusCode == http.StatusUnauthorized {
			return nil, application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonCredentialRejected,
				err,
			)
		}
	}
	return nil, err
}

func (authorization *authorization) HTTPClient() *http.Client {
	return authorization.http
}

func (authorization *authorization) AccessToken(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token, err := authorization.source.Token()
	if err != nil {
		return nil, err
	}
	if token.AccessToken == "" || len(token.AccessToken) > maximumGrantBytes {
		return nil, errors.New("OAuth access token is empty or too large")
	}
	return []byte(token.AccessToken), nil
}

// Authorize loads an existing valid grant or starts explicit PKCE
// authorization. Callers must invoke it only from the local CLI login boundary.
func (manager *Manager) Authorize(
	ctx context.Context,
	route config.OAuthClient,
	provider Provider,
) (Authorization, error) {
	if provider.Confidential {
		return nil, errors.New("confidential OAuth provider requires an external client credential")
	}
	if (provider.ID == domain.ProviderGoogle || provider.ID == domain.ProviderGoogleTasks) &&
		manager.googleSecret == "" {
		return nil, errors.New(
			"google desktop OAuth requires CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET",
		)
	}
	if route.Authorization.Backend != config.CredentialOSKeyring ||
		!route.Authorization.Consent {
		return nil, errors.New("OAuth authorization handle is not explicitly consented")
	}
	grant, err := manager.load(route, provider)
	if errors.Is(err, keyring.ErrNotFound) ||
		errors.Is(err, errStoredGrantMismatch) {
		grant, err = manager.authorize(
			ctx, route, provider, manager.publicClientSecret(provider),
		)
	}
	if err != nil {
		return nil, err
	}
	return manager.authorization(ctx, route, provider, grant), nil
}

func (manager *Manager) publicClientSecret(provider Provider) string {
	if provider.ID == domain.ProviderGoogle || provider.ID == domain.ProviderGoogleTasks {
		return manager.googleSecret
	}
	return ""
}

// AuthorizeConfidential loads an existing valid grant or starts a documented
// confidential-client authorization. The transient client secret is used only
// for the authorization-code exchange and is never persisted.
func (manager *Manager) AuthorizeConfidential(
	ctx context.Context,
	route config.OAuthClient,
	provider Provider,
	resolve ClientCredentialResolver,
) (Authorization, error) {
	if !provider.Confidential || !provider.DisableRefresh {
		return nil, errors.New("OAuth provider is not a supported non-refreshing confidential client")
	}
	if route.Authorization.Backend != config.CredentialOSKeyring ||
		!route.Authorization.Consent {
		return nil, errors.New("OAuth authorization handle is not explicitly consented")
	}
	grant, err := manager.load(route, provider)
	if errors.Is(err, keyring.ErrNotFound) || errors.Is(err, errStoredGrantMismatch) {
		if resolve == nil {
			return nil, errors.New("OAuth confidential client credential resolver is unavailable")
		}
		clientSecret, resolveErr := resolve(ctx)
		defer overwrite(clientSecret)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if !validClientCredential(clientSecret) {
			return nil, errors.New("OAuth confidential client credential is malformed")
		}
		grant, err = manager.authorize(ctx, route, provider, string(clientSecret))
	}
	if err != nil {
		return nil, err
	}
	return manager.authorization(ctx, route, provider, grant), nil
}

func validClientCredential(value []byte) bool {
	if len(value) == 0 || len(value) > maximumClientSecret {
		return false
	}
	for _, character := range value {
		if character == '\r' || character == '\n' || character == 0 {
			return false
		}
	}
	return true
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (manager *Manager) authorization(
	ctx context.Context,
	route config.OAuthClient,
	provider Provider,
	grant storedGrant,
) Authorization {
	persistedProvider := provider
	persistedProvider.Scopes = append([]string(nil), grant.Scopes...)
	if provider.DisableRefresh {
		source := classifyingTokenSource{source: nonRefreshingTokenSource{token: grant.Token}}
		baseContext := context.WithValue(
			context.WithoutCancel(ctx), oauth2.HTTPClient, manager.http,
		)
		return &authorization{
			http: oauth2.NewClient(baseContext, source), source: source,
		}
	}
	// Refreshable authorization obeys the interactive login context above. The
	// token source must outlive that one RPC without inheriting cancellation.
	baseContext := context.WithValue(
		context.WithoutCancel(ctx),
		oauth2.HTTPClient,
		manager.http,
	)
	persisting := &persistingTokenSource{
		ctx: baseContext, manager: manager, route: route,
		provider: persistedProvider,
	}
	reused := classifyingTokenSource{source: oauth2.ReuseTokenSource(&grant.Token, persisting)}
	return &authorization{
		http: oauth2.NewClient(baseContext, reused), source: reused,
	}
}

type nonRefreshingTokenSource struct {
	token oauth2.Token
}

func (source nonRefreshingTokenSource) Token() (*oauth2.Token, error) {
	if !source.token.Valid() {
		return nil, application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonSessionExpired,
			errors.New("OAuth access token expired and the provider does not document refresh"),
		)
	}
	copy := source.token
	return &copy, nil
}

// Client is the compatibility projection used by HTTP API adapters.
func (manager *Manager) Client(
	ctx context.Context,
	route config.OAuthRoute,
	provider Provider,
) (*http.Client, error) {
	authorization, err := manager.Authorize(ctx, route.Client(), provider)
	if err != nil {
		return nil, err
	}
	return authorization.HTTPClient(), nil
}

func (manager *Manager) load(
	route config.OAuthClient,
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
	providerMatches := grant.Provider == provider.ID ||
		provider.ID == domain.ProviderGoogle &&
			grant.Provider == domain.ProviderID("google-api")
	if grant.Version != 1 ||
		!providerMatches ||
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

func oauthConfig(
	route config.OAuthClient,
	provider Provider,
	clientSecret string,
) oauth2.Config {
	authStyle := oauth2.AuthStyleInParams
	if provider.Confidential {
		authStyle = oauth2.AuthStyleInHeader
	}
	result := oauth2.Config{
		ClientID: route.ClientID, RedirectURL: route.RedirectURI,
		Scopes: append([]string(nil), provider.Scopes...),
		Endpoint: oauth2.Endpoint{
			AuthURL: provider.AuthURL, TokenURL: provider.TokenURL,
			AuthStyle: authStyle,
		},
	}
	if provider.Confidential || provider.ID == domain.ProviderGoogle ||
		provider.ID == domain.ProviderGoogleTasks {
		result.ClientSecret = clientSecret
	}
	return result
}

type persistingTokenSource struct {
	mu       sync.Mutex
	ctx      context.Context
	manager  *Manager
	route    config.OAuthClient
	provider Provider
}

func (source *persistingTokenSource) Token() (*oauth2.Token, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	lockContext, cancel := context.WithTimeout(source.ctx, refreshLockTimeout)
	defer cancel()
	lock, err := filelock.Acquire(
		lockContext,
		source.manager.refreshLockPath(source.route),
	)
	if err != nil {
		return nil, fmt.Errorf("acquire OAuth refresh lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	latest, err := source.manager.load(source.route, source.provider)
	if err != nil {
		return nil, fmt.Errorf("reload OAuth grant before refresh: %w", err)
	}
	if latest.Token.Valid() {
		return &latest.Token, nil
	}
	configuration := oauthConfig(
		source.route,
		source.provider,
		source.manager.publicClientSecret(source.provider),
	)
	token, err := configuration.TokenSource(source.ctx, &latest.Token).Token()
	if err != nil {
		return nil, err
	}
	if err := source.manager.save(source.route, source.provider, *token); err != nil {
		return nil, fmt.Errorf("persist refreshed OAuth grant: %w", err)
	}
	return token, nil
}

func (manager *Manager) refreshLockPath(route config.OAuthClient) string {
	digest := sha256.Sum256([]byte(
		keyringService + "\x00" + route.Authorization.Key,
	))
	return filepath.Join(manager.lockDir, hex.EncodeToString(digest[:])+".lock")
}

func (manager *Manager) save(
	route config.OAuthClient,
	provider Provider,
	token oauth2.Token,
) error {
	if provider.DisableRefresh {
		token.RefreshToken = ""
	}
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
