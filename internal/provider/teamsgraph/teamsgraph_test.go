package teamsgraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

var syntheticGraphScopes = []string{
	"User.Read", "Chat.ReadWrite", "Team.ReadBasic.All", "Channel.ReadBasic.All",
	"ChannelMessage.Read.All", "ChatMessage.Send", "ChannelMessage.Send",
	"ChannelMessage.ReadWrite", "Chat.Create", "Channel.Create",
	"ChatMember.ReadWrite", "ChannelMember.ReadWrite.All",
}

const syntheticGraphAccount = domain.AccountID("acc_11111111111111111111111111111111")

func TestTeamsGraphReadsBindChatsChannelsBodiesAndSearch(t *testing.T) {
	t.Parallel()

	server := newGraphServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1.0/me":
			writeGraphFixture(t, writer, "teams-graph-identity-v1.json")
		case "/v1.0/me/chats":
			writeGraphFixture(t, writer, "teams-graph-chats-v1.json")
		case "/v1.0/me/joinedTeams":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{
				"id": "team-synthetic", "displayName": "Synthetic Team",
			}}})
		case "/v1.0/teams/team-synthetic/channels":
			writeGraphFixture(t, writer, "teams-graph-channels-v1.json")
		case "/v1.0/chats/chat-synthetic/messages":
			writeGraphFixture(t, writer, "teams-graph-messages-v1.json")
		case "/v1.0/chats/chat-synthetic/messages/message-synthetic":
			writeGraphFixture(t, writer, "teams-graph-message-v1.json")
		case "/v1.0/search/query":
			if request.Method != http.MethodPost {
				t.Errorf("search method = %s", request.Method)
			}
			var body struct {
				Requests []struct {
					EntityTypes []string `json:"entityTypes"`
					Query       struct {
						QueryString string `json:"queryString"`
					} `json:"query"`
					From int `json:"from"`
					Size int `json:"size"`
				} `json:"requests"`
			}
			decodeGraphRequest(t, request, &body)
			if len(body.Requests) != 1 || body.Requests[0].Query.QueryString != "synthetic" {
				t.Errorf("search request = %+v", body)
			}
			writeGraphFixture(t, writer, "teams-graph-search-v1.json")
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := newTestGraphClient(t, server)
	defer func() { _ = client.Close() }()

	capabilities := client.MessageCapabilities()
	if !capabilities.ListConversations || !capabilities.History || !capabilities.Search ||
		!capabilities.Send || !capabilities.Edit || !capabilities.Delete ||
		!capabilities.Reactions || !capabilities.CreateConversation || !capabilities.Membership ||
		capabilities.IncrementalSync || capabilities.AttachmentReads || capabilities.AttachmentWrites {
		t.Fatalf("Teams Graph capabilities = %+v", capabilities)
	}
	first, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic", Limit: 25,
	})
	if err != nil || len(first.Conversations) != 1 || first.Conversations[0].Kind != application.ConversationGroup ||
		first.Conversations[0].Visibility != application.ConversationVisibilityPrivate || first.NextCursor == "" {
		t.Fatalf("first ListConversations() = %+v, %v", first, err)
	}
	if _, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: "acc_22222222222222222222222222222222", WorkspaceID: "workspace-synthetic",
		Cursor: first.NextCursor, Limit: 25,
	}); err == nil || !strings.Contains(err.Error(), "selected route") {
		t.Fatalf("cross-account Teams Graph cursor error = %v", err)
	}
	second, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic",
		Cursor: first.NextCursor, Limit: 25,
	})
	if err != nil || len(second.Conversations) != 1 || second.NextCursor != "" ||
		second.Conversations[0].Kind != application.ConversationChannel ||
		second.Conversations[0].ContainerID == "" || second.Conversations[0].MemberCountKnown {
		t.Fatalf("second ListConversations() = %+v, %v", second, err)
	}
	chatID := first.Conversations[0].ID
	page, err := client.ListMessages(t.Context(), application.MessageListInput{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic",
		ConversationID: chatID, Limit: 25,
	})
	if err != nil || len(page.Messages) != 1 ||
		strings.Contains(page.Messages[0].Snippet, "secret body") {
		t.Fatalf("ListMessages() = %+v, %v", page, err)
	}
	message, err := client.GetMessage(t.Context(), application.MessageGetInput{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic",
		ConversationID: chatID, MessageID: "message-synthetic",
	})
	if err != nil || !strings.Contains(message.Content.Text, "secret body") ||
		len(message.Links) != 2 || len(message.Mentions) != 1 || len(message.Reactions) != 1 ||
		!message.Reactions[0].CountKnown || len(message.Attachments) != 1 ||
		message.Attachments[0].SizeKnown || message.Attachments[0].Downloadable {
		t.Fatalf("GetMessage() = %+v, %v", message, err)
	}
	search, err := client.SearchMessages(t.Context(), application.MessageSearchInput{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic",
		Query: "synthetic", Limit: 25,
	})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ConversationID != chatID ||
		strings.Contains(search.Messages[0].Snippet, "secret search body") {
		t.Fatalf("SearchMessages() = %+v, %v", search, err)
	}
}

