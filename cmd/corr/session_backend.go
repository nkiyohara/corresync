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

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/audit"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
	caldavprovider "github.com/nkiyohara/corresync/internal/provider/caldav"
	"github.com/nkiyohara/corresync/internal/provider/imapmail"
	"github.com/nkiyohara/corresync/internal/provider/jmap"
	"github.com/nkiyohara/corresync/internal/provider/outlookweb"
	"github.com/nkiyohara/corresync/internal/session"
)

type sessionCloser interface {
	Close() error
}

type sessionAccount struct {
	closers      []sessionCloser
	mail         *application.MailService
	calendar     *application.CalendarService
	captured     time.Time
	capabilities domain.Capabilities
}

func (account sessionAccount) mailService() (*application.MailService, error) {
	if account.mail == nil {
		return nil, errors.New("configured account has no mail route")
	}
	return account.mail, nil
}

func (account sessionAccount) calendarService() (*application.CalendarService, error) {
	if account.calendar == nil {
		return nil, errors.New("configured account has no calendar route")
	}
	return account.calendar, nil
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
	credentials   *credential.Resolver
	newJMAP       func(context.Context, jmap.Options) (*jmap.Client, error)
	newIMAP       func(context.Context, imapmail.Options) (*imapmail.Client, error)
	newCalDAV     func(context.Context, caldavprovider.Options) (*caldavprovider.Client, error)

	mu           sync.Mutex
	activationMu sync.Mutex
	accounts     map[domain.AccountID]sessionAccount
	previews     map[string]sessionPreview
	closed       bool
	active       sync.WaitGroup
	lifecycle    context.Context
	cancel       context.CancelFunc
	close        sync.Once
	closeErr     error

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
	credentials, err := credential.New(credential.Options{
		Helper: configuration.Credentials.Helper,
	})
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
		credentials:      credentials,
		newJMAP:          jmap.New,
		newIMAP:          imapmail.New,
		newCalDAV:        caldavprovider.New,
		accounts:         make(map[domain.AccountID]sessionAccount),
		previews:         make(map[string]sessionPreview),
		lifecycle:        lifecycle,
		cancel:           cancel,
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
	}, nil
}

func (backend *sessionBackend) DefaultAccount() domain.AccountID {
	return backend.configuration.Accounts[backend.configuration.DefaultAccount].ID
}

func (backend *sessionBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	_, account, err := backend.configuration.ResolveAccount(reference)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}

