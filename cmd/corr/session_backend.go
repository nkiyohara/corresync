package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/paths"
	caldavprovider "github.com/nkiyohara/corresync/internal/provider/caldav"
	"github.com/nkiyohara/corresync/internal/provider/googleapi"
	"github.com/nkiyohara/corresync/internal/provider/googletasks"
	"github.com/nkiyohara/corresync/internal/provider/graphapi"
	"github.com/nkiyohara/corresync/internal/provider/imapmail"
	"github.com/nkiyohara/corresync/internal/provider/jmap"
	"github.com/nkiyohara/corresync/internal/provider/mattermostapi"
	"github.com/nkiyohara/corresync/internal/provider/outlookweb"
	"github.com/nkiyohara/corresync/internal/provider/slackapi"
	"github.com/nkiyohara/corresync/internal/provider/teamsgraph"
	"github.com/nkiyohara/corresync/internal/provider/teamsweb"
	"github.com/nkiyohara/corresync/internal/provider/ticktick"
	"github.com/nkiyohara/corresync/internal/provider/todoist"
	"github.com/nkiyohara/corresync/internal/rollout"
	"github.com/nkiyohara/corresync/internal/session"
)

type sessionCloser interface {
	Close() error
}

type bearerTransport struct {
	base       *http.Transport
	authorizer *credential.BearerAuthorizer
}

