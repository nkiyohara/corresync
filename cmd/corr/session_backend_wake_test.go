package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
	"github.com/nkiyohara/corresync/internal/provider/outlookweb"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
	"github.com/nkiyohara/corresync/internal/session"
)

// wakeMailPort is a provider whose reachability the test controls. Every
// method that is not overridden succeeds with an empty result.
type wakeMailPort struct {
	*authenticationFailureMailPort
	mu       sync.Mutex
	listErr  error
	sendErr  error
	gate     chan struct{}
	calledAt []time.Time
	lists    atomic.Int32
	sends    atomic.Int32
}

func newWakeMailPort() *wakeMailPort {
	return &wakeMailPort{authenticationFailureMailPort: &authenticationFailureMailPort{}}
}

func (port *wakeMailPort) failReads(err error) {
	port.mu.Lock()
	port.listErr = err
	port.mu.Unlock()
}

func (port *wakeMailPort) failSends(err error) {
	port.mu.Lock()
	port.sendErr = err
	port.mu.Unlock()
}

// freeze makes the next reads block until the returned release is called,
// modelling a poll that was in flight when the machine went to sleep.
func (port *wakeMailPort) freeze() (release func()) {
	gate := make(chan struct{})
	port.mu.Lock()
	port.gate = gate
	port.mu.Unlock()
	return func() {
		port.mu.Lock()
		port.gate = nil
		port.mu.Unlock()
		close(gate)
	}
}

func (port *wakeMailPort) ListMessages(
	context.Context,
	application.MailListInput,
) (application.MailPage, error) {
	port.lists.Add(1)
	port.mu.Lock()
	port.calledAt = append(port.calledAt, time.Now())
	gate, err := port.gate, port.listErr
	port.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return application.MailPage{IncludesLastItem: true}, err
}

func (port *wakeMailPort) SendMail(
	context.Context,
	application.MailSendInput,
) (application.MailSendResult, error) {
	port.sends.Add(1)
	port.mu.Lock()
	defer port.mu.Unlock()
	return application.MailSendResult{}, port.sendErr
}

type wakeTestAccount struct {
	alias    string
	id       domain.AccountID
	provider domain.ProviderID
	port     *wakeMailPort
	closer   *logoutTestCloser
}

