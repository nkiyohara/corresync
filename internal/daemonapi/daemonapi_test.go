package daemonapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/application"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
)

type fakeBackend struct {
	mailInput         application.MailListInput
	searchInput       application.MailSearchInput
	bodyInput         application.MailBodyInput
	attachmentInput   application.MailAttachmentInput
	draftInput        application.MailDraftInput
	sendInput         application.MailSendInput
	moveInput         application.MailMoveInput
	stateInput        application.MailReadStateInput
	deleteInput       application.MailDeleteInput
	folderInput       application.MailFolderListInput
	calendarListInput application.CalendarListInput
	createInput       application.CalendarCreateInput
	updateInput       application.CalendarUpdateInput
	cancelInput       application.CalendarCancelInput
	terminalInput     TerminalLoginInput
	commitToken       string
	caller            domain.Caller
}

func (backend *fakeBackend) DefaultAccount() domain.AccountID { return "work" }

func (backend *fakeBackend) SessionStatus(
	context.Context,
	domain.Caller,
) (SessionStatusResult, error) {
	return SessionStatusResult{
		Accounts: []SessionStatus{{Account: "work", State: "signed_out"}},
	}, nil
}
func (*fakeBackend) Login(_ context.Context, account domain.AccountID, _ domain.Caller) (LoginResult, error) {
	return LoginResult{Account: account, Authenticated: true, CapturedAt: time.Unix(2, 0)}, nil
}
func (backend *fakeBackend) TerminalLogin(_ context.Context, input TerminalLoginInput, caller domain.Caller) (TerminalLoginResult, error) {
	backend.terminalInput, backend.caller = input, caller
	if input.SessionID == "" {
		return TerminalLoginResult{
			Account: input.Account, SessionID: "tls1_" + strings.Repeat("a", 32), Status: "pending",
			View: &TerminalLoginView{
				Origin: "https://login.example", Title: "Sign in", Text: "Continue",
				Controls: []TerminalLoginControl{{ID: "control-1", Kind: "input", Name: "Email"}},
			},
		}, nil
	}
	return TerminalLoginResult{
		Account: input.Account, Status: "authenticated", CapturedAt: time.Unix(3, 0),
	}, nil
}
func (backend *fakeBackend) ListMail(_ context.Context, input application.MailListInput, caller domain.Caller) (application.MailPage, error) {
	backend.mailInput, backend.caller = input, caller
	return application.MailPage{Messages: []application.MailSummary{{ID: "message-1"}}}, nil
}
func (backend *fakeBackend) SearchMail(_ context.Context, input application.MailSearchInput, caller domain.Caller) (application.MailPage, error) {
	backend.searchInput, backend.caller = input, caller
	return application.MailPage{Messages: []application.MailSummary{{ID: "search-message-1"}}}, nil
}
func (backend *fakeBackend) ListMailFolders(_ context.Context, input application.MailFolderListInput, caller domain.Caller) (application.MailFolderPage, error) {
	backend.folderInput, backend.caller = input, caller
	return application.MailFolderPage{Folders: []application.MailFolderSummary{{ID: "folder-1"}}}, nil
}
func (backend *fakeBackend) GetMailBody(_ context.Context, input application.MailBodyInput, caller domain.Caller) (application.MailBodyAccess, error) {
	backend.bodyInput, backend.caller = input, caller
	return application.MailBodyAccess{
		Status: "completed", Body: &application.MailBody{ID: input.MessageID, Text: "Synthetic body"},
	}, nil
}
func (backend *fakeBackend) CommitMailBody(_ context.Context, token string, caller domain.Caller) (application.MailBodyAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailBodyAccess{
		Status: "completed", Body: &application.MailBody{ID: "message-1", Text: "Synthetic body"},
	}, nil
}
func (backend *fakeBackend) GetMailAttachment(_ context.Context, input application.MailAttachmentInput, caller domain.Caller) (application.MailAttachmentAccess, error) {
	backend.attachmentInput, backend.caller = input, caller
	return application.MailAttachmentAccess{
		Status: "completed", Attachment: &application.MailAttachment{
			MailAttachmentMetadata: application.MailAttachmentMetadata{ID: input.AttachmentID},
			ContentBase64:          "Zml4dHVyZQ==",
		},
	}, nil
}
func (backend *fakeBackend) CommitMailAttachment(_ context.Context, token string, caller domain.Caller) (application.MailAttachmentAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailAttachmentAccess{Status: "completed"}, nil
}
func (backend *fakeBackend) CreateMailDraft(_ context.Context, input application.MailDraftInput, caller domain.Caller) (application.MailDraftAccess, error) {
	backend.draftInput, backend.caller = input, caller
	return application.MailDraftAccess{
		Status: "completed", Draft: &application.MailDraft{ID: "draft-1", ChangeKey: "change-1"},
		Review: input.Review(),
	}, nil
}
func (backend *fakeBackend) CommitMailDraft(_ context.Context, token string, caller domain.Caller) (application.MailDraftAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailDraftAccess{
		Status: "completed", Draft: &application.MailDraft{ID: "draft-1", ChangeKey: "change-1"},
	}, nil
}
func (backend *fakeBackend) SendMail(_ context.Context, input application.MailSendInput, caller domain.Caller) (application.MailSendAccess, error) {
	backend.sendInput, backend.caller = input, caller
	return application.MailSendAccess{Status: "approval_required", Review: input.Review()}, nil
}
func (backend *fakeBackend) CommitMailSend(_ context.Context, token string, caller domain.Caller) (application.MailSendAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailSendAccess{Status: "sent", Sent: &application.MailSendResult{}}, nil
}
func (backend *fakeBackend) MoveMail(_ context.Context, input application.MailMoveInput, caller domain.Caller) (application.MailMoveAccess, error) {
	backend.moveInput, backend.caller = input, caller
	return application.MailMoveAccess{Status: "completed", Moved: &application.MailMoveResult{ID: "moved-1"}}, nil
}
func (backend *fakeBackend) CommitMailMove(_ context.Context, token string, caller domain.Caller) (application.MailMoveAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailMoveAccess{Status: "completed", Moved: &application.MailMoveResult{ID: "moved-1"}}, nil
}
func (backend *fakeBackend) SetMailReadState(_ context.Context, input application.MailReadStateInput, caller domain.Caller) (application.MailReadStateAccess, error) {
	backend.stateInput, backend.caller = input, caller
	return application.MailReadStateAccess{Status: "completed", Updated: &application.MailReadStateResult{State: application.MailReadStateRead}}, nil
}
func (backend *fakeBackend) CommitMailReadState(_ context.Context, token string, caller domain.Caller) (application.MailReadStateAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailReadStateAccess{
		Status: "completed", Updated: &application.MailReadStateResult{ID: "message-1", State: application.MailReadStateUnread},
	}, nil
}

