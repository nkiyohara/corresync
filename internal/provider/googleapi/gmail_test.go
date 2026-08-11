package googleapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGmailAPIContractUsesBoundedNativeOperations(t *testing.T) {
	t.Parallel()

	var drafts, sends []string
	var readStateWrites, archiveWrites, trashWrites int
	var permanentDeleteCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case "GET /gmail/v1/users/me/messages":
			if request.URL.Query().Get("maxResults") != "1" ||
				request.URL.Query().Get("labelIds") != "INBOX" {
				t.Errorf("message list query = %q", request.URL.RawQuery)
			}
			writeGoogleJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "m1"}},
				"resultSizeEstimate": 1,
			})
		case "GET /gmail/v1/users/me/messages/m1":
			writeGoogleJSON(
				t,
				writer,
				gmailTestMessage(request.URL.Query().Get("format") == "full"),
			)
		case "GET /gmail/v1/users/me/messages/m1/attachments/a1":
			writeGoogleJSON(t, writer, map[string]any{
				"size": 4,
				"data": base64.RawURLEncoding.EncodeToString([]byte("file")),
			})
		case "GET /gmail/v1/users/me/labels":
			writeGoogleJSON(t, writer, map[string]any{
				"labels": []map[string]any{{
					"id": "INBOX", "name": "Inbox", "type": "system",
					"messagesTotal": 1, "messagesUnread": 1,
				}},
			})
		case "POST /gmail/v1/users/me/drafts":
			drafts = append(drafts, gmailTestRaw(t, request))
			writeGoogleJSON(t, writer, map[string]any{
				"id": "d1",
				"message": map[string]string{
					"id": "m2", "historyId": "102",
				},
			})
		case "POST /gmail/v1/users/me/messages/send":
			sends = append(sends, gmailTestRaw(t, request))
			writeGoogleJSON(t, writer, map[string]string{
				"id": "m3", "historyId": "103",
			})
		case "POST /gmail/v1/users/me/messages/m1/modify":
			var mutation struct {
				Add    []string `json:"addLabelIds"`
				Remove []string `json:"removeLabelIds"`
			}
			if err := json.NewDecoder(request.Body).Decode(&mutation); err != nil {
				t.Error(err)
			}
			message := gmailTestMessage(false)
			switch {
			case slices.Equal(mutation.Remove, []string{"UNREAD"}):
				readStateWrites++
				message.HistoryID = "104"
				message.LabelIDs = []string{"INBOX"}
			case slices.Equal(mutation.Remove, []string{"INBOX"}):
				archiveWrites++
				message.HistoryID = "105"
				message.LabelIDs = []string{"UNREAD"}
			default:
				t.Errorf("unexpected Gmail mutation = %#v", mutation)
			}
			writeGoogleJSON(t, writer, message)
		case "POST /gmail/v1/users/me/messages/m1/trash":
			trashWrites++
			message := gmailTestMessage(false)
			message.HistoryID = "106"
			message.LabelIDs = []string{"TRASH"}
			writeGoogleJSON(t, writer, message)
		case "DELETE /gmail/v1/users/me/messages/m1":
			permanentDeleteCalls++
			http.Error(writer, "permanent delete must remain unreachable", http.StatusTeapot)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL,
		Address: "reader@example.test",
		Mail:    true,
		HTTP:    server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 1,
	})
	if err != nil || len(page.Messages) != 1 ||
		page.Messages[0].Subject != "Synthetic" || page.Messages[0].IsRead {
		t.Fatalf("ListMessages() = %#v, %v", page, err)
	}
	message := page.Messages[0]
	body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{
		MessageID: message.ID,
	})
	if err != nil || body.Text != "hello" || len(body.Attachments) != 1 {
		t.Fatalf("GetMessageBody() = %#v, %v", body, err)
	}
	attachment, err := client.GetMailAttachment(
		t.Context(),
		application.MailAttachmentInput{AttachmentID: body.Attachments[0].ID},
	)
	if err != nil ||
		attachment.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("file")) {
		t.Fatalf("GetMailAttachment() = %#v, %v", attachment, err)
	}
	folders, err := client.ListMailFolders(t.Context(), application.MailFolderListInput{
		Parent: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "msgfolderroot",
		},
		Traversal: application.MailFolderTraversalShallow,
		Limit:     10,
	})
	if err != nil || len(folders.Folders) != 1 ||
		folders.Folders[0].DistinguishedID != "inbox" {
		t.Fatalf("ListMailFolders() = %#v, %v", folders, err)
	}

	draft, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{
		To: []string{"to@example.test"}, BCC: []string{"hidden@example.test"},
		Subject: "Draft", Body: "body",
		Attachments: []application.MailFileAttachment{{
			Name: "draft.txt", ContentType: "text/plain", Content: []byte("draft"),
		}},
	})
	if err != nil || draft.ID == "" || len(drafts) != 1 ||
		!strings.Contains(drafts[0], "draft.txt") {
		t.Fatalf("CreateMailDraft() = %#v, %v, raw %q", draft, err, drafts)
	}
	sent, err := client.SendMail(t.Context(), application.MailSendInput{
		To: []string{"to@example.test"}, Subject: "Sent", Body: "body",
	})
	if err != nil || sent.ID == "" || len(sends) != 1 {
		t.Fatalf("SendMail() = %#v, %v, raw %q", sent, err, sends)
	}
	read, err := client.SetMailReadState(t.Context(), application.MailReadStateInput{
		MessageID: message.ID, ChangeKey: message.ChangeKey,
		State: application.MailReadStateRead,
	})
	if err != nil || read.ChangeKey == message.ChangeKey || readStateWrites != 1 {
		t.Fatalf("SetMailReadState() = %#v, %v", read, err)
	}
	moved, err := client.MoveMail(t.Context(), application.MailMoveInput{
		MessageID: message.ID, ChangeKey: message.ChangeKey,
		Destination: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "archive",
		},
	})
	if err != nil || moved.ChangeKey == message.ChangeKey || archiveWrites != 1 {
		t.Fatalf("MoveMail(archive) = %#v, %v", moved, err)
	}
	_, err = client.MoveMail(t.Context(), application.MailMoveInput{
		MessageID: message.ID, ChangeKey: message.ChangeKey,
		Destination: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "deleteditems",
		},
	})
	if err != nil || trashWrites != 1 {
		t.Fatalf("MoveMail(Trash) error = %v, calls = %d", err, trashWrites)
	}
	deleteErr := client.DeleteMail(t.Context(), application.MailDeleteInput{
		MessageID: message.ID, ChangeKey: message.ChangeKey,
	})
	if deleteErr == nil || !strings.Contains(deleteErr.Error(), "Trash") ||
		permanentDeleteCalls != 0 {
		t.Fatalf("DeleteMail() error = %v, remote calls = %d", deleteErr, permanentDeleteCalls)
	}
}

