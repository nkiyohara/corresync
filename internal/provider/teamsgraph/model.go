package teamsgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/nkiyohara/corresync/internal/application"
)

type graphChat struct {
	ID                  string `json:"id"`
	Topic               string `json:"topic,omitempty"`
	ChatType            string `json:"chatType"`
	CreatedDateTime     string `json:"createdDateTime,omitempty"`
	LastUpdatedDateTime string `json:"lastUpdatedDateTime,omitempty"`
}

type graphTeam struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
}

type graphChannel struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName,omitempty"`
	Description     string `json:"description,omitempty"`
	MembershipType  string `json:"membershipType,omitempty"`
	CreatedDateTime string `json:"createdDateTime,omitempty"`
}

type graphIdentity struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	TenantID    string `json:"tenantId,omitempty"`
}

type graphIdentitySet struct {
	User         *graphIdentity             `json:"user,omitempty"`
	Application  *graphIdentity             `json:"application,omitempty"`
	Conversation *graphConversationIdentity `json:"conversation,omitempty"`
}

type graphConversationIdentity struct {
	ID                       string `json:"id,omitempty"`
	DisplayName              string `json:"displayName,omitempty"`
	ConversationIdentityType string `json:"conversationIdentityType,omitempty"`
}

type graphMessage struct {
	ID                   string            `json:"id"`
	ETag                 string            `json:"etag,omitempty"`
	ReplyToID            string            `json:"replyToId,omitempty"`
	MessageType          string            `json:"messageType,omitempty"`
	CreatedDateTime      string            `json:"createdDateTime"`
	LastModifiedDateTime string            `json:"lastModifiedDateTime,omitempty"`
	LastEditedDateTime   string            `json:"lastEditedDateTime,omitempty"`
	DeletedDateTime      string            `json:"deletedDateTime,omitempty"`
	Summary              string            `json:"summary,omitempty"`
	ChatID               string            `json:"chatId,omitempty"`
	WebURL               string            `json:"webUrl,omitempty"`
	From                 *graphIdentitySet `json:"from,omitempty"`
	Body                 struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	ChannelIdentity struct {
		TeamID    string `json:"teamId,omitempty"`
		ChannelID string `json:"channelId,omitempty"`
	} `json:"channelIdentity,omitempty"`
	Mentions []struct {
		ID          int              `json:"id"`
		MentionText string           `json:"mentionText,omitempty"`
		Mentioned   graphIdentitySet `json:"mentioned"`
	} `json:"mentions,omitempty"`
	Reactions []struct {
		ReactionType    string           `json:"reactionType"`
		CreatedDateTime string           `json:"createdDateTime,omitempty"`
		User            graphIdentitySet `json:"user"`
	} `json:"reactions,omitempty"`
	Attachments []struct {
		ID          string `json:"id"`
		ContentType string `json:"contentType,omitempty"`
		ContentURL  string `json:"contentUrl,omitempty"`
		Name        string `json:"name,omitempty"`
	} `json:"attachments,omitempty"`
}

func mapGraphChat(source graphChat) (application.Conversation, error) {
	id, err := encodeGraphChatID(source.ID)
	if err != nil {
		return application.Conversation{}, err
	}
	var kind application.ConversationKind
	switch source.ChatType {
	case "oneOnOne":
		kind = application.ConversationDirect
	case "group":
		kind = application.ConversationGroup
	case "meeting":
		kind = application.ConversationMeeting
	default:
		return application.Conversation{}, errors.New("the Microsoft Graph response contains an unknown Teams chat type")
	}
	activity, err := optionalGraphTime(source.LastUpdatedDateTime)
	if err != nil {
		return application.Conversation{}, err
	}
	return application.Conversation{
		ID: id, Version: graphConversationVersion(source.ID, source.Topic, source.ChatType, source.LastUpdatedDateTime),
		Kind: kind, Visibility: application.ConversationVisibilityPrivate,
		Name: boundedGraphText(source.Topic, 4096), LastActivityAt: activity,
		MemberCountKnown: false,
	}, nil
}

