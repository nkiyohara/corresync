// Package mattermostapi adapts one explicitly selected Mattermost server and
// team through its supported REST and WebSocket contracts.
package mattermostapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

type Options struct {
	Origin        string
	WorkspaceID   string
	ReadOnly      bool
	Authorization Authorizer
}

type Client struct {
	api           *restapi.Client
	origin        string
	workspaceID   string
	actor         application.MessageActor
	capabilities  application.MessageCapabilities
	degradations  []domain.Degradation
	authorization Authorizer
	pinned        *pinnedOrigin
}

type RateLimitError struct {
	ResetAt time.Time
}

func (failure *RateLimitError) Error() string {
	if failure.ResetAt.IsZero() {
		return "Mattermost API rate limit"
	}
	return "Mattermost API rate limit resets at " + failure.ResetAt.Format(time.RFC3339)
}

// New resolves and pins the selected public endpoint before any credential is
// applied, then confirms the exact delegated actor and team.
func New(ctx context.Context, options Options) (*Client, error) {
	httpClient, pinned, err := newMattermostHTTPClient(ctx, options.Origin, options.Authorization)
	if err != nil {
		return nil, err
	}
	client, err := newWithHTTP(ctx, options, httpClient, pinned)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, err
	}
	return client, nil
}

func newWithHTTP(
	ctx context.Context,
	options Options,
	httpClient *http.Client,
	pinned *pinnedOrigin,
) (*Client, error) {
	if !validMattermostID(options.WorkspaceID) {
		return nil, errors.New("mattermost workspace ID is malformed")
	}
	origin, err := parseMattermostOrigin(options.Origin)
	if err != nil {
		return nil, err
	}
	api, err := restapi.New(restapi.Options{
		BaseURL: strings.TrimRight(origin.String(), "/") + "/api/v4",
		HTTP:    httpClient,
	})
	if err != nil {
		return nil, err
	}
	client := &Client{
		api: api, origin: origin.String(), workspaceID: options.WorkspaceID,
		authorization: options.Authorization, pinned: pinned,
	}
	var user mattermostUser
	if err := client.doJSON(ctx, http.MethodGet, "users/me", nil, nil, &user, false, http.StatusOK); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Mattermost actor: %w", err)
	}
	if !validMattermostID(user.ID) || user.DeleteAt != 0 || user.Username == "" {
		_ = api.Close()
		return nil, errors.New("mattermost returned a malformed delegated actor")
	}
	var teams []mattermostTeam
	if err := client.doJSON(ctx, http.MethodGet, "users/me/teams", nil, nil, &teams, false, http.StatusOK); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Mattermost team: %w", err)
	}
	if len(teams) > maximumMattermostItems {
		_ = api.Close()
		return nil, errors.New("mattermost returned too many actor teams")
	}
	selected := false
	for _, team := range teams {
		if !validMattermostID(team.ID) || team.DeleteAt < 0 {
			_ = api.Close()
			return nil, errors.New("mattermost returned a malformed team")
		}
		if team.ID == options.WorkspaceID && team.DeleteAt == 0 {
			selected = true
		}
	}
	if !selected {
		_ = api.Close()
		return nil, errors.New("mattermost authorization is not a member of the selected team")
	}
	displayName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if displayName == "" {
		displayName = user.Username
	}
	client.actor = application.MessageActor{
		ID: user.ID, Mode: application.MessageActorDelegatedUser,
		DisplayName: boundedMattermostText(displayName, 1024),
	}
	permissions, permissionDegradations, err := client.observePermissions(ctx, user.Roles)
	if err != nil {
		_ = api.Close()
		return nil, err
	}
	has := func(permission string) bool {
		_, exists := permissions[permission]
		return exists
	}
	canRead := has("read_channel") || has("read_public_channel") || has("read_private_channel")
	canReact := has("add_reaction") && has("remove_reaction")
	canCreate := has("create_public_channel") || has("create_private_channel")
	canManageMembers := has("manage_public_channel_members") || has("manage_private_channel_members")
	client.capabilities = application.MessageCapabilities{
		ListConversations: true, History: canRead, SensitiveRead: canRead, Search: canRead,
		IncrementalSync: canRead, AttachmentReads: canRead,
		Send:               !options.ReadOnly && has("create_post"),
		Reply:              !options.ReadOnly && has("create_post"),
		Edit:               !options.ReadOnly && has("edit_post"),
		Delete:             !options.ReadOnly && has("delete_post"),
		Reactions:          !options.ReadOnly && canReact,
		CreateConversation: !options.ReadOnly && canCreate,
		Membership:         !options.ReadOnly && canManageMembers,
		ActorMode:          application.MessageActorDelegatedUser,
	}
	client.degradations = append([]domain.Degradation(nil), permissionDegradations...)
	client.degradations = append(client.degradations, []domain.Degradation{
		{
			Feature: "messages.attachment_write",
			Reason:  "Mattermost file upload and post creation are separate commits; atomic reviewed attachment sends remain unavailable",
		},
		{
			Feature: "messages.mentions",
			Reason:  "Mattermost addresses mentions by mutable usernames; canonical ID-bound mention writes fail closed",
		},
		{
			Feature: "messages.incremental_sync",
			Reason:  "REST synchronization uses bounded snapshot reset; WebSocket events are untrusted invalidations that trigger snapshot recovery",
		},
	}...)
	client.addPermissionDegradations()
	if options.ReadOnly {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.write", Reason: "the selected Mattermost route is configured read-only",
		})
	}
	return client, nil
}

