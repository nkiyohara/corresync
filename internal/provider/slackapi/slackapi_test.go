package slackapi

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
	"sync"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const syntheticSlackScopes = "channels:read,channels:history,chat:write,reactions:write,channels:manage,files:read"

func TestSlackReadsUseOfficialMethodsAndBoundedMetadata(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
		switch request.URL.Path {
		case "/api/auth.test":
			writeSlackFixture(t, writer, "slack-auth-v1.json")
		case "/api/conversations.list":
			if request.URL.Query().Get("types") != "public_channel" || request.URL.Query().Get("limit") != "25" {
				t.Errorf("conversation query = %s", request.URL.RawQuery)
			}
			writeSlackFixture(t, writer, "slack-conversations-v1.json")
		case "/api/conversations.history":
			query := request.URL.Query()
			if query.Get("channel") != "CSYNTHETIC" || query.Get("limit") == "" {
				t.Errorf("history query = %s", request.URL.RawQuery)
			}
			writeSlackFixtureWithOrigin(t, writer, "slack-messages-v1.json", server.URL)
		case "/files-pri/TSYNTHETIC-FSYNTHETIC/synthetic.txt":
			_, _ = writer.Write([]byte("synthetic fixture"))
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()

	client := newTestSlackClient(t, server)
	defer func() { _ = client.Close() }()
	capabilities := client.MessageCapabilities()
	if !capabilities.ListConversations || !capabilities.History || !capabilities.Send ||
		!capabilities.Reactions || !capabilities.AttachmentReads || capabilities.AttachmentWrites ||
		capabilities.ActorMode != application.MessageActorApp {
		t.Fatalf("Slack capabilities = %+v", capabilities)
	}
	conversations, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC", Limit: 25,
	})
	if err != nil || len(conversations.Conversations) != 1 ||
		conversations.Conversations[0].ID != "CSYNTHETIC" || conversations.NextCursor == "" {
		t.Fatalf("ListConversations() = %+v, %v", conversations, err)
	}
	if _, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: "acc_22222222222222222222222222222222", WorkspaceID: "TSYNTHETIC",
		Cursor: conversations.NextCursor, Limit: 25,
	}); err == nil || !strings.Contains(err.Error(), "selected route") {
		t.Fatalf("cross-account Slack cursor error = %v", err)
	}
	page, err := client.ListMessages(t.Context(), application.MessageListInput{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
		ConversationID: "CSYNTHETIC", Limit: 25,
	})
	if err != nil || len(page.Messages) != 1 || strings.Contains(page.Messages[0].Snippet, "secret") {
		t.Fatalf("ListMessages() = %+v, %v", page, err)
	}
	message, err := client.GetMessage(t.Context(), application.MessageGetInput{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
		ConversationID: "CSYNTHETIC", MessageID: "1723636800.000001",
	})
	if err != nil || len(message.Links) != 1 || len(message.Mentions) != 1 ||
		len(message.Attachments) != 1 || len(message.Reactions) != 1 ||
		!message.Reactions[0].CountKnown || message.Summary.Author.ID != "USYNTHETICAPP" {
		t.Fatalf("GetMessage() = %+v, %v", message, err)
	}
	attachment, err := client.GetMessageAttachment(t.Context(), application.MessageAttachmentGetInput{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
		ConversationID: "CSYNTHETIC", MessageID: "1723636800.000001", AttachmentID: "FSYNTHETIC",
	})
	if err != nil || string(attachment.Data) != "synthetic fixture" || attachment.Metadata.Size != 17 {
		t.Fatalf("GetMessageAttachment() = %+v, %v", attachment, err)
	}
}

