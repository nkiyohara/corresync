package jmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

type syntheticJMAP struct {
	t                   *testing.T
	server              *httptest.Server
	mu                  sync.Mutex
	state               string
	ifState             string
	requests            []string
	created             [][]emailAddress
	submit              bool
	readOnly            bool
	draftFailure        bool
	submissionFailure   bool
	brokenWriteResponse bool
	writeStatus         int
	uploads             int
	unauthorized        bool
}

func newSyntheticJMAP(t *testing.T) *syntheticJMAP {
	t.Helper()
	fixture := &syntheticJMAP{t: t, state: "state-1", submit: true}
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
		serverCapabilities := map[string]any{
			coreCapability: map[string]any{},
			mailCapability: map[string]any{},
		}
		accountCapabilities := map[string]any{
			mailCapability: map[string]any{},
		}
		primaryAccounts := map[string]string{
			mailCapability: "account-1",
		}
		if fixture.submit {
			serverCapabilities[submissionCapability] = map[string]any{}
			accountCapabilities[submissionCapability] = map[string]any{}
			primaryAccounts[submissionCapability] = "account-1"
		}
		fixture.writeJSON(writer, map[string]any{
			"capabilities": serverCapabilities,
			"accounts": map[string]any{
				"account-1": map[string]any{
					"accountCapabilities": accountCapabilities,
					"isReadOnly":          fixture.readOnly,
				},
			},
			"primaryAccounts": primaryAccounts,
			"apiUrl":          fixture.server.URL + "/api",
			"downloadUrl":     fixture.server.URL + "/download/{accountId}/{blobId}/{name}",
			"uploadUrl":       fixture.server.URL + "/upload/{accountId}",
		})
	case "/api":
		fixture.serveAPI(writer, request)
	case "/download/account-1/blob-1/report.txt":
		_, _ = writer.Write([]byte("fixture"))
	case "/upload/account-1":
		content, err := io.ReadAll(request.Body)
		if err != nil {
			fixture.t.Errorf("read upload: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		fixture.mu.Lock()
		fixture.uploads++
		fixture.mu.Unlock()
		fixture.writeJSON(writer, map[string]any{
			"accountId": "account-1",
			"blobId":    "uploaded-blob-1",
			"type":      request.Header.Get("Content-Type"),
			"size":      len(content),
		})
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (fixture *syntheticJMAP) serveAPI(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	unauthorized := fixture.unauthorized
	fixture.mu.Unlock()
	if unauthorized {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
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
	brokenWriteResponse := fixture.brokenWriteResponse
	draftFailure := fixture.draftFailure
	submissionFailure := fixture.submissionFailure
	writeStatus := fixture.writeStatus
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
		properties, _ := arguments["properties"].([]any)
		hasReplyTo := false
		for _, property := range properties {
			if property == "replyTo" {
				hasReplyTo = true
				break
			}
		}
		if !hasReplyTo {
			fixture.t.Error("Email/get did not request replyTo")
		}
		result = map[string]any{
			"accountId": "account-1", "state": state, "notFound": []string{},
			"list": []map[string]any{{
				"id": "message-1", "blobId": "message-blob", "threadId": "thread-1",
				"mailboxIds": map[string]bool{"inbox-1": true},
				"keywords":   map[string]bool{}, "size": 42,
				"receivedAt": "2026-07-28T12:00:00Z",
				"messageId":  []string{"message@example.invalid"},
				"from":       []map[string]string{{"name": "Sender", "email": "sender@example.invalid"}},
				"replyTo":    []map[string]string{{"name": "Replies", "email": "replies@example.invalid"}},
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
			create, _ := arguments["create"].(map[string]any)
			draft, _ := create["draft"].(map[string]any)
			rawTo, _ := draft["to"].([]any)
			createdTo := make([]emailAddress, 0, len(rawTo))
			for _, rawAddress := range rawTo {
				address, _ := rawAddress.(map[string]any)
				name, _ := address["name"].(string)
				emailValue, _ := address["email"].(string)
				createdTo = append(
					createdTo,
					emailAddress{Name: name, Email: emailValue},
				)
			}
			fixture.mu.Lock()
			fixture.created = append(fixture.created, createdTo)
			fixture.mu.Unlock()
			if draftFailure {
				result = map[string]any{
					"accountId": "account-1",
					"oldState":  state,
					"newState":  state,
					"notCreated": map[string]any{
						"draft": map[string]any{
							"type":        "invalidProperties",
							"description": "synthetic draft rejection",
						},
					},
				}
			} else {
				result = map[string]any{
					"accountId": "account-1", "oldState": state, "newState": "state-2",
					"created": map[string]any{
						"draft": map[string]any{"id": "draft-1", "blobId": "draft-blob"},
					},
				}
			}
		}
	case "Identity/get":
		result = map[string]any{
			"accountId": "account-1", "state": "identities-1",
			"list": []map[string]any{{
				"id": "identity-1", "name": "Reader",
				"email": "reader@example.invalid",
			}},
			"notFound": []string{},
		}
	case "EmailSubmission/set":
		if submissionFailure {
			result = map[string]any{
				"accountId": "account-1", "oldState": "submissions-1",
				"newState": "submissions-1",
				"notCreated": map[string]any{
					"send": map[string]any{
						"type":        "invalidProperties",
						"description": "synthetic submission rejection",
					},
				},
			}
		} else {
			result = map[string]any{
				"accountId": "account-1", "oldState": "submissions-1",
				"newState": "submissions-2",
				"created": map[string]any{
					"send": map[string]any{
						"id": "submission-1", "emailId": "draft-1",
					},
				},
			}
		}
	default:
		fixture.t.Errorf("unexpected method %q", method)
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if brokenWriteResponse && method == "Email/set" {
		_, _ = writer.Write([]byte(`{`))
		return
	}
	if writeStatus != 0 && method == "Email/set" {
		writer.WriteHeader(writeStatus)
		return
	}
	fixture.writeJSON(writer, map[string]any{
		"methodResponses": []any{[]any{method, result, "c1"}},
	})
}

func TestClientClassifiesRuntimeUnauthorizedResponse(t *testing.T) {
	t.Parallel()

	fixture := newSyntheticJMAP(t)
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	fixture.mu.Lock()
	fixture.unauthorized = true
	fixture.mu.Unlock()
	_, err = client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 25,
	})
	reason, ok := application.ProviderAuthenticationReason(err)
	if !ok || reason != application.AuthenticationReasonCredentialRejected {
		t.Fatalf("ListMessages() error = %v, reason = %q", err, reason)
	}
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

	draft, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{
		To:      []string{"recipient@example.invalid"},
		Subject: "Synthetic draft",
		Body:    "Draft body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID != "draft-1" || draft.ChangeKey != "state-2" {
		t.Fatalf("draft = %#v", draft)
	}
	sent, err := client.SendMail(t.Context(), application.MailSendInput{
		To:      []string{"recipient@example.invalid"},
		Subject: "Synthetic send",
		Body:    "Send body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID != "draft-1" || sent.ChangeKey != "state-2" {
		t.Fatalf("sent = %#v", sent)
	}
	reply, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{
		ComposeMode:        application.MailComposeReply,
		ReferenceMessageID: "message-1",
		ReferenceChangeKey: "state-2",
		Body:               "Reply body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ID != "draft-1" {
		t.Fatalf("reply = %#v", reply)
	}
	fixture.mu.Lock()
	created := append([][]emailAddress(nil), fixture.created...)
	fixture.mu.Unlock()
	if len(created) != 3 ||
		len(created[2]) != 1 ||
		created[2][0].Email != "replies@example.invalid" {
		t.Fatalf("created recipients = %#v", created)
	}
	if observed := client.ObservedCapabilities(); !observed.Submission ||
		observed.ReadOnly {
		t.Fatalf("observed capabilities = %#v", observed)
	}
}

func TestClientRejectsCrossOriginSessionEndpointsBeforeCredentialReuse(
	t *testing.T,
) {
	t.Parallel()
	var attackerRequests atomic.Int32
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		attackerRequests.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(attacker.Close)
	session := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/session" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if _, _, ok := request.BasicAuth(); !ok {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"capabilities": map[string]any{
				coreCapability: map[string]any{},
				mailCapability: map[string]any{},
			},
			"accounts": map[string]any{
				"account-1": map[string]any{
					"accountCapabilities": map[string]any{
						mailCapability: map[string]any{},
					},
				},
			},
			"primaryAccounts": map[string]string{
				mailCapability: "account-1",
			},
			"apiUrl":      attacker.URL + "/api",
			"downloadUrl": attacker.URL + "/download/{accountId}/{blobId}/{name}",
			"uploadUrl":   attacker.URL + "/upload/{accountId}",
		})
	}))
	t.Cleanup(session.Close)

	_, err := New(t.Context(), Options{
		SessionURL: session.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     session.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "session origin") {
		t.Fatalf("cross-origin session endpoints error = %v", err)
	}
	if attackerRequests.Load() != 0 {
		t.Fatalf(
			"credential was sent to unapproved origin %d times",
			attackerRequests.Load(),
		)
	}
}

