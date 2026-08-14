package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type ConversationReader interface {
	ListConversations(context.Context, ConversationListInput) (ConversationPage, error)
}

type MessageReader interface {
	ListMessages(context.Context, MessageListInput) (MessagePage, error)
	SearchMessages(context.Context, MessageSearchInput) (MessagePage, error)
	GetMessage(context.Context, MessageGetInput) (Message, error)
	GetMessageAttachment(context.Context, MessageAttachmentGetInput) (MessageAttachmentContent, error)
	SyncMessages(context.Context, MessageSyncInput) (MessageChangePage, error)
}

// MessagingPort is deliberately closed. A provider can translate these use
// cases but cannot inject an arbitrary action or property mutation.
type MessagingPort interface {
	ConversationReader
	MessageReader
	MessageWriter
}

type MessagingOptions struct {
	Provenance   MessagingProvenance
	Capabilities MessageCapabilities
	Degradations []domain.Degradation
}

// MessagingService owns policy, isolation, result validation, preview/commit,
// and audit. Provider adapters contain translation only.
type MessagingService struct {
	guard        *Guard
	port         MessagingPort
	provenance   MessagingProvenance
	capabilities MessageCapabilities
	degradations []domain.Degradation
}

func NewMessagingService(guard *Guard, port MessagingPort, options MessagingOptions) (*MessagingService, error) {
	if guard == nil {
		return nil, errors.New("messaging guard is required")
	}
	if port == nil {
		return nil, errors.New("messaging port is required")
	}
	if err := options.Provenance.Validate(); err != nil {
		return nil, fmt.Errorf("validate messaging provenance: %w", err)
	}
	if options.Provenance.ConversationID != "" || options.Provenance.SourceObjectID != "" {
		return nil, errors.New("messaging service provenance cannot preselect a conversation or message")
	}
	if err := options.Capabilities.Validate(); err != nil {
		return nil, err
	}
	if options.Capabilities.ActorMode != options.Provenance.Actor.Mode {
		return nil, errors.New("messaging capability actor does not match route provenance")
	}
	if err := validateMessageDegradations(options.Degradations); err != nil {
		return nil, err
	}
	return &MessagingService{
		guard: guard, port: port, provenance: options.Provenance,
		capabilities: options.Capabilities,
		degradations: append([]domain.Degradation(nil), options.Degradations...),
	}, nil
}

func (service *MessagingService) Capabilities() MessageCapabilities {
	return service.capabilities
}

func (service *MessagingService) Degradations() []domain.Degradation {
	return append([]domain.Degradation(nil), service.degradations...)
}

