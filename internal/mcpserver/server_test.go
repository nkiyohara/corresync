package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func TestAuthenticationFailureHasStructuredMCPErrorAndJSONFallback(t *testing.T) {
	t.Parallel()

	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateReauthenticationNeeded,
		application.AuthenticationReasonSessionExpired,
		"acc_00000000000000000000000000000001",
		"work",
		application.AuthenticationServiceMail,
		domain.ProviderMicrosoftOWA,
	)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{err: application.NewAuthenticationActionError(action)}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_list",
		Arguments: map[string]any{
			"account": "work",
		},
	})
	if err != nil {
		t.Fatalf("CallTool() protocol error = %v", err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !result.IsError || !ok ||
		structured["code"] != string(application.AuthenticationCodeReauthenticationNeed) ||
		structured["alias"] != "work" ||
		structured["service"] != string(application.AuthenticationServiceMail) {
		t.Fatalf("CallTool() result = %+v", result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("fallback content = %+v", result.Content)
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("fallback content type = %T", result.Content[0])
	}
	var fallback application.AuthenticationActionRequired
	if err := json.Unmarshal([]byte(textContent.Text), &fallback); err != nil ||
		fallback.Code != action.Code || fallback.Account != action.Account {
		t.Fatalf("fallback = %q, error = %v", textContent.Text, err)
	}
}

type fakeBackend struct {
	mailInput            application.MailListInput
	searchInput          application.MailSearchInput
	searchAllInput       application.MailProjectionInput
	folderInput          application.MailFolderListInput
	calendarFolderInput  application.CalendarFolderListInput
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
	calendarFolderPage   application.CalendarFolderPage
	bodyAccess           application.MailBodyAccess
	attachmentAccess     application.MailAttachmentAccess
	draftInput           application.MailDraftInput
	draftAccess          application.MailDraftAccess
	sendInput            application.MailSendInput
	sendAccess           application.MailSendAccess
	sendDraftInput       application.MailDraftSendInput
	sendDraftAccess      application.MailDraftSendAccess
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
	sessionStatus        application.SessionStatusResult
	accountAddInput      application.AccountAddInput
	accountRenameInput   application.AccountRenameInput
	accountRemoveInput   application.AccountRemoveInput
	accountChangeAccess  application.AccountChangeAccess
	settingsInput        application.SettingsUpdateInput
	settingsView         application.SettingsView
	settingsAccess       application.SettingsChangeAccess
	monitorStatus        application.MonitorStatus
	monitorListInput     application.MonitorEventListInput
	monitorPage          application.MonitorEventPage
	monitorAckInput      application.MonitorAcknowledgeInput
	monitorEvent         application.MonitorEvent
	taskListsInput       application.TaskListInput
	taskReadInput        application.TaskReadInput
	taskProjectionInput  application.TaskProjectionInput
	taskGetInput         application.TaskGetInput
	taskSearchInput      application.TaskSearchInput
	taskSyncInput        application.TaskSyncInput
	taskCreateInput      application.TaskCreateInput
	taskUpdateInput      application.TaskUpdateInput
	taskStateInput       application.TaskStateInput
	taskListsPage        application.TaskListPage
	taskPage             application.TaskPage
	taskProjectionPage   application.TaskProjectionPage
	task                 application.Task
	taskChangePage       application.TaskChangePage
	taskAccess           application.TaskWriteAccess
	taskAction           string
	err                  error
}

func (backend *fakeBackend) DefaultAccount() domain.AccountID { return "work" }
func (backend *fakeBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	if reference == "" {
		return backend.DefaultAccount(), nil
	}
	if backend.accountView.Alias == reference {
		return backend.accountView.ID, nil
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

func (backend *fakeBackend) ShowSettings(
	context.Context,
) (application.SettingsView, error) {
	return backend.settingsView, backend.err
}

func (backend *fakeBackend) PreviewSettingsUpdate(
	_ context.Context,
	input application.SettingsUpdateInput,
	caller domain.Caller,
) (application.SettingsChangeAccess, error) {
	backend.settingsInput, backend.caller = input, caller
	return backend.settingsAccess, backend.err
}

func (backend *fakeBackend) CommitSettingsUpdate(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.SettingsChangeAccess, error) {
	backend.approvalToken, backend.caller = token, caller
	return backend.settingsAccess, backend.err
}

func (backend *fakeBackend) SessionStatus(
	_ context.Context,
	caller domain.Caller,
) (application.SessionStatusResult, error) {
	backend.caller = caller
	return backend.sessionStatus, backend.err
}

func (backend *fakeBackend) PreviewAccountAdd(
	_ context.Context,
	input application.AccountAddInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.accountAddInput, backend.caller = input, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) CommitAccountAdd(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.approvalToken, backend.caller = token, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) PreviewAccountRename(
	_ context.Context,
	input application.AccountRenameInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.accountRenameInput, backend.caller = input, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) CommitAccountRename(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.approvalToken, backend.caller = token, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) PreviewAccountRemove(
	_ context.Context,
	input application.AccountRemoveInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.accountRemoveInput, backend.caller = input, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) CommitAccountRemove(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	backend.approvalToken, backend.caller = token, caller
	return backend.accountChangeAccess, backend.err
}

func (backend *fakeBackend) MonitorStatus(
	_ context.Context,
	_ domain.AccountID,
	caller domain.Caller,
) (application.MonitorStatus, error) {
	backend.caller = caller
	return backend.monitorStatus, backend.err
}

func (backend *fakeBackend) ListMonitorEvents(
	_ context.Context,
	input application.MonitorEventListInput,
	caller domain.Caller,
) (application.MonitorEventPage, error) {
	backend.monitorListInput = input
	backend.caller = caller
	return backend.monitorPage, backend.err
}

func (backend *fakeBackend) AcknowledgeMonitorEvent(
	_ context.Context,
	input application.MonitorAcknowledgeInput,
	caller domain.Caller,
) (application.MonitorEvent, error) {
	backend.monitorAckInput = input
	backend.caller = caller
	return backend.monitorEvent, backend.err
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

func (backend *fakeBackend) ListCalendarFolders(
	_ context.Context,
	input application.CalendarFolderListInput,
	caller domain.Caller,
) (application.CalendarFolderPage, error) {
	backend.calendarFolderInput = input
	backend.caller = caller
	return backend.calendarFolderPage, backend.err
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

func (backend *fakeBackend) ListTaskLists(
	_ context.Context,
	input application.TaskListInput,
	caller domain.Caller,
) (application.TaskListPage, error) {
	backend.taskListsInput, backend.caller = input, caller
	return backend.taskListsPage, backend.err
}

func (backend *fakeBackend) ListTasks(
	_ context.Context,
	input application.TaskReadInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	backend.taskReadInput, backend.caller = input, caller
	return backend.taskPage, backend.err
}

func (backend *fakeBackend) ListAllTasks(
	_ context.Context,
	input application.TaskProjectionInput,
	caller domain.Caller,
) (application.TaskProjectionPage, error) {
	backend.taskProjectionInput, backend.caller = input, caller
	return backend.taskProjectionPage, backend.err
}

func (backend *fakeBackend) GetTask(
	_ context.Context,
	input application.TaskGetInput,
	caller domain.Caller,
) (application.Task, error) {
	backend.taskGetInput, backend.caller = input, caller
	return backend.task, backend.err
}

func (backend *fakeBackend) SearchTasks(
	_ context.Context,
	input application.TaskSearchInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	backend.taskSearchInput, backend.caller = input, caller
	return backend.taskPage, backend.err
}

func (backend *fakeBackend) SyncTasks(
	_ context.Context,
	input application.TaskSyncInput,
	caller domain.Caller,
) (application.TaskChangePage, error) {
	backend.taskSyncInput, backend.caller = input, caller
	return backend.taskChangePage, backend.err
}

func (backend *fakeBackend) CreateTask(
	_ context.Context,
	input application.TaskCreateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskCreateInput, backend.taskAction, backend.caller = input, "create", caller
	return backend.taskAccess, backend.err
}

func (backend *fakeBackend) UpdateTask(
	_ context.Context,
	input application.TaskUpdateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskUpdateInput, backend.taskAction, backend.caller = input, "update", caller
	return backend.taskAccess, backend.err
}

func (backend *fakeBackend) CompleteTask(
	_ context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "complete", caller
	return backend.taskAccess, backend.err
}

func (backend *fakeBackend) ReopenTask(
	_ context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "reopen", caller
	return backend.taskAccess, backend.err
}

func (backend *fakeBackend) DeleteTask(
	_ context.Context,
	input application.TaskDeleteInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "delete", caller
	return backend.taskAccess, backend.err
}

func (backend *fakeBackend) CommitTaskCreate(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTask("commit_create", token, caller)
}

func (backend *fakeBackend) CommitTaskUpdate(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTask("commit_update", token, caller)
}

func (backend *fakeBackend) CommitTaskComplete(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTask("commit_complete", token, caller)
}

func (backend *fakeBackend) CommitTaskReopen(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTask("commit_reopen", token, caller)
}

func (backend *fakeBackend) CommitTaskDelete(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTask("commit_delete", token, caller)
}

func (backend *fakeBackend) commitTask(
	action, token string,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	backend.taskAction, backend.approvalToken, backend.caller = action, token, caller
	return backend.taskAccess, backend.err
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

func (backend *fakeBackend) SendMailDraft(
	_ context.Context,
	input application.MailDraftSendInput,
	caller domain.Caller,
) (application.MailDraftSendAccess, error) {
	backend.sendDraftInput = input
	backend.caller = caller
	return backend.sendDraftAccess, backend.err
}

func (backend *fakeBackend) CommitMailSendDraft(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MailDraftSendAccess, error) {
	backend.approvalToken = token
	backend.caller = caller
	return backend.sendDraftAccess, backend.err
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
	if len(tools.Tools) != 61 || mailTool == nil || toolNamed(tools.Tools, "task_list") == nil {
		t.Fatalf("unexpected tools: %+v", tools.Tools)
	}
	annotation := mailTool.Annotations
	if annotation == nil || !annotation.ReadOnlyHint || annotation.DestructiveHint == nil || *annotation.DestructiveHint {
		t.Fatalf("unsafe or missing annotations: %+v", annotation)
	}
	if !strings.HasPrefix(mailTool.Description, "Use when the user asks to check mail") {
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

func TestMonitoringToolsAndResourcesCannotEnableOrBroadenPolicy(t *testing.T) {
	t.Parallel()
	account := domain.AccountID("acc_00000000000000000000000000000001")
	eventID := "evt_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	backend := &fakeBackend{
		monitorStatus: application.MonitorStatus{
			Account: account, Alias: "work", Mode: domain.MonitorQueue,
			CollectionEnabled: true,
		},
		monitorPage: application.MonitorEventPage{
			Events: []application.MonitorEvent{{
				ID: eventID, Account: account, AccountAlias: "work",
				Provider: domain.ProviderJMAP, SourceObjectID: "synthetic",
				Trust:    application.MonitorTrustMarker,
				Delivery: application.MonitorDeliveryQueue, State: "pending",
				DeliveryCount: 1,
			}},
			Limit: 50, Total: 1,
		},
		monitorEvent: application.MonitorEvent{
			ID: eventID, Account: account, AccountAlias: "work",
			Provider: domain.ProviderJMAP, SourceObjectID: "synthetic",
			Trust:    application.MonitorTrustMarker,
			Delivery: application.MonitorDeliveryQueue, State: "acknowledged",
		},
	}
	server, err := New(
		backend,
		Options{Version: "v0.1.0", Instance: "test-server"},
	)
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"monitor_status", "events_list", "event_acknowledge"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil {
			t.Fatalf("missing tool %q", name)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"mode", "runner", "egress", "filter", "purge", "approve"} {
			if strings.Contains(strings.ToLower(string(schema)), forbidden) {
				t.Fatalf("tool %q exposes forbidden configuration input %q: %s", name, forbidden, schema)
			}
		}
	}
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "monitor_status", Arguments: map[string]any{"account": string(account)},
	}); err != nil {
		t.Fatalf("monitor_status error = %v", err)
	}
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "events_list", Arguments: map[string]any{"account": string(account)},
	}); err != nil {
		t.Fatalf("events_list error = %v", err)
	}
	if backend.monitorListInput.Limit != 50 {
		t.Fatalf("events_list limit = %d", backend.monitorListInput.Limit)
	}
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "event_acknowledge",
		Arguments: map[string]any{
			"account": string(account),
			"eventId": eventID,
		},
	}); err != nil {
		t.Fatalf("event_acknowledge error = %v", err)
	}
	templates, err := client.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates.ResourceTemplates) != 2 {
		t.Fatalf("resource templates = %+v", templates.ResourceTemplates)
	}
	resource, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: "corresync://events/" + string(account),
	})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	if len(resource.Contents) != 1 ||
		!strings.Contains(resource.Contents[0].Text, application.MonitorTrustMarker) {
		t.Fatalf("resource contents = %+v", resource.Contents)
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

func TestCalendarFolderListToolUsesBoundedDefaults(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{calendarFolderPage: application.CalendarFolderPage{
		Calendars: []application.CalendarFolderSummary{{
			ID: "calendar-1", DisplayName: "Synthetic", IsDefault: true,
			CanEdit: true, AccessRole: "owner",
		}},
		TotalCalendars: 1, IncludesLastItem: true,
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientSession := connectTestClient(t, server)
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "calendar_list_folders", Arguments: map[string]any{},
	})
	if err != nil || result.IsError {
		t.Fatalf("calendar_list_folders failed: result=%+v error=%v", result, err)
	}
	if backend.calendarFolderInput.Account != "work" ||
		backend.calendarFolderInput.Limit != application.MaxCalendarFolderPageSize {
		t.Fatalf("unexpected calendar folder input: %+v", backend.calendarFolderInput)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["totalCalendars"] != float64(1) {
		t.Fatalf("unexpected structured output: %#v", result.StructuredContent)
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
			"onlineMeeting":     true,
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
		len(backend.calendarCreate.OptionalAttendees) != 1 ||
		!backend.calendarCreate.OnlineMeeting ||
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
			"replaceRecurrence": true,
			"recurrence": map[string]any{
				"pattern": "weekly", "interval": 1,
				"daysOfWeek":          []string{"Monday"},
				"numberOfOccurrences": 4,
			},
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
		backend.calendarUpdate.Reminder == nil ||
		!backend.calendarUpdate.ReplaceRecurrence ||
		backend.calendarUpdate.Recurrence == nil ||
		backend.calendarUpdate.Recurrence.NumberOfOccurrences != 4 ||
		!backend.calendarUpdate.ReplaceAttendees ||
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

func TestMailSendDraftToolsBindExactIdentityAndSeparateCommit(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{sendDraftAccess: application.MailDraftSendAccess{
		Status: "approval_required",
	}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatal(err)
	}
	clientSession := connectTestClient(t, server)
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mail_send_draft", "mail_send_draft_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe saved draft send tool %q: %+v", name, tool)
		}
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_send_draft",
		Arguments: map[string]any{
			"draftId": "draft-1", "draftChangeKey": "change-1",
		},
	})
	if err != nil || result.IsError || backend.sendDraftInput.Account != "work" ||
		backend.sendDraftInput.DraftID != "draft-1" ||
		backend.sendDraftInput.DraftChangeKey != "change-1" {
		t.Fatalf("mail_send_draft failed: result=%+v input=%+v error=%v", result, backend.sendDraftInput, err)
	}
	result, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "mail_send_draft_commit",
		Arguments: map[string]any{
			"token": "opv1_synthetic", // gitleaks:allow -- synthetic fixture
		},
	})
	if err != nil || result.IsError || backend.approvalToken != "opv1_synthetic" {
		t.Fatalf("mail_send_draft_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
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

func TestTaskToolsUseTypedRoutesAndSeparateDestructiveCommit(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{taskAccess: application.TaskWriteAccess{Status: "approval_required"}}
	server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"task_lists", "task_list", "task_list_all", "task_get", "task_search", "task_sync"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe task read tool %q: %+v", name, tool)
		}
	}
	for _, name := range []string{"task_create", "task_create_commit", "task_update", "task_update_commit", "task_complete", "task_complete_commit", "task_reopen", "task_reopen_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe task write tool %q: %+v", name, tool)
		}
	}
	for _, name := range []string{"task_delete", "task_delete_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe task delete tool %q: %+v", name, tool)
		}
	}
	if got := toolNamed(tools.Tools, "task_delete_commit").Meta["io.github.nkiyohara.corresync/data-classification"]; got != "approval-capability" {
		t.Fatalf("task delete commit classification = %v", got)
	}

	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "task_list",
		Arguments: map[string]any{"listId": "list-1", "status": "needs_action"},
	})
	if err != nil || result.IsError || backend.taskReadInput.Account != "work" ||
		backend.taskReadInput.ListID != "list-1" || backend.taskReadInput.Limit != 50 ||
		backend.taskReadInput.Status != application.TaskStatusNeedsAction {
		t.Fatalf("task_list failed: result=%+v input=%+v error=%v", result, backend.taskReadInput, err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "task-create-v1.json")) // #nosec G304 -- fixed synthetic fixture.
	if err != nil {
		t.Fatal(err)
	}
	var taskArguments map[string]any
	if err := json.Unmarshal(fixture, &taskArguments); err != nil {
		t.Fatal(err)
	}
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "task_create", Arguments: taskArguments,
	})
	if err != nil || result.IsError || backend.taskCreateInput.Account != "work" ||
		backend.taskCreateInput.Title != "Review the synthetic release checklist" ||
		backend.taskCreateInput.Due == nil || len(backend.taskCreateInput.Labels) != 2 ||
		len(backend.taskCreateInput.Checklist) != 1 || len(backend.taskCreateInput.Sources) != 1 {
		t.Fatalf("task_create failed: result=%+v input=%+v error=%v", result, backend.taskCreateInput, err)
	}
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "task_delete",
		Arguments: map[string]any{
			"listId": "list-1", "taskId": "task-1", "version": "version-1",
		},
	})
	if err != nil || result.IsError || backend.taskAction != "delete" ||
		backend.taskStateInput.Version != "version-1" {
		t.Fatalf("task_delete failed: result=%+v input=%+v error=%v", result, backend.taskStateInput, err)
	}
	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "task_delete_commit",
		Arguments: map[string]any{"token": "opv1_task_synthetic"}, //nolint:gosec // gitleaks:allow -- synthetic approval fixture
	})
	if err != nil || result.IsError || backend.taskAction != "commit_delete" ||
		backend.approvalToken != "opv1_task_synthetic" {
		t.Fatalf("task_delete_commit failed: result=%+v token=%q error=%v", result, backend.approvalToken, err)
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
		sessionStatus: application.SessionStatusResult{
			Accounts: []application.SessionStatus{{
				Account:          account.ID,
				Alias:            account.Alias,
				Provider:         domain.ProviderMicrosoftOWA,
				MailProvider:     domain.ProviderMicrosoftOWA,
				CalendarProvider: domain.ProviderMicrosoftGraph,
				State:            "authenticated",
				Authenticated:    true,
				Capabilities:     &domain.Capabilities{Mail: true, Calendar: true},
				Services: application.ServiceAuthenticationStatuses{
					Mail: &application.ServiceAuthenticationStatus{
						Service:  application.AuthenticationServiceMail,
						Provider: domain.ProviderMicrosoftOWA,
						State:    application.AuthenticationStateAuthenticated,
					},
					Calendar: &application.ServiceAuthenticationStatus{
						Service:  application.AuthenticationServiceCalendar,
						Provider: domain.ProviderMicrosoftGraph,
						State:    application.AuthenticationStateAuthenticated,
					},
				},
			}},
		},
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
		{"account_status", map[string]any{"account": "work"}},
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
	statusResult, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "account_status", Arguments: map[string]any{"account": "work"},
	})
	if err != nil || statusResult.IsError {
		t.Fatalf(
			"account_status failed: result=%+v error=%v",
			statusResult,
			err,
		)
	}
	structured, ok := statusResult.StructuredContent.(map[string]any)
	accounts, accountsOK := structured["accounts"].([]any)
	if !ok || !accountsOK || len(accounts) != 1 {
		t.Fatalf("account_status output = %#v", statusResult.StructuredContent)
	}
	status, statusOK := accounts[0].(map[string]any)
	if !statusOK ||
		status["mailProvider"] != string(domain.ProviderMicrosoftOWA) ||
		status["calendarProvider"] != string(domain.ProviderMicrosoftGraph) {
		t.Fatalf("account_status route output = %#v", accounts[0])
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"account_discover", "account_list", "account_show", "account_status",
	} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("%s is missing read-only annotations: %+v", name, tool)
		}
	}
}

