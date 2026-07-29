package graphapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGraphContractUsesDelegatedReadsAndETagConditions(t *testing.T) {
	t.Parallel()

	var sentBody []byte
	var replyDraftBody []byte
	var forwardBody []byte
	var responseDraftSent bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /me":
			writeGraphJSON(t, writer, map[string]string{
				"id": "user1", "mail": "reader@example.test",
			})
		case "GET /me/mailFolders/inbox":
			writeGraphJSON(t, writer, map[string]string{"id": "inbox1"})
		case "GET /me/calendar":
			writeGraphJSON(t, writer, map[string]any{
				"id": "calendar1", "canEdit": true,
			})
		case "GET /me/calendars":
			writeGraphJSON(t, writer, map[string]any{
				"@odata.count": 1,
				"value": []graphCalendar{{
					ID: "calendar1", Name: "Primary",
					CanEdit: true, IsDefaultCalendar: true,
				}},
			})
		case "GET /me/mailFolders/inbox/messages":
			writeGraphJSON(t, writer, map[string]any{
				"@odata.count": 1,
				"value":        []graphMessage{graphTestMessage(false, `W/"m1"`)},
			})
		case "GET /me/messages/m1":
			writeGraphJSON(t, writer, graphTestMessage(
				strings.Contains(request.URL.Query().Get("$select"), "body"),
				`W/"m1"`,
			))
		case "GET /me/messages/m1/attachments":
			writeGraphJSON(t, writer, map[string]any{
				"value": []graphAttachment{{
					ODataType: "#microsoft.graph.fileAttachment",
					ID:        "a1", Name: "file.txt", ContentType: "text/plain", Size: 4,
				}},
			})
		case "GET /me/messages/m1/attachments/a1":
			writeGraphJSON(t, writer, graphAttachment{
				ODataType: "#microsoft.graph.fileAttachment",
				ID:        "a1", Name: "file.txt", ContentType: "text/plain", Size: 4,
				ContentBytes: base64.StdEncoding.EncodeToString([]byte("file")),
			})
		case "GET /me/mailFolders":
			writeGraphJSON(t, writer, map[string]any{
				"@odata.count": 1,
				"value": []graphFolder{{
					ID: "folder1", DisplayName: "Synthetic",
					TotalItemCount: 1, UnreadItemCount: 1,
				}},
			})
		case "POST /me/messages":
			writeGraphJSONStatus(
				t,
				writer,
				graphTestMessage(false, `W/"m2"`),
				http.StatusCreated,
			)
		case "POST /me/sendMail":
			sentBody = readGraphBody(t, request)
			writer.WriteHeader(http.StatusAccepted)
		case "POST /me/messages/m1/createReply":
			replyDraftBody = readGraphBody(t, request)
			message := graphTestMessage(false, `W/"reply"`)
			message.ID = "reply1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/m1/createForward":
			forwardBody = readGraphBody(t, request)
			message := graphTestMessage(false, `W/"forward"`)
			message.ID = "forward1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/forward1/send":
			responseDraftSent = true
			writer.WriteHeader(http.StatusAccepted)
		case "PATCH /me/messages/m1":
			requireGraphCondition(t, request, `W/"m1"`)
			writeGraphJSON(t, writer, graphTestMessage(false, `W/"m3"`))
		case "POST /me/messages/m1/move":
			moved := graphTestMessage(false, `W/"m4"`)
			moved.ID = "moved1"
			writeGraphJSONStatus(t, writer, moved, http.StatusCreated)
		case "GET /me/calendarView":
			if request.Header.Get("Prefer") != `outlook.timezone="UTC"` ||
				request.URL.Query().Get("$top") != "1000" {
				t.Errorf(
					"calendar list headers/query = %q / %q",
					request.Header,
					request.URL.RawQuery,
				)
			}
			writeGraphJSON(t, writer, map[string]any{
				"value": []graphEvent{graphTestEvent(`W/"e1"`)},
			})
		case "POST /me/events":
			writeGraphJSONStatus(
				t,
				writer,
				graphTestEvent(`W/"e2"`),
				http.StatusCreated,
			)
		case "GET /me/events/e1":
			writeGraphJSON(t, writer, graphTestEvent(`W/"e1"`))
		case "PATCH /me/events/e1":
			requireGraphCondition(t, request, `W/"e1"`)
			writeGraphJSON(t, writer, graphTestEvent(`W/"e3"`))
		case "DELETE /me/events/e1":
			requireGraphCondition(t, request, `W/"e1"`)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected synthetic Graph request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Mail: true, Calendar: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()

	messages, err := client.ListMessages(
		t.Context(),
		application.MailListInput{
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished, ID: "inbox",
			},
			Limit: 10,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages.Messages) != 1 ||
		messages.Messages[0].Subject != "Synthetic message" {
		t.Fatalf("messages = %#v", messages)
	}
	body, err := client.GetMessageBody(
		t.Context(),
		application.MailBodyInput{MessageID: messages.Messages[0].ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "hello" || len(body.Attachments) != 1 {
		t.Fatalf("body = %#v", body)
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
	if err != nil || len(folders.Folders) != 1 {
		t.Fatalf("folders = %#v error = %v", folders, err)
	}
	draft, err := client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			To: []string{"to@example.test"}, Subject: "Draft", Body: "body",
		},
	)
	if err != nil || draft.ID == "" || draft.ChangeKey == "" {
		t.Fatalf("draft = %#v error = %v", draft, err)
	}
	if _, err := client.SendMail(
		t.Context(),
		application.MailSendInput{
			To: []string{"to@example.test"}, Subject: "Sent", Body: "body",
		},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sentBody), `"saveToSentItems":true`) {
		t.Fatalf("send request = %s", sentBody)
	}
	replyDraft, err := client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			ComposeMode:        application.MailComposeReply,
			ReferenceMessageID: messages.Messages[0].ID,
			ReferenceChangeKey: messages.Messages[0].ChangeKey,
			Body:               "reply body",
		},
	)
	if err != nil || replyDraft.ID == "" || replyDraft.ChangeKey == "" ||
		!strings.Contains(string(replyDraftBody), `"content":"reply body"`) ||
		strings.Contains(string(replyDraftBody), `"toRecipients"`) {
		t.Fatalf(
			"reply draft = %#v error = %v request = %s",
			replyDraft,
			err,
			replyDraftBody,
		)
	}
	if _, err := client.SendMail(
		t.Context(),
		application.MailSendInput{
			To:                 []string{"forward@example.test"},
			ComposeMode:        application.MailComposeForward,
			ReferenceMessageID: messages.Messages[0].ID,
			ReferenceChangeKey: messages.Messages[0].ChangeKey,
			Body:               "forward body",
		},
	); err != nil || !responseDraftSent ||
		!strings.Contains(string(forwardBody), `"forward@example.test"`) {
		t.Fatalf(
			"forward error = %v sent = %t request = %s",
			err,
			responseDraftSent,
			forwardBody,
		)
	}
	read, err := client.SetMailReadState(
		t.Context(),
		application.MailReadStateInput{
			MessageID: messages.Messages[0].ID,
			ChangeKey: messages.Messages[0].ChangeKey,
			State:     application.MailReadStateRead,
		},
	)
	if err != nil || read.ChangeKey == messages.Messages[0].ChangeKey {
		t.Fatalf("read state = %#v error = %v", read, err)
	}
	moved, err := client.MoveMail(
		t.Context(),
		application.MailMoveInput{
			MessageID: messages.Messages[0].ID,
			ChangeKey: messages.Messages[0].ChangeKey,
			Destination: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "archive",
			},
		},
	)
	if err != nil || moved.ID == messages.Messages[0].ID ||
		moved.ChangeKey == messages.Messages[0].ChangeKey {
		t.Fatalf("move = %#v error = %v", moved, err)
	}
	if err := client.DeleteMail(
		t.Context(),
		application.MailDeleteInput{
			MessageID: messages.Messages[0].ID,
			ChangeKey: messages.Messages[0].ChangeKey,
		},
	); err == nil || !strings.Contains(err.Error(), "permanent") {
		t.Fatalf("delete degradation = %v", err)
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
	if err != nil || len(calendar.Events) != 1 ||
		calendar.Events[0].OriginalStart != "2026-08-01T10:00:00" ||
		calendar.Events[0].OriginalStartTimeZone != "Pacific Standard Time" {
		t.Fatalf("calendar = %#v error = %v", calendar, err)
	}
	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished, ID: "calendar",
			},
			Subject: "Created", Start: "2026-08-01T10:00:00Z",
			End: "2026-08-01T11:00:00Z", TeamsMeeting: true,
		},
	)
	if err != nil || created.ID == "" ||
		created.OnlineMeetingProvider != "teams" ||
		created.OnlineMeetingJoinURL == "" {
		t.Fatalf("created = %#v error = %v", created, err)
	}
	subject := "Updated"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID:   calendar.Events[0].ID,
			ChangeKey: calendar.Events[0].ChangeKey,
			Subject:   &subject,
		},
	)
	if err != nil || updated.ChangeKey == calendar.Events[0].ChangeKey {
		t.Fatalf("updated = %#v error = %v", updated, err)
	}
	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID:   calendar.Events[0].ID,
			ChangeKey: calendar.Events[0].ChangeKey,
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestGraphRejectsDelegatedIdentityMismatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writeGraphJSON(t, writer, map[string]string{
			"id": "user1", "mail": "other@example.test",
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

func graphTestMessage(body bool, etag string) graphMessage {
	message := graphMessage{
		ODataETag: etag, ID: "m1", ChangeKey: "change",
		Subject: "Synthetic message",
		From: graphRecipient{
			EmailAddress: graphEmailAddress{
				Name: "Sender", Address: "sender@example.test",
			},
		},
		ReceivedAt: "2026-08-01T10:00:00Z",
		Importance: "normal", HasAttachments: true,
	}
	if body {
		message.Body = graphItemBody{ContentType: "text", Content: "hello"}
	}
	return message
}

func graphTestEvent(etag string) graphEvent {
	event := graphEvent{
		ODataETag: etag, ID: "e1", ChangeKey: "change",
		Subject:               "Synthetic event",
		OriginalStartTimeZone: "Pacific Standard Time",
		OriginalEndTimeZone:   "Pacific Standard Time",
		Start: graphDateTimeZone{
			DateTime: "2026-08-01T10:00:00", TimeZone: "UTC",
		},
		End: graphDateTimeZone{
			DateTime: "2026-08-01T11:00:00", TimeZone: "UTC",
		},
		IsOnline: true, IsOrganizer: true,
		OnlineMeetingProvider: "teamsForBusiness",
	}
	event.Organizer = graphRecipient{
		EmailAddress: graphEmailAddress{
			Name: "Reader", Address: "reader@example.test",
		},
	}
	event.OnlineMeeting.JoinURL =
		"https://teams.microsoft.com/l/meetup-join/synthetic"
	return event
}

func requireGraphCondition(t *testing.T, request *http.Request, want string) {
	t.Helper()
	if request.Header.Get("If-Match") != want {
		t.Errorf("If-Match = %q, want %q", request.Header.Get("If-Match"), want)
	}
}

func readGraphBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Error(err)
	}
	return body
}

func writeGraphJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writeGraphJSONStatus(t, writer, value, http.StatusOK)
}

func writeGraphJSONStatus(
	t *testing.T,
	writer http.ResponseWriter,
	value any,
	status int,
) {
	t.Helper()
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
