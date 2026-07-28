package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestDecodeMailAttachmentsRejectsAggregateBeforeApplicationUse(t *testing.T) {
	t.Parallel()

	chunk := strings.Repeat("x", application.MaxMailAttachmentTotalBytes/2+1)
	encoded := base64.StdEncoding.EncodeToString([]byte(chunk))
	_, err := decodeMailAttachments([]MailFileAttachmentInput{
		{Name: "one.bin", ContentBase64: encoded},
		{Name: "two.bin", ContentBase64: encoded},
	})
	if err == nil {
		t.Fatal("decodeMailAttachments() accepted an oversized aggregate")
	}
}

func TestMutationToolsHaveNoAllAccountsInput(t *testing.T) {
	t.Parallel()

	server, err := New(
		&fakeBackend{},
		Options{Version: "dev", Instance: "test-server"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			continue
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), "allAccounts") {
			t.Fatalf("mutation tool %q exposes allAccounts", tool.Name)
		}
	}
}

type fakeBackend struct {
	mailInput            application.MailListInput
	searchInput          application.MailSearchInput
	searchAllInput       application.MailProjectionInput
	folderInput          application.MailFolderListInput
	bodyInput            application.MailBodyInput
	attachmentInput      application.MailAttachmentInput
	approvalToken        string
	calendarInput        application.CalendarListInput
	agendaInput          application.AgendaProjectionInput
	calendarCreate       application.CalendarCreateInput
	calendarUpdate       application.CalendarUpdateInput
	calendarCancel       application.CalendarCancelInput
	caller               domain.Caller
	mailPage             application.MailPage
	mailProjection       application.MailProjectionPage
	folderPage           application.MailFolderPage
	bodyAccess           application.MailBodyAccess
	attachmentAccess     application.MailAttachmentAccess
	draftInput           application.MailDraftInput
	draftAccess          application.MailDraftAccess
	sendInput            application.MailSendInput
	sendAccess           application.MailSendAccess
	moveInput            application.MailMoveInput
	moveAccess           application.MailMoveAccess
	readStateInput       application.MailReadStateInput
	readStateAccess      application.MailReadStateAccess
	calendarPage         application.CalendarPage
	agendaPage           application.AgendaProjectionPage
	calendarAccess       application.CalendarCreateAccess
	calendarUpdateAccess application.CalendarUpdateAccess
	calendarCancelAccess application.CalendarCancelAccess
	discoveryAddress     string
	discoveryResult      application.AccountDiscoveryResult
	accountReference     string
	accountCatalog       application.AccountCatalog
	accountView          application.AccountView
	err                  error
}

func (backend *fakeBackend) DefaultAccount() domain.AccountID { return "work" }
func (backend *fakeBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	if reference == "" {
		return backend.DefaultAccount(), nil
	}
	return domain.AccountID(reference), nil
}

func (backend *fakeBackend) DiscoverAccounts(
	_ context.Context,
	address string,
) (application.AccountDiscoveryResult, error) {
	backend.discoveryAddress = address
	return backend.discoveryResult, backend.err
}

func (backend *fakeBackend) ListAccounts(
	context.Context,
) (application.AccountCatalog, error) {
	return backend.accountCatalog, backend.err
}

func (backend *fakeBackend) ShowAccount(
	_ context.Context,
	reference string,
) (application.AccountView, error) {
	backend.accountReference = reference
	return backend.accountView, backend.err
}

func (backend *fakeBackend) ListMail(
	_ context.Context,
	input application.MailListInput,
	caller domain.Caller,
) (application.MailPage, error) {
	backend.mailInput = input
	backend.caller = caller
	return backend.mailPage, backend.err
}

func (backend *fakeBackend) SearchMail(
	_ context.Context,
	input application.MailSearchInput,
	caller domain.Caller,
) (application.MailPage, error) {
	backend.searchInput = input
	backend.caller = caller
	return backend.mailPage, backend.err
}

func (backend *fakeBackend) SearchAllMail(
	_ context.Context,
	input application.MailProjectionInput,
	caller domain.Caller,
) (application.MailProjectionPage, error) {
	backend.searchAllInput = input
	backend.caller = caller
	return backend.mailProjection, backend.err
}