func (backend *sessionBackend) Login(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
) (daemonapi.LoginResult, error) {
	if err := caller.Validate(); err != nil {
		return daemonapi.LoginResult{}, err
	}
	if caller.Surface != "cli" {
		return daemonapi.LoginResult{}, errors.New(
			"authentication can only be started by an explicit local CLI command",
		)
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return daemonapi.LoginResult{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, err := backend.activateAccount(ctx, accountID)
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
		configured := backend.configuration.Accounts[alias]
		accountID := configured.ID
		state := daemonapi.SessionStatus{
			Account: accountID, Alias: alias, Provider: configured.PrimaryProvider(),
			State: "signed_out",
		}
		if account, exists := backend.accounts[accountID]; exists {
			capturedAt := account.captured.UTC()
			state.State = "authenticated"
			state.Authenticated = true
			state.CapturedAt = &capturedAt
			capabilities := account.capabilities
			state.Capabilities = &capabilities
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
		_, configured, exists := backend.configuration.AccountByID(input.Account)
		if !exists {
			return daemonapi.TerminalLoginResult{}, fmt.Errorf(
				"account %q is not configured",
				input.Account,
			)
		}
		account, err := backend.outlookAccount(configured, interaction.handle, credentials)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, errors.Join(
				err, backend.dropTerminalInteraction(interaction, true),
			)
		}
		standards, err := backend.nonOutlookAccount(ctx, configured)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, errors.Join(
				err,
				closeSessionAccount(account),
				backend.dropTerminalInteraction(interaction, false),
			)
		}
		account = mergeSessionAccounts(account, standards)
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
	_, configured, exists := backend.configuration.AccountByID(input.Account)
	if !exists {
		return nil, fmt.Errorf("account %q is not configured", input.Account)
	}
	profileDirectory, err := paths.ProfileDir(input.Account)
	if err != nil {
		return nil, err
	}
	web, ok := configured.OutlookWeb()
	if !ok {
		return nil, errors.New("terminal login is available only for Outlook Web routes")
	}
	handle, err := backend.app.launch(backend.lifecycle, browser.Options{
		Origin: web.Origin, ProfileDir: profileDirectory,
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailPage{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailPage{}, err
	}
	return mail.List(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailPage{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailPage{}, err
	}
	return mail.Search(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailFolderPage{}, err
	}
	return mail.ListFolders(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailBodyAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailBodyAccess{}, err
	}
	access, err := mail.GetBody(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailAttachmentAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailAttachmentAccess{}, err
	}
	access, err := mail.GetAttachment(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailDraftAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailDraftAccess{}, err
	}
	access, err := mail.CreateDraft(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailSendAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailSendAccess{}, err
	}
	access, err := mail.Send(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailMoveAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailMoveAccess{}, err
	}
	access, err := mail.Move(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailReadStateAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailReadStateAccess{}, err
	}
	access, err := mail.SetReadState(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.MailDeleteAccess{}, err
	}
	mail, err := services.mailService()
	if err != nil {
		return application.MailDeleteAccess{}, err
	}
	access, err := mail.Delete(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.CalendarPage{}, err
	}
	calendar, err := services.calendarService()
	if err != nil {
		return application.CalendarPage{}, err
	}
	return calendar.List(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.CalendarCreateAccess{}, err
	}
	calendar, err := services.calendarService()
	if err != nil {
		return application.CalendarCreateAccess{}, err
	}
	access, err := calendar.Create(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.CalendarUpdateAccess{}, err
	}
	calendar, err := services.calendarService()
	if err != nil {
		return application.CalendarUpdateAccess{}, err
	}
	access, err := calendar.Update(ctx, input, caller)
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

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.CalendarCancelAccess{}, err
	}
	calendar, err := services.calendarService()
	if err != nil {
		return application.CalendarCancelAccess{}, err
	}
	access, err := calendar.Cancel(ctx, input, caller)
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
	_ context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
) (sessionAccount, error) {
	if err := caller.Validate(); err != nil {
		return sessionAccount{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if account, exists := backend.accounts[accountID]; exists {
		return account, nil
	}
	if _, exists := backend.terminalAccounts[accountID]; exists {
		return sessionAccount{}, errors.New("terminal login is in progress for this account")
	}
	if _, _, exists := backend.configuration.AccountByID(accountID); !exists {
		return sessionAccount{}, fmt.Errorf("account %q is not configured", accountID)
	}
	return sessionAccount{}, errors.New(
		"account is not authenticated; run `corr auth login --account <alias>` interactively",
	)
}

func (backend *sessionBackend) activateAccount(
	ctx context.Context,
	accountID domain.AccountID,
) (sessionAccount, error) {
	backend.activationMu.Lock()
	defer backend.activationMu.Unlock()

	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return sessionAccount{}, errors.New("session backend is closed")
	}
	if account, exists := backend.accounts[accountID]; exists {
		backend.mu.Unlock()
		return account, nil
	}
	if _, exists := backend.terminalAccounts[accountID]; exists {
		backend.mu.Unlock()
		return sessionAccount{}, errors.New("terminal login is in progress for this account")
	}
	_, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		backend.mu.Unlock()
		return sessionAccount{}, fmt.Errorf("account %q is not configured", accountID)
	}
	backend.mu.Unlock()

	var services sessionAccount
	if hasOutlookRoute(configured) {
		var handle browserHandle
		var captured session.Credentials
		handle, captured, err := backend.app.authenticate(
			ctx,
			backend.configuration,
			accountID,
			configured,
		)
		if err != nil {
			return sessionAccount{}, err
		}
		web, err := backend.outlookAccount(configured, handle, captured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, handle.Close())
		}
		services = mergeSessionAccounts(services, web)
	}
	standards, err := backend.nonOutlookAccount(ctx, configured)
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(services))
	}
	services = mergeSessionAccounts(services, standards)

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return sessionAccount{}, errors.Join(
			errors.New("session backend closed during authentication"),
			closeSessionAccount(services),
		)
	}
	backend.accounts[accountID] = services
	return services, nil
}

