package mattermostapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func (client *Client) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
) (application.ConversationPage, error) {
	if err := client.requireCapability(client.capabilities.ListConversations, "conversation listing"); err != nil {
		return application.ConversationPage{}, err
	}
	if input.WorkspaceID != client.workspaceID {
		return application.ConversationPage{}, errors.New("mattermost conversation route selected a different team")
	}
	var source []mattermostChannel
	if err := client.doJSON(ctx, http.MethodGet, "users/me/channels", nil, nil, &source, false, http.StatusOK); err != nil {
		return application.ConversationPage{}, err
	}
	if len(source) > maximumMattermostItems {
		return application.ConversationPage{}, errors.New("mattermost returned too many conversations")
	}
	conversations := make([]application.Conversation, 0, len(source))
	for _, channel := range source {
		if channel.DeleteAt != 0 {
			continue
		}
		if (channel.Type == "O" || channel.Type == "P") && channel.TeamID != client.workspaceID {
			continue
		}
		conversation, err := mapMattermostConversation(channel, client.workspaceID)
		if err != nil {
			return application.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	sort.Slice(conversations, func(left, right int) bool {
		return conversations[left].ID < conversations[right].ID
	})
	snapshot := mattermostConversationSnapshot(conversations)
	offset := 0
	if input.Cursor != "" {
		cursor, err := decodeMattermostCursor(input.Cursor, mattermostCursor{
			Kind: mattermostCursorConversations, Account: input.Account,
			WorkspaceID: input.WorkspaceID,
		})
		if err != nil {
			return application.ConversationPage{}, err
		}
		if cursor.SnapshotSHA256 != snapshot || cursor.Offset > len(conversations) {
			return application.ConversationPage{}, restapi.ErrPrecondition
		}
		offset = cursor.Offset
	}
	end := min(offset+input.Limit, len(conversations))
	page := append([]application.Conversation(nil), conversations[offset:end]...)
	next := ""
	if end < len(conversations) {
		var err error
		next, err = encodeMattermostCursor(mattermostCursor{
			Version: 1, Kind: mattermostCursorConversations, Account: input.Account,
			WorkspaceID: input.WorkspaceID, SnapshotSHA256: snapshot, Offset: end,
		})
		if err != nil {
			return application.ConversationPage{}, err
		}
	}
	return application.ConversationPage{
		Conversations: page, NextCursor: next, ObservedAt: time.Now().UTC(),
	}, nil
}

func mattermostConversationSnapshot(conversations []application.Conversation) string {
	var value strings.Builder
	for _, conversation := range conversations {
		value.WriteString(conversation.ID)
		value.WriteByte(0)
		value.WriteString(conversation.Version)
		value.WriteByte(0)
	}
	return mattermostDigest(value.String())
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
) (application.MessagePage, error) {
	if err := client.requireCapability(client.capabilities.History, "message history"); err != nil {
		return application.MessagePage{}, err
	}
	if input.WorkspaceID != client.workspaceID || !validMattermostID(input.ConversationID) ||
		input.ThreadRootID != "" && !validMattermostID(input.ThreadRootID) {
		return application.MessagePage{}, errors.New("mattermost message route is malformed")
	}
	if _, err := client.getMattermostConversation(ctx, input.ConversationID); err != nil {
		return application.MessagePage{}, err
	}
	kind := mattermostCursorMessages
	resource := "channels/" + input.ConversationID + "/posts"
	if input.ThreadRootID != "" {
		kind = mattermostCursorThread
		resource = "posts/" + input.ThreadRootID + "/thread"
	}
	page := 0
	if input.Cursor != "" {
		cursor, err := decodeMattermostCursor(input.Cursor, mattermostCursor{
			Kind: kind, Account: input.Account, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		page = cursor.Page
	}
	limit := min(input.Limit, application.MaxMessagePageSize)
	posts, err := client.getPostPage(ctx, resource, url.Values{
		"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(limit + 1)},
	}, limit+1)
	if err != nil {
		return application.MessagePage{}, err
	}
	if input.ThreadRootID != "" {
		for _, post := range posts {
			if post.ID != input.ThreadRootID && post.RootID != input.ThreadRootID {
				return application.MessagePage{}, errors.New("mattermost thread response escaped the selected root")
			}
		}
	}
	hasMore := len(posts) > limit
	if hasMore {
		posts = posts[:limit]
	}
	messages := make([]application.MessageSummary, 0, len(posts))
	for _, post := range posts {
		summary, err := mapMattermostSummary(post, input.ConversationID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	next := ""
	if hasMore {
		next, err = encodeMattermostCursor(mattermostCursor{
			Version: 1, Kind: kind, Account: input.Account, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID, Page: page + 1,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	return application.MessagePage{Messages: messages, NextCursor: next, ObservedAt: time.Now().UTC()}, nil
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
) (application.MessagePage, error) {
	if err := client.requireCapability(client.capabilities.Search, "message search"); err != nil {
		return application.MessagePage{}, err
	}
	if input.WorkspaceID != client.workspaceID ||
		input.ConversationID != "" && !validMattermostID(input.ConversationID) {
		return application.MessagePage{}, errors.New("mattermost search route is malformed")
	}
	queryDigest := mattermostDigest(input.Query)
	page := 0
	if input.Cursor != "" {
		cursor, err := decodeMattermostCursor(input.Cursor, mattermostCursor{
			Kind: mattermostCursorSearch, Account: input.Account,
			WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID,
			QuerySHA256: queryDigest,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
		page = cursor.Page
	}
	limit := min(input.Limit, application.MaxMessagePageSize)
	var response mattermostPostList
	if err := client.doJSON(
		ctx, http.MethodPost, "teams/"+client.workspaceID+"/posts/search", nil,
		struct {
			Terms   string `json:"terms"`
			Page    int    `json:"page"`
			PerPage int    `json:"per_page"`
		}{Terms: input.Query, Page: page, PerPage: limit + 1},
		&response, false, http.StatusOK,
	); err != nil {
		return application.MessagePage{}, err
	}
	posts, err := orderedMattermostPosts(response, limit+1)
	if err != nil {
		return application.MessagePage{}, err
	}
	messages := make([]application.MessageSummary, 0, min(len(posts), limit))
	conversationCache := make(map[string]struct{})
	providerHasMore := len(posts) > limit
	if providerHasMore {
		posts = posts[:limit]
	}
	for _, post := range posts {
		if input.ConversationID != "" && post.ChannelID != input.ConversationID {
			continue
		}
		if _, confirmed := conversationCache[post.ChannelID]; !confirmed {
			if _, err := client.getMattermostConversation(ctx, post.ChannelID); err != nil {
				return application.MessagePage{}, err
			}
			conversationCache[post.ChannelID] = struct{}{}
		}
		summary, err := mapMattermostSummary(post, post.ChannelID)
		if err != nil {
			return application.MessagePage{}, err
		}
		messages = append(messages, summary)
	}
	next := ""
	if providerHasMore {
		next, err = encodeMattermostCursor(mattermostCursor{
			Version: 1, Kind: mattermostCursorSearch, Account: input.Account,
			WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID,
			QuerySHA256: queryDigest, Page: page + 1,
		})
		if err != nil {
			return application.MessagePage{}, err
		}
	}
	return application.MessagePage{
		Messages: messages, NextCursor: next,
		Partial: input.ConversationID != "" && len(messages) < len(posts),
		PartialReason: func() string {
			if input.ConversationID != "" && len(messages) < len(posts) {
				return "provider_filter"
			}
			return ""
		}(),
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *Client) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.SensitiveRead, "message body reads"); err != nil {
		return application.Message{}, err
	}
	if input.WorkspaceID != client.workspaceID || !validMattermostID(input.ConversationID) ||
		!validMattermostID(input.MessageID) || input.ThreadRootID != "" && !validMattermostID(input.ThreadRootID) {
		return application.Message{}, errors.New("mattermost message identity is malformed")
	}
	return client.getMattermostMessage(ctx, input.ConversationID, input.ThreadRootID, input.MessageID)
}

func (client *Client) GetMessageAttachment(
	ctx context.Context,
	input application.MessageAttachmentGetInput,
) (application.MessageAttachmentContent, error) {
	if err := client.requireCapability(client.capabilities.AttachmentReads, "attachment reads"); err != nil {
		return application.MessageAttachmentContent{}, err
	}
	if input.WorkspaceID != client.workspaceID {
		return application.MessageAttachmentContent{}, errors.New("mattermost attachment route selected a different team")
	}
	message, err := client.getMattermostMessage(ctx, input.ConversationID, input.ThreadRootID, input.MessageID)
	if err != nil {
		return application.MessageAttachmentContent{}, err
	}
	var selected *application.MessageAttachment
	for index := range message.Attachments {
		if message.Attachments[index].ID == input.AttachmentID {
			selected = &message.Attachments[index]
			break
		}
	}
	if selected == nil || !selected.Downloadable || selected.Size > application.MaxMessageAttachmentBytes {
		return application.MessageAttachmentContent{}, errors.New("mattermost attachment is unavailable or exceeds the configured limit")
	}
	result, err := client.do(ctx, http.MethodGet, "files/"+selected.ID, nil, false, http.StatusOK)
	if err != nil {
		return application.MessageAttachmentContent{}, err
	}
	if int64(len(result.Body)) != selected.Size || len(result.Body) > application.MaxMessageAttachmentBytes {
		return application.MessageAttachmentContent{}, errors.New("mattermost attachment body does not match its bounded metadata")
	}
	return application.MessageAttachmentContent{Metadata: *selected, Data: result.Body}, nil
}

func (client *Client) getMattermostMessage(
	ctx context.Context,
	conversationID, threadRootID, messageID string,
) (application.Message, error) {
	post, err := client.getMattermostPost(ctx, conversationID, messageID)
	if err != nil {
		return application.Message{}, err
	}
	if post.RootID != threadRootID {
		return application.Message{}, errors.New("mattermost message does not match the selected thread")
	}
	var files []mattermostFileInfo
	if len(post.FileIDs) != 0 {
		if err := client.doJSON(ctx, http.MethodGet, "posts/"+post.ID+"/files/info", nil, nil, &files, false, http.StatusOK); err != nil {
			return application.Message{}, err
		}
	}
	var reactions []mattermostReaction
	if err := client.doJSON(ctx, http.MethodGet, "posts/"+post.ID+"/reactions", nil, nil, &reactions, false, http.StatusOK); err != nil {
		return application.Message{}, err
	}
	return mapMattermostMessage(post, conversationID, client.actor.ID, files, reactions)
}

func (client *Client) getMattermostPost(
	ctx context.Context,
	conversationID, messageID string,
) (mattermostPost, error) {
	if !validMattermostID(conversationID) || !validMattermostID(messageID) {
		return mattermostPost{}, errors.New("mattermost message identity is malformed")
	}
	if _, err := client.getMattermostConversation(ctx, conversationID); err != nil {
		return mattermostPost{}, err
	}
	var post mattermostPost
	if err := client.doJSON(ctx, http.MethodGet, "posts/"+messageID, nil, nil, &post, false, http.StatusOK); err != nil {
		return mattermostPost{}, err
	}
	if post.ID != messageID || post.ChannelID != conversationID {
		return mattermostPost{}, errors.New("mattermost returned a different selected message")
	}
	if _, err := mapMattermostSummary(post, conversationID); err != nil {
		return mattermostPost{}, err
	}
	return post, nil
}

func (client *Client) getMattermostConversation(ctx context.Context, id string) (mattermostChannel, error) {
	if !validMattermostID(id) {
		return mattermostChannel{}, errors.New("mattermost conversation ID is malformed")
	}
	var channel mattermostChannel
	if err := client.doJSON(ctx, http.MethodGet, "channels/"+id, nil, nil, &channel, false, http.StatusOK); err != nil {
		return mattermostChannel{}, err
	}
	if channel.ID != id {
		return mattermostChannel{}, errors.New("mattermost returned a different selected conversation")
	}
	if _, err := mapMattermostConversation(channel, client.workspaceID); err != nil {
		return mattermostChannel{}, err
	}
	return channel, nil
}

func (client *Client) getPostPage(
	ctx context.Context,
	resource string,
	query url.Values,
	maximum int,
) ([]mattermostPost, error) {
	var response mattermostPostList
	if err := client.doJSON(ctx, http.MethodGet, resource, query, nil, &response, false, http.StatusOK); err != nil {
		return nil, err
	}
	return orderedMattermostPosts(response, maximum)
}

func orderedMattermostPosts(response mattermostPostList, maximum int) ([]mattermostPost, error) {
	if len(response.Order) > maximum || len(response.Posts) > maximumMattermostItems {
		return nil, errors.New("mattermost returned too many posts")
	}
	result := make([]mattermostPost, 0, len(response.Order))
	seen := make(map[string]struct{}, len(response.Order))
	for _, id := range response.Order {
		if !validMattermostID(id) {
			return nil, errors.New("mattermost returned a malformed post order")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("mattermost returned a duplicate post")
		}
		post, exists := response.Posts[id]
		if !exists || post.ID != id {
			return nil, errors.New("mattermost omitted an ordered post")
		}
		seen[id] = struct{}{}
		result = append(result, post)
	}
	return result, nil
}