func (backend *fakeBackend) DeleteMail(_ context.Context, input application.MailDeleteInput, caller domain.Caller) (application.MailDeleteAccess, error) {
	backend.deleteInput, backend.caller = input, caller
	return application.MailDeleteAccess{}, nil
}

func (backend *fakeBackend) CommitMailDelete(_ context.Context, token string, caller domain.Caller) (application.MailDeleteAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailDeleteAccess{}, nil
}
func (backend *fakeBackend) ListCalendar(_ context.Context, input application.CalendarListInput, caller domain.Caller) (application.CalendarPage, error) {
	backend.calendarListInput, backend.caller = input, caller
	return application.CalendarPage{
		Events: []application.CalendarEvent{{ID: "event-1", Start: input.Start, End: input.End}},
		Start:  input.Start, End: input.End,
	}, nil
}
func (backend *fakeBackend) CreateCalendar(_ context.Context, input application.CalendarCreateInput, caller domain.Caller) (application.CalendarCreateAccess, error) {
	backend.createInput, backend.caller = input, caller
	return application.CalendarCreateAccess{Status: "approval_required"}, nil
}
func (backend *fakeBackend) CommitCalendarCreate(_ context.Context, token string, caller domain.Caller) (application.CalendarCreateAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.CalendarCreateAccess{
		Status: "created", Created: &application.CalendarCreateResult{
			ID: "event-1", IsOnlineMeeting: true, OnlineMeetingProvider: "TeamsForBusiness",
			OnlineMeetingJoinURL: "https://teams.microsoft.com/l/meetup-join/synthetic",
		},
	}, nil
}
func (backend *fakeBackend) UpdateCalendar(_ context.Context, input application.CalendarUpdateInput, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	backend.updateInput, backend.caller = input, caller
	return application.CalendarUpdateAccess{Status: "approval_required"}, nil
}
func (backend *fakeBackend) CommitCalendarUpdate(_ context.Context, token string, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.CalendarUpdateAccess{
		Status: "updated", Updated: &application.CalendarUpdateResult{ID: "event-1", ChangeKey: "change-2"},
	}, nil
}
func (backend *fakeBackend) CancelCalendar(_ context.Context, input application.CalendarCancelInput, caller domain.Caller) (application.CalendarCancelAccess, error) {
	backend.cancelInput, backend.caller = input, caller
	return application.CalendarCancelAccess{Status: "approval_required"}, nil
}
func (backend *fakeBackend) CommitCalendarCancel(_ context.Context, token string, caller domain.Caller) (application.CalendarCancelAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.CalendarCancelAccess{
		Status: "cancelled", Cancelled: &application.CalendarCancelResult{ID: "event-1"},
	}, nil
}