func TestAccountMutationToolsUseTypedPreviewCommitBoundary(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		accountChangeAccess: application.AccountChangeAccess{
			Status: "approval_required",
		},
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
		{
			"account_add",
			map[string]any{
				"alias": "team", "address": "reader@example.invalid",
				"mail": map[string]any{
					"provider": "microsoft-owa",
					"outlookWeb": map[string]any{
						"origin": "https://outlook.example.invalid",
					},
				},
				"default": false,
			},
		},
		{
			"account_add_commit",
			// #nosec G101 -- synthetic non-production approval fixture.
			map[string]any{"token": "opv1_add_synthetic"}, // gitleaks:allow
		},
		{
			"account_rename",
			map[string]any{"account": "work", "newAlias": "office"},
		},
		{
			"account_rename_commit",
			// #nosec G101 -- synthetic non-production approval fixture.
			map[string]any{"token": "opv1_rename_synthetic"}, // gitleaks:allow
		},
		{
			"account_remove",
			map[string]any{
				"account": "work", "replacementDefault": "personal",
			},
		},
		{
			"account_remove_commit",
			// #nosec G101 -- synthetic non-production approval fixture.
			map[string]any{"token": "opv1_remove_synthetic"}, // gitleaks:allow
		},
	} {
		result, callErr := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name: call.name, Arguments: call.arguments,
		})
		if callErr != nil || result.IsError {
			t.Fatalf("%s failed: result=%+v error=%v", call.name, result, callErr)
		}
	}
	if backend.accountAddInput.Alias != "team" ||
		backend.accountAddInput.Mail == nil ||
		backend.accountAddInput.Mail.Provider != domain.ProviderMicrosoftOWA ||
		backend.accountRenameInput.NewAlias != "office" ||
		backend.accountRemoveInput.ReplacementDefault != "personal" ||
		backend.approvalToken != "opv1_remove_synthetic" ||
		backend.caller != (domain.Caller{
			Surface: "mcp", Instance: "test-server",
		}) {
		t.Fatalf("typed lifecycle inputs were not forwarded: %+v", backend)
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"account_add", "account_add_commit",
		"account_rename", "account_rename_commit",
	} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil ||
			tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil ||
			*tool.Annotations.DestructiveHint {
			t.Fatalf("%s has unsafe annotations: %+v", name, tool)
		}
	}
	for _, name := range []string{"account_remove", "account_remove_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil ||
			tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil ||
			!*tool.Annotations.DestructiveHint {
			t.Fatalf("%s has unsafe annotations: %+v", name, tool)
		}
	}
}