func TestJMAPReplyTargetRejectsMalformedProviderAddress(t *testing.T) {
	t.Parallel()
	source := email{
		From: []emailAddress{{Email: "sender@example.invalid"}},
		ReplyTo: []emailAddress{{
			Email: "malformed address",
		}},
	}
	if _, err := uniqueDerivedAddresses(
		jmapReplyTarget(source),
		"reader@example.invalid",
	); err == nil {
		t.Fatal("malformed replyTo was accepted")
	}
	source.ReplyTo = nil
	got, err := uniqueDerivedAddresses(
		jmapReplyTarget(source),
		"reader@example.invalid",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Email != "sender@example.invalid" {
		t.Fatalf("fallback reply target = %#v", got)
	}
	cc := jmapAddressesExcluding(
		[]emailAddress{
			{Email: "observer@example.invalid"},
			{Email: "sender@example.invalid"},
		},
		got,
	)
	if len(cc) != 1 || cc[0].Email != "observer@example.invalid" {
		t.Fatalf("reply-all Cc recipients = %#v", cc)
	}
}

func TestJMAPMarksUnverifiableSuccessfulWriteAsAmbiguous(t *testing.T) {
	t.Parallel()
	fixture := newSyntheticJMAP(t)
	fixture.mu.Lock()
	fixture.brokenWriteResponse = true
	fixture.mu.Unlock()
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			To:      []string{"recipient@example.invalid"},
			Subject: "Unverifiable draft",
		},
	)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CreateMailDraft() error = %v", err)
	}
}