func TestServerAuthenticatesBeforeDecoding(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, &fakeBackend{}, syntheticCredential("a"))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+requestHost+requestPath, strings.NewReader("not JSON"))
	request.Host = requestHost
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want unauthorized", recorder.Code)
	}
}

func TestValidateSessionStatusResultRejectsInvalidState(t *testing.T) {
	t.Parallel()

	capturedAt := time.Unix(1, 0)
	tests := []SessionStatusResult{
		{Accounts: []SessionStatus{
			{Account: "work", State: "signed_out"},
			{Account: "work", State: "signed_out"},
		}},
		{Accounts: []SessionStatus{
			{Account: "work", State: "signed_out"},
			{Account: "personal", State: "signed_out"},
		}},
		{Accounts: []SessionStatus{{Account: "work", State: "unknown"}}},
		{Accounts: []SessionStatus{{
			Account: "work", State: "authenticated", Authenticated: true,
		}}},
		{Accounts: []SessionStatus{{
			Account: "work", State: "signed_out", CapturedAt: &capturedAt,
		}}},
	}
	for index, result := range tests {
		if err := validateSessionStatusResult(result); err == nil {
			t.Fatalf("case %d unexpectedly passed: %+v", index, result)
		}
	}
}

func TestClientAndServerRoundTripOverLocalIPC(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	endpoint, err := localipc.ResolveInState(
		filepath.Join(root, "config.toml"), filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	credential, err := localipc.IssueCredential(endpoint)
	if err != nil {
		t.Fatalf("IssueCredential() error = %v", err)
	}
	backend := &fakeBackend{}
	server := newTestServer(t, backend, credential.Value())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
		_ = credential.Close()
		<-serveDone
	})

	client, err := NewClient(endpoint)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	status, err := client.Status(t.Context(), caller)
	if err != nil || status.DefaultAccount != "work" || status.ProtocolVersion != ProtocolVersion {
		t.Fatalf("Status() = %+v, %v", status, err)
	}
	login, err := client.Login(t.Context(), "work", caller)
	if err != nil || !login.Authenticated || login.Account != "work" {
		t.Fatalf("Login() = %+v, %v", login, err)
	}
	sessions, err := client.SessionStatus(t.Context(), caller)
	if err != nil ||
		len(sessions.Accounts) != 1 ||
		sessions.Accounts[0].Account != "work" ||
		sessions.Accounts[0].State != "signed_out" {
		t.Fatalf("SessionStatus() = %+v, %v", sessions, err)
	}
	terminalLogin, err := client.TerminalLogin(t.Context(), TerminalLoginInput{Account: "work"}, caller)
	if err != nil || terminalLogin.Status != "pending" || terminalLogin.View == nil ||
		len(terminalLogin.View.Controls) != 1 {
		t.Fatalf("TerminalLogin(start) = %+v, %v", terminalLogin, err)
	}
	terminalLogin, err = client.TerminalLogin(t.Context(), TerminalLoginInput{
		Account: "work", SessionID: terminalLogin.SessionID,
		Action: &TerminalLoginAction{Type: "key", ControlID: "control-1", Key: "a"},
	}, caller)
	if err != nil || terminalLogin.Status != "authenticated" || backend.terminalInput.Action.Key != "a" {
		t.Fatalf("TerminalLogin(continue) = %+v, %v; input=%+v", terminalLogin, err, backend.terminalInput)
	}
	page, err := client.ListMail(t.Context(), application.MailListInput{
		Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit: 25, TimeZone: "UTC",
	}, caller)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("ListMail() = %+v, %v", page, err)
	}
	if backend.caller != caller || backend.mailInput.Account != "work" {
		t.Fatalf("backend received caller=%+v input=%+v", backend.caller, backend.mailInput)
	}
	search, err := client.SearchMail(t.Context(), application.MailSearchInput{
		Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Query: "subject:synthetic", Limit: 25, TimeZone: "UTC",
	}, caller)
	if err != nil || len(search.Messages) != 1 || backend.searchInput.Query != "subject:synthetic" {
		t.Fatalf("SearchMail() = %+v, %v; backend input=%+v", search, err, backend.searchInput)
	}
	moved, err := client.MoveMail(t.Context(), application.MailMoveInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1",
		Destination: application.MailFolder{Kind: application.MailFolderOpaque, ID: "folder-1"},
	}, caller)
	if err != nil || moved.Moved == nil || moved.Moved.ID != "moved-1" || backend.moveInput.ChangeKey != "change-1" {
		t.Fatalf("MoveMail() = %+v, %v; backend input=%+v", moved, err, backend.moveInput)
	}
	readState, err := client.SetMailReadState(t.Context(), application.MailReadStateInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1", State: application.MailReadStateRead,
	}, caller)
	if err != nil || readState.Updated == nil || readState.Updated.State != application.MailReadStateRead ||
		backend.stateInput.ChangeKey != "change-1" {
		t.Fatalf("SetMailReadState() = %+v, %v; backend input=%+v", readState, err, backend.stateInput)
	}
	folders, err := client.ListMailFolders(t.Context(), application.MailFolderListInput{
		Account:   "work",
		Parent:    application.MailFolder{Kind: application.MailFolderDistinguished, ID: "msgfolderroot"},
		Traversal: application.MailFolderTraversalDeep,
		Limit:     100, TimeZone: "UTC",
	}, caller)
	if err != nil || len(folders.Folders) != 1 || backend.folderInput.Account != "work" {
		t.Fatalf("ListMailFolders() = %+v, %v; backend input=%+v", folders, err, backend.folderInput)
	}
	body, err := client.GetMailBody(t.Context(), application.MailBodyInput{
		Account: "work", MessageID: "message-1",
	}, caller)
	if err != nil || body.Body == nil || body.Body.Text != "Synthetic body" || backend.bodyInput.MessageID != "message-1" {
		t.Fatalf("GetMailBody() = %+v, %v; backend input=%+v", body, err, backend.bodyInput)
	}
	body, err = client.CommitMailBody(t.Context(), "opv1_body", caller)
	if err != nil || body.Status != "completed" || backend.commitToken != "opv1_body" {
		t.Fatalf("CommitMailBody() = %+v, %v; token=%q", body, err, backend.commitToken)
	}
	attachment, err := client.GetMailAttachment(t.Context(), application.MailAttachmentInput{
		Account: "work", AttachmentID: "attachment-1",
	}, caller)
	if err != nil || attachment.Attachment == nil ||
		attachment.Attachment.ContentBase64 != "Zml4dHVyZQ==" ||
		backend.attachmentInput.AttachmentID != "attachment-1" {
		t.Fatalf("GetMailAttachment() = %+v, %v; backend input=%+v", attachment, err, backend.attachmentInput)
	}
	attachment, err = client.CommitMailAttachment(t.Context(), "opv1_attachment", caller)
	if err != nil || attachment.Status != "completed" || backend.commitToken != "opv1_attachment" {
		t.Fatalf("CommitMailAttachment() = %+v, %v; token=%q", attachment, err, backend.commitToken)
	}
	draft, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{
		Account: "work", To: []string{"reader@example.test"}, Subject: "Synthetic draft", Body: "Synthetic body",
	}, caller)
	if err != nil || draft.Draft == nil || draft.Draft.ID != "draft-1" || backend.draftInput.Subject != "Synthetic draft" {
		t.Fatalf("CreateMailDraft() = %+v, %v; backend input=%+v", draft, err, backend.draftInput)
	}
	draft, err = client.CommitMailDraft(t.Context(), "opv1_draft", caller)
	if err != nil || draft.Status != "completed" || backend.commitToken != "opv1_draft" {
		t.Fatalf("CommitMailDraft() = %+v, %v; token=%q", draft, err, backend.commitToken)
	}
	send, err := client.SendMail(t.Context(), application.MailSendInput{
		Account: "work", To: []string{"reader@example.test"}, Subject: "Synthetic send", Body: "Synthetic body",
	}, caller)
	if err != nil || send.Status != "approval_required" || backend.sendInput.Subject != "Synthetic send" {
		t.Fatalf("SendMail() = %+v, %v; backend input=%+v", send, err, backend.sendInput)
	}
	send, err = client.CommitMailSend(t.Context(), "opv1_send", caller)
	if err != nil || send.Status != "sent" || send.Sent == nil || backend.commitToken != "opv1_send" {
		t.Fatalf("CommitMailSend() = %+v, %v; token=%q", send, err, backend.commitToken)
	}
	moved, err = client.CommitMailMove(t.Context(), "opv1_move", caller)
	if err != nil || moved.Moved == nil || backend.commitToken != "opv1_move" {
		t.Fatalf("CommitMailMove() = %+v, %v; token=%q", moved, err, backend.commitToken)
	}
	readState, err = client.CommitMailReadState(t.Context(), "opv1_state", caller)
	if err != nil || readState.Updated == nil || readState.Updated.State != application.MailReadStateUnread || backend.commitToken != "opv1_state" {
		t.Fatalf("CommitMailReadState() = %+v, %v; token=%q", readState, err, backend.commitToken)
	}
	calendarPage, err := client.ListCalendar(t.Context(), application.CalendarListInput{
		Account: "work", Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Start: "2026-07-20T09:00:00Z", End: "2026-07-20T10:00:00Z",
	}, caller)
	if err != nil || len(calendarPage.Events) != 1 || backend.calendarListInput.Start != "2026-07-20T09:00:00Z" {
		t.Fatalf("ListCalendar() = %+v, %v; backend input=%+v", calendarPage, err, backend.calendarListInput)
	}
	calendarAccess, err := client.CreateCalendar(t.Context(), application.CalendarCreateInput{
		Account:      "work",
		Calendar:     application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Subject:      "Synthetic event",
		Start:        "2026-07-20T09:00:00Z",
		End:          "2026-07-20T10:00:00Z",
		TeamsMeeting: true,
	}, caller)
	if err != nil || calendarAccess.Status != "approval_required" || backend.createInput.Subject != "Synthetic event" ||
		!backend.createInput.TeamsMeeting {
		t.Fatalf("CreateCalendar() = %+v, %v; backend input=%+v", calendarAccess, err, backend.createInput)
	}
	calendarAccess, err = client.CommitCalendarCreate(t.Context(), "opv1_synthetic", caller)
	if err != nil || calendarAccess.Status != "created" || calendarAccess.Created == nil ||
		calendarAccess.Created.OnlineMeetingJoinURL == "" {
		t.Fatalf("CommitCalendarCreate() = %+v, %v", calendarAccess, err)
	}
	updatedSubject := "Updated synthetic event"
	updateAccess, err := client.UpdateCalendar(t.Context(), application.CalendarUpdateInput{
		Account: "work", EventID: "event-1", ChangeKey: "change-1", Subject: &updatedSubject,
	}, caller)
	if err != nil || updateAccess.Status != "approval_required" || backend.updateInput.Subject == nil ||
		*backend.updateInput.Subject != updatedSubject {
		t.Fatalf("UpdateCalendar() = %+v, %v; backend input=%+v", updateAccess, err, backend.updateInput)
	}
	updateAccess, err = client.CommitCalendarUpdate(t.Context(), "opv1_synthetic", caller)
	if err != nil || updateAccess.Status != "updated" {
		t.Fatalf("CommitCalendarUpdate() = %+v, %v", updateAccess, err)
	}
	cancelAccess, err := client.CancelCalendar(t.Context(), application.CalendarCancelInput{
		Account: "work", EventID: "event-1", ChangeKey: "change-2",
	}, caller)
	if err != nil || cancelAccess.Status != "approval_required" || backend.cancelInput.ChangeKey != "change-2" {
		t.Fatalf("CancelCalendar() = %+v, %v; backend input=%+v", cancelAccess, err, backend.cancelInput)
	}
	cancelAccess, err = client.CommitCalendarCancel(t.Context(), "opv1_synthetic", caller)
	if err != nil || cancelAccess.Status != "cancelled" {
		t.Fatalf("CommitCalendarCancel() = %+v, %v", cancelAccess, err)
	}
	if err := client.Shutdown(t.Context(), caller); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("server did not publish the authenticated shutdown request")
	}
}

