package teamsweb

import (
	"context"
	"errors"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

func (client *Client) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
) (application.ConversationPage, error) {
	if err := input.Validate(); err != nil {
		return application.ConversationPage{}, err
	}
	if err := client.requireRoute(input.Account, input.WorkspaceID); err != nil {
		return application.ConversationPage{}, err
	}
	if err := client.requireCapability(client.capabilities.ListConversations, "conversation listing"); err != nil {
		return application.ConversationPage{}, err
	}
	expected := pageCursor{Kind: webCursorConversations, Account: input.Account, WorkspaceID: input.WorkspaceID}
	providerCursor, err := unwrapPageCursor(input.Cursor, expected)
	if err != nil {
		return application.ConversationPage{}, err
	}
	input.Cursor = providerCursor
	page, err := client.driver.TeamsListConversations(ctx, input)
	if err != nil {
		return application.ConversationPage{}, driverReadFailure(ctx, err)
	}
	for _, conversation := range page.Conversations {
		if err := validateConversationID(conversation.ID); err != nil {
			return application.ConversationPage{}, errors.New("the Teams Web page contains a malformed conversation identity")
		}
	}
	page.NextCursor, err = wrapPageCursor(page.NextCursor, expected)
	return page, err
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
) (application.MessagePage, error) {
	if err := input.Validate(); err != nil {
		return application.MessagePage{}, err
	}
	if err := client.requireRoute(input.Account, input.WorkspaceID); err != nil {
		return application.MessagePage{}, err
	}
	if err := client.requireCapability(client.capabilities.History, "message history"); err != nil {
		return application.MessagePage{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.MessagePage{}, err
	}
	if locator.IsChat() && input.ThreadRootID != "" {
		return application.MessagePage{}, errors.New("the Teams chat does not expose threaded history")
	}
	expected := pageCursor{
		Kind: webCursorMessages, Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
	}
	providerCursor, err := unwrapPageCursor(input.Cursor, expected)
	if err != nil {
		return application.MessagePage{}, err
	}
	input.Cursor = providerCursor
	page, err := client.driver.TeamsListMessages(ctx, input)
	if err != nil {
		return application.MessagePage{}, driverReadFailure(ctx, err)
	}
	for _, message := range page.Messages {
		if message.ConversationID != input.ConversationID || message.ID == "" ||
			input.ThreadRootID != "" && message.ThreadRootID != input.ThreadRootID &&
				message.ID != input.ThreadRootID {
			return application.MessagePage{}, errors.New(
				"the Teams Web page returned a message outside the selected conversation",
			)
		}
	}
	page.NextCursor, err = wrapPageCursor(page.NextCursor, expected)
	return page, err
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
) (application.MessagePage, error) {
	if err := input.Validate(); err != nil {
		return application.MessagePage{}, err
	}
	if err := client.requireRoute(input.Account, input.WorkspaceID); err != nil {
		return application.MessagePage{}, err
	}
	if err := client.requireCapability(client.capabilities.Search, "message search"); err != nil {
		return application.MessagePage{}, err
	}
	if input.ConversationID != "" {
		if err := validateConversationID(input.ConversationID); err != nil {
			return application.MessagePage{}, err
		}
	}
	expected := pageCursor{
		Kind: webCursorSearch, Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, QuerySHA256: queryDigest(input.Query),
	}
	providerCursor, err := unwrapPageCursor(input.Cursor, expected)
	if err != nil {
		return application.MessagePage{}, err
	}
	input.Cursor = providerCursor
	page, err := client.driver.TeamsSearchMessages(ctx, input)
	if err != nil {
		return application.MessagePage{}, driverReadFailure(ctx, err)
	}
	for _, message := range page.Messages {
		if message.ID == "" || validateConversationID(message.ConversationID) != nil ||
			input.ConversationID != "" && message.ConversationID != input.ConversationID {
			return application.MessagePage{}, errors.New(
				"the Teams Web search returned a message outside the selected route",
			)
		}
	}
	page.NextCursor, err = wrapPageCursor(page.NextCursor, expected)
	return page, err
}

func (client *Client) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
) (application.Message, error) {
	if err := input.Validate(); err != nil {
		return application.Message{}, err
	}
	if err := client.requireRoute(input.Account, input.WorkspaceID); err != nil {
		return application.Message{}, err
	}
	if err := client.requireCapability(client.capabilities.SensitiveRead, "sensitive message reads"); err != nil {
		return application.Message{}, err
	}
	if err := validateConversationID(input.ConversationID); err != nil {
		return application.Message{}, err
	}
	message, err := client.driver.TeamsGetMessage(ctx, input)
	if err != nil {
		return application.Message{}, driverReadFailure(ctx, err)
	}
	if message.Summary.ConversationID != input.ConversationID ||
		message.Summary.ID != input.MessageID ||
		input.ThreadRootID != "" && message.Summary.ThreadRootID != input.ThreadRootID &&
			message.Summary.ID != input.ThreadRootID {
		return application.Message{}, errors.New(
			"the Teams Web page returned a different selected message",
		)
	}
	return message, nil
}

func (client *Client) GetMessageAttachment(
	context.Context,
	application.MessageAttachmentGetInput,
) (application.MessageAttachmentContent, error) {
	return application.MessageAttachmentContent{}, errors.New("the Teams Web attachment reads are not enabled")
}

func (client *Client) SyncMessages(
	context.Context,
	application.MessageSyncInput,
) (application.MessageChangePage, error) {
	return application.MessageChangePage{}, errors.New("the Teams Web incremental synchronization is not enabled")
}
