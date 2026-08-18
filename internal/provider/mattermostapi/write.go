package mattermostapi

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
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Message{}, err
	}
	if !validMattermostID(input.ConversationID) ||
		input.ReplyToID != "" && !validMattermostID(input.ReplyToID) {
		return application.Message{}, errors.New("mattermost send target is malformed")
	}
	if len(input.Attachments) != 0 {
		return application.Message{}, errors.New("mattermost attachment writes are not enabled")
	}
	text, err := mattermostWriteText(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	if _, err := client.getMattermostConversation(ctx, input.ConversationID); err != nil {
		return application.Message{}, err
	}
	rootID := ""
	if input.ReplyToID != "" {
		target, err := client.getMattermostPost(ctx, input.ConversationID, input.ReplyToID)
		if err != nil {
			return application.Message{}, err
		}
		rootID = target.RootID
		if rootID == "" {
			rootID = target.ID
		}
	}
	var response mattermostPost
	if err := client.doJSON(ctx, http.MethodPost, "posts", nil, struct {
		ChannelID string `json:"channel_id"`
		Message   string `json:"message"`
		RootID    string `json:"root_id,omitempty"`
	}{ChannelID: input.ConversationID, Message: text, RootID: rootID}, &response, true, http.StatusCreated); err != nil {
		return application.Message{}, err
	}
	if response.ChannelID != input.ConversationID || response.UserID != client.actor.ID ||
		response.RootID != rootID || response.Message != text {
		return application.Message{}, fmt.Errorf("%w: Mattermost returned a mismatched send result", application.ErrWriteOutcomeUnknown)
	}
	message, err := mapMattermostMessage(response, input.ConversationID, client.actor.ID, nil, nil)
	if err != nil {
		return application.Message{}, fmt.Errorf("%w: Mattermost returned a malformed send result", application.ErrWriteOutcomeUnknown)
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
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Message{}, err
	}
	if err := client.requireMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	); err != nil {
		return application.Message{}, err
	}
	text, err := mattermostWriteText(input.Content, input.Mentions)
	if err != nil {
		return application.Message{}, err
	}
	var response mattermostPost
	if err := client.doJSON(
		ctx, http.MethodPut, "posts/"+input.MessageID+"/patch", nil,
		struct {
			Message string `json:"message"`
		}{Message: text}, &response, true, http.StatusOK,
	); err != nil {
		return application.Message{}, err
	}
	if response.ID != input.MessageID || response.ChannelID != input.ConversationID ||
		response.UserID != client.actor.ID || response.RootID != input.ThreadRootID ||
		response.Message != text {
		return application.Message{}, fmt.Errorf("%w: Mattermost returned a mismatched edit result", application.ErrWriteOutcomeUnknown)
	}
	message, err := mapMattermostMessage(response, input.ConversationID, client.actor.ID, nil, nil)
	if err != nil {
		return application.Message{}, fmt.Errorf("%w: Mattermost returned a malformed edit result", application.ErrWriteOutcomeUnknown)
	}
	return message, nil
}

func (client *Client) DeleteMessage(ctx context.Context, input application.MessageDeleteInput) error {
	if err := client.requireCapability(client.capabilities.Delete, "message delete"); err != nil {
		return err
	}
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return err
	}
	if err := client.requireMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	); err != nil {
		return err
	}
	var response struct {
		Status string `json:"status"`
	}
	if err := client.doJSON(
		ctx, http.MethodDelete, "posts/"+input.MessageID, nil, nil, &response,
		true, http.StatusOK,
	); err != nil {
		return err
	}
	if response.Status != "OK" {
		return fmt.Errorf("%w: Mattermost did not confirm the selected deletion", application.ErrWriteOutcomeUnknown)
	}
	return nil
}

