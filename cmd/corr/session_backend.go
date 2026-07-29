package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/audit"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/dispatch"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/eventqueue"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/paths"
	caldavprovider "github.com/nkiyohara/corresync/internal/provider/caldav"
	"github.com/nkiyohara/corresync/internal/provider/googleapi"
	"github.com/nkiyohara/corresync/internal/provider/googleweb"
	"github.com/nkiyohara/corresync/internal/provider/graphapi"
	"github.com/nkiyohara/corresync/internal/provider/imapmail"
	"github.com/nkiyohara/corresync/internal/provider/jmap"
	"github.com/nkiyohara/corresync/internal/provider/outlookweb"
	"github.com/nkiyohara/corresync/internal/session"
)

type sessionCloser interface {
	Close() error
}

type oauthClientManager interface {
	Client(
		context.Context,
		config.OAuthRoute,
		oauthlocal.Provider,
	) (*http.Client, error)
}

type sessionAccount struct {
	closers      []sessionCloser
	mail         *application.MailService
	calendar     *application.CalendarService
	captured     time.Time
	capabilities domain.Capabilities
	degradations []domain.Degradation
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
	oauth         oauthClientManager
	newJMAP       func(context.Context, jmap.Options) (*jmap.Client, error)
	newIMAP       func(context.Context, imapmail.Options) (*imapmail.Client, error)
	newCalDAV     func(context.Context, caldavprovider.Options) (*caldavprovider.Client, error)
	newGoogle     func(context.Context, googleapi.Options) (*googleapi.Client, error)
	newGoogleWeb  func(context.Context, googleweb.Options) (*googleweb.Client, error)
	newGraph      func(context.Context, graphapi.Options) (*graphapi.Client, error)
	monitorStore  *eventqueue.Store
	monitor       *application.MonitorService
	monitorEngine *application.MonitorEngine

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
	monitorStarted   map[domain.AccountID]bool
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
	oauth, err := oauthlocal.New(oauthlocal.Options{
		BeforeOpen: func(provider oauthlocal.Provider) {
			_, _ = fmt.Fprintf(
				app.stderr,
				"Opening %s authorization for scopes: %s\n",
				provider.ID,
				strings.Join(provider.Scopes, ", "),
			)
		},
	})
	if err != nil {
		_ = recorder.Close()
		return nil, err
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	backend := &sessionBackend{
		app:              app,
		configuration:    configuration,
		guard:            guard,
		recorder:         recorder,
		credentials:      credentials,
		oauth:            oauth,
		newJMAP:          jmap.New,
		newIMAP:          imapmail.New,
		newCalDAV:        caldavprovider.New,
		newGoogle:        googleapi.New,
		newGoogleWeb:     googleweb.New,
		newGraph:         graphapi.New,
		monitorStore:     eventqueue.New(),
		accounts:         make(map[domain.AccountID]sessionAccount),
		previews:         make(map[string]sessionPreview),
		lifecycle:        lifecycle,
		cancel:           cancel,
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
		monitorStarted:   make(map[domain.AccountID]bool),
	}
	monitor, err := application.NewMonitorService(backend, backend.monitorStore, recorder)
	if err != nil {
		cancel()
		_ = recorder.Close()
		return nil, err
	}
	runner, err := dispatch.NewRunner(backend.runnerConfiguration)
	if err != nil {
		cancel()
		_ = recorder.Close()
		return nil, err
	}
	engine, err := application.NewMonitorEngine(
		backend.monitorStore,
		recorder,
		dispatch.NewDesktopNotifier(),
		runner,
	)
	if err != nil {
		cancel()
		_ = recorder.Close()
		return nil, err
	}
	backend.monitor = monitor
	backend.monitorEngine = engine
	return backend, nil
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

func (backend *sessionBackend) MonitorPolicy(
	ctx context.Context,
	accountID domain.AccountID,
) (application.MonitorPolicy, error) {
	if err := ctx.Err(); err != nil {
		return application.MonitorPolicy{}, err
	}
	alias, account, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return application.MonitorPolicy{}, fmt.Errorf("account %q is not configured", accountID)
	}
	policy := application.MonitorPolicy{
		Account: accountID, Alias: alias, Address: account.Address,
		Mode: domain.MonitorOff,
	}
	if policy.Address == "" {
		if web, ok := account.OutlookWeb(); ok {
			policy.Address = web.Mailbox
		}
	}
	if account.Monitor == nil {
		return policy, nil
	}
	configured := account.Monitor
	policy.Mode = configured.Mode
	policy.PollInterval = time.Duration(configured.PollInterval)
	policy.Debounce = time.Duration(configured.Debounce)
	policy.Retention = time.Duration(configured.Retention)
	policy.RateLimitHour = configured.RateLimitHour
	policy.SenderDomains = append([]string(nil), configured.Filter.SenderDomains...)
	policy.SubjectContains = append([]string(nil), configured.Filter.SubjectContains...)
	policy.ImportantOnly = configured.Filter.ImportantOnly
	if configured.QuietHours != nil {
		policy.QuietStart = configured.QuietHours.Start
		policy.QuietEnd = configured.QuietHours.End
		policy.QuietTimeZone = configured.QuietHours.TimeZone
	}
	if configured.Notification != nil {
		policy.NotificationTarget = configured.Notification.Adapter
		policy.NotificationFields = append(
			[]string(nil),
			configured.Notification.Fields...,
		)
	}
	if configured.Runner != nil {
		policy.RunnerTarget = configured.Runner.Command
		policy.RunnerEgress = configured.Runner.Egress
		policy.RunnerFields = append([]string(nil), configured.Runner.Fields...)
	}
	return policy, nil
}

func (backend *sessionBackend) runnerConfiguration(
	accountID domain.AccountID,
) (config.Runner, error) {
	_, account, exists := backend.configuration.AccountByID(accountID)
	if !exists || account.Monitor == nil ||
		account.Monitor.Mode != domain.MonitorAgent ||
		account.Monitor.Runner == nil {
		return config.Runner{}, errors.New("account has no enabled monitor runner")
	}
	return *account.Monitor.Runner, nil
}

func (backend *sessionBackend) ProjectionAccounts(
	ctx context.Context,
) ([]application.ProjectionAccount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return nil, errors.New("session backend is closed")
	}
	aliases := make([]string, 0, len(backend.configuration.Accounts))
	for alias := range backend.configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	accounts := make([]application.ProjectionAccount, 0, len(aliases))
	for _, alias := range aliases {
		configured := backend.configuration.Accounts[alias]
		account := application.ProjectionAccount{
			Account: configured.ID, Alias: alias,
			MailProvider:     configured.MailProvider(),
			CalendarProvider: configured.CalendarProvider(),
		}
		active, authenticated := backend.accounts[configured.ID]
		if authenticated {
			capabilities := active.capabilities
			account.Authenticated = true
			account.Capabilities = &capabilities
			account.MailDegradations = projectionDegradations(
				active.degradations,
				"mail.",
			)
			account.CalendarDegradations = projectionDegradations(
				active.degradations,
				"calendar.",
			)
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func projectionDegradations(
	values []domain.Degradation,
	prefix string,
) []domain.Degradation {
	result := make([]domain.Degradation, 0, len(values))
	for _, degradation := range values {
		if strings.HasPrefix(degradation.Feature, prefix) {
			result = append(result, degradation)
		}
	}
	return result
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
			state.Degradations = append(
				[]domain.Degradation(nil),
				account.degradations...,
			)
		} else if _, exists := backend.terminalAccounts[accountID]; exists {
			state.State = "pending"
		}
		result.Accounts = append(result.Accounts, state)
	}
	return result, nil
}

func (backend *sessionBackend) MonitorStatus(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (application.MonitorStatus, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MonitorStatus{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()
	return backend.monitor.Status(ctx, account, caller)
}

func (backend *sessionBackend) ListMonitorEvents(
	ctx context.Context,
	input application.MonitorEventListInput,
	caller domain.Caller,
) (application.MonitorEventPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MonitorEventPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()
	return backend.monitor.List(ctx, input, caller)
}

func (backend *sessionBackend) AcknowledgeMonitorEvent(
	ctx context.Context,
	input application.MonitorAcknowledgeInput,
	caller domain.Caller,
) (application.MonitorEvent, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MonitorEvent{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()
	return backend.monitor.Acknowledge(ctx, input, caller)
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
		backend.startMonitorLocked(input.Account, account)
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
	if hasGoogleWebRoute(configured) {
		return nil, errors.New(
			"terminal login cannot complete a Google Web route; use visible `corr auth login`",
		)
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

func (backend *sessionBackend) SearchAllMail(
	ctx context.Context,
	input application.MailProjectionInput,
	caller domain.Caller,
) (application.MailProjectionPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.MailProjectionPage{}, errors.New(
			"session backend is closed",
		)
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	service, err := application.NewProjectionService(backend)
	if err != nil {
		return application.MailProjectionPage{}, err
	}
	return service.SearchAllMail(ctx, input, caller)
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

func (backend *sessionBackend) ListCalendarFolders(
	ctx context.Context,
	input application.CalendarFolderListInput,
	caller domain.Caller,
) (application.CalendarFolderPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.CalendarFolderPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	services, err := backend.accountServices(ctx, input.Account, caller)
	if err != nil {
		return application.CalendarFolderPage{}, err
	}
	calendar, err := services.calendarService()
	if err != nil {
		return application.CalendarFolderPage{}, err
	}
	return calendar.ListFolders(ctx, input, caller)
}

func (backend *sessionBackend) ListAgenda(
	ctx context.Context,
	input application.AgendaProjectionInput,
	caller domain.Caller,
) (application.AgendaProjectionPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.AgendaProjectionPage{}, errors.New(
			"session backend is closed",
		)
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	service, err := application.NewProjectionService(backend)
	if err != nil {
		return application.AgendaProjectionPage{}, err
	}
	return service.ListAgenda(ctx, input, caller)
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
	if hasGoogleWebRoute(configured) {
		web, err := backend.googleWebAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(services))
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
	backend.startMonitorLocked(accountID, services)
	return services, nil
}

func (backend *sessionBackend) startMonitorLocked(
	accountID domain.AccountID,
	account sessionAccount,
) {
	if backend.monitorStarted[accountID] || account.mail == nil {
		return
	}
	policy, err := backend.MonitorPolicy(backend.lifecycle, accountID)
	if err != nil || !policy.Mode.Collects() {
		return
	}
	backend.monitorStarted[accountID] = true
	backend.active.Add(1)
	go backend.monitorLoop(policy, account.mail)
}

func (backend *sessionBackend) monitorLoop(
	policy application.MonitorPolicy,
	mail *application.MailService,
) {
	defer backend.active.Done()
	poll := func() {
		if err := backend.monitorEngine.Poll(backend.lifecycle, policy, mail); err != nil &&
			backend.lifecycle.Err() == nil {
			_, _ = fmt.Fprintf(
				backend.app.stderr,
				"monitor %s encountered a safe failure and will retry; inspect monitor status and the local audit; if the pending queue is saturated, run `corr events purge --account %s --approve`\n",
				policy.Alias,
				policy.Alias,
			)
		}
	}
	poll()
	ticker := time.NewTicker(policy.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-backend.lifecycle.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
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
				Effects:      providerCalendarEffects(domain.ProviderMicrosoftOWA),
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
	if calendar != nil {
		services.degradations = append(
			services.degradations,
			domain.Degradation{
				Feature: "calendar.original_time_zone",
				Reason:  "Outlook Web calendar views are requested in UTC; an event whose response omits source-zone fields retains the instant but reports UTC as the original zone",
				Lossy:   true,
			},
		)
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
	observed := client.ObservedCapabilities()
	capabilities, degradations := jmapCapabilityReport(observed)
	result := sessionAccount{
		closers:      []sessionCloser{client},
		mail:         mail,
		captured:     time.Now().UTC(),
		capabilities: capabilities,
		degradations: degradations,
	}
	return result, nil
}

func jmapCapabilityReport(
	observed jmap.ObservedCapabilities,
) (domain.Capabilities, []domain.Degradation) {
	capabilities := domain.Capabilities{
		Mail: true, Folders: true, AttachmentReads: true,
		AttachmentWrites: !observed.ReadOnly, IncrementalSync: true,
	}
	degradations := make([]domain.Degradation, 0, 2)
	if observed.ReadOnly {
		degradations = append(degradations, domain.Degradation{
			Feature: "mail.write",
			Reason:  "the authenticated JMAP account is read-only",
		})
	}
	if !observed.Submission {
		degradations = append(degradations, domain.Degradation{
			Feature: "mail.send",
			Reason:  "the authenticated JMAP account has no matching submission capability",
		})
	}
	return capabilities, degradations
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
	result := sessionAccount{
		closers:  []sessionCloser{client},
		mail:     mail,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: true, Folders: true, AttachmentReads: true,
			AttachmentWrites: true,
		},
	}
	observed := client.ObservedCapabilities()
	if !observed.Move {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.move",
			Reason:  "the authenticated IMAP server does not advertise MOVE",
		})
	}
	if !observed.UIDPlus {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.delete",
			Reason:  "the authenticated IMAP server does not advertise UIDPLUS; safe UID EXPUNGE is unavailable",
		})
	}
	return result, nil
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
			Effects:      providerCalendarEffects(domain.ProviderCalDAV),
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

func (backend *sessionBackend) googleAPIAccount(
	ctx context.Context,
	configured config.Account,
	route config.OAuthRoute,
	mailEnabled, calendarEnabled bool,
) (sessionAccount, error) {
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderGoogleAPI,
		mailEnabled,
		calendarEnabled,
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorizedHTTP, err := manager.Client(ctx, route, provider)
	if err != nil {
		return sessionAccount{}, err
	}
	factory := backend.newGoogle
	if factory == nil {
		factory = googleapi.New
	}
	client, err := factory(ctx, googleapi.Options{
		APIBase: route.APIBase,
		Address: configured.Address,
		Mail:    mailEnabled, Calendar: calendarEnabled,
		HTTP: authorizedHTTP,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	result := sessionAccount{
		closers:  []sessionCloser{client},
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: mailEnabled, Calendar: calendarEnabled,
			Folders: mailEnabled, Labels: mailEnabled,
			AttachmentReads: mailEnabled, AttachmentWrites: mailEnabled,
		},
	}
	if mailEnabled {
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "mail.search",
				Reason:  "Gmail search syntax differs from Outlook AQS",
				Lossy:   true,
			},
			domain.Degradation{
				Feature: "mail.state",
				Reason:  "Gmail exposes no atomic historyId precondition for label updates",
			},
			domain.Degradation{
				Feature: "mail.move",
				Reason:  "Gmail exposes no atomic historyId precondition for moves",
			},
			domain.Degradation{
				Feature: "mail.push_history",
				Reason:  "the Google API route does not register push watches or expose history cursors",
			},
			domain.Degradation{
				Feature: "mail.scheduled_send",
				Reason:  "the Gmail API does not expose scheduled sending",
			},
		)
	}
	if calendarEnabled {
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "calendar.online_meeting_create",
				Reason:  "the Google API route does not provision online meetings",
			},
		)
	}
	if mailEnabled {
		result.mail, err = application.NewMailService(
			backend.guard,
			client,
			application.MailOptions{
				MaxRecipients: backend.configuration.Policy.MaxRecipients,
				Provenance: domain.Provenance{
					AccountID: configured.ID,
					Provider:  domain.ProviderGoogleAPI,
					MailboxID: "gmail-me",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, client.Close())
		}
	}
	if calendarEnabled {
		result.calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees: backend.configuration.Policy.MaxAttendees,
				Effects:      providerCalendarEffects(domain.ProviderGoogleAPI),
				Provenance: domain.Provenance{
					AccountID:  configured.ID,
					Provider:   domain.ProviderGoogleAPI,
					CalendarID: "primary",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, client.Close())
		}
	}
	return result, nil
}

func (backend *sessionBackend) graphAPIAccount(
	ctx context.Context,
	configured config.Account,
	route config.OAuthRoute,
	mailEnabled, calendarEnabled bool,
) (sessionAccount, error) {
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderMicrosoftGraph,
		mailEnabled,
		calendarEnabled,
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorizedHTTP, err := manager.Client(ctx, route, provider)
	if err != nil {
		return sessionAccount{}, err
	}
	factory := backend.newGraph
	if factory == nil {
		factory = graphapi.New
	}
	client, err := factory(ctx, graphapi.Options{
		APIBase: route.APIBase,
		Address: configured.Address,
		Mail:    mailEnabled, Calendar: calendarEnabled,
		HTTP: authorizedHTTP,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	result := sessionAccount{
		closers:  []sessionCloser{client},
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: mailEnabled, Calendar: calendarEnabled,
			Folders:          mailEnabled,
			AttachmentReads:  mailEnabled,
			AttachmentWrites: mailEnabled,
		},
	}
	if mailEnabled {
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "mail.search",
				Reason:  "Microsoft Graph search syntax differs from Outlook AQS",
				Lossy:   true,
			},
			domain.Degradation{
				Feature: "mail.reply_forward",
				Reason:  "Graph response-draft actions expose no atomic source ETag precondition; Corresync revalidates immediately before creating the draft",
			},
			domain.Degradation{
				Feature: "mail.move",
				Reason:  "Graph message move exposes no atomic source ETag precondition; Corresync revalidates immediately before the action",
			},
			domain.Degradation{
				Feature: "mail.delete",
				Reason:  "Graph permanentDelete exposes no atomic ETag precondition",
			},
			domain.Degradation{
				Feature: "mail.send_identity",
				Reason:  "Graph accepts sends asynchronously without returning a sent item identity",
				Lossy:   true,
			},
		)
	}
	if calendarEnabled {
		result.capabilities.OnlineMeeting = "teams"
	}
	if mailEnabled {
		result.mail, err = application.NewMailService(
			backend.guard,
			client,
			application.MailOptions{
				MaxRecipients: backend.configuration.Policy.MaxRecipients,
				Provenance: domain.Provenance{
					AccountID: configured.ID,
					Provider:  domain.ProviderMicrosoftGraph,
					MailboxID: "graph-me",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, client.Close())
		}
	}
	if calendarEnabled {
		result.calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees: backend.configuration.Policy.MaxAttendees,
				Effects: providerCalendarEffects(
					domain.ProviderMicrosoftGraph,
				),
				Provenance: domain.Provenance{
					AccountID:  configured.ID,
					Provider:   domain.ProviderMicrosoftGraph,
					CalendarID: "primary",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, client.Close())
		}
	}
	return result, nil
}

func (backend *sessionBackend) googleWebAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	mailEnabled := configured.Mail != nil &&
		configured.Mail.Provider == domain.ProviderGoogleWeb
	calendarEnabled := configured.Calendar != nil &&
		configured.Calendar.Provider == domain.ProviderGoogleWeb
	if !mailEnabled && !calendarEnabled {
		return sessionAccount{}, errors.New("account has no Google Web route")
	}
	mailOrigin := ""
	if mailEnabled {
		if configured.Mail.GoogleWeb == nil {
			return sessionAccount{}, errors.New("google Web mail route settings are missing")
		}
		mailOrigin = configured.Mail.GoogleWeb.Origin
	}
	calendarOrigin := ""
	if calendarEnabled {
		if configured.Calendar.GoogleWeb == nil {
			return sessionAccount{}, errors.New(
				"google Web calendar route settings are missing",
			)
		}
		calendarOrigin = configured.Calendar.GoogleWeb.Origin
	}
	primaryOrigin := mailOrigin
	startURL := strings.TrimSuffix(mailOrigin, "/") + "/mail/u/0/#inbox"
	additionalOrigins := make([]string, 0, 1)
	if !mailEnabled {
		primaryOrigin = calendarOrigin
		startURL = strings.TrimSuffix(calendarOrigin, "/") + "/calendar/u/0/r/agenda"
	} else if calendarEnabled && calendarOrigin != mailOrigin {
		additionalOrigins = append(additionalOrigins, calendarOrigin)
	}
	profileDirectory, err := paths.ProviderProfileDir(
		configured.ID,
		domain.ProviderGoogleWeb,
	)
	if err != nil {
		return sessionAccount{}, err
	}
	_, _ = fmt.Fprintln(
		backend.app.stderr,
		"Opening an isolated Google browser profile; complete sign-in for each configured service.",
	)
	handle, err := backend.app.launch(context.WithoutCancel(ctx), browser.Options{
		Origin:            primaryOrigin,
		AdditionalOrigins: additionalOrigins,
		StartURL:          startURL,
		ProfileDir:        profileDirectory,
		Executable:        backend.configuration.Browser.Executable,
		BrowserOwnedOnly:  true,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	driver, supported := handle.(googleWebBrowserHandle)
	if !supported {
		return sessionAccount{}, errors.Join(
			errors.New("configured browser launcher does not support Google Web"),
			handle.Close(),
		)
	}
	loginContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(backend.configuration.Browser.LoginTimeout),
	)
	defer cancel()
	factory := backend.newGoogleWeb
	if factory == nil {
		factory = googleweb.New
	}
	client, err := factory(loginContext, googleweb.Options{
		ExpectedAddress: configured.Address,
		MailOrigin:      mailOrigin, CalendarOrigin: calendarOrigin,
		Mail: mailEnabled, Calendar: calendarEnabled, Driver: driver,
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, handle.Close())
	}
	result := sessionAccount{
		closers:  []sessionCloser{handle},
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: mailEnabled, Calendar: calendarEnabled, Folders: mailEnabled,
		},
	}
	if mailEnabled {
		result.mail, err = application.NewMailService(
			backend.guard,
			client,
			application.MailOptions{
				MaxRecipients: backend.configuration.Policy.MaxRecipients,
				Provenance: domain.Provenance{
					AccountID: configured.ID,
					Provider:  domain.ProviderGoogleWeb,
					MailboxID: "browser-owned-gmail",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, handle.Close())
		}
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "mail.pagination",
				Reason:  "the Gmail browser route returns a bounded visible DOM snapshot and never claims it is the final remote page",
				Lossy:   true,
			},
			domain.Degradation{
				Feature: "mail.attachments",
				Reason:  "the closed Google Web adapter does not retrieve attachments",
			},
			domain.Degradation{
				Feature: "mail.writes",
				Reason:  "browser-owned Google sessions are read-only; writes require an explicitly consented Google API route",
			},
		)
	}
	if calendarEnabled {
		result.calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees: backend.configuration.Policy.MaxAttendees,
				Effects:      providerCalendarEffects(domain.ProviderGoogleWeb),
				Provenance: domain.Provenance{
					AccountID:  configured.ID,
					Provider:   domain.ProviderGoogleWeb,
					CalendarID: "browser-owned-primary",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, handle.Close())
		}
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "calendar.discovery",
				Reason:  "the Google Web route exposes only the primary visible calendar",
				Lossy:   true,
			},
			domain.Degradation{
				Feature: "calendar.writes",
				Reason:  "browser-owned Google sessions are read-only; writes require an explicitly consented Google API route",
			},
			domain.Degradation{
				Feature: "calendar.online_meeting_create",
				Reason:  "the closed Google Web adapter does not provision online meetings",
			},
		)
	}
	return result, nil
}