func mapGraphChannel(team graphTeam, source graphChannel) (application.Conversation, error) {
	id, err := encodeGraphChannelID(team.ID, source.ID)
	if err != nil {
		return application.Conversation{}, err
	}
	containerID, err := encodeGraphTeamID(team.ID)
	if err != nil {
		return application.Conversation{}, err
	}
	var visibility application.ConversationVisibility
	switch source.MembershipType {
	case "standard":
		visibility = application.ConversationVisibilityPublic
	case "private":
		visibility = application.ConversationVisibilityPrivate
	case "shared":
		visibility = application.ConversationVisibilityShared
	default:
		visibility = application.ConversationVisibilityUnknown
	}
	name := source.DisplayName
	if team.DisplayName != "" && name != "" {
		name = team.DisplayName + " / " + name
	}
	return application.Conversation{
		ID: id, ContainerID: containerID,
		Version: graphConversationVersion(source.ID, source.DisplayName, source.Description, source.MembershipType),
		Kind:    application.ConversationChannel, Visibility: visibility,
		Name: boundedGraphText(name, 4096), Topic: boundedGraphText(source.Description, 8192),
		MemberCountKnown: false,
	}, nil
}

func graphConversationVersion(values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return "tgcv1_" + hex.EncodeToString(digest[:])
}

func mapGraphSummary(source graphMessage, conversationID string) (application.MessageSummary, error) {
	if !validGraphOpaque(source.ID) {
		return application.MessageSummary{}, errors.New("the Microsoft Graph response contains a malformed Teams message ID")
	}
	created, err := graphTime(source.CreatedDateTime)
	if err != nil {
		return application.MessageSummary{}, err
	}
	updated, err := optionalGraphTime(source.LastModifiedDateTime)
	if err != nil {
		return application.MessageSummary{}, err
	}
	author := application.MessageActor{Mode: application.MessageActorUnavailable}
	if source.From != nil {
		switch {
		case source.From.User != nil && validGraphOpaque(source.From.User.ID):
			author = application.MessageActor{
				ID: source.From.User.ID, Mode: application.MessageActorDelegatedUser,
				DisplayName: boundedGraphText(source.From.User.DisplayName, 1024),
			}
		case source.From.Application != nil && validGraphOpaque(source.From.Application.ID):
			author = application.MessageActor{
				ID: source.From.Application.ID, Mode: application.MessageActorApp,
				DisplayName: boundedGraphText(source.From.Application.DisplayName, 1024),
			}
		}
	}
	deleted := source.DeletedDateTime != ""
	if author.Mode == application.MessageActorUnavailable && !deleted && source.MessageType == "message" {
		return application.MessageSummary{}, errors.New("the Microsoft Graph response contains a Teams message without an actor")
	}
	threadRoot := source.ReplyToID
	if threadRoot == source.ID {
		threadRoot = ""
	}
	snippet := source.Summary
	return application.MessageSummary{
		ID: source.ID, Version: graphMessageVersion(source), ConversationID: conversationID,
		ThreadRootID: threadRoot, Author: author,
		CreatedAt: created, UpdatedAt: updated,
		Snippet:        boundedGraphText(snippet, application.MaxMessageSnippetBytes),
		HasAttachments: len(source.Attachments) != 0, Deleted: deleted,
	}, nil
}

func mapGraphMessage(source graphMessage, conversationID, actorID string) (application.Message, error) {
	summary, err := mapGraphSummary(source, conversationID)
	if err != nil {
		return application.Message{}, err
	}
	if len(source.Mentions) > maximumGraphItems || len(source.Reactions) > maximumGraphItems ||
		len(source.Attachments) > maximumGraphItems {
		return application.Message{}, errors.New("the Microsoft Graph Teams message collections exceed the configured limit")
	}
	format := application.MessageFormatPlain
	if strings.EqualFold(source.Body.ContentType, "html") {
		format = application.MessageFormatHTML
	} else if source.Body.ContentType != "" && !strings.EqualFold(source.Body.ContentType, "text") {
		return application.Message{}, errors.New("the Microsoft Graph response contains an unknown Teams message body format")
	}
	links := graphHTMLLinks(source.Body.Content)
	if source.WebURL != "" {
		if link := graphLink(source.WebURL, "Open in Microsoft Teams"); link != nil {
			links = appendUniqueGraphLink(links, *link)
		}
	}
	mentions := make([]application.MessageMention, 0, len(source.Mentions))
	for _, mention := range source.Mentions {
		switch {
		case mention.Mentioned.User != nil && validGraphOpaque(mention.Mentioned.User.ID):
			mentions = append(mentions, application.MessageMention{
				ID: mention.Mentioned.User.ID, Kind: application.MessageMentionUser,
				DisplayName: boundedGraphText(mention.MentionText, 1024),
			})
		case mention.Mentioned.Conversation != nil && validGraphOpaque(mention.Mentioned.Conversation.ID):
			mentions = append(mentions, application.MessageMention{
				ID: mention.Mentioned.Conversation.ID, Kind: application.MessageMentionChannel,
				DisplayName: boundedGraphText(mention.MentionText, 1024),
			})
		default:
			return application.Message{}, errors.New("the Microsoft Graph response contains a malformed Teams mention")
		}
	}
	reactions, err := graphReactions(source, actorID)
	if err != nil {
		return application.Message{}, err
	}
	attachments := make([]application.MessageAttachment, 0, len(source.Attachments))
	for _, attachment := range source.Attachments {
		if !validGraphOpaque(attachment.ID) {
			return application.Message{}, errors.New("the Microsoft Graph response contains malformed Teams attachment metadata")
		}
		name := attachment.Name
		if name == "" {
			name = "Teams attachment " + attachment.ID
		}
		attachments = append(attachments, application.MessageAttachment{
			ID: attachment.ID, Name: boundedGraphText(name, 4096),
			MediaType: boundedGraphText(attachment.ContentType, 256),
			SizeKnown: false, Downloadable: false,
		})
	}
	return application.Message{
		Summary: summary, Content: application.MessageContent{Format: format, Text: source.Body.Content},
		Links: links, Mentions: mentions, Reactions: reactions, Attachments: attachments,
	}, nil
}

