package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const testMessageAccount = domain.AccountID("acc_11111111111111111111111111111111")

type fakeMessagingPort struct {
	conversations ConversationPage
	page          MessagePage
	message       Message
	attachment    MessageAttachmentContent
	changes       MessageChangePage
	reaction      MessageReaction
	conversation  Conversation
	membership    ConversationMembershipResult
	err           error

	listConversationCalls int
	listMessageCalls      int
	searchCalls           int
	getCalls              int
	attachmentCalls       int
	syncCalls             int
	sendCalls             int
	editCalls             int
	deleteCalls           int
	reactionCalls         int
	createCalls           int
	membershipCalls       int

	sendInput MessageSendInput
}

func (port *fakeMessagingPort) ListConversations(context.Context, ConversationListInput) (ConversationPage, error) {
	port.listConversationCalls++
	return port.conversations, port.err
}

func (port *fakeMessagingPort) ListMessages(context.Context, MessageListInput) (MessagePage, error) {
	port.listMessageCalls++
	return port.page, port.err
}

func (port *fakeMessagingPort) SearchMessages(context.Context, MessageSearchInput) (MessagePage, error) {
	port.searchCalls++
	return port.page, port.err
}

func (port *fakeMessagingPort) GetMessage(context.Context, MessageGetInput) (Message, error) {
	port.getCalls++
	return port.message, port.err
}

func (port *fakeMessagingPort) GetMessageAttachment(context.Context, MessageAttachmentGetInput) (MessageAttachmentContent, error) {
	port.attachmentCalls++
	return port.attachment, port.err
}

func (port *fakeMessagingPort) SyncMessages(context.Context, MessageSyncInput) (MessageChangePage, error) {
	port.syncCalls++
	return port.changes, port.err
}

func (port *fakeMessagingPort) SendMessage(_ context.Context, input MessageSendInput) (Message, error) {
	port.sendCalls++
	port.sendInput = input
	message := port.message
	if message.Summary.ID == "" {
		message = validMessage("message-created", input.ConversationID)
	}
	return message, port.err
}

func (port *fakeMessagingPort) EditMessage(_ context.Context, input MessageEditInput) (Message, error) {
	port.editCalls++
	message := port.message
	if message.Summary.ID == "" {
		message = validMessage(input.MessageID, input.ConversationID)
	}
	return message, port.err
}

func (port *fakeMessagingPort) DeleteMessage(context.Context, MessageDeleteInput) error {
	port.deleteCalls++
	return port.err
}

func (port *fakeMessagingPort) SetMessageReaction(context.Context, MessageReactionInput) (MessageReaction, error) {
	port.reactionCalls++
	return port.reaction, port.err
}

func (port *fakeMessagingPort) CreateConversation(context.Context, ConversationCreateInput) (Conversation, error) {
	port.createCalls++
	return port.conversation, port.err
}

func (port *fakeMessagingPort) ChangeConversationMembership(context.Context, ConversationMembershipInput) (ConversationMembershipResult, error) {
	port.membershipCalls++
	return port.membership, port.err
}

func testMessageProvenance() MessagingProvenance {
	return MessagingProvenance{
		AccountID: testMessageAccount, Provider: domain.MessagingProviderSlack,
		Route: MessagingRouteSlackAPI, WorkspaceID: "workspace-1",
		Actor: MessageActor{ID: "actor-1", Mode: MessageActorApp, DisplayName: "Synthetic app"},
	}
}

func testMessageCapabilities() MessageCapabilities {
	return MessageCapabilities{
		ListConversations: true, History: true, SensitiveRead: true, Search: true,
		IncrementalSync: true, Send: true, Reply: true, Edit: true, Delete: true,
		Reactions: true, AttachmentReads: true, AttachmentWrites: true,
		CreateConversation: true, Membership: true, ActorMode: MessageActorApp,
	}
}