func (client *Client) SetMessageReaction(
	ctx context.Context,
	input application.MessageReactionInput,
) (application.MessageReaction, error) {
	if err := client.requireCapability(client.capabilities.Reactions, "message reactions"); err != nil {
		return application.MessageReaction{}, err
	}
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return application.MessageReaction{}, err
	}
	if !validMattermostEmoji(input.Reaction) {
		return application.MessageReaction{}, errors.New("mattermost reaction name is malformed")
	}
	if err := client.requireMessageVersion(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID, input.Version,
	); err != nil {
		return application.MessageReaction{}, err
	}
	if input.Remove {
		var response struct {
			Status string `json:"status"`
		}
		resource := "users/" + client.actor.ID + "/posts/" + input.MessageID +
			"/reactions/" + input.Reaction
		if err := client.doJSON(ctx, http.MethodDelete, resource, nil, nil, &response, true, http.StatusOK); err != nil {
			return application.MessageReaction{}, err
		}
		if response.Status != "OK" {
			return application.MessageReaction{}, fmt.Errorf("%w: Mattermost did not confirm reaction removal", application.ErrWriteOutcomeUnknown)
		}
		return application.MessageReaction{Name: input.Reaction, CountKnown: false}, nil
	}
	var response mattermostReaction
	if err := client.doJSON(ctx, http.MethodPost, "reactions", nil, mattermostReaction{
		UserID: client.actor.ID, PostID: input.MessageID, EmojiName: input.Reaction,
	}, &response, true, http.StatusCreated); err != nil {
		return application.MessageReaction{}, err
	}
	if response.UserID != client.actor.ID || response.PostID != input.MessageID ||
		response.EmojiName != input.Reaction {
		return application.MessageReaction{}, fmt.Errorf("%w: Mattermost returned a mismatched reaction result", application.ErrWriteOutcomeUnknown)
	}
	return application.MessageReaction{
		Name: input.Reaction, CountKnown: false, ReactedByActor: true,
	}, nil
}