func (transport *bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil || transport.authorizer == nil {
		return nil, errors.New("bearer transport is unavailable")
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	if err := transport.authorizer.Apply(copy); err != nil {
		return nil, err
	}
	return transport.base.RoundTrip(copy)
}

func (transport *bearerTransport) CloseIdleConnections() {
	if transport != nil && transport.base != nil {
		transport.base.CloseIdleConnections()
	}
}

type oauthClientManager interface {
	Authorize(
		context.Context,
		config.OAuthClient,
		oauthlocal.Provider,
	) (oauthlocal.Authorization, error)
	AuthorizeConfidential(
		context.Context,
		config.OAuthClient,
		oauthlocal.Provider,
		oauthlocal.ClientCredentialResolver,
	) (oauthlocal.Authorization, error)
}

type sessionAccount struct {
	closers            []sessionCloser
	mail               *application.MailService
	calendar           *application.CalendarService
	tasks              *application.TaskService
	messages           *application.MessagingService
	mailLease          *sessionLease
	calendarLease      *sessionLease
	taskLease          *sessionLease
	messageLease       *sessionLease
	captured           time.Time
	capabilities       domain.Capabilities
	degradations       []domain.Degradation
	staticDegradations []domain.Degradation
	usage              *accountUsage
	borrowedLease      *sessionLease
	borrowedService    application.AuthenticationService
}

type sessionServiceSet uint8

const (
	sessionServiceMail sessionServiceSet = 1 << iota
	sessionServiceCalendar
	sessionServiceTasks
	sessionServiceMessages
)

type sessionLease struct {
	services     sessionServiceSet
	closers      []sessionCloser
	captured     time.Time
	capabilities domain.Capabilities
	degradations []domain.Degradation
	usage        *accountUsage
	close        sync.Once
	closeErr     error
}

func leasedSessionAccount(account sessionAccount) sessionAccount {
	services := sessionServiceSet(0)
	if account.mail != nil {
		services |= sessionServiceMail
	}
	if account.calendar != nil {
		services |= sessionServiceCalendar
	}
	if account.tasks != nil {
		services |= sessionServiceTasks
	}
	if account.messages != nil {
		services |= sessionServiceMessages
	}
	if services == 0 {
		return account
	}
	lease := &sessionLease{
		services:     services,
		closers:      append([]sessionCloser(nil), account.closers...),
		captured:     account.captured,
		capabilities: account.capabilities,
		degradations: append([]domain.Degradation(nil), account.degradations...),
		usage:        newAccountUsage(),
	}
	if account.mail != nil {
		account.mailLease = lease
	}
	if account.calendar != nil {
		account.calendarLease = lease
	}
	if account.tasks != nil {
		account.taskLease = lease
	}
	if account.messages != nil {
		account.messageLease = lease
	}
	account.closers = nil
	return account
}

func (lease *sessionLease) closeAfterDrain() error {
	if lease == nil {
		return nil
	}
	lease.close.Do(func() {
		closeErrors := make([]error, 0, len(lease.closers))
		for _, closer := range lease.closers {
			if closer != nil {
				closeErrors = append(closeErrors, closer.Close())
			}
		}
		lease.closeErr = errors.Join(closeErrors...)
	})
	return lease.closeErr
}

func (account sessionAccount) lease(
	service application.AuthenticationService,
) *sessionLease {
	switch service {
	case application.AuthenticationServiceMail:
		return account.mailLease
	case application.AuthenticationServiceCalendar:
		return account.calendarLease
	case application.AuthenticationServiceTasks:
		return account.taskLease
	case application.AuthenticationServiceMessages:
		return account.messageLease
	default:
		return nil
	}
}

func (account sessionAccount) serviceActive(
	service application.AuthenticationService,
) bool {
	switch service {
	case application.AuthenticationServiceMail:
		return account.mail != nil && account.mailLease != nil
	case application.AuthenticationServiceCalendar:
		return account.calendar != nil && account.calendarLease != nil
	case application.AuthenticationServiceTasks:
		return account.tasks != nil && account.taskLease != nil
	case application.AuthenticationServiceMessages:
		return account.messages != nil && account.messageLease != nil
	default:
		return false
	}
}

func (account sessionAccount) hasActiveService() bool {
	return account.serviceActive(application.AuthenticationServiceMail) ||
		account.serviceActive(application.AuthenticationServiceCalendar) ||
		account.serviceActive(application.AuthenticationServiceTasks) ||
		account.serviceActive(application.AuthenticationServiceMessages)
}

func (account sessionAccount) leases() []*sessionLease {
	result := make([]*sessionLease, 0, 4)
	seen := make(map[*sessionLease]struct{}, 4)
	for _, lease := range []*sessionLease{
		account.mailLease,
		account.calendarLease,
		account.taskLease,
		account.messageLease,
	} {
		if lease == nil {
			continue
		}
		if _, exists := seen[lease]; exists {
			continue
		}
		seen[lease] = struct{}{}
		result = append(result, lease)
	}
	return result
}

func (account *sessionAccount) refreshSnapshot() {
	account.captured = time.Time{}
	account.capabilities = domain.Capabilities{}
	account.degradations = append(
		[]domain.Degradation(nil),
		account.staticDegradations...,
	)
	for _, lease := range account.leases() {
		account.capabilities = mergeCapabilities(
			account.capabilities,
			lease.capabilities,
		)
		account.degradations = append(
			account.degradations,
			lease.degradations...,
		)
		if lease.captured.After(account.captured) {
			account.captured = lease.captured
		}
	}
}

func (account *sessionAccount) detachLease(lease *sessionLease) sessionServiceSet {
	if account == nil || lease == nil {
		return 0
	}
	var detached sessionServiceSet
	if account.mailLease == lease {
		account.mail = nil
		account.mailLease = nil
		detached |= sessionServiceMail
	}
	if account.calendarLease == lease {
		account.calendar = nil
		account.calendarLease = nil
		detached |= sessionServiceCalendar
	}
	if account.taskLease == lease {
		account.tasks = nil
		account.taskLease = nil
		detached |= sessionServiceTasks
	}
	if account.messageLease == lease {
		account.messages = nil
		account.messageLease = nil
		detached |= sessionServiceMessages
	}
	account.refreshSnapshot()
	return detached
}

type accountUsage struct {
	mu      sync.Mutex
	active  int
	closing bool
	done    chan struct{}
}

func newAccountUsage() *accountUsage {
	return &accountUsage{done: make(chan struct{})}
}

func (usage *accountUsage) begin() error {
	if usage == nil {
		return errors.New("account session usage is unavailable")
	}
	usage.mu.Lock()
	defer usage.mu.Unlock()
	if usage.closing {
		return errors.New("account logout is in progress")
	}
	usage.active++
	return nil
}

func (usage *accountUsage) end() {
	usage.mu.Lock()
	defer usage.mu.Unlock()
	if usage.active > 0 {
		usage.active--
	}
	if usage.closing && usage.active == 0 {
		select {
		case <-usage.done:
		default:
			close(usage.done)
		}
	}
}

func (usage *accountUsage) closeAfterActive() <-chan struct{} {
	usage.mu.Lock()
	defer usage.mu.Unlock()
	usage.closing = true
	if usage.active == 0 {
		select {
		case <-usage.done:
		default:
			close(usage.done)
		}
	}
	return usage.done
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

func (account sessionAccount) taskService() (*application.TaskService, error) {
	if account.tasks == nil {
		return nil, errors.New("configured account has no active task route")
	}
	return account.tasks, nil
}

func (account sessionAccount) messagingService() (*application.MessagingService, error) {
	if account.messages == nil {
		return nil, errors.New("configured account has no active messaging route")
	}
	return account.messages, nil
}

type sessionPreview struct {
	account   domain.AccountID
	service   application.AuthenticationService
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

const (
	terminalProgressWait         = 5 * time.Second
	terminalProgressPollInterval = 100 * time.Millisecond
	terminalProgressQuietPeriod  = 500 * time.Millisecond
)

// sessionBackend lazily opens one dedicated browser per configured account and
// keeps it for the lifetime of its owning server. Every adapter call passes
// through the same application guard and content-free audit recorder.
type sessionBackend struct {
	app            *runtime
	configuration  config.Config
	guard          *application.Guard
	recorder       *audit.FileRecorder
	credentials    *credential.Resolver
	oauth          oauthClientManager
	newJMAP        func(context.Context, jmap.Options) (*jmap.Client, error)
	newIMAP        func(context.Context, imapmail.Options) (*imapmail.Client, error)
	newCalDAV      func(context.Context, caldavprovider.Options) (*caldavprovider.Client, error)
	newCalDAVTasks func(context.Context, caldavprovider.TaskOptions) (*caldavprovider.Client, error)
	newGoogle      func(context.Context, googleapi.Options) (*googleapi.Client, error)
	newGoogleTasks func(context.Context, googletasks.Options) (*googletasks.Client, error)
	newGraph       func(context.Context, graphapi.Options) (*graphapi.Client, error)
	newTodoist     func(context.Context, todoist.Options) (*todoist.Client, error)
	newTickTick    func(context.Context, ticktick.Options) (*ticktick.Client, error)
	newSlack       func(context.Context, slackapi.Options) (*slackapi.Client, error)
	newTeamsGraph  func(context.Context, teamsgraph.Options) (*teamsgraph.Client, error)
	newTeamsWeb    func(context.Context, teamsweb.Options) (*teamsweb.Client, error)
	newMattermost  func(context.Context, mattermostapi.Options) (*mattermostapi.Client, error)
	monitorStore   *eventqueue.Store
	monitor        *application.MonitorService
	monitorEngine  *application.MonitorEngine

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
	monitorCancel    map[domain.AccountID]context.CancelFunc
	monitorDone      map[domain.AccountID]chan struct{}
	reauthentication map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason
	signedOutReason  map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason
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
		GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
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
		newCalDAVTasks:   caldavprovider.NewTasks,
		newGoogle:        googleapi.New,
		newGoogleTasks:   googletasks.New,
		newGraph:         graphapi.New,
		newTodoist:       todoist.New,
		newTickTick:      ticktick.New,
		monitorStore:     eventqueue.New(),
		accounts:         make(map[domain.AccountID]sessionAccount),
		previews:         make(map[string]sessionPreview),
		lifecycle:        lifecycle,
		cancel:           cancel,
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
		monitorStarted:   make(map[domain.AccountID]bool),
		monitorCancel:    make(map[domain.AccountID]context.CancelFunc),
		monitorDone:      make(map[domain.AccountID]chan struct{}),
		reauthentication: make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
		signedOutReason:  make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
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

func configuredAuthenticationServices(
	account config.Account,
) []application.AuthenticationService {
	services := make([]application.AuthenticationService, 0, 4)
	if account.Mail != nil {
		services = append(services, application.AuthenticationServiceMail)
	}
	if account.Calendar != nil {
		services = append(services, application.AuthenticationServiceCalendar)
	}
	if account.Tasks != nil {
		services = append(services, application.AuthenticationServiceTasks)
	}
	if account.Messages != nil {
		services = append(services, application.AuthenticationServiceMessages)
	}
	return services
}

func configuredServiceProvider(
	account config.Account,
	service application.AuthenticationService,
) domain.ProviderID {
	switch service {
	case application.AuthenticationServiceMail:
		return account.MailProvider()
	case application.AuthenticationServiceCalendar:
		return account.CalendarProvider()
	case application.AuthenticationServiceTasks:
		return account.TaskProvider()
	case application.AuthenticationServiceMessages:
		return domain.ProviderID(account.MessagingProvider())
	default:
		return ""
	}
}

func configuredServiceImplemented(
	account config.Account,
	service application.AuthenticationService,
) bool {
	provider := configuredServiceProvider(account, service)
	switch service {
	case application.AuthenticationServiceMail:
		switch provider { //nolint:exhaustive // Unsupported provider IDs deliberately return false.
		case domain.ProviderMicrosoftOWA, domain.ProviderJMAP,
			domain.ProviderIMAPSMTP, domain.ProviderGoogle,
			domain.ProviderMicrosoftGraph:
			return true
		default:
			return false
		}
	case application.AuthenticationServiceCalendar:
		switch provider { //nolint:exhaustive // Unsupported provider IDs deliberately return false.
		case domain.ProviderMicrosoftOWA, domain.ProviderCalDAV,
			domain.ProviderGoogle, domain.ProviderMicrosoftGraph:
			return true
		default:
			return false
		}
	case application.AuthenticationServiceTasks:
		switch provider { //nolint:exhaustive // Unsupported provider IDs deliberately return false.
		case domain.ProviderMicrosoftGraph, domain.ProviderTodoist,
			domain.ProviderCalDAV, domain.ProviderGoogleTasks,
			domain.ProviderTickTick:
			return true
		default:
			return false
		}
	case application.AuthenticationServiceMessages:
		if account.Messages == nil {
			return false
		}
		return rollout.RequireMessaging(
			account.Messages.Provider,
			account.Messages.Kind(),
		) == nil
	default:
		return false
	}
}

func sessionServiceMask(
	service application.AuthenticationService,
) sessionServiceSet {
	switch service {
	case application.AuthenticationServiceMail:
		return sessionServiceMail
	case application.AuthenticationServiceCalendar:
		return sessionServiceCalendar
	case application.AuthenticationServiceTasks:
		return sessionServiceTasks
	case application.AuthenticationServiceMessages:
		return sessionServiceMessages
	default:
		return 0
	}
}

func (set sessionServiceSet) includes(
	service application.AuthenticationService,
) bool {
	return set&sessionServiceMask(service) != 0
}

func (backend *sessionBackend) serviceAuthenticationStatusLocked(
	accountID domain.AccountID,
	alias string,
	configured config.Account,
	service application.AuthenticationService,
) (application.ServiceAuthenticationStatus, error) {
	provider := configuredServiceProvider(configured, service)
	if provider == "" {
		return application.ServiceAuthenticationStatus{}, errors.New(
			"configured account has no selected service route",
		)
	}
	status := application.ServiceAuthenticationStatus{
		Service:  service,
		Provider: provider,
		State:    application.AuthenticationStateSignedOut,
		Reason:   application.AuthenticationReasonNeverAuthenticated,
	}
	if account, exists := backend.accounts[accountID]; exists &&
		account.serviceActive(service) {
		status.State = application.AuthenticationStateAuthenticated
		status.Reason = ""
	} else if _, pending := backend.terminalAccounts[accountID]; pending {
		status.State = application.AuthenticationStatePending
		status.Reason = application.AuthenticationReasonInteractionPending
	} else if failures := backend.reauthentication[accountID]; failures != nil &&
		failures[service] != "" {
		status.State = application.AuthenticationStateReauthenticationNeeded
		status.Reason = failures[service]
	} else if reasons := backend.signedOutReason[accountID]; reasons != nil &&
		reasons[service] != "" {
		status.Reason = reasons[service]
	}
	if status.State != application.AuthenticationStateAuthenticated {
		action, err := application.NewAuthenticationActionRequired(
			status.State,
			status.Reason,
			accountID,
			alias,
			service,
			provider,
		)
		if err != nil {
			return application.ServiceAuthenticationStatus{}, err
		}
		status.Action = &action
	}
	if err := status.Validate(accountID, alias); err != nil {
		return application.ServiceAuthenticationStatus{}, err
	}
	return status, nil
}

func (backend *sessionBackend) authenticationActionErrorLocked(
	accountID domain.AccountID,
	service application.AuthenticationService,
) error {
	alias, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return fmt.Errorf("account %q is not configured", accountID)
	}
	status, err := backend.serviceAuthenticationStatusLocked(
		accountID,
		alias,
		configured,
		service,
	)
	if err != nil {
		return err
	}
	if status.Action == nil {
		return errors.New("authenticated service unexpectedly requires recovery")
	}
	return application.NewAuthenticationActionError(*status.Action)
}

func setAuthenticationReason(
	destination *map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason,
	account domain.AccountID,
	service application.AuthenticationService,
	reason application.AuthenticationReason,
) {
	if *destination == nil {
		*destination = make(
			map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason,
		)
	}
	services := (*destination)[account]
	if services == nil {
		services = make(map[application.AuthenticationService]application.AuthenticationReason)
		(*destination)[account] = services
	}
	services[service] = reason
}

func (backend *sessionBackend) clearAuthenticationReasonsLocked(
	accountID domain.AccountID,
	account sessionAccount,
) {
	for _, service := range []application.AuthenticationService{
		application.AuthenticationServiceMail,
		application.AuthenticationServiceCalendar,
		application.AuthenticationServiceTasks,
		application.AuthenticationServiceMessages,
	} {
		if !account.serviceActive(service) {
			continue
		}
		if reasons := backend.reauthentication[accountID]; reasons != nil {
			delete(reasons, service)
		}
		if reasons := backend.signedOutReason[accountID]; reasons != nil {
			delete(reasons, service)
		}
	}
	if len(backend.reauthentication[accountID]) == 0 {
		delete(backend.reauthentication, accountID)
	}
	if len(backend.signedOutReason[accountID]) == 0 {
		delete(backend.signedOutReason, accountID)
	}
}

func sessionAccountComplete(configured config.Account, account sessionAccount) bool {
	for _, service := range configuredAuthenticationServices(configured) {
		if configuredServiceImplemented(configured, service) &&
			!account.serviceActive(service) {
			return false
		}
	}
	return true
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
			TaskProvider:     configured.TaskProvider(),
		}
		for _, service := range configuredAuthenticationServices(configured) {
			if service == application.AuthenticationServiceMessages {
				continue
			}
			status, err := backend.serviceAuthenticationStatusLocked(
				configured.ID,
				alias,
				configured,
				service,
			)
			if err != nil {
				return nil, err
			}
			if err := account.Services.Set(status); err != nil {
				return nil, err
			}
		}
		active, exists := backend.accounts[configured.ID]
		if exists && active.hasActiveService() {
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
			account.TaskDegradations = projectionDegradations(
				active.degradations,
				"tasks.",
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
			Account:           accountID,
			Alias:             alias,
			Provider:          configured.PrimaryProvider(),
			MailProvider:      configured.MailProvider(),
			CalendarProvider:  configured.CalendarProvider(),
			TaskProvider:      configured.TaskProvider(),
			MessagingProvider: configured.MessagingProvider(),
			State:             "signed_out",
		}
		for _, service := range configuredAuthenticationServices(configured) {
			serviceStatus, err := backend.serviceAuthenticationStatusLocked(
				accountID,
				alias,
				configured,
				service,
			)
			if err != nil {
				return daemonapi.SessionStatusResult{}, err
			}
			if err := state.Services.Set(serviceStatus); err != nil {
				return daemonapi.SessionStatusResult{}, err
			}
		}
		if account, exists := backend.accounts[accountID]; exists &&
			account.hasActiveService() {
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

func (backend *sessionBackend) Logout(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
) (daemonapi.LogoutResult, error) {
	if err := ctx.Err(); err != nil {
		return daemonapi.LogoutResult{}, err
	}
	if err := caller.Validate(); err != nil {
		return daemonapi.LogoutResult{}, err
	}
	if caller.Surface != "cli" {
		return daemonapi.LogoutResult{}, errors.New(
			"logout can only be started by an explicit local CLI command",
		)
	}
	if err := accountID.ValidateOpaque(); err != nil {
		return daemonapi.LogoutResult{}, err
	}

	backend.activationMu.Lock()
	defer backend.activationMu.Unlock()

	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return daemonapi.LogoutResult{}, errors.New("session backend is closed")
	}
	if _, _, exists := backend.configuration.AccountByID(accountID); !exists {
		backend.mu.Unlock()
		return daemonapi.LogoutResult{}, fmt.Errorf(
			"account %q is not configured",
			accountID,
		)
	}
	account, authenticated := backend.accounts[accountID]
	accountDone := make([]<-chan struct{}, 0, 3)
	if authenticated {
		leases := account.leases()
		for _, lease := range leases {
			if lease.usage != nil {
				accountDone = append(accountDone, lease.usage.closeAfterActive())
			}
		}
		if len(leases) == 0 && account.usage != nil {
			accountDone = append(accountDone, account.usage.closeAfterActive())
		}
		delete(backend.accounts, accountID)
	}
	_, configured, _ := backend.configuration.AccountByID(accountID)
	delete(backend.reauthentication, accountID)
	delete(backend.signedOutReason, accountID)
	for _, service := range configuredAuthenticationServices(configured) {
		setAuthenticationReason(
			&backend.signedOutReason,
			accountID,
			service,
			application.AuthenticationReasonUserSignedOut,
		)
	}
	var terminal *terminalLoginSession
	if terminalID, exists := backend.terminalAccounts[accountID]; exists {
		terminal = backend.terminalSessions[terminalID]
		delete(backend.terminalSessions, terminalID)
		delete(backend.terminalAccounts, accountID)
	}
	monitorCancel := backend.monitorCancel[accountID]
	monitorDone := backend.monitorDone[accountID]
	delete(backend.monitorCancel, accountID)
	delete(backend.monitorDone, accountID)
	delete(backend.monitorStarted, accountID)
	for token, preview := range backend.previews {
		if preview.account == accountID {
			delete(backend.previews, token)
		}
	}
	backend.mu.Unlock()

	if monitorCancel != nil {
		monitorCancel()
	}
	var closeErrors []error
	if terminal != nil {
		closeErrors = append(closeErrors, terminal.handle.Close())
	}
	for _, done := range append([]<-chan struct{}{monitorDone}, accountDone...) {
		if done == nil {
			continue
		}
		<-done
	}
	if authenticated {
		closeErrors = append(closeErrors, closeSessionAccount(account))
	}
	if err := errors.Join(closeErrors...); err != nil {
		return daemonapi.LogoutResult{}, err
	}
	return daemonapi.LogoutResult{
		Account: accountID, LoggedOut: true,
	}, nil
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

	progressChecked := false
	if input.Action == nil {
		if err := awaitTerminalLoginProgress(ctx, interaction); err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
		progressChecked = true
	}
	if input.Action != nil && input.Action.Type != "refresh" {
		action, err := terminalBrowserAction(*input.Action)
		if err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
		if err := interaction.handle.TerminalAct(ctx, action); err != nil {
			return daemonapi.TerminalLoginResult{}, err
		}
		if terminalActionWaitsForProgress(*input.Action) {
			if err := awaitTerminalLoginProgress(ctx, interaction); err != nil {
				return daemonapi.TerminalLoginResult{}, err
			}
			progressChecked = true
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
		// The monitor belongs to the daemon lifecycle, not this login request.
		backend.startMonitorLocked(input.Account, account) //nolint:contextcheck
		_ = backend.dropTerminalInteraction(interaction, false)
		return authenticatedTerminalResult(input.Account, account.captured), nil
	}
	if !errors.Is(credentialsErr, session.ErrNotReady) {
		return daemonapi.TerminalLoginResult{}, credentialsErr
	}

	refreshView := input.Action == nil || input.Action.Type == "refresh" ||
		input.Action.Type == "activate" || input.Action.Key == "enter"
	if refreshView && !progressChecked {
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

func terminalActionWaitsForProgress(action daemonapi.TerminalLoginAction) bool {
	return action.Type == "activate" || action.Type == "key" && action.Key == "enter"
}

func awaitTerminalLoginProgress(
	ctx context.Context,
	interaction *terminalLoginSession,
) error {
	latest := interaction.view
	var quietSince time.Time
	timer := time.NewTimer(terminalProgressWait)
	defer timer.Stop()
	ticker := time.NewTicker(terminalProgressPollInterval)
	defer ticker.Stop()
	for {
		_, credentialsErr := interaction.handle.CurrentSession()
		if credentialsErr == nil {
			return nil
		}
		if !errors.Is(credentialsErr, session.ErrNotReady) {
			return credentialsErr
		}
		view, err := interaction.handle.TerminalSnapshot(ctx)
		if err != nil {
			return err
		}
		candidate := terminalLoginView(view)
		if !terminalLoginViewsEqual(latest, candidate) {
			interaction.view = candidate
			latest = candidate
			quietSince = time.Now()
		} else if !quietSince.IsZero() && terminalLoginViewReady(candidate) &&
			time.Since(quietSince) >= terminalProgressQuietPeriod {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
		}
	}
}

func terminalLoginViewReady(view daemonapi.TerminalLoginView) bool {
	if view.Origin == "" || len(view.Controls) == 0 {
		return false
	}
	if len(view.Controls) == 1 && view.Controls[0].Kind == "activate" &&
		strings.EqualFold(strings.TrimSpace(view.Controls[0].Name), "cancel") {
		return false
	}
	return true
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
		return nil, errUnsupportedLegacyGoogleRoute
	}
	if hasGoogleRoute(configured) && !rollout.GoogleOAuthApproved {
		return nil, rollout.ErrGoogleOAuthPending
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

func terminalLoginViewsEqual(left, right daemonapi.TerminalLoginView) bool {
	if left.Origin != right.Origin || left.Title != right.Title || left.Text != right.Text ||
		len(left.Controls) != len(right.Controls) {
		return false
	}
	for index := range left.Controls {
		if left.Controls[index] != right.Controls[index] {
			return false
		}
	}
	return true
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

func withSessionService[T, S any](
	backend *sessionBackend,
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	service application.AuthenticationService,
	selectService func(sessionAccount) (S, error),
	use func(S) (T, error),
) (T, error) {
	var zero T
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return zero, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, err := backend.accountServices(
		ctx,
		accountID,
		caller,
		service,
	)
	if err != nil {
		return zero, err
	}
	selected, err := selectService(account)
	if err != nil {
		return finishSessionCall(backend, accountID, account, zero, err)
	}
	result, callErr := use(selected)
	return finishSessionCall(backend, accountID, account, result, callErr)
}

func withMailService[T any](
	backend *sessionBackend,
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	use func(*application.MailService) (T, error),
) (T, error) {
	return withSessionService(
		backend,
		ctx,
		accountID,
		caller,
		application.AuthenticationServiceMail,
		func(account sessionAccount) (*application.MailService, error) {
			return account.mailService()
		},
		use,
	)
}

func (backend *sessionBackend) ListMail(
	ctx context.Context,
	input application.MailListInput,
	caller domain.Caller,
) (application.MailPage, error) {
	return withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailPage, error) {
			return mail.List(ctx, input, caller)
		},
	)
}

func (backend *sessionBackend) SearchMail(
	ctx context.Context,
	input application.MailSearchInput,
	caller domain.Caller,
) (application.MailPage, error) {
	return withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailPage, error) {
			return mail.Search(ctx, input, caller)
		},
	)
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
	return withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailFolderPage, error) {
			return mail.ListFolders(ctx, input, caller)
		},
	)
}

func (backend *sessionBackend) GetMailBody(
	ctx context.Context,
	input application.MailBodyInput,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailBodyAccess, error) {
			return mail.GetBody(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailAttachmentAccess, error) {
			return mail.GetAttachment(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailDraftAccess, error) {
			return mail.CreateDraft(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailDraft(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailDraftAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailDraftAccess, error) {
		return mail.CommitDraft(ctx, token, caller)
	})
}

func (backend *sessionBackend) SendMail(
	ctx context.Context,
	input application.MailSendInput,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailSendAccess, error) {
			return mail.Send(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailSend(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailSendAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailSendAccess, error) {
		return mail.CommitSend(ctx, token, caller)
	})
}

func (backend *sessionBackend) SendMailDraft(
	ctx context.Context,
	input application.MailDraftSendInput,
	caller domain.Caller,
) (application.MailDraftSendAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailDraftSendAccess, error) {
			return mail.SendDraft(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailSendDraft(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailDraftSendAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailDraftSendAccess, error) {
		return mail.CommitSendDraft(ctx, token, caller)
	})
}

func (backend *sessionBackend) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailMoveAccess, error) {
			return mail.Move(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailMove(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailMoveAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailMoveAccess, error) {
		return mail.CommitMove(ctx, token, caller)
	})
}

func (backend *sessionBackend) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailReadStateAccess, error) {
			return mail.SetReadState(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailReadState(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailReadStateAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailReadStateAccess, error) {
		return mail.CommitReadState(ctx, token, caller)
	})
}

func (backend *sessionBackend) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
	caller domain.Caller,
) (application.MailDeleteAccess, error) {
	access, err := withMailService(
		backend,
		ctx,
		input.Account,
		caller,
		func(mail *application.MailService) (application.MailDeleteAccess, error) {
			return mail.Delete(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceMail,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitMailDelete(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailDeleteAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailDeleteAccess, error) {
		return mail.CommitDelete(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitMailBody(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailBodyAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailBodyAccess, error) {
		return mail.CommitBody(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitMailAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MailAttachmentAccess, error) {
	return commitMailPreview(backend, token, func(mail *application.MailService) (application.MailAttachmentAccess, error) {
		return mail.CommitAttachment(ctx, token, caller)
	})
}

func commitMailPreview[T any](
	backend *sessionBackend,
	token string,
	commit func(*application.MailService) (T, error),
) (T, error) {
	var zero T
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return zero, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, preview, exists := backend.accountForPreview(token)
	if !exists || preview.service != application.AuthenticationServiceMail || account.mail == nil {
		if exists {
			account.usage.end()
		}
		return zero, errors.New("invalid or expired approval token")
	}
	access, callErr := commit(account.mail)
	access, err := finishSessionCall(
		backend, preview.account, account, access, callErr,
	)
	if err != nil {
		return zero, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) rememberPreview(
	token string,
	account domain.AccountID,
	service application.AuthenticationService,
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
	backend.previews[token] = sessionPreview{
		account: account, service: service, expiresAt: expiresAt,
	}
}

func (backend *sessionBackend) accountForPreview(
	token string,
) (sessionAccount, sessionPreview, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	preview, exists := backend.previews[token]
	if !exists {
		return sessionAccount{}, sessionPreview{}, false
	}
	if !time.Now().Before(preview.expiresAt) {
		delete(backend.previews, token)
		return sessionAccount{}, sessionPreview{}, false
	}
	account, exists := backend.accounts[preview.account]
	if !exists || !account.serviceActive(preview.service) {
		return sessionAccount{}, sessionPreview{}, false
	}
	lease := account.lease(preview.service)
	if lease == nil || lease.usage == nil {
		return sessionAccount{}, sessionPreview{}, false
	}
	if err := lease.usage.begin(); err != nil {
		return sessionAccount{}, sessionPreview{}, false
	}
	account.usage = lease.usage
	account.borrowedLease = lease
	account.borrowedService = preview.service
	return account, preview, true
}

func (backend *sessionBackend) forgetPreview(token string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	delete(backend.previews, token)
}

func withCalendarService[T any](
	backend *sessionBackend,
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	use func(*application.CalendarService) (T, error),
) (T, error) {
	return withSessionService(
		backend,
		ctx,
		accountID,
		caller,
		application.AuthenticationServiceCalendar,
		func(account sessionAccount) (*application.CalendarService, error) {
			return account.calendarService()
		},
		use,
	)
}

func (backend *sessionBackend) ListCalendar(
	ctx context.Context,
	input application.CalendarListInput,
	caller domain.Caller,
) (application.CalendarPage, error) {
	return withCalendarService(
		backend,
		ctx,
		input.Account,
		caller,
		func(calendar *application.CalendarService) (application.CalendarPage, error) {
			return calendar.List(ctx, input, caller)
		},
	)
}

func (backend *sessionBackend) ListCalendarFolders(
	ctx context.Context,
	input application.CalendarFolderListInput,
	caller domain.Caller,
) (application.CalendarFolderPage, error) {
	return withCalendarService(
		backend,
		ctx,
		input.Account,
		caller,
		func(calendar *application.CalendarService) (application.CalendarFolderPage, error) {
			return calendar.ListFolders(ctx, input, caller)
		},
	)
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
	access, err := withCalendarService(
		backend,
		ctx,
		input.Account,
		caller,
		func(calendar *application.CalendarService) (application.CalendarCreateAccess, error) {
			return calendar.Create(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceCalendar,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarCreate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCreateAccess, error) {
	return commitCalendarPreview(backend, token, func(calendar *application.CalendarService) (application.CalendarCreateAccess, error) {
		return calendar.CommitCreate(ctx, token, caller)
	})
}

func (backend *sessionBackend) UpdateCalendar(
	ctx context.Context,
	input application.CalendarUpdateInput,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	access, err := withCalendarService(
		backend,
		ctx,
		input.Account,
		caller,
		func(calendar *application.CalendarService) (application.CalendarUpdateAccess, error) {
			return calendar.Update(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceCalendar,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarUpdate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarUpdateAccess, error) {
	return commitCalendarPreview(backend, token, func(calendar *application.CalendarService) (application.CalendarUpdateAccess, error) {
		return calendar.CommitUpdate(ctx, token, caller)
	})
}

func (backend *sessionBackend) CancelCalendar(
	ctx context.Context,
	input application.CalendarCancelInput,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	access, err := withCalendarService(
		backend,
		ctx,
		input.Account,
		caller,
		func(calendar *application.CalendarService) (application.CalendarCancelAccess, error) {
			return calendar.Cancel(ctx, input, caller)
		},
	)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			input.Account,
			application.AuthenticationServiceCalendar,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitCalendarCancel(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.CalendarCancelAccess, error) {
	return commitCalendarPreview(backend, token, func(calendar *application.CalendarService) (application.CalendarCancelAccess, error) {
		return calendar.CommitCancel(ctx, token, caller)
	})
}

func commitCalendarPreview[T any](
	backend *sessionBackend,
	token string,
	commit func(*application.CalendarService) (T, error),
) (T, error) {
	var zero T
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return zero, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, preview, exists := backend.accountForPreview(token)
	if !exists || preview.service != application.AuthenticationServiceCalendar || account.calendar == nil {
		if exists {
			account.usage.end()
		}
		return zero, errors.New("invalid or expired approval token")
	}
	access, callErr := commit(account.calendar)
	access, err := finishSessionCall(
		backend, preview.account, account, access, callErr,
	)
	if err != nil {
		return zero, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func withTaskService[T any](
	backend *sessionBackend,
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	use func(*application.TaskService) (T, error),
) (T, error) {
	return withSessionService(
		backend,
		ctx,
		accountID,
		caller,
		application.AuthenticationServiceTasks,
		func(account sessionAccount) (*application.TaskService, error) {
			return account.taskService()
		},
		use,
	)
}

func (backend *sessionBackend) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
	caller domain.Caller,
) (application.TaskListPage, error) {
	return withTaskService(backend, ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskListPage, error) {
		return tasks.ListLists(ctx, input, caller)
	})
}

func (backend *sessionBackend) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	return withTaskService(backend, ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskPage, error) {
		return tasks.List(ctx, input, caller)
	})
}

func (backend *sessionBackend) ListAllTasks(
	ctx context.Context,
	input application.TaskProjectionInput,
	caller domain.Caller,
) (application.TaskProjectionPage, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.TaskProjectionPage{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()
	service, err := application.NewProjectionService(backend)
	if err != nil {
		return application.TaskProjectionPage{}, err
	}
	return service.ListAllTasks(ctx, input, caller)
}

func (backend *sessionBackend) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
	caller domain.Caller,
) (application.Task, error) {
	return withTaskService(backend, ctx, input.Account, caller, func(tasks *application.TaskService) (application.Task, error) {
		return tasks.Get(ctx, input, caller)
	})
}

func (backend *sessionBackend) SearchTasks(
	ctx context.Context,
	input application.TaskSearchInput,
	caller domain.Caller,
) (application.TaskPage, error) {
	return withTaskService(backend, ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskPage, error) {
		return tasks.Search(ctx, input, caller)
	})
}

func (backend *sessionBackend) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
	caller domain.Caller,
) (application.TaskChangePage, error) {
	return withTaskService(backend, ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskChangePage, error) {
		return tasks.Sync(ctx, input, caller)
	})
}

func (backend *sessionBackend) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return backend.prepareTaskWrite(ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.Create(ctx, input, caller)
	})
}

func (backend *sessionBackend) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return backend.prepareTaskWrite(ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.Update(ctx, input, caller)
	})
}

func (backend *sessionBackend) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return backend.prepareTaskWrite(ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.Complete(ctx, input, caller)
	})
}

func (backend *sessionBackend) ReopenTask(
	ctx context.Context,
	input application.TaskStateInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return backend.prepareTaskWrite(ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.Reopen(ctx, input, caller)
	})
}

func (backend *sessionBackend) DeleteTask(
	ctx context.Context,
	input application.TaskDeleteInput,
	caller domain.Caller,
) (application.TaskWriteAccess, error) {
	return backend.prepareTaskWrite(ctx, input.Account, caller, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.Delete(ctx, input, caller)
	})
}

func (backend *sessionBackend) prepareTaskWrite(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	prepare func(*application.TaskService) (application.TaskWriteAccess, error),
) (application.TaskWriteAccess, error) {
	access, err := withTaskService(backend, ctx, accountID, caller, prepare)
	if err == nil && access.Preview != nil {
		backend.rememberPreview(
			access.Preview.Token,
			accountID,
			application.AuthenticationServiceTasks,
			access.Preview.ExpiresAt,
		)
	}
	return access, err
}

func (backend *sessionBackend) CommitTaskCreate(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTaskWrite(token, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.CommitCreate(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitTaskUpdate(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTaskWrite(token, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.CommitUpdate(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitTaskComplete(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTaskWrite(token, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.CommitComplete(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitTaskReopen(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTaskWrite(token, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.CommitReopen(ctx, token, caller)
	})
}

func (backend *sessionBackend) CommitTaskDelete(ctx context.Context, token string, caller domain.Caller) (application.TaskWriteAccess, error) {
	return backend.commitTaskWrite(token, func(tasks *application.TaskService) (application.TaskWriteAccess, error) {
		return tasks.CommitDelete(ctx, token, caller)
	})
}

func (backend *sessionBackend) commitTaskWrite(
	token string,
	commit func(*application.TaskService) (application.TaskWriteAccess, error),
) (application.TaskWriteAccess, error) {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.TaskWriteAccess{}, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, preview, exists := backend.accountForPreview(token)
	if !exists || preview.service != application.AuthenticationServiceTasks || account.tasks == nil {
		if exists {
			account.usage.end()
		}
		return application.TaskWriteAccess{}, errors.New("invalid or expired approval token")
	}
	access, callErr := commit(account.tasks)
	access, err := finishSessionCall(
		backend, preview.account, account, access, callErr,
	)
	if err != nil {
		return application.TaskWriteAccess{}, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) ListConversations(
	ctx context.Context,
	input application.ConversationListInput,
	caller domain.Caller,
) (application.ConversationPage, error) {
	return withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.ConversationPage, error) {
		return messages.ListConversations(ctx, input, caller)
	})
}

func (backend *sessionBackend) ListMessages(
	ctx context.Context,
	input application.MessageListInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	return withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessagePage, error) {
		return messages.ListMessages(ctx, input, caller)
	})
}

func (backend *sessionBackend) SearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
	caller domain.Caller,
) (application.MessagePage, error) {
	return withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessagePage, error) {
		return messages.SearchMessages(ctx, input, caller)
	})
}

func (backend *sessionBackend) GetMessage(
	ctx context.Context,
	input application.MessageGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	access, err := withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageSensitiveAccess, error) {
		return messages.GetMessage(ctx, input, caller)
	})
	backend.rememberMessagePreview(input.Account, access.Preview)
	return access, err
}

func (backend *sessionBackend) CommitGetMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	return commitMessagingPreview(backend, token, func(messages *application.MessagingService) (application.MessageSensitiveAccess, error) {
		return messages.CommitGetMessage(ctx, token, caller)
	})
}

func (backend *sessionBackend) GetMessageAttachment(
	ctx context.Context,
	input application.MessageAttachmentGetInput,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	access, err := withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageSensitiveAccess, error) {
		return messages.GetAttachment(ctx, input, caller)
	})
	backend.rememberMessagePreview(input.Account, access.Preview)
	return access, err
}

func (backend *sessionBackend) CommitGetMessageAttachment(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageSensitiveAccess, error) {
	return commitMessagingPreview(backend, token, func(messages *application.MessagingService) (application.MessageSensitiveAccess, error) {
		return messages.CommitGetAttachment(ctx, token, caller)
	})
}

func (backend *sessionBackend) SyncMessages(
	ctx context.Context,
	input application.MessageSyncInput,
	caller domain.Caller,
) (application.MessageChangePage, error) {
	return withMessagingService(backend, ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageChangePage, error) {
		return messages.SyncMessages(ctx, input, caller)
	})
}

