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
)

const slackHistoryPageLimit = 15

func (client *Client) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
) (application.ConversationPage, error) {
	if err := client.requireCapability(client.capabilities.ListConversations, "conversation listing"); err != nil {
		return application.ConversationPage{}, err
	}
	providerCursor := ""
	if input.Cursor != "" {
		cursor, err := decodeSlackPageCursor(input.Cursor, slackPageCursor{
			Kind: slackCursorConversations, Account: input.Account, WorkspaceID: input.WorkspaceID,
		})
		if err != nil {
			return application.ConversationPage{}, err
		}
		providerCursor = cursor.Opaque
	}
	limit := min(input.Limit, application.MaxConversationPageSize)
	var response struct {
		slackEnvelope
		Channels []slackConversation `json:"channels"`
	}
	result, err := client.api.DoJSON(ctx, http.MethodGet, "conversations.list", url.Values{
		"types":            {client.conversationTypes},
		"exclude_archived": {"true"},
		"limit":            {strconv.Itoa(limit)},
		"cursor":           {providerCursor},
	}, nil, &response, false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.ConversationPage{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return application.ConversationPage{}, err
	}
	if len(response.Channels) > limit {
		return application.ConversationPage{}, errors.New("slack returned too many conversations")
	}
	conversations := make([]application.Conversation, 0, len(response.Channels))
	for _, source := range response.Channels {
		if source.IsArchived {
			continue
		}
		conversation, err := mapSlackConversation(source)
		if err != nil {
			return application.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	nextCursor := ""
	if response.Metadata.NextCursor != "" {
		nextCursor, err = encodeSlackPageCursor(slackPageCursor{
			Version: 1, Kind: slackCursorConversations, Account: input.Account,
			WorkspaceID: input.WorkspaceID, Opaque: response.Metadata.NextCursor,
		})
		if err != nil {
			return application.ConversationPage{}, err
		}
	}
	return application.ConversationPage{
		Conversations: conversations, NextCursor: nextCursor,
		ObservedAt: time.Now().UTC(), Degradations: slackWarningDegradations(response.slackEnvelope),
	}, nil
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
) (application.MessagePage, error) {
	if err := client.requireCapability(client.capabilities.History, "message history"); err != nil {
		return application.MessagePage{}, err
	}
	if !validSlackID(input.ConversationID) || input.ThreadRootID != "" && !validSlackTimestamp(input.ThreadRootID) {
		return application.MessagePage{}, errors.New("slack message route is malformed")
	}
	resource := "conversations.history"
	cursorKind := slackCursorMessages
	if input.ThreadRootID != "" {
		resource = "conversations.replies"
		cursorKind = slackCursorThread
	}
	providerCursor := ""
	if input.Cursor != "" {
		cursor, err := decodeSlackPageCursor(input.Cursor, slackPageCursor{
			Kind: cursorKind, Account: input.Account, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		providerCursor = cursor.Opaque
	}
	query := url.Values{"channel": {input.ConversationID}, "cursor": {providerCursor}}
	if input.ThreadRootID != "" {
		query.Set("ts", input.ThreadRootID)
	}
	limit := min(input.Limit, slackHistoryPageLimit)
	query.Set("limit", strconv.Itoa(limit))
	var response struct {
		slackEnvelope
		Messages []slackMessage `json:"messages"`
		HasMore  bool           `json:"has_more,omitempty"`
	}
	result, err := client.api.DoJSON(
		ctx, http.MethodGet, resource, query, nil, &response, false, nil,
		http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		return application.MessagePage{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return application.MessagePage{}, err
	}
	if len(response.Messages) > limit {
		return application.MessagePage{}, errors.New("slack returned too many messages")
	}
	messages := make([]application.MessageSummary, 0, len(response.Messages))
	for _, source := range response.Messages {
		summary, err := mapSlackSummary(source, input.ConversationID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	partial := response.HasMore && response.Metadata.NextCursor == ""
	reason := ""
	if partial {
		reason = "provider_limit"
	}
	nextCursor := ""
	if response.Metadata.NextCursor != "" {
		nextCursor, err = encodeSlackPageCursor(slackPageCursor{
			Version: 1, Kind: cursorKind, Account: input.Account, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
			Opaque: response.Metadata.NextCursor,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	return application.MessagePage{
		Messages: messages, NextCursor: nextCursor,
		Partial: partial, PartialReason: reason, ObservedAt: time.Now().UTC(),
		Degradations: slackWarningDegradations(response.slackEnvelope),
	}, nil
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
) (application.MessagePage, error) {
	if err := client.requireCapability(client.capabilities.Search, "message search"); err != nil {
		return application.MessagePage{}, err
	}
	if input.ConversationID != "" {
		return application.MessagePage{}, errors.New("slack search cannot safely bind an opaque conversation ID")
	}
	page := 1
	if input.Cursor != "" {
		cursor, err := decodeSlackPageCursor(input.Cursor, slackPageCursor{
			Kind: slackCursorSearch, Account: input.Account, WorkspaceID: input.WorkspaceID,
			QuerySHA256: slackQueryDigest(input.Query),
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		page = cursor.Page
	}
	limit := min(input.Limit, 100)
	var response struct {
		slackEnvelope
		Messages struct {
			Matches []struct {
				slackMessage
				Channel struct {
					ID string `json:"id"`
				} `json:"channel"`
			} `json:"matches"`
			Paging struct {
				Page  int `json:"page"`
				Pages int `json:"pages"`
			} `json:"paging"`
		} `json:"messages"`
	}
	result, err := client.api.DoJSON(ctx, http.MethodGet, "search.messages", url.Values{
		"query": {input.Query}, "count": {strconv.Itoa(limit)},
		"page": {strconv.Itoa(page)}, "highlight": {"false"},
	}, nil, &response, false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.MessagePage{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return application.MessagePage{}, err
	}
	if len(response.Messages.Matches) > limit || response.Messages.Paging.Page < 0 ||
		response.Messages.Paging.Pages < response.Messages.Paging.Page || response.Messages.Paging.Pages > 100 {
		return application.MessagePage{}, errors.New("slack returned malformed search pagination")
	}
	messages := make([]application.MessageSummary, 0, len(response.Messages.Matches))
	for _, match := range response.Messages.Matches {
		summary, err := mapSlackSummary(match.slackMessage, match.Channel.ID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	next := ""
	if page < response.Messages.Paging.Pages {
		next, err = encodeSlackPageCursor(slackPageCursor{
			Version: 1, Kind: slackCursorSearch, Account: input.Account,
			WorkspaceID: input.WorkspaceID, QuerySHA256: slackQueryDigest(input.Query), Page: page + 1,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	return application.MessagePage{
		Messages: messages, NextCursor: next, ObservedAt: time.Now().UTC(),
		Degradations: slackWarningDegradations(response.slackEnvelope),
	}, nil
}

func (client *Client) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.SensitiveRead, "message body reads"); err != nil {
		return application.Message{}, err
	}
	source, err := client.getSlackMessage(ctx, input.ConversationID, input.ThreadRootID, input.MessageID)
	if err != nil {
		return application.Message{}, err
	}
	return mapSlackMessage(source, input.ConversationID, client.actor.ID)
}

func (client *Client) GetMessageAttachment(
	ctx context.Context,
	input application.MessageAttachmentGetInput,
) (application.MessageAttachmentContent, error) {
	if err := client.requireCapability(client.capabilities.AttachmentReads, "attachment reads"); err != nil {
		return application.MessageAttachmentContent{}, err
	}
	return client.getSlackAttachment(ctx, input)
}

func (client *Client) getSlackMessage(ctx context.Context, conversationID, threadRootID, messageID string) (slackMessage, error) {
	if !validSlackID(conversationID) || !validSlackTimestamp(messageID) ||
		threadRootID != "" && !validSlackTimestamp(threadRootID) {
		return slackMessage{}, errors.New("slack message identity is malformed")
	}
	resource := "conversations.history"
	query := url.Values{
		"channel": {conversationID}, "oldest": {messageID}, "latest": {messageID},
		"inclusive": {"true"}, "limit": {"1"},
	}
	if threadRootID != "" {
		resource = "conversations.replies"
		query.Set("ts", threadRootID)
	}
	var response struct {
		slackEnvelope
		Messages []slackMessage `json:"messages"`
	}
	result, err := client.api.DoJSON(
		ctx, http.MethodGet, resource, query, nil, &response, false, nil,
		http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		return slackMessage{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return slackMessage{}, err
	}
	if len(response.Messages) != 1 || response.Messages[0].TS != messageID {
		return slackMessage{}, errors.New("slack did not return the exact selected message")
	}
	return response.Messages[0], nil
}

func (client *Client) requireCapability(enabled bool, feature string) error {
	if client == nil || client.api == nil || !validSlackID(client.workspaceID) {
		return errors.New("the Slack messaging service is not enabled")
	}
	if !enabled {
		return fmt.Errorf("the Slack authorization does not support %s", feature)
	}
	return nil
}

func slackWarningDegradations(envelope slackEnvelope) []domain.Degradation {
	codes := append([]string(nil), envelope.Metadata.Warnings...)
	if envelope.Warning != "" {
		codes = append(codes, strings.Split(envelope.Warning, ",")...)
	}
	result := make([]domain.Degradation, 0, len(codes))
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if !validSlackCode(code) {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, domain.Degradation{
			Feature: "messages.provider_warning", Reason: "Slack reported warning " + code,
		})
	}
	return result
}
