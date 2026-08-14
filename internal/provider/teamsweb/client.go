// Package teamsweb adapts a visible, browser-owned Microsoft Teams session to
// Corresync's provider-neutral messaging port. Browser authentication material
// never crosses the closed semantic Driver boundary.
package teamsweb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

const defaultOrigin = "https://teams.microsoft.com"

// Driver is the only browser surface reachable by this provider adapter. It
// exposes neither selectors, scripts, URLs, clicks, nor browser storage.
type Driver interface {
	ObserveTeams(context.Context, string) (teamscontract.Observation, error)
	TeamsListConversations(context.Context, application.ConversationListInput) (application.ConversationPage, error)
	TeamsGetConversation(context.Context, string) (application.Conversation, error)
	TeamsListMessages(context.Context, application.MessageListInput) (application.MessagePage, error)
	TeamsSearchMessages(context.Context, application.MessageSearchInput) (application.MessagePage, error)
	TeamsGetMessage(context.Context, application.MessageGetInput) (application.Message, error)
	TeamsSendMessage(context.Context, application.MessageSendInput) (application.Message, error)
	TeamsEditMessage(context.Context, application.MessageEditInput) (application.Message, error)
	TeamsDeleteMessage(context.Context, application.MessageDeleteInput) error
	TeamsSetReaction(context.Context, application.MessageReactionInput) (application.MessageReaction, error)
	TeamsCreateConversation(context.Context, application.ConversationCreateInput) (application.Conversation, error)
	TeamsChangeMembership(context.Context, application.ConversationMembershipInput) (application.ConversationMembershipResult, error)
}

type Options struct {
	Origin      string
	WorkspaceID string
	ReadOnly    bool
	Driver      Driver
}

type Client struct {
	origin       *url.URL
	workspaceID  string
	actor        application.MessageActor
	capabilities application.MessageCapabilities
	degradations []domain.Degradation
	driver       Driver
}

func New(ctx context.Context, options Options) (*Client, error) {
	if !teamscontract.ValidOpaque(options.WorkspaceID) {
		return nil, errors.New("the Teams Web workspace ID is malformed")
	}
	if options.Driver == nil {
		return nil, errors.New("the Teams Web browser driver is required")
	}
	origin, err := teamsOrigin(options.Origin)
	if err != nil {
		return nil, err
	}
	observation, err := options.Driver.ObserveTeams(ctx, origin.String())
	if err != nil {
		return nil, fmt.Errorf(
			"the Teams Web application did not reach a recognized signed-in state; complete sign-in in the visible browser: %w",
			err,
		)
	}
	if observation.WorkspaceID != options.WorkspaceID {
		return nil, errors.New("the Teams Web browser workspace does not match the configured route")
	}
	if err := observation.Actor.Validate(true); err != nil ||
		observation.Actor.Mode != application.MessageActorDelegatedUser {
		return nil, errors.New("the Teams Web application did not expose one delegated actor")
	}
	if observation.Revision == "" || len(observation.Revision) > 256 ||
		strings.ContainsAny(observation.Revision, "\r\n\x00") {
		return nil, errors.New("the Teams Web semantic revision is unrecognized")
	}
	if err := observation.Capabilities.Validate(); err != nil ||
		observation.Capabilities.ActorMode != application.MessageActorDelegatedUser {
		return nil, errors.New("the Teams Web application exposed malformed semantic capabilities")
	}
	client := &Client{
		origin: origin, workspaceID: options.WorkspaceID,
		actor: observation.Actor, driver: options.Driver,
	}
	client.capabilities = teamscontract.Intersect(observation.Capabilities, options.ReadOnly)
	client.degradations = []domain.Degradation{
		{Feature: "messages.incremental_sync", Reason: "Teams Web exposes no stable deletion-aware cursor contract; bounded snapshots are used instead"},
		{Feature: "messages.attachment_read", Reason: "Teams Web file controls do not expose a bounded credential-free attachment contract"},
		{Feature: "messages.attachment_write", Reason: "Teams Web attachment upload remains disabled until its multi-step ambiguous outcome contract is proven"},
		{Feature: "messages.concurrency", Reason: "Teams Web exposes no atomic version precondition; Corresync revalidates the visible item immediately before mutation"},
		{Feature: "messages.create_conversation", Reason: "Teams Web keeps a new chat as a draft until its first send, so standalone conversation creation is outside the released parity cohort"},
		{Feature: "messages.teams_web_revision", Reason: "recognized closed semantic DOM revision " + observation.Revision},
	}
	if options.ReadOnly {
		client.degradations = append(client.degradations, domain.Degradation{
			Feature: "messages.write", Reason: "the configured Teams Web route has a local read-only ceiling",
		})
	}
	return client, nil
}

func teamsOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		raw = defaultOrigin
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "teams.microsoft.com" ||
		parsed.User != nil || parsed.Path != "" && parsed.Path != "/" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("the Teams Web origin must be the exact provider-owned HTTPS origin")
	}
	parsed.Path = ""
	return parsed, nil
}

func (client *Client) MessageActor() application.MessageActor { return client.actor }

func (client *Client) MessageCapabilities() application.MessageCapabilities {
	return client.capabilities
}

func (client *Client) MessageDegradations() []domain.Degradation {
	return append([]domain.Degradation(nil), client.degradations...)
}

func (client *Client) requireRoute(account domain.AccountID, workspaceID string) error {
	if client == nil || client.driver == nil || client.origin == nil {
		return errors.New("the Teams Web messaging service is not enabled")
	}
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if workspaceID != client.workspaceID {
		return errors.New("the Teams Web operation escaped the configured workspace")
	}
	return nil
}

func (client *Client) requireWriteRoute(route application.MessageWriteRoute) error {
	if err := client.requireRoute(route.Account, route.WorkspaceID); err != nil {
		return err
	}
	if route.Actor.ID != client.actor.ID || route.Actor.Mode != client.actor.Mode {
		return errors.New("the Teams Web operation actor does not match the browser identity")
	}
	return nil
}

func (client *Client) requireCapability(enabled bool, feature string) error {
	if client == nil || client.driver == nil {
		return errors.New("the Teams Web messaging service is not enabled")
	}
	if !enabled {
		return fmt.Errorf("the Teams Web semantic controls do not support %s", feature)
	}
	return nil
}

func driverReadFailure(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	return application.NewProviderAuthenticationFailure(
		application.AuthenticationReasonInteractionRequired,
		errors.New("the Teams Web browser-owned session must be reviewed again"),
	)
}

func validateConversationID(id string) error {
	_, err := teamscontract.DecodeConversationID(id)
	return err
}