func testMessagingService(t *testing.T, port MessagingPort, rules policy.Rules) (*MessagingService, *memoryAudit) {
	t.Helper()
	guard, audit := newTestGuard(t, rules)
	service, err := NewMessagingService(guard, port, MessagingOptions{
		Provenance: testMessageProvenance(), Capabilities: testMessageCapabilities(),
		Degradations: []domain.Degradation{{
			Feature: "messages.fixture", Reason: "Synthetic messaging contract route.",
		}},
	})
	if err != nil {
		t.Fatalf("NewMessagingService() error = %v", err)
	}
	return service, audit
}

func validMessageSummary(id, conversationID string) MessageSummary {
	return MessageSummary{
		ID: id, Version: "version-1", ConversationID: conversationID,
		Author:    MessageActor{ID: "author-1", Mode: MessageActorDelegatedUser, DisplayName: "Synthetic user"},
		CreatedAt: "2026-08-14T12:00:00Z", Snippet: "Synthetic message",
	}
}

func validMessage(id, conversationID string) Message {
	return Message{
		Summary: validMessageSummary(id, conversationID),
		Content: MessageContent{Format: MessageFormatPlain, Text: "Synthetic message body"},
	}
}

func validMessageWriteRoute() MessageWriteRoute {
	provenance := testMessageProvenance()
	return MessageWriteRoute{Account: provenance.AccountID, WorkspaceID: provenance.WorkspaceID, Actor: provenance.Actor}
}

func TestMessagingReadsNormalizeAndIsolateProvenance(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 14, 12, 1, 0, 0, time.UTC)
	port := &fakeMessagingPort{
		conversations: ConversationPage{Conversations: []Conversation{{
			ID: "conversation-1", Version: "version-1", Kind: ConversationChannel, Name: "Synthetic",
		}}, ObservedAt: now},
		page: MessagePage{Messages: []MessageSummary{validMessageSummary("message-1", "conversation-1")}, ObservedAt: now},
	}
	service, audit := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}

	conversations, err := service.ListConversations(t.Context(), ConversationListInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", Limit: 25,
	}, caller)
	if err != nil {
		t.Fatalf("ListConversations() error = %v", err)
	}
	messages, err := service.ListMessages(t.Context(), MessageListInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 25,
	}, caller)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(conversations.Conversations) != 1 || len(messages.Messages) != 1 {
		t.Fatalf("unexpected pages: conversations=%+v messages=%+v", conversations, messages)
	}
	got := messages.Messages[0].Provenance
	if got.AccountID != testMessageAccount || got.Provider != domain.MessagingProviderSlack ||
		got.Route != MessagingRouteSlackAPI || got.WorkspaceID != "workspace-1" ||
		got.Actor.ID != "actor-1" || got.ConversationID != "conversation-1" || got.SourceObjectID != "message-1" {
		t.Fatalf("normalized provenance = %+v", got)
	}
	if len(messages.Degradations) != 1 || port.listConversationCalls != 1 || port.listMessageCalls != 1 || len(audit.events) != 4 {
		t.Fatalf("unexpected route results: page=%+v port=%+v audit=%+v", messages, port, audit.events)
	}
}

func TestMessagingRejectsCrossAccountWorkspaceAndProviderLeakage(t *testing.T) {
	t.Parallel()

	port := &fakeMessagingPort{page: MessagePage{
		Messages:   []MessageSummary{validMessageSummary("message-1", "escaped-conversation")},
		ObservedAt: time.Now().UTC(),
	}}
	service, _ := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}

	inputs := []MessageListInput{
		{Account: "acc_22222222222222222222222222222222", WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 10},
		{Account: testMessageAccount, WorkspaceID: "workspace-2", ConversationID: "conversation-1", Limit: 10},
	}
	for _, input := range inputs {
		if _, err := service.ListMessages(t.Context(), input, caller); err == nil {
			t.Fatalf("ListMessages(%+v) unexpectedly succeeded", input)
		}
	}
	if _, err := service.ListMessages(t.Context(), MessageListInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 10,
	}, caller); err == nil || !strings.Contains(err.Error(), "another conversation") {
		t.Fatalf("provider leakage error = %v", err)
	}
	if port.listMessageCalls != 1 {
		t.Fatalf("provider calls = %d, want only the routed request", port.listMessageCalls)
	}
}