func newWakeTestBackend(
	t *testing.T,
	accounts ...*wakeTestAccount,
) *sessionBackend {
	t.Helper()
	guard := daemonMCPGuard(t, policy.DefaultRules(), &lockedAudit{})
	configuration := config.Config{
		Accounts: make(map[string]config.Account, len(accounts)),
		Policy:   config.Policy{MaxRecipients: 20},
	}
	backend := &sessionBackend{
		app:              &runtime{stderr: &lockedBuffer{}, info: buildinfo.Info{Version: "test"}},
		guard:            guard,
		accounts:         make(map[domain.AccountID]sessionAccount, len(accounts)),
		previews:         make(map[string]sessionPreview),
		monitorCancel:    make(map[domain.AccountID]context.CancelFunc),
		monitorDone:      make(map[domain.AccountID]chan struct{}),
		monitorStarted:   make(map[domain.AccountID]bool),
		reauthentication: make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
		signedOutReason:  make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
	}
	for _, account := range accounts {
		configuration.Accounts[account.alias] = config.Account{
			ID:   account.id,
			Mail: &config.MailRoute{Provider: account.provider},
		}
		if account.port == nil {
			continue
		}
		mail, err := application.NewMailService(guard, account.port, application.MailOptions{
			MaxRecipients: 20,
			Provenance: domain.Provenance{
				AccountID: account.id, Provider: account.provider,
				MailboxID: "synthetic-mailbox",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		account.closer = &logoutTestCloser{}
		backend.accounts[account.id] = leasedSessionAccount(sessionAccount{
			closers: []sessionCloser{account.closer}, mail: mail,
			captured:     time.Unix(1, 0).UTC(),
			capabilities: domain.Capabilities{Mail: true},
		})
	}
	backend.configuration = configuration
	return backend
}

type lockedAudit struct {
	mu     sync.Mutex
	events int
}

func (recorder *lockedAudit) Record(context.Context, application.AuditEvent) error {
	recorder.mu.Lock()
	recorder.events++
	recorder.mu.Unlock()
	return nil
}

func wakeInboxInput(account domain.AccountID) application.MailListInput {
	return application.MailListInput{
		Account: account,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	}
}

var wakeCaller = domain.Caller{Surface: "mcp", Instance: "synthetic-client"}

func mailServiceState(
	t *testing.T,
	backend *sessionBackend,
	alias string,
) application.ServiceAuthenticationStatus {
	t.Helper()
	status, err := backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range status.Accounts {
		if account.Alias == alias && account.Services.Mail != nil {
			return *account.Services.Mail
		}
	}
	t.Fatalf("no mail status for %q in %+v", alias, status)
	return application.ServiceAuthenticationStatus{}
}

// Each failure is what a read observes right after wake, before the network
// is back: the adapter has already spent its own bounded retries.
func transientWakeFailures() map[string]error {
	return map[string]error{
		"dial refused": fmt.Errorf("list messages: %w", &net.OpError{
			Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED,
		}),
		"DNS unavailable": fmt.Errorf("list messages: %w", &net.DNSError{
			Err: "no such host", Name: "mail.example.invalid", IsTemporary: true,
		}),
		"request timed out": fmt.Errorf("list messages: %w", context.DeadlineExceeded),
		"API circuit open":  &restapi.CircuitOpenError{RetryAfter: 5 * time.Second},
		"API throttled": &restapi.CircuitOpenError{
			RetryAfter: time.Minute, Throttled: true,
		},
		"Outlook Web unavailable": &outlookweb.HTTPError{
			StatusCode: http.StatusServiceUnavailable,
		},
	}
}

func TestSessionBackendTransientFailureAfterWakeKeepsSessionAndRecoversWithoutRestart(
	t *testing.T,
) {
	t.Parallel()
	for name, failure := range transientWakeFailures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			work := &wakeTestAccount{
				alias: "work", id: "acc_00000000000000000000000000000001",
				provider: domain.ProviderJMAP, port: newWakeMailPort(),
			}
			backend := newWakeTestBackend(t, work)

			work.port.failReads(failure)
			_, err := backend.ListMail(t.Context(), wakeInboxInput(work.id), wakeCaller)
			if err == nil || !errors.Is(err, failure) {
				t.Fatalf("ListMail() while offline = %v", err)
			}
			if _, ok := application.AuthenticationActionFromError(err); ok {
				t.Fatalf("transient failure asked the user to sign in: %v", err)
			}
			// The session layer adds no retry of its own on top of the
			// adapter's bounded read retry.
			if work.port.lists.Load() != 1 || work.closer.calls.Load() != 0 {
				t.Fatalf(
					"provider calls = %d, closes = %d",
					work.port.lists.Load(), work.closer.calls.Load(),
				)
			}
			if state := mailServiceState(t, backend, "work"); state.State !=
				application.AuthenticationStateAuthenticated || state.Reason != "" {
				t.Fatalf("mail state after transient failure = %+v", state)
			}

			// The network returns; the same daemon and lease serve the read.
			work.port.failReads(nil)
			if _, err := backend.ListMail(
				t.Context(), wakeInboxInput(work.id), wakeCaller,
			); err != nil {
				t.Fatalf("ListMail() after reconnect = %v", err)
			}
			if work.port.lists.Load() != 2 || work.closer.calls.Load() != 0 {
				t.Fatalf(
					"provider calls = %d, closes = %d",
					work.port.lists.Load(), work.closer.calls.Load(),
				)
			}
		})
	}
}

