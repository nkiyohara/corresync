package googleapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGoogleAPIContractUsesBoundedReadsAndConditionalCalendarWrites(
	t *testing.T,
) {
	t.Parallel()

	var draftRaw string
	var sendRaw string
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /gmail/v1/users/me/profile":
			writeGoogleJSON(t, writer, map[string]any{
				"emailAddress": "reader@example.test",
			})
		case "GET /calendar/v3/users/me/calendarList/primary":
			writeGoogleJSON(t, writer, map[string]any{
				"id": "reader@example.test", "accessRole": "owner",
			})
		case "GET /calendar/v3/users/me/calendarList":
			writeGoogleJSON(t, writer, map[string]any{
				"items": []googleCalendarListEntry{{
					ID: "reader@example.test", Summary: "Primary",
					TimeZone: "Europe/London", AccessRole: "owner", Primary: true,
				}},
			})
		case "GET /gmail/v1/users/me/messages":
			if request.URL.Query().Get("maxResults") != "1" {
				t.Errorf("maxResults = %q", request.URL.Query().Get("maxResults"))
			}
			writeGoogleJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "m1"}},
				"resultSizeEstimate": 1,
			})
		case "GET /gmail/v1/users/me/messages/m1":
			format := request.URL.Query().Get("format")
			message := googleTestMessage(format == "full")
			writeGoogleJSON(t, writer, message)
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
			draftRaw = googleTestRaw(t, request)
			writeGoogleJSON(t, writer, map[string]any{
				"id": "d1",
				"message": map[string]string{
					"id": "m2", "historyId": "102",
				},
			})
		case "POST /gmail/v1/users/me/messages/send":
			sendRaw = googleTestRaw(t, request)
			writeGoogleJSON(t, writer, map[string]string{
				"id": "m3", "historyId": "103",
			})
		case "GET /calendar/v3/calendars/primary/events":
			if request.URL.Query().Get("showDeleted") != "false" ||
				request.URL.Query().Get("maxResults") != "2500" {
				t.Errorf("calendar list query = %q", request.URL.RawQuery)
			}
			writeGoogleJSON(t, writer, map[string]any{
				"items": []googleEvent{googleTestEvent(`"etag1"`)},
			})
		case "POST /calendar/v3/calendars/primary/events":
			if request.URL.Query().Get("sendUpdates") != "all" {
				t.Errorf("calendar create query = %q", request.URL.RawQuery)
			}
			writeGoogleJSON(t, writer, googleTestEvent(`"etag2"`))
		case "GET /calendar/v3/calendars/primary/events/e1":
			writeGoogleJSON(t, writer, googleTestEvent(`"etag1"`))
		case "PATCH /calendar/v3/calendars/primary/events/e1":
			if request.Header.Get("If-Match") != `"etag1"` ||
				request.URL.Query().Get("sendUpdates") != "all" {
				t.Errorf(
					"calendar update condition = %q query = %q",
					request.Header.Get("If-Match"),
					request.URL.RawQuery,
				)
			}
			writeGoogleJSON(t, writer, googleTestEvent(`"etag3"`))
		case "DELETE /calendar/v3/calendars/primary/events/e1":
			if request.Header.Get("If-Match") != `"etag1"` ||
				request.URL.Query().Get("sendUpdates") != "all" {
				t.Errorf(
					"calendar cancel condition = %q query = %q",
					request.Header.Get("If-Match"),
					request.URL.RawQuery,
				)
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected synthetic Google API request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL,
		Address: "reader@example.test",
		Mail:    true, Calendar: true,
		HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()

	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Subject != "Synthetic" ||
		page.Messages[0].IsRead {
		t.Fatalf("mail page = %#v", page)
	}
	body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{
		MessageID: page.Messages[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "hello" || len(body.Attachments) != 1 {
		t.Fatalf("mail body = %#v", body)
	}
	attachment, err := client.GetMailAttachment(
		t.Context(),
		application.MailAttachmentInput{AttachmentID: body.Attachments[0].ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("file")) {
		t.Fatalf("attachment = %#v", attachment)
	}
	folders, err := client.ListMailFolders(
		t.Context(),
		application.MailFolderListInput{
			Parent: application.MailFolder{
				Kind: application.MailFolderDistinguished, ID: "msgfolderroot",
			},
			Traversal: application.MailFolderTraversalShallow,
			Limit:     10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders.Folders) != 1 || folders.Folders[0].DistinguishedID != "inbox" {
		t.Fatalf("folders = %#v", folders)
	}

	draft, err := client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			To: []string{"to@example.test"}, BCC: []string{"hidden@example.test"},
			Subject: "Draft", Body: "body",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if draft.ID == "" || !strings.Contains(draftRaw, "Bcc: hidden@example.test") {
		t.Fatalf("draft = %#v raw = %q", draft, draftRaw)
	}
	sent, err := client.SendMail(
		t.Context(),
		application.MailSendInput{
			To: []string{"to@example.test"}, BCC: []string{"hidden@example.test"},
			Subject: "Sent", Body: "body",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID == "" || sent.ChangeKey == "" ||
		!strings.Contains(sendRaw, "Bcc: hidden@example.test") {
		t.Fatalf("sent = %#v raw = %q", sent, sendRaw)
	}
	if _, err := client.SetMailReadState(
		t.Context(), application.MailReadStateInput{},
	); err == nil || !strings.Contains(err.Error(), "atomic") {
		t.Fatalf("read-state degradation = %v", err)
	}
	if _, err := client.MoveMail(
		t.Context(), application.MailMoveInput{},
	); err == nil || !strings.Contains(err.Error(), "atomic") {
		t.Fatalf("move degradation = %v", err)
	}

	calendars, err := client.ListCalendarFolders(
		t.Context(),
		application.CalendarFolderListInput{Limit: 10},
	)
	if err != nil || len(calendars.Calendars) != 1 ||
		!calendars.Calendars[0].IsDefault ||
		!calendars.Calendars[0].CanEdit ||
		calendars.Calendars[0].ID == "" {
		t.Fatalf("calendars = %#v error = %v", calendars, err)
	}
	calendar, err := client.ListCalendarEvents(
		t.Context(),
		application.CalendarListInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Start: "2026-08-01T00:00:00Z", End: "2026-08-02T00:00:00Z",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(calendar.Events) != 1 ||
		calendar.Events[0].Subject != "Synthetic event" ||
		calendar.Events[0].OriginalStart != "2026-08-01T10:00:00Z" ||
		calendar.Events[0].OriginalStartTimeZone != "Europe/London" {
		t.Fatalf("calendar = %#v", calendar)
	}
	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Subject: "Created", Start: "2026-08-01T10:00:00Z",
			End:               "2026-08-01T11:00:00Z",
			RequiredAttendees: []string{"person@example.test"},
		},
	)
	if err != nil || created.ID == "" || created.ChangeKey == "" {
		t.Fatalf("created = %#v error = %v", created, err)
	}
	subject := "Updated"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: calendar.Events[0].ID, ChangeKey: calendar.Events[0].ChangeKey,
			Subject: &subject,
		},
	)
	if err != nil || updated.ChangeKey == calendar.Events[0].ChangeKey {
		t.Fatalf("updated = %#v error = %v", updated, err)
	}
	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID: calendar.Events[0].ID, ChangeKey: calendar.Events[0].ChangeKey,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestGoogleAPICalendarOnlyRouteBindsTheConfiguredIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/calendar/v3/users/me/calendarList/primary" {
			http.Error(writer, "unexpected request", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writeGoogleJSON(t, writer, map[string]any{
			"id": "other@example.test", "accessRole": "owner",
		})
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase:  server.URL,
		Address:  "reader@example.test",
		Calendar: true,
		HTTP:     server.Client(),
	})
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("New() error = %v, want calendar identity mismatch", err)
	}
}

func TestGoogleAPIRejectsIdentityMismatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writeGoogleJSON(t, writer, map[string]string{
			"emailAddress": "different@example.test",
		})
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, HTTP: server.Client(),
	})
	if client != nil || err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("client = %#v error = %v", client, err)
	}
}

