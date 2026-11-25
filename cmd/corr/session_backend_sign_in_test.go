package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
	"github.com/nkiyohara/corresync/internal/session"
)

var (
	signInMCPCaller   = domain.Caller{Surface: "mcp", Instance: "process-41"}
	signInOtherCaller = domain.Caller{Surface: "mcp", Instance: "process-42"}
	signInCLICaller   = domain.Caller{Surface: "cli", Instance: "process-7"}
)

// gatedBrowserHandle completes sign-in only when the test releases it, as a
// person finishing the visible browser flow would.
type gatedBrowserHandle struct {
	release chan struct{}
	closed  atomic.Bool
}

func (handle *gatedBrowserHandle) WaitForSession(ctx context.Context) (session.Credentials, error) {
	select {
	case <-handle.release:
		return session.Credentials{}, nil
	case <-ctx.Done():
		return session.Credentials{}, ctx.Err()
	}
}

func (*gatedBrowserHandle) Apply(*http.Request) error { return nil }

func (handle *gatedBrowserHandle) Close() error {
	handle.closed.Store(true)
	return nil
}

type signInAudit struct {
	mu     sync.Mutex
	events []application.AuditEvent
}

func (recorder *signInAudit) Record(_ context.Context, event application.AuditEvent) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

func (recorder *signInAudit) reasons() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	reasons := make([]string, 0, len(recorder.events))
	for _, event := range recorder.events {
		reasons = append(reasons, event.Reason)
	}
	return reasons
}

type signInLaunches struct {
	mu      sync.Mutex
	options []browser.Options
}

func (launches *signInLaunches) count() int {
	launches.mu.Lock()
	defer launches.mu.Unlock()
	return len(launches.options)
}

type signInFixture struct {
	backend   *sessionBackend
	account   domain.AccountID
	audit     *signInAudit
	launches  *signInLaunches
	clock     *signInClock
	noDisplay *atomic.Bool
	stateDir  string
	handle    browserHandle
}

type signInClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *signInClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *signInClock) advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

// newSignInFixture builds a daemon backend for the Outlook default account
// whose current routes were already verified by a CLI-completed sign-in.
func newSignInFixture(t *testing.T, handle browserHandle, loginTimeout time.Duration) signInFixture {
	t.Helper()
	fixture := newUnverifiedSignInFixture(t, handle, loginTimeout)
	fixture.verify()
	return fixture
}

func newUnverifiedSignInFixture(t *testing.T, handle browserHandle, loginTimeout time.Duration) signInFixture {
	t.Helper()
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configuration.Browser.LoginTimeout = config.Duration(loginTimeout)
	fixture := signInFixture{
		audit: &signInAudit{}, launches: &signInLaunches{},
		clock:     &signInClock{now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)},
		noDisplay: &atomic.Bool{}, stateDir: filepath.Join(t.TempDir(), "sign-in"), handle: handle,
	}
	return fixture.restart(t, configuration)
}

// restart builds a fresh daemon backend over the same persisted sign-in
// state, as a daemon restart after a configuration commit does.
func (fixture signInFixture) restart(t *testing.T, configuration config.Config) signInFixture {
	t.Helper()
	lifecycle, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	launches, handle := fixture.launches, fixture.handle
	backend := &sessionBackend{
		app: &runtime{
			stderr: io.Discard,
			launch: func(_ context.Context, options browser.Options) (browserHandle, error) {
				launches.mu.Lock()
				defer launches.mu.Unlock()
				launches.options = append(launches.options, options)
				return handle, nil
			},
		},
		configuration: configuration, lifecycle: lifecycle, cancel: cancel,
		guard:            daemonMCPGuard(t, policy.DefaultRules(), &daemonMCPAudit{}),
		accounts:         make(map[domain.AccountID]sessionAccount),
		previews:         make(map[string]sessionPreview),
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
		monitorCancel:    make(map[domain.AccountID]context.CancelFunc),
		monitorDone:      make(map[domain.AccountID]chan struct{}),
		monitorStarted:   make(map[domain.AccountID]bool),
	}
	noDisplay := fixture.noDisplay
	coordinator := newSignInCoordinator()
	coordinator.now = fixture.clock.Now
	coordinator.audit = fixture.audit
	coordinator.files = signInStateFiles{dir: fixture.stateDir}
	coordinator.display = func() error {
		if noDisplay.Load() {
			return browser.ErrGraphicalSessionUnavailable
		}
		return nil
	}
	backend.mu.Lock()
	backend.signIn = coordinator
	backend.loadSignInStateLocked()
	backend.mu.Unlock()
	t.Cleanup(func() {
		cancel()
		backend.active.Wait()
		for _, account := range backend.accounts {
			_ = closeSessionAccount(account)
		}
	})
	fixture.backend = backend
	if account, exists := configuration.Accounts["work"]; exists {
		fixture.account = account.ID
	}
	return fixture
}

