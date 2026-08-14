package mattermostapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	fixtureAccountID = "acc_11111111111111111111111111111111"
	fixtureTeamID    = "teamid00000000000000000000"
	fixtureActorID   = "actorid0000000000000000000"
	fixtureChannelID = "channelid00000000000000000"
	fixtureDirectID  = "directid000000000000000000"
	fixturePostID    = "postid00000000000000000000"
	fixtureRootID    = "rootid00000000000000000000"
	fixtureFileID    = "fileid00000000000000000000"
	fixtureMemberID  = "memberid000000000000000000"
)

type fixtureRoundTripper struct {
	mu       sync.Mutex
	requests []*http.Request
	handle   func(*http.Request, []byte) (int, http.Header, []byte)
}

func (transport *fixtureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.mu.Unlock()
	status, header, responseBody := transport.handle(request, body)
	if header == nil {
		header = make(http.Header)
	}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(responseBody)),
		Request: request,
	}, nil
}

func fixtureJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func fixtureIdentity(request *http.Request) (int, []byte, bool) {
	switch request.URL.Path {
	case "/api/v4/users/me":
		return http.StatusOK, fixtureJSON(mattermostUser{
			ID: fixtureActorID, Username: "synthetic", FirstName: "Synthetic", LastName: "Actor",
			Roles: "system_user",
		}), true
	case "/api/v4/users/me/teams":
		return http.StatusOK, fixtureJSON([]mattermostTeam{{ID: fixtureTeamID}}), true
	case "/api/v4/teams/" + fixtureTeamID + "/members/" + fixtureActorID:
		return http.StatusOK, fixtureJSON(mattermostTeamMember{
			TeamID: fixtureTeamID, UserID: fixtureActorID, Roles: "team_user",
		}), true
	case "/api/v4/users/" + fixtureActorID + "/teams/" + fixtureTeamID + "/channels/members":
		return http.StatusOK, fixtureJSON([]mattermostChannelMember{{
			ChannelID: fixtureChannelID, UserID: fixtureActorID, Roles: "channel_user",
		}}), true
	case "/api/v4/roles/names":
		return http.StatusOK, fixtureJSON([]mattermostRole{
			{ID: "roleid00000000000000000000", Name: "system_user"},
			{
				ID: "roleid00000000000000000001", Name: "team_user",
				Permissions: []string{"create_public_channel", "create_private_channel"},
			},
			{
				ID: "roleid00000000000000000002", Name: "channel_user",
				Permissions: []string{
					"read_channel", "create_post", "edit_post", "delete_post",
					"add_reaction", "remove_reaction", "manage_public_channel_members",
				},
			},
		}), true
	default:
		return 0, nil, false
	}
}

func fixtureChannel() mattermostChannel {
	return mattermostChannel{
		ID: fixtureChannelID, CreateAt: 1_700_000_000_000, UpdateAt: 1_700_000_000_100,
		TeamID: fixtureTeamID, Type: "O", Name: "general", DisplayName: "General",
		Header: "Synthetic discussion", LastPostAt: 1_700_000_000_200,
	}
}

func fixturePost() mattermostPost {
	return mattermostPost{
		ID: fixturePostID, CreateAt: 1_700_000_000_200, UpdateAt: 1_700_000_000_200,
		UserID: fixtureActorID, ChannelID: fixtureChannelID,
		Message: "Synthetic **message**", FileIDs: []string{fixtureFileID},
	}
}

