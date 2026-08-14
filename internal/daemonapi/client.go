package daemonapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

// Client calls one daemon namespace and reloads its rotating credential for
// every operation. It never retries an ambiguous application call.
type Client struct {
	endpoint localipc.Endpoint
	host     string
	http     *http.Client
}

// NewClient creates a no-TCP HTTP transport over Unix socket or named pipe.
func NewClient(endpoint localipc.Endpoint) (*Client, error) {
	return newClient(endpoint, requestHost)
}

// NewLegacyClient creates the narrowly scoped v0.6 client used only to stop an
// old session owner before migrating browser profiles.
func NewLegacyClient(endpoint localipc.Endpoint) (*Client, error) {
	return newClient(endpoint, "owa.local")
}

func newClient(endpoint localipc.Endpoint, host string) (*Client, error) {
	if endpoint.Address == "" || endpoint.CredentialPath == "" {
		return nil, errors.New("complete daemon endpoint is required")
	}
	transport := &http.Transport{
		Proxy:              nil,
		DialContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return localipc.DialContext(ctx, endpoint) },
		DisableCompression: true,
		DisableKeepAlives:  true,
		MaxConnsPerHost:    maxConcurrentCalls,
	}
	return &Client{endpoint: endpoint, host: host, http: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// Close releases idle transport resources and no daemon state.
func (client *Client) Close() error {
	client.http.CloseIdleConnections()
	return nil
}

// Login asks the session owner to ensure an interactive account session.
func (client *Client) Login(ctx context.Context, account domain.AccountID, caller domain.Caller) (LoginResult, error) {
	if err := account.Validate(); err != nil {
		return LoginResult{}, err
	}
	var result LoginResult
	if err := client.call(ctx, MethodLogin, caller, LoginInput{Account: account}, &result); err != nil {
		return LoginResult{}, err
	}
	if result.Account != account || !result.Authenticated || result.CapturedAt.IsZero() {
		return LoginResult{}, errors.New("daemon returned invalid login state")
	}
	return result, nil
}

// Logout closes exactly one account's in-memory provider sessions.
func (client *Client) Logout(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (LogoutResult, error) {
	if err := account.ValidateOpaque(); err != nil {
		return LogoutResult{}, err
	}
	var result LogoutResult
	if err := client.call(
		ctx,
		MethodLogout,
		caller,
		LogoutInput{Account: account},
		&result,
	); err != nil {
		return LogoutResult{}, err
	}
	if result.Account != account || !result.LoggedOut {
		return LogoutResult{}, errors.New("daemon returned invalid logout state")
	}
	return result, nil
}

// SessionStatus returns content-free in-memory authentication state.
func (client *Client) SessionStatus(ctx context.Context, caller domain.Caller) (SessionStatusResult, error) {
	var result SessionStatusResult
	if err := client.call(ctx, MethodSessionStatus, caller, struct{}{}, &result); err != nil {
		return SessionStatusResult{}, err
	}
	if err := validateSessionStatusResult(result); err != nil {
		return SessionStatusResult{}, err
	}
	return result, nil
}

// MonitorStatus reads one account's consent and local queue health.
func (client *Client) MonitorStatus(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (application.MonitorStatus, error) {
	if err := account.ValidateOpaque(); err != nil {
		return application.MonitorStatus{}, err
	}
	var result application.MonitorStatus
	if err := client.call(
		ctx,
		MethodMonitorStatus,
		caller,
		MonitorStatusInput{Account: account},
		&result,
	); err != nil {
		return application.MonitorStatus{}, err
	}
	if err := result.Validate(account); err != nil {
		return application.MonitorStatus{}, fmt.Errorf(
			"daemon returned invalid monitor status: %w",
			err,
		)
	}
	return result, nil
}

// ListMonitorEvents returns one bounded account-local queue page.
func (client *Client) ListMonitorEvents(
	ctx context.Context,
	input application.MonitorEventListInput,
	caller domain.Caller,
) (application.MonitorEventPage, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEventPage{}, err
	}
	var result application.MonitorEventPage
	if err := client.call(ctx, MethodEventsList, caller, input, &result); err != nil {
		return application.MonitorEventPage{}, err
	}
	if err := result.Validate(input); err != nil {
		return application.MonitorEventPage{}, fmt.Errorf(
			"daemon returned invalid monitor event page: %w",
			err,
		)
	}
	return result, nil
}

// AcknowledgeMonitorEvent changes only one account-local queue item.
func (client *Client) AcknowledgeMonitorEvent(
	ctx context.Context,
	input application.MonitorAcknowledgeInput,
	caller domain.Caller,
) (application.MonitorEvent, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEvent{}, err
	}
	var result application.MonitorEvent
	if err := client.call(ctx, MethodEventAcknowledge, caller, input, &result); err != nil {
		return application.MonitorEvent{}, err
	}
	if result.ID != input.EventID || result.State != "acknowledged" {
		return application.MonitorEvent{}, errors.New("daemon returned invalid acknowledgement")
	}
	if err := result.Validate(input.Account); err != nil {
		return application.MonitorEvent{}, fmt.Errorf(
			"daemon returned invalid acknowledgement: %w",
			err,
		)
	}
	return result, nil
}

func validateSessionStatusResult(result SessionStatusResult) error {
	seen := make(map[domain.AccountID]struct{}, len(result.Accounts))
	for index, account := range result.Accounts {
		if err := account.Account.ValidateOpaque(); err != nil {
			return errors.New("daemon returned an invalid session account")
		}
		if err := domain.AccountAlias(account.Alias).Validate(); err != nil {
			return errors.New("daemon returned an invalid session account alias")
		}
		if err := account.Provider.Validate(); err != nil {
			return errors.New("daemon returned an invalid session provider")
		}
		if account.MailProvider != "" {
			if err := account.MailProvider.Validate(); err != nil {
				return errors.New("daemon returned an invalid mail session provider")
			}
		}
		if account.CalendarProvider != "" {
			if err := account.CalendarProvider.Validate(); err != nil {
				return errors.New("daemon returned an invalid calendar session provider")
			}
		}
		if account.TaskProvider != "" {
			if err := account.TaskProvider.Validate(); err != nil {
				return errors.New("daemon returned an invalid task session provider")
			}
		}
		if account.MailProvider == "" && account.CalendarProvider == "" && account.TaskProvider == "" {
			return errors.New("daemon returned a session without a provider route")
		}
		expectedProvider := account.MailProvider
		if expectedProvider == "" {
			expectedProvider = account.CalendarProvider
		}
		if expectedProvider == "" {
			expectedProvider = account.TaskProvider
		}
		if account.Provider != expectedProvider {
			return errors.New("daemon returned an inconsistent primary session provider")
		}
		if (account.MailProvider != "") != (account.Services.Mail != nil) ||
			(account.CalendarProvider != "") != (account.Services.Calendar != nil) ||
			(account.TaskProvider != "") != (account.Services.Tasks != nil) {
			return errors.New("daemon returned inconsistent service authentication routes")
		}
		authenticatedServices := 0
		pendingServices := 0
		for _, service := range account.Services.Values() {
			if err := service.Validate(account.Account, account.Alias); err != nil {
				return errors.New("daemon returned invalid service authentication state")
			}
			var provider domain.ProviderID
			switch service.Service {
			case application.AuthenticationServiceMail:
				provider = account.MailProvider
			case application.AuthenticationServiceCalendar:
				provider = account.CalendarProvider
			case application.AuthenticationServiceTasks:
				provider = account.TaskProvider
			}
			if service.Provider != provider {
				return errors.New("daemon returned an inconsistent service provider")
			}
			switch service.State {
			case application.AuthenticationStateAuthenticated:
				authenticatedServices++
			case application.AuthenticationStatePending:
				pendingServices++
			case application.AuthenticationStateSignedOut,
				application.AuthenticationStateReauthenticationNeeded:
			}
		}
		if _, exists := seen[account.Account]; exists {
			return errors.New("daemon returned duplicate session accounts")
		}
		seen[account.Account] = struct{}{}
		if index > 0 && result.Accounts[index-1].Alias >= account.Alias {
			return errors.New("daemon returned unsorted session accounts")
		}
		expectedState := "signed_out"
		if authenticatedServices > 0 {
			expectedState = "authenticated"
		} else if pendingServices > 0 {
			expectedState = "pending"
		}
		if account.State != expectedState ||
			account.Authenticated != (authenticatedServices > 0) {
			return errors.New("daemon returned inconsistent aggregate session state")
		}
		switch expectedState {
		case "authenticated":
			if account.CapturedAt == nil || account.CapturedAt.IsZero() {
				return errors.New("daemon returned invalid authenticated session state")
			}
			if account.Capabilities == nil || account.Capabilities.Validate() != nil {
				return errors.New("daemon returned invalid account capabilities")
			}
			if account.Capabilities.Mail !=
				(account.Services.Mail != nil && account.Services.Mail.State == application.AuthenticationStateAuthenticated) ||
				account.Capabilities.Calendar !=
					(account.Services.Calendar != nil && account.Services.Calendar.State == application.AuthenticationStateAuthenticated) ||
				account.Capabilities.Tasks !=
					(account.Services.Tasks != nil && account.Services.Tasks.State == application.AuthenticationStateAuthenticated) {
				return errors.New("daemon returned capabilities inconsistent with service authentication")
			}
			if len(account.Degradations) > 32 {
				return errors.New("daemon returned unbounded account degradations")
			}
			for _, degradation := range account.Degradations {
				if degradation.Validate() != nil {
					return errors.New("daemon returned invalid account degradation")
				}
			}
		case "pending", "signed_out":
			if account.CapturedAt != nil ||
				account.Capabilities != nil || len(account.Degradations) != 0 {
				return errors.New("daemon returned invalid inactive session state")
			}
		default:
			return errors.New("daemon returned unknown session state")
		}
	}
	return nil
}

// TerminalLogin starts or advances a caller-bound text-only browser login.
func (client *Client) TerminalLogin(
	ctx context.Context,
	input TerminalLoginInput,
	caller domain.Caller,
) (TerminalLoginResult, error) {
	if err := input.validate(); err != nil {
		return TerminalLoginResult{}, err
	}
	var result TerminalLoginResult
	if err := client.call(ctx, MethodTerminalLogin, caller, input, &result); err != nil {
		return TerminalLoginResult{}, err
	}
	if err := validateTerminalLoginResult(input, result); err != nil {
		return TerminalLoginResult{}, err
	}
	return result, nil
}

func validateTerminalLoginResult(input TerminalLoginInput, result TerminalLoginResult) error {
	if result.Account != input.Account {
		return errors.New("daemon returned terminal login state for a different account")
	}
	switch result.Status {
	case "pending":
		if !terminalSessionIDPattern.MatchString(result.SessionID) || result.View == nil ||
			!result.CapturedAt.IsZero() {
			return errors.New("daemon returned invalid pending terminal login state")
		}
		if input.SessionID != "" && result.SessionID != input.SessionID {
			return errors.New("daemon returned a different terminal login session")
		}
		for _, control := range result.View.Controls {
			if !terminalControlIDPattern.MatchString(control.ID) ||
				control.Kind != "input" && control.Kind != "activate" {
				return errors.New("daemon returned an invalid terminal login control")
			}
		}
		return nil
	case "authenticated":
		if result.CapturedAt.IsZero() || result.View != nil {
			return errors.New("daemon returned invalid authenticated terminal login state")
		}
		return nil
	case "cancelled":
		if !result.CapturedAt.IsZero() || result.View != nil {
			return errors.New("daemon returned invalid cancelled terminal login state")
		}
		return nil
	default:
		return errors.New("daemon returned an unknown terminal login status")
	}
}

func (client *Client) ListMail(ctx context.Context, input application.MailListInput, caller domain.Caller) (application.MailPage, error) {
	var result application.MailPage
	return result, client.call(ctx, MethodMailList, caller, input, &result)
}

// SearchMail executes one bounded, read-only AQS search through the session owner.
func (client *Client) SearchMail(ctx context.Context, input application.MailSearchInput, caller domain.Caller) (application.MailPage, error) {
	var result application.MailPage
	return result, client.call(ctx, MethodMailSearch, caller, input, &result)
}

// SearchAllMail returns one validated read-only projection without exposing
// daemon-owned account sessions to the caller.
func (client *Client) SearchAllMail(
	ctx context.Context,
	input application.MailProjectionInput,
	caller domain.Caller,
) (application.MailProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return application.MailProjectionPage{}, err
	}
	var result application.MailProjectionPage
	if err := client.call(
		ctx,
		MethodMailSearchAll,
		caller,
		input,
		&result,
	); err != nil {
		return application.MailProjectionPage{}, err
	}
	if err := result.Validate(); err != nil {
		return application.MailProjectionPage{}, fmt.Errorf(
			"validate daemon mail projection: %w",
			err,
		)
	}
	return result, nil
}

