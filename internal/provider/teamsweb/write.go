package teamsweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

func (client *Client) SendMessage(
	ctx context.Context,
	input application.MessageSendInput,
) (application.Message, error) {
	if err := input.Validate(); err != nil {
		return application.Message{}, err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Message{}, err
	}
	capability := client.capabilities.Send
	feature := "message send"
	if input.ReplyToID != "" {
		capability = client.capabilities.Reply
		feature = "message reply"
	}
	if err := client.requireCapability(capability, feature); err != nil {
		return application.Message{}, err
	}
	if err := validateConversationID(input.ConversationID); err != nil {
		return application.Message{}, err
	}
	if len(input.Attachments) != 0 {
		return application.Message{}, errors.New("the Teams Web attachment writes are not enabled")
	}
	if err := teamscontract.ValidateWriteContent(input.Content); err != nil {
		return application.Message{}, err
	}
	message, err := client.driver.TeamsSendMessage(ctx, input)
	if err != nil {
		return application.Message{}, writeUnknown("send Teams Web message", err)
	}
	if message.Summary.ConversationID != input.ConversationID || message.Summary.ID == "" {
		return application.Message{}, writeUnknown("Teams Web returned a mismatched send result", nil)
	}
	return message, nil
}

func (client *Client) EditMessage(
	ctx context.Context,
	input application.MessageEditInput,
) (application.Message, error) {
	if err := input.Validate(); err != nil {
		return application.Message{}, err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Message{}, err
	}
	if err := client.requireCapability(client.capabilities.Edit, "message editing"); err != nil {
		return application.Message{}, err
	}
	if err := teamscontract.ValidateWriteContent(input.Content); err != nil {
		return application.Message{}, err
	}
	messageBefore, err := client.requireVersion(
		ctx, input.Account, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	)
	if err != nil {
		return application.Message{}, err
	}
	if messageBefore.Summary.Author.ID != client.actor.ID {
		return application.Message{}, errors.New("the Teams Web route can edit only the delegated actor's own message")
	}
	message, err := client.driver.TeamsEditMessage(ctx, input)
	if err != nil {
		return application.Message{}, writeUnknown("edit Teams Web message", err)
	}
	if message.Summary.ID != input.MessageID || message.Summary.ConversationID != input.ConversationID {
		return application.Message{}, writeUnknown("Teams Web returned a mismatched edit result", nil)
	}
	return message, nil
}

func (client *Client) DeleteMessage(ctx context.Context, input application.MessageDeleteInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return err
	}
	if err := client.requireCapability(client.capabilities.Delete, "message deletion"); err != nil {
		return err
	}
	message, err := client.requireVersion(
		ctx, input.Account, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	)
	if err != nil {
		return err
	}
	if message.Summary.Author.ID != client.actor.ID {
		return errors.New("the Teams Web route can delete only the delegated actor's own message")
	}
	if err := client.driver.TeamsDeleteMessage(ctx, input); err != nil {
		return writeUnknown("delete Teams Web message", err)
	}
	return nil
}

func (client *Client) SetMessageReaction(
	ctx context.Context,
	input application.MessageReactionInput,
) (application.MessageReaction, error) {
	if err := input.Validate(); err != nil {
		return application.MessageReaction{}, err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return application.MessageReaction{}, err
	}
	if err := client.requireCapability(client.capabilities.Reactions, "message reactions"); err != nil {
		return application.MessageReaction{}, err
	}
	if err := teamscontract.ValidateReaction(input.Reaction); err != nil {
		return application.MessageReaction{}, err
	}
	if _, err := client.requireVersion(
		ctx, input.Account, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	); err != nil {
		return application.MessageReaction{}, err
	}
	reaction, err := client.driver.TeamsSetReaction(ctx, input)
	if err != nil {
		return application.MessageReaction{}, writeUnknown("change Teams Web reaction", err)
	}
	if reaction.Name != input.Reaction || reaction.ReactedByActor == input.Remove {
		return application.MessageReaction{}, writeUnknown("Teams Web returned a mismatched reaction result", nil)
	}
	return reaction, nil
}

func (client *Client) CreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	if err := input.Validate(); err != nil {
		return application.Conversation{}, err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Conversation{}, err
	}
	if err := client.requireCapability(client.capabilities.CreateConversation, "conversation creation"); err != nil {
		return application.Conversation{}, err
	}
	if input.Kind == application.ConversationChannel {
		if _, err := teamscontract.DecodeTeamID(input.ContainerID); err != nil {
			return application.Conversation{}, err
		}
	}
	conversation, err := client.driver.TeamsCreateConversation(ctx, input)
	if err != nil {
		return application.Conversation{}, writeUnknown("create Teams Web conversation", err)
	}
	if conversation.ID == "" || conversation.Kind != input.Kind ||
		validateConversationID(conversation.ID) != nil {
		return application.Conversation{}, writeUnknown("Teams Web returned a mismatched conversation result", nil)
	}
	return conversation, nil
}

func (client *Client) ChangeConversationMembership(
	ctx context.Context,
	input application.ConversationMembershipInput,
) (application.ConversationMembershipResult, error) {
	if err := input.Validate(); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if err := client.requireWriteRoute(input.MessageWriteRoute); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if err := client.requireCapability(client.capabilities.Membership, "conversation membership"); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if err := validateConversationID(input.ConversationID); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	conversation, err := client.driver.TeamsGetConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if conversation.ID != input.ConversationID || conversation.Version != input.Version {
		return application.ConversationMembershipResult{}, restapi.ErrPrecondition
	}
	result, err := client.driver.TeamsChangeMembership(ctx, input)
	if err != nil {
		return application.ConversationMembershipResult{}, writeUnknown("change Teams Web membership", err)
	}
	if result.ConversationID != input.ConversationID || result.Action != input.Action ||
		result.Member.ID != input.Member.ID || result.Version == "" ||
		result.Version == input.Version {
		return application.ConversationMembershipResult{}, writeUnknown("Teams Web returned a mismatched membership result", nil)
	}
	return result, nil
}

func (client *Client) requireVersion(
	ctx context.Context,
	account domain.AccountID,
	conversationID, threadRootID, messageID, version string,
) (application.Message, error) {
	if err := validateConversationID(conversationID); err != nil {
		return application.Message{}, err
	}
	message, err := client.driver.TeamsGetMessage(ctx, application.MessageGetInput{
		Account: account, WorkspaceID: client.workspaceID, ConversationID: conversationID,
		ThreadRootID: threadRootID, MessageID: messageID,
	})
	if err != nil {
		return application.Message{}, err
	}
	if message.Summary.ConversationID != conversationID || message.Summary.ID != messageID ||
		message.Summary.Version != version {
		return application.Message{}, restapi.ErrPrecondition
	}
	return message, nil
}

func writeUnknown(operation string, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s", application.ErrWriteOutcomeUnknown, operation)
	}
	return errors.Join(application.ErrWriteOutcomeUnknown, fmt.Errorf("%s: %w", operation, err))
}