func newFixtureClient(t *testing.T, readOnly bool, handler func(*http.Request, []byte) (int, http.Header, []byte)) *Client {
	t.Helper()
	transport := &fixtureRoundTripper{handle: func(request *http.Request, body []byte) (int, http.Header, []byte) {
		if status, response, ok := fixtureIdentity(request); ok {
			return status, nil, response
		}
		return handler(request, body)
	}}
	client, err := newWithHTTP(t.Context(), Options{
		Origin: "https://chat.example.test", WorkspaceID: fixtureTeamID, ReadOnly: readOnly,
	}, &http.Client{Transport: transport}, nil)
	if err != nil {
		t.Fatalf("newWithHTTP() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestMattermostReadsRemainBoundToSelectedTeamAndContent(t *testing.T) {
	channel := fixtureChannel()
	direct := mattermostChannel{
		ID: fixtureDirectID, CreateAt: channel.CreateAt, UpdateAt: channel.UpdateAt,
		Type: "D", DisplayName: "Direct", LastPostAt: channel.LastPostAt,
	}
	post := fixturePost()
	files := []mattermostFileInfo{{
		ID: fixtureFileID, PostID: fixturePostID, Name: "note.txt", MimeType: "text/plain", Size: 4,
	}}
	reactions := []mattermostReaction{{
		UserID: fixtureActorID, PostID: fixturePostID, EmojiName: "thumbsup",
	}}
	client := newFixtureClient(t, false, func(request *http.Request, _ []byte) (int, http.Header, []byte) {
		switch request.URL.Path {
		case "/api/v4/users/me/channels":
			return http.StatusOK, nil, fixtureJSON([]mattermostChannel{channel, direct})
		case "/api/v4/channels/" + fixtureChannelID:
			return http.StatusOK, nil, fixtureJSON(channel)
		case "/api/v4/channels/" + fixtureChannelID + "/posts":
			return http.StatusOK, nil, fixtureJSON(mattermostPostList{
				Order: []string{fixturePostID}, Posts: map[string]mattermostPost{fixturePostID: post},
			})
		case "/api/v4/posts/" + fixturePostID:
			return http.StatusOK, nil, fixtureJSON(post)
		case "/api/v4/posts/" + fixturePostID + "/files/info":
			return http.StatusOK, nil, fixtureJSON(files)
		case "/api/v4/posts/" + fixturePostID + "/reactions":
			return http.StatusOK, nil, fixtureJSON(reactions)
		case "/api/v4/files/" + fixtureFileID:
			return http.StatusOK, http.Header{"Content-Type": {"text/plain"}}, []byte("test")
		case "/api/v4/teams/" + fixtureTeamID + "/posts/search":
			return http.StatusOK, nil, fixtureJSON(mattermostPostList{
				Order: []string{fixturePostID}, Posts: map[string]mattermostPost{fixturePostID: post},
			})
		default:
			return http.StatusNotFound, nil, []byte(`{"id":"not_found"}`)
		}
	})
	if client.MessageActor().ID != fixtureActorID || !client.MessageCapabilities().AttachmentReads {
		t.Fatalf("observed identity/capabilities = %#v, %#v", client.MessageActor(), client.MessageCapabilities())
	}
	conversations, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID, Limit: 10,
	})
	if err != nil || len(conversations.Conversations) != 2 {
		t.Fatalf("ListConversations() = %#v, %v", conversations, err)
	}
	messages, err := client.ListMessages(t.Context(), application.MessageListInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID,
		ConversationID: fixtureChannelID, Limit: 10,
	})
	if err != nil || len(messages.Messages) != 1 || messages.Messages[0].ID != fixturePostID {
		t.Fatalf("ListMessages() = %#v, %v", messages, err)
	}
	message, err := client.GetMessage(t.Context(), application.MessageGetInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID,
		ConversationID: fixtureChannelID, MessageID: fixturePostID,
	})
	if err != nil || message.Content.Text != post.Message || len(message.Attachments) != 1 ||
		len(message.Reactions) != 1 || !message.Reactions[0].ReactedByActor {
		t.Fatalf("GetMessage() = %#v, %v", message, err)
	}
	attachment, err := client.GetMessageAttachment(t.Context(), application.MessageAttachmentGetInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID, ConversationID: fixtureChannelID,
		MessageID: fixturePostID, AttachmentID: fixtureFileID,
	})
	if err != nil || string(attachment.Data) != "test" {
		t.Fatalf("GetMessageAttachment() = %#v, %v", attachment, err)
	}
	search, err := client.SearchMessages(t.Context(), application.MessageSearchInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID, Query: "Synthetic", Limit: 10,
	})
	if err != nil || len(search.Messages) != 1 {
		t.Fatalf("SearchMessages() = %#v, %v", search, err)
	}
	syncPage, err := client.SyncMessages(t.Context(), application.MessageSyncInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID,
		ConversationID: fixtureChannelID, Limit: 10,
	})
	if err != nil || !syncPage.Reset || len(syncPage.Changes) != 1 ||
		syncPage.Cursor.Provider != domain.MessagingProviderMattermost {
		t.Fatalf("SyncMessages() = %#v, %v", syncPage, err)
	}
}