func TestGoogleAPIRejectsReadOnlyPrimaryCalendarForWriteRoute(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writeGoogleJSON(t, writer, map[string]string{
			"id": "reader@example.test", "accessRole": "reader",
		})
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Calendar: true, HTTP: server.Client(),
	})
	if client != nil || err == nil || !strings.Contains(err.Error(), "not editable") {
		t.Fatalf("client = %#v error = %v", client, err)
	}
}

func TestGmailPaginationTraversesBeyondTheFirstFiveHundredMessages(t *testing.T) {
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
			token := request.URL.Query().Get("pageToken")
			if token == "" {
				if request.URL.Query().Get("maxResults") != "500" {
					t.Errorf("first maxResults = %q", request.URL.Query().Get("maxResults"))
				}
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
			if token != "page-2" || request.URL.Query().Get("maxResults") != "2" {
				t.Errorf("second list query = %q", request.URL.RawQuery)
			}
			writeGoogleJSON(t, writer, map[string]any{
				"messages":           []map[string]string{{"id": "m500"}, {"id": "m501"}},
				"resultSizeEstimate": 502,
			})
		case strings.HasPrefix(
			request.URL.Path,
			"/gmail/v1/users/me/messages/m5",
		):
			id := strings.TrimPrefix(request.URL.Path, "/gmail/v1/users/me/messages/")
			message := googleTestMessage(false)
			message.ID = id
			message.HistoryID = strings.TrimPrefix(id, "m") + "1"
			message.Payload.Headers = []gmailHeader{
				{Name: "Subject", Value: id},
				{Name: "From", Value: "Sender <sender@example.test>"},
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
	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Offset: 500, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 ||
		page.Messages[0].Subject != "m500" ||
		page.Messages[1].Subject != "m501" ||
		!page.IncludesLastItem ||
		page.TotalItemsInView != 502 {
		t.Fatalf("page = %#v", page)
	}
}

func googleTestMessage(full bool) gmailMessage {
	message := gmailMessage{
		ID: "m1", ThreadID: "t1", HistoryID: "101",
		InternalDate: "1785578400000",
		LabelIDs:     []string{"INBOX", "UNREAD"},
		Payload: gmailPart{
			MimeType: "multipart/mixed",
			Headers: []gmailHeader{
				{Name: "Subject", Value: "Synthetic"},
				{Name: "From", Value: "Sender <sender@example.test>"},
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

func googleTestEvent(etag string) googleEvent {
	event := googleEvent{
		ID: "e1", ETag: etag, Status: "confirmed",
		Summary: "Synthetic event",
		Start: googleEventTime{
			DateTime: "2026-08-01T10:00:00Z",
			TimeZone: "Europe/London",
		},
		End: googleEventTime{
			DateTime: "2026-08-01T11:00:00Z",
			TimeZone: "Europe/London",
		},
		Organizer: googleEventPerson{
			Email: "reader@example.test", Self: true,
		},
		Attendees: []googleEventPerson{{
			Email: "person@example.test", ResponseStatus: "accepted",
		}},
	}
	return event
}

func googleTestRaw(t *testing.T, request *http.Request) string {
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

func writeGoogleJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil &&
		!errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}
