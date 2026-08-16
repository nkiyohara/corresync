package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const messagePreviewRunes = 500

// MessageWriteRoute is embedded in every write payload so the immutable
// preview binds the exact workspace and observed actor as well as the account.
type MessageWriteRoute struct {
	Account     domain.AccountID `json:"account"`
	WorkspaceID string           `json:"workspaceId"`
	Actor       MessageActor     `json:"actor"`
}

func (route MessageWriteRoute) Validate() error {
	if err := validateMessagingIdentity(route.Account, route.WorkspaceID, ""); err != nil {
		return err
	}
	return route.Actor.Validate(true)
}

type MessageAttachmentUpload struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType,omitempty"`
	Data      []byte `json:"data"`
}

func (upload MessageAttachmentUpload) Validate() error {
	if err := validateMessageDisplay("message upload name", upload.Name, 4096); err != nil || upload.Name == "" {
		return errors.New("message upload name is malformed")
	}
	if err := validateMessageDisplay("message upload media type", upload.MediaType, 256); err != nil {
		return err
	}
	if len(upload.Data) == 0 || len(upload.Data) > MaxMessageAttachmentBytes {
		return errors.New("message upload is empty or exceeds the configured limit")
	}
	return nil
}

type MessageSendInput struct {
	MessageWriteRoute
	ConversationID string                    `json:"conversationId"`
	ReplyToID      string                    `json:"replyToId,omitempty"`
	Content        MessageContent            `json:"content"`
	Mentions       []MessageMention          `json:"mentions,omitempty"`
	Attachments    []MessageAttachmentUpload `json:"attachments,omitempty"`
}

func (input MessageSendInput) Validate() error {
	if err := input.MessageWriteRoute.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("message conversation ID", input.ConversationID); err != nil {
		return err
	}
	if input.ReplyToID != "" {
		if err := validateOpaqueValue("message reply target ID", input.ReplyToID); err != nil {
			return err
		}
	}
	if err := input.Content.validate(len(input.Attachments) != 0); err != nil {
		return err
	}
	if len(input.Mentions) > MaxMessageCollectionItems || len(input.Attachments) > MaxMessageCollectionItems {
		return errors.New("message send collections exceed the configured limit")
	}
	for _, mention := range input.Mentions {
		if err := mention.Validate(); err != nil {
			return err
		}
	}
	total := 0
	for _, attachment := range input.Attachments {
		if err := attachment.Validate(); err != nil {
			return err
		}
		total += len(attachment.Data)
		if total > MaxMessageAttachmentBytes {
			return errors.New("message uploads exceed the aggregate configured limit")
		}
	}
	return nil
}