// ListMailFolders discovers bounded folder metadata through the session owner.
func (client *Client) ListMailFolders(ctx context.Context, input application.MailFolderListInput, caller domain.Caller) (application.MailFolderPage, error) {
	var result application.MailFolderPage
	return result, client.call(ctx, MethodMailFolders, caller, input, &result)
}

func (client *Client) GetMailBody(ctx context.Context, input application.MailBodyInput, caller domain.Caller) (application.MailBodyAccess, error) {
	var result application.MailBodyAccess
	return result, client.call(ctx, MethodMailGetBody, caller, input, &result)
}

func (client *Client) CommitMailBody(ctx context.Context, token string, caller domain.Caller) (application.MailBodyAccess, error) {
	var result application.MailBodyAccess
	return result, client.call(ctx, MethodMailCommitBody, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) GetMailAttachment(ctx context.Context, input application.MailAttachmentInput, caller domain.Caller) (application.MailAttachmentAccess, error) {
	var result application.MailAttachmentAccess
	return result, client.call(ctx, MethodMailGetAttachment, caller, input, &result)
}

func (client *Client) CommitMailAttachment(ctx context.Context, token string, caller domain.Caller) (application.MailAttachmentAccess, error) {
	var result application.MailAttachmentAccess
	return result, client.call(ctx, MethodMailCommitAttachment, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) CreateMailDraft(ctx context.Context, input application.MailDraftInput, caller domain.Caller) (application.MailDraftAccess, error) {
	var result application.MailDraftAccess
	return result, client.call(ctx, MethodMailCreateDraft, caller, input, &result)
}

func (client *Client) CommitMailDraft(ctx context.Context, token string, caller domain.Caller) (application.MailDraftAccess, error) {
	var result application.MailDraftAccess
	return result, client.call(ctx, MethodMailCommitDraft, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SendMail(ctx context.Context, input application.MailSendInput, caller domain.Caller) (application.MailSendAccess, error) {
	var result application.MailSendAccess
	return result, client.call(ctx, MethodMailSend, caller, input, &result)
}

func (client *Client) CommitMailSend(ctx context.Context, token string, caller domain.Caller) (application.MailSendAccess, error) {
	var result application.MailSendAccess
	return result, client.call(ctx, MethodMailCommitSend, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SendMailDraft(ctx context.Context, input application.MailDraftSendInput, caller domain.Caller) (application.MailDraftSendAccess, error) {
	var result application.MailDraftSendAccess
	return result, client.call(ctx, MethodMailSendDraft, caller, input, &result)
}

func (client *Client) CommitMailSendDraft(ctx context.Context, token string, caller domain.Caller) (application.MailDraftSendAccess, error) {
	var result application.MailDraftSendAccess
	return result, client.call(ctx, MethodMailCommitSendDraft, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) MoveMail(ctx context.Context, input application.MailMoveInput, caller domain.Caller) (application.MailMoveAccess, error) {
	var result application.MailMoveAccess
	return result, client.call(ctx, MethodMailMove, caller, input, &result)
}

func (client *Client) CommitMailMove(ctx context.Context, token string, caller domain.Caller) (application.MailMoveAccess, error) {
	var result application.MailMoveAccess
	return result, client.call(ctx, MethodMailCommitMove, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SetMailReadState(ctx context.Context, input application.MailReadStateInput, caller domain.Caller) (application.MailReadStateAccess, error) {
	var result application.MailReadStateAccess
	return result, client.call(ctx, MethodMailReadState, caller, input, &result)
}

func (client *Client) CommitMailReadState(ctx context.Context, token string, caller domain.Caller) (application.MailReadStateAccess, error) {
	var result application.MailReadStateAccess
	return result, client.call(ctx, MethodMailCommitState, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) DeleteMail(ctx context.Context, input application.MailDeleteInput, caller domain.Caller) (application.MailDeleteAccess, error) {
	var result application.MailDeleteAccess
	return result, client.call(ctx, MethodMailDelete, caller, input, &result)
}

func (client *Client) CommitMailDelete(ctx context.Context, token string, caller domain.Caller) (application.MailDeleteAccess, error) {
	var result application.MailDeleteAccess
	return result, client.call(ctx, MethodMailCommitDelete, caller, ApprovalInput{Token: token}, &result)
}

// ListCalendarFolders discovers bounded selectable calendar metadata.
func (client *Client) ListCalendarFolders(
	ctx context.Context,
	input application.CalendarFolderListInput,
	caller domain.Caller,
) (application.CalendarFolderPage, error) {
	var result application.CalendarFolderPage
	return result, client.call(ctx, MethodCalendarFolders, caller, input, &result)
}

func (client *Client) ListCalendar(ctx context.Context, input application.CalendarListInput, caller domain.Caller) (application.CalendarPage, error) {
	var result application.CalendarPage
	return result, client.call(ctx, MethodCalendarList, caller, input, &result)
}

// ListAgenda returns one validated read-only cross-account event projection.
func (client *Client) ListAgenda(
	ctx context.Context,
	input application.AgendaProjectionInput,
	caller domain.Caller,
) (application.AgendaProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return application.AgendaProjectionPage{}, err
	}
	var result application.AgendaProjectionPage
	if err := client.call(
		ctx,
		MethodAgendaList,
		caller,
		input,
		&result,
	); err != nil {
		return application.AgendaProjectionPage{}, err
	}
	if err := result.Validate(); err != nil {
		return application.AgendaProjectionPage{}, fmt.Errorf(
			"validate daemon agenda projection: %w",
			err,
		)
	}
	return result, nil
}

// CreateCalendar prepares an immutable calendar event preview.
func (client *Client) CreateCalendar(ctx context.Context, input application.CalendarCreateInput, caller domain.Caller) (application.CalendarCreateAccess, error) {
	var result application.CalendarCreateAccess
	return result, client.call(ctx, MethodCalendarCreate, caller, input, &result)
}

// CommitCalendarCreate consumes one caller-bound calendar event preview.
func (client *Client) CommitCalendarCreate(ctx context.Context, token string, caller domain.Caller) (application.CalendarCreateAccess, error) {
	var result application.CalendarCreateAccess
	return result, client.call(ctx, MethodCalendarCommit, caller, ApprovalInput{Token: token}, &result)
}

// UpdateCalendar prepares an immutable patch preview for one event version.
func (client *Client) UpdateCalendar(ctx context.Context, input application.CalendarUpdateInput, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	var result application.CalendarUpdateAccess
	return result, client.call(ctx, MethodCalendarUpdate, caller, input, &result)
}

// CommitCalendarUpdate consumes one caller-bound calendar update preview.
func (client *Client) CommitCalendarUpdate(ctx context.Context, token string, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	var result application.CalendarUpdateAccess
	return result, client.call(ctx, MethodCalendarCommitUpdate, caller, ApprovalInput{Token: token}, &result)
}

// CancelCalendar prepares a destructive preview for one event version.
func (client *Client) CancelCalendar(ctx context.Context, input application.CalendarCancelInput, caller domain.Caller) (application.CalendarCancelAccess, error) {
	var result application.CalendarCancelAccess
	return result, client.call(ctx, MethodCalendarCancel, caller, input, &result)
}

// CommitCalendarCancel consumes one caller-bound cancellation preview.
func (client *Client) CommitCalendarCancel(ctx context.Context, token string, caller domain.Caller) (application.CalendarCancelAccess, error) {
	var result application.CalendarCancelAccess
	return result, client.call(ctx, MethodCalendarCommitCancel, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
	caller domain.Caller,
) (application.TaskListPage, error) {
	if err := input.Validate(); err != nil {
		return application.TaskListPage{}, err
	}
	var result application.TaskListPage
	return result, client.call(ctx, MethodTaskLists, caller, input, &result)
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	if err := input.Validate(); err != nil {
		return application.TaskPage{}, err
	}
	var result application.TaskPage
	return result, client.call(ctx, MethodTaskList, caller, input, &result)
}

func (client *Client) ListAllTasks(
	ctx context.Context,
	input application.TaskProjectionInput,
	caller domain.Caller,
) (application.TaskProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return application.TaskProjectionPage{}, err
	}
	var result application.TaskProjectionPage
	if err := client.call(ctx, MethodTaskListAll, caller, input, &result); err != nil {
		return application.TaskProjectionPage{}, err
	}
	if err := result.Validate(); err != nil {
		return application.TaskProjectionPage{}, fmt.Errorf("validate daemon task projection: %w", err)
	}
	return result, nil
}

func (client *Client) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
	caller domain.Caller,
) (application.Task, error) {
	if err := input.Validate(); err != nil {
		return application.Task{}, err
	}
	var result application.Task
	return result, client.call(ctx, MethodTaskGet, caller, input, &result)
}

func (client *Client) SearchTasks(
	ctx context.Context,
	input application.TaskSearchInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	if err := input.Validate(); err != nil {
		return application.TaskPage{}, err
	}
	var result application.TaskPage
	return result, client.call(ctx, MethodTaskSearch, caller, input, &result)
}

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
	caller domain.Caller,
) (application.TaskChangePage, error) {
	if err := input.ValidateRoute(); err != nil {
		return application.TaskChangePage{}, err
	}
	var result application.TaskChangePage
	return result, client.call(ctx, MethodTaskSync, caller, input, &result)
}

func (client *Client) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.TaskWriteAccess{}, err
	}
	var result application.TaskWriteAccess
	return result, client.call(ctx, MethodTaskCreate, caller, input, &result)
}

func (client *Client) CommitTaskCreate(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return client.commitTask(ctx, MethodTaskCommitCreate, token, caller)
}

func (client *Client) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.TaskWriteAccess{}, err
	}
	var result application.TaskWriteAccess
	return result, client.call(ctx, MethodTaskUpdate, caller, input, &result)
}

func (client *Client) CommitTaskUpdate(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return client.commitTask(ctx, MethodTaskCommitUpdate, token, caller)
}

func (client *Client) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return client.prepareTaskState(ctx, MethodTaskComplete, input, caller)
}

func (client *Client) CommitTaskComplete(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return client.commitTask(ctx, MethodTaskCommitComplete, token, caller)
}

func (client *Client) ReopenTask(
	ctx context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return client.prepareTaskState(ctx, MethodTaskReopen, input, caller)
}

func (client *Client) CommitTaskReopen(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return client.commitTask(ctx, MethodTaskCommitReopen, token, caller)
}

func (client *Client) DeleteTask(
	ctx context.Context,
	input application.TaskDeleteInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return client.prepareTaskState(ctx, MethodTaskDelete, input, caller)
}

func (client *Client) CommitTaskDelete(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return client.commitTask(ctx, MethodTaskCommitDelete, token, caller)
}

func (client *Client) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
	caller domain.Caller,
) (application.ConversationPage, error) {
	if err := input.Validate(); err != nil {
		return application.ConversationPage{}, err
	}
	var result application.ConversationPage
	return result, client.call(ctx, MethodMessageConversations, caller, input, &result)
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	if err := input.Validate(); err != nil {
		return application.MessagePage{}, err
	}
	var result application.MessagePage
	return result, client.call(ctx, MethodMessageList, caller, input, &result)
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	if err := input.Validate(); err != nil {
		return application.MessagePage{}, err
	}
	var result application.MessagePage
	return result, client.call(ctx, MethodMessageSearch, caller, input, &result)
}

func (client *Client) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageSensitiveAccess{}, err
	}
	var result application.MessageSensitiveAccess
	return result, client.call(ctx, MethodMessageGet, caller, input, &result)
}

