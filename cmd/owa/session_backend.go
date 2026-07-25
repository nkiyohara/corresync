package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/application"
	"github.com/nkiyohara/owa-bridge/internal/approval"
	"github.com/nkiyohara/owa-bridge/internal/audit"
	"github.com/nkiyohara/owa-bridge/internal/browser"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/owa"
	"github.com/nkiyohara/owa-bridge/internal/paths"
	"github.com/nkiyohara/owa-bridge/internal/session"
)

type sessionAccount struct {
	handle   browserHandle
	mail     *application.MailService
	calendar *application.CalendarService
	captured time.Time
}

type sessionPreview struct {
	account   domain.AccountID
	expiresAt time.Time
}

type terminalLoginSession struct {
	id       string
	account  domain.AccountID
	caller   domain.Caller
	handle   terminalBrowserHandle
	deadline time.Time
	view     daemonapi.TerminalLoginView
}

// sessionBackend lazily opens one dedicated browser per configured account and
// keeps it for the lifetime of its owning server. Every adapter call passes
// through the same application guard and content-free audit recorder.
type sessionBackend struct {
	app           *runtime
	configuration config.Config
	guard         *application.Guard
	recorder      *audit.FileRecorder

	mu        sync.Mutex
	accounts  map[domain.AccountID]sessionAccount
	previews  map[string]sessionPreview
	closed    bool
	active    sync.WaitGroup
	lifecycle context.Context
	cancel    context.CancelFunc
	close     sync.Once
	closeErr  error

	terminalSessions map[string]*terminalLoginSession
	terminalAccounts map[domain.AccountID]string
}

func newSessionBackend(app *runtime) (*sessionBackend, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return nil, err
	}
	auditPath, err := paths.AuditPath()
	if err != nil {
		return nil, err
	}
	recorder, err := audit.NewFileRecorder(auditPath, audit.Options{})
	if err != nil {
		return nil, err
	}
	approvals, err := approval.NewStore(approval.Options{})
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}
	guard, err := application.NewGuard(configuration.Policy.Rules(), approvals, recorder)
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	return &sessionBackend{
		app:              app,
		configuration:    configuration,
		guard:            guard,
		recorder:         recorder,
		accounts:         make(map[domain.AccountID]sessionAccount),
		previews:         make(map[string]sessionPreview),
		lifecycle:        lifecycle,
		cancel:           cancel,
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
	}, nil
}

func (backend *sessionBackend) DefaultAccount() domain.AccountID {
	return domain.AccountID(backend.configuration.DefaultAccount)
}

func (backend *sessionBackend) Login(
	ctx context.Context,
	accountID domain.AccountID,
	_ domain.Caller,
) (daemonapi.LoginResult, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return daemonapi.LoginResult{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, err := backend.accountServices(ctx, accountID)
	if err != nil {
		return daemonapi.LoginResult{}, err
	}
	return daemonapi.LoginResult{
		Account: accountID, Authenticated: true, CapturedAt: account.captured,
	}, nil
}

func (backend *sessionBackend) SessionStatus(
	_ context.Context,
	_ domain.Caller,
) (daemonapi.SessionStatusResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return daemonapi.SessionStatusResult{}, errors.New("session backend is closed")
	}
	aliases := make([]string, 0, len(backend.configuration.Accounts))
	for alias := range backend.configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	result := daemonapi.SessionStatusResult{
		Accounts: make([]daemonapi.SessionStatus, 0, len(aliases)),
	}
	for _, alias := range aliases {
		accountID := domain.AccountID(alias)
		state := daemonapi.SessionStatus{
			Account: accountID,
			State:   "signed_out",
		}
		if account, exists := backend.accounts[accountID]; exists {
			capturedAt := account.captured.UTC()
			state.State = "authenticated"
			state.Authenticated = true
			state.CapturedAt = &capturedAt
		} else if _, exists := backend.terminalAccounts[accountID]; exists {
			state.State = "pending"
		}
		result.Accounts = append(result.Accounts, state)
	}
	return result, nil
}