type MessageEditInput struct {
	MessageWriteRoute
	ConversationID string           `json:"conversationId"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	MessageID      string           `json:"messageId"`
	Version        string           `json:"version"`
	Content        MessageContent   `json:"content"`
	Mentions       []MessageMention `json:"mentions,omitempty"`
}

func (input MessageEditInput) Validate() error {
	if err := validateMessageWriteIdentity(input.MessageWriteRoute, input.ConversationID, input.MessageID, input.Version); err != nil {
		return err
	}
	if input.ThreadRootID != "" {
		if err := validateOpaqueValue("message thread root ID", input.ThreadRootID); err != nil {
			return err
		}
	}
	if err := input.Content.Validate(); err != nil {
		return err
	}
	if len(input.Mentions) > MaxMessageCollectionItems {
		return errors.New("message edit mentions exceed the configured limit")
	}
	for _, mention := range input.Mentions {
		if err := mention.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type MessageDeleteInput struct {
	MessageWriteRoute
	ConversationID string `json:"conversationId"`
	ThreadRootID   string `json:"threadRootId,omitempty"`
	MessageID      string `json:"messageId"`
	Version        string `json:"version"`
}

func (input MessageDeleteInput) Validate() error {
	if err := validateMessageWriteIdentity(input.MessageWriteRoute, input.ConversationID, input.MessageID, input.Version); err != nil {
		return err
	}
	if input.ThreadRootID != "" {
		return validateOpaqueValue("message thread root ID", input.ThreadRootID)
	}
	return nil
}

type MessageReactionInput struct {
	MessageWriteRoute
	ConversationID string `json:"conversationId"`
	ThreadRootID   string `json:"threadRootId,omitempty"`
	MessageID      string `json:"messageId"`
	Version        string `json:"version"`
	Reaction       string `json:"reaction"`
	Remove         bool   `json:"remove"`
}

func (input MessageReactionInput) Validate() error {
	if err := validateMessageWriteIdentity(input.MessageWriteRoute, input.ConversationID, input.MessageID, input.Version); err != nil {
		return err
	}
	if input.ThreadRootID != "" {
		if err := validateOpaqueValue("message thread root ID", input.ThreadRootID); err != nil {
			return err
		}
	}
	return (MessageReaction{Name: input.Reaction}).Validate()
}

type ConversationMemberRole string

const (
	ConversationMember ConversationMemberRole = "member"
	ConversationOwner  ConversationMemberRole = "owner"
	ConversationGuest  ConversationMemberRole = "guest"
)

type ConversationMemberInput struct {
	ID          string                 `json:"id"`
	Role        ConversationMemberRole `json:"role"`
	DisplayName string                 `json:"displayName,omitempty"`
}

func (member ConversationMemberInput) Validate() error {
	if err := validateOpaqueValue("conversation member ID", member.ID); err != nil {
		return err
	}
	switch member.Role {
	case ConversationMember, ConversationOwner, ConversationGuest:
	default:
		return fmt.Errorf("unsupported conversation member role %q", member.Role)
	}
	return validateMessageDisplay("conversation member display name", member.DisplayName, 1024)
}

type ConversationCreateInput struct {
	MessageWriteRoute
	ContainerID string                    `json:"containerId,omitempty"`
	Kind        ConversationKind          `json:"kind"`
	Visibility  ConversationVisibility    `json:"visibility"`
	Name        string                    `json:"name,omitempty"`
	Topic       string                    `json:"topic,omitempty"`
	Members     []ConversationMemberInput `json:"members"`
}

func (input ConversationCreateInput) Validate() error {
	if err := input.MessageWriteRoute.Validate(); err != nil {
		return err
	}
	if err := input.Kind.Validate(); err != nil {
		return err
	}
	if input.Kind == ConversationMeeting {
		return errors.New("meeting lifecycle creation is outside messaging scope")
	}
	if input.ContainerID != "" {
		if err := validateOpaqueValue("conversation container ID", input.ContainerID); err != nil {
			return err
		}
	}
	if err := input.Visibility.Validate(); err != nil {
		return err
	}
	if input.Kind != ConversationChannel && input.Visibility != ConversationVisibilityPrivate {
		return errors.New("direct and group conversations must be private")
	}
	if input.Kind != ConversationChannel && input.ContainerID != "" {
		return errors.New("only a channel can select a parent container")
	}
	if input.Kind == ConversationChannel && input.Visibility == ConversationVisibilityUnknown {
		return errors.New("channel creation requires explicit visibility")
	}
	if err := validateMessageDisplay("conversation name", input.Name, 4096); err != nil {
		return err
	}
	if err := validateMessageDisplay("conversation topic", input.Topic, 8192); err != nil {
		return err
	}
	if len(input.Members) == 0 || len(input.Members) > MaxMessageCollectionItems {
		return errors.New("conversation creation requires bounded members")
	}
	seen := make(map[string]struct{}, len(input.Members))
	for _, member := range input.Members {
		if err := member.Validate(); err != nil {
			return err
		}
		if _, exists := seen[member.ID]; exists {
			return errors.New("conversation creation contains a duplicate member")
		}
		seen[member.ID] = struct{}{}
	}
	return nil
}

type MembershipAction string

const (
	MembershipAdd    MembershipAction = "add"
	MembershipRemove MembershipAction = "remove"
)

type ConversationMembershipInput struct {
	MessageWriteRoute
	ConversationID string                  `json:"conversationId"`
	Version        string                  `json:"version"`
	Action         MembershipAction        `json:"action"`
	Member         ConversationMemberInput `json:"member"`
}

func (input ConversationMembershipInput) Validate() error {
	if err := input.MessageWriteRoute.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("conversation ID", input.ConversationID); err != nil {
		return err
	}
	if err := validateOpaqueValue("conversation version", input.Version); err != nil {
		return err
	}
	switch input.Action {
	case MembershipAdd, MembershipRemove:
	default:
		return fmt.Errorf("unsupported membership action %q", input.Action)
	}
	return input.Member.Validate()
}

type ConversationMembershipResult struct {
	ConversationID string                  `json:"conversationId"`
	Version        string                  `json:"version"`
	Action         MembershipAction        `json:"action"`
	Member         ConversationMemberInput `json:"member"`
	Provenance     MessagingProvenance     `json:"provenance"`
}

type MessageTextReview struct {
	Format  MessageFormat `json:"format"`
	Preview string        `json:"preview,omitempty"`
	Bytes   int           `json:"bytes"`
	SHA256  string        `json:"sha256"`
}

type MessageAttachmentReview struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType,omitempty"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}

type MessageWriteReview struct {
	Account        domain.AccountID             `json:"account"`
	Provider       domain.MessagingProviderID   `json:"provider"`
	Route          MessagingRouteKind           `json:"route"`
	WorkspaceID    string                       `json:"workspaceId"`
	Actor          MessageActor                 `json:"actor"`
	Capabilities   MessageCapabilities          `json:"capabilities"`
	Action         string                       `json:"action"`
	ConversationID string                       `json:"conversationId,omitempty"`
	ThreadRootID   string                       `json:"threadRootId,omitempty"`
	MessageID      string                       `json:"messageId,omitempty"`
	Version        string                       `json:"version,omitempty"`
	ReplyToID      string                       `json:"replyToId,omitempty"`
	Content        *MessageTextReview           `json:"content,omitempty"`
	Reaction       string                       `json:"reaction,omitempty"`
	RemoveReaction bool                         `json:"removeReaction,omitempty"`
	Kind           ConversationKind             `json:"kind,omitempty"`
	ContainerID    string                       `json:"containerId,omitempty"`
	Visibility     ConversationVisibility       `json:"visibility,omitempty"`
	Name           string                       `json:"name,omitempty"`
	Topic          string                       `json:"topic,omitempty"`
	Members        []ConversationMemberInput    `json:"members,omitempty"`
	Membership     *ConversationMembershipInput `json:"membership,omitempty"`
	Mentions       []MessageMention             `json:"mentions,omitempty"`
	Attachments    []MessageAttachmentReview    `json:"attachments,omitempty"`
	Degradations   []domain.Degradation         `json:"degradations,omitempty"`
}

type MessageDeleteResult struct {
	ConversationID string              `json:"conversationId"`
	MessageID      string              `json:"messageId"`
	Provenance     MessagingProvenance `json:"provenance"`
}

type MessageWriteAccess struct {
	Status       string                        `json:"status"`
	Message      *Message                      `json:"message,omitempty"`
	Conversation *Conversation                 `json:"conversation,omitempty"`
	Membership   *ConversationMembershipResult `json:"membership,omitempty"`
	Reaction     *MessageReaction              `json:"reaction,omitempty"`
	Deleted      *MessageDeleteResult          `json:"deleted,omitempty"`
	Review       MessageWriteReview            `json:"review"`
	Preview      *approval.Preview             `json:"preview,omitempty"`
}

type MessageWriter interface {
	SendMessage(context.Context, MessageSendInput) (Message, error)
	EditMessage(context.Context, MessageEditInput) (Message, error)
	DeleteMessage(context.Context, MessageDeleteInput) error
	SetMessageReaction(context.Context, MessageReactionInput) (MessageReaction, error)
	CreateConversation(context.Context, ConversationCreateInput) (Conversation, error)
	ChangeConversationMembership(context.Context, ConversationMembershipInput) (ConversationMembershipResult, error)
}

func (service *MessagingService) Send(ctx context.Context, input MessageSendInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if len(input.Attachments) != 0 && !service.capabilities.AttachmentWrites {
		return MessageWriteAccess{}, errors.New("the selected messaging route does not support attachment writes")
	}
	action := "send"
	if input.ReplyToID != "" {
		action = "reply"
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, action); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message."+action, domain.EffectExternalWrite,
		conversationTarget(input.WorkspaceID, input.ConversationID), input, input.review(action), caller)
}

func (service *MessagingService) Edit(ctx context.Context, input MessageEditInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, "edit"); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message.edit", domain.EffectExternalWrite,
		messageTarget(input.WorkspaceID, input.ConversationID, input.MessageID), input, input.review(), caller)
}

func (service *MessagingService) Delete(ctx context.Context, input MessageDeleteInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, "delete"); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message.delete", domain.EffectDestructiveWrite,
		messageTarget(input.WorkspaceID, input.ConversationID, input.MessageID), input, input.review(), caller)
}

func (service *MessagingService) React(ctx context.Context, input MessageReactionInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, "react"); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message.react", domain.EffectExternalWrite,
		messageTarget(input.WorkspaceID, input.ConversationID, input.MessageID), input, input.review(), caller)
}

func (service *MessagingService) CreateConversation(ctx context.Context, input ConversationCreateInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, "create_conversation"); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message.create_conversation", domain.EffectExternalWrite,
		workspaceTarget(input.WorkspaceID), input, input.review(), caller)
}

func (service *MessagingService) ChangeMembership(ctx context.Context, input ConversationMembershipInput, caller domain.Caller) (MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageWriteAccess{}, err
	}
	if err := service.validateWriteRoute(input.MessageWriteRoute, "membership"); err != nil {
		return MessageWriteAccess{}, err
	}
	return service.prepareMessageWrite(ctx, "message.membership", domain.EffectExternalWrite,
		conversationTarget(input.WorkspaceID, input.ConversationID), input, input.review(), caller)
}

func (service *MessagingService) prepareMessageWrite(ctx context.Context, name string, effect domain.Effect, target domain.TargetRef, payload any, review MessageWriteReview, caller domain.Caller) (MessageWriteAccess, error) {
	review = service.decorateMessageReview(review)
	operation, err := domain.NewTargetedOperation(name, effect, service.provenance.AccountID, target, payload)
	if err != nil {
		return MessageWriteAccess{}, fmt.Errorf("create %s operation: %w", name, err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return MessageWriteAccess{Status: "approval_required", Review: review, Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MessageWriteAccess{}, fmt.Errorf("%s operation was denied", name)
	case policy.VerdictAllow:
		return MessageWriteAccess{}, fmt.Errorf("%s policy attempted to bypass mandatory preview", name)
	default:
		return MessageWriteAccess{}, fmt.Errorf("%s operation received an unknown policy verdict", name)
	}
}

func (service *MessagingService) CommitSend(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input MessageSendInput
	operation, name, err := service.committedSend(ctx, token, caller, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	message, callErr := service.port.SendMessage(ctx, input)
	if err := service.finishMessageWrite(ctx, operation, caller, input.ConversationID, "", &message, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	status := "sent"
	if name == "message.reply" {
		status = "replied"
	}
	return MessageWriteAccess{Status: status, Message: &message, Review: service.decorateMessageReview(input.review(name[len("message."):]))}, nil
}

func (service *MessagingService) committedSend(ctx context.Context, token string, caller domain.Caller, input *MessageSendInput) (domain.Operation, string, error) {
	for _, name := range []string{"message.send", "message.reply"} {
		operation, err := service.guard.CommitFor(ctx, token, caller, name, domain.EffectExternalWrite)
		if err != nil {
			continue
		}
		if err := operation.DecodePayload(input); err != nil {
			return domain.Operation{}, "", err
		}
		if err := input.Validate(); err != nil {
			return domain.Operation{}, "", err
		}
		action := "send"
		if input.ReplyToID != "" {
			action = "reply"
		}
		if name != "message."+action {
			return domain.Operation{}, "", errors.New("message send preview does not match reply semantics")
		}
		return operation, name, service.validateWriteRoute(input.MessageWriteRoute, action)
	}
	return domain.Operation{}, "", errors.New("consume message send preview: token does not match send or reply")
}

func (service *MessagingService) CommitEdit(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input MessageEditInput
	operation, err := service.committedMessageWrite(ctx, token, caller, "message.edit", domain.EffectExternalWrite, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	message, callErr := service.port.EditMessage(ctx, input)
	if err := service.finishMessageWrite(ctx, operation, caller, input.ConversationID, input.MessageID, &message, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	return MessageWriteAccess{Status: "edited", Message: &message, Review: service.decorateMessageReview(input.review())}, nil
}

func (service *MessagingService) CommitDelete(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input MessageDeleteInput
	operation, err := service.committedMessageWrite(ctx, token, caller, "message.delete", domain.EffectDestructiveWrite, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	callErr := service.port.DeleteMessage(ctx, input)
	if err := service.finishMessageAudit(ctx, operation, caller, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	return MessageWriteAccess{Status: "deleted", Deleted: &MessageDeleteResult{
		ConversationID: input.ConversationID, MessageID: input.MessageID,
		Provenance: service.itemProvenance(input.ConversationID, input.MessageID),
	}, Review: service.decorateMessageReview(input.review())}, nil
}

func (service *MessagingService) CommitReact(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input MessageReactionInput
	operation, err := service.committedMessageWrite(ctx, token, caller, "message.react", domain.EffectExternalWrite, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	reaction, callErr := service.port.SetMessageReaction(ctx, input)
	if callErr == nil {
		callErr = reaction.Validate()
	}
	if callErr == nil && (reaction.Name != input.Reaction ||
		reaction.ReactedByActor != !input.Remove) {
		callErr = errors.Join(
			ErrWriteOutcomeUnknown,
			errors.New("messaging provider returned a mismatched reaction result"),
		)
	}
	if err := service.finishMessageAudit(ctx, operation, caller, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	return MessageWriteAccess{Status: "reacted", Reaction: &reaction, Review: service.decorateMessageReview(input.review())}, nil
}

func (service *MessagingService) CommitCreateConversation(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input ConversationCreateInput
	operation, err := service.committedMessageWrite(ctx, token, caller, "message.create_conversation", domain.EffectExternalWrite, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	conversation, callErr := service.port.CreateConversation(ctx, input)
	if callErr == nil {
		conversation.Provenance = service.itemProvenance(conversation.ID, conversation.ID)
		callErr = conversation.Validate()
	}
	expectedContainerID := input.ContainerID
	if input.Kind == ConversationChannel && expectedContainerID == "" {
		expectedContainerID = service.provenance.WorkspaceID
	}
	if callErr == nil && (conversation.ContainerID != expectedContainerID ||
		conversation.Kind != input.Kind || conversation.Visibility != input.Visibility ||
		conversation.Name != input.Name || conversation.Topic != input.Topic) {
		callErr = errors.Join(
			ErrWriteOutcomeUnknown,
			errors.New("messaging provider returned a mismatched conversation result"),
		)
	}
	if err := service.finishMessageAudit(ctx, operation, caller, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	return MessageWriteAccess{Status: "created", Conversation: &conversation, Review: service.decorateMessageReview(input.review())}, nil
}

func (service *MessagingService) CommitMembership(ctx context.Context, token string, caller domain.Caller) (MessageWriteAccess, error) {
	var input ConversationMembershipInput
	operation, err := service.committedMessageWrite(ctx, token, caller, "message.membership", domain.EffectExternalWrite, &input)
	if err != nil {
		return MessageWriteAccess{}, err
	}
	result, callErr := service.port.ChangeConversationMembership(ctx, input)
	if callErr == nil {
		result.Provenance = service.itemProvenance(input.ConversationID, input.ConversationID)
		if result.ConversationID != input.ConversationID || result.Action != input.Action || result.Member.ID != input.Member.ID || result.Version == "" {
			callErr = errors.Join(ErrWriteOutcomeUnknown, errors.New("messaging provider returned a mismatched membership result"))
		} else if err := result.Member.Validate(); err != nil {
			callErr = errors.Join(ErrWriteOutcomeUnknown, err)
		}
	}
	if err := service.finishMessageAudit(ctx, operation, caller, callErr); err != nil {
		return MessageWriteAccess{}, err
	}
	return MessageWriteAccess{Status: "membership_changed", Membership: &result, Review: service.decorateMessageReview(input.review())}, nil
}

func (service *MessagingService) committedMessageWrite(ctx context.Context, token string, caller domain.Caller, name string, effect domain.Effect, destination any) (domain.Operation, error) {
	operation, err := service.guard.CommitFor(ctx, token, caller, name, effect)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := operation.DecodePayload(destination); err != nil {
		return domain.Operation{}, err
	}
	var route MessageWriteRoute
	var action string
	switch input := destination.(type) {
	case *MessageEditInput:
		err, route, action = input.Validate(), input.MessageWriteRoute, "edit"
	case *MessageDeleteInput:
		err, route, action = input.Validate(), input.MessageWriteRoute, "delete"
	case *MessageReactionInput:
		err, route, action = input.Validate(), input.MessageWriteRoute, "react"
	case *ConversationCreateInput:
		err, route, action = input.Validate(), input.MessageWriteRoute, "create_conversation"
	case *ConversationMembershipInput:
		err, route, action = input.Validate(), input.MessageWriteRoute, "membership"
	default:
		return domain.Operation{}, errors.New("unsupported messaging write payload")
	}
	if err != nil {
		return domain.Operation{}, err
	}
	if operation.Account() != route.Account {
		return domain.Operation{}, errors.New("messaging operation account does not match its payload")
	}
	return operation, service.validateWriteRoute(route, action)
}

func (service *MessagingService) validateWriteRoute(route MessageWriteRoute, action string) error {
	if err := route.Validate(); err != nil {
		return err
	}
	if err := service.validateRoute(route.Account, route.WorkspaceID); err != nil {
		return err
	}
	if route.Actor.ID != service.provenance.Actor.ID || route.Actor.Mode != service.provenance.Actor.Mode {
		return errors.New("messaging operation actor does not match the routed service")
	}
	supported := map[string]bool{
		"send": service.capabilities.Send, "reply": service.capabilities.Reply,
		"edit": service.capabilities.Edit, "delete": service.capabilities.Delete,
		"react":               service.capabilities.Reactions,
		"create_conversation": service.capabilities.CreateConversation,
		"membership":          service.capabilities.Membership,
	}
	if !supported[action] {
		return fmt.Errorf("the selected messaging route does not support %s", action)
	}
	return nil
}

func (service *MessagingService) finishMessageWrite(ctx context.Context, operation domain.Operation, caller domain.Caller, conversationID, messageID string, message *Message, callErr error) error {
	if callErr == nil {
		service.normalizeMessage(conversationID, &message.Summary)
		if message.Summary.ConversationID != conversationID || messageID != "" && message.Summary.ID != messageID {
			callErr = errors.Join(ErrWriteOutcomeUnknown, errors.New("messaging provider returned a different message"))
		} else if err := message.Validate(); err != nil {
			callErr = errors.Join(ErrWriteOutcomeUnknown, fmt.Errorf("validate message write result: %w", err))
		}
	}
	return service.finishMessageAudit(ctx, operation, caller, callErr)
}

func (service *MessagingService) finishMessageAudit(ctx context.Context, operation domain.Operation, caller domain.Caller, callErr error) error {
	outcome, reason := writeAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	return errors.Join(callErr, auditErr)
}

func (service *MessagingService) decorateMessageReview(review MessageWriteReview) MessageWriteReview {
	review.Account = service.provenance.AccountID
	review.Provider = service.provenance.Provider
	review.Route = service.provenance.Route
	review.WorkspaceID = service.provenance.WorkspaceID
	review.Actor = service.provenance.Actor
	review.Capabilities = service.capabilities
	review.Degradations = append([]domain.Degradation(nil), service.degradations...)
	return review
}

func validateMessageWriteIdentity(route MessageWriteRoute, conversationID, messageID, version string) error {
	if err := route.Validate(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"conversation ID": conversationID, "message ID": messageID, "message version": version,
	} {
		if err := validateOpaqueValue(name, value); err != nil {
			return err
		}
	}
	return nil
}

func (input MessageSendInput) review(action string) MessageWriteReview {
	attachments := make([]MessageAttachmentReview, 0, len(input.Attachments))
	for _, attachment := range input.Attachments {
		digest := sha256.Sum256(attachment.Data)
		attachments = append(attachments, MessageAttachmentReview{
			Name: attachment.Name, MediaType: attachment.MediaType, Bytes: len(attachment.Data),
			SHA256: hex.EncodeToString(digest[:]),
		})
	}
	return MessageWriteReview{
		Action: action, ConversationID: input.ConversationID, ReplyToID: input.ReplyToID,
		Content: messageTextReview(input.Content), Mentions: append([]MessageMention(nil), input.Mentions...),
		Attachments: attachments,
	}
}

func (input MessageEditInput) review() MessageWriteReview {
	return MessageWriteReview{
		Action: "edit", ConversationID: input.ConversationID, MessageID: input.MessageID,
		ThreadRootID: input.ThreadRootID, Version: input.Version, Content: messageTextReview(input.Content),
		Mentions: append([]MessageMention(nil), input.Mentions...),
	}
}

func (input MessageDeleteInput) review() MessageWriteReview {
	return MessageWriteReview{Action: "delete", ConversationID: input.ConversationID, ThreadRootID: input.ThreadRootID, MessageID: input.MessageID, Version: input.Version}
}

func (input MessageReactionInput) review() MessageWriteReview {
	return MessageWriteReview{
		Action: "react", ConversationID: input.ConversationID, MessageID: input.MessageID,
		ThreadRootID: input.ThreadRootID, Version: input.Version,
		Reaction: input.Reaction, RemoveReaction: input.Remove,
	}
}

func (input ConversationCreateInput) review() MessageWriteReview {
	return MessageWriteReview{
		Action: "create_conversation", ContainerID: input.ContainerID,
		Kind: input.Kind, Visibility: input.Visibility, Name: input.Name, Topic: input.Topic,
		Members: append([]ConversationMemberInput(nil), input.Members...),
	}
}

func (input ConversationMembershipInput) review() MessageWriteReview {
	cloned := input
	return MessageWriteReview{
		Action: "membership", ConversationID: input.ConversationID, Version: input.Version,
		Membership: &cloned,
	}
}

func messageTextReview(content MessageContent) *MessageTextReview {
	digest := sha256.Sum256([]byte(content.Text))
	return &MessageTextReview{
		Format: content.Format, Preview: prefixRunes(content.Text, messagePreviewRunes),
		Bytes: len(content.Text), SHA256: hex.EncodeToString(digest[:]),
	}
}

func workspaceTarget(workspaceID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetWorkspace, ID: workspaceID}
}

func conversationTarget(workspaceID, conversationID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetConversation, ID: joinTargetIDs(workspaceID, conversationID)}
}

func messageTarget(workspaceID, conversationID, messageID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetMessage, ID: joinTargetIDs(workspaceID, conversationID, messageID)}
}

func joinTargetIDs(values ...string) string {
	var result strings.Builder
	result.Grow(len(values) * 8)
	for _, value := range values {
		result.WriteString(strconv.Itoa(len(value)))
		result.WriteByte(':')
		result.WriteString(value)
	}
	return result.String()
}