func (backend *sessionBackend) SendMessage(
	ctx context.Context,
	input application.MessageSendInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.Send(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitSendMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitSend(ctx, token, caller)
	})
}

func (backend *sessionBackend) EditMessage(
	ctx context.Context,
	input application.MessageEditInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.Edit(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitEditMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitEdit(ctx, token, caller)
	})
}

func (backend *sessionBackend) DeleteMessage(
	ctx context.Context,
	input application.MessageDeleteInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.Delete(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitDeleteMessage(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitDelete(ctx, token, caller)
	})
}

func (backend *sessionBackend) ReactToMessage(
	ctx context.Context,
	input application.MessageReactionInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.React(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitMessageReaction(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitReact(ctx, token, caller)
	})
}

func (backend *sessionBackend) CreateConversation(
	ctx context.Context,
	input application.ConversationCreateInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CreateConversation(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitCreateConversation(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitCreateConversation(ctx, token, caller)
	})
}

func (backend *sessionBackend) ChangeConversationMembership(
	ctx context.Context,
	input application.ConversationMembershipInput,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.prepareMessageWrite(ctx, input.Account, caller, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.ChangeMembership(ctx, input, caller)
	})
}

func (backend *sessionBackend) CommitConversationMembership(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.MessageWriteAccess, error) {
	return backend.commitMessageWrite(token, func(messages *application.MessagingService) (application.MessageWriteAccess, error) {
		return messages.CommitMembership(ctx, token, caller)
	})
}