// verify records the current routes as verified by a CLI-completed sign-in,
// as Login does when a person signs in from the CLI.
func (fixture signInFixture) verify() {
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	fixture.backend.recordCLISignInLocked(fixture.account)
}

func (fixture signInFixture) request(t *testing.T, caller domain.Caller) application.AccountSignInRequest {
	t.Helper()
	request, err := fixture.backend.RequestAccountSignIn(t.Context(), fixture.account, caller)
	if err != nil {
		t.Fatalf("RequestAccountSignIn() error = %v", err)
	}
	if err := request.Validate(fixture.account); err != nil {
		t.Fatalf("invalid request result: %v", err)
	}
	return request
}

func (fixture signInFixture) approval(t *testing.T) application.AccountSignInApproval {
	t.Helper()
	request := fixture.request(t, signInMCPCaller)
	if request.Approval == nil {
		t.Fatalf("request result = %+v, want an approval prompt", request.Result)
	}
	return *request.Approval
}

func (fixture signInFixture) resolve(
	t *testing.T,
	ticket string,
	decision application.AccountSignInDecision,
) application.AccountSignInResult {
	t.Helper()
	result, err := fixture.backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
		Account: fixture.account, Ticket: ticket, Decision: decision,
	}, signInMCPCaller)
	if err != nil {
		t.Fatalf("ResolveAccountSignIn() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("invalid sign-in result: %v", err)
	}
	return result
}

func (fixture signInFixture) status(t *testing.T) application.SessionStatus {
	t.Helper()
	result, err := fixture.backend.SessionStatus(t.Context(), signInMCPCaller)
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	for _, status := range result.Accounts {
		if status.Account == fixture.account {
			return status
		}
	}
	t.Fatal("account missing from status")
	return application.SessionStatus{}
}

func (fixture signInFixture) waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (fixture signInFixture) running() bool {
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	return fixture.backend.interactiveLoginPendingLocked(fixture.account)
}

func TestApprovedMCPSignInStartsVisibleLoginAndFinishesWithoutAnotherCall(t *testing.T) {
	handle := &gatedBrowserHandle{release: make(chan struct{})}
	fixture := newSignInFixture(t, handle, time.Minute)

	approval := fixture.approval(t)
	if approval.Alias != "work" || len(approval.Routes) != 2 ||
		approval.Routes[0] != (application.AccountSignInRoute{
			Service: "mail", Provider: domain.ProviderMicrosoftOWA,
			Kind: application.AccountSignInDedicatedBrowser, Host: "outlook.cloud.microsoft",
		}) {
		t.Fatalf("approval = %+v", approval)
	}
	if fixture.launches.count() != 0 {
		t.Fatal("a request launched a browser before approval")
	}

	result := fixture.resolve(t, approval.Ticket, application.AccountSignInDecisionApprovedByUser)
	if result.Status != application.AccountSignInStarted || result.ExpiresAt == nil ||
		!result.ExpiresAt.Equal(fixture.clock.Now().Add(time.Minute)) || result.NextAction != nil {
		t.Fatalf("approved result = %+v", result)
	}
	fixture.waitUntil(t, func() bool { return fixture.launches.count() == 1 })
	options := fixture.launches.options[0]
	if options.Headless || options.Origin != "https://outlook.cloud.microsoft" || options.ProfileDir == "" {
		t.Fatalf("approved sign-in did not open the visible dedicated browser: %+v", options)
	}

	status := fixture.status(t)
	if status.State != "pending" || status.Services.Mail.State != application.AuthenticationStatePending ||
		status.Services.Mail.Action.Code != application.AuthenticationCodePending {
		t.Fatalf("unfinished sign-in is not visible in status: %+v", status)
	}
	again := fixture.request(t, signInOtherCaller)
	if again.Result == nil || again.Result.Status != application.AccountSignInInProgress ||
		again.Result.Reason != application.AccountSignInReasonSignInRunning {
		t.Fatalf("second request while running = %+v", again)
	}

	// The person finishes in the browser. No further tool call is made.
	close(handle.release)
	fixture.waitUntil(t, func() bool { return fixture.status(t).Authenticated })
	fixture.waitUntil(t, func() bool { return !fixture.running() })
	if fixture.launches.count() != 1 {
		t.Fatalf("launches = %d, want exactly one", fixture.launches.count())
	}
	if done := fixture.request(t, signInMCPCaller); done.Result == nil ||
		done.Result.Status != application.AccountSignInAlreadySignedIn {
		t.Fatalf("request after completion = %+v", done)
	}
	fixture.waitUntil(t, func() bool { return len(fixture.audit.reasons()) == 9 })
	want := []string{
		"sign_in_requested", "approval_requested",
		"sign_in_requested", "sign_in_running",
		"user_approved", "sign_in_started", "sign_in_completed",
		"sign_in_requested", "already_signed_in",
	}
	got := fixture.audit.reasons()
	// The completion is recorded by the daemon-owned goroutine, so order it
	// relative to the concurrent second request only by presence.
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("audit reasons = %v, want %v", fixture.audit.reasons(), want)
	}
	for _, event := range fixture.audit.events {
		if event.Operation.Name != "account.sign_in" || event.Operation.Account != fixture.account ||
			event.Caller.Surface != "mcp" {
			t.Fatalf("audit event is not content-free account metadata: %+v", event)
		}
	}
}

