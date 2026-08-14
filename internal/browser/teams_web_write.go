package browser

import (
	"context"
	"errors"
	"net/url"

	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

type teamsWriteSignal struct {
	State     string `json:"state"`
	MessageID string `json:"messageId"`
	ChannelID string `json:"channelId"`
}

func (browser *Browser) TeamsSendMessage(
	ctx context.Context,
	input application.MessageSendInput,
) (application.Message, error) {
	if err := input.Validate(); err != nil {
		return application.Message{}, err
	}
	state, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute)
	if err != nil {
		return application.Message{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.Message{}, err
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		target := teamsConversationURL(locator, input.WorkspaceID)
		if input.ReplyToID != "" {
			target = teamsMessageURL(locator, input.WorkspaceID, input.ReplyToID, input.ReplyToID)
		}
		if err := navigateTeamsApplication(operationContext, target); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsComposeActionScript, "send", input.ReplyToID, input.Content.Text,
			input.Mentions, "", state.actor.ID,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.Message{}, err
	}
	if signal.State != "confirmed" || !boundedTeamsValue(signal.MessageID) {
		return application.Message{}, errors.New("the Teams Web app did not confirm the submitted message")
	}
	return browser.TeamsGetMessage(ctx, application.MessageGetInput{
		Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, ThreadRootID: input.ReplyToID,
		MessageID: signal.MessageID,
	})
}

func (browser *Browser) TeamsEditMessage(
	ctx context.Context,
	input application.MessageEditInput,
) (application.Message, error) {
	if err := input.Validate(); err != nil {
		return application.Message{}, err
	}
	state, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute)
	if err != nil {
		return application.Message{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.Message{}, err
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(
			operationContext,
			teamsMessageURL(locator, input.WorkspaceID, input.ThreadRootID, input.MessageID),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsComposeActionScript, "edit", input.MessageID, input.Content.Text,
			input.Mentions, input.MessageID, state.actor.ID,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.Message{}, err
	}
	if signal.State != "confirmed" || signal.MessageID != input.MessageID {
		return application.Message{}, errors.New("the Teams Web app did not confirm the edited message")
	}
	return browser.TeamsGetMessage(ctx, application.MessageGetInput{
		Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
		MessageID: input.MessageID,
	})
}

func (browser *Browser) TeamsDeleteMessage(
	ctx context.Context,
	input application.MessageDeleteInput,
) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if _, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute); err != nil {
		return err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return err
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(
			operationContext,
			teamsMessageURL(locator, input.WorkspaceID, input.ThreadRootID, input.MessageID),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsMessageMenuActionScript, "delete", input.MessageID, "", false,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return err
	}
	if signal.State != "confirmed" || signal.MessageID != input.MessageID {
		return errors.New("the Teams Web app did not confirm the deleted message")
	}
	return nil
}

func (browser *Browser) TeamsSetReaction(
	ctx context.Context,
	input application.MessageReactionInput,
) (application.MessageReaction, error) {
	if err := input.Validate(); err != nil {
		return application.MessageReaction{}, err
	}
	if _, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute); err != nil {
		return application.MessageReaction{}, err
	}
	selectorID, ok := teamsReactionControl(input.Reaction)
	if !ok {
		return application.MessageReaction{}, errors.New("the Teams Web reaction is unsupported")
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.MessageReaction{}, err
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(
			operationContext,
			teamsMessageURL(locator, input.WorkspaceID, input.ThreadRootID, input.MessageID),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsMessageMenuActionScript, "reaction", input.MessageID, selectorID, input.Remove,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.MessageReaction{}, err
	}
	if signal.State != "confirmed" || signal.MessageID != input.MessageID {
		return application.MessageReaction{}, errors.New("the Teams Web app did not confirm the reaction change")
	}
	return application.MessageReaction{
		Name: input.Reaction, CountKnown: false, ReactedByActor: !input.Remove,
	}, nil
}

func (browser *Browser) TeamsCreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	if err := input.Validate(); err != nil {
		return application.Conversation{}, err
	}
	if _, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Conversation{}, err
	}
	if input.Kind != application.ConversationChannel {
		return application.Conversation{}, errors.New(
			"the Teams Web app keeps new chats as drafts until a message is sent and cannot create one as a standalone operation",
		)
	}
	if input.Visibility == application.ConversationVisibilityShared {
		return application.Conversation{}, errors.New("the Teams Web shared channel creation is not enabled")
	}
	teamID, err := teamscontract.DecodeTeamID(input.ContainerID)
	if err != nil {
		return application.Conversation{}, err
	}
	for _, member := range input.Members {
		if member.ID != input.Actor.ID {
			return application.Conversation{}, errors.New(
				"the Teams Web channel creation cannot atomically add selected members",
			)
		}
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		query := url.Values{"groupId": {teamID}, "tenantId": {input.WorkspaceID}}
		if err := navigateTeamsApplication(
			operationContext,
			teamsWebOrigin+"/l/team/"+url.PathEscape(teamID)+"/conversations?"+query.Encode(),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsCreateChannelScript, teamID, input.Name, input.Topic, string(input.Visibility),
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.Conversation{}, err
	}
	if signal.State != "confirmed" || !boundedTeamsValue(signal.ChannelID) {
		return application.Conversation{}, errors.New("the Teams Web app did not confirm the created channel")
	}
	conversationID, err := teamscontract.EncodeChannelID(teamID, signal.ChannelID)
	if err != nil {
		return application.Conversation{}, err
	}
	return browser.TeamsGetConversation(ctx, conversationID)
}

func (browser *Browser) TeamsChangeMembership(
	ctx context.Context,
	input application.ConversationMembershipInput,
) (application.ConversationMembershipResult, error) {
	if err := input.Validate(); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if _, err := browser.validateTeamsWriteRoute(input.MessageWriteRoute); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	var signal teamsWriteSignal
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(
			operationContext,
			teamsConversationURL(locator, input.WorkspaceID),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsMembershipScript, string(input.Action), input.Member.ID, string(input.Member.Role),
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &signal, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if signal.State != "confirmed" {
		return application.ConversationMembershipResult{}, errors.New(
			"the Teams Web app did not confirm the membership change",
		)
	}
	conversation, err := browser.TeamsGetConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	return application.ConversationMembershipResult{
		ConversationID: input.ConversationID, Version: conversation.Version,
		Action: input.Action, Member: input.Member,
	}, nil
}

func teamsReactionControl(reaction string) (string, bool) {
	value, exists := map[string]string{
		"like": "like", "heart": "heart", "laugh": "laugh",
		"surprised": "surprised", "sad": "sad", "angry": "angry",
	}[reaction]
	return value, exists
}

func (browser *Browser) validateTeamsWriteRoute(
	route application.MessageWriteRoute,
) (teamsBrowserState, error) {
	state, err := browser.teamsStateSnapshot()
	if err != nil {
		return teamsBrowserState{}, err
	}
	if route.WorkspaceID != state.workspaceID || route.Actor.ID != state.actor.ID ||
		route.Actor.Mode != state.actor.Mode {
		return teamsBrowserState{}, errors.New(
			"the Teams Web write route does not match the browser identity",
		)
	}
	return state, nil
}