func (client *Client) observePermissions(
	ctx context.Context,
	systemRoles string,
) (map[string]struct{}, []domain.Degradation, error) {
	var teamMember mattermostTeamMember
	if err := client.doJSON(
		ctx, http.MethodGet,
		"teams/"+client.workspaceID+"/members/"+client.actor.ID,
		nil, nil, &teamMember, false, http.StatusOK,
	); err != nil {
		return nil, nil, fmt.Errorf("observe Mattermost team roles: %w", err)
	}
	if teamMember.TeamID != client.workspaceID || teamMember.UserID != client.actor.ID ||
		teamMember.DeleteAt != 0 {
		return nil, nil, errors.New("mattermost returned a mismatched team membership")
	}
	var channelMembers []mattermostChannelMember
	if err := client.doJSON(
		ctx, http.MethodGet,
		"users/"+client.actor.ID+"/teams/"+client.workspaceID+"/channels/members",
		nil, nil, &channelMembers, false, http.StatusOK,
	); err != nil {
		return nil, nil, fmt.Errorf("observe Mattermost channel roles: %w", err)
	}
	if len(channelMembers) > maximumMattermostItems {
		return nil, nil, errors.New("mattermost returned too many channel role bindings")
	}
	roleNames := make(map[string]struct{})
	for _, roles := range []string{systemRoles, teamMember.Roles} {
		if err := addMattermostRoles(roleNames, roles); err != nil {
			return nil, nil, err
		}
	}
	for _, member := range channelMembers {
		if !validMattermostID(member.ChannelID) || member.UserID != client.actor.ID {
			return nil, nil, errors.New("mattermost returned a mismatched channel role binding")
		}
		if err := addMattermostRoles(roleNames, member.Roles); err != nil {
			return nil, nil, err
		}
	}
	if len(roleNames) == 0 || len(roleNames) > maximumMattermostItems {
		return nil, nil, errors.New("mattermost returned no bounded actor roles")
	}
	names := make([]string, 0, len(roleNames))
	for name := range roleNames {
		names = append(names, name)
	}
	sort.Strings(names)
	var roles []mattermostRole
	if err := client.doJSON(
		ctx, http.MethodPost, "roles/names", nil, names, &roles, false, http.StatusOK,
	); err != nil {
		if restapi.IsStatus(err, http.StatusForbidden) || restapi.IsStatus(err, http.StatusNotFound) {
			return map[string]struct{}{}, []domain.Degradation{{
				Feature: "messages.permissions",
				Reason:  "the selected Mattermost server did not expose bounded role definitions; permission-dependent capabilities remain unavailable",
			}}, nil
		}
		return nil, nil, fmt.Errorf("observe Mattermost role permissions: %w", err)
	}
	if len(roles) != len(names) || len(roles) > maximumMattermostItems {
		return nil, nil, errors.New("mattermost returned an incomplete actor role set")
	}
	permissions := make(map[string]struct{})
	seenRoles := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if !validMattermostID(role.ID) || !validMattermostCode(role.Name, 128) ||
			len(role.Permissions) > maximumMattermostItems {
			return nil, nil, errors.New("mattermost returned a malformed actor role")
		}
		if _, requested := roleNames[role.Name]; !requested {
			return nil, nil, errors.New("mattermost returned an unrequested actor role")
		}
		if _, duplicate := seenRoles[role.Name]; duplicate {
			return nil, nil, errors.New("mattermost returned a duplicate actor role")
		}
		seenRoles[role.Name] = struct{}{}
		for _, permission := range role.Permissions {
			if !validMattermostCode(permission, 128) {
				return nil, nil, errors.New("mattermost returned a malformed role permission")
			}
			permissions[permission] = struct{}{}
		}
	}
	return permissions, nil, nil
}

