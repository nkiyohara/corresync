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

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

const (
	testAccountID  = "acc_00000000000000000000000000000001"
	testAccountID2 = "acc_00000000000000000000000000000002"
)

type fakeBackend struct {
	mailInput           application.MailListInput
	searchInput         application.MailSearchInput
	searchAllInput      application.MailProjectionInput
	bodyInput           application.MailBodyInput
	attachmentInput     application.MailAttachmentInput
	draftInput          application.MailDraftInput
	sendInput           application.MailSendInput
	sendDraftInput      application.MailDraftSendInput
	moveInput           application.MailMoveInput
	stateInput          application.MailReadStateInput
	deleteInput         application.MailDeleteInput
	folderInput         application.MailFolderListInput
	calendarFolderInput application.CalendarFolderListInput
	calendarListInput   application.CalendarListInput
	agendaInput         application.AgendaProjectionInput
	createInput         application.CalendarCreateInput
	updateInput         application.CalendarUpdateInput
	cancelInput         application.CalendarCancelInput
	terminalInput       TerminalLoginInput
	monitorListInput    application.MonitorEventListInput
	monitorAckInput     application.MonitorAcknowledgeInput
	taskListInput       application.TaskListInput
	taskReadInput       application.TaskReadInput
	taskProjectionInput application.TaskProjectionInput
	taskGetInput        application.TaskGetInput
	taskSearchInput     application.TaskSearchInput
	taskSyncInput       application.TaskSyncInput
	taskCreateInput     application.TaskCreateInput
	taskUpdateInput     application.TaskUpdateInput
	taskStateInput      application.TaskStateInput
	taskAction          string
	conversationInput   application.ConversationListInput
	messageListInput    application.MessageListInput
	messageSearchInput  application.MessageSearchInput
	messageGetInput     application.MessageGetInput
	messageAttachInput  application.MessageAttachmentGetInput
	messageSyncInput    application.MessageSyncInput
	messageSendInput    application.MessageSendInput
	messageEditInput    application.MessageEditInput
	messageDeleteInput  application.MessageDeleteInput
	messageReactInput   application.MessageReactionInput
	conversationCreate  application.ConversationCreateInput
	membershipInput     application.ConversationMembershipInput
	messageAction       string
	commitToken         string
	caller              domain.Caller
}

func (backend *fakeBackend) DefaultAccount() domain.AccountID { return testAccountID }

type providerNeutralBackend struct {
	*fakeBackend
}

func (*providerNeutralBackend) DefaultAccount() domain.AccountID { return "" }

func (backend *fakeBackend) SessionStatus(
	context.Context,
	domain.Caller,
) (SessionStatusResult, error) {
	return SessionStatusResult{
		Accounts: []SessionStatus{{
			Account: testAccountID, Alias: "work",
			Provider:     domain.ProviderMicrosoftOWA,
			MailProvider: domain.ProviderMicrosoftOWA, State: "signed_out",
			Services: testSessionAuthenticationStatuses(
				testAccountID,
				"work",
				domain.ProviderMicrosoftOWA,
			),
		}},
	}, nil
}

func (backend *fakeBackend) MonitorStatus(
	context.Context,
	domain.AccountID,
	domain.Caller,
) (application.MonitorStatus, error) {
	return application.MonitorStatus{
		Account: testAccountID, Alias: "work", Mode: domain.MonitorOff,
	}, nil
}

func (backend *fakeBackend) ListMonitorEvents(
	_ context.Context,
	input application.MonitorEventListInput,
	caller domain.Caller,
) (application.MonitorEventPage, error) {
	backend.monitorListInput, backend.caller = input, caller
	return application.MonitorEventPage{
		Events: []application.MonitorEvent{monitorTestEvent("pending")},
		Offset: input.Offset, Limit: input.Limit, Total: 1,
	}, nil
}

func (backend *fakeBackend) AcknowledgeMonitorEvent(
	_ context.Context,
	input application.MonitorAcknowledgeInput,
	caller domain.Caller,
) (application.MonitorEvent, error) {
	backend.monitorAckInput, backend.caller = input, caller
	return monitorTestEvent("acknowledged"), nil
}

