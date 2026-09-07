package application

import (
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func TestMessagingSensitiveReadsRejectWrongWorkspaceBeforePreparation(t *testing.T) {
	for _, preview := range []bool{false, true} {
		rules := policy.DefaultRules()
		rules.PreviewSensitiveReads = preview
		port := &fakeMessagingPort{message: validMessage("message-1", "conversation-1")}
		service, audit := testMessagingService(t, port, rules)
		caller := domain.Caller{Surface: "mcp", Instance: "workspace-boundary"}
		input := MessageGetInput{Account: testMessageAccount, WorkspaceID: "other-workspace", ConversationID: "conversation-1", MessageID: "message-1"}
		if _, err := service.GetMessage(t.Context(), input, caller); err == nil {
			t.Fatal("wrong workspace accepted")
		}
		attachment := MessageAttachmentGetInput{Account: input.Account, WorkspaceID: input.WorkspaceID, ConversationID: input.ConversationID, MessageID: input.MessageID, AttachmentID: "attachment-1"}
		if _, err := service.GetAttachment(t.Context(), attachment, caller); err == nil {
			t.Fatal("wrong attachment workspace accepted")
		}
		if port.getCalls != 0 || port.attachmentCalls != 0 || len(audit.events) != 0 {
			t.Fatal("wrong workspace reached preparation or provider")
		}
	}
}

func TestMessagingMalformedReactionSuccessIsOutcomeUnknown(t *testing.T) {
	port := &fakeMessagingPort{reaction: MessageReaction{Name: "thumbsup", Count: -1, CountKnown: true, ReactedByActor: true}}
	service, _ := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "malformed-write"}
	access, err := service.React(t.Context(), MessageReactionInput{MessageWriteRoute: validMessageWriteRoute(), ConversationID: "conversation-1", MessageID: "message-1", Version: "version-1", Reaction: "thumbsup"}, caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitReact(t.Context(), access.Preview.Token, caller)
	if port.reactionCalls != 1 || !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("error=%v calls=%d", err, port.reactionCalls)
	}
}

func TestMessagingMalformedConversationSuccessIsOutcomeUnknown(t *testing.T) {
	port := &fakeMessagingPort{conversation: Conversation{ID: "", Kind: ConversationGroup, Visibility: ConversationVisibilityPrivate}}
	service, _ := testMessagingService(t, port, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "malformed-write"}
	access, err := service.CreateConversation(t.Context(), ConversationCreateInput{MessageWriteRoute: validMessageWriteRoute(), Kind: ConversationGroup, Visibility: ConversationVisibilityPrivate, Members: []ConversationMemberInput{{ID: "member-1", Role: ConversationMember}}}, caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitCreateConversation(t.Context(), access.Preview.Token, caller)
	if port.createCalls != 1 || !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("error=%v calls=%d", err, port.createCalls)
	}
}
