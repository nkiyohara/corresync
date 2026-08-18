package browser

import (
	"net/url"
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

func TestTeamsOriginAndApplicationPathsStayClosed(t *testing.T) {
	t.Parallel()
	browser := &Browser{allowedOrigins: map[string]struct{}{teamsWebOrigin: {}}}
	for _, origin := range []string{
		"https://user@teams.microsoft.com",
		"https://teams.microsoft.com/v2/",
		"https://teams.microsoft.com?tenant=other",
	} {
		if err := browser.validateTeamsOrigin(origin); err == nil {
			t.Fatalf("validateTeamsOrigin(%q) unexpectedly succeeded", origin)
		}
	}
	for _, path := range []string{"/l/call/opaque", "/l/meeting/opaque", "/l/entity/opaque"} {
		if teamsApplicationPath(path) {
			t.Fatalf("teamsApplicationPath(%q) accepted an out-of-scope surface", path)
		}
	}
	if teamsApplicationPath("/v2/calls/opaque") {
		t.Fatal("teamsApplicationPath accepted an arbitrary v2 surface")
	}
	for _, path := range []string{
		"/", "/v2/", "/l/chat/opaque", "/l/channel/opaque",
		"/l/team/opaque", "/l/message/opaque",
	} {
		if !teamsApplicationPath(path) {
			t.Fatalf("teamsApplicationPath(%q) rejected an approved surface", path)
		}
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

func TestTeamsNavigationRetainsEveryReviewedRouteComponent(t *testing.T) {
	t.Parallel()
	expected, err := url.Parse(
		teamsWebOrigin + "/l/message/channel/message?tenantId=tenant&groupId=team&parentMessageId=root",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		teamsWebOrigin + "/l/message/other/message?tenantId=tenant&groupId=team&parentMessageId=root",
		teamsWebOrigin + "/l/message/channel/message?tenantId=other&groupId=team&parentMessageId=root",
		teamsWebOrigin + "/l/message/channel/message?tenantId=tenant&groupId=other&parentMessageId=root",
		teamsWebOrigin + "/l/message/channel/message?tenantId=tenant&groupId=team&parentMessageId=other",
	} {
		actual, parseErr := url.Parse(raw)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if teamsNavigationMatches(expected, actual) {
			t.Fatalf("teamsNavigationMatches accepted %q", raw)
		}
	}
	actual, err := url.Parse(expected.String() + "&providerHint=opaque")
	if err != nil || !teamsNavigationMatches(expected, actual) {
		t.Fatalf("teamsNavigationMatches rejected a route-preserving provider hint: %v", err)
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

func TestTeamsObservationDoesNotTrustNavigationTenantHints(t *testing.T) {
	t.Parallel()
	for _, forbidden := range []string{
		`searchParams.get("tenantId")`, `"[data-tenant-id]"`,
	} {
		if strings.Contains(teamsObservationScript, forbidden) {
			t.Fatalf("Teams observation trusts an ambient tenant hint: %s", forbidden)
		}
	}
	if !strings.Contains(
		teamsObservationScript,
		`[data-tid='tenant-switcher'][data-tenant-id]`,
	) {
		t.Fatal("Teams observation omitted its explicit tenant switcher boundary")
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
		ID: "message-synthetic", ChatID: "19:synthetic@thread.v2",
		AuthorID: actor.ID, AuthorName: "Actor",
		CreatedAt: "2026-08-14T10:00:00Z", Snippet: "Synthetic",
		Content: "Synthetic body", Format: "plain",
	}, conversation.ID, actor)
	if err != nil || message.Summary.Author != actor ||
		!strings.HasPrefix(message.Summary.Version, "twmv1_") ||
		message.Content.Text != "Synthetic body" {
		t.Fatalf("message = %#v, %v", message, err)
	}
	if _, err := mapTeamsMessage(teamsMessageRow{
		ID: "message-synthetic", ChatID: "19:synthetic@thread.v2",
		CreatedAt: "not-a-time", Format: "plain",
	}, conversation.ID, actor); err == nil {
		t.Fatal("mapTeamsMessage accepted a malformed timestamp")
	}
	if _, err := mapTeamsMessage(teamsMessageRow{
		ID: "message-synthetic", ChatID: "19:other@thread.v2",
		AuthorID: actor.ID, CreatedAt: "2026-08-14T10:00:00Z", Format: "plain",
	}, conversation.ID, actor); err == nil {
		t.Fatal("mapTeamsMessage accepted another conversation's row")
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