func monitorTestEvent(state string) application.MonitorEvent {
	event := application.MonitorEvent{
		ID:      "evt_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Account: testAccountID, AccountAlias: "work",
		Provider: domain.ProviderJMAP, SourceObjectID: "synthetic-message",
		Trust:    application.MonitorTrustMarker,
		Delivery: application.MonitorDeliveryQueue, State: state,
		DeliveryCount: 1, DetectedAt: time.Unix(4, 0).UTC(),
	}
	if state == "acknowledged" {
		acknowledged := time.Unix(5, 0).UTC()
		event.AcknowledgedAt = &acknowledged
	}
	return event
}
func (*fakeBackend) Login(_ context.Context, account domain.AccountID, _ domain.Caller) (LoginResult, error) {
	return LoginResult{Account: account, Authenticated: true, CapturedAt: time.Unix(2, 0)}, nil
}
func (*fakeBackend) Logout(
	_ context.Context,
	account domain.AccountID,
	_ domain.Caller,
) (LogoutResult, error) {
	return LogoutResult{Account: account, LoggedOut: true}, nil
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
func (backend *fakeBackend) SearchAllMail(_ context.Context, input application.MailProjectionInput, caller domain.Caller) (application.MailProjectionPage, error) {
	backend.searchAllInput, backend.caller = input, caller
	capabilities := domain.Capabilities{Mail: true}
	return application.MailProjectionPage{
		Messages: []application.ProjectedMail{{
			AccountAlias: "work",
			Message: application.MailSummary{
				ID: "search-all-message-1", ReceivedAt: "2026-07-28T10:00:00Z",
				Provenance: domain.Provenance{
					AccountID: testAccountID, Provider: domain.ProviderMicrosoftOWA,
					MailboxID: "mailbox", SourceObjectID: "search-all-message-1",
				},
			},
		}},
		Accounts: []application.ProjectionAccountStatus{{
			Account: testAccountID, Alias: "work",
			Provider: domain.ProviderMicrosoftOWA, Service: "mail",
			Complete: true, FetchedItems: 1, Exhausted: true,
			Capabilities: &capabilities,
		}},
		Offset: input.Offset, Limit: input.Limit, Complete: true,
	}, nil
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
func (backend *fakeBackend) SendMailDraft(_ context.Context, input application.MailDraftSendInput, caller domain.Caller) (application.MailDraftSendAccess, error) {
	backend.sendDraftInput, backend.caller = input, caller
	return application.MailDraftSendAccess{Status: "approval_required"}, nil
}
func (backend *fakeBackend) CommitMailSendDraft(_ context.Context, token string, caller domain.Caller) (application.MailDraftSendAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MailDraftSendAccess{Status: "sent", Sent: &application.MailSendResult{}}, nil
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
func (backend *fakeBackend) ListCalendarFolders(_ context.Context, input application.CalendarFolderListInput, caller domain.Caller) (application.CalendarFolderPage, error) {
	backend.calendarFolderInput, backend.caller = input, caller
	return application.CalendarFolderPage{
		Calendars: []application.CalendarFolderSummary{{
			ID: "calendar-1", DisplayName: "Work", IsDefault: true,
			CanEdit: true, AccessRole: "owner",
		}},
		TotalCalendars: 1, IncludesLastItem: true,
	}, nil
}
func (backend *fakeBackend) ListCalendar(_ context.Context, input application.CalendarListInput, caller domain.Caller) (application.CalendarPage, error) {
	backend.calendarListInput, backend.caller = input, caller
	return application.CalendarPage{
		Events: []application.CalendarEvent{{ID: "event-1", Start: input.Start, End: input.End}},
		Start:  input.Start, End: input.End,
	}, nil
}
func (backend *fakeBackend) ListAgenda(_ context.Context, input application.AgendaProjectionInput, caller domain.Caller) (application.AgendaProjectionPage, error) {
	backend.agendaInput, backend.caller = input, caller
	capabilities := domain.Capabilities{Calendar: true}
	return application.AgendaProjectionPage{
		Events: []application.ProjectedAgendaEvent{{
			AccountAlias: "work", DisplayStart: "2026-07-28T10:00:00Z",
			DisplayEnd:      "2026-07-28T11:00:00Z",
			DisplayTimeZone: input.DisplayTimeZone,
			Event: application.CalendarEvent{
				ID:    "agenda-event-1",
				Start: "2026-07-28T10:00:00Z", End: "2026-07-28T11:00:00Z",
				OriginalStart:         "2026-07-28T10:00:00Z",
				OriginalEnd:           "2026-07-28T11:00:00Z",
				OriginalStartTimeZone: "UTC", OriginalEndTimeZone: "UTC",
				Provenance: domain.Provenance{
					AccountID: testAccountID, Provider: domain.ProviderMicrosoftOWA,
					CalendarID: "calendar", SourceObjectID: "agenda-event-1",
				},
			},
		}},
		Accounts: []application.ProjectionAccountStatus{{
			Account: testAccountID, Alias: "work",
			Provider: domain.ProviderMicrosoftOWA, Service: "calendar",
			Complete: true, FetchedItems: 1, Exhausted: true,
			Capabilities: &capabilities,
		}},
		Start: input.Start, End: input.End,
		DisplayTimeZone: input.DisplayTimeZone,
		Offset:          input.Offset, Limit: input.Limit, Complete: true,
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

func daemonTestTask(id string) application.Task {
	return application.Task{
		ID: id, Version: "version-1", ListID: "list-1", Title: "Synthetic task",
		Status: application.TaskStatusNeedsAction, Priority: application.TaskPriorityNone,
		Capabilities: application.TaskCapabilities{Read: true, CrossListRead: true},
		Provenance: domain.Provenance{
			AccountID: testAccountID, Provider: domain.ProviderTodoist,
			TaskListID: "list-1", SourceObjectID: id,
		},
	}
}

func (backend *fakeBackend) ListTaskLists(_ context.Context, input application.TaskListInput, caller domain.Caller) (application.TaskListPage, error) {
	backend.taskListInput, backend.caller = input, caller
	return application.TaskListPage{
		Lists: []application.TaskList{{
			ID: "list-1", DisplayName: "Synthetic", Editable: true,
			Capabilities: application.TaskCapabilities{Read: true, CrossListRead: true},
			Provenance:   domain.Provenance{AccountID: testAccountID, Provider: domain.ProviderTodoist, TaskListID: "list-1"},
		}},
		Offset: input.Offset, Limit: input.Limit,
	}, nil
}

func (backend *fakeBackend) ListTasks(_ context.Context, input application.TaskReadInput, caller domain.Caller) (application.TaskPage, error) {
	backend.taskReadInput, backend.caller = input, caller
	return application.TaskPage{Tasks: []application.Task{daemonTestTask("task-1")}, Offset: input.Offset, Limit: input.Limit}, nil
}

func (backend *fakeBackend) ListAllTasks(_ context.Context, input application.TaskProjectionInput, caller domain.Caller) (application.TaskProjectionPage, error) {
	backend.taskProjectionInput, backend.caller = input, caller
	capabilities := domain.Capabilities{Tasks: true}
	return application.TaskProjectionPage{
		Tasks: []application.ProjectedTask{{AccountAlias: "work", Task: daemonTestTask("task-1")}},
		Accounts: []application.ProjectionAccountStatus{{
			Account: testAccountID, Alias: "work", Provider: domain.ProviderTodoist,
			Service: "tasks", Complete: true, FetchedItems: 1, Exhausted: true,
			Capabilities: &capabilities,
		}},
		Offset: input.Offset, Limit: input.Limit, Complete: true,
	}, nil
}

func (backend *fakeBackend) GetTask(_ context.Context, input application.TaskGetInput, caller domain.Caller) (application.Task, error) {
	backend.taskGetInput, backend.caller = input, caller
	return daemonTestTask(input.TaskID), nil
}

func (backend *fakeBackend) SearchTasks(_ context.Context, input application.TaskSearchInput, caller domain.Caller) (application.TaskPage, error) {
	backend.taskSearchInput, backend.caller = input, caller
	return application.TaskPage{Tasks: []application.Task{daemonTestTask("task-1")}, Offset: input.Offset, Limit: input.Limit}, nil
}

func (backend *fakeBackend) SyncTasks(_ context.Context, input application.TaskSyncInput, caller domain.Caller) (application.TaskChangePage, error) {
	backend.taskSyncInput, backend.caller = input, caller
	return application.TaskChangePage{
		Changes: []application.TaskChange{{Kind: application.TaskChangeUpsert, Task: func() *application.Task {
			task := daemonTestTask("task-1")
			return &task
		}()}},
		Cursor: application.TaskCursor{
			Provider: domain.ProviderTodoist, Account: testAccountID, ListID: input.ListID,
			Mode: application.TaskSyncDelta, Value: "cursor-1",
		},
	}, nil
}

func (backend *fakeBackend) CreateTask(_ context.Context, input application.TaskCreateInput, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.taskCreateInput, backend.taskAction, backend.caller = input, "create", caller
	return application.TaskWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) UpdateTask(_ context.Context, input application.TaskUpdateInput, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.taskUpdateInput, backend.taskAction, backend.caller = input, "update", caller
	return application.TaskWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CompleteTask(_ context.Context, input application.TaskStateInput, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "complete", caller
	return application.TaskWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) ReopenTask(_ context.Context, input application.TaskStateInput, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "reopen", caller
	return application.TaskWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) DeleteTask(_ context.Context, input application.TaskDeleteInput, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.taskStateInput, backend.taskAction, backend.caller = input, "delete", caller
	return application.TaskWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitTaskCreate(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.committedTask("created", token, caller), nil
}

func (backend *fakeBackend) CommitTaskUpdate(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.committedTask("updated", token, caller), nil
}

func (backend *fakeBackend) CommitTaskComplete(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.committedTask("completed", token, caller), nil
}

func (backend *fakeBackend) CommitTaskReopen(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.committedTask("reopened", token, caller), nil
}

func (backend *fakeBackend) CommitTaskDelete(_ context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	backend.commitToken, backend.taskAction, backend.caller = token, "deleted", caller
	return application.TaskWriteAccess{Status: "deleted", Deleted: &application.TaskDeleteResult{
		ListID: "list-1", TaskID: "task-1",
		Provenance: domain.Provenance{AccountID: testAccountID, Provider: domain.ProviderTodoist, TaskListID: "list-1", SourceObjectID: "task-1"},
	}}, nil
}

func (backend *fakeBackend) committedTask(status, token string, caller domain.Caller) application.TaskWriteAccess {
	backend.commitToken, backend.taskAction, backend.caller = token, status, caller
	task := daemonTestTask("task-1")
	if status == "completed" {
		task.Status = application.TaskStatusCompleted
		task.CompletedAt = &application.TaskTemporal{Kind: application.TaskTemporalZoned, Value: "2026-08-13T12:00:00Z", TimeZone: "UTC"}
	}
	return application.TaskWriteAccess{Status: status, Task: &task}
}

func (backend *fakeBackend) ListConversations(
	_ context.Context,
	input application.ConversationListInput,
	caller domain.Caller,
) (application.ConversationPage, error) {
	backend.conversationInput, backend.caller = input, caller
	return application.ConversationPage{Conversations: []application.Conversation{{ID: "conversation-1"}}}, nil
}

func (backend *fakeBackend) ListMessages(
	_ context.Context,
	input application.MessageListInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	backend.messageListInput, backend.caller = input, caller
	return application.MessagePage{Messages: []application.MessageSummary{{ID: "message-1"}}}, nil
}

func (backend *fakeBackend) SearchMessages(
	_ context.Context,
	input application.MessageSearchInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	backend.messageSearchInput, backend.caller = input, caller
	return application.MessagePage{Messages: []application.MessageSummary{{ID: "message-1"}}}, nil
}

func (backend *fakeBackend) GetMessage(
	_ context.Context,
	input application.MessageGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	backend.messageGetInput, backend.caller = input, caller
	return application.MessageSensitiveAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitGetMessage(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MessageSensitiveAccess{Status: "completed"}, nil
}

func (backend *fakeBackend) GetMessageAttachment(
	_ context.Context,
	input application.MessageAttachmentGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	backend.messageAttachInput, backend.caller = input, caller
	return application.MessageSensitiveAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitGetMessageAttachment(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	backend.commitToken, backend.caller = token, caller
	return application.MessageSensitiveAccess{Status: "completed"}, nil
}

func (backend *fakeBackend) SyncMessages(
	_ context.Context,
	input application.MessageSyncInput,
	caller domain.Caller,
) (application.MessageChangePage, error) {
	backend.messageSyncInput, backend.caller = input, caller
	return application.MessageChangePage{Changes: []application.MessageChange{{Kind: application.MessageChangeDelete, ID: "message-1"}}}, nil
}

func (backend *fakeBackend) SendMessage(
	_ context.Context,
	input application.MessageSendInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.messageSendInput, backend.messageAction, backend.caller = input, "send", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitSendMessage(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("sent", token, caller), nil
}

func (backend *fakeBackend) EditMessage(
	_ context.Context,
	input application.MessageEditInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.messageEditInput, backend.messageAction, backend.caller = input, "edit", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitEditMessage(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("edited", token, caller), nil
}

func (backend *fakeBackend) DeleteMessage(
	_ context.Context,
	input application.MessageDeleteInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.messageDeleteInput, backend.messageAction, backend.caller = input, "delete", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitDeleteMessage(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("deleted", token, caller), nil
}

func (backend *fakeBackend) ReactToMessage(
	_ context.Context,
	input application.MessageReactionInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.messageReactInput, backend.messageAction, backend.caller = input, "react", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitMessageReaction(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("reacted", token, caller), nil
}

func (backend *fakeBackend) CreateConversation(
	_ context.Context,
	input application.ConversationCreateInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.conversationCreate, backend.messageAction, backend.caller = input, "create_conversation", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitCreateConversation(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("conversation_created", token, caller), nil
}

func (backend *fakeBackend) ChangeConversationMembership(
	_ context.Context,
	input application.ConversationMembershipInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	backend.membershipInput, backend.messageAction, backend.caller = input, "membership", caller
	return application.MessageWriteAccess{Status: "approval_required"}, nil
}

func (backend *fakeBackend) CommitConversationMembership(
	_ context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.committedMessage("membership_changed", token, caller), nil
}

func (backend *fakeBackend) committedMessage(
	status string,
	token string,
	caller domain.Caller,
) application.MessageWriteAccess {
	backend.commitToken, backend.messageAction, backend.caller = token, status, caller
	return application.MessageWriteAccess{Status: status}
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
	signedOut := func(account, alias string) SessionStatus {
		return SessionStatus{
			Account: domain.AccountID(account), Alias: alias,
			Provider:     domain.ProviderMicrosoftOWA,
			MailProvider: domain.ProviderMicrosoftOWA, State: "signed_out",
			Services: testSessionAuthenticationStatuses(
				domain.AccountID(account),
				alias,
				domain.ProviderMicrosoftOWA,
			),
		}
	}
	tests := []SessionStatusResult{
		{Accounts: []SessionStatus{
			signedOut(testAccountID, "personal"),
			signedOut(testAccountID, "work"),
		}},
		{Accounts: []SessionStatus{
			signedOut(testAccountID, "work"),
			signedOut(testAccountID2, "personal"),
		}},
		{Accounts: []SessionStatus{{
			Account: testAccountID, Alias: "work",
			Provider: domain.ProviderMicrosoftOWA, State: "unknown",
		}}},
		{Accounts: []SessionStatus{{
			Account: testAccountID, Alias: "work",
			Provider: domain.ProviderMicrosoftOWA,
			State:    "authenticated", Authenticated: true,
		}}},
		{Accounts: []SessionStatus{{
			Account: testAccountID, Alias: "work",
			Provider: domain.ProviderMicrosoftOWA,
			State:    "signed_out", CapturedAt: &capturedAt,
		}}},
		{Accounts: []SessionStatus{{
			Account: testAccountID, Alias: "work",
			Provider:     domain.ProviderMicrosoftOWA,
			MailProvider: domain.ProviderGoogle,
			State:        "signed_out",
		}}},
	}
	for index, result := range tests {
		if err := validateSessionStatusResult(result); err == nil {
			t.Fatalf("case %d unexpectedly passed: %+v", index, result)
		}
	}
}

func TestValidateSessionStatusResultAcceptsMessagingOnlyRoute(t *testing.T) {
	t.Parallel()
	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateSignedOut,
		application.AuthenticationReasonNeverAuthenticated,
		testAccountID,
		"work",
		application.AuthenticationServiceMessages,
		domain.ProviderID(domain.MessagingProviderSlack),
	)
	if err != nil {
		t.Fatal(err)
	}
	status := application.ServiceAuthenticationStatus{
		Service:  application.AuthenticationServiceMessages,
		Provider: domain.ProviderID(domain.MessagingProviderSlack),
		State:    application.AuthenticationStateSignedOut,
		Reason:   application.AuthenticationReasonNeverAuthenticated,
		Action:   &action,
	}
	if err := validateSessionStatusResult(SessionStatusResult{Accounts: []SessionStatus{{
		Account: testAccountID, Alias: "work",
		MessagingProvider: domain.MessagingProviderSlack,
		State:             "signed_out",
		Services:          application.ServiceAuthenticationStatuses{Messages: &status},
	}}}); err != nil {
		t.Fatalf("validateSessionStatusResult() error = %v", err)
	}
}

func testSessionAuthenticationStatuses(
	account domain.AccountID,
	alias string,
	mailProvider domain.ProviderID,
) application.ServiceAuthenticationStatuses {
	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateSignedOut,
		application.AuthenticationReasonNeverAuthenticated,
		account,
		alias,
		application.AuthenticationServiceMail,
		mailProvider,
	)
	if err != nil {
		panic(err)
	}
	return application.ServiceAuthenticationStatuses{
		Mail: &application.ServiceAuthenticationStatus{
			Service:  application.AuthenticationServiceMail,
			Provider: mailProvider,
			State:    application.AuthenticationStateSignedOut,
			Reason:   application.AuthenticationReasonNeverAuthenticated,
			Action:   &action,
		},
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
	if err != nil || status.DefaultAccount != testAccountID || status.ProtocolVersion != ProtocolVersion {
		t.Fatalf("Status() = %+v, %v", status, err)
	}
	login, err := client.Login(t.Context(), testAccountID, caller)
	if err != nil || !login.Authenticated || login.Account != testAccountID {
		t.Fatalf("Login() = %+v, %v", login, err)
	}
	logout, err := client.Logout(t.Context(), testAccountID, caller)
	if err != nil || !logout.LoggedOut || logout.Account != testAccountID {
		t.Fatalf("Logout() = %+v, %v", logout, err)
	}
	sessions, err := client.SessionStatus(t.Context(), caller)
	if err != nil ||
		len(sessions.Accounts) != 1 ||
		sessions.Accounts[0].Account != testAccountID ||
		sessions.Accounts[0].State != "signed_out" {
		t.Fatalf("SessionStatus() = %+v, %v", sessions, err)
	}
	monitorStatus, err := client.MonitorStatus(t.Context(), testAccountID, caller)
	if err != nil || monitorStatus.Mode != domain.MonitorOff {
		t.Fatalf("MonitorStatus() = %+v, %v", monitorStatus, err)
	}
	monitorPage, err := client.ListMonitorEvents(
		t.Context(),
		application.MonitorEventListInput{Account: testAccountID, Limit: 50},
		caller,
	)
	if err != nil || len(monitorPage.Events) != 1 {
		t.Fatalf("ListMonitorEvents() = %+v, %v", monitorPage, err)
	}
	acknowledged, err := client.AcknowledgeMonitorEvent(
		t.Context(),
		application.MonitorAcknowledgeInput{
			Account: testAccountID,
			EventID: monitorPage.Events[0].ID,
		},
		caller,
	)
	if err != nil || acknowledged.State != "acknowledged" {
		t.Fatalf("AcknowledgeMonitorEvent() = %+v, %v", acknowledged, err)
	}
	terminalLogin, err := client.TerminalLogin(t.Context(), TerminalLoginInput{Account: testAccountID}, caller)
	if err != nil || terminalLogin.Status != "pending" || terminalLogin.View == nil ||
		len(terminalLogin.View.Controls) != 1 {
		t.Fatalf("TerminalLogin(start) = %+v, %v", terminalLogin, err)
	}
	terminalLogin, err = client.TerminalLogin(t.Context(), TerminalLoginInput{
		Account: testAccountID, SessionID: terminalLogin.SessionID,
		Action: &TerminalLoginAction{Type: "key", ControlID: "control-1", Key: "a"},
	}, caller)
	if err != nil || terminalLogin.Status != "authenticated" || backend.terminalInput.Action.Key != "a" {
		t.Fatalf("TerminalLogin(continue) = %+v, %v; input=%+v", terminalLogin, err, backend.terminalInput)
	}
	page, err := client.ListMail(t.Context(), application.MailListInput{
		Account: testAccountID, Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit: 25, TimeZone: "UTC",
	}, caller)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("ListMail() = %+v, %v", page, err)
	}
	if backend.caller != caller || backend.mailInput.Account != testAccountID {
		t.Fatalf("backend received caller=%+v input=%+v", backend.caller, backend.mailInput)
	}
	search, err := client.SearchMail(t.Context(), application.MailSearchInput{
		Account: testAccountID, Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Query: "subject:synthetic", Limit: 25, TimeZone: "UTC",
	}, caller)
	if err != nil || len(search.Messages) != 1 || backend.searchInput.Query != "subject:synthetic" {
		t.Fatalf("SearchMail() = %+v, %v; backend input=%+v", search, err, backend.searchInput)
	}
	searchAll, err := client.SearchAllMail(t.Context(), application.MailProjectionInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Query: "subject:synthetic", Limit: 25, TimeZone: "UTC",
	}, caller)
	if err != nil || len(searchAll.Messages) != 1 ||
		backend.searchAllInput.Query != "subject:synthetic" {
		t.Fatalf(
			"SearchAllMail() = %+v, %v; backend input=%+v",
			searchAll,
			err,
			backend.searchAllInput,
		)
	}
	moved, err := client.MoveMail(t.Context(), application.MailMoveInput{
		Account: testAccountID, MessageID: "message-1", ChangeKey: "change-1",
		Destination: application.MailFolder{Kind: application.MailFolderOpaque, ID: "folder-1"},
	}, caller)
	if err != nil || moved.Moved == nil || moved.Moved.ID != "moved-1" || backend.moveInput.ChangeKey != "change-1" {
		t.Fatalf("MoveMail() = %+v, %v; backend input=%+v", moved, err, backend.moveInput)
	}
	readState, err := client.SetMailReadState(t.Context(), application.MailReadStateInput{
		Account: testAccountID, MessageID: "message-1", ChangeKey: "change-1", State: application.MailReadStateRead,
	}, caller)
	if err != nil || readState.Updated == nil || readState.Updated.State != application.MailReadStateRead ||
		backend.stateInput.ChangeKey != "change-1" {
		t.Fatalf("SetMailReadState() = %+v, %v; backend input=%+v", readState, err, backend.stateInput)
	}
	folders, err := client.ListMailFolders(t.Context(), application.MailFolderListInput{
		Account:   testAccountID,
		Parent:    application.MailFolder{Kind: application.MailFolderDistinguished, ID: "msgfolderroot"},
		Traversal: application.MailFolderTraversalDeep,
		Limit:     100, TimeZone: "UTC",
	}, caller)
	if err != nil || len(folders.Folders) != 1 || backend.folderInput.Account != testAccountID {
		t.Fatalf("ListMailFolders() = %+v, %v; backend input=%+v", folders, err, backend.folderInput)
	}
	calendars, err := client.ListCalendarFolders(t.Context(), application.CalendarFolderListInput{
		Account: testAccountID, Limit: 100,
	}, caller)
	if err != nil || len(calendars.Calendars) != 1 ||
		backend.calendarFolderInput.Account != testAccountID {
		t.Fatalf(
			"ListCalendarFolders() = %+v, %v; backend input=%+v",
			calendars,
			err,
			backend.calendarFolderInput,
		)
	}
	body, err := client.GetMailBody(t.Context(), application.MailBodyInput{
		Account: testAccountID, MessageID: "message-1",
	}, caller)
	if err != nil || body.Body == nil || body.Body.Text != "Synthetic body" || backend.bodyInput.MessageID != "message-1" {
		t.Fatalf("GetMailBody() = %+v, %v; backend input=%+v", body, err, backend.bodyInput)
	}
	body, err = client.CommitMailBody(t.Context(), "opv1_body", caller)
	if err != nil || body.Status != "completed" || backend.commitToken != "opv1_body" {
		t.Fatalf("CommitMailBody() = %+v, %v; token=%q", body, err, backend.commitToken)
	}
	attachment, err := client.GetMailAttachment(t.Context(), application.MailAttachmentInput{
		Account: testAccountID, AttachmentID: "attachment-1",
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
		Account: testAccountID, To: []string{"reader@example.test"}, Subject: "Synthetic draft", Body: "Synthetic body",
	}, caller)
	if err != nil || draft.Draft == nil || draft.Draft.ID != "draft-1" || backend.draftInput.Subject != "Synthetic draft" {
		t.Fatalf("CreateMailDraft() = %+v, %v; backend input=%+v", draft, err, backend.draftInput)
	}
	draft, err = client.CommitMailDraft(t.Context(), "opv1_draft", caller)
	if err != nil || draft.Status != "completed" || backend.commitToken != "opv1_draft" {
		t.Fatalf("CommitMailDraft() = %+v, %v; token=%q", draft, err, backend.commitToken)
	}
	send, err := client.SendMail(t.Context(), application.MailSendInput{
		Account: testAccountID, To: []string{"reader@example.test"}, Subject: "Synthetic send", Body: "Synthetic body",
	}, caller)
	if err != nil || send.Status != "approval_required" || backend.sendInput.Subject != "Synthetic send" {
		t.Fatalf("SendMail() = %+v, %v; backend input=%+v", send, err, backend.sendInput)
	}
	send, err = client.CommitMailSend(t.Context(), "opv1_send", caller)
	if err != nil || send.Status != "sent" || send.Sent == nil || backend.commitToken != "opv1_send" {
		t.Fatalf("CommitMailSend() = %+v, %v; token=%q", send, err, backend.commitToken)
	}
	savedDraft, err := client.SendMailDraft(t.Context(), application.MailDraftSendInput{
		Account: testAccountID, DraftID: "draft-1", DraftChangeKey: "change-1",
	}, caller)
	if err != nil || savedDraft.Status != "approval_required" ||
		backend.sendDraftInput.DraftID != "draft-1" {
		t.Fatalf("SendMailDraft() = %+v, %v; input=%+v", savedDraft, err, backend.sendDraftInput)
	}
	savedDraft, err = client.CommitMailSendDraft(t.Context(), "opv1_send_draft", caller)
	if err != nil || savedDraft.Status != "sent" || savedDraft.Sent == nil ||
		backend.commitToken != "opv1_send_draft" {
		t.Fatalf("CommitMailSendDraft() = %+v, %v; token=%q", savedDraft, err, backend.commitToken)
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
		Account: testAccountID, Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Start: "2026-07-20T09:00:00Z", End: "2026-07-20T10:00:00Z",
	}, caller)
	if err != nil || len(calendarPage.Events) != 1 || backend.calendarListInput.Start != "2026-07-20T09:00:00Z" {
		t.Fatalf("ListCalendar() = %+v, %v; backend input=%+v", calendarPage, err, backend.calendarListInput)
	}
	agenda, err := client.ListAgenda(t.Context(), application.AgendaProjectionInput{
		Start:           "2026-07-20T09:00:00Z",
		End:             "2026-07-20T12:00:00Z",
		DisplayTimeZone: "UTC",
		Limit:           25,
	}, caller)
	if err != nil || len(agenda.Events) != 1 ||
		backend.agendaInput.DisplayTimeZone != "UTC" {
		t.Fatalf(
			"ListAgenda() = %+v, %v; backend input=%+v",
			agenda,
			err,
			backend.agendaInput,
		)
	}
	calendarAccess, err := client.CreateCalendar(t.Context(), application.CalendarCreateInput{
		Account:      testAccountID,
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
		Account: testAccountID, EventID: "event-1", ChangeKey: "change-1", Subject: &updatedSubject,
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
		Account: testAccountID, EventID: "event-1", ChangeKey: "change-2",
	}, caller)
	if err != nil || cancelAccess.Status != "approval_required" || backend.cancelInput.ChangeKey != "change-2" {
		t.Fatalf("CancelCalendar() = %+v, %v; backend input=%+v", cancelAccess, err, backend.cancelInput)
	}
	cancelAccess, err = client.CommitCalendarCancel(t.Context(), "opv1_synthetic", caller)
	if err != nil || cancelAccess.Status != "cancelled" {
		t.Fatalf("CommitCalendarCancel() = %+v, %v", cancelAccess, err)
	}
	taskLists, err := client.ListTaskLists(t.Context(), application.TaskListInput{
		Account: testAccountID, Limit: 25,
	}, caller)
	if err != nil || len(taskLists.Lists) != 1 || backend.taskListInput.Account != testAccountID {
		t.Fatalf("ListTaskLists() = %+v, %v; input=%+v", taskLists, err, backend.taskListInput)
	}
	tasks, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccountID, ListID: "list-1", Limit: 25,
	}, caller)
	if err != nil || len(tasks.Tasks) != 1 || backend.taskReadInput.ListID != "list-1" {
		t.Fatalf("ListTasks() = %+v, %v; input=%+v", tasks, err, backend.taskReadInput)
	}
	projectedTasks, err := client.ListAllTasks(t.Context(), application.TaskProjectionInput{Limit: 25}, caller)
	if err != nil || len(projectedTasks.Tasks) != 1 || backend.taskProjectionInput.Limit != 25 {
		t.Fatalf("ListAllTasks() = %+v, %v; input=%+v", projectedTasks, err, backend.taskProjectionInput)
	}
	task, err := client.GetTask(t.Context(), application.TaskGetInput{
		Account: testAccountID, ListID: "list-1", TaskID: "task-1",
	}, caller)
	if err != nil || task.ID != "task-1" || backend.taskGetInput.TaskID != "task-1" {
		t.Fatalf("GetTask() = %+v, %v; input=%+v", task, err, backend.taskGetInput)
	}
	searchedTasks, err := client.SearchTasks(t.Context(), application.TaskSearchInput{
		Account: testAccountID, ListID: "list-1", Query: "synthetic", Limit: 25,
	}, caller)
	if err != nil || len(searchedTasks.Tasks) != 1 || backend.taskSearchInput.Query != "synthetic" {
		t.Fatalf("SearchTasks() = %+v, %v; input=%+v", searchedTasks, err, backend.taskSearchInput)
	}
	syncedTasks, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccountID, ListID: "list-1", Limit: 25,
	}, caller)
	if err != nil || len(syncedTasks.Changes) != 1 || backend.taskSyncInput.ListID != "list-1" {
		t.Fatalf("SyncTasks() = %+v, %v; input=%+v", syncedTasks, err, backend.taskSyncInput)
	}
	taskAccess, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccountID, ListID: "list-1", Title: "Synthetic task", Priority: application.TaskPriorityNone,
	}, caller)
	if err != nil || taskAccess.Status != "approval_required" || backend.taskCreateInput.Title != "Synthetic task" {
		t.Fatalf("CreateTask() = %+v, %v; input=%+v", taskAccess, err, backend.taskCreateInput)
	}
	taskAccess, err = client.CommitTaskCreate(t.Context(), "opv1_task_create", caller)
	if err != nil || taskAccess.Status != "created" || backend.commitToken != "opv1_task_create" {
		t.Fatalf("CommitTaskCreate() = %+v, %v", taskAccess, err)
	}
	updatedTitle := "Updated task"
	taskAccess, err = client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: testAccountID, ListID: "list-1", TaskID: "task-1", Version: "version-1", Title: &updatedTitle,
	}, caller)
	if err != nil || taskAccess.Status != "approval_required" || backend.taskUpdateInput.Title == nil {
		t.Fatalf("UpdateTask() = %+v, %v; input=%+v", taskAccess, err, backend.taskUpdateInput)
	}
	taskAccess, err = client.CommitTaskUpdate(t.Context(), "opv1_task_update", caller)
	if err != nil || taskAccess.Status != "updated" {
		t.Fatalf("CommitTaskUpdate() = %+v, %v", taskAccess, err)
	}
	stateInput := application.TaskStateInput{
		Account: testAccountID, ListID: "list-1", TaskID: "task-1", Version: "version-1",
	}
	for _, operation := range []struct {
		name    string
		prepare func(context.Context, application.TaskStateInput, domain.Caller) (application.TaskWriteAccess, error)
		commit  func(context.Context, string, domain.Caller) (application.TaskWriteAccess, error)
		status  string
	}{
		{"complete", client.CompleteTask, client.CommitTaskComplete, "completed"},
		{"reopen", client.ReopenTask, client.CommitTaskReopen, "reopened"},
		{"delete", client.DeleteTask, client.CommitTaskDelete, "deleted"},
	} {
		prepared, prepareErr := operation.prepare(t.Context(), stateInput, caller)
		if prepareErr != nil || prepared.Status != "approval_required" || backend.taskAction != operation.name {
			t.Fatalf("%s task = %+v, %v action=%q", operation.name, prepared, prepareErr, backend.taskAction)
		}
		committed, commitErr := operation.commit(t.Context(), "opv1_task_"+operation.name, caller)
		if commitErr != nil || committed.Status != operation.status {
			t.Fatalf("commit %s task = %+v, %v", operation.name, committed, commitErr)
		}
	}
	conversations, err := client.ListConversations(t.Context(), application.ConversationListInput{
		Account: testAccountID, WorkspaceID: "workspace-1", Limit: 25,
	}, caller)
	if err != nil || len(conversations.Conversations) != 1 ||
		backend.conversationInput.WorkspaceID != "workspace-1" {
		t.Fatalf("ListConversations() = %+v, %v; input=%+v", conversations, err, backend.conversationInput)
	}
	messages, err := client.ListMessages(t.Context(), application.MessageListInput{
		Account: testAccountID, WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 25,
	}, caller)
	if err != nil || len(messages.Messages) != 1 || backend.messageListInput.ConversationID != "conversation-1" {
		t.Fatalf("ListMessages() = %+v, %v; input=%+v", messages, err, backend.messageListInput)
	}
	messages, err = client.SearchMessages(t.Context(), application.MessageSearchInput{
		Account: testAccountID, WorkspaceID: "workspace-1", ConversationID: "conversation-1",
		Query: "synthetic", Limit: 25,
	}, caller)
	if err != nil || len(messages.Messages) != 1 || backend.messageSearchInput.Query != "synthetic" {
		t.Fatalf("SearchMessages() = %+v, %v; input=%+v", messages, err, backend.messageSearchInput)
	}
	messageRoute := application.MessageGetInput{
		Account: testAccountID, WorkspaceID: "workspace-1", ConversationID: "conversation-1", MessageID: "message-1",
	}
	messageAccess, err := client.GetMessage(t.Context(), messageRoute, caller)
	if err != nil || messageAccess.Status != "approval_required" || backend.messageGetInput.MessageID != "message-1" {
		t.Fatalf("GetMessage() = %+v, %v; input=%+v", messageAccess, err, backend.messageGetInput)
	}
	messageAccess, err = client.CommitGetMessage(t.Context(), "opv1_message_get", caller)
	if err != nil || messageAccess.Status != "completed" || backend.commitToken != "opv1_message_get" {
		t.Fatalf("CommitGetMessage() = %+v, %v", messageAccess, err)
	}
	messageAccess, err = client.GetMessageAttachment(t.Context(), application.MessageAttachmentGetInput{
		Account: testAccountID, WorkspaceID: "workspace-1", ConversationID: "conversation-1",
		MessageID: "message-1", AttachmentID: "attachment-1",
	}, caller)
	if err != nil || messageAccess.Status != "approval_required" ||
		backend.messageAttachInput.AttachmentID != "attachment-1" {
		t.Fatalf("GetMessageAttachment() = %+v, %v; input=%+v", messageAccess, err, backend.messageAttachInput)
	}
	messageAccess, err = client.CommitGetMessageAttachment(t.Context(), "opv1_message_attachment", caller)
	if err != nil || messageAccess.Status != "completed" || backend.commitToken != "opv1_message_attachment" {
		t.Fatalf("CommitGetMessageAttachment() = %+v, %v", messageAccess, err)
	}
	changes, err := client.SyncMessages(t.Context(), application.MessageSyncInput{
		Account: testAccountID, WorkspaceID: "workspace-1", ConversationID: "conversation-1", Limit: 25,
	}, caller)
	if err != nil || len(changes.Changes) != 1 || backend.messageSyncInput.Limit != 25 {
		t.Fatalf("SyncMessages() = %+v, %v; input=%+v", changes, err, backend.messageSyncInput)
	}
	writeRoute := application.MessageWriteRoute{
		Account: testAccountID, WorkspaceID: "workspace-1",
		Actor: application.MessageActor{ID: "actor-1", Mode: application.MessageActorDelegatedUser},
	}
	writeAccess, err := client.SendMessage(t.Context(), application.MessageSendInput{
		MessageWriteRoute: writeRoute, ConversationID: "conversation-1",
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Synthetic message"},
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || backend.messageSendInput.Content.Text != "Synthetic message" {
		t.Fatalf("SendMessage() = %+v, %v; input=%+v", writeAccess, err, backend.messageSendInput)
	}
	writeAccess, err = client.CommitSendMessage(t.Context(), "opv1_message_send", caller)
	if err != nil || writeAccess.Status != "sent" || backend.commitToken != "opv1_message_send" {
		t.Fatalf("CommitSendMessage() = %+v, %v", writeAccess, err)
	}
	writeAccess, err = client.EditMessage(t.Context(), application.MessageEditInput{
		MessageWriteRoute: writeRoute, ConversationID: "conversation-1", MessageID: "message-1", Version: "version-1",
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "Edited message"},
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || backend.messageEditInput.Content.Text != "Edited message" {
		t.Fatalf("EditMessage() = %+v, %v; input=%+v", writeAccess, err, backend.messageEditInput)
	}
	writeAccess, err = client.CommitEditMessage(t.Context(), "opv1_message_edit", caller)
	if err != nil || writeAccess.Status != "edited" {
		t.Fatalf("CommitEditMessage() = %+v, %v", writeAccess, err)
	}
	writeAccess, err = client.DeleteMessage(t.Context(), application.MessageDeleteInput{
		MessageWriteRoute: writeRoute, ConversationID: "conversation-1", MessageID: "message-1", Version: "version-1",
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || backend.messageDeleteInput.MessageID != "message-1" {
		t.Fatalf("DeleteMessage() = %+v, %v; input=%+v", writeAccess, err, backend.messageDeleteInput)
	}
	writeAccess, err = client.CommitDeleteMessage(t.Context(), "opv1_message_delete", caller)
	if err != nil || writeAccess.Status != "deleted" {
		t.Fatalf("CommitDeleteMessage() = %+v, %v", writeAccess, err)
	}
	writeAccess, err = client.ReactToMessage(t.Context(), application.MessageReactionInput{
		MessageWriteRoute: writeRoute, ConversationID: "conversation-1", MessageID: "message-1", Version: "version-1",
		Reaction: "thumbsup",
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || backend.messageReactInput.Reaction != "thumbsup" {
		t.Fatalf("ReactToMessage() = %+v, %v; input=%+v", writeAccess, err, backend.messageReactInput)
	}
	writeAccess, err = client.CommitMessageReaction(t.Context(), "opv1_message_react", caller)
	if err != nil || writeAccess.Status != "reacted" {
		t.Fatalf("CommitMessageReaction() = %+v, %v", writeAccess, err)
	}
	writeAccess, err = client.CreateConversation(t.Context(), application.ConversationCreateInput{
		MessageWriteRoute: writeRoute, Kind: application.ConversationGroup,
		Visibility: application.ConversationVisibilityPrivate,
		Members:    []application.ConversationMemberInput{{ID: "member-1", Role: application.ConversationMember}},
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || len(backend.conversationCreate.Members) != 1 {
		t.Fatalf("CreateConversation() = %+v, %v; input=%+v", writeAccess, err, backend.conversationCreate)
	}
	writeAccess, err = client.CommitCreateConversation(t.Context(), "opv1_conversation_create", caller)
	if err != nil || writeAccess.Status != "conversation_created" {
		t.Fatalf("CommitCreateConversation() = %+v, %v", writeAccess, err)
	}
	writeAccess, err = client.ChangeConversationMembership(t.Context(), application.ConversationMembershipInput{
		MessageWriteRoute: writeRoute, ConversationID: "conversation-1", Version: "version-1",
		Action: application.MembershipAdd,
		Member: application.ConversationMemberInput{ID: "member-2", Role: application.ConversationMember},
	}, caller)
	if err != nil || writeAccess.Status != "approval_required" || backend.membershipInput.Member.ID != "member-2" {
		t.Fatalf("ChangeConversationMembership() = %+v, %v; input=%+v", writeAccess, err, backend.membershipInput)
	}
	writeAccess, err = client.CommitConversationMembership(t.Context(), "opv1_conversation_membership", caller)
	if err != nil || writeAccess.Status != "membership_changed" || backend.caller != caller {
		t.Fatalf("CommitConversationMembership() = %+v, %v; caller=%+v", writeAccess, err, backend.caller)
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

func TestProviderNeutralStatusRoundTripOverLocalIPC(t *testing.T) {
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
	backend := &providerNeutralBackend{fakeBackend: &fakeBackend{}}
	options := ServerOptions{
		Version: "dev", ProcessID: 123, StartedAt: time.Unix(1, 0),
		Credential: credential.Value(), ConfigDigest: strings.Repeat("a", 64),
	}
	if _, err := NewServer(backend, options); err == nil ||
		!strings.Contains(err.Error(), "default daemon account is required") {
		t.Fatalf("NewServer() error = %v, want explicit provider-neutral opt-in", err)
	}
	options.AllowNoDefaultAccount = true
	server, err := NewServer(backend, options)
	if err != nil {
		t.Fatalf("NewServer() provider-neutral error = %v", err)
	}
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
	status, err := client.Status(
		t.Context(),
		domain.Caller{Surface: "mcp", Instance: "provider-neutral-test"},
	)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.DefaultAccount != "" || status.ProtocolVersion != ProtocolVersion {
		t.Fatalf("provider-neutral Status() = %+v", status)
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
					DefaultAccount:  testAccountID,
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
	if _, err := client.Login(t.Context(), testAccountID, caller); !errors.As(err, &versionErr) {
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
