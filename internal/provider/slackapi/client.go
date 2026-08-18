// Package slackapi adapts one explicitly installed Slack workspace to the
// provider-neutral messaging port through Slack's supported Web API.
package slackapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const (
	maximumSlackScopes = 128
	maximumSlackCode   = 128
	maximumRetryAfter  = 24 * time.Hour
)

// Options selects one already authorized, account-scoped Slack installation.
// HTTP owns bearer authorization; this package never accepts or exposes the
// token itself.
type Options struct {
	APIBase     string
	WorkspaceID string
	ReadOnly    bool
	HTTP        *http.Client
	// FilesHTTP owns authorization for Slack's distinct, fixed file origin.
	// Synthetic tests may omit it when their API and file fixtures share one
	// origin; production construction always supplies the separate client.
	FilesHTTP *http.Client
}

// Client owns one authorized Slack Web API transport.
type Client struct {
	api               *restapi.Client
	files             *http.Client
	apiHost           string
	workspaceID       string
	actor             application.MessageActor
	capabilities      application.MessageCapabilities
	degradations      []domain.Degradation
	conversationTypes string
	readOnly          bool
}

type slackEnvelope struct {
	OK       bool                  `json:"ok"`
	Error    string                `json:"error,omitempty"`
	Warning  string                `json:"warning,omitempty"`
	Metadata slackResponseMetadata `json:"response_metadata,omitempty"`
}

type slackResponseMetadata struct {
	NextCursor string   `json:"next_cursor,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// RateLimitError preserves Slack's bounded Retry-After instruction without
// retrying an operation or exposing response content.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (failure *RateLimitError) Error() string {
	return fmt.Sprintf("slack API rate limit; retry after %s", failure.RetryAfter)
}

// APIError is one bounded provider code. Slack error messages and response
// bodies are intentionally not retained.
type APIError struct {
	Code string
}

func (failure *APIError) Error() string { return "slack API returned " + failure.Code }

// New confirms the exact workspace and actor and observes granted scopes from
// Slack's response header. It never starts installation or authorization.
func New(ctx context.Context, options Options) (*Client, error) {
	if !validSlackID(options.WorkspaceID) {
		return nil, errors.New("slack workspace ID is malformed")
	}
	api, err := restapi.New(restapi.Options{BaseURL: options.APIBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	parsedBase, _ := url.Parse(options.APIBase)
	filesHTTP := options.FilesHTTP
	if filesHTTP == nil {
		filesHTTP = options.HTTP
	}
	fileHTTP := *filesHTTP
	if fileHTTP.Timeout == 0 {
		fileHTTP.Timeout = 30 * time.Second
	}
	fileHTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("slack file redirects are not accepted")
	}
	var identity struct {
		slackEnvelope
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id,omitempty"`
		User   string `json:"user,omitempty"`
	}
	result, err := api.DoJSON(
		ctx, http.MethodPost, "auth.test", nil, struct{}{}, &identity,
		false, nil, http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Slack identity: %w", err)
	}
	if err := validateSlackResponse(result, identity.slackEnvelope, false); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Slack identity: %w", err)
	}
	if identity.TeamID != options.WorkspaceID || !validSlackID(identity.UserID) {
		_ = api.Close()
		return nil, errors.New("slack authorization identity does not match the configured workspace")
	}
	mode := application.MessageActorDelegatedUser
	if identity.BotID != "" {
		if !validSlackID(identity.BotID) {
			_ = api.Close()
			return nil, errors.New("slack returned a malformed bot identity")
		}
		mode = application.MessageActorApp
	}
	actor := application.MessageActor{ID: identity.UserID, Mode: mode, DisplayName: boundedSlackText(identity.User, 1024)}
	scopes, scopeErr := parseSlackScopes(result.Header.Values("X-Oauth-Scopes"))
	if scopeErr != nil {
		_ = api.Close()
		return nil, scopeErr
	}
	client := &Client{
		api: api, workspaceID: options.WorkspaceID, actor: actor,
		readOnly: options.ReadOnly, files: &fileHTTP, apiHost: parsedBase.Host,
	}
	client.observeCapabilities(scopes)
	client.degradations = append(client.degradations, slackWarningDegradations(identity.slackEnvelope)...)
	return client, nil
}

// FileOrigin returns the one official private-file origin paired with a
// selectable Slack API base. Keeping the mapping in the adapter prevents an
// arbitrary response URL from choosing where bearer authorization is sent.
func FileOrigin(apiBase string) (string, error) {
	switch apiBase {
	case "https://slack.com/api":
		return "https://files.slack.com", nil
	case "https://slack-gov.com/api":
		return "https://files.slack-gov.com", nil
	default:
		return "", errors.New("slack API base has no approved file origin")
	}
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	if client.files != nil {
		client.files.CloseIdleConnections()
	}
	return client.api.Close()
}

func (client *Client) MessageActor() application.MessageActor { return client.actor }

func (client *Client) MessageCapabilities() application.MessageCapabilities {
	return client.capabilities
}