func (backend *sessionBackend) outlookAccount(
	configured config.Account,
	handle browserHandle,
	credentials session.Credentials,
) (sessionAccount, error) {
	web, ok := configured.OutlookWeb()
	if !ok {
		return sessionAccount{}, errors.New("account is not an Outlook Web route")
	}
	client, err := outlookweb.NewClient(outlookweb.Options{
		Origin:     web.Origin,
		Mailbox:    web.Mailbox,
		Authorizer: handle,
		UserAgent:  "corresync/" + backend.app.info.Version,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	var mail *application.MailService
	if configured.Mail != nil &&
		configured.Mail.Provider == domain.ProviderMicrosoftOWA {
		var err error
		mail, err = application.NewMailService(backend.guard, client, application.MailOptions{
			MaxRecipients: backend.configuration.Policy.MaxRecipients,
			Provenance: domain.Provenance{
				AccountID: configured.ID,
				Provider:  configured.MailProvider(),
				MailboxID: "configured-mailbox",
			},
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	var calendar *application.CalendarService
	if configured.Calendar != nil &&
		configured.Calendar.Provider == domain.ProviderMicrosoftOWA {
		var err error
		calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees: backend.configuration.Policy.MaxAttendees,
				Provenance: domain.Provenance{
					AccountID:  configured.ID,
					Provider:   configured.CalendarProvider(),
					CalendarID: "calendar",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, err
		}
	}
	services := sessionAccount{
		closers: []sessionCloser{handle}, mail: mail, calendar: calendar,
		captured:     credentials.CapturedAt(),
		capabilities: outlookWebCapabilities(configured),
	}
	return services, nil
}

func (backend *sessionBackend) jmapAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	route := configured.Mail.JMAP
	if route == nil {
		return sessionAccount{}, errors.New("JMAP route settings are missing")
	}
	secret, err := backend.credentials.Resolve(ctx, route.Credential)
	if err != nil {
		return sessionAccount{}, err
	}
	defer func() { _ = secret.Close() }()
	password := []byte(secret.String())
	defer func() {
		for index := range password {
			password[index] = 0
		}
	}()
	factory := backend.newJMAP
	if factory == nil {
		factory = jmap.New
	}
	client, err := factory(ctx, jmap.Options{
		SessionURL: route.SessionURL,
		Username:   route.Username,
		Password:   password,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	mail, err := application.NewMailService(backend.guard, client, application.MailOptions{
		MaxRecipients: backend.configuration.Policy.MaxRecipients,
		Provenance: domain.Provenance{
			AccountID: configured.ID,
			Provider:  domain.ProviderJMAP,
			MailboxID: "primary-mail-account",
		},
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return sessionAccount{
		closers:  []sessionCloser{client},
		mail:     mail,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: true, Folders: true, AttachmentReads: true,
			AttachmentWrites: true, IncrementalSync: true,
		},
	}, nil
}

func (backend *sessionBackend) imapAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	route := configured.Mail.IMAPSMTP
	if route == nil {
		return sessionAccount{}, errors.New("IMAP/SMTP route settings are missing")
	}
	secret, err := backend.credentials.Resolve(ctx, route.Credential)
	if err != nil {
		return sessionAccount{}, err
	}
	defer func() { _ = secret.Close() }()
	password := []byte(secret.String())
	defer func() {
		for index := range password {
			password[index] = 0
		}
	}()
	factory := backend.newIMAP
	if factory == nil {
		factory = imapmail.New
	}
	sender := route.Mailbox
	if sender == "" {
		sender = configured.Address
	}
	client, err := factory(ctx, imapmail.Options{
		IMAP: imapmail.Endpoint{
			Host: route.IMAP.Host, Port: route.IMAP.Port, Mode: string(route.IMAP.Mode),
		},
		SMTP: imapmail.Endpoint{
			Host: route.SMTP.Host, Port: route.SMTP.Port, Mode: string(route.SMTP.Mode),
		},
		Username: route.Username,
		Sender:   sender,
		Password: password,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	mail, err := application.NewMailService(backend.guard, client, application.MailOptions{
		MaxRecipients: backend.configuration.Policy.MaxRecipients,
		Provenance: domain.Provenance{
			AccountID: configured.ID, Provider: domain.ProviderIMAPSMTP,
			MailboxID: "configured-imap-account",
		},
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return sessionAccount{
		closers:  []sessionCloser{client},
		mail:     mail,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: true, Folders: true, AttachmentReads: true,
			AttachmentWrites: true,
		},
	}, nil
}

func (backend *sessionBackend) calDAVAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	route := configured.Calendar.CalDAV
	if route == nil {
		return sessionAccount{}, errors.New("CalDAV route settings are missing")
	}
	secret, err := backend.credentials.Resolve(ctx, route.Credential)
	if err != nil {
		return sessionAccount{}, err
	}
	defer func() { _ = secret.Close() }()
	password := []byte(secret.String())
	defer func() {
		for index := range password {
			password[index] = 0
		}
	}()
	factory := backend.newCalDAV
	if factory == nil {
		factory = caldavprovider.New
	}
	client, err := factory(ctx, caldavprovider.Options{
		Endpoint: route.Endpoint, CalendarPath: route.CalendarPath,
		Username: route.Username, Password: password,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	calendar, err := application.NewCalendarService(
		backend.guard,
		client,
		application.CalendarOptions{
			MaxAttendees: backend.configuration.Policy.MaxAttendees,
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderCalDAV,
				CalendarID: "configured-caldav-calendar",
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return sessionAccount{
		closers:  []sessionCloser{client},
		calendar: calendar,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Calendar: true,
		},
	}, nil
}

func (backend *sessionBackend) nonOutlookAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	var combined sessionAccount
	if configured.Mail != nil {
		var mail sessionAccount
		var err error
		switch configured.Mail.Provider {
		case domain.ProviderMicrosoftOWA:
		case domain.ProviderJMAP:
			mail, err = backend.jmapAccount(ctx, configured)
		case domain.ProviderIMAPSMTP:
			mail, err = backend.imapAccount(ctx, configured)
		case domain.ProviderMicrosoftGraph,
			domain.ProviderGoogleAPI,
			domain.ProviderGoogleWeb,
			domain.ProviderCalDAV,
			domain.ProviderPOP3:
			err = fmt.Errorf(
				"configured mail provider %q is not available in this build",
				configured.Mail.Provider,
			)
		default:
			err = fmt.Errorf("unknown configured mail provider %q", configured.Mail.Provider)
		}
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, mail)
	}
	if configured.Calendar != nil {
		var calendar sessionAccount
		var err error
		switch configured.Calendar.Provider {
		case domain.ProviderMicrosoftOWA:
		case domain.ProviderCalDAV:
			calendar, err = backend.calDAVAccount(ctx, configured)
		case domain.ProviderMicrosoftGraph,
			domain.ProviderGoogleAPI,
			domain.ProviderGoogleWeb,
			domain.ProviderJMAP,
			domain.ProviderIMAPSMTP,
			domain.ProviderPOP3:
			err = fmt.Errorf(
				"configured calendar provider %q is not available in this build",
				configured.Calendar.Provider,
			)
		default:
			err = fmt.Errorf(
				"unknown configured calendar provider %q",
				configured.Calendar.Provider,
			)
		}
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, calendar)
	}
	return combined, nil
}

func hasOutlookRoute(account config.Account) bool {
	return account.Mail != nil &&
		account.Mail.Provider == domain.ProviderMicrosoftOWA ||
		account.Calendar != nil &&
			account.Calendar.Provider == domain.ProviderMicrosoftOWA
}

func mergeSessionAccounts(
	left sessionAccount,
	right sessionAccount,
) sessionAccount {
	merged := sessionAccount{
		closers:  append(append([]sessionCloser(nil), left.closers...), right.closers...),
		mail:     left.mail,
		calendar: left.calendar,
		captured: left.captured,
		capabilities: domain.Capabilities{
			Mail:             left.capabilities.Mail || right.capabilities.Mail,
			Calendar:         left.capabilities.Calendar || right.capabilities.Calendar,
			Folders:          left.capabilities.Folders || right.capabilities.Folders,
			Labels:           left.capabilities.Labels || right.capabilities.Labels,
			Push:             left.capabilities.Push || right.capabilities.Push,
			FreeBusy:         left.capabilities.FreeBusy || right.capabilities.FreeBusy,
			IncrementalSync:  left.capabilities.IncrementalSync || right.capabilities.IncrementalSync,
			ScheduledSend:    left.capabilities.ScheduledSend || right.capabilities.ScheduledSend,
			SharedMailboxes:  left.capabilities.SharedMailboxes || right.capabilities.SharedMailboxes,
			SharedCalendars:  left.capabilities.SharedCalendars || right.capabilities.SharedCalendars,
			AttachmentReads:  left.capabilities.AttachmentReads || right.capabilities.AttachmentReads,
			AttachmentWrites: left.capabilities.AttachmentWrites || right.capabilities.AttachmentWrites,
		},
	}
	if right.mail != nil {
		merged.mail = right.mail
	}
	if right.calendar != nil {
		merged.calendar = right.calendar
	}
	if right.captured.After(merged.captured) {
		merged.captured = right.captured
	}
	merged.capabilities.OnlineMeeting = left.capabilities.OnlineMeeting
	if right.capabilities.OnlineMeeting != "" {
		merged.capabilities.OnlineMeeting = right.capabilities.OnlineMeeting
	}
	return merged
}

func outlookWebCapabilities(account config.Account) domain.Capabilities {
	capabilities := domain.Capabilities{
		Mail: account.Mail != nil &&
			account.Mail.Provider == domain.ProviderMicrosoftOWA,
		Calendar: account.Calendar != nil &&
			account.Calendar.Provider == domain.ProviderMicrosoftOWA,
	}
	capabilities.Folders = capabilities.Mail
	capabilities.SharedMailboxes = capabilities.Mail
	capabilities.AttachmentReads = capabilities.Mail
	capabilities.AttachmentWrites = capabilities.Mail
	if capabilities.Calendar {
		capabilities.OnlineMeeting = "teams"
	}
	return capabilities
}

func closeSessionAccount(account sessionAccount) error {
	closeErrors := make([]error, 0, len(account.closers))
	for _, closer := range account.closers {
		if closer != nil {
			closeErrors = append(closeErrors, closer.Close())
		}
	}
	return errors.Join(closeErrors...)
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
			closeErrors = append(closeErrors, closeSessionAccount(account))
		}
		for _, interaction := range backend.terminalSessions {
			closeErrors = append(closeErrors, interaction.handle.Close())
		}
		closeErrors = append(closeErrors, backend.recorder.Close())
		backend.closeErr = errors.Join(closeErrors...)
	})
	return backend.closeErr
}