func TestMCPSignInAnswersOtherThanApprovalStartNothing(t *testing.T) {
	tests := []struct {
		decision application.AccountSignInDecision
		status   application.AccountSignInStatus
		reason   application.AccountSignInReason
		cooldown bool
	}{
		{application.AccountSignInDecisionDeclinedByUser, application.AccountSignInDeclined, application.AccountSignInReasonUserDeclined, true},
		{application.AccountSignInDecisionCancelledByUser, application.AccountSignInDeclined, application.AccountSignInReasonUserCancelled, true},
		// A prompt that was shown and failed (client error, call cancelled or
		// timed out) cools down like a refusal; a client that could not show
		// one does not.
		{application.AccountSignInDecisionElicitationFailed, application.AccountSignInUnavailable, application.AccountSignInReasonElicitationFailed, true},
		{application.AccountSignInDecisionElicitationUnsupported, application.AccountSignInUnavailable, application.AccountSignInReasonElicitationUnsupported, false},
	}
	for _, test := range tests {
		t.Run(string(test.decision), func(t *testing.T) {
			fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
			approval := fixture.approval(t)
			result := fixture.resolve(t, approval.Ticket, test.decision)
			if result.Status != test.status || result.Reason != test.reason || result.NextAction == nil ||
				!slices.Equal(result.NextAction.Command.Args, []string{"auth", "login", "--account", "work"}) {
				t.Fatalf("result = %+v", result)
			}
			if fixture.launches.count() != 0 || fixture.running() {
				t.Fatal("a non-approval answer started sign-in")
			}
			next := fixture.request(t, signInMCPCaller)
			if test.cooldown {
				wantStatus, wantReason := application.AccountSignInDeclined, application.AccountSignInReasonRecentlyDeclined
				if test.status == application.AccountSignInUnavailable {
					wantStatus, wantReason = application.AccountSignInUnavailable, application.AccountSignInReasonRecentlyUnfinished
				}
				if next.Result == nil || next.Result.Status != wantStatus || next.Result.Reason != wantReason {
					t.Fatalf("request during cool-down = %+v", next)
				}
				fixture.clock.advance(application.AccountSignInCooldown - time.Second)
				if still := fixture.request(t, signInOtherCaller); still.Approval != nil {
					t.Fatal("cool-down ended early")
				}
				fixture.clock.advance(time.Second)
				if after := fixture.request(t, signInMCPCaller); after.Approval == nil {
					t.Fatalf("request after cool-down = %+v", after.Result)
				}
			} else if next.Approval == nil {
				t.Fatalf("a non-human failure imposed a cool-down: %+v", next.Result)
			}
			if fixture.launches.count() != 0 {
				t.Fatal("browser launched")
			}
			// A consumed ticket cannot be reused to approve afterwards.
			if _, err := fixture.backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
				Account: fixture.account, Ticket: approval.Ticket,
				Decision: application.AccountSignInDecisionApprovedByUser,
			}, signInMCPCaller); !errors.Is(err, errInvalidSignInApproval) {
				t.Fatalf("reused ticket error = %v", err)
			}
		})
	}
}