func withMessagingService[T any](
	backend *sessionBackend,
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	use func(*application.MessagingService) (T, error),
) (T, error) {
	var zero T
	if err := backend.requireMessagingRoute(accountID); err != nil {
		return zero, err
	}
	return withSessionService(
		backend, ctx, accountID, caller,
		application.AuthenticationServiceMessages,
		func(account sessionAccount) (*application.MessagingService, error) {
			return account.messagingService()
		},
		use,
	)
}

func (backend *sessionBackend) requireMessagingRoute(accountID domain.AccountID) error {
	backend.mu.Lock()
	alias, configured, exists := backend.configuration.AccountByID(accountID)
	backend.mu.Unlock()
	if !exists {
		return fmt.Errorf("account %q is not configured", accountID)
	}
	if configured.Messages == nil {
		return fmt.Errorf("configured account %q has no messaging route", alias)
	}
	return rollout.RequireMessaging(configured.Messages.Provider, configured.Messages.Kind())
}

func (backend *sessionBackend) rememberMessagePreview(
	accountID domain.AccountID,
	preview *approval.Preview,
) {
	if preview == nil {
		return
	}
	backend.rememberPreview(
		preview.Token,
		accountID,
		application.AuthenticationServiceMessages,
		preview.ExpiresAt,
	)
}

func (backend *sessionBackend) prepareMessageWrite(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	prepare func(*application.MessagingService) (application.MessageWriteAccess, error),
) (application.MessageWriteAccess, error) {
	access, err := withMessagingService(backend, ctx, accountID, caller, prepare)
	if err == nil {
		backend.rememberMessagePreview(accountID, access.Preview)
	}
	return access, err
}

func commitMessagingPreview[T any](
	backend *sessionBackend,
	token string,
	commit func(*application.MessagingService) (T, error),
) (T, error) {
	var zero T
	if err := backend.requireAnyMessagingRoute(); err != nil {
		return zero, err
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return zero, errors.New("session backend is closed")
	}
	backend.active.Add(1)
	backend.mu.Unlock()
	defer backend.active.Done()

	account, preview, exists := backend.accountForPreview(token)
	if !exists || preview.service != application.AuthenticationServiceMessages || account.messages == nil {
		if exists {
			account.usage.end()
		}
		return zero, errors.New("invalid or expired approval token")
	}
	if err := backend.requireMessagingRoute(preview.account); err != nil {
		account.usage.end()
		return zero, err
	}
	access, callErr := commit(account.messages)
	access, err := finishSessionCall(backend, preview.account, account, access, callErr)
	if err != nil {
		return zero, err
	}
	backend.forgetPreview(token)
	return access, nil
}

func (backend *sessionBackend) requireAnyMessagingRoute() error {
	backend.mu.Lock()
	aliases := make([]string, 0, len(backend.configuration.Accounts))
	for alias := range backend.configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	routes := make([]config.MessagingRoute, 0, len(aliases))
	for _, alias := range aliases {
		if route := backend.configuration.Accounts[alias].Messages; route != nil {
			routes = append(routes, *route)
		}
	}
	backend.mu.Unlock()
	if len(routes) == 0 {
		return errors.New("no messaging route is configured")
	}
	for _, route := range routes {
		if err := rollout.RequireMessaging(route.Provider, route.Kind()); err != nil {
			return err
		}
	}
	return nil
}

func (backend *sessionBackend) commitMessageWrite(
	token string,
	commit func(*application.MessagingService) (application.MessageWriteAccess, error),
) (application.MessageWriteAccess, error) {
	return commitMessagingPreview(backend, token, commit)
}