func (backend *fakeBackend) ListMailFolders(
	_ context.Context,
	input application.MailFolderListInput,
	caller domain.Caller,
) (application.MailFolderPage, error) {
	backend.folderInput = input
	backend.caller = caller
	return backend.folderPage, backend.err
}

func (backend *fakeBackend) ListCalendar(
	_ context.Context,
	input application.CalendarListInput,
	caller domain.Caller,
) (application.CalendarPage, error) {
	backend.calendarInput = input
	backend.caller = caller
	return backend.calendarPage, backend.err
}

func (backend *fakeBackend) ListAgenda(
	_ context.Context,
	input application.AgendaProjectionInput,
	caller domain.Caller,
) (application.AgendaProjectionPage, error) {
	backend.agendaInput = input
	backend.caller = caller
	return backend.agendaPage, backend.err
}

func (backend *fakeBackend) CreateCalendar(
	_ context.Context,
	input application.CalendarCreateInput,
	caller domain.Caller,
) (application.CalendarCreateAccess, error) {
	backend.calendarCreate = input
	backend.caller = caller
	return backend.calendarAccess, backend.err
}

func (backend *fakeBackend) CommitCalendarCreate(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCreateAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.calendarAccess, backend.err
}

func (backend *fakeBackend) UpdateCalendar(
	_ context.Context,
	input application.CalendarUpdateInput,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	backend.calendarUpdate = input
	backend.caller = caller
	return backend.calendarUpdateAccess, backend.err
}

func (backend *fakeBackend) CommitCalendarUpdate(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.calendarUpdateAccess, backend.err
}

func (backend *fakeBackend) CancelCalendar(
	_ context.Context,
	input application.CalendarCancelInput,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	backend.calendarCancel = input
	backend.caller = caller
	return backend.calendarCancelAccess, backend.err
}

func (backend *fakeBackend) CommitCalendarCancel(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.calendarCancelAccess, backend.err
}

func (backend *fakeBackend) GetMailBody(
	_ context.Context,
	input application.MailBodyInput,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	backend.caller = caller
	backend.bodyInput = input
	return backend.bodyAccess, backend.err
}

func (backend *fakeBackend) CommitMailBody(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	backend.caller = caller
	backend.approvalToken = token
	return backend.bodyAccess, backend.err
}

func (backend *fakeBackend) GetMailAttachment(
	_ context.Context,
	input application.MailAttachmentInput,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	backend.caller = caller
	backend.attachmentInput = input
	return backend.attachmentAccess, backend.err
}

func (backend *fakeBackend) CommitMailAttachment(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	backend.caller = caller
	backend.approvalToken = token
	return backend.attachmentAccess, backend.err
}

func (backend *fakeBackend) CreateMailDraft(
	_ context.Context,
	input application.MailDraftInput,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	backend.draftInput = input
	backend.caller = caller
	return backend.draftAccess, backend.err
}

func (backend *fakeBackend) CommitMailDraft(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.draftAccess, backend.err
}

func (backend *fakeBackend) SendMail(
	_ context.Context,
	input application.MailSendInput,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	backend.sendInput = input
	backend.caller = caller
	return backend.sendAccess, backend.err
}

func (backend *fakeBackend) CommitMailSend(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.sendAccess, backend.err
}

func (backend *fakeBackend) MoveMail(
	_ context.Context,
	input application.MailMoveInput,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	backend.moveInput = input
	backend.caller = caller
	return backend.moveAccess, backend.err
}

func (backend *fakeBackend) CommitMailMove(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.moveAccess, backend.err
}

func (backend *fakeBackend) SetMailReadState(
	_ context.Context,
	input application.MailReadStateInput,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	backend.readStateInput = input
	backend.caller = caller
	return backend.readStateAccess, backend.err
}

func (backend *fakeBackend) CommitMailReadState(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.readStateAccess, backend.err
}

func (backend *fakeBackend) DeleteMail(
	_ context.Context, input application.MailDeleteInput, _ domain.Caller,
) (application.MailDeleteAccess, error) {
	return application.MailDeleteAccess{Review: input.Review()}, nil
}