func (backend *sessionBackend) TerminalLogin(
	ctx context.Context,
	input daemonapi.TerminalLoginInput,
	caller domain.Caller,
) (daemonapi.TerminalLoginResult, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return daemonapi.TerminalLoginResult{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	defer backend.active.Done()
	defer backend.mu.Unlock()

	if account, exists := backend.accounts[input.Account]; exists {
		return authenticatedTerminalResult(input.Account, account.captured), nil
	}

	interaction, err := backend.terminalInteraction(input, caller)
	if err != nil {
		return daemonapi.TerminalLoginResult{}, err
	}
	if time.Now().After(interaction.deadline) {
		closeErr := backend.dropTerminalInteraction(interaction, true)
		return daemonapi.TerminalLoginResult{}, errors.Join(
			errors.New("terminal login expired; start it again"), closeErr,
		)
	}
	if input.Action != nil && input.Action.Type == "cancel" {
		return daemonapi.TerminalLoginResult{
			Account: input.Account, Status: "cancelled",
		}, backend.dropTerminalInteraction(interaction, true)
	}

	if input.Action != nil && input.Action.Type != "refresh" {
		action, err := terminalBrowserAction(*input.Action)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
		if err := interaction.handle.TerminalAct(ctx, action); err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
	}

	credentials, credentialsErr := interaction.handle.CurrentSession()
	if credentialsErr == nil {
		configured := backend.configuration.Accounts[string(input.Account)]
		account, err := backend.accountFromHandle(configured, interaction.handle, credentials)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, errors.Join(
				err, backend.dropTerminalInteraction(interaction, true),
			)
		}
		backend.accounts[input.Account] = account
		_ = backend.dropTerminalInteraction(interaction, false)
		return authenticatedTerminalResult(input.Account, account.captured), nil
	}
	if !errors.Is(credentialsErr, session.ErrNotReady) {
		return daemonapi.TerminalLoginResult{}, credentialsErr
	}

	refreshView := input.Action == nil || input.Action.Type == "refresh" ||
		input.Action.Type == "activate" || input.Action.Key == "enter"
	if refreshView {
		view, err := interaction.handle.TerminalSnapshot(ctx)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
		interaction.view = terminalLoginView(view)
	}
	view := interaction.view
	return daemonapi.TerminalLoginResult{
		Account: input.Account, SessionID: interaction.id, Status: "pending", View: &view,
	}, nil
}

func (backend *sessionBackend) terminalInteraction(
	input daemonapi.TerminalLoginInput,
	caller domain.Caller,
) (*terminalLoginSession, error) {
	if input.SessionID != "" {
		interaction, exists := backend.terminalSessions[input.SessionID]
		if !exists || interaction.account != input.Account || interaction.caller != caller {
			return nil, errors.New("invalid or expired terminal login session")
		}
		return interaction, nil
	}
	if existingID, exists := backend.terminalAccounts[input.Account]; exists {
		interaction := backend.terminalSessions[existingID]
		if interaction == nil || interaction.caller != caller {
			return nil, errors.New("a terminal login is already active for this account")
		}
		return interaction, nil
	}
	configured, exists := backend.configuration.Accounts[string(input.Account)]
	if !exists {
		return nil, fmt.Errorf("account %q is not configured", input.Account)
	}
	profileDirectory, err := paths.ProfileDir(input.Account)
	if err != nil {
		return nil, err
	}
	handle, err := backend.app.launch(backend.lifecycle, browser.Options{
		Origin: configured.Origin, ProfileDir: profileDirectory,
		Executable: backend.configuration.Browser.Executable, Headless: true,
	})
	if err != nil {
		return nil, err
	}
	terminalHandle, supported := handle.(terminalBrowserHandle)
	if !supported {
		return nil, errors.Join(
			errors.New("configured browser launcher does not support terminal login"),
			handle.Close(),
		)
	}
	id, err := newTerminalLoginSessionID()
	if err != nil {
		return nil, errors.Join(err, handle.Close())
	}
	interaction := &terminalLoginSession{
		id: id, account: input.Account, caller: caller, handle: terminalHandle,
		deadline: time.Now().Add(time.Duration(backend.configuration.Browser.LoginTimeout)),
	}
	backend.terminalSessions[id] = interaction
	backend.terminalAccounts[input.Account] = id
	return interaction, nil
}

func (backend *sessionBackend) dropTerminalInteraction(
	interaction *terminalLoginSession,
	closeHandle bool,
) error {
	delete(backend.terminalSessions, interaction.id)
	delete(backend.terminalAccounts, interaction.account)
	if closeHandle {
		return interaction.handle.Close()
	}
	return nil
}

func authenticatedTerminalResult(
	account domain.AccountID,
	capturedAt time.Time,
) daemonapi.TerminalLoginResult {
	return daemonapi.TerminalLoginResult{
		Account: account, Status: "authenticated", CapturedAt: capturedAt,
	}
}