func (service *MessagingService) ListConversations(
	ctx context.Context,
	input ConversationListInput,
	caller domain.Caller,
) (ConversationPage, error) {
	if err := input.Validate(); err != nil {
		return ConversationPage{}, err
	}
	if err := service.validateRoute(input.Account, input.WorkspaceID); err != nil {
		return ConversationPage{}, err
	}
	operation, err := domain.NewOperation("message.conversations", domain.EffectRead, input.Account, input)
	if err != nil {
		return ConversationPage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return ConversationPage{}, err
	}
	page, callErr := service.port.ListConversations(ctx, input)
	if callErr == nil {
		callErr = service.normalizeConversationPage(input, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *MessagingService) ListMessages(
	ctx context.Context,
	input MessageListInput,
	caller domain.Caller,
) (MessagePage, error) {
	if err := input.Validate(); err != nil {
		return MessagePage{}, err
	}
	if err := service.validateRoute(input.Account, input.WorkspaceID); err != nil {
		return MessagePage{}, err
	}
	operation, err := domain.NewOperation("message.list", domain.EffectRead, input.Account, input)
	if err != nil {
		return MessagePage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return MessagePage{}, err
	}
	page, callErr := service.port.ListMessages(ctx, input)
	if callErr == nil {
		callErr = service.normalizeMessagePage(input.ConversationID, input.Limit, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *MessagingService) SearchMessages(
	ctx context.Context,
	input MessageSearchInput,
	caller domain.Caller,
) (MessagePage, error) {
	if err := input.Validate(); err != nil {
		return MessagePage{}, err
	}
	if err := service.validateRoute(input.Account, input.WorkspaceID); err != nil {
		return MessagePage{}, err
	}
	if !service.capabilities.Search {
		return MessagePage{}, errors.New("the selected messaging route does not support search")
	}
	operation, err := domain.NewOperation("message.search", domain.EffectRead, input.Account, input)
	if err != nil {
		return MessagePage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return MessagePage{}, err
	}
	page, callErr := service.port.SearchMessages(ctx, input)
	if callErr == nil {
		callErr = service.normalizeMessagePage(input.ConversationID, input.Limit, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

// MessageSensitiveReview exposes only exact routing metadata before a body or
// attachment is released.
type MessageSensitiveReview struct {
	Account        domain.AccountID           `json:"account"`
	Provider       domain.MessagingProviderID `json:"provider"`
	Route          MessagingRouteKind         `json:"route"`
	WorkspaceID    string                     `json:"workspaceId"`
	ConversationID string                     `json:"conversationId"`
	MessageID      string                     `json:"messageId"`
	AttachmentID   string                     `json:"attachmentId,omitempty"`
}

type MessageSensitiveAccess struct {
	Status     string                    `json:"status"`
	Review     MessageSensitiveReview    `json:"review"`
	Preview    *approval.Preview         `json:"preview,omitempty"`
	Message    *Message                  `json:"message,omitempty"`
	Attachment *MessageAttachmentContent `json:"attachment,omitempty"`
}

func (service *MessagingService) GetMessage(
	ctx context.Context,
	input MessageGetInput,
	caller domain.Caller,
) (MessageSensitiveAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageSensitiveAccess{}, err
	}
	if !service.capabilities.SensitiveRead {
		return MessageSensitiveAccess{}, errors.New("the selected messaging route does not support message body reads")
	}
	return service.prepareSensitiveRead(ctx, "message.get", input.Account,
		messageTarget(input.WorkspaceID, input.ConversationID, input.MessageID), input,
		service.sensitiveReview(input.ConversationID, input.MessageID, ""), caller)
}

func (service *MessagingService) GetAttachment(
	ctx context.Context,
	input MessageAttachmentGetInput,
	caller domain.Caller,
) (MessageSensitiveAccess, error) {
	if err := input.Validate(); err != nil {
		return MessageSensitiveAccess{}, err
	}
	if !service.capabilities.AttachmentReads {
		return MessageSensitiveAccess{}, errors.New("the selected messaging route does not support attachment reads")
	}
	return service.prepareSensitiveRead(ctx, "message.get_attachment", input.Account,
		messageTarget(input.WorkspaceID, input.ConversationID, input.MessageID), input,
		service.sensitiveReview(input.ConversationID, input.MessageID, input.AttachmentID), caller)
}

func (service *MessagingService) prepareSensitiveRead(
	ctx context.Context,
	name string,
	account domain.AccountID,
	target domain.TargetRef,
	payload any,
	review MessageSensitiveReview,
	caller domain.Caller,
) (MessageSensitiveAccess, error) {
	if err := service.validateRoute(account, review.WorkspaceID); err != nil {
		return MessageSensitiveAccess{}, err
	}
	operation, err := domain.NewTargetedOperation(name, domain.EffectSensitiveRead, account, target, payload)
	if err != nil {
		return MessageSensitiveAccess{}, err
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MessageSensitiveAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return MessageSensitiveAccess{Status: "approval_required", Review: review, Preview: prepared.Preview}, nil
	case policy.VerdictAllow:
		return service.executeSensitiveRead(ctx, operation, caller, payload, review)
	case policy.VerdictDeny:
		return MessageSensitiveAccess{}, fmt.Errorf("%s operation was denied", name)
	default:
		return MessageSensitiveAccess{}, errors.New("messaging sensitive-read policy returned an unknown verdict")
	}
}

func (service *MessagingService) CommitGetMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MessageSensitiveAccess, error) {
	var input MessageGetInput
	operation, err := service.committedSensitiveRead(ctx, token, caller, "message.get", &input)
	if err != nil {
		return MessageSensitiveAccess{}, err
	}
	return service.executeSensitiveRead(ctx, operation, caller, input,
		service.sensitiveReview(input.ConversationID, input.MessageID, ""))
}

func (service *MessagingService) CommitGetAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MessageSensitiveAccess, error) {
	var input MessageAttachmentGetInput
	operation, err := service.committedSensitiveRead(ctx, token, caller, "message.get_attachment", &input)
	if err != nil {
		return MessageSensitiveAccess{}, err
	}
	return service.executeSensitiveRead(ctx, operation, caller, input,
		service.sensitiveReview(input.ConversationID, input.MessageID, input.AttachmentID))
}

func (service *MessagingService) committedSensitiveRead(
	ctx context.Context,
	token string,
	caller domain.Caller,
	name string,
	destination any,
) (domain.Operation, error) {
	operation, err := service.guard.CommitFor(ctx, token, caller, name, domain.EffectSensitiveRead)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := operation.DecodePayload(destination); err != nil {
		return domain.Operation{}, err
	}
	switch input := destination.(type) {
	case *MessageGetInput:
		err = input.Validate()
	case *MessageAttachmentGetInput:
		err = input.Validate()
	default:
		return domain.Operation{}, errors.New("unsupported messaging sensitive-read payload")
	}
	if err != nil {
		return domain.Operation{}, err
	}
	return operation, service.validateRoute(operation.Account(), service.provenance.WorkspaceID)
}

func (service *MessagingService) executeSensitiveRead(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	payload any,
	review MessageSensitiveReview,
) (MessageSensitiveAccess, error) {
	access := MessageSensitiveAccess{Status: "completed", Review: review}
	var callErr error
	switch input := payload.(type) {
	case MessageGetInput:
		message, err := service.port.GetMessage(ctx, input)
		callErr = err
		if callErr == nil {
			service.normalizeMessage(input.ConversationID, &message.Summary)
			callErr = message.Validate()
		}
		if callErr == nil && message.Summary.ID != input.MessageID {
			callErr = errors.New("messaging provider returned a different message")
		}
		if callErr == nil {
			access.Message = &message
		}
	case MessageAttachmentGetInput:
		attachment, err := service.port.GetMessageAttachment(ctx, input)
		callErr = err
		if callErr == nil {
			callErr = attachment.Validate()
		}
		if callErr == nil && attachment.Metadata.ID != input.AttachmentID {
			callErr = errors.New("messaging provider returned a different attachment")
		}
		if callErr == nil {
			access.Attachment = &attachment
		}
	default:
		callErr = errors.New("unsupported messaging sensitive-read payload")
	}
	auditErr := service.guard.RecordExecution(context.WithoutCancel(ctx), operation, caller, callErr)
	return access, errors.Join(callErr, auditErr)
}

func (service *MessagingService) SyncMessages(
	ctx context.Context,
	input MessageSyncInput,
	caller domain.Caller,
) (MessageChangePage, error) {
	if err := input.Validate(service.provenance); err != nil {
		return MessageChangePage{}, err
	}
	if err := service.validateRoute(input.Account, input.WorkspaceID); err != nil {
		return MessageChangePage{}, err
	}
	if !service.capabilities.IncrementalSync {
		return MessageChangePage{}, errors.New("the selected messaging route does not support incremental sync")
	}
	operation, err := domain.NewOperation("message.sync", domain.EffectRead, input.Account, input)
	if err != nil {
		return MessageChangePage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return MessageChangePage{}, err
	}
	page, callErr := service.port.SyncMessages(ctx, input)
	if callErr == nil {
		callErr = service.normalizeChangePage(input, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *MessagingService) validateRoute(account domain.AccountID, workspaceID string) error {
	if account != service.provenance.AccountID {
		return errors.New("messaging operation account does not match the routed service")
	}
	if workspaceID != service.provenance.WorkspaceID {
		return errors.New("messaging operation workspace does not match the routed service")
	}
	return nil
}

func (service *MessagingService) allowRead(ctx context.Context, operation domain.Operation, caller domain.Caller) error {
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return errors.New("messaging metadata read was not allowed for immediate execution")
	}
	return nil
}

func (service *MessagingService) recordRead(ctx context.Context, operation domain.Operation, caller domain.Caller, callErr error) error {
	return service.guard.RecordExecution(context.WithoutCancel(ctx), operation, caller, callErr)
}

func (service *MessagingService) normalizeConversationPage(input ConversationListInput, page *ConversationPage) error {
	if len(page.Conversations) > input.Limit {
		return errors.New("messaging provider returned too many conversations")
	}
	if err := validateMessagePageEnvelope(page.NextCursor, page.Partial, page.PartialReason, page.ObservedAt, page.Degradations); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(page.Conversations))
	for index := range page.Conversations {
		conversation := &page.Conversations[index]
		conversation.Provenance = service.itemProvenance(conversation.ID, conversation.ID)
		if _, exists := seen[conversation.ID]; exists {
			return errors.New("messaging provider returned a duplicate conversation")
		}
		seen[conversation.ID] = struct{}{}
		if err := conversation.Validate(); err != nil {
			return fmt.Errorf("validate conversation result: %w", err)
		}
	}
	page.Degradations = mergeMessageDegradations(service.degradations, page.Degradations)
	if err := validateMessageDegradations(page.Degradations); err != nil {
		return err
	}
	return validateMessageEncodedSize("conversation page", page)
}

func (service *MessagingService) normalizeMessagePage(conversationID string, limit int, page *MessagePage) error {
	if len(page.Messages) > limit {
		return errors.New("messaging provider returned too many messages")
	}
	if err := validateMessagePageEnvelope(page.NextCursor, page.Partial, page.PartialReason, page.ObservedAt, page.Degradations); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(page.Messages))
	for index := range page.Messages {
		summary := &page.Messages[index]
		if conversationID != "" && summary.ConversationID != conversationID {
			return errors.New("messaging provider returned a message from another conversation")
		}
		service.normalizeMessage(summary.ConversationID, summary)
		key := summary.ConversationID + "\x00" + summary.ID
		if _, exists := seen[key]; exists {
			return errors.New("messaging provider returned a duplicate message")
		}
		seen[key] = struct{}{}
		if err := summary.Validate(); err != nil {
			return fmt.Errorf("validate message result: %w", err)
		}
	}
	page.Degradations = mergeMessageDegradations(service.degradations, page.Degradations)
	if err := validateMessageDegradations(page.Degradations); err != nil {
		return err
	}
	return validateMessageEncodedSize("message page", page)
}

func (service *MessagingService) normalizeMessage(conversationID string, summary *MessageSummary) {
	if summary.ConversationID == "" {
		summary.ConversationID = conversationID
	}
	summary.Provenance = service.itemProvenance(summary.ConversationID, summary.ID)
}

func (service *MessagingService) normalizeChangePage(input MessageSyncInput, page *MessageChangePage) error {
	if len(page.Changes) > input.Limit {
		return errors.New("messaging provider returned too many incremental changes")
	}
	if page.ObservedAt.IsZero() {
		return errors.New("messaging provider omitted sync observation time")
	}
	if err := page.Cursor.Validate(); err != nil {
		return err
	}
	if page.Cursor.Account != input.Account || page.Cursor.Provider != service.provenance.Provider ||
		page.Cursor.Route != service.provenance.Route || page.Cursor.WorkspaceID != input.WorkspaceID ||
		page.Cursor.ConversationID != input.ConversationID {
		return errors.New("messaging provider returned a cursor for another route")
	}
	if err := validateMessageDegradations(page.Degradations); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(page.Changes))
	for index := range page.Changes {
		change := &page.Changes[index]
		var key string
		switch change.Kind {
		case MessageChangeUpsert:
			if change.Message == nil || change.ID != "" {
				return errors.New("message upsert change is malformed")
			}
			service.normalizeMessage(change.Message.ConversationID, change.Message)
			if input.ConversationID != "" && change.Message.ConversationID != input.ConversationID {
				return errors.New("message change escaped the selected conversation")
			}
			if err := change.Message.Validate(); err != nil {
				return err
			}
			key = change.Message.ConversationID + "\x00" + change.Message.ID
		case MessageChangeDelete:
			if change.Message != nil || validateOpaqueValue("deleted message ID", change.ID) != nil {
				return errors.New("message delete change is malformed")
			}
			key = input.ConversationID + "\x00" + change.ID
		default:
			return fmt.Errorf("unsupported message change kind %q", change.Kind)
		}
		if _, exists := seen[key]; exists {
			return errors.New("messaging provider returned duplicate incremental changes")
		}
		seen[key] = struct{}{}
	}
	page.Degradations = mergeMessageDegradations(service.degradations, page.Degradations)
	if err := validateMessageDegradations(page.Degradations); err != nil {
		return err
	}
	return validateMessageEncodedSize("message change page", page)
}

func (service *MessagingService) itemProvenance(conversationID, objectID string) MessagingProvenance {
	provenance := service.provenance
	provenance.ConversationID = conversationID
	provenance.SourceObjectID = objectID
	return provenance
}

func (service *MessagingService) sensitiveReview(conversationID, messageID, attachmentID string) MessageSensitiveReview {
	return MessageSensitiveReview{
		Account: service.provenance.AccountID, Provider: service.provenance.Provider,
		Route: service.provenance.Route, WorkspaceID: service.provenance.WorkspaceID,
		ConversationID: conversationID, MessageID: messageID, AttachmentID: attachmentID,
	}
}

func validateMessagePageEnvelope(cursor string, partial bool, reason string, observedAt time.Time, degradations []domain.Degradation) error {
	if len(cursor) > MaxMessageCursorBytes || observedAt.IsZero() {
		return errors.New("messaging provider returned an invalid page envelope")
	}
	if partial != (reason != "") {
		return errors.New("messaging partial result requires exactly one bounded reason")
	}
	if reason != "" && !slices.Contains([]string{
		"permission_limited", "retention_gap", "provider_limit", "rate_limited", "cursor_reset",
	}, reason) {
		return errors.New("messaging provider returned an unsupported partial reason")
	}
	return validateMessageDegradations(degradations)
}

func validateMessageDegradations(degradations []domain.Degradation) error {
	if len(degradations) > 64 {
		return errors.New("messaging degradations are unbounded")
	}
	for _, degradation := range degradations {
		if err := degradation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func mergeMessageDegradations(route, item []domain.Degradation) []domain.Degradation {
	merged := make([]domain.Degradation, 0, len(route)+len(item))
	seen := make(map[domain.Degradation]struct{}, len(route)+len(item))
	for _, degradation := range append(append([]domain.Degradation(nil), route...), item...) {
		if _, exists := seen[degradation]; !exists {
			merged = append(merged, degradation)
			seen[degradation] = struct{}{}
		}
	}
	return merged
}
