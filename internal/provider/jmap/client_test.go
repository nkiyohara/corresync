package jmap

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

type syntheticJMAP struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	state    string
	ifState  string
	requests []string
}

func newSyntheticJMAP(t *testing.T) *syntheticJMAP {
	t.Helper()
	fixture := &syntheticJMAP{t: t, state: "state-1"}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *syntheticJMAP) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	username, password, ok := request.BasicAuth()
	if !ok || username != "reader@example.invalid" || password != "synthetic-secret" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch request.URL.Path {
	case "/session":
		fixture.writeJSON(writer, map[string]any{
			"capabilities": map[string]any{
				coreCapability:       map[string]any{},
				mailCapability:       map[string]any{},
				submissionCapability: map[string]any{},
			},
			"accounts": map[string]any{
				"account-1": map[string]any{
					"accountCapabilities": map[string]any{
						mailCapability:       map[string]any{},
						submissionCapability: map[string]any{},
					},
					"isReadOnly": false,
				},
			},
			"primaryAccounts": map[string]string{
				mailCapability:       "account-1",
				submissionCapability: "account-1",
			},
			"apiUrl":      fixture.server.URL + "/api",
			"downloadUrl": fixture.server.URL + "/download/{accountId}/{blobId}/{name}",
			"uploadUrl":   fixture.server.URL + "/upload/{accountId}",
		})
	case "/api":
		fixture.serveAPI(writer, request)
	case "/download/account-1/blob-1/report.txt":
		_, _ = writer.Write([]byte("fixture"))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (fixture *syntheticJMAP) serveAPI(writer http.ResponseWriter, request *http.Request) {
	var document requestDocument
	if err := json.NewDecoder(request.Body).Decode(&document); err != nil {
		fixture.t.Errorf("decode request: %v", err)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(document.MethodCalls) != 1 {
		fixture.t.Errorf("method calls = %#v", document.MethodCalls)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	method, _ := document.MethodCalls[0][0].(string)
	arguments, _ := document.MethodCalls[0][1].(map[string]any)
	fixture.mu.Lock()
	fixture.requests = append(fixture.requests, method)
	state := fixture.state
	fixture.mu.Unlock()
	var result any
	switch method {
	case "Mailbox/get":
		result = map[string]any{
			"accountId": "account-1", "state": "mailboxes-1",
			"list": []map[string]any{
				{
					"id": "inbox-1", "name": "Inbox", "parentId": nil,
					"role": "inbox", "sortOrder": 1, "totalEmails": 1, "unreadEmails": 1,
				},
				{
					"id": "drafts-1", "name": "Drafts", "parentId": nil,
					"role": "drafts", "sortOrder": 2, "totalEmails": 0, "unreadEmails": 0,
				},
			},
		}
	case "Email/query":
		result = map[string]any{
			"accountId": "account-1", "queryState": "query-1",
			"canCalculateChanges": true, "position": 0,
			"ids": []string{"message-1"}, "total": 1, "limit": 25,
		}
	case "Email/get":
		result = map[string]any{
			"accountId": "account-1", "state": state, "notFound": []string{},
			"list": []map[string]any{{
				"id": "message-1", "blobId": "message-blob", "threadId": "thread-1",
				"mailboxIds": map[string]bool{"inbox-1": true},
				"keywords":   map[string]bool{}, "size": 42,
				"receivedAt": "2026-07-28T12:00:00Z",
				"messageId":  []string{"message@example.invalid"},
				"from":       []map[string]string{{"name": "Sender", "email": "sender@example.invalid"}},
				"to":         []map[string]string{{"email": "reader@example.invalid"}},
				"subject":    "Synthetic message", "hasAttachment": true,
				"textBody":   []map[string]any{{"partId": "text", "type": "text/plain"}},
				"bodyValues": map[string]any{"text": map[string]any{"value": "Hello"}},
				"attachments": []map[string]any{{
					"partId": "attachment", "blobId": "blob-1", "size": 7,
					"name": "report.txt", "type": "text/plain", "disposition": "attachment",
				}},
			}},
		}
	case "Email/set":
		if value, exists := arguments["ifInState"]; exists {
			fixture.mu.Lock()
			fixture.ifState, _ = value.(string)
			fixture.state = "state-2"
			fixture.mu.Unlock()
			result = map[string]any{
				"accountId": "account-1", "oldState": state, "newState": "state-2",
				"updated": map[string]any{"message-1": nil},
			}
		} else {
			result = map[string]any{
				"accountId": "account-1", "oldState": state, "newState": "state-2",
				"created": map[string]any{
					"draft": map[string]any{"id": "draft-1", "blobId": "draft-blob"},
				},
			}
		}
	default:
		fixture.t.Errorf("unexpected method %q", method)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	fixture.writeJSON(writer, map[string]any{
		"methodResponses": []any{[]any{method, result, "c1"}},
	})
}

func (fixture *syntheticJMAP) writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		fixture.t.Errorf("encode response: %v", err)
	}
}

func TestClientReadsAndConditionallyUpdatesSyntheticJMAP(t *testing.T) {
	t.Parallel()
	fixture := newSyntheticJMAP(t)
	secret := []byte("synthetic-secret")
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   secret,
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for index := range secret {
		secret[index] = 'x'
	}

	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 ||
		page.Messages[0].ID != "message-1" ||
		page.Messages[0].ChangeKey != "state-1" ||
		page.Messages[0].Subject != "Synthetic message" {
		t.Fatalf("page = %#v", page)
	}

	body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{
		MessageID: "message-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "Hello" || len(body.Attachments) != 1 {
		t.Fatalf("body = %#v", body)
	}
	attachment, err := client.GetMailAttachment(
		t.Context(),
		application.MailAttachmentInput{AttachmentID: body.Attachments[0].ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.ContentBase64 != "Zml4dHVyZQ==" {
		t.Fatalf("attachment = %#v", attachment)
	}

	updated, err := client.SetMailReadState(
		t.Context(),
		application.MailReadStateInput{
			MessageID: "message-1", ChangeKey: page.Messages[0].ChangeKey,
			State: application.MailReadStateRead,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ChangeKey != "state-2" {
		t.Fatalf("updated = %#v", updated)
	}
	fixture.mu.Lock()
	ifState := fixture.ifState
	fixture.mu.Unlock()
	if ifState != "state-1" {
		t.Fatalf("ifInState = %q", ifState)
	}
}

func TestClientOwnsAndZerosCredential(t *testing.T) {
	t.Parallel()
	fixture := newSyntheticJMAP(t)
	secret := []byte("synthetic-secret")
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   secret,
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	owned := client.password
	if !bytes.Equal(owned, secret) {
		t.Fatal("client did not copy the credential")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(owned, make([]byte, len(owned))) {
		t.Fatal("Close() did not zero the owned credential")
	}
	if !bytes.Equal(secret, []byte("synthetic-secret")) {
		t.Fatal("Close() mutated caller-owned credential")
	}
}

func TestClientRejectsRedirectedSession(t *testing.T) {
	t.Parallel()
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("redirect target was reached")
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	_, err := New(t.Context(), Options{
		SessionURL: source.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: source.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestBoundedBodyTextRejectsOversizedUTF8WithoutTruncating(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("界", application.MaxMailBodyBytes/3+1)
	_, err := boundedBodyText(email{
		TextBody:   []emailPart{{PartID: "text"}},
		BodyValues: map[string]bodyValue{"text": {Value: value}},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("boundedBodyText() error = %v", err)
	}
}