func (client *Client) CommitGetMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	var result application.MessageSensitiveAccess
	return result, client.call(ctx, MethodMessageCommitGet, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) GetMessageAttachment(
	ctx context.Context,
	input application.MessageAttachmentGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageSensitiveAccess{}, err
	}
	var result application.MessageSensitiveAccess
	return result, client.call(ctx, MethodMessageAttachment, caller, input, &result)
}

func (client *Client) CommitGetMessageAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	var result application.MessageSensitiveAccess
	return result, client.call(ctx, MethodMessageCommitAttach, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SyncMessages(
	ctx context.Context,
	input application.MessageSyncInput,
	caller domain.Caller,
) (application.MessageChangePage, error) {
	if err := input.ValidateRoute(); err != nil {
		return application.MessageChangePage{}, err
	}
	var result application.MessageChangePage
	return result, client.call(ctx, MethodMessageSync, caller, input, &result)
}

func (client *Client) SendMessage(
	ctx context.Context,
	input application.MessageSendInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodMessageSend, input, caller)
}

func (client *Client) CommitSendMessage(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodMessageCommitSend, token, caller)
}

func (client *Client) EditMessage(
	ctx context.Context,
	input application.MessageEditInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodMessageEdit, input, caller)
}

func (client *Client) CommitEditMessage(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodMessageCommitEdit, token, caller)
}

