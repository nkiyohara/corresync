package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxConversationPageSize   = 100
	MaxMessagePageSize        = 100
	MaxMessageQueryBytes      = 1024
	MaxMessageTextBytes       = 1 << 20
	MaxMessageSnippetBytes    = 1024
	MaxMessageAttachmentBytes = 3 << 20
	MaxMessageResultBytes     = 8 << 20
	MaxMessageCollectionItems = 256
	MaxMessageCursorBytes     = 8192
)

type MessagingRouteKind = domain.MessagingRouteKind

const (
	MessagingRouteTeamsGraph = domain.MessagingRouteTeamsGraph
	MessagingRouteTeamsWeb   = domain.MessagingRouteTeamsWeb
	MessagingRouteSlackAPI   = domain.MessagingRouteSlackAPI
	MessagingRouteMattermost = domain.MessagingRouteMattermost
)

// MessageActorMode preserves whether the provider acts as the signed-in user
// or as a visibly attributed app. Unavailable is a capability state, not an
// executable actor.
type MessageActorMode string

const (
	MessageActorDelegatedUser MessageActorMode = "delegated_user"
	MessageActorApp           MessageActorMode = "app"
	MessageActorUnavailable   MessageActorMode = "unavailable"
)

func (mode MessageActorMode) Validate() error {
	switch mode {
	case MessageActorDelegatedUser, MessageActorApp, MessageActorUnavailable:
		return nil
	default:
		return fmt.Errorf("unsupported messaging actor mode %q", mode)
	}
}

// MessageActor is a bounded provider identity. DisplayName is untrusted
// presentation data and is never used as a routing key.
type MessageActor struct {
	ID          string           `json:"id,omitempty"`
	Mode        MessageActorMode `json:"mode"`
	DisplayName string           `json:"displayName,omitempty"`
}

func (actor MessageActor) Validate(routable bool) error {
	if err := actor.Mode.Validate(); err != nil {
		return err
	}
	if routable && actor.Mode == MessageActorUnavailable {
		return errors.New("an unavailable messaging actor cannot execute a route")
	}
	if actor.Mode != MessageActorUnavailable {
		if err := validateOpaqueValue("messaging actor ID", actor.ID); err != nil {
			return err
		}
	} else if actor.ID != "" {
		return errors.New("an unavailable messaging actor cannot carry an ID")
	}
	return validateMessageDisplay("messaging actor display name", actor.DisplayName, 1024)
}

// MessagingProvenance is the complete non-secret routing boundary attached to
// every conversation and message result.
type MessagingProvenance struct {
	AccountID      domain.AccountID           `json:"accountId"`
	Provider       domain.MessagingProviderID `json:"provider"`
	Route          MessagingRouteKind         `json:"route"`
	WorkspaceID    string                     `json:"workspaceId"`
	Actor          MessageActor               `json:"actor"`
	ConversationID string                     `json:"conversationId,omitempty"`
	SourceObjectID string                     `json:"sourceObjectId,omitempty"`
}