func TestClientInspectsAndStopsIncompatibleDaemon(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	endpoint, err := localipc.ResolveInState(
		filepath.Join(root, "config.toml"), filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	credential, err := localipc.IssueCredential(endpoint)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("IssueCredential() error = %v", err)
	}

	previousProtocol := ProtocolVersion - 1
	type observedRequest struct {
		version int
		method  Method
	}
	var observedMu sync.Mutex
	var observed []observedRequest
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var envelope requestEnvelope
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				t.Errorf("decode request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			observedMu.Lock()
			observed = append(observed, observedRequest{version: envelope.Version, method: envelope.Method})
			observedMu.Unlock()

			response := responseEnvelope{Version: previousProtocol, ID: envelope.ID}
			status := http.StatusOK
			var responseErr error
			switch {
			case envelope.Version != previousProtocol:
				status = http.StatusBadRequest
				response.Error = &Error{
					Code:    "invalid_request",
					Message: fmt.Sprintf("unsupported daemon protocol version %d", envelope.Version),
				}
			case envelope.Method == MethodStatus:
				encoded, encodeErr := json.Marshal(Status{
					ProtocolVersion: previousProtocol,
					Version:         "0.4.1",
					ProcessID:       123,
					StartedAt:       time.Unix(1, 0).UTC(),
					DefaultAccount:  "work",
					ConfigDigest:    strings.Repeat("a", 64),
				})
				response.Result = encoded
				responseErr = encodeErr
			case envelope.Method == MethodShutdown:
				response.Result = json.RawMessage(`{"stopping":true}`)
			default:
				t.Errorf("incompatible daemon received retried method %q", envelope.Method)
				response.Result = json.RawMessage(`{}`)
			}
			if responseErr != nil {
				t.Errorf("encode response: %v", responseErr)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", contentType)
			writer.WriteHeader(status)
			if err := json.NewEncoder(writer).Encode(response); err != nil {
				t.Errorf("write response: %v", err)
			}
		}),
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		_ = credential.Close()
		<-serveDone
	})

	client, err := NewClient(endpoint)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	statusResult, statusErr := client.Status(t.Context(), caller)
	var versionErr *ProtocolVersionError
	if !errors.As(statusErr, &versionErr) || !versionErr.RequestRejected() ||
		versionErr.ClientVersion != ProtocolVersion ||
		versionErr.DaemonVersion != previousProtocol {
		t.Fatalf("Status() error = %v, want rejected protocol mismatch", statusErr)
	}
	if statusResult.Version != "" {
		t.Fatalf("Status() result = %+v, want fail-closed zero value", statusResult)
	}
	owner, ownerErr := client.InspectOwner(t.Context(), caller)
	if !errors.As(ownerErr, &versionErr) {
		t.Fatalf("InspectOwner() error = %v, want protocol mismatch", ownerErr)
	}
	renderedOwner := fmt.Sprintf("%+v %#v", owner, owner)
	if strings.Contains(renderedOwner, credential.Value()) {
		t.Fatal("formatted owner snapshot exposed its credential")
	}
	encodedOwner, err := json.Marshal(owner)
	if err == nil || len(encodedOwner) != 0 ||
		!strings.Contains(err.Error(), "cannot be serialized") {
		t.Fatalf("json.Marshal(owner) = %s, %v", encodedOwner, err)
	}
	ownerStatus := owner.Status()
	if ownerStatus.Version != "0.4.1" ||
		ownerStatus.ProtocolVersion != previousProtocol {
		t.Fatalf("InspectOwner() status = %+v", ownerStatus)
	}

	observedMu.Lock()
	beforeLogin := len(observed)
	observedMu.Unlock()
	if _, err := client.Login(t.Context(), "work", caller); !errors.As(err, &versionErr) {
		t.Fatalf("Login() error = %v, want protocol mismatch", err)
	}
	observedMu.Lock()
	loginRequests := len(observed) - beforeLogin
	observedMu.Unlock()
	if loginRequests != 1 {
		t.Fatalf("Login() made %d requests across a protocol mismatch, want 1", loginRequests)
	}

	if err := client.Shutdown(t.Context(), caller); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	want := []observedRequest{
		{version: ProtocolVersion, method: MethodStatus},
		{version: previousProtocol, method: MethodStatus},
		{version: ProtocolVersion, method: MethodStatus},
		{version: previousProtocol, method: MethodStatus},
		{version: ProtocolVersion, method: MethodLogin},
		{version: ProtocolVersion, method: MethodStatus},
		{version: previousProtocol, method: MethodStatus},
		{version: previousProtocol, method: MethodShutdown},
	}
	if len(observed) != len(want) {
		t.Fatalf("observed requests = %+v, want %+v", observed, want)
	}
	for index := range want {
		if observed[index] != want[index] {
			t.Fatalf("observed request %d = %+v, want %+v", index, observed[index], want[index])
		}
	}
}

func TestServerRejectsUnknownEnvelopeFields(t *testing.T) {
	t.Parallel()

	token := syntheticCredential("b")
	server := newTestServer(t, &fakeBackend{}, token)
	body := `{"version":7,"id":"abcdefghijklmnop","method":"status","caller":{"surface":"cli","instance":"process-1"},"params":{},"extra":true}`
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+requestHost+requestPath, strings.NewReader(body))
	request.Host = requestHost
	request.Header.Set("Authorization", authorizationType+token)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want bad request", recorder.Code)
	}
	var response responseEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error == nil || response.Error.Code != "invalid_request" {
		t.Fatalf("unexpected response: %+v, %v", response, err)
	}
}

func newTestServer(t *testing.T, backend Backend, token string) *Server {
	t.Helper()
	server, err := NewServer(backend, ServerOptions{
		Version: "dev", ProcessID: 123, StartedAt: time.Unix(1, 0), Credential: token,
		ConfigDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func syntheticCredential(character string) string {
	return "ipc1_" + strings.Repeat(character, 43)
}
