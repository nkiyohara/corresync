package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
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

func newSignInFixture(t *testing.T, handle browserHandle, loginTimeout time.Duration) signInFixture {
	t.Helper()
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configuration.Browser.LoginTimeout = config.Duration(loginTimeout)
	launches := &signInLaunches{}
	audit := &signInAudit{}
	lifecycle, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
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
	clock := &signInClock{now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)}
	noDisplay := &atomic.Bool{}
	coordinator := newSignInCoordinator()
	coordinator.now = clock.Now
	coordinator.audit = audit
	coordinator.display = func() error {
		if noDisplay.Load() {
			return browser.ErrGraphicalSessionUnavailable
		}
		return nil
	}
	backend.signIn = coordinator
	t.Cleanup(func() {
		cancel()
		backend.active.Wait()
		for _, account := range backend.accounts {
			_ = closeSessionAccount(account)
		}
	})
	return signInFixture{
		backend: backend, account: configuration.Accounts["work"].ID,
		audit: audit, launches: launches, clock: clock, noDisplay: noDisplay,
	}
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
		approval.Routes[0] != (application.AccountSignInRoute{Service: "mail", Provider: domain.ProviderMicrosoftOWA}) {
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
		{application.AccountSignInDecisionElicitationFailed, application.AccountSignInUnavailable, application.AccountSignInReasonElicitationFailed, false},
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
				if next.Result == nil || next.Result.Status != application.AccountSignInDeclined ||
					next.Result.Reason != application.AccountSignInReasonRecentlyDeclined {
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

	fixture.clock.advance(application.AccountSignInApprovalTTL)
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