func (backend *fakeBackend) CommitMailDelete(
	context.Context, string, domain.Caller,
) (application.MailDeleteAccess, error) {
	return application.MailDeleteAccess{}, nil
}

func TestMailListToolUsesDefaultsAndReturnsStructuredOutput(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{mailPage: application.MailPage{
		Messages:         []application.MailSummary{{ID: "message-1", Subject: "Quarterly plan"}},
		TotalItemsInView: 1,
		IncludesLastItem: true,
	}}
	server, err := New(backend, Options{Version: "v0.1.0", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.1.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	initializeResult := clientSession.InitializeResult()
	if initializeResult == nil || !strings.Contains(initializeResult.Instructions, "check, find, read") ||
		!strings.Contains(initializeResult.Instructions, "mail_list") {
		t.Fatalf("server instructions do not describe mail discovery: %+v", initializeResult)
	}

	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	mailTool := toolNamed(tools.Tools, "mail_list")
	if len(tools.Tools) != 29 || mailTool == nil {
		t.Fatalf("unexpected tools: %+v", tools.Tools)
	}
	annotation := mailTool.Annotations
	if annotation == nil || !annotation.ReadOnlyHint || annotation.DestructiveHint == nil || *annotation.DestructiveHint {
		t.Fatalf("unsafe or missing annotations: %+v", annotation)
	}
	if !strings.HasPrefix(mailTool.Description, "Use when the user asks to check Outlook") {
		t.Fatalf("mail_list description does not front-load discovery guidance: %q", mailTool.Description)
	}

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "mail_list",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() returned tool error: %+v", result.Content)
	}
	if backend.mailInput.Account != "work" || backend.mailInput.Folder.ID != "inbox" ||
		backend.mailInput.Limit != 25 || backend.mailInput.TimeZone != "UTC" {
		t.Fatalf("unexpected backend input: %+v", backend.mailInput)
	}
	if backend.caller != (domain.Caller{Surface: "mcp", Instance: "test-server"}) {
		t.Fatalf("unexpected caller: %+v", backend.caller)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["totalItemsInView"] != float64(1) {
		t.Fatalf("unexpected structured output: %#v", result.StructuredContent)
	}
}

func TestMailSearchAllToolUsesProjectionDefaults(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	tool := toolNamed(tools.Tools, "mail_search_all")
	if tool == nil || tool.Annotations == nil ||
		!tool.Annotations.ReadOnlyHint ||
		tool.Annotations.DestructiveHint == nil ||
		*tool.Annotations.DestructiveHint {
		t.Fatalf("unsafe cross-account search annotations: %+v", tool)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_search_all",
		Arguments: map[string]any{
			"query": "subject:synthetic",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_search_all failed: result=%+v error=%v", result, err)
	}
	if backend.searchAllInput.Folder.ID != "inbox" ||
		backend.searchAllInput.Query != "subject:synthetic" ||
		backend.searchAllInput.Limit != 25 ||
		backend.searchAllInput.TimeZone != "UTC" {
		t.Fatalf(
			"unexpected cross-account search input: %+v",
			backend.searchAllInput,
		)
	}
}

func TestMailSearchToolUsesBoundedDefaults(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{mailPage: application.MailPage{
		Messages: []application.MailSummary{{ID: "message-1", Subject: "Synthetic"}},
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	searchTool := toolNamed(tools.Tools, "mail_search")
	if searchTool == nil || searchTool.Annotations == nil || !searchTool.Annotations.ReadOnlyHint ||
		searchTool.Annotations.DestructiveHint == nil || *searchTool.Annotations.DestructiveHint {
		t.Fatalf("unsafe search annotations: %+v", searchTool)
	}
	if !strings.HasPrefix(searchTool.Description, "Use when the user asks to find") {
		t.Fatalf("mail_search description does not front-load discovery guidance: %q", searchTool.Description)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_search", Arguments: map[string]any{"query": "subject:synthetic"},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_search failed: result=%+v error=%v", result, err)
	}
	if backend.searchInput.Account != "work" || backend.searchInput.Folder.ID != "inbox" ||
		backend.searchInput.Query != "subject:synthetic" || backend.searchInput.Limit != 25 ||
		backend.searchInput.TimeZone != "UTC" {
		t.Fatalf("unexpected search input: %+v", backend.searchInput)
	}
}

func TestMailMoveToolsKeepVersionedPreviewAndCommitSeparate(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{moveAccess: application.MailMoveAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, name := range []string{"mail_move", "mail_move_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe move annotations for %s: %+v", name, tool)
		}
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_move", Arguments: map[string]any{
			"messageId": "message-1", "changeKey": "change-1", "destinationId": "folder-1",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_move failed: result=%+v error=%v", result, err)
	}
	if backend.moveInput.Account != "work" || backend.moveInput.MessageID != "message-1" ||
		backend.moveInput.ChangeKey != "change-1" || backend.moveInput.Destination.ID != "folder-1" {
		t.Fatalf("unexpected move input: %+v", backend.moveInput)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_move_commit", Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("mail_move_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
	}
}

func TestMailReadStateToolsExposeOnlyClosedStateUpdate(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{readStateAccess: application.MailReadStateAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	tool := toolNamed(tools.Tools, "mail_set_read_state")
	if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
		tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Fatalf("unsafe read-state annotations: %+v", tool)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_set_read_state", Arguments: map[string]any{
			"messageId": "message-1", "changeKey": "change-1", "state": "unread",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_set_read_state failed: result=%+v error=%v", result, err)
	}
	if backend.readStateInput.Account != "work" || backend.readStateInput.MessageID != "message-1" ||
		backend.readStateInput.State != application.MailReadStateUnread {
		t.Fatalf("unexpected read-state input: %+v", backend.readStateInput)
	}
}

func TestMailFolderListToolUsesBoundedDefaults(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{folderPage: application.MailFolderPage{
		Folders:      []application.MailFolderSummary{{ID: "folder-1", DisplayName: "Synthetic"}},
		TotalFolders: 1, IncludesLastItem: true,
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_list_folders", Arguments: map[string]any{},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_list_folders failed: result=%+v error=%v", result, err)
	}
	if backend.folderInput.Account != "work" || backend.folderInput.Parent.ID != "msgfolderroot" ||
		backend.folderInput.Traversal != application.MailFolderTraversalDeep ||
		backend.folderInput.Limit != 100 || backend.folderInput.TimeZone != "UTC" {
		t.Fatalf("unexpected folder input: %+v", backend.folderInput)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["totalFolders"] != float64(1) {
		t.Fatalf("unexpected structured output: %#v", result.StructuredContent)
	}
}

func TestCalendarListToolMapsRequiredWindow(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{calendarPage: application.CalendarPage{
		Events: []application.CalendarEvent{{ID: "event-1", Subject: "Planning"}},
		Start:  "2026-07-17T00:00:00Z",
		End:    "2026-07-18T00:00:00Z",
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_list",
		Arguments: map[string]any{
			"start": "2026-07-17T00:00:00Z",
			"end":   "2026-07-18T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() returned tool error: %+v", result.Content)
	}
	if backend.calendarInput.Account != "work" || backend.calendarInput.Calendar.ID != "calendar" ||
		backend.calendarInput.Start != "2026-07-17T00:00:00Z" {
		t.Fatalf("unexpected calendar input: %+v", backend.calendarInput)
	}
}

func TestAgendaListToolUsesProjectionDefaults(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	tool := toolNamed(tools.Tools, "agenda_list")
	if tool == nil || tool.Annotations == nil ||
		!tool.Annotations.ReadOnlyHint ||
		tool.Annotations.DestructiveHint == nil ||
		*tool.Annotations.DestructiveHint {
		t.Fatalf("unsafe agenda annotations: %+v", tool)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "agenda_list",
		Arguments: map[string]any{
			"start": "2026-07-17T00:00:00Z",
			"end":   "2026-07-18T00:00:00Z",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("agenda_list failed: result=%+v error=%v", result, err)
	}
	if backend.agendaInput.DisplayTimeZone != "UTC" ||
		backend.agendaInput.Limit != 50 ||
		backend.agendaInput.Start != "2026-07-17T00:00:00Z" {
		t.Fatalf("unexpected agenda input: %+v", backend.agendaInput)
	}
}

func TestCalendarCreateToolsKeepMandatoryPreviewAndCommitSeparate(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{calendarAccess: application.CalendarCreateAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, name := range []string{"calendar_create", "calendar_create_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint ||
			tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
			t.Fatalf("unsafe calendar create annotations for %s: %+v", name, tool)
		}
	}
	commitTool := toolNamed(tools.Tools, "calendar_create_commit")
	if classification := commitTool.Meta["io.github.nkiyohara.corresync/data-classification"]; classification != "private-untrusted-sensitive" {
		t.Fatalf("calendar create commit classification = %v", classification)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_create",
		Arguments: map[string]any{
			"subject":           "Synthetic event",
			"body":              "Fixture data only",
			"start":             "2026-07-20T09:00:00Z",
			"end":               "2026-07-20T10:00:00Z",
			"location":          "Room Example",
			"requiredAttendees": []string{"alice@example.invalid"},
			"optionalAttendees": []string{"bob@example.invalid"},
			"teamsMeeting":      true,
			"allDay":            true,
			"timeZone":          "GMT Standard Time",
			"reminder": map[string]any{
				"enabled": true, "minutesBeforeStart": 30,
			},
			"recurrence": map[string]any{
				"pattern": "weekly", "interval": 1,
				"daysOfWeek": []string{"Monday"}, "numberOfOccurrences": 4,
			},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("calendar_create failed: result=%+v error=%v", result, err)
	}
	if backend.calendarCreate.Account != "work" || backend.calendarCreate.Calendar.ID != "calendar" ||
		backend.calendarCreate.Subject != "Synthetic event" || len(backend.calendarCreate.RequiredAttendees) != 1 ||
		len(backend.calendarCreate.OptionalAttendees) != 1 || !backend.calendarCreate.TeamsMeeting ||
		!backend.calendarCreate.AllDay || backend.calendarCreate.TimeZone != "GMT Standard Time" ||
		backend.calendarCreate.Reminder == nil || backend.calendarCreate.Recurrence == nil {
		t.Fatalf("unexpected calendar create input: %+v", backend.calendarCreate)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "calendar_create_commit",
		Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("calendar_create_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
	}
}

func TestCalendarUpdateToolsExposeOnlyClosedVersionedPatch(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{calendarUpdateAccess: application.CalendarUpdateAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, name := range []string{"calendar_update", "calendar_update_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe calendar update annotations for %s: %+v", name, tool)
		}
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_update",
		Arguments: map[string]any{
			"eventId": "event-1", "changeKey": "change-1",
			"subject": "Updated synthetic event", "location": "",
			"start": "2026-07-20T09:00:00Z", "end": "2026-07-20T10:00:00Z",
			"timeZone": "UTC", "allDay": false,
			"reminder":          map[string]any{"enabled": true, "minutesBeforeStart": 10},
			"replaceAttendees":  true,
			"requiredAttendees": []string{"alice@example.invalid"},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("calendar_update failed: result=%+v error=%v", result, err)
	}
	if backend.calendarUpdate.Account != "work" || backend.calendarUpdate.EventID != "event-1" ||
		backend.calendarUpdate.Subject == nil || *backend.calendarUpdate.Subject != "Updated synthetic event" ||
		backend.calendarUpdate.Location == nil || *backend.calendarUpdate.Location != "" ||
		backend.calendarUpdate.Start == nil || backend.calendarUpdate.End == nil ||
		backend.calendarUpdate.TimeZone == nil || backend.calendarUpdate.AllDay == nil ||
		backend.calendarUpdate.Reminder == nil || !backend.calendarUpdate.ReplaceAttendees ||
		len(backend.calendarUpdate.RequiredAttendees) != 1 {
		t.Fatalf("unexpected calendar update input: %+v", backend.calendarUpdate)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "calendar_update_commit",
		Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("calendar_update_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
	}
}

func TestCalendarCancelCommitAloneIsAnnotatedDestructive(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{calendarCancelAccess: application.CalendarCancelAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	previewTool := toolNamed(tools.Tools, "calendar_cancel")
	commitTool := toolNamed(tools.Tools, "calendar_cancel_commit")
	if previewTool == nil || previewTool.Annotations == nil ||
		previewTool.Annotations.DestructiveHint == nil || *previewTool.Annotations.DestructiveHint {
		t.Fatalf("cancel preview should not itself be destructive: %+v", previewTool)
	}
	if commitTool == nil || commitTool.Annotations == nil ||
		commitTool.Annotations.DestructiveHint == nil || !*commitTool.Annotations.DestructiveHint {
		t.Fatalf("cancel commit must be destructive: %+v", commitTool)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "calendar_cancel",
		Arguments: map[string]any{"eventId": "event-1", "changeKey": "change-1"},
	})
	if err != nil || result.IsError {
		t.Fatalf("calendar_cancel failed: result=%+v error=%v", result, err)
	}
	if backend.calendarCancel.Account != "work" || backend.calendarCancel.EventID != "event-1" ||
		backend.calendarCancel.ChangeKey != "change-1" {
		t.Fatalf("unexpected calendar cancel input: %+v", backend.calendarCancel)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "calendar_cancel_commit",
		Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("calendar_cancel_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
	}
}

func TestMailBodyToolsKeepPreviewAndCommitSeparate(t *testing.T) {
	t.Parallel()

	body := application.MailBody{ID: "message-1", Text: "untrusted body"}
	backend := &fakeBackend{bodyAccess: application.MailBodyAccess{Status: "completed", Body: &body}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_get_body",
		Arguments: map[string]any{
			"messageId": "message-1",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_get_body failed: result=%+v error=%v", result, err)
	}
	if backend.bodyInput.Account != "work" || backend.bodyInput.MessageID != "message-1" {
		t.Fatalf("unexpected body input: %+v", backend.bodyInput)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "mail_get_body_commit",
		Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_get_body_commit failed: result=%+v error=%v", result, err)
	}
	if backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("commit token was not passed to backend: %q", backend.approvalToken)
	}
}

func TestMailAttachmentToolsUseBoundedSensitiveRead(t *testing.T) {
	t.Parallel()

	attachment := application.MailAttachment{
		MailAttachmentMetadata: application.MailAttachmentMetadata{ID: "attachment-1", Name: "fixture.txt"},
		ContentBase64:          "Zml4dHVyZQ==",
	}
	backend := &fakeBackend{attachmentAccess: application.MailAttachmentAccess{
		Status: "completed", Attachment: &attachment,
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	for _, name := range []string{"mail_get_attachment", "mail_get_attachment_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe attachment tool %s: %+v", name, tool)
		}
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_get_attachment", Arguments: map[string]any{"attachmentId": "attachment-1"},
	})
	if err != nil || result.IsError || backend.attachmentInput.AttachmentID != "attachment-1" {
		t.Fatalf("mail_get_attachment failed: result=%+v input=%+v error=%v", result, backend.attachmentInput, err)
	}
}

func TestMailDraftToolsAreSaveOnlyWrites(t *testing.T) {
	t.Parallel()

	draft := application.MailDraft{ID: "draft-1"}
	backend := &fakeBackend{draftAccess: application.MailDraftAccess{
		Status: "completed", Draft: &draft,
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	draftTool := toolNamed(tools.Tools, "mail_create_draft")
	if draftTool == nil || draftTool.Annotations == nil || draftTool.Annotations.ReadOnlyHint ||
		draftTool.Annotations.DestructiveHint == nil || *draftTool.Annotations.DestructiveHint {
		t.Fatalf("unsafe draft annotations: %+v", draftTool)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_create_draft",
		Arguments: map[string]any{
			"to": []string{"alice@example.invalid"}, "subject": "Draft", "body": "Hello",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_create_draft failed: result=%+v error=%v", result, err)
	}
	if backend.draftInput.Account != "work" || backend.draftInput.Subject != "Draft" {
		t.Fatalf("unexpected draft input: %+v", backend.draftInput)
	}
}

func TestMailSendToolsRequireSeparateExternalCommit(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{sendAccess: application.MailSendAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	previewTool := toolNamed(tools.Tools, "mail_send")
	commitTool := toolNamed(tools.Tools, "mail_send_commit")
	for _, tool := range []*mcp.Tool{previewTool, commitTool} {
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe send annotations: %+v", tool)
		}
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_send",
		Arguments: map[string]any{
			"to": []string{"alice@example.invalid"}, "subject": "Send", "body": "<p>Hello</p>",
			"bodyFormat": "html", "attachments": []map[string]any{{
				"name": "fixture.txt", "contentType": "text/plain", "contentBase64": "Zml4dHVyZQ==",
			}},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("mail_send failed: result=%+v error=%v", result, err)
	}
	if backend.sendInput.Account != "work" || backend.sendInput.Subject != "Send" ||
		backend.sendInput.BodyFormat != application.MailBodyHTML || len(backend.sendInput.Attachments) != 1 ||
		string(backend.sendInput.Attachments[0].Content) != "fixture" {
		t.Fatalf("unexpected send input: %+v", backend.sendInput)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_send_commit", Arguments: map[string]any{"token": "opv1_synthetic"}, // gitleaks:allow -- synthetic fixture
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("mail_send_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
	}
}

func TestMailListToolPropagatesApplicationErrorsAsToolErrors(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{err: errors.New("account is unavailable")}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "dev"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "mail_list",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() protocol error = %v", err)
	}
	if !result.IsError {
		t.Fatalf("CallTool() IsError = false, want true: %+v", result)
	}
}

func TestAccountToolsUseTypedReadOnlyBackend(t *testing.T) {
	t.Parallel()
	account := application.AccountView{
		ID: "acc_00000000000000000000000000000001", Alias: "work",
		Address: "reader@example.invalid",
		Mail: &application.AccountRouteView{
			Provider: domain.ProviderMicrosoftOWA,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "origin", Value: "https://outlook.example.invalid"},
			},
		},
		Calendar: &application.AccountRouteView{
			Provider: domain.ProviderMicrosoftOWA,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "origin", Value: "https://outlook.example.invalid"},
			},
		},
		IsDefault: true,
	}
	backend := &fakeBackend{
		discoveryResult: application.AccountDiscoveryResult{
			Address: "reader@example.invalid",
			Domain:  "example.invalid",
		},
		accountCatalog: application.AccountCatalog{Accounts: []application.AccountView{account}},
		accountView:    account,
	}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)

	for _, call := range []struct {
		name      string
		arguments map[string]any
	}{
		{"account_discover", map[string]any{"address": "reader@example.invalid"}},
		{"account_list", map[string]any{}},
		{"account_show", map[string]any{"account": "work"}},
	} {
		result, callErr := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name: call.name, Arguments: call.arguments,
		})
		if callErr != nil || result.IsError {
			t.Fatalf("%s failed: result=%+v error=%v", call.name, result, callErr)
		}
	}
	if backend.discoveryAddress != "reader@example.invalid" ||
		backend.accountReference != "work" {
		t.Fatalf(
			"typed account inputs were not forwarded: address=%q reference=%q",
			backend.discoveryAddress,
			backend.accountReference,
		)
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"account_discover", "account_list", "account_show"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s is missing read-only annotations: %+v", name, tool)
		}
	}
}

func TestNewValidatesDependencies(t *testing.T) {
	t.Parallel()

	if _, err := New(nil, Options{Version: "dev", Instance: "test"}); err == nil {
		t.Fatal("New() unexpectedly accepted a nil backend")
	}
	if _, err := New(&fakeBackend{}, Options{Instance: "test"}); err == nil {
		t.Fatal("New() unexpectedly accepted an empty version")
	}
	if _, err := New(&fakeBackend{}, Options{Version: "dev"}); err == nil {
		t.Fatal("New() unexpectedly accepted an empty instance")
	}
}

func toolNamed(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func connectTestClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "dev"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}