func TestMCPSignInWithoutDisplayReturnsCommandAndStartsNothing(t *testing.T) {
	fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	fixture.noDisplay.Store(true)
	request := fixture.request(t, signInMCPCaller)
	if request.Result == nil || request.Result.Status != application.AccountSignInUnavailable ||
		request.Result.Reason != application.AccountSignInReasonDisplayUnavailable ||
		request.Result.NextAction == nil || request.Result.NextAction.Command.Args[3] != "work" {
		t.Fatalf("request without display = %+v", request)
	}
	if fixture.launches.count() != 0 || len(fixture.backend.signIn.approvals) != 0 {
		t.Fatal("a display-less host reserved a prompt or launched a browser")
	}

	// A display that disappears between prompt and answer also starts nothing.
	fixture.noDisplay.Store(false)
	approval := fixture.approval(t)
	fixture.noDisplay.Store(true)
	result := fixture.resolve(t, approval.Ticket, application.AccountSignInDecisionApprovedByUser)
	if result.Status != application.AccountSignInUnavailable ||
		result.Reason != application.AccountSignInReasonDisplayUnavailable || fixture.launches.count() != 0 {
		t.Fatalf("approved without display = %+v", result)
	}
}

func TestMCPSignInAllowsOnePendingApprovalPerAccount(t *testing.T) {
	fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	first := fixture.approval(t)
	for _, caller := range []domain.Caller{signInMCPCaller, signInOtherCaller} {
		second := fixture.request(t, caller)
		if second.Result == nil || second.Result.Status != application.AccountSignInInProgress ||
			second.Result.Reason != application.AccountSignInReasonApprovalPending {
			t.Fatalf("second request = %+v", second)
		}
	}
	// Another MCP caller cannot answer a prompt reserved for the first.
	if _, err := fixture.backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
		Account: fixture.account, Ticket: first.Ticket,
		Decision: application.AccountSignInDecisionApprovedByUser,
	}, signInOtherCaller); !errors.Is(err, errInvalidSignInApproval) {
		t.Fatalf("cross-caller resolution error = %v", err)
	}

	// An unanswered prompt expires and cools the account down.
	fixture.clock.advance(application.AccountSignInApprovalTTL)
	if cooled := fixture.request(t, signInMCPCaller); cooled.Result == nil ||
		cooled.Result.Reason != application.AccountSignInReasonRecentlyUnfinished {
		t.Fatalf("request after an unanswered prompt = %+v", cooled)
	}
	fixture.clock.advance(application.AccountSignInCooldown)
	renewed := fixture.approval(t)
	if renewed.Ticket == first.Ticket {
		t.Fatal("expired approval ticket was reused")
	}
	if _, err := fixture.backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
		Account: fixture.account, Ticket: first.Ticket,
		Decision: application.AccountSignInDecisionApprovedByUser,
	}, signInMCPCaller); !errors.Is(err, errInvalidSignInApproval) {
		t.Fatalf("expired ticket error = %v", err)
	}
	if !slices.Contains(fixture.audit.reasons(), "approval_expired") {
		t.Fatalf("approval expiry was not audited: %v", fixture.audit.reasons())
	}
	// An answer that arrives after its own expiry starts nothing.
	fixture.clock.advance(application.AccountSignInApprovalTTL)
	late := fixture.resolve(t, renewed.Ticket, application.AccountSignInDecisionApprovedByUser)
	if late.Status != application.AccountSignInUnavailable ||
		late.Reason != application.AccountSignInReasonApprovalExpired || fixture.launches.count() != 0 {
		t.Fatalf("late approval = %+v", late)
	}
}

