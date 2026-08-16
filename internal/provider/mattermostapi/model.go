package mattermostapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

const maximumMattermostItems = application.MaxMessageCollectionItems

type mattermostUser struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Roles     string `json:"roles,omitempty"`
	DeleteAt  int64  `json:"delete_at,omitempty"`
}

type mattermostTeam struct {
	ID       string `json:"id"`
	DeleteAt int64  `json:"delete_at,omitempty"`
}

type mattermostTeamMember struct {
	TeamID   string `json:"team_id"`
	UserID   string `json:"user_id"`
	Roles    string `json:"roles"`
	DeleteAt int64  `json:"delete_at,omitempty"`
}

type mattermostChannelMember struct {
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	Roles     string `json:"roles"`
}

type mattermostRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type mattermostChannel struct {
	ID          string `json:"id"`
	CreateAt    int64  `json:"create_at"`
	UpdateAt    int64  `json:"update_at"`
	DeleteAt    int64  `json:"delete_at,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
	Name        string `json:"name,omitempty"`
	Header      string `json:"header,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	LastPostAt  int64  `json:"last_post_at,omitempty"`
}

type mattermostPost struct {
	ID         string   `json:"id"`
	CreateAt   int64    `json:"create_at"`
	UpdateAt   int64    `json:"update_at"`
	EditAt     int64    `json:"edit_at,omitempty"`
	DeleteAt   int64    `json:"delete_at,omitempty"`
	UserID     string   `json:"user_id"`
	ChannelID  string   `json:"channel_id"`
	RootID     string   `json:"root_id,omitempty"`
	Message    string   `json:"message,omitempty"`
	ReplyCount int      `json:"reply_count,omitempty"`
	FileIDs    []string `json:"file_ids,omitempty"`
}

type mattermostPostList struct {
	Order []string                  `json:"order"`
	Posts map[string]mattermostPost `json:"posts"`
}