func (backend *sessionBackend) accountServices(
	_ context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	service application.AuthenticationService,
) (sessionAccount, error) {
	if err := caller.Validate(); err != nil {
		return sessionAccount{}, err
	}
	if err := service.Validate(); err != nil {
		return sessionAccount{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	alias, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return sessionAccount{}, fmt.Errorf(
			"account %q is not configured",
			accountID,
		)
	}
	if configuredServiceProvider(configured, service) == "" {
		return sessionAccount{}, fmt.Errorf(
			"configured account %q has no %s route",
			alias,
			service,
		)
	}
	if !configuredServiceImplemented(configured, service) {
		return sessionAccount{}, fmt.Errorf(
			"configured %s provider %q is not available in this build",
			service,
			configuredServiceProvider(configured, service),
		)
	}
	if (configuredServiceProvider(configured, service) == domain.ProviderGoogle ||
		configuredServiceProvider(configured, service) == domain.ProviderGoogleTasks) &&
		!rollout.GoogleOAuthApproved {
		return sessionAccount{}, rollout.ErrGoogleOAuthPending
	}
	if account, active := backend.accounts[accountID]; active &&
		account.serviceActive(service) {
		lease := account.lease(service)
		if lease == nil || lease.usage == nil {
			return sessionAccount{}, errors.New("account service lease is unavailable")
		}
		if err := lease.usage.begin(); err != nil {
			return sessionAccount{}, err
		}
		account.usage = lease.usage
		account.borrowedLease = lease
		account.borrowedService = service
		return account, nil
	}
	return sessionAccount{}, backend.authenticationActionErrorLocked(
		accountID,
		service,
	)
}

func (backend *sessionBackend) finishServiceUse(
	accountID domain.AccountID,
	account sessionAccount,
	callErr error,
) error {
	lease := account.borrowedLease
	if lease == nil || lease.usage == nil {
		return callErr
	}
	if callErr == nil || errors.Is(callErr, application.ErrWriteOutcomeUnknown) {
		lease.usage.end()
		return callErr
	}
	reason, authenticationFailure := application.ProviderAuthenticationReason(callErr)
	if !authenticationFailure && errors.Is(callErr, outlookweb.ErrSessionExpired) {
		reason = application.AuthenticationReasonSessionExpired
		authenticationFailure = true
	}
	if !authenticationFailure {
		lease.usage.end()
		return callErr
	}

	backend.mu.Lock()
	invalidation := backend.invalidateAuthenticationLeaseLocked(
		accountID,
		account.borrowedService,
		lease,
		reason,
	)
	backend.mu.Unlock()

	lease.usage.end()
	if invalidation.monitorCancel != nil {
		invalidation.monitorCancel()
	}
	if invalidation.monitorDone != nil {
		<-invalidation.monitorDone
	}
	<-invalidation.leaseDone
	_ = lease.closeAfterDrain()
	return invalidation.actionErr
}

type authenticationLeaseInvalidation struct {
	monitorCancel context.CancelFunc
	monitorDone   <-chan struct{}
	leaseDone     <-chan struct{}
	actionErr     error
}

// invalidateAuthenticationLeaseLocked makes the stale lease unreachable and
// closes its usage gate. The caller owns cancellation, draining, and close
// after releasing backend.mu.
func (backend *sessionBackend) invalidateAuthenticationLeaseLocked(
	accountID domain.AccountID,
	requestedService application.AuthenticationService,
	lease *sessionLease,
	reason application.AuthenticationReason,
) authenticationLeaseInvalidation {
	current, active := backend.accounts[accountID]
	detached := sessionServiceSet(0)
	if active && current.lease(requestedService) == lease {
		detached = current.detachLease(lease)
		for _, service := range []application.AuthenticationService{
			application.AuthenticationServiceMail,
			application.AuthenticationServiceCalendar,
			application.AuthenticationServiceTasks,
		} {
			if detached.includes(service) {
				setAuthenticationReason(
					&backend.reauthentication,
					accountID,
					service,
					reason,
				)
			}
		}
		for token, preview := range backend.previews {
			if preview.account == accountID && detached.includes(preview.service) {
				delete(backend.previews, token)
			}
		}
		if len(current.leases()) == 0 {
			delete(backend.accounts, accountID)
		} else {
			backend.accounts[accountID] = current
		}
	}
	var result authenticationLeaseInvalidation
	if detached.includes(application.AuthenticationServiceMail) {
		result.monitorCancel = backend.monitorCancel[accountID]
		result.monitorDone = backend.monitorDone[accountID]
		delete(backend.monitorCancel, accountID)
		delete(backend.monitorDone, accountID)
		delete(backend.monitorStarted, accountID)
	}
	result.leaseDone = lease.usage.closeAfterActive()
	result.actionErr = backend.authenticationActionForReasonLocked(
		accountID,
		requestedService,
		reason,
	)
	return result
}

func (backend *sessionBackend) authenticationActionForReasonLocked(
	accountID domain.AccountID,
	service application.AuthenticationService,
	reason application.AuthenticationReason,
) error {
	alias, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return fmt.Errorf("account %q is not configured", accountID)
	}
	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateReauthenticationNeeded,
		reason,
		accountID,
		alias,
		service,
		configuredServiceProvider(configured, service),
	)
	if err != nil {
		return err
	}
	return application.NewAuthenticationActionError(action)
}

func finishSessionCall[T any](
	backend *sessionBackend,
	accountID domain.AccountID,
	account sessionAccount,
	result T,
	callErr error,
) (T, error) {
	return result, backend.finishServiceUse(accountID, account, callErr)
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
	_, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		backend.mu.Unlock()
		return sessionAccount{}, fmt.Errorf("account %q is not configured", accountID)
	}
	if account, active := backend.accounts[accountID]; active &&
		sessionAccountComplete(configured, account) {
		backend.mu.Unlock()
		return account, nil
	}
	if _, pending := backend.terminalAccounts[accountID]; pending {
		backend.mu.Unlock()
		return sessionAccount{}, errors.New("terminal login is in progress for this account")
	}
	backend.mu.Unlock()

	if hasGoogleWebRoute(configured) {
		return sessionAccount{}, errUnsupportedLegacyGoogleRoute
	}
	if hasGoogleRoute(configured) && !rollout.GoogleOAuthApproved {
		return sessionAccount{}, rollout.ErrGoogleOAuthPending
	}
	taskDegradation, err := inactiveTaskRoute(configured)
	if err != nil {
		return sessionAccount{}, err
	}
	messagingDegradation, err := inactiveMessagingRoute(configured)
	if err != nil {
		return sessionAccount{}, err
	}
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
	activeRoutes := configured
	if messagingDegradation != nil {
		activeRoutes.Messages = nil
	}
	standards, err := backend.nonOutlookAccount(ctx, activeRoutes)
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(services))
	}
	services = mergeSessionAccounts(services, standards)
	if activeRoutes.Messages != nil && activeRoutes.Messages.TeamsWeb != nil {
		teams, err := backend.teamsWebMessagingAccount(ctx, activeRoutes)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(services))
		}
		services = mergeSessionAccounts(services, teams)
	}
	if taskDegradation != nil {
		services.degradations = append(services.degradations, *taskDegradation)
		services.staticDegradations = append(
			services.staticDegradations,
			*taskDegradation,
		)
	}
	if messagingDegradation != nil {
		services.degradations = append(services.degradations, *messagingDegradation)
		services.staticDegradations = append(
			services.staticDegradations,
			*messagingDegradation,
		)
	}

	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return sessionAccount{}, errors.Join(
			errors.New("session backend closed during authentication"),
			closeSessionAccount(services),
		)
	}
	previous, replacing := backend.accounts[accountID]
	if replacing {
		delete(backend.accounts, accountID)
		for token, preview := range backend.previews {
			if preview.account == accountID {
				delete(backend.previews, token)
			}
		}
	}
	monitorCancel := backend.monitorCancel[accountID]
	monitorDone := backend.monitorDone[accountID]
	delete(backend.monitorCancel, accountID)
	delete(backend.monitorDone, accountID)
	delete(backend.monitorStarted, accountID)
	backend.mu.Unlock()

	if monitorCancel != nil {
		monitorCancel()
	}
	if monitorDone != nil {
		<-monitorDone
	}
	if replacing {
		if err := closeSessionAccount(previous); err != nil {
			return sessionAccount{}, errors.Join(
				fmt.Errorf("close previous account session: %w", err),
				closeSessionAccount(services),
			)
		}
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return sessionAccount{}, errors.Join(
			errors.New("session backend closed during authentication"),
			closeSessionAccount(services),
		)
	}
	backend.accounts[accountID] = services
	backend.clearAuthenticationReasonsLocked(accountID, services)
	// The monitor belongs to the daemon lifecycle, not this login request.
	backend.startMonitorLocked(accountID, services) //nolint:contextcheck
	return services, nil
}

// inactiveTaskRoute keeps a staged task selection from granting authority or
// disabling an otherwise usable mail/calendar route. A task-only account still
// fails before authentication because it has no active service in this build.
func inactiveTaskRoute(configured config.Account) (*domain.Degradation, error) {
	if configured.Tasks == nil {
		return nil, nil
	}
	if configured.Tasks.Provider == domain.ProviderMicrosoftGraph &&
		configured.Tasks.MicrosoftGraph != nil {
		return nil, nil
	}
	if configured.Tasks.Provider == domain.ProviderTodoist &&
		configured.Tasks.Todoist != nil {
		return nil, nil
	}
	if configured.Tasks.Provider == domain.ProviderCalDAV &&
		configured.Tasks.CalDAV != nil {
		return nil, nil
	}
	if configured.Tasks.Provider == domain.ProviderGoogleTasks &&
		configured.Tasks.GoogleTasks != nil {
		return nil, nil
	}
	if configured.Tasks.Provider == domain.ProviderTickTick &&
		configured.Tasks.TickTick != nil {
		return nil, nil
	}
	if configured.Mail == nil && configured.Calendar == nil {
		return nil, fmt.Errorf(
			"configured task provider %q is not available in this build",
			configured.Tasks.Provider,
		)
	}
	return &domain.Degradation{
		Feature: "tasks.route",
		Reason: fmt.Sprintf(
			"configured task provider %q is not available in this build",
			configured.Tasks.Provider,
		),
	}, nil
}

// inactiveMessagingRoute keeps the complete v0.9 implementation dormant
// until release-owned evidence is accepted. The check happens before browser
// launch, credential resolution, OAuth, or provider traffic. A mixed account
// retains its already released services with an explicit degradation; a
// messaging-only account cannot be mistaken for an authenticated empty
// session.
func inactiveMessagingRoute(configured config.Account) (*domain.Degradation, error) {
	if configured.Messages == nil {
		return nil, nil
	}
	err := rollout.RequireMessaging(
		configured.Messages.Provider,
		configured.Messages.Kind(),
	)
	if err == nil {
		return nil, nil
	}
	if configured.Mail == nil && configured.Calendar == nil && configured.Tasks == nil {
		return nil, err
	}
	return &domain.Degradation{
		Feature: "messages.route",
		Reason:  "the configured messaging route is awaiting the v0.9 release evidence gate",
	}, nil
}

type observedMessagingClient interface {
	application.MessagingPort
	MessageActor() application.MessageActor
	MessageCapabilities() application.MessageCapabilities
	MessageDegradations() []domain.Degradation
}

func (backend *sessionBackend) messagingSessionAccount(
	configured config.Account,
	provider domain.MessagingProviderID,
	route domain.MessagingRouteKind,
	workspaceID string,
	client observedMessagingClient,
	closers ...sessionCloser,
) (sessionAccount, error) {
	capabilities := client.MessageCapabilities()
	messages, err := application.NewMessagingService(
		backend.guard,
		client,
		application.MessagingOptions{
			Provenance: application.MessagingProvenance{
				AccountID: configured.ID, Provider: provider, Route: route,
				WorkspaceID: workspaceID, Actor: client.MessageActor(),
			},
			Capabilities: capabilities,
			Degradations: client.MessageDegradations(),
		},
	)
	result := sessionAccount{
		closers: closers, messages: messages, captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Messages: true, IncrementalSync: capabilities.IncrementalSync,
			AttachmentReads:  capabilities.AttachmentReads,
			AttachmentWrites: capabilities.AttachmentWrites,
		},
		degradations: client.MessageDegradations(),
	}
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
	}
	return leasedSessionAccount(result), nil
}