func TestMCPSignInReportsInProgressWhileCLISignInRuns(t *testing.T) {
	handle := &gatedBrowserHandle{release: make(chan struct{})}
	fixture := newSignInFixture(t, handle, time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := fixture.backend.Login(context.Background(), fixture.account, signInCLICaller)
		done <- err
	}()
	fixture.waitUntil(t, func() bool { return fixture.launches.count() == 1 })
	request := fixture.request(t, signInMCPCaller)
	if request.Result == nil || request.Result.Status != application.AccountSignInInProgress ||
		request.Result.Reason != application.AccountSignInReasonSignInRunning {
		t.Fatalf("request during CLI sign-in = %+v", request)
	}
	if fixture.status(t).State != "pending" {
		t.Fatal("CLI sign-in is not visible as pending")
	}
	close(handle.release)
	if err := <-done; err != nil {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if fixture.running() || fixture.launches.count() != 1 {
		t.Fatal("CLI sign-in state was not cleared")
	}
}

func TestMCPStartedSignInExpiryClosesBrowserAndClearsState(t *testing.T) {
	handle := &signedOutBrowserHandle{}
	fixture := newSignInFixture(t, handle, 20*time.Millisecond)
	approval := fixture.approval(t)
	if result := fixture.resolve(t, approval.Ticket, application.AccountSignInDecisionApprovedByUser); result.Status != application.AccountSignInStarted {
		t.Fatalf("result = %+v", result)
	}
	fixture.waitUntil(t, func() bool { return slices.Contains(fixture.audit.reasons(), "sign_in_expired") })
	fixture.waitUntil(t, func() bool { return !fixture.running() })
	fixture.backend.mu.Lock()
	closed := handle.closed
	fixture.backend.mu.Unlock()
	if !closed {
		t.Fatal("expired sign-in left its browser open")
	}
	status := fixture.status(t)
	if status.State != "signed_out" || status.Authenticated ||
		status.Services.Mail.State != application.AuthenticationStateSignedOut {
		t.Fatalf("expired sign-in left state behind: %+v", status)
	}
	next := fixture.request(t, signInMCPCaller)
	if next.Result == nil || next.Result.Status != application.AccountSignInUnavailable ||
		next.Result.Reason != application.AccountSignInReasonRecentlyUnfinished {
		t.Fatalf("request right after an unfinished sign-in = %+v", next)
	}
}

func TestMCPCallerCannotStartSignInThroughAnyOtherPath(t *testing.T) {
	fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	backend := fixture.backend
	if _, err := backend.Login(t.Context(), fixture.account, signInMCPCaller); err == nil {
		t.Fatal("ordinary login accepted an MCP caller")
	}
	if _, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: fixture.account}, signInMCPCaller); err == nil {
		t.Fatal("terminal login accepted an MCP caller")
	}
	if _, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: fixture.account, Graphical: true}, signInMCPCaller); err == nil {
		t.Fatal("graphical login accepted an MCP caller")
	}
	for _, clearBrowser := range []bool{false, true} {
		if _, err := backend.LogoutWithOptions(t.Context(), daemonapi.LogoutInput{
			Account: fixture.account, ClearBrowser: clearBrowser,
		}, signInMCPCaller); err == nil {
			t.Fatalf("logout (clearBrowser=%t) accepted an MCP caller", clearBrowser)
		}
	}
	if _, err := backend.RequestAccountSignIn(t.Context(), fixture.account, signInCLICaller); err == nil {
		t.Fatal("the MCP request method accepted a CLI caller")
	}
	forged := "sit1_" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, caller := range []domain.Caller{signInMCPCaller, signInCLICaller} {
		if _, err := backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
			Account: fixture.account, Ticket: forged,
			Decision: application.AccountSignInDecisionApprovedByUser,
		}, caller); err == nil {
			t.Fatalf("a forged approval from %s was accepted", caller.Surface)
		}
	}
	approval := fixture.approval(t)
	if _, err := backend.ResolveAccountSignIn(t.Context(), application.AccountSignInResolution{
		Account: fixture.account, Ticket: approval.Ticket,
		Decision: application.AccountSignInDecisionApprovedByUser,
	}, signInCLICaller); err == nil {
		t.Fatal("a CLI caller consumed an MCP approval")
	}
	if _, err := backend.RequestAccountSignIn(
		t.Context(), "acc_ffffffffffffffffffffffffffffffff", signInMCPCaller,
	); err == nil {
		t.Fatal("an unconfigured account reserved a prompt")
	}
	if fixture.launches.count() != 0 || fixture.running() {
		t.Fatal("a refused path started sign-in")
	}
}

