package graphapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGraphContractUsesDelegatedReadsAndETagConditions(t *testing.T) {
	t.Parallel()

	var sentBody []byte
	var newDraftBodies [][]byte
	var replyDraftBody []byte
	var forwardBody []byte
	var responseDraftSent bool
	var attachmentDraftSent bool
	var attachedDrafts []string
	var messagePermanentlyDeleted bool
	var calendarRecurrenceUpdated bool
	var calendarRecurrenceRemoved bool
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
			newDraftBodies = append(newDraftBodies, readGraphBody(t, request))
			draftID := "draft1"
			if len(newDraftBodies) == 2 {
				draftID = "senddraft1"
			}
			message := graphTestMessage(false, `W/"draft-initial"`)
			message.ID = draftID
			writeGraphJSONStatus(
				t,
				writer,
				message,
				http.StatusCreated,
			)
		case "POST /me/messages/draft1/attachments":
			attachedDrafts = append(attachedDrafts, "draft1")
			requireGraphAttachment(t, request, "draft.txt")
			writeGraphJSONStatus(t, writer, graphAttachment{
				ODataType: "#microsoft.graph.fileAttachment",
				ID:        "attachment-draft", Name: "draft.txt", Size: 5,
			}, http.StatusCreated)
		case "GET /me/messages/draft1":
			message := graphTestMessage(false, `W/"draft-final"`)
			message.ID = "draft1"
			writeGraphJSON(t, writer, message)
		case "POST /me/messages/senddraft1/attachments":
			attachedDrafts = append(attachedDrafts, "senddraft1")
			requireGraphAttachment(t, request, "send.txt")
			writeGraphJSONStatus(t, writer, graphAttachment{
				ODataType: "#microsoft.graph.fileAttachment",
				ID:        "attachment-send", Name: "send.txt", Size: 4,
			}, http.StatusCreated)
		case "GET /me/messages/senddraft1":
			message := graphTestMessage(false, `W/"send-draft-final"`)
			message.ID = "senddraft1"
			writeGraphJSON(t, writer, message)
		case "POST /me/messages/senddraft1/send":
			attachmentDraftSent = true
			writer.WriteHeader(http.StatusAccepted)
		case "POST /me/sendMail":
			sentBody = readGraphBody(t, request)
			writer.WriteHeader(http.StatusAccepted)
		case "POST /me/messages/m1/createReply":
			replyDraftBody = readGraphBody(t, request)
			message := graphTestMessage(false, `W/"reply"`)
			message.ID = "reply1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/reply1/attachments":
			attachedDrafts = append(attachedDrafts, "reply1")
			requireGraphAttachment(t, request, "reply.txt")
			writeGraphJSONStatus(t, writer, graphAttachment{
				ODataType: "#microsoft.graph.fileAttachment",
				ID:        "attachment-reply", Name: "reply.txt", Size: 5,
			}, http.StatusCreated)
		case "GET /me/messages/reply1":
			message := graphTestMessage(false, `W/"reply-final"`)
			message.ID = "reply1"
			writeGraphJSON(t, writer, message)
		case "POST /me/messages/m1/createForward":
			forwardBody = readGraphBody(t, request)
			message := graphTestMessage(false, `W/"forward"`)
			message.ID = "forward1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/forward1/attachments":
			attachedDrafts = append(attachedDrafts, "forward1")
			requireGraphAttachment(t, request, "forward.txt")
			writeGraphJSONStatus(t, writer, graphAttachment{
				ODataType: "#microsoft.graph.fileAttachment",
				ID:        "attachment-forward", Name: "forward.txt", Size: 7,
			}, http.StatusCreated)
		case "GET /me/messages/forward1":
			message := graphTestMessage(false, `W/"forward-final"`)
			message.ID = "forward1"
			writeGraphJSON(t, writer, message)
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
		case "POST /users/user1/messages/m1/permanentDelete":
			messagePermanentlyDeleted = true
			writer.WriteHeader(http.StatusNoContent)
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
			var patch map[string]json.RawMessage
			if err := json.NewDecoder(request.Body).Decode(&patch); err != nil {
				t.Fatal(err)
			}
			calendarRecurrenceUpdated = strings.Contains(
				string(patch["recurrence"]),
				`"type":"weekly"`,
			)
			calendarRecurrenceRemoved =
				string(patch["recurrence"]) == "null"
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
			Attachments: []application.MailFileAttachment{{
				Name: "draft.txt", Content: []byte("draft"),
			}},
		},
	)
	if err != nil || draft.ID == "" || draft.ChangeKey == "" {
		t.Fatalf("draft = %#v error = %v", draft, err)
	}
	if len(newDraftBodies) != 1 ||
		strings.Contains(string(newDraftBodies[0]), `"attachments"`) {
		t.Fatalf("new draft embedded attachments inline: %q", newDraftBodies)
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
	if _, err := client.SendMail(
		t.Context(),
		application.MailSendInput{
			To: []string{"to@example.test"}, Subject: "Sent attachment",
			Body: "body",
			Attachments: []application.MailFileAttachment{{
				Name: "send.txt", Content: []byte("send"),
			}},
		},
	); err != nil || !attachmentDraftSent ||
		len(newDraftBodies) != 2 ||
		strings.Contains(string(newDraftBodies[1]), `"attachments"`) {
		t.Fatalf(
			"attachment send error = %v sent = %t drafts = %q",
			err,
			attachmentDraftSent,
			newDraftBodies,
		)
	}
	replyDraft, err := client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			ComposeMode:        application.MailComposeReply,
			ReferenceMessageID: messages.Messages[0].ID,
			ReferenceChangeKey: messages.Messages[0].ChangeKey,
			Body:               "reply body",
			Attachments: []application.MailFileAttachment{{
				Name: "reply.txt", Content: []byte("reply"),
			}},
		},
	)
	if err != nil || replyDraft.ID == "" || replyDraft.ChangeKey == "" ||
		!strings.Contains(string(replyDraftBody), `"content":"reply body"`) ||
		strings.Contains(string(replyDraftBody), `"toRecipients"`) ||
		strings.Contains(string(replyDraftBody), `"attachments"`) {
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
			Attachments: []application.MailFileAttachment{{
				Name: "forward.txt", Content: []byte("forward"),
			}},
		},
	); err != nil || !responseDraftSent ||
		!strings.Contains(string(forwardBody), `"forward@example.test"`) ||
		strings.Contains(string(forwardBody), `"attachments"`) {
		t.Fatalf(
			"forward error = %v sent = %t request = %s",
			err,
			responseDraftSent,
			forwardBody,
		)
	}
	if !slices.Equal(
		attachedDrafts,
		[]string{"draft1", "senddraft1", "reply1", "forward1"},
	) {
		t.Fatalf("attachment draft sequence = %#v", attachedDrafts)
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
	); err != nil || !messagePermanentlyDeleted {
		t.Fatalf(
			"permanent delete error = %v requested = %t",
			err,
			messagePermanentlyDeleted,
		)
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
			End: "2026-08-01T11:00:00Z", OnlineMeeting: true,
		},
	)
	if err != nil || created.ID == "" ||
		created.OnlineMeetingProvider != "teams" ||
		created.OnlineMeetingJoinURL == "" {
		t.Fatalf("created = %#v error = %v", created, err)
	}
	subject := "Updated"
	start := "2026-08-01T10:00:00Z"
	end := "2026-08-01T11:00:00Z"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID:           calendar.Events[0].ID,
			ChangeKey:         calendar.Events[0].ChangeKey,
			Subject:           &subject,
			Start:             &start,
			End:               &end,
			ReplaceRecurrence: true,
			Recurrence: &application.CalendarRecurrence{
				Pattern:  application.CalendarRecurrenceWeekly,
				Interval: 1, DaysOfWeek: []string{"Monday"},
				NumberOfOccurrences: 4,
			},
		},
	)
	if err != nil || updated.ChangeKey == calendar.Events[0].ChangeKey ||
		!calendarRecurrenceUpdated {
		t.Fatalf("updated = %#v error = %v", updated, err)
	}
	updated, err = client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: calendar.Events[0].ID, ChangeKey: calendar.Events[0].ChangeKey,
			ReplaceRecurrence: true,
		},
	)
	if err != nil || updated.ChangeKey == calendar.Events[0].ChangeKey ||
		!calendarRecurrenceRemoved {
		t.Fatalf("recurrence removal = %#v error = %v", updated, err)
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

func TestGraphDeepMailFolderTraversalIsBoundedAndFollowsSafePages(t *testing.T) {
	t.Parallel()

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeGraphJSON(t, writer, map[string]string{
				"id": "user1", "mail": "reader@example.test",
			})
		case "/me/mailFolders/inbox":
			writeGraphJSON(t, writer, map[string]string{"id": "inbox1"})
		case "/me/mailFolders":
			if request.URL.Query().Get("$skiptoken") == "next" {
				writeGraphJSON(t, writer, map[string]any{
					"@odata.count": 2,
					"value": []graphFolder{
						{ID: "B", DisplayName: "B"},
					},
				})
			} else {
				writeGraphJSON(t, writer, map[string]any{
					"@odata.count":    2,
					"@odata.nextLink": server.URL + "/me/mailFolders?$skiptoken=next",
					"value": []graphFolder{
						{ID: "A", DisplayName: "A", ChildFolderCount: 2},
					},
				})
			}
		case "/me/mailFolders/A/childFolders":
			writeGraphJSON(t, writer, map[string]any{
				"@odata.count": 2,
				"value": []graphFolder{
					{
						ID: "C", ParentFolderID: "A",
						DisplayName: "C", ChildFolderCount: 1,
					},
					{ID: "D", ParentFolderID: "A", DisplayName: "D"},
				},
			})
		case "/me/mailFolders/C/childFolders":
			writeGraphJSON(t, writer, map[string]any{
				"@odata.count": 1,
				"value": []graphFolder{{
					ID: "E", ParentFolderID: "C", DisplayName: "E",
				}},
			})
		default:
			http.Error(writer, "unexpected request", http.StatusNotFound)
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
	for _, unsafe := range []string{
		"https://attacker.invalid/me/mailFolders?$skiptoken=next",
		server.URL + "/me/messages?$skiptoken=next",
	} {
		if _, _, err := client.folderContinuation(unsafe); err == nil {
			t.Fatalf("unsafe folder continuation accepted: %s", unsafe)
		}
	}

	page, err := client.ListMailFolders(
		t.Context(),
		application.MailFolderListInput{
			Parent: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "msgfolderroot",
			},
			Traversal: application.MailFolderTraversalDeep,
			Offset:    1,
			Limit:     3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalFolders != 5 ||
		page.IncludesLastItem ||
		len(page.Folders) != 3 ||
		page.Folders[0].DisplayName != "B" ||
		page.Folders[1].DisplayName != "C" ||
		page.Folders[2].DisplayName != "D" {
		t.Fatalf("deep folder page = %#v", page)
	}
}

func TestGraphDraftAttachmentFailureReportsPartialOutcome(t *testing.T) {
	t.Parallel()

	var draftCreated bool
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
		case "POST /me/messages":
			draftCreated = true
			message := graphTestMessage(false, `W/"draft-initial"`)
			message.ID = "draft1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/draft1/attachments":
			http.Error(writer, `{"error":{"code":"SyntheticFailure"}}`, http.StatusBadRequest)
		default:
			http.Error(writer, "unexpected synthetic Graph request", http.StatusNotFound)
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

	_, err = client.CreateMailDraft(
		t.Context(),
		application.MailDraftInput{
			To: []string{"to@example.test"}, Body: "body",
			Attachments: []application.MailFileAttachment{{
				Name: "fixture.txt", Content: []byte("fixture"),
			}},
		},
	)
	if !draftCreated || !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("draft created = %t error = %v", draftCreated, err)
	}
}

func TestGraphDraftSendFailureReportsPartialOutcome(t *testing.T) {
	t.Parallel()

	var draftCalls int
	var sendCalls int
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
		case "GET /me/messages/m1":
			writeGraphJSON(t, writer, graphTestMessage(false, `W/"m1"`))
		case "POST /me/messages/m1/createReply":
			draftCalls++
			message := graphTestMessage(false, `W/"draft1"`)
			message.ID = "draft1"
			writeGraphJSONStatus(t, writer, message, http.StatusCreated)
		case "POST /me/messages/draft1/send":
			sendCalls++
			http.Error(
				writer,
				`{"error":{"code":"SyntheticRejection"}}`,
				http.StatusBadRequest,
			)
		default:
			http.Error(writer, "unexpected synthetic Graph request", http.StatusNotFound)
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
	_, err = client.SendMail(t.Context(), application.MailSendInput{
		ComposeMode:        application.MailComposeReply,
		ReferenceMessageID: messageID,
		ReferenceChangeKey: encodeETag(`W/"m1"`),
		Body:               "synthetic reply",
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) ||
		draftCalls != 1 || sendCalls != 1 {
		t.Fatalf(
			"SendMail() error = %v, draft calls = %d, send calls = %d",
			err,
			draftCalls,
			sendCalls,
		)
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

func TestGraphRejectsDotSegmentOpaqueIdentifiers(t *testing.T) {
	t.Parallel()
	for _, id := range []string{".", ".."} {
		encoded, err := encodeMessageID(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeMessageID(encoded); err == nil {
			t.Errorf("decodeMessageID(%q) accepted dot segment %q", encoded, id)
		}
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

func requireGraphAttachment(
	t *testing.T,
	request *http.Request,
	name string,
) {
	t.Helper()
	var attachment struct {
		ODataType   string `json:"@odata.type"`
		Name        string `json:"name"`
		ContentData string `json:"contentBytes"`
	}
	if err := json.NewDecoder(request.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(attachment.ContentData)
	if err != nil ||
		attachment.ODataType != "#microsoft.graph.fileAttachment" ||
		attachment.Name != name ||
		string(content) != strings.TrimSuffix(name, ".txt") {
		t.Fatalf("attachment request = %+v content = %q", attachment, content)
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
