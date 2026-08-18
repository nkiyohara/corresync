package teamsgraph

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

type graphWriteBody struct {
	Body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	Mentions []graphWriteMention `json:"mentions,omitempty"`
}

type graphWriteMention struct {
	ID          int              `json:"id"`
	MentionText string           `json:"mentionText"`
	Mentioned   graphIdentitySet `json:"mentioned"`
}

func (client *Client) SendMessage(
	ctx context.Context,
	input application.MessageSendInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.Send, "message send"); err != nil {
		return application.Message{}, err
	}
	if input.ReplyToID != "" && !client.capabilities.Reply {
		return application.Message{}, errors.New("the Teams Graph route does not support replies")
	}
	if len(input.Attachments) != 0 {
		return application.Message{}, errors.New("the Teams Graph attachment writes are not enabled")
	}
	if err := teamscontract.ValidateWriteContent(input.Content); err != nil {
		return application.Message{}, err
	}
	locator, err := decodeGraphConversationID(input.ConversationID)
	if err != nil {
		return application.Message{}, err
	}
	body, err := graphMessageWriteBody(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	resource, err := locator.collectionResource("")
	if err != nil {
		return application.Message{}, err
	}
	request := any(body)
	if input.ReplyToID != "" {
		if !validGraphOpaque(input.ReplyToID) {
			return application.Message{}, errors.New("the Teams Graph reply target is malformed")
		}
		if locator.isChat() {
			resource += "/replyWithQuote"
			request = struct {
				MessageIDs   []string       `json:"messageIds"`
				ReplyMessage graphWriteBody `json:"replyMessage"`
			}{MessageIDs: []string{input.ReplyToID}, ReplyMessage: body}
		} else {
			resource += "/" + graphPathSegment(input.ReplyToID) + "/replies"
		}
	}
	var response graphMessage
	result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, request, &response,
		true, nil, http.StatusCreated, http.StatusTooManyRequests)
	if err != nil {
		return application.Message{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.Message{}, err
	}
	if err := validateGraphWriteMessage(response, locator, input.ReplyToID); err != nil {
		return application.Message{}, errors.Join(application.ErrWriteOutcomeUnknown, err)
	}
	message, err := mapGraphMessage(response, input.ConversationID, client.actor.ID)
	if err != nil {
		return application.Message{}, errors.Join(application.ErrWriteOutcomeUnknown, err)
	}
	return message, nil
}

func (client *Client) EditMessage(
	ctx context.Context,
	input application.MessageEditInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.Edit, "message edit"); err != nil {
		return application.Message{}, err
	}
	if err := teamscontract.ValidateWriteContent(input.Content); err != nil {
		return application.Message{}, err
	}
	locator, source, err := client.requireGraphMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	)
	if err != nil {
		return application.Message{}, err
	}
	if err := client.requireOwnGraphMessage(source); err != nil {
		return application.Message{}, err
	}
	body, err := graphMessageWriteBody(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	resource, err := locator.messageResource(input.ThreadRootID, input.MessageID)
	if err != nil {
		return application.Message{}, err
	}
	var response graphMessage
	result, err := client.api.DoJSON(ctx, http.MethodPatch, resource, nil, body, &response,
		true, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.Message{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.Message{}, err
	}
	if response.ID != input.MessageID {
		return application.Message{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			errors.New("the Microsoft Graph response contains a different edited Teams message"),
		)
	}
	if err := validateGraphMessageRoute(response, locator); err != nil {
		return application.Message{}, errors.Join(application.ErrWriteOutcomeUnknown, err)
	}
	message, err := mapGraphMessage(response, input.ConversationID, client.actor.ID)
	if err != nil {
		return application.Message{}, errors.Join(application.ErrWriteOutcomeUnknown, err)
	}
	return message, nil
}

func (client *Client) DeleteMessage(ctx context.Context, input application.MessageDeleteInput) error {
	if err := client.requireCapability(client.capabilities.Delete, "message deletion"); err != nil {
		return err
	}
	locator, source, err := client.requireGraphMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	)
	if err != nil {
		return err
	}
	if err := client.requireOwnGraphMessage(source); err != nil {
		return err
	}
	resource, err := client.graphMessageActionResource(
		locator, input.ThreadRootID, input.MessageID, "softDelete", true,
	)
	if err != nil {
		return err
	}
	result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, nil, nil,
		true, nil, http.StatusNoContent, http.StatusTooManyRequests)
	if err != nil {
		return err
	}
	return validateGraphResult(result)
}