func (backend *sessionBackend) slackMessagingAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Messages == nil || configured.Messages.Slack == nil {
		return sessionAccount{}, errors.New("slack messaging route settings are missing")
	}
	selected := configured.Messages.Slack
	secret, err := backend.credentials.Resolve(ctx, selected.Authorization)
	if err != nil {
		return sessionAccount{}, err
	}
	fileOrigin, err := slackapi.FileOrigin(selected.APIBase)
	if err != nil {
		return sessionAccount{}, errors.Join(err, secret.Close())
	}
	parsed, _ := url.Parse(selected.APIBase)
	origin := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	authorizer, authorizerErr := credential.NewBearerAuthorizer(origin, secret)
	fileAuthorizer, fileAuthorizerErr := credential.NewBearerAuthorizer(fileOrigin, secret)
	closeSecretErr := secret.Close()
	if authorizerErr != nil || fileAuthorizerErr != nil || closeSecretErr != nil {
		if authorizer != nil {
			authorizerErr = errors.Join(authorizerErr, authorizer.Close())
		}
		if fileAuthorizer != nil {
			fileAuthorizerErr = errors.Join(fileAuthorizerErr, fileAuthorizer.Close())
		}
		return sessionAccount{}, errors.Join(
			authorizerErr, fileAuthorizerErr, closeSecretErr,
		)
	}
	newTransport := func(authorizer *credential.BearerAuthorizer) *bearerTransport {
		base := http.DefaultTransport.(*http.Transport).Clone()
		base.Proxy = nil
		base.DisableCompression = true
		return &bearerTransport{base: base, authorizer: authorizer}
	}
	transport := newTransport(authorizer)
	fileTransport := newTransport(fileAuthorizer)
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("slack API redirects are not accepted")
		},
	}
	fileHTTPClient := &http.Client{
		Transport: fileTransport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("slack file redirects are not accepted")
		},
	}
	factory := backend.newSlack
	if factory == nil {
		factory = slackapi.New
	}
	client, err := factory(ctx, slackapi.Options{
		APIBase: selected.APIBase, WorkspaceID: selected.WorkspaceID,
		ReadOnly: selected.ReadOnly, HTTP: httpClient, FilesHTTP: fileHTTPClient,
	})
	if err != nil {
		transport.CloseIdleConnections()
		fileTransport.CloseIdleConnections()
		return sessionAccount{}, errors.Join(
			err, authorizer.Close(), fileAuthorizer.Close(),
		)
	}
	return backend.messagingSessionAccount(
		configured,
		domain.MessagingProviderSlack,
		domain.MessagingRouteSlackAPI,
		selected.WorkspaceID,
		client,
		client,
		authorizer,
		fileAuthorizer,
	)
}

func (backend *sessionBackend) mattermostMessagingAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Messages == nil || configured.Messages.Mattermost == nil {
		return sessionAccount{}, errors.New("mattermost messaging route settings are missing")
	}
	selected := configured.Messages.Mattermost
	secret, err := backend.credentials.Resolve(ctx, selected.Authorization)
	if err != nil {
		return sessionAccount{}, err
	}
	authorizer, authorizerErr := credential.NewBearerAuthorizer(selected.Origin, secret)
	closeSecretErr := secret.Close()
	if authorizerErr != nil || closeSecretErr != nil {
		return sessionAccount{}, errors.Join(authorizerErr, closeSecretErr)
	}
	factory := backend.newMattermost
	if factory == nil {
		factory = mattermostapi.New
	}
	client, err := factory(ctx, mattermostapi.Options{
		Origin: selected.Origin, WorkspaceID: selected.WorkspaceID,
		ReadOnly: selected.ReadOnly, Authorization: authorizer,
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, authorizer.Close())
	}
	return backend.messagingSessionAccount(
		configured,
		domain.MessagingProviderMattermost,
		domain.MessagingRouteMattermost,
		selected.WorkspaceID,
		client,
		client,
	)
}

func (backend *sessionBackend) teamsWebMessagingAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Messages == nil || configured.Messages.TeamsWeb == nil {
		return sessionAccount{}, errors.New("teams Web messaging route settings are missing")
	}
	selected := configured.Messages.TeamsWeb
	profileDirectory, err := paths.ProviderProfileDir(
		configured.ID,
		domain.ProviderID(domain.MessagingProviderMicrosoftTeams),
	)
	if err != nil {
		return sessionAccount{}, err
	}
	if _, err := fmt.Fprintf(
		backend.app.stderr,
		"Opening Teams Web for account %q; complete sign-in in the browser.\n",
		configured.ID,
	); err != nil {
		return sessionAccount{}, err
	}
	handle, err := backend.app.launch(ctx, browser.Options{
		Origin: selected.Web.Origin, StartURL: strings.TrimRight(selected.Web.Origin, "/") + "/v2/",
		ProfileDir: profileDirectory, Executable: backend.configuration.Browser.Executable,
		BrowserOwnedOnly: true,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	driver, supported := handle.(teamsweb.Driver)
	if !supported {
		return sessionAccount{}, errors.Join(
			errors.New("the browser does not implement the closed Teams Web driver"),
			handle.Close(),
		)
	}
	waitContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(backend.configuration.Browser.LoginTimeout),
	)
	defer cancel()
	factory := backend.newTeamsWeb
	if factory == nil {
		factory = teamsweb.New
	}
	client, err := factory(waitContext, teamsweb.Options{
		Origin: selected.Web.Origin, WorkspaceID: selected.WorkspaceID,
		ReadOnly: selected.ReadOnly, Driver: driver,
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, handle.Close())
	}
	return backend.messagingSessionAccount(
		configured,
		domain.MessagingProviderMicrosoftTeams,
		domain.MessagingRouteTeamsWeb,
		selected.WorkspaceID,
		client,
		handle,
	)
}

func (backend *sessionBackend) startMonitorLocked(
	accountID domain.AccountID,
	account sessionAccount,
) {
	if backend.monitorStarted[accountID] || account.mail == nil ||
		account.mailLease == nil || account.mailLease.usage == nil {
		return
	}
	policy, err := backend.MonitorPolicy(backend.lifecycle, accountID)
	if err != nil || !policy.Mode.Collects() {
		return
	}
	backend.monitorStarted[accountID] = true
	// Logout retains and calls cancel; daemon shutdown cancels the parent.
	monitorContext, cancel := context.WithCancel(backend.lifecycle) //nolint:gosec
	done := make(chan struct{})
	backend.monitorCancel[accountID] = cancel
	backend.monitorDone[accountID] = done
	backend.active.Add(1)
	go backend.monitorLoop(
		monitorContext,
		accountID,
		policy,
		account.mail,
		account.mailLease,
		done,
	)
}

func (backend *sessionBackend) monitorLoop(
	ctx context.Context,
	accountID domain.AccountID,
	policy application.MonitorPolicy,
	mail *application.MailService,
	lease *sessionLease,
	done chan<- struct{},
) {
	defer backend.active.Done()
	defer close(done)
	poll := func() bool {
		err := backend.monitorEngine.Poll(ctx, policy, mail)
		if err == nil || ctx.Err() != nil {
			return true
		}
		reason, authenticationFailure := application.ProviderAuthenticationReason(err)
		if !authenticationFailure && errors.Is(err, outlookweb.ErrSessionExpired) {
			reason = application.AuthenticationReasonSessionExpired
			authenticationFailure = true
		}
		if authenticationFailure {
			backend.invalidateMonitorAuthentication(accountID, lease, reason)
			return false
		}
		if ctx.Err() == nil {
			_, _ = fmt.Fprintf(
				backend.app.stderr,
				"monitor %s encountered a safe failure and will retry; inspect monitor status and the local audit; if the pending queue is saturated, run `corr events purge --account %s --approve`\n",
				policy.Alias,
				policy.Alias,
			)
		}
		return true
	}
	if !poll() {
		return
	}
	ticker := time.NewTicker(policy.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !poll() {
				return
			}
		}
	}
}

func (backend *sessionBackend) invalidateMonitorAuthentication(
	accountID domain.AccountID,
	lease *sessionLease,
	reason application.AuthenticationReason,
) {
	if lease == nil || lease.usage == nil {
		return
	}
	backend.mu.Lock()
	invalidation := backend.invalidateAuthenticationLeaseLocked(
		accountID,
		application.AuthenticationServiceMail,
		lease,
		reason,
	)
	backend.mu.Unlock()
	if invalidation.monitorCancel != nil {
		invalidation.monitorCancel()
	}
	<-invalidation.leaseDone
	_ = lease.closeAfterDrain()
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
				MaxAttendees:          backend.configuration.Policy.MaxAttendees,
				Effects:               providerCalendarEffects(domain.ProviderMicrosoftOWA),
				OnlineMeetingProvider: "teams",
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
	return leasedSessionAccount(services), nil
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
	password := secret.CopyBytes()
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
	return leasedSessionAccount(result), nil
}

func jmapCapabilityReport(
	observed jmap.ObservedCapabilities,
) (domain.Capabilities, []domain.Degradation) {
	capabilities := domain.Capabilities{
		Mail: true, Folders: true, AttachmentReads: true,
		AttachmentWrites: !observed.ReadOnly, IncrementalSync: true,
	}
	degradations := make([]domain.Degradation, 0, 3)
	degradations = append(degradations, domain.Degradation{
		Feature: "mail.send_draft",
		Reason:  "JMAP EmailSubmission cannot atomically require the reviewed Email state; exact-version saved-draft send is unavailable",
	})
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
	password := secret.CopyBytes()
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
	result.capabilities.DraftSend = observed.Drafts && observed.Sent && observed.UIDPlus
	if !observed.Move && !observed.UIDPlus {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.move",
			Reason:  "the authenticated IMAP server advertises neither MOVE nor UIDPLUS; safe targeted move is unavailable",
		})
	}
	if !observed.UIDPlus {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.delete",
			Reason:  "the authenticated IMAP server does not advertise UIDPLUS; safe UID EXPUNGE is unavailable",
		})
	}
	if !observed.Sent {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.send",
			Reason:  "the authenticated IMAP account exposes no Sent mailbox; SMTP submission fails closed before sending",
		})
	}
	if !result.capabilities.DraftSend {
		result.degradations = append(result.degradations, domain.Degradation{
			Feature: "mail.send_draft",
			Reason:  "exact saved-draft send requires observed Drafts and Sent mailboxes plus UIDPLUS for targeted cleanup",
		})
	}
	return leasedSessionAccount(result), nil
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
	password := secret.CopyBytes()
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
			Effects:      calDAVCalendarEffects(client.SchedulingAvailable()),
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderCalDAV,
				CalendarID: "configured-caldav-calendar",
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	result := sessionAccount{
		closers:  []sessionCloser{client},
		calendar: calendar,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Calendar: true,
		},
	}
	if !client.SchedulingAvailable() {
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "calendar.attendee_scheduling",
				Reason:  "the authenticated CalDAV principal does not expose RFC 6638 server-managed scheduling; attendee create, update, and cancellation fail closed",
			},
		)
	}
	return leasedSessionAccount(result), nil
}

func (backend *sessionBackend) calDAVTaskAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Tasks == nil || configured.Tasks.Provider != domain.ProviderCalDAV ||
		configured.Tasks.CalDAV == nil {
		return sessionAccount{}, errors.New("CalDAV task route settings are missing")
	}
	route := configured.Tasks.CalDAV
	secret, err := backend.credentials.Resolve(ctx, route.Credential)
	if err != nil {
		return sessionAccount{}, err
	}
	defer func() { _ = secret.Close() }()
	password := secret.CopyBytes()
	defer func() {
		for index := range password {
			password[index] = 0
		}
	}()
	factory := backend.newCalDAVTasks
	if factory == nil {
		factory = caldavprovider.NewTasks
	}
	client, err := factory(ctx, caldavprovider.TaskOptions{
		Endpoint: route.Endpoint, TaskListPath: route.TaskListPath,
		Username: route.Username, Password: password,
	})
	if err != nil {
		return sessionAccount{}, err
	}
	capabilities := client.TaskCapabilities()
	tasks, err := application.NewTaskService(
		backend.guard,
		client,
		application.TaskOptions{
			Capabilities: capabilities,
			Degradations: client.TaskDegradations(),
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderCalDAV,
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return leasedSessionAccount(sessionAccount{
		closers: []sessionCloser{client}, tasks: tasks,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Tasks: true, IncrementalSync: len(capabilities.SyncModes) != 0,
		},
	}), nil
}