func (backend *sessionBackend) nonOutlookAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	var combined sessionAccount
	sharedGoogle := configured.Mail != nil &&
		configured.Mail.Provider == domain.ProviderGoogleAPI &&
		configured.Mail.GoogleAPI != nil &&
		configured.Calendar != nil &&
		configured.Calendar.Provider == domain.ProviderGoogleAPI &&
		configured.Calendar.GoogleAPI != nil &&
		oauthRoutesEqual(
			*configured.Mail.GoogleAPI,
			*configured.Calendar.GoogleAPI,
		)
	if sharedGoogle {
		google, err := backend.googleAPIAccount(
			ctx,
			configured,
			*configured.Mail.GoogleAPI,
			true,
			true,
		)
		if err != nil {
			return sessionAccount{}, err
		}
		combined = mergeSessionAccounts(combined, google)
	}
	sharedGraph := configured.Mail != nil &&
		configured.Mail.Provider == domain.ProviderMicrosoftGraph &&
		configured.Mail.MicrosoftGraph != nil &&
		configured.Calendar != nil &&
		configured.Calendar.Provider == domain.ProviderMicrosoftGraph &&
		configured.Calendar.MicrosoftGraph != nil &&
		oauthRoutesEqual(
			*configured.Mail.MicrosoftGraph,
			*configured.Calendar.MicrosoftGraph,
		)
	if sharedGraph {
		graph, err := backend.graphAPIAccount(
			ctx,
			configured,
			*configured.Mail.MicrosoftGraph,
			true,
			true,
		)
		if err != nil {
			return sessionAccount{}, errors.Join(
				err,
				closeSessionAccount(combined),
			)
		}
		combined = mergeSessionAccounts(combined, graph)
	}
	if configured.Mail != nil {
		var mail sessionAccount
		var err error
		switch configured.Mail.Provider {
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb:
		case domain.ProviderJMAP:
			mail, err = backend.jmapAccount(ctx, configured)
		case domain.ProviderIMAPSMTP:
			mail, err = backend.imapAccount(ctx, configured)
		case domain.ProviderGoogleAPI:
			if configured.Mail.GoogleAPI == nil {
				err = errors.New("google API mail route settings are missing")
			} else if !sharedGoogle {
				mail, err = backend.googleAPIAccount(
					ctx, configured, *configured.Mail.GoogleAPI, true, false,
				)
			}
		case domain.ProviderMicrosoftGraph:
			if configured.Mail.MicrosoftGraph == nil {
				err = errors.New("microsoft Graph mail route settings are missing")
			} else if !sharedGraph {
				mail, err = backend.graphAPIAccount(
					ctx,
					configured,
					*configured.Mail.MicrosoftGraph,
					true,
					false,
				)
			}
		case domain.ProviderCalDAV,
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
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb:
		case domain.ProviderCalDAV:
			calendar, err = backend.calDAVAccount(ctx, configured)
		case domain.ProviderGoogleAPI:
			if configured.Calendar.GoogleAPI == nil {
				err = errors.New("google API calendar route settings are missing")
			} else if !sharedGoogle {
				calendar, err = backend.googleAPIAccount(
					ctx, configured, *configured.Calendar.GoogleAPI, false, true,
				)
			}
		case domain.ProviderMicrosoftGraph:
			if configured.Calendar.MicrosoftGraph == nil {
				err = errors.New("microsoft Graph calendar route settings are missing")
			} else if !sharedGraph {
				calendar, err = backend.graphAPIAccount(
					ctx,
					configured,
					*configured.Calendar.MicrosoftGraph,
					false,
					true,
				)
			}
		case domain.ProviderJMAP,
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

func oauthRoutesEqual(left, right config.OAuthRoute) bool {
	return left.APIBase == right.APIBase &&
		left.ClientID == right.ClientID &&
		left.RedirectURI == right.RedirectURI &&
		left.Authorization == right.Authorization
}

func hasOutlookRoute(account config.Account) bool {
	return account.Mail != nil &&
		account.Mail.Provider == domain.ProviderMicrosoftOWA ||
		account.Calendar != nil &&
			account.Calendar.Provider == domain.ProviderMicrosoftOWA
}

func hasGoogleWebRoute(account config.Account) bool {
	return account.Mail != nil &&
		account.Mail.Provider == domain.ProviderGoogleWeb ||
		account.Calendar != nil &&
			account.Calendar.Provider == domain.ProviderGoogleWeb
}

func hasBrowserRoute(account config.Account) bool {
	return hasOutlookRoute(account) || hasGoogleWebRoute(account)
}

func providerCalendarEffects(provider domain.ProviderID) application.CalendarEffects {
	switch provider {
	case domain.ProviderMicrosoftOWA:
		return application.CalendarEffects{
			CreateAttendeeNotifications: true,
			UpdateAttendeeNotifications: true,
			CancelAttendeeNotifications: true,
			CancellationMode:            application.CalendarCancellationModeAll,
			CancellationDisposition:     application.CalendarDispositionDeletedItems,
		}
	case domain.ProviderGoogleAPI, domain.ProviderMicrosoftGraph:
		return application.CalendarEffects{
			CreateAttendeeNotifications: true,
			UpdateAttendeeNotifications: true,
			CancelAttendeeNotifications: true,
			CancellationMode:            application.CalendarCancellationProviderManaged,
			CancellationDisposition:     application.CalendarDispositionRemoteDelete,
		}
	case domain.ProviderCalDAV, domain.ProviderGoogleWeb:
		return application.CalendarEffects{
			CancellationMode:        application.CalendarCancellationNoScheduling,
			CancellationDisposition: application.CalendarDispositionCalendarObject,
		}
	case domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3:
		return application.CalendarEffects{}
	default:
		return application.CalendarEffects{}
	}
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
		degradations: append(
			append(
				[]domain.Degradation(nil),
				left.degradations...,
			),
			right.degradations...,
		),
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