func terminalLoginView(view browser.TerminalView) daemonapi.TerminalLoginView {
	controls := make([]daemonapi.TerminalLoginControl, 0, len(view.Controls))
	for _, control := range view.Controls {
		controls = append(controls, daemonapi.TerminalLoginControl{
			ID: control.ID, Kind: control.Kind, Name: control.Name,
			Sensitive: control.Sensitive, Disabled: control.Disabled,
		})
	}
	return daemonapi.TerminalLoginView{
		Origin: view.Origin, Title: view.Title, Text: view.Text, Controls: controls,
	}
}

func terminalBrowserAction(action daemonapi.TerminalLoginAction) (browser.TerminalAction, error) {
	browserAction := browser.TerminalAction{ElementID: action.ControlID}
	switch action.Type {
	case "activate":
		browserAction.Kind = browser.TerminalActivate
	case "focus":
		browserAction.Kind = browser.TerminalFocus
	case "key":
		browserAction.Kind = browser.TerminalKey
		switch action.Key {
		case "enter":
			browserAction.Key = "Enter"
		case "backspace":
			browserAction.Key = "Backspace"
		case "tab":
			browserAction.Key = "Tab"
		default:
			browserAction.Key = action.Key
		}
	default:
		return browser.TerminalAction{}, errors.New("unsupported terminal login action")
	}
	return browserAction, nil
}

func newTerminalLoginSessionID() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate terminal login session ID: %w", err)
	}
	return "tls1_" + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (backend *sessionBackend) ListMail(
	ctx context.Context,
	input application.MailListInput,
	caller domain.Caller,
) (application.MailPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailPage{}, err
	}
	return services.mail.List(ctx, input, caller)
}

func (backend *sessionBackend) SearchMail(
	ctx context.Context,
	input application.MailSearchInput,
	caller domain.Caller,
) (application.MailPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailPage{}, err
	}
	return services.mail.Search(ctx, input, caller)
}

func (backend *sessionBackend) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
	caller domain.Caller,
) (application.MailFolderPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailFolderPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	return services.mail.ListFolders(ctx, input, caller)
}