func (backend *sessionBackend) googleAccount(
	ctx context.Context,
	configured config.Account,
	clientRoute config.OAuthClient,
	mailRoute *config.GoogleMailRoute,
	calendarRoute *config.OAuthRoute,
) (sessionAccount, error) {
	mailEnabled := mailRoute != nil
	calendarEnabled := calendarRoute != nil
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderGoogle,
		oauthlocal.Services{Mail: mailEnabled, Calendar: calendarEnabled},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{
			GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorization, err := manager.Authorize(ctx, clientRoute, provider)
	if err != nil {
		return sessionAccount{}, err
	}
	apiBase := "https://www.googleapis.com"
	if calendarEnabled {
		apiBase = calendarRoute.APIBase
	}
	sender := ""
	if mailEnabled {
		sender = mailRoute.Mailbox
	}
	factory := backend.newGoogle
	if factory == nil {
		factory = googleapi.New
	}
	client, err := factory(ctx, googleapi.Options{
		APIBase:  apiBase,
		Address:  configured.Address,
		Sender:   sender,
		Mail:     mailEnabled,
		Calendar: calendarEnabled,
		HTTP:     authorization.HTTPClient(),
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
		result.mail, err = application.NewMailService(
			backend.guard,
			client,
			application.MailOptions{
				MaxRecipients: backend.configuration.Policy.MaxRecipients,
				Provenance: domain.Provenance{
					AccountID: configured.ID,
					Provider:  domain.ProviderGoogle,
					MailboxID: "gmail-api",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
		}
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "mail.send_draft",
				Reason:  "the Gmail draft send action exposes no atomic reviewed-version precondition; exact-version saved-draft send is unavailable",
			},
			domain.Degradation{
				Feature: "mail.delete",
				Reason:  "the approved gmail.modify design does not permit immediate permanent deletion; move the message to Trash instead",
			},
			domain.Degradation{
				Feature: "mail.push_history",
				Reason:  "the Gmail API route does not register push watches or expose durable history cursors",
			},
			domain.Degradation{
				Feature: "mail.scheduled_send",
				Reason:  "the Gmail API adapter does not expose scheduled sending",
			},
		)
	}
	if calendarEnabled {
		if client.MeetAvailable() {
			result.capabilities.OnlineMeeting = "google-meet"
		} else {
			result.degradations = append(
				result.degradations,
				domain.Degradation{
					Feature: "calendar.online_meeting_create",
					Reason:  "the authenticated Google calendar does not advertise hangoutsMeet as an allowed conference solution",
				},
			)
		}
		result.calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees: backend.configuration.Policy.MaxAttendees,
				Effects:      providerCalendarEffects(domain.ProviderGoogle),
				OnlineMeetingProvider: func() string {
					if client.MeetAvailable() {
						return "google-meet"
					}
					return ""
				}(),
				Provenance: domain.Provenance{
					AccountID:  configured.ID,
					Provider:   domain.ProviderGoogle,
					CalendarID: "primary",
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(
				err,
				closeSessionAccount(result),
			)
		}
	}
	return leasedSessionAccount(result), nil
}

func (backend *sessionBackend) graphAPIAccount(
	ctx context.Context,
	configured config.Account,
	route config.OAuthRoute,
	mailEnabled, calendarEnabled, tasksEnabled, taskWrite bool,
	messaging *config.TeamsGraphMessagingRoute,
) (sessionAccount, error) {
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderMicrosoftGraph,
		oauthlocal.Services{
			Mail: mailEnabled, Calendar: calendarEnabled,
			Tasks: tasksEnabled, TaskWrite: taskWrite,
			Messages:       messaging != nil,
			MessageWrite:   messaging != nil && !messaging.ReadOnly,
			MicrosoftCloud: route.MicrosoftCloud,
		},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{
			GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorization, err := manager.Authorize(ctx, route.Client(), provider)
	if err != nil {
		return sessionAccount{}, err
	}
	authorizedHTTP := authorization.HTTPClient()
	result := sessionAccount{
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Mail: mailEnabled, Calendar: calendarEnabled, Tasks: tasksEnabled,
			Folders:          mailEnabled,
			AttachmentReads:  mailEnabled,
			AttachmentWrites: mailEnabled,
		},
	}
	var client *graphapi.Client
	if mailEnabled || calendarEnabled || tasksEnabled {
		factory := backend.newGraph
		if factory == nil {
			factory = graphapi.New
		}
		client, err = factory(ctx, graphapi.Options{
			APIBase: route.APIBase,
			Address: configured.Address,
			Mail:    mailEnabled, Calendar: calendarEnabled,
			Tasks: tasksEnabled, TaskWrite: taskWrite,
			HTTP: authorizedHTTP,
		})
		if err != nil {
			return sessionAccount{}, err
		}
		result.closers = append(result.closers, client)
	}
	if mailEnabled {
		result.degradations = append(
			result.degradations,
			domain.Degradation{
				Feature: "mail.send_draft",
				Reason:  "Graph draft send exposes no atomic changeKey precondition; exact-version saved-draft send is unavailable",
			},
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
				Reason:  "Graph permanentDelete exposes no atomic ETag precondition; Corresync revalidates the exact reviewed message immediately before the action",
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
	if tasksEnabled {
		result.capabilities.IncrementalSync = true
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
			return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
		}
	}
	if calendarEnabled {
		result.calendar, err = application.NewCalendarService(
			backend.guard,
			client,
			application.CalendarOptions{
				MaxAttendees:          backend.configuration.Policy.MaxAttendees,
				OnlineMeetingProvider: "teams",
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
			return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
		}
	}
	if tasksEnabled {
		taskDegradations := []domain.Degradation{
			{
				Feature: "tasks.linked_sources",
				Reason:  "only typed Corresync linked resources are projected; unrelated provider links remain untouched",
			},
			{
				Feature: "tasks.search",
				Reason:  "Microsoft Graph To Do does not expose task search",
			},
		}
		if taskWrite {
			taskDegradations = append(taskDegradations,
				domain.Degradation{
					Feature: "tasks.concurrency",
					Reason:  "To Do does not document an atomic If-Match contract; Corresync revalidates the exact ETag immediately before each core task write",
				},
				domain.Degradation{
					Feature: "tasks.write_assembly",
					Reason:  "checklist and linked-resource replacement uses bounded follow-up requests and reports partial outcomes as unknown",
				},
			)
		}
		result.tasks, err = application.NewTaskService(
			backend.guard,
			client,
			application.TaskOptions{
				Capabilities: client.TaskCapabilities(),
				Degradations: taskDegradations,
				Provenance: domain.Provenance{
					AccountID: configured.ID,
					Provider:  domain.ProviderMicrosoftGraph,
				},
			},
		)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
		}
	}
	result = leasedSessionAccount(result)
	if messaging == nil {
		return result, nil
	}
	factory := backend.newTeamsGraph
	if factory == nil {
		factory = teamsgraph.New
	}
	messagingClient, err := factory(ctx, teamsgraph.Options{
		APIBase: route.APIBase, WorkspaceID: messaging.WorkspaceID,
		GrantedScopes: provider.Scopes, ReadOnly: messaging.ReadOnly,
		HTTP: authorizedHTTP,
	})
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
	}
	messagingAccount, err := backend.messagingSessionAccount(
		configured,
		domain.MessagingProviderMicrosoftTeams,
		domain.MessagingRouteTeamsGraph,
		messaging.WorkspaceID,
		messagingClient,
		messagingClient,
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(result))
	}
	return mergeSessionAccounts(result, messagingAccount), nil
}

func (backend *sessionBackend) todoistAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Tasks == nil || configured.Tasks.Provider != domain.ProviderTodoist ||
		configured.Tasks.Todoist == nil {
		return sessionAccount{}, errors.New("the Todoist task route settings are missing")
	}
	selected := configured.Tasks.Todoist
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderTodoist,
		oauthlocal.Services{Tasks: true, TaskWrite: !selected.ReadOnly},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{
			GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorization, err := manager.Authorize(ctx, selected.OAuth.Client(), provider)
	if err != nil {
		return sessionAccount{}, err
	}
	factory := backend.newTodoist
	if factory == nil {
		factory = todoist.New
	}
	client, err := factory(ctx, todoist.Options{
		APIBase: selected.OAuth.APIBase, Address: configured.Address,
		ReadOnly: selected.ReadOnly, HTTP: authorization.HTTPClient(),
	})
	if err != nil {
		return sessionAccount{}, err
	}
	tasks, err := application.NewTaskService(
		backend.guard,
		client,
		application.TaskOptions{
			Capabilities: client.TaskCapabilities(),
			Degradations: client.TaskDegradations(),
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderTodoist,
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return leasedSessionAccount(sessionAccount{
		closers: []sessionCloser{client}, tasks: tasks,
		captured:     time.Now().UTC(),
		capabilities: domain.Capabilities{Tasks: true, IncrementalSync: true},
	}), nil
}

func (backend *sessionBackend) googleTaskAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Tasks == nil || configured.Tasks.Provider != domain.ProviderGoogleTasks ||
		configured.Tasks.GoogleTasks == nil {
		return sessionAccount{}, errors.New("the Google Tasks route settings are missing")
	}
	selected := configured.Tasks.GoogleTasks
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderGoogleTasks,
		oauthlocal.Services{Tasks: true, TaskWrite: !selected.ReadOnly},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{
			GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorization, err := manager.Authorize(ctx, selected.OAuth.Client(), provider)
	if err != nil {
		return sessionAccount{}, err
	}
	factory := backend.newGoogleTasks
	if factory == nil {
		factory = googletasks.New
	}
	client, err := factory(ctx, googletasks.Options{
		APIBase: selected.OAuth.APIBase, Address: configured.Address,
		Account: configured.ID, ReadOnly: selected.ReadOnly,
		HTTP: authorization.HTTPClient(),
	})
	if err != nil {
		return sessionAccount{}, err
	}
	capabilities := client.TaskCapabilities()
	tasks, err := application.NewTaskService(
		backend.guard,
		client,
		application.TaskOptions{
			Capabilities: capabilities,
			Degradations: client.TaskDegradations(),
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderGoogleTasks,
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return leasedSessionAccount(sessionAccount{
		closers: []sessionCloser{client}, tasks: tasks,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Tasks: true, IncrementalSync: len(capabilities.SyncModes) != 0,
		},
	}), nil
}

func (backend *sessionBackend) tickTickAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	if configured.Tasks == nil || configured.Tasks.Provider != domain.ProviderTickTick ||
		configured.Tasks.TickTick == nil {
		return sessionAccount{}, errors.New("the TickTick task route settings are missing")
	}
	selected := configured.Tasks.TickTick
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderTickTick,
		oauthlocal.Services{Tasks: true, TaskWrite: !selected.ReadOnly},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	manager := backend.oauth
	if manager == nil {
		manager, err = oauthlocal.New(oauthlocal.Options{
			GoogleClientSecret: os.Getenv("CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET"),
		})
		if err != nil {
			return sessionAccount{}, err
		}
	}
	authorization, err := manager.AuthorizeConfidential(
		ctx, selected.OAuth.Client(), provider,
		func(ctx context.Context) ([]byte, error) {
			secret, err := backend.credentials.Resolve(ctx, selected.OAuth.ClientSecret)
			if err != nil {
				return nil, fmt.Errorf("resolve TickTick OAuth client credential: %w", err)
			}
			defer func() { _ = secret.Close() }()
			return secret.CopyBytes(), nil
		},
	)
	if err != nil {
		return sessionAccount{}, err
	}
	factory := backend.newTickTick
	if factory == nil {
		factory = ticktick.New
	}
	client, err := factory(ctx, ticktick.Options{
		APIBase: selected.OAuth.APIBase, Account: configured.ID,
		ReadOnly: selected.ReadOnly, HTTP: authorization.HTTPClient(),
	})
	if err != nil {
		return sessionAccount{}, err
	}
	capabilities := client.TaskCapabilities()
	tasks, err := application.NewTaskService(
		backend.guard,
		client,
		application.TaskOptions{
			Capabilities: capabilities,
			Degradations: client.TaskDegradations(),
			Provenance: domain.Provenance{
				AccountID: configured.ID, Provider: domain.ProviderTickTick,
			},
		},
	)
	if err != nil {
		return sessionAccount{}, errors.Join(err, client.Close())
	}
	return leasedSessionAccount(sessionAccount{
		closers: []sessionCloser{client}, tasks: tasks,
		captured: time.Now().UTC(),
		capabilities: domain.Capabilities{
			Tasks: true, IncrementalSync: len(capabilities.SyncModes) != 0,
		},
	}), nil
}

type graphServiceSelection struct {
	route                 config.OAuthRoute
	mail, calendar, tasks bool
	taskWrite             bool
	messaging             *config.TeamsGraphMessagingRoute
}

func configuredGraphServices(account config.Account) ([]graphServiceSelection, error) {
	selections := make([]graphServiceSelection, 0, 4)
	add := func(
		route *config.OAuthRoute,
		mail, calendar, tasks, taskWrite bool,
		messaging *config.TeamsGraphMessagingRoute,
	) {
		if route == nil {
			return
		}
		for index := range selections {
			selection := &selections[index]
			if oauthRoutesEqual(selection.route, *route) {
				selection.mail = selection.mail || mail
				selection.calendar = selection.calendar || calendar
				selection.tasks = selection.tasks || tasks
				selection.taskWrite = selection.taskWrite || taskWrite
				if messaging != nil {
					selection.messaging = messaging
				}
				return
			}
		}
		selections = append(selections, graphServiceSelection{
			route: *route, mail: mail, calendar: calendar,
			tasks: tasks, taskWrite: taskWrite, messaging: messaging,
		})
	}
	if account.Mail != nil && account.Mail.Provider == domain.ProviderMicrosoftGraph {
		if account.Mail.MicrosoftGraph == nil {
			return nil, errors.New("microsoft Graph mail route settings are missing")
		}
		add(account.Mail.MicrosoftGraph, true, false, false, false, nil)
	}
	if account.Calendar != nil && account.Calendar.Provider == domain.ProviderMicrosoftGraph {
		if account.Calendar.MicrosoftGraph == nil {
			return nil, errors.New("microsoft Graph calendar route settings are missing")
		}
		add(account.Calendar.MicrosoftGraph, false, true, false, false, nil)
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderMicrosoftGraph {
		if account.Tasks.MicrosoftGraph == nil {
			return nil, errors.New("the Microsoft To Do route settings are missing")
		}
		add(
			&account.Tasks.MicrosoftGraph.OAuth,
			false, false, true, !account.Tasks.MicrosoftGraph.ReadOnly, nil,
		)
	}
	if account.Messages != nil && account.Messages.TeamsGraph != nil {
		add(
			&account.Messages.TeamsGraph.OAuth,
			false, false, false, false,
			account.Messages.TeamsGraph,
		)
	}
	return selections, nil
}

func (backend *sessionBackend) nonOutlookAccount(
	ctx context.Context,
	configured config.Account,
) (sessionAccount, error) {
	var combined sessionAccount
	var googleMail *config.GoogleMailRoute
	if configured.Mail != nil &&
		configured.Mail.Provider == domain.ProviderGoogle {
		googleMail = configured.Mail.Google
	}
	var googleCalendar *config.OAuthRoute
	if configured.Calendar != nil &&
		configured.Calendar.Provider == domain.ProviderGoogle {
		googleCalendar = configured.Calendar.Google
	}
	if googleMail != nil || googleCalendar != nil {
		sharedGoogle := googleMail != nil &&
			googleCalendar != nil &&
			oauthClientsEqual(googleMail.Client(), googleCalendar.Client())
		if sharedGoogle {
			google, err := backend.googleAccount(
				ctx,
				configured,
				googleMail.Client(),
				googleMail,
				googleCalendar,
			)
			if err != nil {
				return sessionAccount{}, err
			}
			combined = mergeSessionAccounts(combined, google)
		} else {
			if googleMail != nil {
				google, err := backend.googleAccount(
					ctx,
					configured,
					googleMail.Client(),
					googleMail,
					nil,
				)
				if err != nil {
					return sessionAccount{}, err
				}
				combined = mergeSessionAccounts(combined, google)
			}
			if googleCalendar != nil {
				google, err := backend.googleAccount(
					ctx,
					configured,
					googleCalendar.Client(),
					nil,
					googleCalendar,
				)
				if err != nil {
					return sessionAccount{}, errors.Join(
						err,
						closeSessionAccount(combined),
					)
				}
				combined = mergeSessionAccounts(combined, google)
			}
		}
	}
	graphServices, err := configuredGraphServices(configured)
	if err != nil {
		return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
	}
	for _, selected := range graphServices {
		graph, err := backend.graphAPIAccount(
			ctx,
			configured,
			selected.route,
			selected.mail,
			selected.calendar,
			selected.tasks,
			selected.taskWrite,
			selected.messaging,
		)
		if err != nil {
			return sessionAccount{}, errors.Join(
				err,
				closeSessionAccount(combined),
			)
		}
		combined = mergeSessionAccounts(combined, graph)
	}
	if configured.Messages != nil && configured.Messages.Slack != nil {
		messages, err := backend.slackMessagingAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, messages)
	}
	if configured.Messages != nil && configured.Messages.Mattermost != nil {
		messages, err := backend.mattermostMessagingAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, messages)
	}
	if configured.Tasks != nil && configured.Tasks.Provider == domain.ProviderTodoist {
		tasks, err := backend.todoistAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, tasks)
	}
	if configured.Tasks != nil && configured.Tasks.Provider == domain.ProviderCalDAV {
		tasks, err := backend.calDAVTaskAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, tasks)
	}
	if configured.Tasks != nil && configured.Tasks.Provider == domain.ProviderGoogleTasks {
		tasks, err := backend.googleTaskAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, tasks)
	}
	if configured.Tasks != nil && configured.Tasks.Provider == domain.ProviderTickTick {
		tasks, err := backend.tickTickAccount(ctx, configured)
		if err != nil {
			return sessionAccount{}, errors.Join(err, closeSessionAccount(combined))
		}
		combined = mergeSessionAccounts(combined, tasks)
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
		case domain.ProviderGoogle:
			if configured.Mail.Google == nil {
				err = errors.New("google mail route settings are missing")
			}
		case domain.ProviderMicrosoftGraph:
			if configured.Mail.MicrosoftGraph == nil {
				err = errors.New("microsoft Graph mail route settings are missing")
			}
		case domain.ProviderCalDAV,
			domain.ProviderMicrosoftTasks,
			domain.ProviderTodoist,
			domain.ProviderGoogleTasks,
			domain.ProviderAppleReminders,
			domain.ProviderTickTick,
			domain.ProviderAnyDoMCP,
			domain.ProviderThings,
			domain.ProviderOmniFocus,
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
		case domain.ProviderGoogle:
			if configured.Calendar.Google == nil {
				err = errors.New("google calendar route settings are missing")
			}
		case domain.ProviderMicrosoftGraph:
			if configured.Calendar.MicrosoftGraph == nil {
				err = errors.New("microsoft Graph calendar route settings are missing")
			}
		case domain.ProviderJMAP,
			domain.ProviderIMAPSMTP,
			domain.ProviderMicrosoftTasks,
			domain.ProviderTodoist,
			domain.ProviderGoogleTasks,
			domain.ProviderAppleReminders,
			domain.ProviderTickTick,
			domain.ProviderAnyDoMCP,
			domain.ProviderThings,
			domain.ProviderOmniFocus,
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
		microsoftcloud.Equivalent(left.MicrosoftCloud, right.MicrosoftCloud) &&
		oauthClientsEqual(left.Client(), right.Client())
}