func TestMessagingSensitiveReadsUseExactPreviewAndCommit(t *testing.T) {
	t.Parallel()

	port := &fakeMessagingPort{
		message: validMessage("message-1", "conversation-1"),
		attachment: MessageAttachmentContent{
			Metadata: MessageAttachment{ID: "attachment-1", Name: "fixture.txt", MediaType: "text/plain", Size: 7, Downloadable: true},
			Data:     []byte("fixture"),
		},
	}
	rules := policy.DefaultRules()
	rules.PreviewSensitiveReads = true
	service, _ := testMessagingService(t, port, rules)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}

	preview, err := service.GetMessage(t.Context(), MessageGetInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", ConversationID: "conversation-1", MessageID: "message-1",
	}, caller)
	if err != nil || preview.Preview == nil || preview.Message != nil || port.getCalls != 0 {
		t.Fatalf("GetMessage() preview = %+v err=%v calls=%d", preview, err, port.getCalls)
	}
	if _, err := service.CommitGetMessage(t.Context(), preview.Preview.Token,
		domain.Caller{Surface: "mcp", Instance: "session-2"}); err == nil {
		t.Fatal("another MCP caller consumed a sensitive-read preview")
	}
	access, err := service.CommitGetMessage(t.Context(), preview.Preview.Token, caller)
	if err != nil || access.Message == nil || access.Message.Summary.Provenance.SourceObjectID != "message-1" || port.getCalls != 1 {
		t.Fatalf("CommitGetMessage() = %+v err=%v calls=%d", access, err, port.getCalls)
	}

	attachmentPreview, err := service.GetAttachment(t.Context(), MessageAttachmentGetInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", ConversationID: "conversation-1",
		MessageID: "message-1", AttachmentID: "attachment-1",
	}, caller)
	if err != nil || attachmentPreview.Preview == nil || port.attachmentCalls != 0 {
		t.Fatalf("GetAttachment() preview = %+v err=%v", attachmentPreview, err)
	}
	attachmentAccess, err := service.CommitGetAttachment(t.Context(), attachmentPreview.Preview.Token, caller)
	if err != nil || attachmentAccess.Attachment == nil || string(attachmentAccess.Attachment.Data) != "fixture" {
		t.Fatalf("CommitGetAttachment() = %+v err=%v", attachmentAccess, err)
	}
}

func TestMessageSendBindsActorContentAndAttachments(t *testing.T) {
	t.Parallel()

	port := &fakeMessagingPort{}
	service, audit := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	input := MessageSendInput{
		MessageWriteRoute: validMessageWriteRoute(), ConversationID: "conversation-1",
		Content:     MessageContent{Format: MessageFormatMarkdown, Text: "Original **body**"},
		Attachments: []MessageAttachmentUpload{{Name: "fixture.txt", MediaType: "text/plain", Data: []byte("VERYSECRET")}},
	}
	preview, err := service.Send(t.Context(), input, caller)
	if err != nil || preview.Preview == nil || port.sendCalls != 0 {
		t.Fatalf("Send() preview = %+v err=%v calls=%d", preview, err, port.sendCalls)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "VERYSECRET") || !strings.Contains(string(encoded), "Original **body**") ||
		len(preview.Review.Attachments) != 1 || len(preview.Review.Attachments[0].SHA256) != 64 {
		t.Fatalf("unsafe or incomplete send review: %s", encoded)
	}
	input.Content.Text = "Mutated body"
	input.Attachments[0].Data[0] = 'X'
	committed, err := service.CommitSend(t.Context(), preview.Preview.Token, caller)
	if err != nil || committed.Message == nil || port.sendCalls != 1 {
		t.Fatalf("CommitSend() = %+v err=%v calls=%d", committed, err, port.sendCalls)
	}
	if port.sendInput.Content.Text != "Original **body**" || string(port.sendInput.Attachments[0].Data) != "VERYSECRET" {
		t.Fatalf("preview payload was not snapshotted: %+v", port.sendInput)
	}
	if len(audit.events) != 3 || audit.events[2].Outcome != AuditOutcomeSuccess {
		t.Fatalf("write audit = %+v", audit.events)
	}
}