func (provenance MessagingProvenance) Validate() error {
	if err := provenance.AccountID.ValidateOpaque(); err != nil {
		return err
	}
	if err := provenance.Provider.Validate(); err != nil {
		return err
	}
	if err := provenance.Route.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("messaging workspace ID", provenance.WorkspaceID); err != nil {
		return err
	}
	if err := provenance.Actor.Validate(true); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"conversation ID":  provenance.ConversationID,
		"source object ID": provenance.SourceObjectID,
	} {
		if value != "" {
			if err := validateOpaqueValue(name, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// MessageCapabilities contains only account-observed operations. False means
// unavailable; it never means an adapter forgot to report a capability.
type MessageCapabilities struct {
	ListConversations  bool             `json:"listConversations"`
	History            bool             `json:"history"`
	SensitiveRead      bool             `json:"sensitiveRead"`
	Search             bool             `json:"search"`
	IncrementalSync    bool             `json:"incrementalSync"`
	Send               bool             `json:"send"`
	Reply              bool             `json:"reply"`
	Edit               bool             `json:"edit"`
	Delete             bool             `json:"delete"`
	Reactions          bool             `json:"reactions"`
	AttachmentReads    bool             `json:"attachmentReads"`
	AttachmentWrites   bool             `json:"attachmentWrites"`
	CreateConversation bool             `json:"createConversation"`
	Membership         bool             `json:"membership"`
	ActorMode          MessageActorMode `json:"actorMode"`
}

func (capabilities MessageCapabilities) Validate() error {
	if err := capabilities.ActorMode.Validate(); err != nil {
		return err
	}
	if capabilities.SensitiveRead && !capabilities.History {
		return errors.New("messaging sensitive reads require history")
	}
	if capabilities.IncrementalSync && !capabilities.History {
		return errors.New("messaging incremental sync requires history")
	}
	if capabilities.Reply && !capabilities.Send {
		return errors.New("messaging reply capability requires send")
	}
	if capabilities.AttachmentReads && !capabilities.SensitiveRead {
		return errors.New("messaging attachment reads require sensitive message reads")
	}
	write := capabilities.Send || capabilities.Reply || capabilities.Edit ||
		capabilities.Delete || capabilities.Reactions || capabilities.AttachmentWrites ||
		capabilities.CreateConversation || capabilities.Membership
	if write && capabilities.ActorMode == MessageActorUnavailable {
		return errors.New("messaging writes require an observed actor")
	}
	return nil
}

type ConversationKind string

const (
	ConversationDirect  ConversationKind = "direct"
	ConversationGroup   ConversationKind = "group"
	ConversationChannel ConversationKind = "channel"
	ConversationMeeting ConversationKind = "meeting"
)

func (kind ConversationKind) Validate() error {
	switch kind {
	case ConversationDirect, ConversationGroup, ConversationChannel, ConversationMeeting:
		return nil
	default:
		return fmt.Errorf("unsupported conversation kind %q", kind)
	}
}

// ConversationVisibility preserves provider-observed access shape. Unknown is
// explicit: a missing provider field must never be normalized to private.
type ConversationVisibility string

const (
	ConversationVisibilityUnknown ConversationVisibility = "unknown"
	ConversationVisibilityPublic  ConversationVisibility = "public"
	ConversationVisibilityPrivate ConversationVisibility = "private"
	ConversationVisibilityShared  ConversationVisibility = "shared"
)

func (visibility ConversationVisibility) Validate() error {
	switch visibility {
	case ConversationVisibilityUnknown, ConversationVisibilityPublic,
		ConversationVisibilityPrivate, ConversationVisibilityShared:
		return nil
	default:
		return fmt.Errorf("unsupported conversation visibility %q", visibility)
	}
}

type Conversation struct {
	ID               string                 `json:"id"`
	ContainerID      string                 `json:"containerId,omitempty"`
	Version          string                 `json:"version,omitempty"`
	Kind             ConversationKind       `json:"kind"`
	Visibility       ConversationVisibility `json:"visibility"`
	Name             string                 `json:"name,omitempty"`
	Topic            string                 `json:"topic,omitempty"`
	MemberCount      int                    `json:"memberCount,omitempty"`
	MemberCountKnown bool                   `json:"memberCountKnown"`
	LastActivityAt   string                 `json:"lastActivityAt,omitempty"`
	Provenance       MessagingProvenance    `json:"provenance"`
}

func (conversation Conversation) Validate() error {
	if err := validateOpaqueValue("conversation ID", conversation.ID); err != nil {
		return err
	}
	if conversation.Version != "" {
		if err := validateOpaqueValue("conversation version", conversation.Version); err != nil {
			return err
		}
	}
	if conversation.ContainerID != "" {
		if err := validateOpaqueValue("conversation container ID", conversation.ContainerID); err != nil {
			return err
		}
	}
	if err := conversation.Kind.Validate(); err != nil {
		return err
	}
	if err := conversation.Visibility.Validate(); err != nil {
		return err
	}
	if err := validateMessageDisplay("conversation name", conversation.Name, 4096); err != nil {
		return err
	}
	if err := validateMessageDisplay("conversation topic", conversation.Topic, 8192); err != nil {
		return err
	}
	if conversation.MemberCount < 0 || conversation.MemberCount > 1_000_000 {
		return errors.New("conversation member count is out of bounds")
	}
	if !conversation.MemberCountKnown && conversation.MemberCount != 0 {
		return errors.New("an unobserved conversation member count must be zero")
	}
	if err := validateOptionalTimestamp("conversation activity", conversation.LastActivityAt); err != nil {
		return err
	}
	if err := conversation.Provenance.Validate(); err != nil {
		return err
	}
	if conversation.Provenance.ConversationID != conversation.ID ||
		conversation.Provenance.SourceObjectID != conversation.ID {
		return errors.New("conversation provenance does not match its identity")
	}
	return nil
}

type ConversationListInput struct {
	Account     domain.AccountID `json:"account"`
	WorkspaceID string           `json:"workspaceId"`
	Cursor      string           `json:"cursor,omitempty"`
	Limit       int              `json:"limit"`
}

func (input ConversationListInput) Validate() error {
	return validateMessagingReadRoute(input.Account, input.WorkspaceID, "", input.Cursor, input.Limit)
}

type ConversationPage struct {
	Conversations []Conversation       `json:"conversations"`
	NextCursor    string               `json:"nextCursor,omitempty"`
	Partial       bool                 `json:"partial"`
	PartialReason string               `json:"partialReason,omitempty"`
	ObservedAt    time.Time            `json:"observedAt"`
	Degradations  []domain.Degradation `json:"degradations,omitempty"`
}

type MessageFormat string

const (
	MessageFormatPlain    MessageFormat = "plain"
	MessageFormatMarkdown MessageFormat = "markdown"
	MessageFormatHTML     MessageFormat = "html"
)

func (format MessageFormat) Validate() error {
	switch format {
	case MessageFormatPlain, MessageFormatMarkdown, MessageFormatHTML:
		return nil
	default:
		return fmt.Errorf("unsupported message format %q", format)
	}
}

type MessageContent struct {
	Format MessageFormat `json:"format"`
	Text   string        `json:"text"`
}

func (content MessageContent) Validate() error {
	return content.validate(false)
}

func (content MessageContent) validate(optional bool) error {
	if err := content.Format.Validate(); err != nil {
		return err
	}
	return validateMessageText("message content", content.Text, MaxMessageTextBytes, optional)
}

type MessageMentionKind string

const (
	MessageMentionUser    MessageMentionKind = "user"
	MessageMentionChannel MessageMentionKind = "channel"
)

type MessageMention struct {
	ID          string             `json:"id"`
	Kind        MessageMentionKind `json:"kind"`
	DisplayName string             `json:"displayName,omitempty"`
}

func (mention MessageMention) Validate() error {
	if err := validateOpaqueValue("mention ID", mention.ID); err != nil {
		return err
	}
	switch mention.Kind {
	case MessageMentionUser, MessageMentionChannel:
	default:
		return fmt.Errorf("unsupported mention kind %q", mention.Kind)
	}
	return validateMessageDisplay("mention display name", mention.DisplayName, 1024)
}

type MessageLink struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

func (link MessageLink) Validate() error {
	if len(link.URL) == 0 || len(link.URL) > 8192 || strings.ContainsAny(link.URL, "\r\n\x00") {
		return errors.New("message link is malformed")
	}
	parsed, err := url.Parse(link.URL)
	if err != nil || !parsed.IsAbs() || parsed.User != nil {
		return errors.New("message link must be one absolute credential-free URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
	default:
		return errors.New("message link uses an unsupported scheme")
	}
	return validateMessageDisplay("message link label", link.Label, 2048)
}

type MessageReaction struct {
	Name           string `json:"name"`
	Count          int    `json:"count"`
	CountKnown     bool   `json:"countKnown"`
	ReactedByActor bool   `json:"reactedByActor"`
}

func (reaction MessageReaction) Validate() error {
	if err := validateMessageDisplay("reaction name", reaction.Name, 256); err != nil || reaction.Name == "" {
		return errors.New("reaction name is malformed")
	}
	if reaction.Count < 0 || reaction.Count > 1_000_000 {
		return errors.New("reaction count is out of bounds")
	}
	if !reaction.CountKnown && reaction.Count != 0 {
		return errors.New("an unobserved reaction count must be zero")
	}
	return nil
}

type MessageAttachment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MediaType    string `json:"mediaType,omitempty"`
	Size         int64  `json:"size"`
	SizeKnown    bool   `json:"sizeKnown"`
	Downloadable bool   `json:"downloadable"`
}

func (attachment MessageAttachment) Validate() error {
	if err := validateOpaqueValue("message attachment ID", attachment.ID); err != nil {
		return err
	}
	if err := validateMessageDisplay("message attachment name", attachment.Name, 4096); err != nil || attachment.Name == "" {
		return errors.New("message attachment name is malformed")
	}
	if err := validateMessageDisplay("message attachment media type", attachment.MediaType, 256); err != nil {
		return err
	}
	if attachment.Size < 0 || attachment.Size > 1<<40 {
		return errors.New("message attachment size is out of bounds")
	}
	if !attachment.SizeKnown && attachment.Size != 0 {
		return errors.New("an unobserved message attachment size must be zero")
	}
	return nil
}

type MessageSummary struct {
	ID             string              `json:"id"`
	Version        string              `json:"version,omitempty"`
	ConversationID string              `json:"conversationId"`
	ThreadRootID   string              `json:"threadRootId,omitempty"`
	Author         MessageActor        `json:"author"`
	CreatedAt      string              `json:"createdAt"`
	UpdatedAt      string              `json:"updatedAt,omitempty"`
	Snippet        string              `json:"snippet,omitempty"`
	ReplyCount     int                 `json:"replyCount,omitempty"`
	HasAttachments bool                `json:"hasAttachments"`
	Deleted        bool                `json:"deleted"`
	Provenance     MessagingProvenance `json:"provenance"`
}

func (summary MessageSummary) Validate() error {
	for name, value := range map[string]string{
		"message ID": summary.ID, "message conversation ID": summary.ConversationID,
	} {
		if err := validateOpaqueValue(name, value); err != nil {
			return err
		}
	}
	for name, value := range map[string]string{
		"message version": summary.Version, "message thread root ID": summary.ThreadRootID,
	} {
		if value != "" {
			if err := validateOpaqueValue(name, value); err != nil {
				return err
			}
		}
	}
	if err := summary.Author.Validate(false); err != nil {
		return err
	}
	if err := validateTimestamp("message creation", summary.CreatedAt); err != nil {
		return err
	}
	if err := validateOptionalTimestamp("message update", summary.UpdatedAt); err != nil {
		return err
	}
	if err := validateMessageText("message snippet", summary.Snippet, MaxMessageSnippetBytes, true); err != nil {
		return err
	}
	if summary.ReplyCount < 0 || summary.ReplyCount > 1_000_000 {
		return errors.New("message reply count is out of bounds")
	}
	if err := summary.Provenance.Validate(); err != nil {
		return err
	}
	if summary.Provenance.ConversationID != summary.ConversationID ||
		summary.Provenance.SourceObjectID != summary.ID {
		return errors.New("message provenance does not match its identity")
	}
	return nil
}

type Message struct {
	Summary     MessageSummary      `json:"summary"`
	Content     MessageContent      `json:"content"`
	Links       []MessageLink       `json:"links,omitempty"`
	Mentions    []MessageMention    `json:"mentions,omitempty"`
	Reactions   []MessageReaction   `json:"reactions,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

func (message Message) Validate() error {
	if err := message.Summary.Validate(); err != nil {
		return err
	}
	if err := message.Content.validate(message.Summary.Deleted || len(message.Attachments) != 0); err != nil {
		return err
	}
	if len(message.Links) > MaxMessageCollectionItems ||
		len(message.Mentions) > MaxMessageCollectionItems ||
		len(message.Reactions) > MaxMessageCollectionItems ||
		len(message.Attachments) > MaxMessageCollectionItems {
		return errors.New("message collection exceeds the configured limit")
	}
	for _, link := range message.Links {
		if err := link.Validate(); err != nil {
			return err
		}
	}
	for _, mention := range message.Mentions {
		if err := mention.Validate(); err != nil {
			return err
		}
	}
	for _, reaction := range message.Reactions {
		if err := reaction.Validate(); err != nil {
			return err
		}
	}
	for _, attachment := range message.Attachments {
		if err := attachment.Validate(); err != nil {
			return err
		}
	}
	return validateMessageEncodedSize("message result", message)
}

type MessageListInput struct {
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	Cursor         string           `json:"cursor,omitempty"`
	Limit          int              `json:"limit"`
}

func (input MessageListInput) Validate() error {
	if err := validateMessagingReadRoute(input.Account, input.WorkspaceID, input.ConversationID, input.Cursor, input.Limit); err != nil {
		return err
	}
	if input.ThreadRootID != "" {
		return validateOpaqueValue("message thread root ID", input.ThreadRootID)
	}
	return nil
}

type MessageSearchInput struct {
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId,omitempty"`
	Query          string           `json:"query"`
	Cursor         string           `json:"cursor,omitempty"`
	Limit          int              `json:"limit"`
}

func (input MessageSearchInput) Validate() error {
	if err := validateMessagingReadRoute(input.Account, input.WorkspaceID, input.ConversationID, input.Cursor, input.Limit); err != nil {
		return err
	}
	return validateMessageText("message search query", input.Query, MaxMessageQueryBytes, false)
}

type MessageGetInput struct {
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	MessageID      string           `json:"messageId"`
}

func (input MessageGetInput) Validate() error {
	if err := validateMessagingIdentity(input.Account, input.WorkspaceID, input.ConversationID); err != nil {
		return err
	}
	if input.ThreadRootID != "" {
		if err := validateOpaqueValue("message thread root ID", input.ThreadRootID); err != nil {
			return err
		}
	}
	return validateOpaqueValue("message ID", input.MessageID)
}

type MessagePage struct {
	Messages      []MessageSummary     `json:"messages"`
	NextCursor    string               `json:"nextCursor,omitempty"`
	Partial       bool                 `json:"partial"`
	PartialReason string               `json:"partialReason,omitempty"`
	ObservedAt    time.Time            `json:"observedAt"`
	Degradations  []domain.Degradation `json:"degradations,omitempty"`
}

type MessageCursor struct {
	Version        int                        `json:"version"`
	Account        domain.AccountID           `json:"account"`
	Provider       domain.MessagingProviderID `json:"provider"`
	Route          MessagingRouteKind         `json:"route"`
	WorkspaceID    string                     `json:"workspaceId"`
	ConversationID string                     `json:"conversationId,omitempty"`
	Opaque         string                     `json:"opaque"`
}

func (cursor MessageCursor) Validate() error {
	if cursor.Version != 1 {
		return errors.New("unsupported message cursor version")
	}
	if err := cursor.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := cursor.Provider.Validate(); err != nil {
		return err
	}
	if err := cursor.Route.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("message cursor workspace ID", cursor.WorkspaceID); err != nil {
		return err
	}
	if cursor.ConversationID != "" {
		if err := validateOpaqueValue("message cursor conversation ID", cursor.ConversationID); err != nil {
			return err
		}
	}
	if cursor.Opaque == "" || len(cursor.Opaque) > MaxMessageCursorBytes ||
		strings.ContainsAny(cursor.Opaque, "\r\n\x00") {
		return errors.New("message cursor is malformed")
	}
	return nil
}

type MessageSyncInput struct {
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId,omitempty"`
	Cursor         *MessageCursor   `json:"cursor,omitempty"`
	Limit          int              `json:"limit"`
}

func (input MessageSyncInput) Validate(provenance MessagingProvenance) error {
	if err := validateMessagingReadRoute(input.Account, input.WorkspaceID, input.ConversationID, "", input.Limit); err != nil {
		return err
	}
	if input.Cursor == nil {
		return nil
	}
	if err := input.Cursor.Validate(); err != nil {
		return err
	}
	if input.Cursor.Account != input.Account || input.Cursor.Provider != provenance.Provider ||
		input.Cursor.Route != provenance.Route || input.Cursor.WorkspaceID != input.WorkspaceID ||
		input.Cursor.ConversationID != input.ConversationID {
		return errors.New("message cursor does not match the selected route")
	}
	return nil
}

type MessageChangeKind string

const (
	MessageChangeUpsert MessageChangeKind = "upsert"
	MessageChangeDelete MessageChangeKind = "delete"
)

type MessageChange struct {
	Kind    MessageChangeKind `json:"kind"`
	Message *MessageSummary   `json:"message,omitempty"`
	ID      string            `json:"id,omitempty"`
}

type MessageChangePage struct {
	Changes      []MessageChange      `json:"changes"`
	Cursor       MessageCursor        `json:"cursor"`
	HasMore      bool                 `json:"hasMore"`
	Reset        bool                 `json:"reset"`
	ObservedAt   time.Time            `json:"observedAt"`
	Degradations []domain.Degradation `json:"degradations,omitempty"`
}

type MessageAttachmentGetInput struct {
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	MessageID      string           `json:"messageId"`
	AttachmentID   string           `json:"attachmentId"`
}

func (input MessageAttachmentGetInput) Validate() error {
	if err := (MessageGetInput{Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID,
		MessageID: input.MessageID}).Validate(); err != nil {
		return err
	}
	return validateOpaqueValue("message attachment ID", input.AttachmentID)
}

type MessageAttachmentContent struct {
	Metadata MessageAttachment `json:"metadata"`
	Data     []byte            `json:"data"`
}

func (content MessageAttachmentContent) Validate() error {
	if err := content.Metadata.Validate(); err != nil {
		return err
	}
	if len(content.Data) > MaxMessageAttachmentBytes || int64(len(content.Data)) != content.Metadata.Size {
		return errors.New("message attachment content does not match bounded metadata")
	}
	return nil
}

func validateMessagingReadRoute(account domain.AccountID, workspaceID, conversationID, cursor string, limit int) error {
	if err := validateMessagingIdentity(account, workspaceID, conversationID); err != nil {
		return err
	}
	if len(cursor) > MaxMessageCursorBytes || strings.ContainsAny(cursor, "\r\n\x00") {
		return errors.New("messaging pagination cursor is malformed")
	}
	if limit < 1 || limit > MaxMessagePageSize {
		return fmt.Errorf("messaging limit must be between 1 and %d", MaxMessagePageSize)
	}
	return nil
}

func validateMessagingIdentity(account domain.AccountID, workspaceID, conversationID string) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateOpaqueValue("messaging workspace ID", workspaceID); err != nil {
		return err
	}
	if conversationID != "" {
		return validateOpaqueValue("conversation ID", conversationID)
	}
	return nil
}

func validateMessageText(name, value string, maximum int, optional bool) error {
	if !optional && value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(value) > maximum || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s is malformed", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return fmt.Errorf("%s contains an unsupported control character", name)
		}
	}
	return nil
}

func validateMessageDisplay(name, value string, maximum int) error {
	return validateMessageText(name, value, maximum, true)
}

func validateTimestamp(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s timestamp is required", name)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Format(time.RFC3339Nano) != value {
		return fmt.Errorf("%s timestamp must be canonical RFC3339", name)
	}
	return nil
}

func validateOptionalTimestamp(name, value string) error {
	if value == "" {
		return nil
	}
	return validateTimestamp(name, value)
}

func validateMessageEncodedSize(name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	if len(encoded) > MaxMessageResultBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxMessageResultBytes)
	}
	return nil
}
