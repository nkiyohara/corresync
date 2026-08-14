package slackapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

const maximumSlackItems = application.MaxMessageCollectionItems

type slackConversation struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	IsChannel  bool   `json:"is_channel,omitempty"`
	IsGroup    bool   `json:"is_group,omitempty"`
	IsIM       bool   `json:"is_im,omitempty"`
	IsMPIM     bool   `json:"is_mpim,omitempty"`
	IsPrivate  bool   `json:"is_private,omitempty"`
	IsArchived bool   `json:"is_archived,omitempty"`
	NumMembers int    `json:"num_members,omitempty"`
	Updated    int64  `json:"updated,omitempty"`
	Topic      struct {
		Value string `json:"value,omitempty"`
	} `json:"topic,omitempty"`
}

type slackMessage struct {
	Type       string `json:"type,omitempty"`
	Subtype    string `json:"subtype,omitempty"`
	Text       string `json:"text,omitempty"`
	User       string `json:"user,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	Username   string `json:"username,omitempty"`
	TS         string `json:"ts"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	ReplyCount int    `json:"reply_count,omitempty"`
	Edited     *struct {
		TS string `json:"ts"`
	} `json:"edited,omitempty"`
	Files     []slackFile     `json:"files,omitempty"`
	Reactions []slackReaction `json:"reactions,omitempty"`
}

type slackFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name,omitempty"`
	Title              string `json:"title,omitempty"`
	MediaType          string `json:"mimetype,omitempty"`
	Size               int64  `json:"size,omitempty"`
	Mode               string `json:"mode,omitempty"`
	FileAccess         string `json:"file_access,omitempty"`
	URLPrivate         string `json:"url_private,omitempty"`
	URLPrivateDownload string `json:"url_private_download,omitempty"`
}

type slackReaction struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Users []string `json:"users,omitempty"`
}

func mapSlackConversation(source slackConversation) (application.Conversation, error) {
	if !validSlackID(source.ID) || source.IsArchived || source.NumMembers < 0 {
		return application.Conversation{}, errors.New("slack returned a malformed conversation")
	}
	var kind application.ConversationKind
	switch {
	case source.IsIM:
		kind = application.ConversationDirect
	case source.IsMPIM:
		kind = application.ConversationGroup
	case source.IsChannel || source.IsGroup:
		kind = application.ConversationChannel
	default:
		return application.Conversation{}, errors.New("slack returned an unknown conversation kind")
	}
	conversation := application.Conversation{
		ID: source.ID, Version: slackConversationVersion(source), Kind: kind,
		Name: boundedSlackText(source.Name, 4096), Topic: boundedSlackText(source.Topic.Value, 8192),
		MemberCount: source.NumMembers, MemberCountKnown: true,
	}
	if source.IsPrivate || source.IsIM || source.IsMPIM || source.IsGroup {
		conversation.Visibility = application.ConversationVisibilityPrivate
	} else {
		conversation.Visibility = application.ConversationVisibilityPublic
	}
	return conversation, nil
}

func slackConversationVersion(source slackConversation) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%d\x00%d\x00%t",
		source.ID, source.Name, source.Topic.Value, source.NumMembers, source.Updated, source.IsArchived,
	)))
	return "slcv1_" + hex.EncodeToString(digest[:])
}

func mapSlackSummary(source slackMessage, conversationID string) (application.MessageSummary, error) {
	if !validSlackID(conversationID) || !validSlackTimestamp(source.TS) {
		return application.MessageSummary{}, errors.New("slack returned a malformed message identity")
	}
	created, err := slackTime(source.TS)
	if err != nil {
		return application.MessageSummary{}, err
	}
	authorID := source.User
	mode := application.MessageActorDelegatedUser
	if source.BotID != "" {
		mode = application.MessageActorApp
		if authorID == "" {
			authorID = source.BotID
		}
	}
	if source.Subtype == "message_deleted" {
		if authorID == "" {
			authorID = "deleted"
		}
	} else if !validSlackID(authorID) {
		return application.MessageSummary{}, errors.New("slack returned a message without a bounded author")
	}
	updated := ""
	version := source.TS
	if source.Edited != nil {
		parsed, err := slackTime(source.Edited.TS)
		if err != nil {
			return application.MessageSummary{}, err
		}
		updated = parsed.Format(time.RFC3339Nano)
		version = source.Edited.TS
	}
	threadRootID := source.ThreadTS
	if threadRootID == source.TS {
		threadRootID = ""
	}
	return application.MessageSummary{
		ID: source.TS, Version: "slmv1_" + version, ConversationID: conversationID,
		ThreadRootID: threadRootID,
		Author: application.MessageActor{
			ID: authorID, Mode: mode, DisplayName: boundedSlackText(source.Username, 1024),
		},
		CreatedAt: created.Format(time.RFC3339Nano), UpdatedAt: updated,
		Snippet:    boundedSlackText(source.Text, application.MaxMessageSnippetBytes),
		ReplyCount: source.ReplyCount, HasAttachments: len(source.Files) != 0,
		Deleted: source.Subtype == "message_deleted",
	}, nil
}

