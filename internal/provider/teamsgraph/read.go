package teamsgraph

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type graphCollection[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink,omitempty"`
}

func (client *Client) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
) (application.ConversationPage, error) {
	if err := client.requireCapability(client.capabilities.ListConversations, "conversation listing"); err != nil {
		return application.ConversationPage{}, err
	}
	cursor := graphPageCursor{
		Version: 1, Kind: graphCursorConversations, Stage: graphStageChats,
		Account: input.Account, WorkspaceID: input.WorkspaceID,
	}
	if input.Cursor != "" {
		decoded, err := decodeGraphCursor(input.Cursor, graphPageCursor{
			Kind: graphCursorConversations, Account: input.Account, WorkspaceID: input.WorkspaceID,
		})
		if err != nil {
			return application.ConversationPage{}, err
		}
		cursor = decoded
	}
	if cursor.Stage == graphStageChats {
		return client.listGraphChats(ctx, input, cursor)
	}
	return client.listGraphChannels(ctx, input, cursor)
}

func (client *Client) listGraphChats(
	ctx context.Context,
	input application.ConversationListInput,
	cursor graphPageCursor,
) (application.ConversationPage, error) {
	resource := "me/chats"
	query := url.Values{
		"$top":     {graphTop(input.Limit, 50)},
		"$orderby": {"lastMessagePreview/createdDateTime desc"},
		"$select":  {"id,topic,chatType,createdDateTime,lastUpdatedDateTime"},
	}
	if cursor.NextLink != "" {
		continued, err := client.continuation(cursor.NextLink, resource, query)
		if err != nil {
			return application.ConversationPage{}, err
		}
		query = continued
	}
	var response graphCollection[graphChat]
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, query, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.ConversationPage{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.ConversationPage{}, err
	}
	if len(response.Value) > min(input.Limit, 50) {
		return application.ConversationPage{}, errors.New("the Microsoft Graph response contains too many Teams chats")
	}
	conversations := make([]application.Conversation, 0, len(response.Value))
	for _, source := range response.Value {
		conversation, err := mapGraphChat(source)
		if err != nil {
			return application.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	next := graphPageCursor{
		Version: 1, Kind: graphCursorConversations, Account: input.Account,
		WorkspaceID: input.WorkspaceID,
	}
	if response.NextLink != "" {
		if _, err := client.continuation(response.NextLink, resource, queryWithoutSkipToken(query)); err != nil {
			return application.ConversationPage{}, err
		}
		next.Stage, next.NextLink = graphStageChats, response.NextLink
	} else {
		next.Stage = graphStageChannels
	}
	nextCursor, err := encodeGraphCursor(next)
	if err != nil {
		return application.ConversationPage{}, err
	}
	return application.ConversationPage{
		Conversations: conversations, NextCursor: nextCursor, ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *Client) listGraphChannels(
	ctx context.Context,
	input application.ConversationListInput,
	cursor graphPageCursor,
) (application.ConversationPage, error) {
	teams, err := client.joinedGraphTeams(ctx)
	if err != nil {
		return application.ConversationPage{}, err
	}
	if cursor.TeamIndex >= len(teams) {
		return application.ConversationPage{
			Conversations: []application.Conversation{}, ObservedAt: time.Now().UTC(),
		}, nil
	}
	team := teams[cursor.TeamIndex]
	if cursor.TeamID != "" && cursor.TeamID != team.ID {
		return application.ConversationPage{}, errors.New("the Teams Graph channel cursor no longer matches the joined team set")
	}
	resource := "teams/" + graphPathSegment(team.ID) + "/channels"
	query := url.Values{
		"$select": {"id,displayName,description,membershipType,createdDateTime"},
		"$top":    {graphTop(input.Limit, 50)},
	}
	if cursor.NextLink != "" {
		continued, err := client.continuation(cursor.NextLink, resource, query)
		if err != nil {
			return application.ConversationPage{}, err
		}
		query = continued
	}
	var response graphCollection[graphChannel]
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, query, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.ConversationPage{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.ConversationPage{}, err
	}
	if len(response.Value) > min(input.Limit, 50) {
		return application.ConversationPage{}, errors.New("the Microsoft Graph response contains too many Teams channels")
	}
	conversations := make([]application.Conversation, 0, len(response.Value))
	for _, source := range response.Value {
		conversation, err := mapGraphChannel(team, source)
		if err != nil {
			return application.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	nextCursor := ""
	next := graphPageCursor{
		Version: 1, Kind: graphCursorConversations, Stage: graphStageChannels,
		Account: input.Account, WorkspaceID: input.WorkspaceID,
	}
	switch {
	case response.NextLink != "":
		if _, err := client.continuation(response.NextLink, resource, queryWithoutSkipToken(query)); err != nil {
			return application.ConversationPage{}, err
		}
		next.TeamIndex, next.TeamID, next.NextLink = cursor.TeamIndex, team.ID, response.NextLink
	case cursor.TeamIndex+1 < len(teams):
		next.TeamIndex = cursor.TeamIndex + 1
	default:
		return application.ConversationPage{
			Conversations: conversations, ObservedAt: time.Now().UTC(),
		}, nil
	}
	nextCursor, err = encodeGraphCursor(next)
	if err != nil {
		return application.ConversationPage{}, err
	}
	return application.ConversationPage{
		Conversations: conversations, NextCursor: nextCursor, ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *Client) joinedGraphTeams(ctx context.Context) ([]graphTeam, error) {
	var response graphCollection[graphTeam]
	result, err := client.api.DoJSON(ctx, http.MethodGet, "me/joinedTeams", url.Values{
		"$select": {"id,displayName"},
	}, nil, &response, false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return nil, err
	}
	if err := validateGraphResult(result); err != nil {
		return nil, err
	}
	if response.NextLink != "" || len(response.Value) > maximumGraphItems {
		return nil, errors.New("the Microsoft Graph response contains an unbounded joined team set")
	}
	for _, team := range response.Value {
		if !validGraphOpaque(team.ID) {
			return nil, errors.New("the Microsoft Graph response contains a malformed joined team")
		}
	}
	return response.Value, nil
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
) (application.MessagePage, error) {
	if err := client.requireCapability(client.capabilities.History, "message history"); err != nil {
		return application.MessagePage{}, err
	}
	locator, err := decodeGraphConversationID(input.ConversationID)
	if err != nil {
		return application.MessagePage{}, err
	}
	resource, err := locator.collectionResource(input.ThreadRootID)
	if err != nil {
		return application.MessagePage{}, err
	}
	query := url.Values{"$top": {graphTop(input.Limit, 50)}}
	if input.Cursor != "" {
		cursor, err := decodeGraphCursor(input.Cursor, graphPageCursor{
			Kind: graphCursorMessages, Account: input.Account, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		query, err = client.continuation(cursor.NextLink, resource, query)
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	var response graphCollection[graphMessage]
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, query, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.MessagePage{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.MessagePage{}, err
	}
	if len(response.Value) > min(input.Limit, 50) {
		return application.MessagePage{}, errors.New("the Microsoft Graph response contains too many Teams messages")
	}
	messages := make([]application.MessageSummary, 0, len(response.Value))
	for _, source := range response.Value {
		if err := validateGraphMessageRoute(source, locator); err != nil {
			return application.MessagePage{}, err
		}
		summary, err := mapGraphSummary(source, input.ConversationID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	nextCursor := ""
	if response.NextLink != "" {
		if _, err := client.continuation(response.NextLink, resource, queryWithoutSkipToken(query)); err != nil {
			return application.MessagePage{}, err
		}
		nextCursor, err = encodeGraphCursor(graphPageCursor{
			Version: 1, Kind: graphCursorMessages, Account: input.Account,
			WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID,
			ThreadRootID: input.ThreadRootID, NextLink: response.NextLink,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	return application.MessagePage{
		Messages: messages, NextCursor: nextCursor, ObservedAt: time.Now().UTC(),
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
		return application.MessagePage{}, errors.New("the Teams Graph search cannot safely scope an opaque conversation locator")
	}
	offset := 0
	if input.Cursor != "" {
		cursor, err := decodeGraphCursor(input.Cursor, graphPageCursor{
			Kind: graphCursorSearch, Account: input.Account, WorkspaceID: input.WorkspaceID,
			QuerySHA256: graphQueryDigest(input.Query),
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		offset = cursor.Offset
	}
	limit := min(input.Limit, 25)
	request := struct {
		Requests []struct {
			EntityTypes []string `json:"entityTypes"`
			Query       struct {
				QueryString string `json:"queryString"`
			} `json:"query"`
			From int `json:"from"`
			Size int `json:"size"`
		} `json:"requests"`
	}{Requests: make([]struct {
		EntityTypes []string `json:"entityTypes"`
		Query       struct {
			QueryString string `json:"queryString"`
		} `json:"query"`
		From int `json:"from"`
		Size int `json:"size"`
	}, 1)}
	request.Requests[0].EntityTypes = []string{"chatMessage"}
	request.Requests[0].Query.QueryString = input.Query
	request.Requests[0].From = offset
	request.Requests[0].Size = limit
	var response struct {
		Value []struct {
			HitsContainers []struct {
				Hits []struct {
					HitID    string       `json:"hitId"`
					Resource graphMessage `json:"resource"`
				} `json:"hits"`
				MoreResultsAvailable bool `json:"moreResultsAvailable"`
			} `json:"hitsContainers"`
		} `json:"value"`
	}
	result, err := client.api.DoJSON(ctx, http.MethodPost, "search/query", nil, request, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.MessagePage{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.MessagePage{}, err
	}
	if len(response.Value) != 1 || len(response.Value[0].HitsContainers) != 1 ||
		len(response.Value[0].HitsContainers[0].Hits) > limit {
		return application.MessagePage{}, errors.New("the Microsoft Graph response contains malformed Teams search results")
	}
	container := response.Value[0].HitsContainers[0]
	messages := make([]application.MessageSummary, 0, len(container.Hits))
	for _, hit := range container.Hits {
		if hit.Resource.ID == "" {
			hit.Resource.ID = hit.HitID
		}
		conversationID, locatorErr := graphMessageConversationID(hit.Resource)
		if locatorErr != nil {
			return application.MessagePage{}, locatorErr
		}
		summary, err := mapGraphSummary(hit.Resource, conversationID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	nextCursor := ""
	partial, reason := false, ""
	if container.MoreResultsAvailable {
		nextOffset := offset + len(container.Hits)
		if len(container.Hits) == 0 || nextOffset > 10_000 {
			partial, reason = true, "provider_limit"
		} else {
			nextCursor, err = encodeGraphCursor(graphPageCursor{
				Version: 1, Kind: graphCursorSearch, Account: input.Account,
				WorkspaceID: input.WorkspaceID, QuerySHA256: graphQueryDigest(input.Query),
				Offset: nextOffset,
			})
			if err != nil {
				return application.MessagePage{}, err
			}
		}
	}
	return application.MessagePage{
		Messages: messages, NextCursor: nextCursor, Partial: partial,
		PartialReason: reason, ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *Client) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.SensitiveRead, "message body reads"); err != nil {
		return application.Message{}, err
	}
	source, err := client.getGraphMessage(ctx, input.ConversationID, input.ThreadRootID, input.MessageID)
	if err != nil {
		return application.Message{}, err
	}
	return mapGraphMessage(source, input.ConversationID, client.actor.ID)
}

func (client *Client) GetMessageAttachment(
	context.Context,
	application.MessageAttachmentGetInput,
) (application.MessageAttachmentContent, error) {
	return application.MessageAttachmentContent{}, errors.New("the Teams Graph attachment reads are not enabled")
}

func (client *Client) SyncMessages(
	context.Context,
	application.MessageSyncInput,
) (application.MessageChangePage, error) {
	return application.MessageChangePage{}, errors.New("the Teams Graph incremental synchronization is not enabled")
}

func (client *Client) getGraphMessage(
	ctx context.Context,
	conversationID, threadRootID, messageID string,
) (graphMessage, error) {
	locator, err := decodeGraphConversationID(conversationID)
	if err != nil {
		return graphMessage{}, err
	}
	resource, err := locator.messageResource(threadRootID, messageID)
	if err != nil {
		return graphMessage{}, err
	}
	var response graphMessage
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, nil, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return graphMessage{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return graphMessage{}, err
	}
	if response.ID != messageID {
		return graphMessage{}, errors.New("the Microsoft Graph response does not contain the exact selected Teams message")
	}
	if err := validateGraphMessageRoute(response, locator); err != nil {
		return graphMessage{}, err
	}
	return response, nil
}

func graphMessageConversationID(message graphMessage) (string, error) {
	if message.ChatID != "" {
		return encodeGraphChatID(message.ChatID)
	}
	return encodeGraphChannelID(message.ChannelIdentity.TeamID, message.ChannelIdentity.ChannelID)
}

func queryWithoutSkipToken(query url.Values) url.Values {
	result := cloneGraphQuery(query)
	result.Del("$skiptoken")
	return result
}

var _ application.MessagingPort = (*Client)(nil)