func TestGmailInlineAttachmentIsRefetchedByPartID(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case "/gmail/v1/users/me/messages/m1":
			message := gmailTestMessage(request.URL.Query().Get("format") == "full")
			if request.URL.Query().Get("format") == "full" {
				message.Payload.Parts[1].Body = gmailBody{
					Size: 4,
					Data: base64.RawURLEncoding.EncodeToString([]byte("file")),
				}
			}
			writeGoogleJSON(t, writer, message)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	messageID, err := encodeMessageID("m1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.GetMessageBody(
		t.Context(), application.MailBodyInput{MessageID: messageID},
	)
	if err != nil || len(body.Attachments) != 1 {
		t.Fatalf("GetMessageBody() = %#v, %v", body, err)
	}
	attachment, err := client.GetMailAttachment(
		t.Context(),
		application.MailAttachmentInput{AttachmentID: body.Attachments[0].ID},
	)
	if err != nil ||
		attachment.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("file")) {
		t.Fatalf("GetMailAttachment() = %#v, %v", attachment, err)
	}
}

func TestGmailRejectsHeaderInjectionAndUnboundedMIMENesting(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case "/gmail/v1/users/me/messages":
			writeGoogleJSON(t, writer, map[string]any{
				"messages": []map[string]string{{"id": "m1"}},
			})
		case "/gmail/v1/users/me/messages/m1":
			message := gmailTestMessage(false)
			message.Payload.Headers[0].Value = "subject\r\nBcc: attacker@example.test"
			writeGoogleJSON(t, writer, message)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("ListMessages() error = %v", err)
	}

	part := gmailPart{PartID: "leaf", MimeType: "text/plain"}
	for range 34 {
		part = gmailPart{MimeType: "multipart/mixed", Parts: []gmailPart{part}}
	}
	if err := walkGmailParts(part, func(gmailPart, []byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("walkGmailParts() error = %v", err)
	}
}