func (client *Client) CreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
) (application.Conversation, error) {
	if err := client.requireCapability(client.capabilities.CreateConversation, "conversation creation"); err != nil {
		return application.Conversation{}, err
	}
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return application.Conversation{}, err
	}
	var response mattermostChannel
	var resource string
	var request any
	switch input.Kind {
	case application.ConversationChannel:
		if input.ContainerID != "" && input.ContainerID != client.workspaceID {
			return application.Conversation{}, errors.New("mattermost channel selected a different parent team")
		}
		if !validMattermostChannelName(input.Name) {
			return application.Conversation{}, errors.New("mattermost channel name must be a 2-64 character lowercase handle")
		}
		channelType := "O"
		if input.Visibility == application.ConversationVisibilityPrivate {
			channelType = "P"
		} else if input.Visibility != application.ConversationVisibilityPublic {
			return application.Conversation{}, errors.New("mattermost channel visibility must be public or private")
		}
		for _, member := range input.Members {
			if member.ID != client.actor.ID || member.Role != application.ConversationMember {
				return application.Conversation{}, errors.New("mattermost channel creation cannot atomically add another member")
			}
		}
		resource = "channels"
		request = struct {
			TeamID      string `json:"team_id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
			Header      string `json:"header,omitempty"`
		}{TeamID: client.workspaceID, Name: input.Name, DisplayName: input.Name, Type: channelType, Header: input.Topic}
	case application.ConversationDirect, application.ConversationGroup:
		if input.Visibility != application.ConversationVisibilityPrivate || input.ContainerID != "" ||
			input.Name != "" || input.Topic != "" {
			return application.Conversation{}, errors.New("mattermost direct/group creation accepts only private member selection")
		}
		members := make([]string, 0, len(input.Members))
		actorPresent := false
		for _, member := range input.Members {
			if !validMattermostID(member.ID) || member.Role != application.ConversationMember {
				return application.Conversation{}, errors.New("mattermost direct/group member selection is malformed")
			}
			members = append(members, member.ID)
			actorPresent = actorPresent || member.ID == client.actor.ID
		}
		if !actorPresent || input.Kind == application.ConversationDirect && len(members) != 2 ||
			input.Kind == application.ConversationGroup && (len(members) < 3 || len(members) > 8) {
			return application.Conversation{}, errors.New("mattermost direct/group member count is malformed")
		}
		resource = "channels/direct"
		if input.Kind == application.ConversationGroup {
			resource = "channels/group"
		}
		request = members
	case application.ConversationMeeting:
		return application.Conversation{}, errors.New("mattermost meeting lifecycle creation is outside messaging scope")
	default:
		return application.Conversation{}, errors.New("mattermost conversation kind is unsupported")
	}
	if err := client.doJSON(ctx, http.MethodPost, resource, nil, request, &response, true, http.StatusCreated); err != nil {
		return application.Conversation{}, err
	}
	conversation, err := mapMattermostConversation(response, client.workspaceID)
	if err != nil {
		return application.Conversation{}, fmt.Errorf("%w: Mattermost returned a malformed conversation result", application.ErrWriteOutcomeUnknown)
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
	if err := client.validateWriteRoute(input.MessageWriteRoute); err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if !validMattermostID(input.Member.ID) || input.Member.Role != application.ConversationMember {
		return application.ConversationMembershipResult{}, errors.New("mattermost membership changes support only ordinary members")
	}
	if input.Action != application.MembershipAdd && input.Action != application.MembershipRemove {
		return application.ConversationMembershipResult{}, errors.New("mattermost membership action is unsupported")
	}
	before, err := client.getMattermostConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, err
	}
	if before.Type != "O" && before.Type != "P" {
		return application.ConversationMembershipResult{}, errors.New("mattermost direct/group membership is immutable")
	}
	if mattermostConversationVersion(before) != input.Version {
		return application.ConversationMembershipResult{}, restapi.ErrPrecondition
	}
	if input.Action == application.MembershipAdd {
		var response struct {
			ChannelID string `json:"channel_id"`
			UserID    string `json:"user_id"`
		}
		if err := client.doJSON(
			ctx, http.MethodPost, "channels/"+input.ConversationID+"/members", nil,
			struct {
				UserID string `json:"user_id"`
			}{UserID: input.Member.ID}, &response, true, http.StatusCreated,
		); err != nil {
			return application.ConversationMembershipResult{}, err
		}
		if response.ChannelID != input.ConversationID || response.UserID != input.Member.ID {
			return application.ConversationMembershipResult{}, fmt.Errorf("%w: Mattermost returned a mismatched membership result", application.ErrWriteOutcomeUnknown)
		}
	} else {
		var response struct {
			Status string `json:"status"`
		}
		resource := "channels/" + input.ConversationID + "/members/" + input.Member.ID
		if err := client.doJSON(ctx, http.MethodDelete, resource, nil, nil, &response, true, http.StatusOK); err != nil {
			return application.ConversationMembershipResult{}, err
		}
		if response.Status != "OK" {
			return application.ConversationMembershipResult{}, fmt.Errorf("%w: Mattermost did not confirm membership removal", application.ErrWriteOutcomeUnknown)
		}
	}
	after, err := client.getMattermostConversation(ctx, input.ConversationID)
	if err != nil {
		return application.ConversationMembershipResult{}, fmt.Errorf("%w: confirm Mattermost membership result", application.ErrWriteOutcomeUnknown)
	}
	return application.ConversationMembershipResult{
		ConversationID: input.ConversationID, Version: mattermostConversationVersion(after),
		Action: input.Action, Member: input.Member,
	}, nil
}

func (client *Client) requireMessageVersion(
	ctx context.Context,
	conversationID, threadRootID, messageID, version string,
) error {
	post, err := client.getMattermostPost(ctx, conversationID, messageID)
	if err != nil {
		return err
	}
	if post.RootID != threadRootID || mattermostMessageVersion(post) != version {
		return restapi.ErrPrecondition
	}
	if post.UserID != client.actor.ID {
		return errors.New("mattermost permits this route to change only its own message")
	}
	return nil
}

func mattermostWriteText(
	content application.MessageContent,
	mentions []application.MessageMention,
) (string, error) {
	if len(mentions) != 0 {
		return "", errors.New("mattermost ID-bound mention writes are not enabled")
	}
	switch content.Format {
	case application.MessageFormatMarkdown:
		if len(content.Text) == 0 || len(content.Text) > application.MaxMessageTextBytes {
			return "", errors.New("mattermost message text is empty or exceeds the configured limit")
		}
		return content.Text, nil
	case application.MessageFormatPlain:
		if len(content.Text) == 0 {
			return "", errors.New("mattermost message text is empty")
		}
		var result strings.Builder
		result.Grow(len(content.Text))
		for _, character := range content.Text {
			if strings.ContainsRune("\\`*_{}[]<>()#+-.!|~", character) {
				result.WriteByte('\\')
			}
			result.WriteRune(character)
		}
		if result.Len() > application.MaxMessageTextBytes {
			return "", errors.New("mattermost rendered message exceeds the configured limit")
		}
		return result.String(), nil
	case application.MessageFormatHTML:
		return "", errors.New("mattermost REST does not accept canonical HTML message content")
	default:
		return "", errors.New("mattermost message format is unsupported")
	}
}

func validMattermostChannelName(value string) bool {
	if len(value) < 2 || len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
