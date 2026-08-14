package browser

import (
	"strconv"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

func TestMapTeamsObservationPreservesObservedControls(t *testing.T) {
	t.Parallel()
	observation, err := mapTeamsObservation(teamsObservationSnapshot{
		State: "ready", WorkspaceID: "tenant-synthetic",
		ActorID: "actor@example.test", DisplayName: "Synthetic Actor",
		List: true, History: true, Search: true, Send: true,
		Edit: true, Delete: true, Reactions: true, Create: true, Membership: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.WorkspaceID != "tenant-synthetic" ||
		observation.Actor.ID != "actor@example.test" ||
		observation.Revision != teamsSemanticRevision ||
		teamscontract.Intersect(observation.Capabilities, false) != teamscontract.FullCohort(false) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestTeamsDeepLinksStayOnProviderOrigin(t *testing.T) {
	t.Parallel()
	chat := teamscontract.Locator{ChatID: "19:chat/with?syntax@thread.v2"}
	chatURL := teamsConversationURL(chat, "tenant-synthetic")
	if !strings.HasPrefix(chatURL, teamsWebOrigin+"/l/chat/") ||
		strings.Contains(chatURL, "chat/with?") {
		t.Fatalf("chat URL = %q", chatURL)
	}
	channel := teamscontract.Locator{TeamID: "team-synthetic", ChannelID: "19:channel@thread.tacv2"}
	channelURL := teamsMessageURL(channel, "tenant-synthetic", "root-synthetic", "message-synthetic")
	if !strings.HasPrefix(channelURL, teamsWebOrigin+"/l/message/") ||
		!strings.Contains(channelURL, "groupId=team-synthetic") ||
		!strings.Contains(channelURL, "parentMessageId=root-synthetic") {
		t.Fatalf("channel URL = %q", channelURL)
	}
}

func TestTeamsSemanticArgumentsAreJSONStringLiterals(t *testing.T) {
	t.Parallel()
	argument := `"); globalThis.corresyncPwned = true; ("`
	expression, err := teamsCallExpression(`value => value`, argument)
	if err != nil {
		t.Fatal(err)
	}
	literal := strings.TrimSuffix(strings.TrimPrefix(expression, `(value => value)(`), `)`)
	decoded, err := strconv.Unquote(literal)
	if err != nil || decoded != argument {
		t.Fatalf("expression did not retain the argument as one JSON string: %q", expression)
	}
}

func TestMapTeamsConversationAndMessageBounds(t *testing.T) {
	t.Parallel()
	conversation, err := mapTeamsConversation(teamsConversationRow{
		ChatID: "19:synthetic@thread.v2", Kind: "group", Visibility: "private",
		Name: "Synthetic", MemberCount: 3, MemberCountKnown: true,
		LastActivityAt: "2026-08-14T10:00:00Z",
	})
	if err != nil || !strings.HasPrefix(conversation.ID, "tgh1_") ||
		!strings.HasPrefix(conversation.Version, "twcv1_") {
		t.Fatalf("conversation = %#v, %v", conversation, err)
	}
	actor := application.MessageActor{
		ID: "actor@example.test", Mode: application.MessageActorDelegatedUser,
	}
	message, err := mapTeamsMessage(teamsMessageRow{
		ID: "message-synthetic", AuthorID: actor.ID, AuthorName: "Actor",
		CreatedAt: "2026-08-14T10:00:00Z", Snippet: "Synthetic",
		Content: "Synthetic body", Format: "plain",
	}, conversation.ID, actor)
	if err != nil || message.Summary.Author != actor ||
		!strings.HasPrefix(message.Summary.Version, "twmv1_") ||
		message.Content.Text != "Synthetic body" {
		t.Fatalf("message = %#v, %v", message, err)
	}
	if _, err := mapTeamsMessage(teamsMessageRow{
		ID: "message-synthetic", CreatedAt: "not-a-time", Format: "plain",
	}, conversation.ID, actor); err == nil {
		t.Fatal("mapTeamsMessage accepted a malformed timestamp")
	}
}

func TestTeamsVisibleCursorIsSnapshotBound(t *testing.T) {
	t.Parallel()
	cursor := encodeTeamsVisibleOffset(2, strings.Repeat("a", 64))
	if offset, err := teamsVisibleOffset(cursor, strings.Repeat("a", 64), 3); err != nil || offset != 2 {
		t.Fatalf("offset = %d, %v", offset, err)
	}
	if _, err := teamsVisibleOffset(cursor, strings.Repeat("b", 64), 3); err == nil {
		t.Fatal("snapshot drift was accepted")
	}
}