func mapSlackMessage(source slackMessage, conversationID, actorID string) (application.Message, error) {
	summary, err := mapSlackSummary(source, conversationID)
	if err != nil {
		return application.Message{}, err
	}
	links, mentions := parseSlackMarkup(source.Text)
	attachments := make([]application.MessageAttachment, 0, len(source.Files))
	if len(source.Files) > maximumSlackItems || len(source.Reactions) > maximumSlackItems {
		return application.Message{}, errors.New("slack message collections exceed the configured limit")
	}
	for _, file := range source.Files {
		name := file.Name
		if name == "" {
			name = file.Title
		}
		if !validSlackID(file.ID) || name == "" || file.Size < 0 {
			return application.Message{}, errors.New("slack returned malformed file metadata")
		}
		attachments = append(attachments, application.MessageAttachment{
			ID: file.ID, Name: boundedSlackText(name, 4096),
			MediaType: boundedSlackText(file.MediaType, 256), Size: file.Size,
			SizeKnown: true, Downloadable: file.URLPrivateDownload != "" || file.URLPrivate != "",
		})
	}
	reactions := make([]application.MessageReaction, 0, len(source.Reactions))
	for _, reaction := range source.Reactions {
		if !validSlackCode(reaction.Name) || reaction.Count < 0 || len(reaction.Users) > maximumSlackItems {
			return application.Message{}, errors.New("slack returned malformed reaction metadata")
		}
		reacted := false
		for _, user := range reaction.Users {
			if user == actorID {
				reacted = true
			}
		}
		reactions = append(reactions, application.MessageReaction{
			Name: reaction.Name, Count: reaction.Count, CountKnown: true, ReactedByActor: reacted,
		})
	}
	return application.Message{
		Summary: summary,
		Content: application.MessageContent{Format: application.MessageFormatMarkdown, Text: source.Text},
		Links:   links, Mentions: mentions, Reactions: reactions, Attachments: attachments,
	}, nil
}

func slackTime(value string) (time.Time, error) {
	seconds, fraction, found := strings.Cut(value, ".")
	if !found || len(fraction) != 6 || !digits(seconds) || !digits(fraction) {
		return time.Time{}, errors.New("slack returned a malformed message timestamp")
	}
	whole, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil || whole < 0 {
		return time.Time{}, errors.New("slack returned a malformed message timestamp")
	}
	microseconds, _ := strconv.ParseInt(fraction, 10, 64)
	return time.Unix(whole, microseconds*int64(time.Microsecond)).UTC(), nil
}

func validSlackTimestamp(value string) bool {
	_, err := slackTime(value)
	return err == nil
}

func validSlackID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') &&
			character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func boundedSlackText(value string, maximum int) string {
	if maximum < 1 || value == "" {
		return ""
	}
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func parseSlackMarkup(text string) ([]application.MessageLink, []application.MessageMention) {
	links := make([]application.MessageLink, 0)
	mentions := make([]application.MessageMention, 0)
	for offset := 0; offset < len(text) && len(links)+len(mentions) < maximumSlackItems; {
		start := strings.IndexByte(text[offset:], '<')
		if start < 0 {
			break
		}
		start += offset
		end := strings.IndexByte(text[start+1:], '>')
		if end < 0 {
			break
		}
		end += start + 1
		target, label, _ := strings.Cut(text[start+1:end], "|")
		switch {
		case strings.HasPrefix(target, "@") && validSlackID(strings.TrimPrefix(target, "@")):
			mentions = append(mentions, application.MessageMention{
				ID: strings.TrimPrefix(target, "@"), Kind: application.MessageMentionUser,
				DisplayName: boundedSlackText(label, 1024),
			})
		case strings.HasPrefix(target, "#") && validSlackID(strings.TrimPrefix(target, "#")):
			mentions = append(mentions, application.MessageMention{
				ID: strings.TrimPrefix(target, "#"), Kind: application.MessageMentionChannel,
				DisplayName: boundedSlackText(label, 1024),
			})
		default:
			parsed, err := url.Parse(target)
			if err == nil && parsed.IsAbs() && parsed.User == nil &&
				(parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "mailto") {
				links = append(links, application.MessageLink{
					URL: target, Label: boundedSlackText(label, 2048),
				})
			}
		}
		offset = end + 1
	}
	return links, mentions
}
