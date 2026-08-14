package teamsweb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

const syntheticWebAccount domain.AccountID = "acc_11111111111111111111111111111111"

var _ Driver = (*browser.Browser)(nil)

type fakeDriver struct {
	observation  application.MessageCapabilities
	workspace    string
	actor        application.MessageActor
	nextCursor   string
	conversation application.Conversation
	message      application.Message
	reaction     application.MessageReaction
	membership   application.ConversationMembershipResult
	writeErr     error
	getCalls     int
}

func newFakeDriver(t *testing.T) *fakeDriver {
	t.Helper()
	chatID, err := teamscontract.EncodeChatID("19:synthetic@thread.v2")
	if err != nil {
		t.Fatal(err)
	}
	actor := application.MessageActor{
		ID: "actor@example.test", Mode: application.MessageActorDelegatedUser,
		DisplayName: "Synthetic Actor",
	}
	capabilities := teamscontract.FullCohort(false)
	return &fakeDriver{
		observation: capabilities, workspace: "workspace-synthetic", actor: actor,
		conversation: application.Conversation{
			ID: chatID, Version: "twcv1_synthetic", Kind: application.ConversationGroup,
			Visibility: application.ConversationVisibilityPrivate,
			Name:       "Synthetic conversation", MemberCount: 3, MemberCountKnown: true,
		},
		message: application.Message{
			Summary: application.MessageSummary{
				ID: "1740000000000", Version: "twmv1_synthetic", ConversationID: chatID,
				Author: actor, CreatedAt: "2026-08-14T10:00:00Z", Snippet: "Synthetic message",
			},
			Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Synthetic message"},
		},
		reaction: application.MessageReaction{Name: "like", CountKnown: false, ReactedByActor: true},
		membership: application.ConversationMembershipResult{
			ConversationID: chatID, Version: "twcv1_after", Action: application.MembershipAdd,
			Member: application.ConversationMemberInput{ID: "member@example.test", Role: application.ConversationMember},
		},
	}
}

func (driver *fakeDriver) ObserveTeams(context.Context, string) (teamscontract.Observation, error) {
	return teamscontract.Observation{
		WorkspaceID: driver.workspace, Actor: driver.actor,
		Capabilities: driver.observation, Revision: "synthetic-v1",
	}, nil
}

