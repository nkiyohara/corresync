// Package teamsgraph adapts one explicitly authorized delegated Microsoft
// Graph identity to Corresync's provider-neutral messaging port.
package teamsgraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

const (
	maximumGraphScopes = 64
	maximumGraphItems  = application.MaxMessageCollectionItems
	maximumRetryAfter  = 24 * time.Hour
)

// Options selects one already granted delegated Teams route. HTTP owns OAuth;
// the adapter never accepts, inspects, or exposes token material.
type Options struct {
	APIBase       string
	WorkspaceID   string
	GrantedScopes []string
	ReadOnly      bool
	HTTP          *http.Client
}

// Client owns one account- and workspace-scoped Graph transport.
type Client struct {
	api          *restapi.Client
	apiBase      *url.URL
	workspaceID  string
	actor        application.MessageActor
	capabilities application.MessageCapabilities
	degradations []domain.Degradation
	readOnly     bool
}

// RateLimitError preserves Graph's bounded Retry-After instruction. Writes
// are never retried automatically.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (failure *RateLimitError) Error() string {
	return fmt.Sprintf("Microsoft Graph rate limit; retry after %s", failure.RetryAfter)
}

// New confirms the delegated actor through Graph. It does not start or widen
// authorization and treats the manager-observed grant scope set as evidence.
func New(ctx context.Context, options Options) (*Client, error) {
	if !validGraphOpaque(options.WorkspaceID) {
		return nil, errors.New("the Teams Graph workspace ID is malformed")
	}
	scopes, err := graphScopeSet(options.GrantedScopes)
	if err != nil {
		return nil, err
	}
	api, err := restapi.New(restapi.Options{BaseURL: options.APIBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	apiBase, _ := url.Parse(options.APIBase)
	var identity struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	result, err := api.DoJSON(ctx, http.MethodGet, "me", url.Values{
		"$select": {"id,displayName,userPrincipalName"},
	}, nil, &identity, false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Teams Graph identity: %w", err)
	}
	if err := validateGraphResult(result); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Teams Graph identity: %w", err)
	}
	if !validGraphOpaque(identity.ID) {
		_ = api.Close()
		return nil, errors.New("the Microsoft Graph response contains no delegated Teams actor")
	}
	displayName := identity.DisplayName
	if displayName == "" {
		displayName = identity.UserPrincipalName
	}
	client := &Client{
		api: api, apiBase: apiBase, workspaceID: options.WorkspaceID,
		actor: application.MessageActor{
			ID: identity.ID, Mode: application.MessageActorDelegatedUser,
			DisplayName: boundedGraphText(displayName, 1024),
		},
		readOnly: options.ReadOnly,
	}
	client.observeCapabilities(scopes)
	return client, nil
}

func (client *Client) Close() error {
	if client == nil || client.api == nil {
		return nil
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
			if _, exists := scopes[strings.ToLower(name)]; !exists {
				return false
			}
		}
		return true
	}
	hasAny := func(names ...string) bool {
		for _, name := range names {
			if _, exists := scopes[strings.ToLower(name)]; exists {
				return true
			}
		}
		return false
	}
	read := hasAny("Chat.Read", "Chat.ReadWrite") &&
		has("Team.ReadBasic.All", "Channel.ReadBasic.All", "ChannelMessage.Read.All")
	write := !client.readOnly
	observed := application.MessageCapabilities{
		ListConversations: read,
		History:           read,
		SensitiveRead:     read,
		Search:            read,
		IncrementalSync:   false,
		Send: write && has(
			"ChatMessage.Send", "ChannelMessage.Send",
		),
		Reply: write && has(
			"ChatMessage.Send", "ChannelMessage.Send",
		),
		Edit: write && has(
			"Chat.ReadWrite", "ChannelMessage.ReadWrite",
		),
		Delete: write && has(
			"Chat.ReadWrite", "ChannelMessage.ReadWrite",
		),
		Reactions: write && has(
			"ChatMessage.Send", "ChannelMessage.Send",
		),
		AttachmentReads:  false,
		AttachmentWrites: false,
		CreateConversation: write && has(
			"Chat.Create", "Channel.Create",
		),
		Membership: write && has(
			"ChatMember.ReadWrite", "ChannelMember.ReadWrite.All",
		),
		ActorMode: application.MessageActorDelegatedUser,
	}
	client.capabilities = teamscontract.Intersect(observed, client.readOnly)
	client.degradations = []domain.Degradation{
		{Feature: "messages.incremental_sync", Reason: "Microsoft Graph does not expose one delegated, relay-free delta contract spanning chats, channels, replies, edits, and deletions"},
		{Feature: "messages.attachment_read", Reason: "Teams file attachments remain references in SharePoint or OneDrive and are not exposed until a separately bounded drive contract is proven"},
		{Feature: "messages.attachment_write", Reason: "Teams file upload is a multi-resource operation and remains disabled until its ambiguous outcome contract is proven"},
		{Feature: "messages.concurrency", Reason: "Teams message writes expose no atomic version precondition; Corresync revalidates the selected item immediately before mutation"},
		{Feature: "messages.create_conversation", Reason: "Teams Web keeps a new chat as a draft until its first send, so the Graph/Web parity cohort disables standalone creation on both routes"},
	}
	if !read {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.read", Reason: "the delegated grant does not contain the complete released chat and channel read scope cohort",
		})
	}
	if client.readOnly {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.write", Reason: "the configured Teams Graph route has a local read-only ceiling",
		})
	}
}

func graphScopeSet(values []string) (map[string]struct{}, error) {
	if len(values) == 0 || len(values) > maximumGraphScopes {
		return nil, errors.New("the Teams Graph route requires one bounded observed scope set")
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00 ,") {
			return nil, errors.New("the Teams Graph granted scope set is malformed")
		}
		result[strings.ToLower(value)] = struct{}{}
	}
	return result, nil
}

func validateGraphResult(result restapi.Result) error {
	if result.Status != http.StatusTooManyRequests {
		return nil
	}
	seconds, err := strconv.Atoi(result.Header.Get("Retry-After"))
	if err != nil || seconds < 1 || time.Duration(seconds)*time.Second > maximumRetryAfter {
		return errors.New("the Microsoft Graph rate-limit interval is malformed")
	}
	return &RateLimitError{RetryAfter: time.Duration(seconds) * time.Second}
}

func (client *Client) requireCapability(enabled bool, feature string) error {
	if client == nil || client.api == nil || !validGraphOpaque(client.workspaceID) {
		return errors.New("the Teams Graph messaging service is not enabled")
	}
	if !enabled {
		return fmt.Errorf("the Teams Graph authorization does not support %s", feature)
	}
	return nil
}

func validGraphOpaque(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func boundedGraphText(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