func (client *Client) SetMessageReaction(
	ctx context.Context,
	input application.MessageReactionInput,
) (application.MessageReaction, error) {
	if err := client.requireCapability(client.capabilities.Reactions, "message reactions"); err != nil {
		return application.MessageReaction{}, err
	}
	if err := teamscontract.ValidateReaction(input.Reaction); err != nil {
		return application.MessageReaction{}, err
	}
	locator, _, err := client.requireGraphMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	)
	if err != nil {
		return application.MessageReaction{}, err
	}
	action := "setReaction"
	if input.Remove {
		action = "unsetReaction"
	}
	resource, err := client.graphMessageActionResource(
		locator, input.ThreadRootID, input.MessageID, action, false,
	)
	if err != nil {
		return application.MessageReaction{}, err
	}
	result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, struct {
		ReactionType string `json:"reactionType"`
	}{ReactionType: input.Reaction}, nil, true, nil,
		http.StatusNoContent, http.StatusTooManyRequests)
	if err != nil {
		return application.MessageReaction{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.MessageReaction{}, err
	}
	return application.MessageReaction{
		Name: input.Reaction, CountKnown: false, ReactedByActor: !input.Remove,
	}, nil
}

func (client *Client) CreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	if err := client.requireCapability(client.capabilities.CreateConversation, "conversation creation"); err != nil {
		return application.Conversation{}, err
	}
	switch input.Kind {
	case application.ConversationDirect, application.ConversationGroup:
		return client.createGraphChat(ctx, input)
	case application.ConversationChannel:
		return client.createGraphChannel(ctx, input)
	case application.ConversationMeeting:
		return application.Conversation{}, errors.New("the Teams meeting lifecycle creation is outside messaging scope")
	default:
		return application.Conversation{}, errors.New("the Teams Graph conversation kind is unsupported")
	}
}

type graphConversationMemberWrite struct {
	ODataType string   `json:"@odata.type"`
	Roles     []string `json:"roles"`
	UserBind  string   `json:"user@odata.bind"`
}