func oauthClientsEqual(left, right config.OAuthClient) bool {
	return left.ClientID == right.ClientID &&
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

func hasGoogleRoute(account config.Account) bool {
	return account.Mail != nil &&
		account.Mail.Provider == domain.ProviderGoogle ||
		account.Calendar != nil &&
			account.Calendar.Provider == domain.ProviderGoogle
}

func hasBrowserRoute(account config.Account) bool {
	return hasOutlookRoute(account)
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
	case domain.ProviderGoogle, domain.ProviderMicrosoftGraph:
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
	case domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3,
		domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
		domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
		domain.ProviderTickTick, domain.ProviderAnyDoMCP,
		domain.ProviderThings, domain.ProviderOmniFocus:
		return application.CalendarEffects{}
	default:
		return application.CalendarEffects{}
	}
}

func calDAVCalendarEffects(
	scheduling bool,
) application.CalendarEffects {
	if !scheduling {
		return providerCalendarEffects(domain.ProviderCalDAV)
	}
	return application.CalendarEffects{
		CreateAttendeeNotifications: true,
		UpdateAttendeeNotifications: true,
		CancelAttendeeNotifications: true,
		CancellationMode:            application.CalendarCancellationProviderManaged,
		CancellationDisposition:     application.CalendarDispositionCalendarObject,
	}
}

func mergeSessionAccounts(
	left sessionAccount,
	right sessionAccount,
) sessionAccount {
	merged := sessionAccount{
		closers:       append(append([]sessionCloser(nil), left.closers...), right.closers...),
		mail:          left.mail,
		calendar:      left.calendar,
		tasks:         left.tasks,
		messages:      left.messages,
		mailLease:     left.mailLease,
		calendarLease: left.calendarLease,
		taskLease:     left.taskLease,
		messageLease:  left.messageLease,
		captured:      left.captured,
		capabilities:  mergeCapabilities(left.capabilities, right.capabilities),
		degradations: append(
			append(
				[]domain.Degradation(nil),
				left.degradations...,
			),
			right.degradations...,
		),
		staticDegradations: append(
			append([]domain.Degradation(nil), left.staticDegradations...),
			right.staticDegradations...,
		),
	}
	if right.mail != nil {
		merged.mail = right.mail
		merged.mailLease = right.mailLease
	}
	if right.calendar != nil {
		merged.calendar = right.calendar
		merged.calendarLease = right.calendarLease
	}
	if right.tasks != nil {
		merged.tasks = right.tasks
		merged.taskLease = right.taskLease
	}
	if right.messages != nil {
		merged.messages = right.messages
		merged.messageLease = right.messageLease
	}
	if right.captured.After(merged.captured) {
		merged.captured = right.captured
	}
	return merged
}

func mergeCapabilities(
	left domain.Capabilities,
	right domain.Capabilities,
) domain.Capabilities {
	merged := domain.Capabilities{
		Mail:             left.Mail || right.Mail,
		Calendar:         left.Calendar || right.Calendar,
		Tasks:            left.Tasks || right.Tasks,
		Messages:         left.Messages || right.Messages,
		Folders:          left.Folders || right.Folders,
		Labels:           left.Labels || right.Labels,
		Push:             left.Push || right.Push,
		FreeBusy:         left.FreeBusy || right.FreeBusy,
		IncrementalSync:  left.IncrementalSync || right.IncrementalSync,
		ScheduledSend:    left.ScheduledSend || right.ScheduledSend,
		SharedMailboxes:  left.SharedMailboxes || right.SharedMailboxes,
		SharedCalendars:  left.SharedCalendars || right.SharedCalendars,
		AttachmentReads:  left.AttachmentReads || right.AttachmentReads,
		AttachmentWrites: left.AttachmentWrites || right.AttachmentWrites,
		DraftSend:        left.DraftSend || right.DraftSend,
		OnlineMeeting:    left.OnlineMeeting,
	}
	if right.OnlineMeeting != "" {
		merged.OnlineMeeting = right.OnlineMeeting
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
	capabilities.DraftSend = capabilities.Mail
	if capabilities.Calendar {
		capabilities.OnlineMeeting = "teams"
	}
	return capabilities
}

func closeSessionAccount(account sessionAccount) error {
	closeErrors := make([]error, 0, len(account.closers)+3)
	leases := account.leases()
	for _, lease := range leases {
		if lease.usage != nil {
			<-lease.usage.closeAfterActive()
		}
		closeErrors = append(closeErrors, lease.closeAfterDrain())
	}
	if len(leases) == 0 && account.usage != nil {
		<-account.usage.closeAfterActive()
	}
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