type mattermostFileInfo struct {
	ID       string `json:"id"`
	PostID   string `json:"post_id"`
	Name     string `json:"name"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
}

type mattermostReaction struct {
	UserID    string `json:"user_id"`
	PostID    string `json:"post_id"`
	EmojiName string `json:"emoji_name"`
	CreateAt  int64  `json:"create_at,omitempty"`
}

func validMattermostID(value string) bool {
	if len(value) != 26 {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func validMattermostEmoji(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_+-", character) {
			continue
		}
		return false
	}
	return true
}

func mapMattermostConversation(source mattermostChannel, workspaceID string) (application.Conversation, error) {
	if !validMattermostID(source.ID) || source.DeleteAt != 0 || source.UpdateAt < 0 || source.LastPostAt < 0 {
		return application.Conversation{}, errors.New("mattermost returned a malformed conversation")
	}
	conversation := application.Conversation{
		ID: source.ID, Version: mattermostConversationVersion(source),
		Name:  boundedMattermostText(firstNonempty(source.DisplayName, source.Name), 4096),
		Topic: boundedMattermostText(firstNonempty(source.Header, source.Purpose), 8192),
	}
	switch source.Type {
	case "O":
		if source.TeamID != workspaceID {
			return application.Conversation{}, errors.New("mattermost channel escaped the selected team")
		}
		conversation.Kind = application.ConversationChannel
		conversation.Visibility = application.ConversationVisibilityPublic
		conversation.ContainerID = source.TeamID
	case "P":
		if source.TeamID != workspaceID {
			return application.Conversation{}, errors.New("mattermost channel escaped the selected team")
		}
		conversation.Kind = application.ConversationChannel
		conversation.Visibility = application.ConversationVisibilityPrivate
		conversation.ContainerID = source.TeamID
	case "D":
		if source.TeamID != "" {
			return application.Conversation{}, errors.New("mattermost direct channel unexpectedly selected a team")
		}
		conversation.Kind = application.ConversationDirect
		conversation.Visibility = application.ConversationVisibilityPrivate
	case "G":
		if source.TeamID != "" {
			return application.Conversation{}, errors.New("mattermost group channel unexpectedly selected a team")
		}
		conversation.Kind = application.ConversationGroup
		conversation.Visibility = application.ConversationVisibilityPrivate
	default:
		return application.Conversation{}, errors.New("mattermost returned an unsupported channel type")
	}
	if source.LastPostAt > 0 {
		conversation.LastActivityAt = mattermostTime(source.LastPostAt).Format(time.RFC3339Nano)
	}
	return conversation, nil
}

func mattermostConversationVersion(source mattermostChannel) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d",
		source.ID, source.TeamID, source.Type, source.Name, source.DisplayName,
		source.Header+"\x00"+source.Purpose, source.UpdateAt, source.DeleteAt,
	)))
	return "mmcv1_" + hex.EncodeToString(digest[:])
}

func mapMattermostSummary(source mattermostPost, conversationID string) (application.MessageSummary, error) {
	if !validMattermostID(source.ID) || !validMattermostID(conversationID) ||
		source.ChannelID != conversationID || !validMattermostID(source.UserID) ||
		source.CreateAt <= 0 || source.UpdateAt < 0 || source.EditAt < 0 || source.DeleteAt < 0 ||
		source.ReplyCount < 0 || len(source.FileIDs) > maximumMattermostItems {
		return application.MessageSummary{}, errors.New("mattermost returned a malformed message")
	}
	if source.RootID != "" && (!validMattermostID(source.RootID) || source.RootID == source.ID) {
		return application.MessageSummary{}, errors.New("mattermost returned a malformed thread identity")
	}
	for _, fileID := range source.FileIDs {
		if !validMattermostID(fileID) {
			return application.MessageSummary{}, errors.New("mattermost returned a malformed file identity")
		}
	}
	updated := ""
	if source.EditAt > 0 {
		updated = mattermostTime(source.EditAt).Format(time.RFC3339Nano)
	}
	return application.MessageSummary{
		ID: source.ID, Version: mattermostMessageVersion(source),
		ConversationID: conversationID, ThreadRootID: source.RootID,
		Author:    application.MessageActor{ID: source.UserID, Mode: application.MessageActorDelegatedUser},
		CreatedAt: mattermostTime(source.CreateAt).Format(time.RFC3339Nano), UpdatedAt: updated,
		Snippet:    boundedMattermostText(source.Message, application.MaxMessageSnippetBytes),
		ReplyCount: source.ReplyCount, HasAttachments: len(source.FileIDs) != 0,
		Deleted: source.DeleteAt != 0,
	}, nil
}

func mattermostMessageVersion(source mattermostPost) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s",
		source.ID, source.ChannelID, source.RootID, source.UserID, source.CreateAt,
		source.UpdateAt, source.EditAt, source.DeleteAt, source.Message,
		strings.Join(source.FileIDs, "\x00"),
	)))
	return "mmmv1_" + hex.EncodeToString(digest[:])
}

func mapMattermostMessage(
	source mattermostPost,
	conversationID, actorID string,
	files []mattermostFileInfo,
	reactions []mattermostReaction,
) (application.Message, error) {
	if len(source.Message) > application.MaxMessageTextBytes {
		return application.Message{}, errors.New("mattermost returned an oversized message body")
	}
	summary, err := mapMattermostSummary(source, conversationID)
	if err != nil {
		return application.Message{}, err
	}
	if len(files) > maximumMattermostItems || len(reactions) > maximumMattermostItems {
		return application.Message{}, errors.New("mattermost message collections exceed the configured limit")
	}
	attachments := make([]application.MessageAttachment, 0, len(files))
	seenFiles := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !validMattermostID(file.ID) || file.PostID != source.ID || file.Name == "" || file.Size < 0 {
			return application.Message{}, errors.New("mattermost returned malformed file metadata")
		}
		if _, exists := seenFiles[file.ID]; exists {
			return application.Message{}, errors.New("mattermost returned duplicate file metadata")
		}
		seenFiles[file.ID] = struct{}{}
		attachments = append(attachments, application.MessageAttachment{
			ID: file.ID, Name: boundedMattermostText(file.Name, 4096),
			MediaType: boundedMattermostText(file.MimeType, 256), Size: file.Size,
			SizeKnown: true, Downloadable: file.Size <= application.MaxMessageAttachmentBytes,
		})
	}
	for _, fileID := range source.FileIDs {
		if _, exists := seenFiles[fileID]; !exists {
			return application.Message{}, errors.New("mattermost omitted selected file metadata")
		}
	}
	if len(seenFiles) != len(source.FileIDs) {
		return application.Message{}, errors.New("mattermost returned unselected file metadata")
	}
	type reactionAggregate struct {
		count   int
		reacted bool
	}
	aggregates := make(map[string]reactionAggregate)
	seenReactions := make(map[string]struct{}, len(reactions))
	for _, reaction := range reactions {
		if reaction.PostID != source.ID || !validMattermostID(reaction.UserID) ||
			!validMattermostEmoji(reaction.EmojiName) {
			return application.Message{}, errors.New("mattermost returned malformed reaction metadata")
		}
		key := reaction.UserID + "\x00" + reaction.EmojiName
		if _, exists := seenReactions[key]; exists {
			return application.Message{}, errors.New("mattermost returned duplicate reaction metadata")
		}
		seenReactions[key] = struct{}{}
		aggregate := aggregates[reaction.EmojiName]
		aggregate.count++
		aggregate.reacted = aggregate.reacted || reaction.UserID == actorID
		aggregates[reaction.EmojiName] = aggregate
	}
	names := make([]string, 0, len(aggregates))
	for name := range aggregates {
		names = append(names, name)
	}
	sort.Strings(names)
	mappedReactions := make([]application.MessageReaction, 0, len(names))
	for _, name := range names {
		aggregate := aggregates[name]
		mappedReactions = append(mappedReactions, application.MessageReaction{
			Name: name, Count: aggregate.count, CountKnown: true,
			ReactedByActor: aggregate.reacted,
		})
	}
	return application.Message{
		Summary: summary,
		Content: application.MessageContent{
			Format: application.MessageFormatMarkdown,
			Text:   boundedMattermostText(source.Message, application.MaxMessageTextBytes),
		},
		Reactions: mappedReactions, Attachments: attachments,
	}, nil
}

func mattermostTime(milliseconds int64) time.Time {
	return time.UnixMilli(milliseconds).UTC()
}

func boundedMattermostText(value string, maximum int) string {
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

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