func (driver *fakeDriver) TeamsListConversations(
	_ context.Context,
	_ application.ConversationListInput,
) (application.ConversationPage, error) {
	return application.ConversationPage{
		Conversations: []application.Conversation{driver.conversation},
		NextCursor:    driver.nextCursor, Partial: true, PartialReason: "provider_limit",
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (driver *fakeDriver) TeamsGetConversation(context.Context, string) (application.Conversation, error) {
	return driver.conversation, nil
}

func (driver *fakeDriver) TeamsListMessages(
	_ context.Context,
	_ application.MessageListInput,
) (application.MessagePage, error) {
	return application.MessagePage{
		Messages:   []application.MessageSummary{driver.message.Summary},
		NextCursor: driver.nextCursor, Partial: true, PartialReason: "provider_limit",
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (driver *fakeDriver) TeamsSearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
) (application.MessagePage, error) {
	return driver.TeamsListMessages(ctx, application.MessageListInput{
		Account: input.Account, WorkspaceID: input.WorkspaceID,
		ConversationID: input.ConversationID, Cursor: input.Cursor, Limit: input.Limit,
	})
}

func (driver *fakeDriver) TeamsGetMessage(
	context.Context,
	application.MessageGetInput,
) (application.Message, error) {
	driver.getCalls++
	return driver.message, nil
}

func (driver *fakeDriver) TeamsSendMessage(
	context.Context,
	application.MessageSendInput,
) (application.Message, error) {
	return driver.message, driver.writeErr
}

func (driver *fakeDriver) TeamsEditMessage(
	context.Context,
	application.MessageEditInput,
) (application.Message, error) {
	return driver.message, driver.writeErr
}

func (driver *fakeDriver) TeamsDeleteMessage(context.Context, application.MessageDeleteInput) error {
	return driver.writeErr
}

func (driver *fakeDriver) TeamsSetReaction(
	context.Context,
	application.MessageReactionInput,
) (application.MessageReaction, error) {
	return driver.reaction, driver.writeErr
}

func (driver *fakeDriver) TeamsCreateConversation(
	context.Context,
	application.ConversationCreateInput,
) (application.Conversation, error) {
	return driver.conversation, driver.writeErr
}

func (driver *fakeDriver) TeamsChangeMembership(
	context.Context,
	application.ConversationMembershipInput,
) (application.ConversationMembershipResult, error) {
	return driver.membership, driver.writeErr
}

func TestTeamsWebClosedDriverCoversReleasedCohort(t *testing.T) {
	t.Parallel()
	driver := newFakeDriver(t)
	driver.nextCursor = "visible-offset-1"
	client, err := New(t.Context(), Options{
		WorkspaceID: "workspace-synthetic", Driver: driver,
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := client.MessageCapabilities()
	if capabilities != teamscontract.FullCohort(false) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	page, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: syntheticWebAccount, WorkspaceID: "workspace-synthetic", Limit: 10,
	})
	if err != nil || len(page.Conversations) != 1 || !strings.HasPrefix(page.NextCursor, "twp1_") {
		t.Fatalf("conversation page = %#v, %v", page, err)
	}
	if _, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account:     "acc_22222222222222222222222222222222",
		WorkspaceID: "workspace-synthetic", Limit: 10, Cursor: page.NextCursor,
	}); err == nil || !strings.Contains(err.Error(), "selected route") {
		t.Fatalf("cross-account cursor error = %v", err)
	}
	messagePage, err := client.ListMessages(t.Context(), application.MessageListInput{
		Account: syntheticWebAccount, WorkspaceID: "workspace-synthetic",
		ConversationID: driver.conversation.ID, Limit: 10,
	})
	if err != nil || len(messagePage.Messages) != 1 ||
		messagePage.Messages[0].ConversationID != driver.conversation.ID {
		t.Fatalf("message page = %#v, %v", messagePage, err)
	}
}

func TestTeamsWebWritesRevalidateAndPreserveUnknownOutcome(t *testing.T) {
	t.Parallel()
	driver := newFakeDriver(t)
	client, err := New(t.Context(), Options{WorkspaceID: driver.workspace, Driver: driver})
	if err != nil {
		t.Fatal(err)
	}
	route := application.MessageWriteRoute{
		Account: syntheticWebAccount, WorkspaceID: driver.workspace, Actor: driver.actor,
	}
	edited, err := client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: route, ConversationID: driver.conversation.ID,
		MessageID: driver.message.Summary.ID, Version: driver.message.Summary.Version,
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Edited"},
	})
	if err != nil || edited.Summary.ID != driver.message.Summary.ID || driver.getCalls != 1 {
		t.Fatalf("edit = %#v, get calls = %d, error = %v", edited, driver.getCalls, err)
	}
	driver.message.Summary.Version = "changed"
	if _, err := client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: route, ConversationID: driver.conversation.ID,
		MessageID: driver.message.Summary.ID, Version: "old",
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Edited"},
	}); !errors.Is(err, restapi.ErrPrecondition) {
		t.Fatalf("stale edit error = %v", err)
	}
	driver.message.Summary.Version = "twmv1_synthetic"
	driver.writeErr = errors.New("synthetic browser disconnect")
	if err := client.DeleteMessage(t.Context(), application.MessageDeleteInput{
		MessageWriteRoute: route, ConversationID: driver.conversation.ID,
		MessageID: driver.message.Summary.ID, Version: driver.message.Summary.Version,
	}); !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("delete error = %v", err)
	}
}

func TestTeamsWebRejectsRouteDriftAndNarrowsCapabilities(t *testing.T) {
	t.Parallel()
	driver := newFakeDriver(t)
	driver.observation.Delete = false
	client, err := New(t.Context(), Options{
		WorkspaceID: driver.workspace, ReadOnly: true, Driver: driver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.MessageCapabilities().Send || client.MessageCapabilities().Delete ||
		!client.MessageCapabilities().History {
		t.Fatalf("read-only capabilities = %#v", client.MessageCapabilities())
	}
	if _, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: syntheticWebAccount, WorkspaceID: "another-workspace", Limit: 10,
	}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("workspace drift error = %v", err)
	}
	driver.workspace = "another-workspace"
	if _, err := New(t.Context(), Options{
		WorkspaceID: "workspace-synthetic", Driver: driver,
	}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("identity drift error = %v", err)
	}
}

func TestTeamsWebRejectsNonProviderOrigin(t *testing.T) {
	t.Parallel()
	if _, err := New(t.Context(), Options{
		Origin: "https://example.test", WorkspaceID: "workspace-synthetic",
		Driver: newFakeDriver(t),
	}); err == nil {
		t.Fatal("New() accepted a non-provider origin")
	}
}