func TestSettingsToolsUseTypedPreviewCommitBoundary(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{
		settingsView: application.SettingsView{
			DefaultAccount: "work", UpdateChannel: "preview",
			AutomaticChecks: true, SafetyMode: "guarded", LoginTimeout: "5m0s",
		},
		settingsAccess: application.SettingsChangeAccess{Status: "approval_required"},
	}
	server, err := New(backend, Options{Version: "dev", Instance: "settings-test"})
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, server)
	for _, call := range []struct {
		name      string
		arguments map[string]any
	}{
		{"settings_show", map[string]any{}},
		{"settings_update", map[string]any{
			"key": application.SettingUpdateChannel, "value": "stable",
		}},
		{"settings_update_commit", map[string]any{
			// #nosec G101 -- synthetic non-production approval fixture.
			"token": "opv1_settings_synthetic", // gitleaks:allow
		}},
	} {
		result, callErr := client.CallTool(t.Context(), &mcp.CallToolParams{
			Name: call.name, Arguments: call.arguments,
		})
		if callErr != nil || result.IsError {
			t.Fatalf("%s failed: result=%+v error=%v", call.name, result, callErr)
		}
	}
	if backend.settingsInput.Key != application.SettingUpdateChannel ||
		backend.settingsInput.Value != "stable" ||
		backend.approvalToken != "opv1_settings_synthetic" ||
		backend.caller != (domain.Caller{Surface: "mcp", Instance: "settings-test"}) {
		t.Fatalf("typed settings inputs were not forwarded: %+v", backend)
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	show := toolNamed(tools.Tools, "settings_show")
	if show == nil || show.Annotations == nil || !show.Annotations.ReadOnlyHint ||
		show.Annotations.DestructiveHint == nil || *show.Annotations.DestructiveHint {
		t.Fatalf("settings_show annotations = %+v", show)
	}
	for _, name := range []string{"settings_update", "settings_update_commit"} {
		tool := toolNamed(tools.Tools, name)
		if tool == nil || tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
			tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("%s annotations = %+v", name, tool)
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