func graphReactions(source graphMessage, actorID string) ([]application.MessageReaction, error) {
	type aggregate struct {
		count   int
		reacted bool
	}
	byName := make(map[string]aggregate)
	order := make([]string, 0, len(source.Reactions))
	for _, reaction := range source.Reactions {
		name := reaction.ReactionType
		if name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
			return nil, errors.New("the Microsoft Graph response contains a malformed Teams reaction")
		}
		item, exists := byName[name]
		if !exists {
			order = append(order, name)
		}
		item.count++
		if reaction.User.User != nil && reaction.User.User.ID == actorID {
			item.reacted = true
		}
		byName[name] = item
	}
	result := make([]application.MessageReaction, 0, len(order))
	for _, name := range order {
		item := byName[name]
		result = append(result, application.MessageReaction{
			Name: name, Count: item.count, CountKnown: true, ReactedByActor: item.reacted,
		})
	}
	return result, nil
}

func graphMessageVersion(source graphMessage) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		source.ID, source.ETag, source.LastModifiedDateTime, source.LastEditedDateTime, source.DeletedDateTime,
	}, "\x00")))
	return "tgmv1_" + hex.EncodeToString(digest[:])
}

func graphTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("the Microsoft Graph response contains a malformed Teams timestamp")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func optionalGraphTime(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return graphTime(value)
}

func graphHTMLLinks(value string) []application.MessageLink {
	result := make([]application.MessageLink, 0)
	tokenizer := html.NewTokenizer(strings.NewReader(value))
	for len(result) < maximumGraphItems {
		token := tokenizer.Next()
		if token == html.ErrorToken {
			break
		}
		if token != html.StartTagToken && token != html.SelfClosingTagToken {
			continue
		}
		name, hasAttributes := tokenizer.TagName()
		if string(name) != "a" || !hasAttributes {
			continue
		}
		for {
			key, raw, more := tokenizer.TagAttr()
			if string(key) == "href" {
				if link := graphLink(string(raw), ""); link != nil {
					result = appendUniqueGraphLink(result, *link)
				}
			}
			if !more {
				break
			}
		}
	}
	return result
}

func graphLink(raw, label string) *application.MessageLink {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "mailto") {
		return nil
	}
	return &application.MessageLink{URL: raw, Label: boundedGraphText(label, 2048)}
}

func appendUniqueGraphLink(values []application.MessageLink, candidate application.MessageLink) []application.MessageLink {
	for _, value := range values {
		if value.URL == candidate.URL {
			return values
		}
	}
	return append(values, candidate)
}

func validateGraphMessageRoute(source graphMessage, locator graphConversationLocator) error {
	if locator.isChat() {
		if source.ChatID != locator.ChatID {
			return errors.New("the Microsoft Graph response contains a message from another chat")
		}
		return nil
	}
	if source.ChannelIdentity.TeamID != locator.TeamID ||
		source.ChannelIdentity.ChannelID != locator.ChannelID {
		return errors.New("the Microsoft Graph response contains a message from another channel")
	}
	return nil
}