func TestMattermostWritesRevalidateExactActorAndVersion(t *testing.T) {
	channel := fixtureChannel()
	post := fixturePost()
	post.FileIDs = nil
	client := newFixtureClient(t, false, func(request *http.Request, body []byte) (int, http.Header, []byte) {
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v4/channels/" + fixtureChannelID:
			return http.StatusOK, nil, fixtureJSON(channel)
		case "GET /api/v4/posts/" + fixturePostID:
			return http.StatusOK, nil, fixtureJSON(post)
		case "POST /api/v4/posts":
			created := post
			created.Message = "created"
			return http.StatusCreated, nil, fixtureJSON(created)
		case "PUT /api/v4/posts/" + fixturePostID + "/patch":
			edited := post
			edited.Message = "edited"
			edited.EditAt = edited.UpdateAt + 1
			return http.StatusOK, nil, fixtureJSON(edited)
		case "DELETE /api/v4/posts/" + fixturePostID,
			"DELETE /api/v4/users/" + fixtureActorID + "/posts/" + fixturePostID + "/reactions/thumbsup",
			"DELETE /api/v4/channels/" + fixtureChannelID + "/members/" + fixtureMemberID:
			return http.StatusOK, nil, []byte(`{"status":"OK"}`)
		case "POST /api/v4/reactions":
			return http.StatusCreated, nil, fixtureJSON(mattermostReaction{
				UserID: fixtureActorID, PostID: fixturePostID, EmojiName: "thumbsup",
			})
		case "POST /api/v4/channels":
			created := channel
			created.Name = "roadmap"
			created.DisplayName = "roadmap"
			return http.StatusCreated, nil, fixtureJSON(created)
		case "POST /api/v4/channels/" + fixtureChannelID + "/members":
			return http.StatusCreated, nil, fixtureJSON(map[string]string{
				"channel_id": fixtureChannelID, "user_id": fixtureMemberID,
			})
		default:
			return http.StatusNotFound, nil, []byte(`{"id":"not_found"}`)
		}
	})
	route := application.MessageWriteRoute{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID, Actor: client.MessageActor(),
	}
	sent, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: route, ConversationID: fixtureChannelID,
		Content: application.MessageContent{Format: application.MessageFormatMarkdown, Text: "created"},
	})
	if err != nil || sent.Content.Text != "created" {
		t.Fatalf("SendMessage() = %#v, %v", sent, err)
	}
	version := mattermostMessageVersion(post)
	edited, err := client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: route, ConversationID: fixtureChannelID, MessageID: fixturePostID,
		Version: version, Content: application.MessageContent{Format: application.MessageFormatMarkdown, Text: "edited"},
	})
	if err != nil || edited.Content.Text != "edited" {
		t.Fatalf("EditMessage() = %#v, %v", edited, err)
	}
	if err := client.DeleteMessage(t.Context(), application.MessageDeleteInput{
		MessageWriteRoute: route, ConversationID: fixtureChannelID,
		MessageID: fixturePostID, Version: version,
	}); err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
	reaction, err := client.SetMessageReaction(t.Context(), application.MessageReactionInput{
		MessageWriteRoute: route, ConversationID: fixtureChannelID,
		MessageID: fixturePostID, Version: version, Reaction: "thumbsup",
	})
	if err != nil || !reaction.ReactedByActor {
		t.Fatalf("SetMessageReaction() = %#v, %v", reaction, err)
	}
	created, err := client.CreateConversation(t.Context(), application.ConversationCreateInput{
		MessageWriteRoute: route, Kind: application.ConversationChannel,
		Visibility: application.ConversationVisibilityPublic, Name: "roadmap",
		Members: []application.ConversationMemberInput{{ID: fixtureActorID, Role: application.ConversationMember}},
	})
	if err != nil || created.Name != "roadmap" {
		t.Fatalf("CreateConversation() = %#v, %v", created, err)
	}
	membership, err := client.ChangeConversationMembership(t.Context(), application.ConversationMembershipInput{
		MessageWriteRoute: route, ConversationID: fixtureChannelID,
		Version: mattermostConversationVersion(channel), Action: application.MembershipAdd,
		Member: application.ConversationMemberInput{ID: fixtureMemberID, Role: application.ConversationMember},
	})
	if err != nil || membership.Member.ID != fixtureMemberID {
		t.Fatalf("ChangeConversationMembership() = %#v, %v", membership, err)
	}
	wrong := route
	wrong.Actor.ID = fixtureMemberID
	if _, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: wrong, ConversationID: fixtureChannelID,
		Content: application.MessageContent{Format: application.MessageFormatMarkdown, Text: "blocked"},
	}); err == nil {
		t.Fatal("SendMessage() accepted a different actor")
	}
}