func (client *Client) MessageDegradations() []domain.Degradation {
	return append([]domain.Degradation(nil), client.degradations...)
}

func (client *Client) observeCapabilities(scopes map[string]struct{}) {
	has := func(names ...string) bool {
		for _, name := range names {
			if _, exists := scopes[name]; exists {
				return true
			}
		}
		return false
	}
	types := make([]string, 0, 4)
	for _, item := range []struct {
		scope string
		kind  string
	}{
		{"channels:read", "public_channel"},
		{"groups:read", "private_channel"},
		{"im:read", "im"},
		{"mpim:read", "mpim"},
	} {
		if has(item.scope) {
			types = append(types, item.kind)
		}
	}
	client.conversationTypes = strings.Join(types, ",")
	history := has("channels:history", "groups:history", "im:history", "mpim:history")
	write := !client.readOnly
	client.capabilities = application.MessageCapabilities{
		ListConversations:  len(types) != 0,
		History:            history,
		SensitiveRead:      history,
		Search:             client.actor.Mode == application.MessageActorDelegatedUser && has("search:read"),
		IncrementalSync:    history,
		Send:               write && has("chat:write"),
		Reply:              write && has("chat:write"),
		Edit:               write && has("chat:write"),
		Delete:             write && has("chat:write"),
		Reactions:          write && has("reactions:write"),
		AttachmentReads:    has("files:read") && history,
		AttachmentWrites:   false,
		CreateConversation: write && has("channels:manage", "groups:write", "im:write", "mpim:write"),
		Membership:         write && has("channels:manage", "groups:write"),
		ActorMode:          client.actor.Mode,
	}
	client.degradations = []domain.Degradation{
		{Feature: "messages.rate_limits", Reason: "Slack applies installation- and distribution-specific rate tiers; Corresync reports throttling and never automatically retries a write"},
		{Feature: "messages.incremental_delete", Reason: "the initial Slack cohort uses bounded Web API polling, which cannot prove deletions that occur between snapshots"},
		{Feature: "messages.concurrency", Reason: "Slack exposes no atomic message-version precondition; Corresync revalidates the reviewed message immediately before an edit, delete, or reaction"},
		{Feature: "messages.attachment_write", Reason: "Slack external file upload remains disabled until its multi-stage outcome contract is proven"},
	}
	if len(scopes) == 0 {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.scopes", Reason: "Slack returned no granted-scope header, so no scope-dependent operation is exposed",
		})
	}
	if client.readOnly {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.write", Reason: "the configured Slack route has a local read-only ceiling",
		})
	}
}

func validateSlackResponse(result restapi.Result, envelope slackEnvelope, write bool) error {
	if result.Status == http.StatusTooManyRequests {
		retry, err := strconv.Atoi(result.Header.Get("Retry-After"))
		if err != nil || retry < 1 || time.Duration(retry)*time.Second > maximumRetryAfter {
			return errors.New("slack returned a malformed rate-limit interval")
		}
		return &RateLimitError{RetryAfter: time.Duration(retry) * time.Second}
	}
	if envelope.OK {
		return validateSlackWarnings(envelope)
	}
	if !validSlackCode(envelope.Error) {
		if write {
			return fmt.Errorf("%w: Slack returned a malformed write response", application.ErrWriteOutcomeUnknown)
		}
		return errors.New("slack returned a malformed API response")
	}
	failure := &APIError{Code: envelope.Error}
	switch envelope.Error {
	case "invalid_auth", "not_authed", "token_expired", "token_revoked", "account_inactive", "team_access_not_granted":
		return application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonCredentialRejected, failure,
		)
	case "request_timeout", "fatal_error", "internal_error":
		if write {
			return fmt.Errorf("%w: %w", application.ErrWriteOutcomeUnknown, failure)
		}
	}
	return failure
}

func validateSlackWarnings(envelope slackEnvelope) error {
	warnings := append([]string(nil), envelope.Metadata.Warnings...)
	if envelope.Warning != "" {
		warnings = append(warnings, strings.Split(envelope.Warning, ",")...)
	}
	for _, warning := range warnings {
		if !validSlackCode(strings.TrimSpace(warning)) {
			return errors.New("slack returned a malformed warning code")
		}
	}
	return nil
}

func parseSlackScopes(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	count := 0
	for _, value := range values {
		for _, scope := range strings.Split(value, ",") {
			scope = strings.TrimSpace(scope)
			if scope == "" {
				continue
			}
			count++
			if !validSlackScope(scope) || count > maximumSlackScopes {
				return nil, errors.New("slack returned malformed or unbounded granted scopes")
			}
			result[scope] = struct{}{}
		}
	}
	return result, nil
}

func validSlackScope(value string) bool {
	if value == "" || len(value) > maximumSlackCode {
		return false
	}
	for _, character := range value {
		if character != ':' && character != '.' && character != '-' && character != '_' &&
			(character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validSlackCode(value string) bool { return validSlackScope(value) }