func (backend *sessionBackend) GetMailBody(
	ctx context.Context,
	input application.MailBodyInput,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailBodyAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailBodyAccess{}, err
	}
	access, err := services.mail.GetBody(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailAttachmentAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailAttachmentAccess{}, err
	}
	access, err := services.mail.GetAttachment(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailDraftAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailDraftAccess{}, err
	}
	access, err := services.mail.CreateDraft(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailDraft(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailDraftAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailDraftAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitDraft(ctx, token, caller)
	if err != nil {
		return application.MailDraftAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) SendMail(
	ctx context.Context,
	input application.MailSendInput,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailSendAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailSendAccess{}, err
	}
	access, err := services.mail.Send(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailSend(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailSendAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailSendAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitSend(ctx, token, caller)
	if err != nil {
		return application.MailSendAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailMoveAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailMoveAccess{}, err
	}
	access, err := services.mail.Move(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailMove(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailMoveAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailMoveAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitMove(ctx, token, caller)
	if err != nil {
		return application.MailMoveAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailReadStateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailReadStateAccess{}, err
	}
	access, err := services.mail.SetReadState(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailReadState(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailReadStateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailReadStateAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitReadState(ctx, token, caller)
	if err != nil {
		return application.MailReadStateAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
	caller domain.Caller,
) (application.MailDeleteAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailDeleteAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.MailDeleteAccess{}, err
	}
	access, err := services.mail.Delete(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailDelete(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailDeleteAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailDeleteAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailDeleteAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitDelete(ctx, token, caller)
	if err != nil {
		return application.MailDeleteAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) CommitMailBody(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailBodyAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailBodyAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitBody(ctx, token, caller)
	if err != nil {
		return application.MailBodyAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) CommitMailAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailAttachmentAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.mail == nil {
		return application.MailAttachmentAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.mail.CommitAttachment(ctx, token, caller)
	if err != nil {
		return application.MailAttachmentAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) rememberPreview(
	token string,
	account domain.AccountID,
	expiresAt time.Time,
) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	now := time.Now()
	for pendingToken, preview := range backend.previews {
		if !now.Before(preview.expiresAt) {
			delete(backend.previews, pendingToken)
		}
	}
	backend.previews[token] = sessionPreview{account: account, expiresAt: expiresAt}
}

func (backend *sessionBackend) accountForPreview(token string) (sessionAccount, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	preview, exists := backend.previews[token]
	if !exists {
		return sessionAccount{}, false
	}
	if !time.Now().Before(preview.expiresAt) {
		delete(backend.previews, token)
		return sessionAccount{}, false
	}
	account, exists := backend.accounts[preview.account]
	return account, exists
}

func (backend *sessionBackend) forgetPreview(token string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	delete(backend.previews, token)
}

func (backend *sessionBackend) ListCalendar(
	ctx context.Context,
	input application.CalendarListInput,
	caller domain.Caller,
) (application.CalendarPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.CalendarPage{}, err
	}
	return services.calendar.List(ctx, input, caller)
}

func (backend *sessionBackend) CreateCalendar(
	ctx context.Context,
	input application.CalendarCreateInput,
	caller domain.Caller,
) (application.CalendarCreateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarCreateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.CalendarCreateAccess{}, err
	}
	access, err := services.calendar.Create(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarCreate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCreateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarCreateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.calendar == nil {
		return application.CalendarCreateAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.calendar.CommitCreate(ctx, token, caller)
	if err != nil {
		return application.CalendarCreateAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) UpdateCalendar(
	ctx context.Context,
	input application.CalendarUpdateInput,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarUpdateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.CalendarUpdateAccess{}, err
	}
	access, err := services.calendar.Update(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarUpdate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarUpdateAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.calendar == nil {
		return application.CalendarUpdateAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.calendar.CommitUpdate(ctx, token, caller)
	if err != nil {
		return application.CalendarUpdateAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) CancelCalendar(
	ctx context.Context,
	input application.CalendarCancelInput,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarCancelAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account)
	if err != nil {
		return application.CalendarCancelAccess{}, err
	}
	access, err := services.calendar.Cancel(ctx, input, caller)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(access.Preview.Token, input.Account, access.Preview.ExpiresAt)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarCancel(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarCancelAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, exists := backend.accountForPreview(token)
	if !exists || account.calendar == nil {
		return application.CalendarCancelAccess{}, errors.New("invalid or expired approval token")
	}
	access, err := account.calendar.CommitCancel(ctx, token, caller)
	if err != nil {
		return application.CalendarCancelAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) accountServices(
	ctx context.Context,
	accountID domain.AccountID,
) (sessionAccount, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if account, exists := backend.accounts[accountID]; exists {
		return account, nil
	}
	if _, exists := backend.terminalAccounts[accountID]; exists {
		return sessionAccount{}, errors.New("terminal login is in progress for this account")
	}
	configured, exists := backend.configuration.Accounts[string(accountID)]
	if !exists {
		return sessionAccount{}, fmt.Errorf("account %q is not configured", accountID)
	}
	handle, credentials, err := backend.app.authenticate(ctx, backend.configuration, accountID, configured)
	if err != nil {
		return sessionAccount{}, err
	}
	services, err := backend.accountFromHandle(configured, handle, credentials)
	if err != nil {
		return sessionAccount{}, errors.Join(err, handle.Close())
	}
	backend.accounts[accountID] = services
	return services, nil
}

func (backend *sessionBackend) accountFromHandle(
	configured config.Account,
	handle browserHandle,
	credentials session.Credentials,
) (sessionAccount, error) {
	client, err := owa.NewClient(owa.Options{
		Origin:     configured.Origin,
		Mailbox:    configured.Mailbox,
		Authorizer: handle,
		UserAgent:  "owa-bridge/" + backend.app.info.Version,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	mail, err := application.NewMailService(backend.guard, client, application.MailOptions{
		MaxRecipients: backend.configuration.Policy.MaxRecipients,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	calendar, err := application.NewCalendarService(
		backend.guard,
		client,
		application.CalendarOptions{MaxAttendees: backend.configuration.Policy.MaxAttendees},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	services := sessionAccount{
		handle: handle, mail: mail, calendar: calendar, captured: credentials.CapturedAt(),
	}
	return services, nil
}

func (backend *sessionBackend) Close() error {
	backend.close.Do(func() {
		backend.mu.Lock()
		backend.closed = true
		backend.cancel()
		backend.mu.Unlock()
		backend.active.Wait()

		backend.mu.Lock()
		defer backend.mu.Unlock()
		closeErrors := make([]error, 0, len(backend.accounts)+len(backend.terminalSessions)+1)
		for _, account := range backend.accounts {
			closeErrors = append(closeErrors, account.handle.Close())
		}
		for _, interaction := range backend.terminalSessions {
			closeErrors = append(closeErrors, interaction.handle.Close())
		}
		closeErrors = append(closeErrors, backend.recorder.Close())
		backend.closeErr = errors.Join(closeErrors...)
	})
	return backend.closeErr
}