func addMattermostRoles(destination map[string]struct{}, value string) error {
	for _, role := range strings.Fields(value) {
		if !validMattermostCode(role, 128) {
			return errors.New("mattermost returned a malformed actor role name")
		}
		destination[role] = struct{}{}
	}
	return nil
}

func validMattermostCode(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func (client *Client) addPermissionDegradations() {
	for _, item := range []struct {
		enabled bool
		feature string
	}{
		{client.capabilities.History, "messages.history"},
		{client.capabilities.Send, "messages.send"},
		{client.capabilities.Edit, "messages.edit"},
		{client.capabilities.Delete, "messages.delete"},
		{client.capabilities.Reactions, "messages.reactions"},
		{client.capabilities.CreateConversation, "messages.create_conversation"},
		{client.capabilities.Membership, "messages.membership"},
	} {
		if !item.enabled {
			client.degradations = append(client.degradations, domain.Degradation{
				Feature: item.feature,
				Reason:  "the selected Mattermost actor roles do not grant this operation",
			})
		}
	}
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	var failures []error
	if client.api != nil {
		failures = append(failures, client.api.Close())
	}
	if closer, ok := client.authorization.(io.Closer); ok {
		failures = append(failures, closer.Close())
	}
	return errors.Join(failures...)
}

func (client *Client) MessageActor() application.MessageActor { return client.actor }

func (client *Client) MessageCapabilities() application.MessageCapabilities {
	return client.capabilities
}

func (client *Client) MessageDegradations() []domain.Degradation {
	return append([]domain.Degradation(nil), client.degradations...)
}

func (client *Client) doJSON(
	ctx context.Context,
	method, resource string,
	query url.Values,
	requestBody, responseBody any,
	write bool,
	accepted ...int,
) error {
	accepted = append(append([]int(nil), accepted...), http.StatusTooManyRequests)
	result, err := client.api.DoJSON(
		ctx, method, resource, query, requestBody, responseBody, write, nil, accepted...,
	)
	if result.Status == http.StatusTooManyRequests {
		return parseMattermostRateLimit(result.Header)
	}
	if err != nil {
		return err
	}
	return nil
}

func (client *Client) do(
	ctx context.Context,
	method, resource string,
	query url.Values,
	write bool,
	accepted ...int,
) (restapi.Result, error) {
	accepted = append(append([]int(nil), accepted...), http.StatusTooManyRequests)
	result, err := client.api.Do(ctx, method, resource, query, nil, write, nil, accepted...)
	if err != nil {
		return restapi.Result{}, err
	}
	if result.Status == http.StatusTooManyRequests {
		return restapi.Result{}, parseMattermostRateLimit(result.Header)
	}
	return result, nil
}

func parseMattermostRateLimit(header http.Header) error {
	raw := header.Get("X-Ratelimit-Reset")
	if raw == "" {
		return &RateLimitError{}
	}
	delaySeconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || delaySeconds < 0 || delaySeconds > int64((24*time.Hour)/time.Second) {
		return errors.New("mattermost returned a malformed rate-limit reset")
	}
	reset := time.Now().UTC().Add(time.Duration(delaySeconds) * time.Second)
	return &RateLimitError{ResetAt: reset}
}

func (client *Client) requireCapability(enabled bool, feature string) error {
	if client == nil || client.api == nil || !validMattermostID(client.workspaceID) ||
		!validMattermostID(client.actor.ID) {
		return errors.New("the Mattermost messaging service is not enabled")
	}
	if !enabled {
		return fmt.Errorf("the Mattermost route does not support %s", feature)
	}
	return nil
}

func (client *Client) validateWriteRoute(route application.MessageWriteRoute) error {
	if route.WorkspaceID != client.workspaceID || route.Actor.ID != client.actor.ID ||
		route.Actor.Mode != client.actor.Mode {
		return errors.New("mattermost write route does not match the selected actor and team")
	}
	return nil
}