func TestTeamsGraphScopeRateAndContinuationFailuresStayClosed(t *testing.T) {
	t.Parallel()

	t.Run("degraded-scopes", func(t *testing.T) {
		server := newGraphServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeGraphFixture(t, writer, "teams-graph-identity-v1.json")
		})
		defer server.Close()
		client, err := New(t.Context(), Options{
			APIBase: server.URL + "/v1.0", WorkspaceID: "workspace-synthetic",
			GrantedScopes: []string{"User.Read"}, HTTP: server.Client(),
		})
		if err != nil || client.MessageCapabilities().ListConversations ||
			len(client.MessageDegradations()) < 5 {
			t.Fatalf("degraded Teams Graph client = %+v, %v", client, err)
		}
	})

	t.Run("rate-limit", func(t *testing.T) {
		server := newGraphServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "60")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"code":"TooManyRequests","message":"never expose"}}`))
		})
		defer server.Close()
		_, err := New(t.Context(), Options{
			APIBase: server.URL + "/v1.0", WorkspaceID: "workspace-synthetic",
			GrantedScopes: syntheticGraphScopes, HTTP: server.Client(),
		})
		var limited *RateLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != time.Minute || strings.Contains(err.Error(), "never expose") {
			t.Fatalf("Teams Graph rate-limit error = %v", err)
		}
	})

	t.Run("continuation-origin", func(t *testing.T) {
		server := newGraphServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writeGraphFixture(t, writer, "teams-graph-identity-v1.json")
		})
		defer server.Close()
		client := newTestGraphClient(t, server)
		cursor, err := encodeGraphCursor(graphPageCursor{
			Version: 1, Kind: graphCursorConversations, Stage: graphStageChats,
			Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic",
			NextLink: "https://evil.example/v1.0/me/chats?$skiptoken=escape",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.ListConversations(t.Context(), application.ConversationListInput{
			Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic", Cursor: cursor, Limit: 25,
		})
		if err == nil || !strings.Contains(err.Error(), "configured origin") {
			t.Fatalf("cross-origin continuation error = %v", err)
		}
	})
}

func TestTeamsGraphWritesRevalidateAndPreserveUnknownOutcomes(t *testing.T) {
	t.Parallel()

	var malformedSend atomic.Bool
	server := newGraphServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1.0/me":
			writeGraphFixture(t, writer, "teams-graph-identity-v1.json")
		case "/v1.0/chats/chat-synthetic/messages/message-synthetic":
			if request.Method == http.MethodPatch {
				var body graphWriteBody
				decodeGraphRequest(t, request, &body)
				if body.Body.Content != "Edited synthetic" {
					t.Errorf("edit body = %+v", body)
				}
			}
			writeGraphFixture(t, writer, "teams-graph-message-v1.json")
		case "/v1.0/chats/chat-synthetic/messages":
			if malformedSend.Load() {
				writeGraphCreatedJSON(t, writer, graphMessage{
					ID: "sent-synthetic", ChatID: "another-chat", MessageType: "message",
					CreatedDateTime: "2026-08-14T12:03:00Z",
				})
				return
			}
			message := readGraphMessageFixture(t)
			message.ID = "sent-synthetic"
			message.ETag = "etag-sent"
			message.CreatedDateTime = "2026-08-14T12:03:00Z"
			writeGraphCreatedJSON(t, writer, message)
		case "/v1.0/chats/chat-synthetic/messages/message-synthetic/setReaction",
			"/v1.0/users/user-synthetic/chats/chat-synthetic/messages/message-synthetic/softDelete":
			writer.WriteHeader(http.StatusNoContent)
		case "/v1.0/chats":
			writeGraphCreatedJSON(t, writer, graphChat{
				ID: "direct-created", ChatType: "oneOnOne",
				CreatedDateTime: "2026-08-14T12:03:00Z", LastUpdatedDateTime: "2026-08-14T12:03:00Z",
			})
		case "/v1.0/teams/team-synthetic/channels":
			writeGraphCreatedJSON(t, writer, graphChannel{
				ID: "channel-created", DisplayName: "Created", MembershipType: "standard",
				CreatedDateTime: "2026-08-14T12:03:00Z",
			})
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := newTestGraphClient(t, server)
	chatID, _ := encodeGraphChatID("chat-synthetic")
	route := application.MessageWriteRoute{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic", Actor: client.MessageActor(),
	}
	version := graphMessageVersion(readGraphMessageFixture(t))
	sent, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: route, ConversationID: chatID,
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Sent synthetic"},
	})
	if err != nil || sent.Summary.ID != "sent-synthetic" {
		t.Fatalf("SendMessage() = %+v, %v", sent, err)
	}
	edited, err := client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: route, ConversationID: chatID, MessageID: "message-synthetic", Version: version,
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Edited synthetic"},
	})
	if err != nil || edited.Summary.ID != "message-synthetic" {
		t.Fatalf("EditMessage() = %+v, %v", edited, err)
	}
	reaction, err := client.SetMessageReaction(t.Context(), application.MessageReactionInput{
		MessageWriteRoute: route, ConversationID: chatID, MessageID: "message-synthetic",
		Version: version, Reaction: "like",
	})
	if err != nil || reaction.CountKnown || !reaction.ReactedByActor {
		t.Fatalf("SetMessageReaction() = %+v, %v", reaction, err)
	}
	if err := client.DeleteMessage(t.Context(), application.MessageDeleteInput{
		MessageWriteRoute: route, ConversationID: chatID, MessageID: "message-synthetic", Version: version,
	}); err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
	createdChat, err := client.CreateConversation(t.Context(), application.ConversationCreateInput{
		MessageWriteRoute: route, Kind: application.ConversationDirect,
		Visibility: application.ConversationVisibilityPrivate,
		Members: []application.ConversationMemberInput{
			{ID: "user-synthetic", Role: application.ConversationOwner},
			{ID: "recipient-synthetic", Role: application.ConversationOwner},
		},
	})
	if err != nil || createdChat.Kind != application.ConversationDirect {
		t.Fatalf("CreateConversation(chat) = %+v, %v", createdChat, err)
	}
	teamID, _ := encodeGraphTeamID("team-synthetic")
	createdChannel, err := client.CreateConversation(t.Context(), application.ConversationCreateInput{
		MessageWriteRoute: route, ContainerID: teamID, Kind: application.ConversationChannel,
		Visibility: application.ConversationVisibilityPublic, Name: "Created",
		Members: []application.ConversationMemberInput{{ID: "user-synthetic", Role: application.ConversationOwner}},
	})
	if err != nil || createdChannel.ContainerID != teamID {
		t.Fatalf("CreateConversation(channel) = %+v, %v", createdChannel, err)
	}
	malformedSend.Store(true)
	_, err = client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: route, ConversationID: chatID,
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Ambiguous"},
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("mismatched Teams send error = %v", err)
	}
}

func TestTeamsGraphMembershipBindsConversationVersionAndRemoteMemberID(t *testing.T) {
	t.Parallel()

	var membershipState atomic.Int32
	server := newGraphServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1.0/me":
			writeGraphFixture(t, writer, "teams-graph-identity-v1.json")
		case "/v1.0/chats/chat-synthetic":
			minute := 2 + membershipState.Load()
			writeGraphJSON(t, writer, graphChat{
				ID: "chat-synthetic", Topic: "Synthetic planning", ChatType: "group",
				CreatedDateTime:     "2026-08-14T12:00:00Z",
				LastUpdatedDateTime: "2026-08-14T12:0" + string('0'+minute) + ":00Z",
			})
		case "/v1.0/chats/chat-synthetic/members":
			switch request.Method {
			case http.MethodGet:
				members := []map[string]string{{"id": "membership-actor", "userId": "user-synthetic"}}
				if membershipState.Load() == 1 {
					members = append(members, map[string]string{
						"id": "membership-recipient", "userId": "recipient-synthetic",
					})
				}
				writeGraphJSON(t, writer, map[string]any{"value": members})
			case http.MethodPost:
				var member graphConversationMemberWrite
				decodeGraphRequest(t, request, &member)
				if !strings.HasSuffix(member.UserBind, "/users/recipient-synthetic") {
					t.Errorf("member binding = %+v", member)
				}
				membershipState.Store(1)
				writer.WriteHeader(http.StatusCreated)
			default:
				http.NotFound(writer, request)
			}
		case "/v1.0/chats/chat-synthetic/members/membership-recipient":
			if request.Method != http.MethodDelete {
				http.NotFound(writer, request)
				return
			}
			membershipState.Store(2)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := newTestGraphClient(t, server)
	chatID, _ := encodeGraphChatID("chat-synthetic")
	route := application.MessageWriteRoute{
		Account: syntheticGraphAccount, WorkspaceID: "workspace-synthetic", Actor: client.MessageActor(),
	}
	before, err := client.getGraphConversation(t.Context(), graphConversationLocator{ChatID: "chat-synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	member := application.ConversationMemberInput{
		ID: "recipient-synthetic", Role: application.ConversationOwner,
	}
	added, err := client.ChangeConversationMembership(t.Context(), application.ConversationMembershipInput{
		MessageWriteRoute: route, ConversationID: chatID, Version: before.Version,
		Action: application.MembershipAdd, Member: member,
	})
	if err != nil || added.Version == before.Version || membershipState.Load() != 1 {
		t.Fatalf("add Teams membership = %+v, %v, state=%d", added, err, membershipState.Load())
	}
	removed, err := client.ChangeConversationMembership(t.Context(), application.ConversationMembershipInput{
		MessageWriteRoute: route, ConversationID: chatID, Version: added.Version,
		Action: application.MembershipRemove, Member: member,
	})
	if err != nil || removed.Version == added.Version || membershipState.Load() != 2 {
		t.Fatalf("remove Teams membership = %+v, %v, state=%d", removed, err, membershipState.Load())
	}
}

func newTestGraphClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(t.Context(), Options{
		APIBase: server.URL + "/v1.0", WorkspaceID: "workspace-synthetic",
		GrantedScopes: syntheticGraphScopes, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func newGraphServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(handler)
}

func writeGraphFixture(t *testing.T, writer http.ResponseWriter, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "contracts", name)) // #nosec G304 -- fixed synthetic fixture names.
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(data)
}

func readGraphMessageFixture(t *testing.T) graphMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "contracts", "teams-graph-message-v1.json")) // #nosec G304 -- fixed synthetic fixture name.
	if err != nil {
		t.Fatal(err)
	}
	var message graphMessage
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func writeGraphJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func writeGraphCreatedJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeGraphRequest(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	data, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode Graph request: %v (%s)", err, data)
	}
}