func (client *Client) createGraphChat(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	if input.ContainerID != "" || input.Visibility != application.ConversationVisibilityPrivate {
		return application.Conversation{}, errors.New("the Teams chats require no container and private visibility")
	}
	if input.Topic != "" || input.Kind == application.ConversationDirect && input.Name != "" {
		return application.Conversation{}, errors.New("the Teams direct chats have no title and expose no separate topic")
	}
	if input.Kind == application.ConversationDirect && len(input.Members) != 2 {
		return application.Conversation{}, errors.New("a Teams direct chat requires exactly two members including the actor")
	}
	if input.Kind == application.ConversationGroup && len(input.Members) < 3 {
		return application.Conversation{}, errors.New("a Teams group chat requires at least three members including the actor")
	}
	members, err := client.graphMemberWrites(input.Members, true, true)
	if err != nil {
		return application.Conversation{}, err
	}
	chatType := "group"
	if input.Kind == application.ConversationDirect {
		chatType = "oneOnOne"
	}
	request := struct {
		ChatType string                         `json:"chatType"`
		Topic    string                         `json:"topic,omitempty"`
		Members  []graphConversationMemberWrite `json:"members"`
	}{ChatType: chatType, Topic: input.Name, Members: members}
	var response graphChat
	result, err := client.api.DoJSON(ctx, http.MethodPost, "chats", nil, request, &response,
		true, nil, http.StatusCreated, http.StatusTooManyRequests)
	if err != nil {
		return application.Conversation{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.Conversation{}, err
	}
	conversation, err := mapGraphChat(response)
	if err != nil || conversation.Kind != input.Kind {
		return application.Conversation{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			errors.New("the Microsoft Graph response contains a mismatched Teams chat result"), err,
		)
	}
	return conversation, nil
}

func (client *Client) createGraphChannel(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	teamID, err := decodeGraphTeamID(input.ContainerID)
	if err != nil {
		return application.Conversation{}, err
	}
	if input.Name == "" || len(input.Name) > 50 {
		return application.Conversation{}, errors.New("a Teams channel name must contain at most 50 bytes")
	}
	membershipType := "standard"
	var members []graphConversationMemberWrite
	switch input.Visibility {
	case application.ConversationVisibilityUnknown:
		return application.Conversation{}, errors.New("the Teams channel creation requires observed public or private visibility")
	case application.ConversationVisibilityPublic:
		for _, member := range input.Members {
			if member.ID != client.actor.ID {
				return application.Conversation{}, errors.New("a standard Teams channel inherits team membership and cannot add selected members atomically")
			}
		}
	case application.ConversationVisibilityPrivate:
		membershipType = "private"
		members, err = client.graphMemberWrites(input.Members, false, true)
		if err != nil {
			return application.Conversation{}, err
		}
	case application.ConversationVisibilityShared:
		return application.Conversation{}, errors.New("shared Teams channel creation is asynchronous and is not enabled")
	}
	request := struct {
		DisplayName    string                         `json:"displayName"`
		Description    string                         `json:"description,omitempty"`
		MembershipType string                         `json:"membershipType"`
		Members        []graphConversationMemberWrite `json:"members,omitempty"`
	}{DisplayName: input.Name, Description: input.Topic, MembershipType: membershipType, Members: members}
	resource := "teams/" + graphPathSegment(teamID) + "/channels"
	var response graphChannel
	result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, request, &response,
		true, nil, http.StatusCreated, http.StatusTooManyRequests)
	if err != nil {
		return application.Conversation{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.Conversation{}, err
	}
	conversation, err := mapGraphChannel(graphTeam{ID: teamID}, response)
	if err != nil || conversation.ContainerID != input.ContainerID ||
		conversation.Visibility != input.Visibility {
		return application.Conversation{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			errors.New("the Microsoft Graph response contains a mismatched Teams channel result"), err,
		)
	}
	return conversation, nil
}

func (client *Client) ChangeConversationMembership(
	ctx context.Context,
	input application.ConversationMembershipInput,
) (application.ConversationMembershipResult, error) {
	if err := client.requireCapability(client.capabilities.Membership, "conversation membership"); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	locator, err := decodeGraphConversationID(input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	before, err := client.getGraphConversation(ctx, locator)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if before.Version != input.Version {
		return application.ConversationMembershipResult{}, restapi.ErrPrecondition
	}
	if !locator.isChat() && before.Visibility == application.ConversationVisibilityPublic {
		return application.ConversationMembershipResult{}, errors.New("standard Teams channels inherit team membership")
	}
	resource := graphMembershipCollection(locator)
	membershipID, exists, err := client.findGraphMember(ctx, resource, input.Member.ID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if input.Action == application.MembershipAdd {
		if exists {
			return application.ConversationMembershipResult{}, restapi.ErrPrecondition
		}
		members, err := client.graphMemberWrites([]application.ConversationMemberInput{input.Member}, locator.isChat(), false)
		if err != nil {
			return application.ConversationMembershipResult{}, err
		}
		result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, members[0], nil,
			true, nil, http.StatusCreated, http.StatusTooManyRequests)
		if err != nil {
			return application.ConversationMembershipResult{}, err
		}
		if err := validateGraphResult(result); err != nil {
			return application.ConversationMembershipResult{}, err
		}
	} else {
		if !exists {
			return application.ConversationMembershipResult{}, restapi.ErrPrecondition
		}
		result, err := client.api.DoJSON(ctx, http.MethodDelete,
			resource+"/"+graphPathSegment(membershipID), nil, nil, nil,
			true, nil, http.StatusNoContent, http.StatusTooManyRequests)
		if err != nil {
			return application.ConversationMembershipResult{}, err
		}
		if err := validateGraphResult(result); err != nil {
			return application.ConversationMembershipResult{}, err
		}
	}
	after, err := client.getGraphConversation(ctx, locator)
	if err != nil {
		return application.ConversationMembershipResult{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			errors.New("confirm Teams Graph membership result"),
		)
	}
	return application.ConversationMembershipResult{
		ConversationID: input.ConversationID, Version: after.Version,
		Action: input.Action, Member: input.Member,
	}, nil
}

func graphMessageWriteBody(
	content application.MessageContent,
	mentions []application.MessageMention,
) (graphWriteBody, error) {
	if content.Format == application.MessageFormatMarkdown {
		return graphWriteBody{}, errors.New("the Microsoft Graph Teams endpoint does not accept canonical Markdown")
	}
	result := graphWriteBody{}
	result.Body.ContentType = "text"
	result.Body.Content = content.Text
	if content.Format == application.MessageFormatHTML || len(mentions) != 0 {
		result.Body.ContentType = "html"
		if content.Format == application.MessageFormatPlain {
			result.Body.Content = html.EscapeString(content.Text)
		}
	}
	for index, mention := range mentions {
		label := mention.DisplayName
		if label == "" {
			label = mention.ID
		}
		result.Body.Content += fmt.Sprintf(" <at id=\"%d\">%s</at>", index, html.EscapeString(label))
		item := graphWriteMention{ID: index, MentionText: label}
		switch mention.Kind {
		case application.MessageMentionUser:
			item.Mentioned.User = &graphIdentity{ID: mention.ID, DisplayName: label}
		case application.MessageMentionChannel:
			item.Mentioned.Conversation = &graphConversationIdentity{
				ID: mention.ID, DisplayName: label, ConversationIdentityType: "channel",
			}
		default:
			return graphWriteBody{}, errors.New("the Teams Graph mention kind is unsupported")
		}
		result.Mentions = append(result.Mentions, item)
	}
	if len(result.Body.Content) > application.MaxMessageTextBytes {
		return graphWriteBody{}, errors.New("rendered Teams Graph message exceeds the configured limit")
	}
	return result, nil
}

func (client *Client) requireGraphMessageVersion(
	ctx context.Context,
	conversationID, threadRootID, messageID, version string,
) (graphConversationLocator, graphMessage, error) {
	locator, err := decodeGraphConversationID(conversationID)
	if err != nil {
		return graphConversationLocator{}, graphMessage{}, err
	}
	source, err := client.getGraphMessage(ctx, conversationID, threadRootID, messageID)
	if err != nil {
		return graphConversationLocator{}, graphMessage{}, err
	}
	if graphMessageVersion(source) != version {
		return graphConversationLocator{}, graphMessage{}, restapi.ErrPrecondition
	}
	return locator, source, nil
}

func (client *Client) requireOwnGraphMessage(source graphMessage) error {
	if source.From == nil || source.From.User == nil || source.From.User.ID != client.actor.ID {
		return errors.New("the Microsoft Graph route permits the delegated actor to change only its own Teams message")
	}
	return nil
}

func (client *Client) graphMessageActionResource(
	locator graphConversationLocator,
	threadRootID, messageID, action string,
	userScopedChat bool,
) (string, error) {
	if locator.isChat() && userScopedChat {
		if threadRootID != "" || !validGraphOpaque(messageID) {
			return "", errors.New("the Teams Graph chat message action target is malformed")
		}
		return "users/" + graphPathSegment(client.actor.ID) + "/chats/" +
			graphPathSegment(locator.ChatID) + "/messages/" + graphPathSegment(messageID) + "/" + action, nil
	}
	resource, err := locator.messageResource(threadRootID, messageID)
	if err != nil {
		return "", err
	}
	return resource + "/" + action, nil
}

func validateGraphWriteMessage(
	message graphMessage,
	locator graphConversationLocator,
	replyToID string,
) error {
	if !validGraphOpaque(message.ID) {
		return errors.New("the Microsoft Graph response contains a malformed Teams write result")
	}
	if err := validateGraphMessageRoute(message, locator); err != nil {
		return err
	}
	if !locator.isChat() && replyToID != "" && message.ReplyToID != replyToID {
		return errors.New("the Microsoft Graph response contains a reply for another Teams thread")
	}
	return nil
}

func (client *Client) graphMemberWrites(
	members []application.ConversationMemberInput,
	chat, requireActor bool,
) ([]graphConversationMemberWrite, error) {
	result := make([]graphConversationMemberWrite, 0, len(members))
	actorPresent := false
	for _, member := range members {
		roles := []string(nil)
		switch member.Role {
		case application.ConversationOwner:
			roles = []string{"owner"}
		case application.ConversationGuest:
			if !chat {
				return nil, errors.New("the Teams channel membership does not accept a guest role")
			}
			roles = []string{"guest"}
		case application.ConversationMember:
			if chat {
				return nil, errors.New("the Teams chat creation requires an explicit owner or guest role")
			}
			roles = []string{}
		default:
			return nil, errors.New("the Teams Graph member role is unsupported")
		}
		if member.ID == client.actor.ID {
			actorPresent = true
		}
		result = append(result, graphConversationMemberWrite{
			ODataType: "#microsoft.graph.aadUserConversationMember", Roles: roles,
			UserBind: strings.TrimRight(client.apiBase.String(), "/") + "/users/" + graphPathSegment(member.ID),
		})
	}
	if requireActor && !actorPresent {
		return nil, errors.New("the Teams conversation creation must include the delegated actor")
	}
	return result, nil
}

func (client *Client) getGraphConversation(
	ctx context.Context,
	locator graphConversationLocator,
) (application.Conversation, error) {
	if locator.isChat() {
		var response graphChat
		resource := "chats/" + graphPathSegment(locator.ChatID)
		result, err := client.api.DoJSON(ctx, http.MethodGet, resource, nil, nil, &response,
			false, nil, http.StatusOK, http.StatusTooManyRequests)
		if err != nil {
			return application.Conversation{}, err
		}
		if err := validateGraphResult(result); err != nil {
			return application.Conversation{}, err
		}
		if response.ID != locator.ChatID {
			return application.Conversation{}, errors.New("the Microsoft Graph response contains another Teams chat")
		}
		return mapGraphChat(response)
	}
	var response graphChannel
	resource := "teams/" + graphPathSegment(locator.TeamID) + "/channels/" + graphPathSegment(locator.ChannelID)
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, nil, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.Conversation{}, err
	}
	if err := validateGraphResult(result); err != nil {
		return application.Conversation{}, err
	}
	if response.ID != locator.ChannelID {
		return application.Conversation{}, errors.New("the Microsoft Graph response contains another Teams channel")
	}
	return mapGraphChannel(graphTeam{ID: locator.TeamID}, response)
}

func graphMembershipCollection(locator graphConversationLocator) string {
	if locator.isChat() {
		return "chats/" + graphPathSegment(locator.ChatID) + "/members"
	}
	return "teams/" + graphPathSegment(locator.TeamID) + "/channels/" +
		graphPathSegment(locator.ChannelID) + "/members"
}

func (client *Client) findGraphMember(
	ctx context.Context,
	resource, userID string,
) (string, bool, error) {
	var response graphCollection[struct {
		ID     string `json:"id"`
		UserID string `json:"userId"`
	}]
	result, err := client.api.DoJSON(ctx, http.MethodGet, resource, nil, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return "", false, err
	}
	if err := validateGraphResult(result); err != nil {
		return "", false, err
	}
	if response.NextLink != "" || len(response.Value) > maximumGraphItems {
		return "", false, errors.New("the Microsoft Graph response contains an unbounded Teams member set")
	}
	for _, member := range response.Value {
		if member.UserID == userID {
			if !validGraphOpaque(member.ID) {
				return "", false, errors.New("the Microsoft Graph response contains a malformed Teams membership ID")
			}
			return member.ID, true, nil
		}
	}
	return "", false, nil
}