func withOutlookOrigin(configuration config.Config, alias, origin string) config.Config {
	account := configuration.Accounts[alias]
	mail, calendar := *account.Mail, *account.Calendar
	mail.OutlookWeb = &config.OutlookWebRoute{Origin: origin}
	calendar.OutlookWeb = &config.OutlookWebRoute{Origin: origin}
	account.Mail, account.Calendar = &mail, &calendar
	configuration.Accounts[alias] = account
	return configuration
}

func cloneSignInConfig(configuration config.Config) config.Config {
	clone := configuration
	clone.Accounts = make(map[string]config.Account, len(configuration.Accounts))
	for alias, account := range configuration.Accounts {
		clone.Accounts[alias] = account
	}
	return clone
}

func TestMCPSignInRequiresACLISignInWithTheCurrentRoutes(t *testing.T) {
	released := make(chan struct{})
	close(released)
	fixture := newUnverifiedSignInFixture(t, &gatedBrowserHandle{release: released}, time.Minute)
	request := fixture.request(t, signInMCPCaller)
	if request.Result == nil || request.Result.Status != application.AccountSignInUnavailable ||
		request.Result.Reason != application.AccountSignInReasonCLISignInRequired ||
		request.Result.NextAction == nil || request.Result.NextAction.Command.Args[3] != "work" {
		t.Fatalf("request before any CLI sign-in = %+v", request)
	}
	if len(fixture.backend.signIn.approvals) != 0 || fixture.launches.count() != 0 {
		t.Fatal("an unverified account reserved a prompt or opened a browser")
	}

	// A person signs in once from the CLI; that enables the tool, also after
	// a daemon restart.
	if _, err := fixture.backend.Login(t.Context(), fixture.account, signInCLICaller); err != nil {
		t.Fatalf("CLI Login() error = %v", err)
	}
	original := fixture.backend.configuration
	restarted := fixture.restart(t, cloneSignInConfig(original))
	if approval := restarted.approval(t); approval.Routes[0].Host != "outlook.cloud.microsoft" {
		t.Fatalf("approval = %+v", approval)
	}
	info, err := os.Stat(filepath.Join(fixture.stateDir, signInVerifiedFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("verification record = %v, %v", info, err)
	}
	if directory, err := os.Stat(fixture.stateDir); err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatalf("sign-in state directory = %v, %v", directory, err)
	}

	changes := map[string]func(config.Config) config.Config{
		"origin": func(configuration config.Config) config.Config {
			return withOutlookOrigin(configuration, "work", "https://outlook.office.com")
		},
		"provider": func(configuration config.Config) config.Config {
			account := configuration.Accounts["work"]
			account.Calendar = &config.CalendarRoute{Provider: domain.ProviderCalDAV, CalDAV: &config.CalDAVRoute{
				Endpoint: "https://dav.example.com/", Username: "reader",
				Credential: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "dav", Consent: true},
			}}
			configuration.Accounts["work"] = account
			return configuration
		},
		"address": func(configuration config.Config) config.Config {
			account := configuration.Accounts["work"]
			account.Address = "someone@example.com"
			configuration.Accounts["work"] = account
			return configuration
		},
	}
	for name, change := range changes {
		changed := fixture.restart(t, change(cloneSignInConfig(original)))
		if result := changed.request(t, signInMCPCaller).Result; result == nil ||
			result.Reason != application.AccountSignInReasonCLISignInRequired {
			t.Fatalf("%s change kept the tool available: %+v", name, result)
		}
	}
}

func TestMCPSignInChangedOAuthClientRequiresCLISignInAgain(t *testing.T) {
	fixture := newUnverifiedSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	configuration := cloneSignInConfig(fixture.backend.configuration)
	account := configuration.Accounts["work"]
	route := sessionGoogleOAuthRoute("https://www.googleapis.com/calendar/v3", "google-work")
	account.Calendar = &config.CalendarRoute{Provider: domain.ProviderGoogle, Google: &route}
	account.Address = "Alice.Example@Example.com"
	configuration.Accounts["work"] = account
	fixture = fixture.restart(t, configuration)
	fixture.verify()
	approval := fixture.approval(t)
	want := application.AccountSignInRoute{
		Service: application.AuthenticationServiceCalendar, Provider: domain.ProviderGoogle,
		Kind: application.AccountSignInProviderAuthorization, Host: "accounts.google.com",
		ClientID: "synthetic-public-client-google-work",
	}
	if approval.Address != "Alice.Example@Example.com" || len(approval.Routes) != 2 || approval.Routes[1] != want {
		t.Fatalf("approval = %+v", approval)
	}

	changed := cloneSignInConfig(configuration)
	account = changed.Accounts["work"]
	other := route
	other.ClientID = "attacker-public-client"
	account.Calendar = &config.CalendarRoute{Provider: domain.ProviderGoogle, Google: &other}
	changed.Accounts["work"] = account
	if result := fixture.restart(t, changed).request(t, signInMCPCaller).Result; result == nil ||
		result.Reason != application.AccountSignInReasonCLISignInRequired {
		t.Fatalf("a changed OAuth client kept the tool available: %+v", result)
	}
}