func TestJMAPDistinguishesAmbiguousAndDefiniteWriteStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		status      int
		wantUnknown bool
	}{
		{
			name:        "server failure",
			status:      http.StatusServiceUnavailable,
			wantUnknown: true,
		},
		{
			name:   "explicit rejection",
			status: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSyntheticJMAP(t)
			fixture.mu.Lock()
			fixture.writeStatus = test.status
			fixture.mu.Unlock()
			client, err := New(t.Context(), Options{
				SessionURL: fixture.server.URL + "/session",
				Username:   "reader@example.invalid",
				Password:   []byte("synthetic-secret"),
				Client:     fixture.server.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })

			_, err = client.CreateMailDraft(
				t.Context(),
				application.MailDraftInput{
					To:      []string{"recipient@example.invalid"},
					Subject: "Status classification",
				},
			)
			if err == nil ||
				errors.Is(err, application.ErrWriteOutcomeUnknown) !=
					test.wantUnknown {
				t.Fatalf("CreateMailDraft() error = %v", err)
			}
		})
	}
}

func TestJMAPSubmissionFailureReportsRetainedDraft(t *testing.T) {
	t.Parallel()
	fixture := newSyntheticJMAP(t)
	fixture.mu.Lock()
	fixture.submissionFailure = true
	fixture.mu.Unlock()
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.SendMail(t.Context(), application.MailSendInput{
		To:      []string{"recipient@example.invalid"},
		Subject: "Synthetic retained draft",
		Body:    "body",
	})
	fixture.mu.Lock()
	requests := append([]string(nil), fixture.requests...)
	fixture.mu.Unlock()
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) ||
		!slices.Equal(
			requests,
			[]string{
				"Identity/get", "Mailbox/get", "Email/set",
				"EmailSubmission/set",
			},
		) {
		t.Fatalf("SendMail() error = %v, requests = %#v", err, requests)
	}
}

func TestJMAPDraftFailureReportsUploadedAttachment(t *testing.T) {
	t.Parallel()
	fixture := newSyntheticJMAP(t)
	fixture.mu.Lock()
	fixture.draftFailure = true
	fixture.mu.Unlock()
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     fixture.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.CreateMailDraft(t.Context(), application.MailDraftInput{
		To:      []string{"recipient@example.invalid"},
		Subject: "Synthetic attachment draft",
		Attachments: []application.MailFileAttachment{{
			Name: "fixture.txt", ContentType: "text/plain",
			Content: []byte("fixture"),
		}},
	})
	fixture.mu.Lock()
	uploads := fixture.uploads
	fixture.mu.Unlock()
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || uploads != 1 {
		t.Fatalf("CreateMailDraft() error = %v, uploads = %d", err, uploads)
	}
}

func TestClientDegradesMissingSubmissionAndReadOnlyAccounts(t *testing.T) {
	t.Parallel()

	noSubmission := newSyntheticJMAP(t)
	noSubmission.submit = false
	client, err := New(t.Context(), Options{
		SessionURL: noSubmission.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     noSubmission.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if client.ObservedCapabilities().Submission {
		t.Fatal("submission was reported without session evidence")
	}
	if _, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 1,
	}); err != nil {
		t.Fatalf("mail read degraded with submission: %v", err)
	}
	if _, err := client.SendMail(t.Context(), application.MailSendInput{
		To: []string{"recipient@example.invalid"}, Subject: "Must not submit",
		Body: "body",
	}); err == nil || !strings.Contains(err.Error(), "submission is unavailable") {
		t.Fatalf("SendMail() error = %v", err)
	}

	readOnly := newSyntheticJMAP(t)
	readOnly.readOnly = true
	readOnlyClient, err := New(t.Context(), Options{
		SessionURL: readOnly.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     readOnly.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readOnlyClient.Close() })
	if !readOnlyClient.ObservedCapabilities().ReadOnly {
		t.Fatal("read-only account was not reported")
	}
	if _, err := readOnlyClient.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			To: []string{"recipient@example.invalid"}, Subject: "Must not write",
			Body: "body",
		},
	); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("CreateMailDraft() error = %v", err)
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