func TestMessageWritesRequireCapabilitiesAndFreshPreview(t *testing.T) {
	t.Parallel()

	port := &fakeMessagingPort{err: ErrWriteOutcomeUnknown}
	service, audit := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	input := MessageDeleteInput{
		MessageWriteRoute: validMessageWriteRoute(), ConversationID: "conversation-1",
		MessageID: "message-1", Version: "version-1",
	}
	preview, err := service.Delete(t.Context(), input, caller)
	if err != nil || preview.Preview == nil {
		t.Fatalf("Delete() = %+v err=%v", preview, err)
	}
	if _, err := service.CommitDelete(t.Context(), preview.Preview.Token, caller); !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitDelete() error = %v, want outcome unknown", err)
	}
	if _, err := service.CommitDelete(t.Context(), preview.Preview.Token, caller); err == nil {
		t.Fatal("message delete preview was reusable")
	}
	if port.deleteCalls != 1 || audit.events[len(audit.events)-1].Outcome != AuditOutcomeUnknown {
		t.Fatalf("delete calls/audit = %d %+v", port.deleteCalls, audit.events)
	}
}

func TestMessagingSyncBindsCursorAndRejectsDuplicates(t *testing.T) {
	t.Parallel()

	provenance := testMessageProvenance()
	cursor := MessageCursor{
		Version: 1, Account: provenance.AccountID, Provider: provenance.Provider,
		Route: provenance.Route, WorkspaceID: provenance.WorkspaceID,
		ConversationID: "conversation-1", Opaque: "cursor-1",
	}
	port := &fakeMessagingPort{changes: MessageChangePage{
		Changes: []MessageChange{{Kind: MessageChangeUpsert, Message: ptrMessageSummary(validMessageSummary("message-1", "conversation-1"))}},
		Cursor:  cursor, ObservedAt: time.Now().UTC(),
	}}
	service, _ := testMessagingService(t, port, policy.DefaultRules())
	input := MessageSyncInput{
		Account: testMessageAccount, WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 10,
	}
	page, err := service.SyncMessages(t.Context(), input, domain.Caller{Surface: "cli", Instance: "process-1"})
	if err != nil || len(page.Changes) != 1 || page.Changes[0].Message.Provenance.SourceObjectID != "message-1" {
		t.Fatalf("SyncMessages() = %+v err=%v", page, err)
	}
	port.changes.Changes = append(port.changes.Changes, port.changes.Changes[0])
	if _, err := service.SyncMessages(t.Context(), input, domain.Caller{Surface: "cli", Instance: "process-1"}); err == nil {
		t.Fatal("duplicate message changes were accepted")
	}
}

func TestMessagingModelBoundsAndClosedValues(t *testing.T) {
	t.Parallel()

	invalid := []error{
		(MessageContent{Format: "provider", Text: "body"}).Validate(),
		(MessageContent{Format: MessageFormatPlain, Text: strings.Repeat("x", MaxMessageTextBytes+1)}).Validate(),
		(MessageLink{URL: "javascript:alert(1)"}).Validate(),
		(MessageAttachmentUpload{Name: "fixture", Data: make([]byte, MaxMessageAttachmentBytes+1)}).Validate(),
		(MessageCapabilities{ListConversations: true, History: true, Reply: true, ActorMode: MessageActorApp}).Validate(),
		(ConversationCreateInput{MessageWriteRoute: validMessageWriteRoute(), Kind: ConversationMeeting,
			Members: []ConversationMemberInput{{ID: "member-1", Role: ConversationMember}}}).Validate(),
	}
	for index, err := range invalid {
		if err == nil {
			t.Fatalf("invalid messaging case %d unexpectedly succeeded", index)
		}
	}
}

func ptrMessageSummary(value MessageSummary) *MessageSummary {
	return &value
}