// The H-1 chain: an MCP caller renames the verified "work" away and adds a new
// "work" whose Outlook Web origin is a look-alike. The daemon restarts after
// each committed change; the new account was never signed in from the CLI.
func TestMCPLookAlikeAccountChainNeverPrompts(t *testing.T) {
	fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	verifiedID := fixture.account
	configuration := cloneSignInConfig(fixture.backend.configuration)
	configuration.Accounts["work-old"] = configuration.Accounts["work"]
	lookAlike := configuration.Accounts["work"]
	lookAlike.ID = "acc_0000000000000000000000000000000b"
	configuration.Accounts["work"] = lookAlike
	configuration = withOutlookOrigin(configuration, "work", "https://outlook-office.example")

	attacked := fixture.restart(t, configuration)
	if attacked.account == verifiedID {
		t.Fatal("fixture did not select the look-alike account")
	}
	result := attacked.request(t, signInMCPCaller).Result
	if result == nil || result.Status != application.AccountSignInUnavailable ||
		result.Reason != application.AccountSignInReasonCLISignInRequired {
		t.Fatalf("look-alike account request = %+v", result)
	}
	if len(attacked.backend.signIn.approvals) != 0 || attacked.launches.count() != 0 {
		t.Fatal("look-alike account reserved a prompt or opened a browser")
	}
	// The renamed, verified account still shows its own genuine host.
	renamed, err := attacked.backend.RequestAccountSignIn(t.Context(), verifiedID, signInMCPCaller)
	if err != nil || renamed.Approval == nil || renamed.Approval.Alias != "work-old" ||
		renamed.Approval.Routes[0].Host != "outlook.cloud.microsoft" {
		t.Fatalf("renamed account request = %+v, %v", renamed, err)
	}

	// Removing the verified account removes its record.
	delete(configuration.Accounts, "work-old")
	fixture.restart(t, configuration)
	verified, err := signInStateFiles{dir: fixture.stateDir}.loadVerified()
	if err != nil || len(verified) != 0 {
		t.Fatalf("records after removal = %v, %v", verified, err)
	}
}

func TestMCPSignInRefusesUndisplayableDestinations(t *testing.T) {
	for name, change := range map[string]func(config.Config) config.Config{
		"cyrillic origin": func(configuration config.Config) config.Config {
			return withOutlookOrigin(configuration, "work", "https://outlооk.example")
		},
		"unicode address": func(configuration config.Config) config.Config {
			account := configuration.Accounts["work"]
			account.Address = "аlice@example.com"
			configuration.Accounts["work"] = account
			return configuration
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newUnverifiedSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
			fixture = fixture.restart(t, change(cloneSignInConfig(fixture.backend.configuration)))
			fixture.verify()
			result := fixture.request(t, signInMCPCaller).Result
			if result == nil || result.Reason != application.AccountSignInReasonDestinationNotDisplayable {
				t.Fatalf("request = %+v", result)
			}
		})
	}
}

func TestMCPSignInPromptListsOnlyEnabledReleasedServices(t *testing.T) {
	fixture := newUnverifiedSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	configuration := cloneSignInConfig(fixture.backend.configuration)
	account := configuration.Accounts["work"]
	account.DisabledServices = []string{"calendar"}
	configuration.Accounts["work"] = account
	fixture = fixture.restart(t, configuration)
	fixture.verify()
	approval := fixture.approval(t)
	if len(approval.Routes) != 1 || approval.Routes[0].Service != application.AuthenticationServiceMail {
		t.Fatalf("routes = %+v", approval.Routes)
	}
}