func TestSlackIdentityScopeAndRateLimitFailuresStayContentFree(t *testing.T) {
	t.Parallel()

	t.Run("workspace", func(t *testing.T) {
		server := newSlackServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
			writeSlackFixture(t, writer, "slack-auth-v1.json")
		})
		defer server.Close()
		_, err := New(t.Context(), Options{
			APIBase: server.URL + "/api", WorkspaceID: "TOTHER", HTTP: server.Client(),
		})
		if err == nil || !strings.Contains(err.Error(), "configured workspace") {
			t.Fatalf("workspace mismatch error = %v", err)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		server := newSlackServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-OAuth-Scopes", strings.Repeat("a,", maximumSlackScopes+1))
			writeSlackFixture(t, writer, "slack-auth-v1.json")
		})
		defer server.Close()
		if _, err := New(t.Context(), Options{
			APIBase: server.URL + "/api", WorkspaceID: "TSYNTHETIC", HTTP: server.Client(),
		}); err == nil || !strings.Contains(err.Error(), "scopes") {
			t.Fatalf("unbounded scope error = %v", err)
		}
	})

	t.Run("startup-warning", func(t *testing.T) {
		server := newSlackServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "team_id": "TSYNTHETIC", "user_id": "USYNTHETICAPP",
				"bot_id": "BSYNTHETICAPP", "warning": "synthetic_warning",
			})
		})
		defer server.Close()
		client := newTestSlackClient(t, server)
		degradations := client.MessageDegradations()
		if !strings.Contains(degradations[len(degradations)-1].Reason, "synthetic_warning") {
			t.Fatalf("startup warning degradations = %+v", degradations)
		}
	})

	t.Run("rate-limit", func(t *testing.T) {
		server := newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
			if request.URL.Path == "/api/auth.test" {
				writeSlackFixture(t, writer, "slack-auth-v1.json")
				return
			}
			writer.Header().Set("Retry-After", "60")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"ok":false,"error":"ratelimited","detail":"never expose me"}`))
		})
		defer server.Close()
		client := newTestSlackClient(t, server)
		_, err := client.ListMessages(t.Context(), application.MessageListInput{
			Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
			ConversationID: "CSYNTHETIC", Limit: 10,
		})
		var limited *RateLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != time.Minute || strings.Contains(err.Error(), "never expose") {
			t.Fatalf("rate-limit error = %v", err)
		}
	})
}

func TestSlackCursorDigestAndFileAuthorityAreClosed(t *testing.T) {
	t.Parallel()

	if !validSlackQueryDigest(strings.Repeat("a", 64)) ||
		validSlackQueryDigest(strings.Repeat("g", 64)) || validSlackQueryDigest("00") {
		t.Fatal("Slack search digest validation did not enforce one SHA-256 value")
	}
	client := &Client{apiHost: "slack.com"}
	for _, rawURL := range []string{
		"https://evil.example/files-pri/T-F/file",
		"https://files.slack.com/files-pri/T-F/file?token=secret",
		"https://files.slack.com/not-files-pri/T-F/file",
	} {
		if _, err := client.validateSlackFileURL(rawURL); err == nil {
			t.Fatalf("unsafe Slack file URL accepted: %s", rawURL)
		}
	}
	if _, err := client.validateSlackFileURL("https://files.slack.com/files-pri/T-F/file"); err != nil {
		t.Fatalf("official Slack file URL rejected: %v", err)
	}
}

func TestSlackWritesRevalidateAndPreserveUnknownOutcomes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calls := make(map[string]int)
	server := newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
		mu.Lock()
		calls[request.URL.Path]++
		mu.Unlock()
		switch request.URL.Path {
		case "/api/auth.test":
			writeSlackFixture(t, writer, "slack-auth-v1.json")
		case "/api/conversations.history":
			writeSlackFixture(t, writer, "slack-messages-v1.json")
		case "/api/chat.update":
			var requestBody struct {
				Channel  string `json:"channel"`
				TS       string `json:"ts"`
				Text     string `json:"text"`
				Markdown bool   `json:"mrkdwn"`
			}
			decodeSlackRequest(t, request, &requestBody)
			if requestBody.Channel != "CSYNTHETIC" || requestBody.TS != "1723636800.000001" ||
				!strings.Contains(requestBody.Text, "<@USYNTHETICRECIPIENT>") {
				t.Errorf("edit request = %+v", requestBody)
			}
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "channel": "CSYNTHETIC", "ts": "1723636800.000001",
				"message": map[string]any{
					"type": "message", "text": requestBody.Text,
					"user": "USYNTHETICAPP", "bot_id": "BSYNTHETICAPP",
					"ts": "1723636800.000001", "edited": map[string]string{"ts": "1723636860.000002"},
				},
			})
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := newTestSlackClient(t, server)

	message, err := client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: application.MessageWriteRoute{
			Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
			Actor: client.MessageActor(),
		},
		ConversationID: "CSYNTHETIC", MessageID: "1723636800.000001",
		Version:  "slmv1_1723636800.000001",
		Content:  application.MessageContent{Format: application.MessageFormatMarkdown, Text: "Edited fixture"},
		Mentions: []application.MessageMention{{ID: "USYNTHETICRECIPIENT", Kind: application.MessageMentionUser}},
	})
	if err != nil || message.Summary.Version != "slmv1_1723636860.000002" {
		t.Fatalf("EditMessage() = %+v, %v", message, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["/api/conversations.history"] != 1 || calls["/api/chat.update"] != 1 {
		t.Fatalf("Slack write calls = %+v", calls)
	}
}

func TestSlackClosedWriteCohortUsesOnlyReviewedMethods(t *testing.T) {
	t.Parallel()

	infoCalls := 0
	server := newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
		switch request.URL.Path {
		case "/api/auth.test":
			writeSlackFixture(t, writer, "slack-auth-v1.json")
		case "/api/conversations.history":
			writeSlackFixture(t, writer, "slack-messages-v1.json")
		case "/api/chat.postMessage":
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "channel": "CSYNTHETIC", "ts": "1723636900.000001",
				"message": map[string]any{
					"type": "message", "text": "New synthetic", "user": "USYNTHETICAPP",
					"bot_id": "BSYNTHETICAPP", "ts": "1723636900.000001",
				},
			})
		case "/api/reactions.add", "/api/chat.delete", "/api/conversations.invite":
			writeSlackJSON(t, writer, map[string]any{"ok": true})
		case "/api/conversations.create":
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "channel": map[string]any{
					"id": "CNEWCHANNEL", "name": "new-channel", "is_channel": true,
					"num_members": 1, "updated": int64(1723636800),
				},
			})
		case "/api/conversations.info":
			infoCalls++
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "channel": map[string]any{
					"id": "CSYNTHETIC", "name": "synthetic-channel", "is_channel": true,
					"num_members": 3 + infoCalls - 1, "updated": int64(1723636800 + infoCalls - 1),
				},
			})
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	client := newTestSlackClient(t, server)
	route := application.MessageWriteRoute{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
		Actor: client.MessageActor(),
	}
	sent, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: route, ConversationID: "CSYNTHETIC",
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "New synthetic"},
	})
	if err != nil || sent.Summary.ID != "1723636900.000001" {
		t.Fatalf("SendMessage() = %+v, %v", sent, err)
	}
	version := "slmv1_1723636800.000001"
	reaction, err := client.SetMessageReaction(t.Context(), application.MessageReactionInput{
		MessageWriteRoute: route, ConversationID: "CSYNTHETIC",
		MessageID: "1723636800.000001", Version: version, Reaction: "thumbsup",
	})
	if err != nil || reaction.Name != "thumbsup" || reaction.CountKnown || !reaction.ReactedByActor {
		t.Fatalf("SetMessageReaction() = %+v, %v", reaction, err)
	}
	if err := client.DeleteMessage(t.Context(), application.MessageDeleteInput{
		MessageWriteRoute: route, ConversationID: "CSYNTHETIC",
		MessageID: "1723636800.000001", Version: version,
	}); err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
	created, err := client.CreateConversation(t.Context(), application.ConversationCreateInput{
		MessageWriteRoute: route, Kind: application.ConversationChannel,
		Visibility: application.ConversationVisibilityPublic, Name: "new-channel",
		Members: []application.ConversationMemberInput{{ID: "USYNTHETICAPP", Role: application.ConversationMember}},
	})
	if err != nil || created.ID != "CNEWCHANNEL" {
		t.Fatalf("CreateConversation() = %+v, %v", created, err)
	}
	before := slackConversation{
		ID: "CSYNTHETIC", Name: "synthetic-channel", IsChannel: true,
		NumMembers: 3, Updated: 1723636800,
	}
	membership, err := client.ChangeConversationMembership(t.Context(), application.ConversationMembershipInput{
		MessageWriteRoute: route, ConversationID: "CSYNTHETIC",
		Version: slackConversationVersion(before), Action: application.MembershipAdd,
		Member: application.ConversationMemberInput{ID: "USYNTHETICRECIPIENT", Role: application.ConversationMember},
	})
	if err != nil || membership.Version == slackConversationVersion(before) ||
		membership.Member.ID != "USYNTHETICRECIPIENT" {
		t.Fatalf("ChangeConversationMembership() = %+v, %v", membership, err)
	}
}

func TestSlackMalformedWriteResponseIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	server := newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
		if request.URL.Path == "/api/auth.test" {
			writeSlackFixture(t, writer, "slack-auth-v1.json")
			return
		}
		if request.URL.Path == "/api/chat.postMessage" {
			_, _ = writer.Write([]byte(`{"ok":`))
			return
		}
		http.NotFound(writer, request)
	})
	defer server.Close()
	client := newTestSlackClient(t, server)
	_, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: application.MessageWriteRoute{
			Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
			Actor: client.MessageActor(),
		},
		ConversationID: "CSYNTHETIC",
		Content:        application.MessageContent{Format: application.MessageFormatPlain, Text: "Synthetic"},
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("malformed Slack write error = %v", err)
	}
}

func TestSlackSyncCursorKeepsHighWaterAcrossPages(t *testing.T) {
	t.Parallel()

	page := 0
	server := newSlackServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-OAuth-Scopes", syntheticSlackScopes)
		if request.URL.Path == "/api/auth.test" {
			writeSlackFixture(t, writer, "slack-auth-v1.json")
			return
		}
		if request.URL.Path != "/api/conversations.history" {
			http.NotFound(writer, request)
			return
		}
		page++
		if page == 1 {
			writeSlackJSON(t, writer, map[string]any{
				"ok": true, "has_more": true,
				"messages": []map[string]any{{
					"type": "message", "text": "Newest synthetic", "user": "USYNTHETICAPP",
					"bot_id": "BSYNTHETICAPP", "ts": "1723636802.000001",
				}},
				"response_metadata": map[string]string{"next_cursor": "page-two"},
			})
			return
		}
		if request.URL.Query().Get("cursor") != "page-two" {
			t.Errorf("second sync cursor = %q", request.URL.Query().Get("cursor"))
		}
		writeSlackJSON(t, writer, map[string]any{
			"ok": true, "has_more": false,
			"messages": []map[string]any{{
				"type": "message", "text": "Older synthetic", "user": "USYNTHETICAPP",
				"bot_id": "BSYNTHETICAPP", "ts": "1723636801.000001",
			}},
			"response_metadata": map[string]string{"next_cursor": ""},
		})
	})
	defer server.Close()
	client := newTestSlackClient(t, server)
	input := application.MessageSyncInput{
		Account: "acc_11111111111111111111111111111111", WorkspaceID: "TSYNTHETIC",
		ConversationID: "CSYNTHETIC", Limit: 10,
	}
	first, err := client.SyncMessages(t.Context(), input)
	if err != nil || !first.HasMore || len(first.Changes) != 1 {
		t.Fatalf("first SyncMessages() = %+v, %v", first, err)
	}
	input.Cursor = &first.Cursor
	second, err := client.SyncMessages(t.Context(), input)
	if err != nil || second.HasMore || len(second.Changes) != 1 {
		t.Fatalf("second SyncMessages() = %+v, %v", second, err)
	}
	state, err := decodeSlackSyncState(second.Cursor.Opaque)
	if err != nil || state.Since != "1723636802.000001" || state.High != state.Since {
		t.Fatalf("final Slack sync state = %+v, %v", state, err)
	}
}

func newTestSlackClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(t.Context(), Options{
		APIBase: server.URL + "/api", WorkspaceID: "TSYNTHETIC",
		HTTP: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func newSlackServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(handler)
}

func writeSlackFixture(t *testing.T, writer http.ResponseWriter, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "contracts", name)) // #nosec G304 -- fixed synthetic fixture names.
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(data)
}

func writeSlackFixtureWithOrigin(t *testing.T, writer http.ResponseWriter, name, origin string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "contracts", name)) // #nosec G304 -- fixed synthetic fixture names.
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("https://files.slack.com"), []byte(origin))
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(data)
}

func writeSlackJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeSlackRequest(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	data, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode request: %v (%s)", err, data)
	}
}

var _ application.MessagingPort = (*Client)(nil)