func TestSessionBackendExpiryAfterWakeIsAccountLocalAndNotRetried(t *testing.T) {
	t.Parallel()
	work := &wakeTestAccount{
		alias: "work", id: "acc_00000000000000000000000000000001",
		provider: domain.ProviderGoogle, port: newWakeMailPort(),
	}
	home := &wakeTestAccount{
		alias: "home", id: "acc_00000000000000000000000000000002",
		provider: domain.ProviderJMAP, port: newWakeMailPort(),
	}
	backend := newWakeTestBackend(t, work, home)

	// After wake, work's grant was revoked while home is still offline.
	work.port.failReads(application.NewProviderAuthenticationFailure(
		application.AuthenticationReasonGrantRevoked,
		errors.New("synthetic private refresh response"),
	))
	home.port.failReads(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH})

	var wait sync.WaitGroup
	results := make(map[string]error, 2)
	var resultsMu sync.Mutex
	for _, account := range []*wakeTestAccount{work, home} {
		wait.Go(func() {
			_, err := backend.ListMail(t.Context(), wakeInboxInput(account.id), wakeCaller)
			resultsMu.Lock()
			results[account.alias] = err
			resultsMu.Unlock()
		})
	}
	wait.Wait()

	action, ok := application.AuthenticationActionFromError(results["work"])
	if !ok || action.Alias != "work" ||
		action.Reason != application.AuthenticationReasonGrantRevoked ||
		action.NextAction.Command.Executable != "corr" {
		t.Fatalf("work after wake = %v", results["work"])
	}
	if _, ok := application.AuthenticationActionFromError(results["home"]); ok ||
		results["home"] == nil {
		t.Fatalf("home after wake = %v", results["home"])
	}
	if work.closer.calls.Load() != 1 || home.closer.calls.Load() != 0 {
		t.Fatalf(
			"closes: work = %d, home = %d",
			work.closer.calls.Load(), home.closer.calls.Load(),
		)
	}

	// The expired account is not retried against the provider again until a
	// human signs in; the offline account recovers when the network returns.
	home.port.failReads(nil)
	for range 3 {
		_, err := backend.ListMail(t.Context(), wakeInboxInput(work.id), wakeCaller)
		if _, ok := application.AuthenticationActionFromError(err); !ok {
			t.Fatalf("work repeat = %v", err)
		}
	}
	if _, err := backend.ListMail(t.Context(), wakeInboxInput(home.id), wakeCaller); err != nil {
		t.Fatalf("home after reconnect = %v", err)
	}
	if work.port.lists.Load() != 1 || home.port.lists.Load() != 2 {
		t.Fatalf(
			"provider calls: work = %d, home = %d",
			work.port.lists.Load(), home.port.lists.Load(),
		)
	}
	if state := mailServiceState(t, backend, "work"); state.State !=
		application.AuthenticationStateReauthenticationNeeded ||
		state.Reason != application.AuthenticationReasonGrantRevoked {
		t.Fatalf("work status = %+v", state)
	}
	if state := mailServiceState(t, backend, "home"); state.State !=
		application.AuthenticationStateAuthenticated {
		t.Fatalf("home status = %+v", state)
	}
}

// wakeBrowserHandle is a browser-owned session whose Chromium process can die
// while the machine sleeps. Apply never waits on the process: it reads the
// last snapshot or reports that the owner is gone.
type wakeBrowserHandle struct {
	dead    atomic.Bool
	applies atomic.Int32
	closes  atomic.Int32
}

func (handle *wakeBrowserHandle) WaitForSession(context.Context) (session.Credentials, error) {
	return session.Credentials{}, errors.New("not used")
}

func (handle *wakeBrowserHandle) Apply(request *http.Request) error {
	handle.applies.Add(1)
	if handle.dead.Load() {
		return browser.ErrBrowserSessionUnavailable
	}
	request.Header.Set("Authorization", "Bearer synthetic-browser-token")
	return nil
}

func (handle *wakeBrowserHandle) Close() error {
	handle.closes.Add(1)
	return nil
}