func TestGmailPaginationTraversesPastFiveHundredMessages(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case request.URL.Path == "/gmail/v1/users/me/messages":
			if request.URL.Query().Get("pageToken") == "" {
				messages := make([]map[string]string, 500)
				for index := range messages {
					messages[index] = map[string]string{"id": "m" + strconv.Itoa(index)}
				}
				writeGoogleJSON(t, writer, map[string]any{
					"messages": messages, "nextPageToken": "page-2",
					"resultSizeEstimate": 502,
				})
				return
			}
			writeGoogleJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "m500"}, {"id": "m501"}},
				"resultSizeEstimate": 502,
			})
		case strings.HasPrefix(request.URL.Path, "/gmail/v1/users/me/messages/m5"):
			id := strings.TrimPrefix(request.URL.Path, "/gmail/v1/users/me/messages/")
			message := gmailTestMessage(false)
			message.ID = id
			message.HistoryID = strings.TrimPrefix(id, "m") + "1"
			message.Payload.Headers[0].Value = id
			writeGoogleJSON(t, writer, message)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Offset: 500, Limit: 2,
	})
	if err != nil || len(page.Messages) != 2 ||
		page.Messages[0].Subject != "m500" ||
		page.Messages[1].Subject != "m501" || !page.IncludesLastItem {
		t.Fatalf("ListMessages() = %#v, %v", page, err)
	}
}

func TestGmailRejectsMalformedListMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case "/gmail/v1/users/me/messages":
			writeGoogleJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "m1"}},
				"resultSizeEstimate": -1,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_, err = client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "message-list metadata") {
		t.Fatalf("ListMessages() error = %v", err)
	}
}

func TestGmailMoveFromTrashReportsPartialOutcome(t *testing.T) {
	t.Parallel()

	var untrashCalls, modifyCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]string{
				"emailAddress": "reader@example.test",
			})
		case "GET /gmail/v1/users/me/messages/m1":
			message := gmailTestMessage(false)
			message.LabelIDs = []string{"TRASH"}
			writeGoogleJSON(t, writer, message)
		case "POST /gmail/v1/users/me/messages/m1/untrash":
			untrashCalls++
			message := gmailTestMessage(false)
			message.HistoryID = "102"
			message.LabelIDs = nil
			writeGoogleJSON(t, writer, message)
		case "POST /gmail/v1/users/me/messages/m1/modify":
			modifyCalls++
			http.Error(writer, `{"error":{"code":400}}`, http.StatusBadRequest)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	messageID, err := encodeMessageID("m1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.MoveMail(t.Context(), application.MailMoveInput{
		MessageID: messageID, ChangeKey: encodeHistoryID("101"),
		Destination: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) ||
		untrashCalls != 1 || modifyCalls != 1 {
		t.Fatalf(
			"MoveMail() error = %v, untrash = %d, modify = %d",
			err, untrashCalls, modifyCalls,
		)
	}
}

func TestGmailReplyTargetsRemainDeterministic(t *testing.T) {
	t.Parallel()

	headers := []gmailHeader{
		{Name: "From", Value: "sender@example.test"},
		{Name: "Reply-To", Value: "replies@example.test"},
	}
	if got := gmailReplyTarget(headers); got != "replies@example.test" {
		t.Fatalf("gmailReplyTarget() = %q", got)
	}
	cc := gmailAddressesExcluding(
		[]string{"observer@example.test", "replies@example.test"},
		[]string{"replies@example.test"},
	)
	if !slices.Equal(cc, []string{"observer@example.test"}) {
		t.Fatalf("gmailAddressesExcluding() = %#v", cc)
	}
}

func gmailTestMessage(full bool) gmailMessage {
	message := gmailMessage{
		ID: "m1", ThreadID: "t1", HistoryID: "101",
		InternalDate: "1785578400000",
		LabelIDs:     []string{"INBOX", "UNREAD"},
		Payload: gmailPart{
			MimeType: "multipart/mixed",
			Headers: []gmailHeader{
				{Name: "Subject", Value: "Synthetic"},
				{Name: "From", Value: "Sender <sender@example.test>"},
				{Name: "Reply-To", Value: "Replies <replies@example.test>"},
				{Name: "To", Value: "reader@example.test"},
				{Name: "Cc", Value: "observer@example.test"},
				{Name: "Message-ID", Value: "<source@example.test>"},
			},
		},
	}
	if full {
		message.Payload.Parts = []gmailPart{
			{
				PartID: "0", MimeType: "text/plain",
				Body: gmailBody{
					Size: 5,
					Data: base64.RawURLEncoding.EncodeToString([]byte("hello")),
				},
			},
			{
				PartID: "1", MimeType: "text/plain", Filename: "file.txt",
				Body: gmailBody{AttachmentID: "a1", Size: 4},
			},
		}
	}
	return message
}

func gmailTestRaw(t *testing.T, request *http.Request) string {
	t.Helper()
	content, err := io.ReadAll(request.Body)
	if err != nil {
		t.Error(err)
		return ""
	}
	var envelope struct {
		Raw     string `json:"raw"`
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	if err := json.Unmarshal(content, &envelope); err != nil {
		t.Error(err)
		return ""
	}
	raw := envelope.Raw
	if raw == "" {
		raw = envelope.Message.Raw
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Error(err)
		return ""
	}
	return string(decoded)
}