func (client *Client) DeleteMessage(
	ctx context.Context,
	input application.MessageDeleteInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodMessageDelete, input, caller)
}

func (client *Client) CommitDeleteMessage(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodMessageCommitDelete, token, caller)
}

func (client *Client) ReactToMessage(
	ctx context.Context,
	input application.MessageReactionInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodMessageReact, input, caller)
}

func (client *Client) CommitMessageReaction(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodMessageCommitReact, token, caller)
}

func (client *Client) CreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodConversationCreate, input, caller)
}

func (client *Client) CommitCreateConversation(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodConversationCommit, token, caller)
}

func (client *Client) ChangeConversationMembership(
	ctx context.Context,
	input application.ConversationMembershipInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.MessageWriteAccess{}, err
	}
	return client.prepareMessageWrite(ctx, MethodMessageMembership, input, caller)
}

func (client *Client) CommitConversationMembership(ctx context.Context, token string, caller domain.Caller) (application.MessageWriteAccess, error) {
	return client.commitMessageWrite(ctx, MethodMessageCommitMember, token, caller)
}

func (client *Client) prepareMessageWrite(
	ctx context.Context,
	method Method,
	input any,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	var result application.MessageWriteAccess
	return result, client.call(ctx, method, caller, input, &result)
}