func TestSessionBackendBrowserOwnerLostDuringSleepRequiresSignInForThatAccountOnly(
	t *testing.T,
) {
	t.Parallel()
	const workID domain.AccountID = "acc_00000000000000000000000000000001"
	home := &wakeTestAccount{
		alias: "home", id: "acc_00000000000000000000000000000002",
		provider: domain.ProviderJMAP, port: newWakeMailPort(),
	}
	backend := newWakeTestBackend(t, home)
	workConfiguration := config.Account{
		ID: workID,
		Mail: &config.MailRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &config.OutlookWebRoute{
				Origin: "https://outlook.example.invalid",
			},
		},
	}
	backend.configuration.Accounts["work"] = workConfiguration
	handle := &wakeBrowserHandle{}
	work, err := backend.outlookAccount(workConfiguration, handle, session.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	backend.accounts[workID] = work

	// Chromium exited while the machine slept. The next read fails before any
	// request leaves the process and names the exact sign-in action.
	handle.dead.Store(true)
	_, err = backend.ListMail(t.Context(), wakeInboxInput(workID), wakeCaller)
	action, ok := application.AuthenticationActionFromError(err)
	if !ok || action.Alias != "work" ||
		action.Reason != application.AuthenticationReasonInteractionRequired ||
		action.Retry.Automatic {
		t.Fatalf("work after browser loss = %v", err)
	}
	if handle.closes.Load() != 1 || handle.applies.Load() != 1 {
		t.Fatalf("browser closes = %d, applies = %d", handle.closes.Load(), handle.applies.Load())
	}
	// A further read does not keep probing the dead owner.
	if _, err := backend.ListMail(t.Context(), wakeInboxInput(workID), wakeCaller); err == nil {
		t.Fatal("stale browser session served a read")
	}
	if handle.applies.Load() != 1 {
		t.Fatalf("dead browser owner was retried: applies = %d", handle.applies.Load())
	}
	if _, err := backend.ListMail(t.Context(), wakeInboxInput(home.id), wakeCaller); err != nil {
		t.Fatalf("other account after browser loss = %v", err)
	}
	if state := mailServiceState(t, backend, "home"); state.State !=
		application.AuthenticationStateAuthenticated {
		t.Fatalf("home status = %+v", state)
	}
}

func TestSessionBackendWriteInterruptedByNetworkLossIsNeverReplayed(t *testing.T) {
	t.Parallel()
	work := &wakeTestAccount{
		alias: "work", id: "acc_00000000000000000000000000000001",
		provider: domain.ProviderJMAP, port: newWakeMailPort(),
	}
	backend := newWakeTestBackend(t, work)
	input := application.MailSendInput{
		Account: work.id, To: []string{"alice@example.invalid"},
		Subject: "Synthetic send", Body: "Synthetic body",
	}
	preview, err := backend.SendMail(t.Context(), input, wakeCaller)
	if err != nil || preview.Preview == nil {
		t.Fatalf("SendMail() preview = %+v, %v", preview, err)
	}

	// The connection drops after the request was dispatched.
	work.port.failSends(fmt.Errorf(
		"%w: synthetic connection reset after dispatch",
		application.ErrWriteOutcomeUnknown,
	))
	_, err = backend.CommitMailSend(t.Context(), preview.Preview.Token, wakeCaller)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitMailSend() interrupted = %v", err)
	}
	if _, ok := application.AuthenticationActionFromError(err); ok {
		t.Fatalf("unknown write outcome was relabeled as sign-in: %v", err)
	}

	// The network returns. Neither the daemon nor a repeated commit of the
	// same approval sends the message again.
	work.port.failSends(nil)
	if _, err := backend.CommitMailSend(
		t.Context(), preview.Preview.Token, wakeCaller,
	); err == nil {
		t.Fatal("interrupted approval was accepted a second time")
	}
	if work.port.sends.Load() != 1 || work.closer.calls.Load() != 0 {
		t.Fatalf("sends = %d, closes = %d", work.port.sends.Load(), work.closer.calls.Load())
	}
	if state := mailServiceState(t, backend, "work"); state.State !=
		application.AuthenticationStateAuthenticated {
		t.Fatalf("mail status after unknown outcome = %+v", state)
	}

	// Only a fresh preview and approval can send again.
	fresh, err := backend.SendMail(t.Context(), input, wakeCaller)
	if err != nil || fresh.Preview == nil || fresh.Preview.Token == preview.Preview.Token {
		t.Fatalf("fresh preview = %+v, %v", fresh, err)
	}
	if _, err := backend.CommitMailSend(t.Context(), fresh.Preview.Token, wakeCaller); err != nil {
		t.Fatalf("fresh commit = %v", err)
	}
	if work.port.sends.Load() != 2 {
		t.Fatalf("sends after fresh approval = %d", work.port.sends.Load())
	}
}
