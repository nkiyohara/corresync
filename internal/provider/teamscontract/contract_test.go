package teamscontract

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestCanonicalLocatorsRoundTrip(t *testing.T) {
	t.Parallel()
	chat, err := EncodeChatID("19:synthetic@thread.v2")
	if err != nil {
		t.Fatal(err)
	}
	chatLocator, err := DecodeConversationID(chat)
	if err != nil || !chatLocator.IsChat() || chatLocator.ChatID != "19:synthetic@thread.v2" {
		t.Fatalf("chat locator = %#v, %v", chatLocator, err)
	}
	channel, err := EncodeChannelID("team-synthetic", "19:channel@thread.tacv2")
	if err != nil {
		t.Fatal(err)
	}
	channelLocator, err := DecodeConversationID(channel)
	if err != nil || channelLocator.IsChat() || channelLocator.TeamID != "team-synthetic" ||
		channelLocator.ChannelID != "19:channel@thread.tacv2" {
		t.Fatalf("channel locator = %#v, %v", channelLocator, err)
	}
	team, err := EncodeTeamID("team-synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if decoded, decodeErr := DecodeTeamID(team); decodeErr != nil || decoded != "team-synthetic" {
		t.Fatalf("team locator = %q, %v", decoded, decodeErr)
	}
}

func TestCohortCanOnlyBeNarrowed(t *testing.T) {
	t.Parallel()
	observed := application.MessageCapabilities{
		ListConversations: true, History: true, SensitiveRead: true, Search: true,
		IncrementalSync: true, Send: true, Reply: true, Edit: true, Delete: true,
		Reactions: true, AttachmentReads: true, AttachmentWrites: true,
		CreateConversation: true, Membership: true,
		ActorMode: application.MessageActorDelegatedUser,
	}
	got := Intersect(observed, false)
	if got.IncrementalSync || got.AttachmentReads || got.AttachmentWrites ||
		got.CreateConversation || !got.Send || !got.Membership ||
		got.ActorMode != application.MessageActorDelegatedUser {
		t.Fatalf("intersection = %#v", got)
	}
	readOnly := Intersect(observed, true)
	if readOnly.Send || readOnly.Edit || readOnly.CreateConversation || !readOnly.History {
		t.Fatalf("read-only intersection = %#v", readOnly)
	}
}

func TestReleasedTeamsCompositionAndReactionVocabulary(t *testing.T) {
	t.Parallel()
	if err := ValidateWriteContent(application.MessageContent{
		Format: application.MessageFormatPlain, Text: "Synthetic",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWriteContent(application.MessageContent{
		Format: application.MessageFormatHTML, Text: "<b>Synthetic</b>",
	}); err == nil {
		t.Fatal("ValidateWriteContent accepted caller-supplied HTML")
	}
	if err := ValidateReaction("like"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReaction("custom-synthetic"); err == nil {
		t.Fatal("ValidateReaction accepted a custom reaction")
	}
}

func TestLocatorsRejectNonCanonicalAndControlValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "tgh1_***", "tgc1_dGVhbQ", "tgc1_dGVhbQ.Y2hhbg.extra"} {
		if _, err := DecodeConversationID(value); err == nil {
			t.Fatalf("DecodeConversationID(%q) succeeded", value)
		}
	}
	if _, err := EncodeChatID("chat\nother"); err == nil {
		t.Fatal("EncodeChatID accepted a control character")
	}
}