func (client *Client) commitMessageWrite(
	ctx context.Context,
	method Method,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	var result application.MessageWriteAccess
	return result, client.call(ctx, method, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) prepareTaskState(
	ctx context.Context,
	method Method,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return application.TaskWriteAccess{}, err
	}
	var result application.TaskWriteAccess
	return result, client.call(ctx, method, caller, input, &result)
}

func (client *Client) commitTask(
	ctx context.Context,
	method Method,
	token string,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	var result application.TaskWriteAccess
	return result, client.call(ctx, method, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) call(ctx context.Context, method Method, caller domain.Caller, input, output any) error {
	return client.callVersion(ctx, ProtocolVersion, method, caller, input, output)
}

func (client *Client) callVersion(
	ctx context.Context,
	protocolVersion int,
	method Method,
	caller domain.Caller,
	input, output any,
) error {
	credential, err := localipc.LoadCredential(client.endpoint)
	if err != nil {
		return fmt.Errorf("load daemon credential: %w", err)
	}
	return client.callWithCredential(
		ctx,
		protocolVersion,
		credential,
		method,
		caller,
		input,
		output,
	)
}

func (client *Client) callWithCredential(
	ctx context.Context,
	protocolVersion int,
	credential string,
	method Method,
	caller domain.Caller,
	input, output any,
) error {
	if !method.valid() {
		return errors.New("invalid daemon method")
	}
	if protocolVersion < 1 {
		return errors.New("invalid daemon protocol version")
	}
	if err := localipc.ValidateCredential(credential); err != nil {
		return errors.New("invalid daemon credential")
	}
	if err := caller.Validate(); err != nil {
		return err
	}
	params, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode daemon params: %w", err)
	}
	id, err := newRequestID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(requestEnvelope{
		Version: protocolVersion, ID: id, Method: method, Caller: caller, Params: params,
	})
	if err != nil {
		return fmt.Errorf("encode daemon request: %w", err)
	}
	if len(payload) > maxRequestBytes {
		return errors.New("daemon request exceeds maximum size")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+client.host+requestPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build daemon request: %w", err)
	}
	request.Header.Set("Authorization", authorizationType+credential)
	request.Header.Set("Content-Type", contentType)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("call local daemon: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read daemon response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return errors.New("daemon response exceeds maximum size")
	}
	var envelope responseEnvelope
	if err := decodeStrict(bytes.NewReader(body), &envelope); err != nil {
		return errors.New("daemon returned an invalid response")
	}
	if envelope.ID != id && envelope.ID != "" {
		return errors.New("daemon returned a mismatched response")
	}
	if envelope.Version != protocolVersion {
		if envelope.Version < 1 {
			return errors.New("daemon returned an invalid response")
		}
		rejected := response.StatusCode == http.StatusBadRequest &&
			envelope.ID == id &&
			envelope.Error != nil &&
			envelope.Error.Code == "invalid_request" &&
			envelope.Error.Message == fmt.Sprintf(
				"unsupported daemon protocol version %d",
				protocolVersion,
			)
		return &ProtocolVersionError{
			ClientVersion: protocolVersion,
			DaemonVersion: envelope.Version,
			rejected:      rejected,
		}
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if response.StatusCode != http.StatusOK || len(envelope.Result) == 0 {
		return fmt.Errorf("daemon returned HTTP %d without a result", response.StatusCode)
	}
	if err := decodeStrict(bytes.NewReader(envelope.Result), output); err != nil {
		return errors.New("daemon returned an invalid result")
	}
	return nil
}

func newRequestID() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate daemon request ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}
