package slackapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func (client *Client) SendMessage(
	ctx context.Context,
	input application.MessageSendInput,
) (application.Message, error) {
	if err := client.requireCapability(client.capabilities.Send, "message send"); err != nil {
		return application.Message{}, err
	}
	if len(input.Attachments) != 0 {
		return application.Message{}, errors.New("slack attachment writes are not enabled")
	}
	text, markdown, err := slackWriteText(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	request := struct {
		Channel  string `json:"channel"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts,omitempty"`
		Markdown bool   `json:"mrkdwn"`
	}{Channel: input.ConversationID, Text: text, ThreadTS: input.ReplyToID, Markdown: markdown}
	if !validSlackID(request.Channel) || request.ThreadTS != "" && !validSlackTimestamp(request.ThreadTS) {
		return application.Message{}, errors.New("slack send target is malformed")
	}
	var response struct {
		slackEnvelope
		Channel string       `json:"channel"`
		TS      string       `json:"ts"`
		Message slackMessage `json:"message"`
	}
	result, err := client.api.DoJSON(
		ctx, http.MethodPost, "chat.postMessage", nil, request, &response, true,
		nil, http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		return application.Message{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, true); err != nil {
		return application.Message{}, err
	}
	if response.Channel != input.ConversationID || !validSlackTimestamp(response.TS) {
		return application.Message{}, fmt.Errorf("%w: Slack returned a mismatched send result", application.ErrWriteOutcomeUnknown)
	}
	if response.Message.TS == "" {
		response.Message.TS = response.TS
	}
	message, err := mapSlackMessage(response.Message, input.ConversationID, client.actor.ID)
	if err != nil || message.Summary.ID != response.TS {
		return application.Message{}, fmt.Errorf("%w: Slack returned a malformed send result", application.ErrWriteOutcomeUnknown)
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
	if err := client.requireMessageVersion(ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version); err != nil {
		return application.Message{}, err
	}
	text, markdown, err := slackWriteText(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	request := struct {
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		Text     string `json:"text"`
		Markdown bool   `json:"mrkdwn"`
	}{Channel: input.ConversationID, TS: input.MessageID, Text: text, Markdown: markdown}
	var response struct {
		slackEnvelope
		Channel string       `json:"channel"`
		TS      string       `json:"ts"`
		Message slackMessage `json:"message"`
	}
	result, err := client.api.DoJSON(
		ctx, http.MethodPost, "chat.update", nil, request, &response, true,
		nil, http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		return application.Message{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, true); err != nil {
		return application.Message{}, err
	}
	if response.Channel != input.ConversationID || response.TS != input.MessageID {
		return application.Message{}, fmt.Errorf("%w: Slack returned a mismatched edit result", application.ErrWriteOutcomeUnknown)
	}
	if response.Message.TS == "" {
		response.Message.TS = response.TS
	}
	message, err := mapSlackMessage(response.Message, input.ConversationID, client.actor.ID)
	if err != nil || message.Summary.ID != input.MessageID {
		return application.Message{}, fmt.Errorf("%w: Slack returned a malformed edit result", application.ErrWriteOutcomeUnknown)
	}
	return message, nil
}

func (client *Client) DeleteMessage(ctx context.Context, input application.MessageDeleteInput) error {
	if err := client.requireCapability(client.capabilities.Delete, "message delete"); err != nil {
		return err
	}
	if err := client.requireMessageVersion(ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version); err != nil {
		return err
	}
	var response slackEnvelope
	result, err := client.api.DoJSON(ctx, http.MethodPost, "chat.delete", nil, struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}{Channel: input.ConversationID, TS: input.MessageID}, &response, true, nil,
		http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return err
	}
	return validateSlackResponse(result, response, true)
}

func (client *Client) SetMessageReaction(
	ctx context.Context,
	input application.MessageReactionInput,
) (application.MessageReaction, error) {
	if err := client.requireCapability(client.capabilities.Reactions, "message reactions"); err != nil {
		return application.MessageReaction{}, err
	}
	if err := client.requireMessageVersion(ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version); err != nil {
		return application.MessageReaction{}, err
	}
	method := "reactions.add"
	if input.Remove {
		method = "reactions.remove"
	}
	var response slackEnvelope
	result, err := client.api.DoJSON(ctx, http.MethodPost, method, nil, struct {
		Channel   string `json:"channel"`
		Timestamp string `json:"timestamp"`
		Name      string `json:"name"`
	}{Channel: input.ConversationID, Timestamp: input.MessageID, Name: input.Reaction},
		&response, true, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.MessageReaction{}, err
	}
	if err := validateSlackResponse(result, response, true); err != nil {
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
	if input.Topic != "" {
		return application.Conversation{}, errors.New("slack conversation creation cannot atomically set a topic")
	}
	var response struct {
		slackEnvelope
		Channel slackConversation `json:"channel"`
	}
	var resource string
	var request any
	switch input.Kind {
	case application.ConversationChannel:
		if input.ContainerID != "" || input.Visibility == application.ConversationVisibilityShared {
			return application.Conversation{}, errors.New("slack channels do not accept a parent container or shared visibility")
		}
		if input.Name == "" {
			return application.Conversation{}, errors.New("slack channel creation requires a name")
		}
		for _, member := range input.Members {
			if member.ID != client.actor.ID || member.Role != application.ConversationMember {
				return application.Conversation{}, errors.New("slack channel creation cannot atomically add another member")
			}
		}
		resource = "conversations.create"
		request = struct {
			Name      string `json:"name"`
			IsPrivate bool   `json:"is_private"`
		}{Name: input.Name, IsPrivate: input.Visibility == application.ConversationVisibilityPrivate}
	case application.ConversationDirect, application.ConversationGroup:
		if input.Visibility != application.ConversationVisibilityPrivate {
			return application.Conversation{}, errors.New("slack direct/group conversations must be private")
		}
		members := make([]string, 0, len(input.Members))
		for _, member := range input.Members {
			if member.Role != application.ConversationMember {
				return application.Conversation{}, errors.New("slack conversation open does not assign owners")
			}
			if member.ID != client.actor.ID {
				members = append(members, member.ID)
			}
		}
		if len(members) == 0 || input.Kind == application.ConversationDirect && len(members) != 1 {
			return application.Conversation{}, errors.New("slack direct/group member selection is malformed")
		}
		resource = "conversations.open"
		request = struct {
			Users string `json:"users"`
		}{Users: strings.Join(members, ",")}
	case application.ConversationMeeting:
		return application.Conversation{}, errors.New("slack has no meeting-chat creation route")
	default:
		return application.Conversation{}, errors.New("slack conversation kind is unsupported")
	}
	result, err := client.api.DoJSON(ctx, http.MethodPost, resource, nil, request, &response,
		true, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.Conversation{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, true); err != nil {
		return application.Conversation{}, err
	}
	conversation, err := mapSlackConversation(response.Channel)
	if err != nil {
		return application.Conversation{}, fmt.Errorf("%w: malformed Slack conversation result", application.ErrWriteOutcomeUnknown)
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
	if input.Member.Role != application.ConversationMember {
		return application.ConversationMembershipResult{}, errors.New("slack membership changes cannot assign an owner")
	}
	before, err := client.getSlackConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if slackConversationVersion(before) != input.Version {
		return application.ConversationMembershipResult{}, restapi.ErrPrecondition
	}
	method := "conversations.invite"
	request := struct {
		Channel string `json:"channel"`
		Users   string `json:"users,omitempty"`
		User    string `json:"user,omitempty"`
	}{Channel: input.ConversationID}
	if input.Action == application.MembershipAdd {
		request.Users = input.Member.ID
	} else {
		method = "conversations.kick"
		request.User = input.Member.ID
	}
	var response slackEnvelope
	result, err := client.api.DoJSON(ctx, http.MethodPost, method, nil, request, &response,
		true, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if err := validateSlackResponse(result, response, true); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	after, err := client.getSlackConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, fmt.Errorf(
			"%w: confirm Slack membership result", application.ErrWriteOutcomeUnknown,
		)
	}
	return application.ConversationMembershipResult{
		ConversationID: input.ConversationID, Version: slackConversationVersion(after),
		Action: input.Action, Member: input.Member,
	}, nil
}

func (client *Client) requireMessageVersion(ctx context.Context, conversationID, threadRootID, messageID, version string) error {
	source, err := client.getSlackMessage(ctx, conversationID, threadRootID, messageID)
	if err != nil {
		return err
	}
	summary, err := mapSlackSummary(source, conversationID)
	if err != nil {
		return err
	}
	if summary.Version != version {
		return restapi.ErrPrecondition
	}
	if summary.Author.ID != client.actor.ID {
		return errors.New("slack permits this token to change only its own message")
	}
	return nil
}

func (client *Client) getSlackConversation(ctx context.Context, id string) (slackConversation, error) {
	if !validSlackID(id) {
		return slackConversation{}, errors.New("slack conversation ID is malformed")
	}
	var response struct {
		slackEnvelope
		Channel slackConversation `json:"channel"`
	}
	result, err := client.api.DoJSON(ctx, http.MethodGet, "conversations.info", map[string][]string{
		"channel": {id}, "include_num_members": {"true"},
	}, nil, &response, false, nil, http.StatusOK, http.StatusTooManyRequests)
	if err != nil {
		return slackConversation{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return slackConversation{}, err
	}
	if response.Channel.ID != id {
		return slackConversation{}, errors.New("slack returned a different conversation")
	}
	return response.Channel, nil
}

func slackWriteText(content application.MessageContent, mentions []application.MessageMention) (string, bool, error) {
	if content.Format == application.MessageFormatHTML {
		return "", false, errors.New("slack Web API does not accept canonical HTML message content")
	}
	var text strings.Builder
	text.Grow(len(content.Text) + len(mentions)*16)
	text.WriteString(content.Text)
	for _, mention := range mentions {
		if text.Len() != 0 {
			text.WriteByte(' ')
		}
		switch mention.Kind {
		case application.MessageMentionUser:
			text.WriteString("<@")
		case application.MessageMentionChannel:
			text.WriteString("<#")
		default:
			return "", false, errors.New("slack mention kind is unsupported")
		}
		text.WriteString(mention.ID)
		text.WriteByte('>')
	}
	if text.Len() > application.MaxMessageTextBytes {
		return "", false, errors.New("slack rendered message exceeds the configured limit")
	}
	return text.String(), content.Format == application.MessageFormatMarkdown, nil
}
