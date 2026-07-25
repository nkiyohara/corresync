package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/application"
	"github.com/nkiyohara/owa-bridge/internal/approval"
	"github.com/nkiyohara/owa-bridge/internal/browser"
	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
)

type adapterTestBackend struct {
	mailCalls             int
	searchCalls           int
	moveCalls             int
	stateCalls            int
	bodyCommits           int
	draftCommits          int
	draftReview           application.MailReview
	sendCommits           int
	sendReview            application.MailReview
	calendarCommits       int
	calendarReview        application.CalendarCreateReview
	calendarUpdateCommits int
	calendarUpdateReview  application.CalendarUpdateReview
	calendarCancelCommits int
	calendarCancelReview  application.CalendarCancelReview
}

func (*adapterTestBackend) DefaultAccount() domain.AccountID { return "work" }

func (*adapterTestBackend) SessionStatus(
	context.Context,
	domain.Caller,
) (daemonapi.SessionStatusResult, error) {
	return daemonapi.SessionStatusResult{
		Accounts: []daemonapi.SessionStatus{{Account: "work", State: "signed_out"}},
	}, nil
}
func (*adapterTestBackend) Login(_ context.Context, account domain.AccountID, _ domain.Caller) (daemonapi.LoginResult, error) {
	return daemonapi.LoginResult{
		Account: account, Authenticated: true, CapturedAt: time.Unix(2, 0),
	}, nil
}
func (backend *adapterTestBackend) ListMail(_ context.Context, _ application.MailListInput, _ domain.Caller) (application.MailPage, error) {
	backend.mailCalls++
	return application.MailPage{
		Messages: []application.MailSummary{{ID: "message-1", Subject: "Synthetic"}},
	}, nil
}
func (backend *adapterTestBackend) SearchMail(context.Context, application.MailSearchInput, domain.Caller) (application.MailPage, error) {
	backend.searchCalls++
	return application.MailPage{Messages: []application.MailSummary{{ID: "search-message-1"}}}, nil
}
func (*adapterTestBackend) ListMailFolders(context.Context, application.MailFolderListInput, domain.Caller) (application.MailFolderPage, error) {
	return application.MailFolderPage{Folders: []application.MailFolderSummary{{ID: "folder-1"}}}, nil
}
func (*adapterTestBackend) GetMailBody(_ context.Context, input application.MailBodyInput, _ domain.Caller) (application.MailBodyAccess, error) {
	return application.MailBodyAccess{
		Status: "approval_required",
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "mail.get_body", Effect: domain.EffectSensitiveRead,
				Account: input.Account, Digest: strings.Repeat("e", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitMailBody(context.Context, string, domain.Caller) (application.MailBodyAccess, error) {
	backend.bodyCommits++
	return application.MailBodyAccess{
		Status: "completed", Body: &application.MailBody{ID: "message-1", Text: "Synthetic body"},
	}, nil
}
func (*adapterTestBackend) GetMailAttachment(_ context.Context, input application.MailAttachmentInput, _ domain.Caller) (application.MailAttachmentAccess, error) {
	return application.MailAttachmentAccess{
		Status: "completed", Attachment: &application.MailAttachment{
			MailAttachmentMetadata: application.MailAttachmentMetadata{ID: input.AttachmentID},
			ContentBase64:          "Zml4dHVyZQ==",
		},
	}, nil
}
func (*adapterTestBackend) CommitMailAttachment(context.Context, string, domain.Caller) (application.MailAttachmentAccess, error) {
	return application.MailAttachmentAccess{Status: "completed"}, nil
}
func (backend *adapterTestBackend) CreateMailDraft(_ context.Context, input application.MailDraftInput, _ domain.Caller) (application.MailDraftAccess, error) {
	backend.draftReview = input.Review()
	return application.MailDraftAccess{
		Status: "approval_required", Review: backend.draftReview,
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "mail.create_draft", Effect: domain.EffectReversibleWrite,
				Account: input.Account, Digest: strings.Repeat("f", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitMailDraft(context.Context, string, domain.Caller) (application.MailDraftAccess, error) {
	backend.draftCommits++
	return application.MailDraftAccess{
		Status: "completed", Draft: &application.MailDraft{ID: "draft-1", ChangeKey: "draft-change-1"},
		Review: backend.draftReview,
	}, nil
}
func (backend *adapterTestBackend) SendMail(_ context.Context, input application.MailSendInput, _ domain.Caller) (application.MailSendAccess, error) {
	backend.sendReview = input.Review()
	return application.MailSendAccess{
		Status: "approval_required", Review: backend.sendReview,
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "mail.send", Effect: domain.EffectExternalWrite,
				Account: input.Account, Digest: strings.Repeat("a", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitMailSend(context.Context, string, domain.Caller) (application.MailSendAccess, error) {
	backend.sendCommits++
	return application.MailSendAccess{
		Status: "sent", Sent: &application.MailSendResult{}, Review: backend.sendReview,
	}, nil
}
func (backend *adapterTestBackend) MoveMail(context.Context, application.MailMoveInput, domain.Caller) (application.MailMoveAccess, error) {
	backend.moveCalls++
	return application.MailMoveAccess{Status: "completed", Moved: &application.MailMoveResult{ID: "moved-1"}}, nil
}
func (*adapterTestBackend) CommitMailMove(context.Context, string, domain.Caller) (application.MailMoveAccess, error) {
	return application.MailMoveAccess{}, nil
}
func (backend *adapterTestBackend) SetMailReadState(context.Context, application.MailReadStateInput, domain.Caller) (application.MailReadStateAccess, error) {
	backend.stateCalls++
	return application.MailReadStateAccess{Status: "completed", Updated: &application.MailReadStateResult{State: application.MailReadStateRead}}, nil
}
func (*adapterTestBackend) CommitMailReadState(context.Context, string, domain.Caller) (application.MailReadStateAccess, error) {
	return application.MailReadStateAccess{}, nil
}

func (*adapterTestBackend) DeleteMail(context.Context, application.MailDeleteInput, domain.Caller) (application.MailDeleteAccess, error) {
	return application.MailDeleteAccess{}, nil
}

func (*adapterTestBackend) CommitMailDelete(context.Context, string, domain.Caller) (application.MailDeleteAccess, error) {
	return application.MailDeleteAccess{}, nil
}
func (*adapterTestBackend) ListCalendar(context.Context, application.CalendarListInput, domain.Caller) (application.CalendarPage, error) {
	return application.CalendarPage{}, nil
}
func (backend *adapterTestBackend) CreateCalendar(_ context.Context, input application.CalendarCreateInput, _ domain.Caller) (application.CalendarCreateAccess, error) {
	backend.calendarReview = input.Review()
	return application.CalendarCreateAccess{
		Status: "approval_required", Review: backend.calendarReview,
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "calendar.create", Effect: domain.EffectExternalWrite,
				Account: input.Account, Digest: strings.Repeat("b", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitCalendarCreate(context.Context, string, domain.Caller) (application.CalendarCreateAccess, error) {
	backend.calendarCommits++
	return application.CalendarCreateAccess{
		Status: "created",
		Created: &application.CalendarCreateResult{
			ID: "event-created-1", ChangeKey: "change-created-1", IsOnlineMeeting: true,
			OnlineMeetingProvider: "TeamsForBusiness",
			OnlineMeetingJoinURL:  "https://teams.microsoft.com/l/meetup-join/synthetic",
		},
		Review: backend.calendarReview,
	}, nil
}
func (backend *adapterTestBackend) UpdateCalendar(_ context.Context, input application.CalendarUpdateInput, _ domain.Caller) (application.CalendarUpdateAccess, error) {
	backend.calendarUpdateReview = input.Review()
	return application.CalendarUpdateAccess{
		Status: "approval_required", Review: backend.calendarUpdateReview,
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "calendar.update", Effect: domain.EffectExternalWrite,
				Account: input.Account, Digest: strings.Repeat("c", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitCalendarUpdate(context.Context, string, domain.Caller) (application.CalendarUpdateAccess, error) {
	backend.calendarUpdateCommits++
	return application.CalendarUpdateAccess{
		Status:  "updated",
		Updated: &application.CalendarUpdateResult{ID: "event-created-1", ChangeKey: "change-updated-1"},
		Review:  backend.calendarUpdateReview,
	}, nil
}
func (backend *adapterTestBackend) CancelCalendar(_ context.Context, input application.CalendarCancelInput, _ domain.Caller) (application.CalendarCancelAccess, error) {
	backend.calendarCancelReview = input.Review()
	return application.CalendarCancelAccess{
		Status: "approval_required", Review: backend.calendarCancelReview,
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "calendar.cancel", Effect: domain.EffectDestructiveWrite,
				Account: input.Account, Digest: strings.Repeat("d", 64),
			},
		},
	}, nil
}
func (backend *adapterTestBackend) CommitCalendarCancel(context.Context, string, domain.Caller) (application.CalendarCancelAccess, error) {
	backend.calendarCancelCommits++
	return application.CalendarCancelAccess{
		Status:    "cancelled",
		Cancelled: &application.CalendarCancelResult{ID: backend.calendarCancelReview.EventID},
		Review:    backend.calendarCancelReview,
	}, nil
}

func TestCLIAndMCPAdaptersUseDaemonWithoutLaunchingBrowser(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}
	endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatalf("config.Fingerprint() error = %v", err)
	}
	backend := &adapterTestBackend{}
	stopServer := startAdapterTestDaemon(t, endpoint, configDigest, backend)
	t.Cleanup(stopServer)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &stderr, buildinfo.Current())
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("adapter unexpectedly launched a browser outside the daemon")
		return nil, errors.New("unreachable")
	}
	command := mailListCommand{Folder: "inbox", Limit: 25, TimeZone: "UTC", JSON: true}
	if err := command.Run(app); err != nil {
		t.Fatalf("mail list Run() error = %v", err)
	}
	if backend.mailCalls != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"message-1"`)) {
		t.Fatalf("mail calls=%d output=%s", backend.mailCalls, stdout.String())
	}
	stdout.Reset()
	search := mailSearchCommand{
		Folder: "inbox", Query: "subject:synthetic", Limit: 25, TimeZone: "UTC", JSON: true,
	}
	if err := search.Run(app); err != nil {
		t.Fatalf("mail search Run() error = %v", err)
	}
	if backend.searchCalls != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"search-message-1"`)) {
		t.Fatalf("search calls=%d output=%s", backend.searchCalls, stdout.String())
	}
	stdout.Reset()
	move := mailMoveCommand{
		MessageID: "message-1", ChangeKey: "change-1", DestinationID: "folder-1", JSON: true,
	}
	if err := move.Run(app); err != nil {
		t.Fatalf("mail move Run() error = %v", err)
	}
	if backend.moveCalls != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"moved-1"`)) {
		t.Fatalf("move calls=%d output=%s", backend.moveCalls, stdout.String())
	}
	stdout.Reset()
	mark := mailMarkCommand{
		MessageID: "message-1", ChangeKey: "change-1", State: "read", JSON: true,
	}
	if err := mark.Run(app); err != nil {
		t.Fatalf("mail mark Run() error = %v", err)
	}
	if backend.stateCalls != 1 || !bytes.Contains(stdout.Bytes(), []byte(`"state": "read"`)) {
		t.Fatalf("read-state calls=%d output=%s", backend.stateCalls, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	body := mailBodyCommand{MessageID: "message-1"}
	if err := body.Run(app); err != nil {
		t.Fatalf("mail body preview Run() error = %v", err)
	}
	if backend.bodyCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("private body was not read")) {
		t.Fatalf("body preview commits=%d output=%s", backend.bodyCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	body.Approve = true
	if err := body.Run(app); err != nil {
		t.Fatalf("mail body commit Run() error = %v", err)
	}
	if backend.bodyCommits != 1 || !bytes.Contains(stdout.Bytes(), []byte("Synthetic body")) ||
		!bytes.Contains(stderr.Bytes(), []byte("Reading the private body")) {
		t.Fatalf("body commits=%d stdout=%q stderr=%s", backend.bodyCommits, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	draft := mailDraftCommand{To: []string{"alice@example.invalid"}, Subject: "Synthetic draft"}
	if err := draft.Run(app); err != nil {
		t.Fatalf("mail draft preview Run() error = %v", err)
	}
	if backend.draftCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("no draft was saved")) {
		t.Fatalf("draft preview commits=%d output=%s", backend.draftCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	draft.Approve = true
	if err := draft.Run(app); err != nil {
		t.Fatalf("mail draft commit Run() error = %v", err)
	}
	if backend.draftCommits != 1 || !bytes.Contains(stdout.Bytes(), []byte("draft-1")) ||
		!bytes.Contains(stderr.Bytes(), []byte("Saving this exact draft")) {
		t.Fatalf("draft commits=%d stdout=%s stderr=%s", backend.draftCommits, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	send := mailSendCommand{
		To: []string{"alice@example.invalid"}, Subject: "Synthetic send",
	}
	if err := send.Run(app); err != nil {
		t.Fatalf("mail send preview Run() error = %v", err)
	}
	if backend.sendCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("nothing was sent")) {
		t.Fatalf("preview commits=%d output=%s", backend.sendCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	send.Approve = true
	if err := send.Run(app); err != nil {
		t.Fatalf("mail send commit Run() error = %v", err)
	}
	if backend.sendCommits != 1 || !bytes.Contains(stdout.Bytes(), []byte("Sent message")) ||
		!bytes.Contains(stderr.Bytes(), []byte("Committing this exact send")) {
		t.Fatalf(
			"commit count=%d stdout=%s stderr=%s",
			backend.sendCommits, stdout.String(), stderr.String(),
		)
	}
	stdout.Reset()
	stderr.Reset()
	calendarCreate := calendarCreateCommand{
		Subject:           "Synthetic event",
		Start:             "2026-07-20T09:00:00Z",
		End:               "2026-07-20T10:00:00Z",
		RequiredAttendees: []string{"alice@example.invalid"},
		TeamsMeeting:      true,
	}
	if err := calendarCreate.Run(app); err != nil {
		t.Fatalf("calendar create preview Run() error = %v", err)
	}
	if backend.calendarCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("no event was created")) {
		t.Fatalf("calendar preview commits=%d output=%s", backend.calendarCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	calendarCreate.Approve = true
	if err := calendarCreate.Run(app); err != nil {
		t.Fatalf("calendar create commit Run() error = %v", err)
	}
	if backend.calendarCommits != 1 || !backend.calendarReview.TeamsMeeting ||
		!bytes.Contains(stdout.Bytes(), []byte("event-created-1")) ||
		!bytes.Contains(stdout.Bytes(), []byte("https://teams.microsoft.com/")) ||
		!bytes.Contains(stderr.Bytes(), []byte("Committing this exact calendar event")) {
		t.Fatalf(
			"calendar commit count=%d stdout=%s stderr=%s",
			backend.calendarCommits, stdout.String(), stderr.String(),
		)
	}
	stdout.Reset()
	stderr.Reset()
	calendarUpdate := calendarUpdateCommand{
		EventID: "event-created-1", ChangeKey: "change-created-1",
		Subject: "Updated synthetic event",
	}
	if err := calendarUpdate.Run(app); err != nil {
		t.Fatalf("calendar update preview Run() error = %v", err)
	}
	if backend.calendarUpdateCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("no event was updated")) {
		t.Fatalf("calendar update preview commits=%d output=%s", backend.calendarUpdateCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	calendarUpdate.Approve = true
	if err := calendarUpdate.Run(app); err != nil {
		t.Fatalf("calendar update commit Run() error = %v", err)
	}
	if backend.calendarUpdateCommits != 1 || !bytes.Contains(stdout.Bytes(), []byte("change-updated-1")) ||
		!bytes.Contains(stderr.Bytes(), []byte("Committing this exact calendar update")) {
		t.Fatalf(
			"calendar update commit count=%d stdout=%s stderr=%s",
			backend.calendarUpdateCommits, stdout.String(), stderr.String(),
		)
	}
	stdout.Reset()
	stderr.Reset()
	calendarCancel := calendarCancelCommand{
		EventID: "event-created-1", ChangeKey: "change-updated-1",
	}
	if err := calendarCancel.Run(app); err != nil {
		t.Fatalf("calendar cancel preview Run() error = %v", err)
	}
	if backend.calendarCancelCommits != 0 || !bytes.Contains(stdout.Bytes(), []byte("nothing was cancelled")) {
		t.Fatalf("calendar cancel preview commits=%d output=%s", backend.calendarCancelCommits, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	calendarCancel.Approve = true
	if err := calendarCancel.Run(app); err != nil {
		t.Fatalf("calendar cancel commit Run() error = %v", err)
	}
	if backend.calendarCancelCommits != 1 || !bytes.Contains(stdout.Bytes(), []byte("event-created-1")) ||
		!bytes.Contains(stderr.Bytes(), []byte("destructive calendar cancellation")) {
		t.Fatalf(
			"calendar cancel commit count=%d stdout=%s stderr=%s",
			backend.calendarCancelCommits, stdout.String(), stderr.String(),
		)
	}

	mcpBackend, err := newDaemonMCPBackend(app)
	if err != nil {
		t.Fatalf("newDaemonMCPBackend() error = %v", err)
	}
	t.Cleanup(func() { _ = mcpBackend.Close() })
	if mcpBackend.DefaultAccount() != "work" {
		t.Fatalf("MCP default account = %q", mcpBackend.DefaultAccount())
	}
}

func startAdapterTestDaemon(
	t *testing.T,
	endpoint localipc.Endpoint,
	configDigest string,
	backend daemonapi.Backend,
) func() {
	t.Helper()
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("localipc.Listen() error = %v", err)
	}
	credential, err := localipc.IssueCredential(endpoint)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("IssueCredential() error = %v", err)
	}
	server, err := daemonapi.NewServer(backend, daemonapi.ServerOptions{
		Version: "dev", ProcessID: 123, StartedAt: time.Unix(1, 0),
		Credential: credential.Value(), ConfigDigest: configDigest,
	})
	if err != nil {
		_ = credential.Close()
		_ = listener.Close()
		t.Fatalf("NewServer() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := errors.Join(
			server.Shutdown(ctx), listener.Close(), credential.Close(), <-serveDone,
		); err != nil {
			t.Errorf("daemon cleanup error = %v", err)
		}
	}
}