func TestMCPSignInCooldownSurvivesDaemonRestart(t *testing.T) {
	fixture := newSignInFixture(t, &gatedBrowserHandle{release: make(chan struct{})}, time.Minute)
	approval := fixture.approval(t)
	fixture.resolve(t, approval.Ticket, application.AccountSignInDecisionDeclinedByUser)
	info, err := os.Stat(filepath.Join(fixture.stateDir, signInCooldownFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cool-down file = %v, %v", info, err)
	}
	configuration := fixture.backend.configuration
	restarted := fixture.restart(t, cloneSignInConfig(configuration))
	result := restarted.request(t, signInMCPCaller).Result
	if result == nil || result.Status != application.AccountSignInDeclined ||
		result.Reason != application.AccountSignInReasonRecentlyDeclined {
		t.Fatalf("request after restart = %+v", result)
	}
	restarted.clock.advance(application.AccountSignInCooldown)
	if after := restarted.restart(t, cloneSignInConfig(configuration)).request(t, signInMCPCaller); after.Approval == nil {
		t.Fatalf("expired cool-down was not pruned: %+v", after.Result)
	}

	// An unreadable cool-down file cools down every account for the full period.
	if err := os.WriteFile(filepath.Join(fixture.stateDir, signInCooldownFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := fixture.restart(t, cloneSignInConfig(configuration))
	if result := broken.request(t, signInMCPCaller).Result; result == nil ||
		result.Reason != application.AccountSignInReasonStateUnavailable {
		t.Fatalf("request with unreadable state = %+v", result)
	}
	if !slices.Contains(broken.audit.reasons(), "cool_down_state_unreadable") {
		t.Fatalf("unreadable state was not audited: %v", broken.audit.reasons())
	}
	broken.clock.advance(application.AccountSignInCooldown)
	if after := broken.request(t, signInMCPCaller); after.Approval == nil {
		t.Fatalf("fail-closed period did not end: %+v", after.Result)
	}
}

func TestCLIDoesNotWaitSilentlyBehindAnMCPSignIn(t *testing.T) {
	handle := &gatedBrowserHandle{release: make(chan struct{})}
	fixture := newUnverifiedSignInFixture(t, handle, time.Minute)
	configuration := cloneSignInConfig(fixture.backend.configuration)
	home := configuration.Accounts["work"]
	home.ID = "acc_0000000000000000000000000000000c"
	configuration.Accounts["home"] = home
	fixture = fixture.restart(t, configuration)
	fixture.verify()
	approval := fixture.approval(t)
	fixture.resolve(t, approval.Ticket, application.AccountSignInDecisionApprovedByUser)
	fixture.waitUntil(t, func() bool { return fixture.launches.count() == 1 })

	ends := fixture.clock.Now().Add(time.Minute).Local().Format("15:04:05 MST")
	check := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), `account "work"`) || !strings.Contains(err.Error(), ends) ||
			!strings.Contains(err.Error(), "corr auth logout --account work") {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := fixture.backend.Login(ctx, home.ID, signInCLICaller)
	check("login of another account", err)
	_, err = fixture.backend.Login(ctx, fixture.account, signInCLICaller)
	check("login of the same account", err)
	_, err = fixture.backend.TerminalLogin(ctx, daemonapi.TerminalLoginInput{Account: home.ID}, signInCLICaller)
	check("terminal login", err)
	_, err = fixture.backend.Logout(ctx, home.ID, signInCLICaller)
	check("logout of another account", err)

	// Logging out the same account cancels the MCP-started sign-in.
	if _, err := fixture.backend.Logout(ctx, fixture.account, signInCLICaller); err != nil {
		t.Fatalf("logout of the signing-in account error = %v", err)
	}
	fixture.backend.mu.Lock()
	runs := len(fixture.backend.signIn.mcpRuns)
	fixture.backend.mu.Unlock()
	if !handle.closed.Load() || fixture.running() || runs != 0 {
		t.Fatal("cancelled sign-in left its browser or state behind")
	}
	fixture.waitUntil(t, func() bool { return slices.Contains(fixture.audit.reasons(), "sign_in_cancelled") })
	if _, err := fixture.backend.Logout(ctx, home.ID, signInCLICaller); err != nil {
		t.Fatalf("logout after cancellation error = %v", err)
	}
}