func TestMattermostRateLimitAndReadOnlyCapabilityFailClosed(t *testing.T) {
	client := newFixtureClient(t, true, func(request *http.Request, _ []byte) (int, http.Header, []byte) {
		if request.URL.Path == "/api/v4/users/me/channels" {
			return http.StatusTooManyRequests,
				http.Header{"X-Ratelimit-Reset": {"0"}}, []byte(`{"id":"rate_limit"}`)
		}
		return http.StatusNotFound, nil, []byte(`{"id":"not_found"}`)
	})
	if client.MessageCapabilities().Send {
		t.Fatal("read-only Mattermost route advertised writes")
	}
	_, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: fixtureAccountID, WorkspaceID: fixtureTeamID, Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("ListConversations() error = %v", err)
	}
}

func TestMattermostRejectsMismatchedSelectedTeam(t *testing.T) {
	transport := &fixtureRoundTripper{handle: func(request *http.Request, _ []byte) (int, http.Header, []byte) {
		if request.URL.Path == "/api/v4/users/me" {
			return http.StatusOK, nil, fixtureJSON(mattermostUser{ID: fixtureActorID, Username: "synthetic"})
		}
		if request.URL.Path == "/api/v4/users/me/teams" {
			return http.StatusOK, nil, fixtureJSON([]mattermostTeam{{ID: fixtureMemberID}})
		}
		return http.StatusNotFound, nil, nil
	}}
	_, err := newWithHTTP(context.Background(), Options{
		Origin: "https://chat.example.test", WorkspaceID: fixtureTeamID,
	}, &http.Client{Transport: transport}, nil)
	if err == nil {
		t.Fatal("newWithHTTP() accepted a different team")
	}
}

func TestMattermostMissingRoleEvidenceNarrowsCapabilities(t *testing.T) {
	transport := &fixtureRoundTripper{handle: func(request *http.Request, _ []byte) (int, http.Header, []byte) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			return http.StatusOK, nil, fixtureJSON(mattermostUser{
				ID: fixtureActorID, Username: "synthetic", Roles: "system_user",
			})
		case "/api/v4/users/me/teams":
			return http.StatusOK, nil, fixtureJSON([]mattermostTeam{{ID: fixtureTeamID}})
		case "/api/v4/teams/" + fixtureTeamID + "/members/" + fixtureActorID:
			return http.StatusOK, nil, fixtureJSON(mattermostTeamMember{
				TeamID: fixtureTeamID, UserID: fixtureActorID, Roles: "team_user",
			})
		case "/api/v4/users/" + fixtureActorID + "/teams/" + fixtureTeamID + "/channels/members":
			return http.StatusOK, nil, fixtureJSON([]mattermostChannelMember{})
		case "/api/v4/roles/names":
			return http.StatusForbidden, nil, []byte(`{"id":"forbidden"}`)
		default:
			return http.StatusNotFound, nil, []byte(`{"id":"not_found"}`)
		}
	}}
	client, err := newWithHTTP(t.Context(), Options{
		Origin: "https://chat.example.test", WorkspaceID: fixtureTeamID,
	}, &http.Client{Transport: transport}, nil)
	if err != nil {
		t.Fatalf("newWithHTTP() error = %v", err)
	}
	defer func() { _ = client.Close() }()
	capabilities := client.MessageCapabilities()
	if !capabilities.ListConversations || capabilities.History || capabilities.Send ||
		capabilities.CreateConversation {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	found := false
	for _, degradation := range client.MessageDegradations() {
		if degradation.Feature == "messages.permissions" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing role evidence was not reported as a degradation")
	}
}
