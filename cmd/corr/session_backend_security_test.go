package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
	caldavprovider "github.com/nkiyohara/corresync/internal/provider/caldav"
	"github.com/nkiyohara/corresync/internal/session"
)

type readyTerminalSecurityBrowser struct{ fakeTerminalBrowser }

func (handle *readyTerminalSecurityBrowser) CurrentSession() (session.Credentials, error) { //nolint:unparam // Implements the browser session interface.
	return handle.credentials, nil
}

func terminalSecurityBackend(t *testing.T, configuration config.Config, launch browserLauncher) *sessionBackend {
	t.Helper()
	lifecycle, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &sessionBackend{
		app: &runtime{launch: launch}, configuration: configuration, lifecycle: lifecycle, cancel: cancel,
		guard:    daemonMCPGuard(t, policy.DefaultRules(), &daemonMCPAudit{}),
		accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview),
		terminalSessions: make(map[string]*terminalLoginSession), terminalAccounts: make(map[domain.AccountID]string),
		monitorCancel: make(map[domain.AccountID]context.CancelFunc), monitorDone: make(map[domain.AccountID]chan struct{}),
		monitorStarted: make(map[domain.AccountID]bool),
	}
}

func readySecurityTerminal(t *testing.T) *readyTerminalSecurityBrowser {
	t.Helper()
	manager, err := session.NewManager("https://outlook.office.com")
	if err != nil {
		t.Fatal(err)
	}
	if !manager.Observe("https://outlook.office.com/owa/service.svc", http.Header{"Authorization": {"Bearer " + strings.Repeat("s", 40)}}) {
		t.Fatal("synthetic session observation failed")
	}
	credentials, err := manager.Current()
	if err != nil {
		t.Fatal(err)
	}
	return &readyTerminalSecurityBrowser{fakeTerminalBrowser: fakeTerminalBrowser{credentials: credentials}}
}

func TestTerminalLoginKeepsDormantMessagingBehindReleaseGate(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configured := configuration.Accounts["work"]
	configured.Messages = dormantSlackAccount().Messages
	configuration.Accounts["work"] = configured
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	handle := readySecurityTerminal(t)
	backend := terminalSecurityBackend(t, configuration, func(context.Context, browser.Options) (browserHandle, error) { return handle, nil })
	keyringReads := 0
	resolver, err := credential.New(credential.Options{Keyring: func(string, string) (string, error) {
		keyringReads++
		return "", errors.New("dormant credential must be unreachable")
	}})
	if err != nil {
		t.Fatal(err)
	}
	backend.credentials = resolver
	result, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: configured.ID}, domain.Caller{Surface: "cli", Instance: "gate-test"})
	if err != nil || result.Status != "authenticated" || keyringReads != 0 {
		t.Fatalf("TerminalLogin() = %+v, %v; keyring reads=%d", result, err, keyringReads)
	}
	account := backend.accounts[configured.ID]
	defer func() { _ = closeSessionAccount(account) }()
	if account.messages != nil || account.capabilities.Messages {
		t.Fatal("terminal login activated dormant messaging")
	}
	if len(account.staticDegradations) != 1 || account.staticDegradations[0].Feature != "messages.route" {
		t.Fatalf("route degradations = %+v", account.staticDegradations)
	}
}

func TestTerminalLoginRequiresValidCLICallerBeforeBrowserWork(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	for _, caller := range []domain.Caller{{Surface: "mcp", Instance: "test"}, {Surface: "cli"}, {Surface: "unknown", Instance: "test"}} {
		t.Run(caller.Surface+caller.Instance, func(t *testing.T) {
			launched := 0
			backend := terminalSecurityBackend(t, config.OutlookDefault(), func(context.Context, browser.Options) (browserHandle, error) {
				launched++
				return readySecurityTerminal(t), nil
			})
			_, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: backend.DefaultAccount()}, caller)
			if err == nil || launched != 0 || len(backend.terminalSessions) != 0 {
				t.Fatalf("TerminalLogin() error=%v launches=%d sessions=%d", err, launched, len(backend.terminalSessions))
			}
		})
	}
}

type terminalDrainSecurityCloser struct {
	backend *sessionBackend
	closed  atomic.Int32
	locked  atomic.Bool
}

func (closer *terminalDrainSecurityCloser) Close() error {
	if closer.backend.mu.TryLock() {
		closer.backend.mu.Unlock()
	} else {
		closer.locked.Store(true)
	}
	closer.closed.Add(1)
	return nil
}

func TestTerminalLoginDrainsPartialAccountOutsideMutexBeforeRepair(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configured := configuration.Accounts["work"]
	configured.Calendar = &config.CalendarRoute{Provider: domain.ProviderCalDAV, CalDAV: &config.CalDAVRoute{
		Endpoint: "https://dav.example.invalid/", Username: "reader@example.invalid",
		Credential: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "synthetic-calendar", Consent: true},
	}}
	configuration.Accounts["work"] = configured
	stop := errors.New("synthetic replacement credential stop")
	launched := make(chan struct{}, 1)
	handle := readySecurityTerminal(t)
	backend := terminalSecurityBackend(t, configuration, func(context.Context, browser.Options) (browserHandle, error) {
		launched <- struct{}{}
		return handle, nil
	})
	resolver, err := credential.New(credential.Options{Keyring: func(string, string) (string, error) { return "", stop }})
	if err != nil {
		t.Fatal(err)
	}
	backend.credentials = resolver
	closer := &terminalDrainSecurityCloser{backend: backend}
	old := leasedSessionAccount(sessionAccount{calendar: new(application.CalendarService), captured: time.Now(), closers: []sessionCloser{closer}})
	backend.accounts[configured.ID] = old
	otherID := domain.AccountID("acc_00000000000000000000000000000099")
	other := leasedSessionAccount(sessionAccount{mail: new(application.MailService), captured: time.Now()})
	backend.accounts[otherID] = other
	backend.previews["partial-preview"] = sessionPreview{account: configured.ID, service: application.AuthenticationServiceCalendar, expiresAt: time.Now().Add(time.Minute)}
	backend.previews["other-preview"] = sessionPreview{account: otherID, service: application.AuthenticationServiceMail, expiresAt: time.Now().Add(time.Minute)}
	borrower, err := backend.accountServices(t.Context(), configured.ID, domain.Caller{Surface: "cli", Instance: "reader"}, application.AuthenticationServiceCalendar)
	if err != nil {
		t.Fatal(err)
	}
	defer borrower.releaseBorrowedUsage()
	done := make(chan error, 1)
	go func() {
		_, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: configured.ID}, domain.Caller{Surface: "cli", Instance: "repair"})
		done <- err
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		backend.mu.Lock()
		_, attached := backend.accounts[configured.ID]
		backend.mu.Unlock()
		if !attached {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("partial account was not detached")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-launched:
		t.Fatal("replacement browser opened before existing borrower drained")
	default:
	}
	if closer.closed.Load() != 0 {
		t.Fatal("borrowed lease closed prematurely")
	}
	borrower.releaseBorrowedUsage()
	select {
	case err := <-done:
		if !errors.Is(err, stop) {
			t.Fatalf("repair error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("repair failed to finish after draining")
	}
	if closer.closed.Load() != 1 || closer.locked.Load() {
		t.Fatalf("old lease closes=%d, backend mutex held=%v", closer.closed.Load(), closer.locked.Load())
	}
	select {
	case <-launched:
	default:
		t.Fatal("partial account was not repaired through browser authentication")
	}
	if !handle.closed {
		t.Fatal("failed replacement leaked its new browser")
	}
	if _, exists := backend.accounts[configured.ID]; exists {
		t.Fatal("failed replacement reported active account")
	}
	if backend.accounts[otherID].mailLease != other.mailLease {
		t.Fatal("repair replaced another account")
	}
	if _, exists := backend.previews["partial-preview"]; exists {
		t.Fatal("old account preview survived repair")
	}
	if _, exists := backend.previews["other-preview"]; !exists {
		t.Fatal("another account preview was removed")
	}
}

func TestSessionBorrowReleaseIsExactlyOnceOnReturnAndPanic(t *testing.T) {
	for _, panics := range []bool{false, true} {
		for _, kind := range []string{"read", "mail commit", "calendar commit", "task commit", "wrong service"} {
			name := kind + " return"
			if panics {
				name = kind + " panic"
			}
			t.Run(name, func(t *testing.T) {
				configuration := config.OutlookDefault()
				id := configuration.Accounts["work"].ID
				account := leasedSessionAccount(sessionAccount{mail: new(application.MailService), calendar: new(application.CalendarService), tasks: new(application.TaskService)})
				backend := &sessionBackend{configuration: configuration, accounts: map[domain.AccountID]sessionAccount{id: account}, previews: make(map[string]sessionPreview)}
				service := application.AuthenticationServiceMail
				if kind == "calendar commit" || kind == "wrong service" {
					service = application.AuthenticationServiceCalendar
				}
				if kind == "task commit" {
					service = application.AuthenticationServiceTasks
				}
				backend.previews["preview"] = sessionPreview{account: id, service: service, expiresAt: time.Now().Add(time.Minute)}
				lease := account.mailLease
				if err := lease.usage.begin(); err != nil {
					t.Fatal(err)
				} // One independent active call must survive this call's cleanup.
				invoked := false
				operation := func() {
					invoked = true
					if panics {
						panic("synthetic service panic")
					}
				}
				recovered := false
				func() {
					defer func() {
						if recover() != nil {
							recovered = true
						}
					}()
					switch kind {
					case "read":
						_, _ = withMailService(backend, t.Context(), id, domain.Caller{Surface: "cli", Instance: "read"}, func(*application.MailService) (int, error) { operation(); return 1, nil })
					case "mail commit", "wrong service":
						_, _ = commitMailPreview(backend, "preview", func(*application.MailService) (int, error) { operation(); return 1, nil })
					case "calendar commit":
						_, _ = commitCalendarPreview(backend, "preview", func(*application.CalendarService) (int, error) { operation(); return 1, nil })
					case "task commit":
						_, _ = backend.commitTaskWrite("preview", func(*application.TaskService) (application.TaskWriteAccess, error) {
							operation()
							return application.TaskWriteAccess{}, nil
						})
					}
				}()
				if kind == "wrong service" {
					if invoked {
						t.Fatal("wrong service reached provider")
					}
				} else if !invoked || recovered != panics {
					t.Fatalf("invoked=%v recovered=%v", invoked, recovered)
				}
				lease.usage.mu.Lock()
				active := lease.usage.active
				lease.usage.mu.Unlock()
				if active != 1 {
					t.Fatalf("active borrowers=%d; want only the independent borrower", active)
				}
				lease.usage.end()
				select {
				case <-lease.usage.closeAfterActive():
				default:
					t.Fatal("lease did not drain")
				}
				if err := closeSessionAccount(account); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTerminalLoginPartialAccountCompletesReauthentication(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configured := configuration.Accounts["work"]
	configured.Calendar = &config.CalendarRoute{Provider: domain.ProviderCalDAV, CalDAV: &config.CalDAVRoute{
		Endpoint: "https://dav.example.invalid/", Username: "reader@example.invalid",
		Credential: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "synthetic-calendar", Consent: true},
	}}
	configuration.Accounts["work"] = configured
	handle := readySecurityTerminal(t)
	launches := 0
	backend := terminalSecurityBackend(t, configuration, func(context.Context, browser.Options) (browserHandle, error) { launches++; return handle, nil })
	resolver, err := credential.New(credential.Options{Keyring: func(string, string) (string, error) { return "synthetic-secret", nil }})
	if err != nil {
		t.Fatal(err)
	}
	backend.credentials = resolver
	backend.newCalDAV = func(context.Context, caldavprovider.Options) (*caldavprovider.Client, error) {
		return new(caldavprovider.Client), nil
	}
	backend.accounts[configured.ID] = leasedSessionAccount(sessionAccount{calendar: new(application.CalendarService), captured: time.Now()})
	setAuthenticationReason(&backend.reauthentication, configured.ID, application.AuthenticationServiceMail, application.AuthenticationReasonSessionExpired)
	caller := domain.Caller{Surface: "cli", Instance: "repair-success"}
	result, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: configured.ID}, caller)
	if err != nil || result.Status != "authenticated" || result.CapturedAt.IsZero() || launches != 1 {
		t.Fatalf("TerminalLogin() = %+v, %v; launches=%d", result, err, launches)
	}
	account := backend.accounts[configured.ID]
	defer func() { _ = closeSessionAccount(account) }()
	if !sessionAccountComplete(configured, account) || len(backend.reauthentication[configured.ID]) != 0 {
		t.Fatal("successful repair did not restore every service and clear stale authentication reason")
	}
	if _, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: configured.ID}, caller); err != nil || launches != 1 {
		t.Fatalf("complete account relaunched authentication: %v launches=%d", err, launches)
	}
}

type auditCancelReadyHandle struct {
	*readyTerminalSecurityBrowser
	cancel context.CancelFunc
}

func (handle *auditCancelReadyHandle) CurrentSession() (session.Credentials, error) { //nolint:unparam // Implements the browser session interface.
	handle.cancel()
	return handle.credentials, nil
}
func TestAuditTerminalCancellationBeforePublishing(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configured := configuration.Accounts["work"]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle := &auditCancelReadyHandle{readyTerminalSecurityBrowser: readySecurityTerminal(t), cancel: cancel}
	backend := terminalSecurityBackend(t, configuration, func(context.Context, browser.Options) (browserHandle, error) { return handle, nil })
	result, err := backend.TerminalLogin(ctx, daemonapi.TerminalLoginInput{Account: configured.ID}, domain.Caller{Surface: "cli", Instance: "cancellation-audit"})
	active, published := backend.accounts[configured.ID]
	if published {
		defer func() { _ = closeSessionAccount(active) }()
	}
	if !handle.closed || len(backend.terminalSessions) != 0 || len(backend.terminalAccounts) != 0 {
		t.Fatal("cancelled acquisition leaked terminal resources")
	}
	if !errors.Is(err, context.Canceled) || published {
		t.Fatalf("cancelled request result=%+v err=%v published=%v", result, err, published)
	}
}

func (handle *auditCancelReadyHandle) WaitForSession(context.Context) (session.Credentials, error) {
	handle.cancel()
	return handle.credentials, nil
}
func TestAuditVisibleCancellationBeforePublishing(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configuration := config.OutlookDefault()
	configured := configuration.Accounts["work"]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle := &auditCancelReadyHandle{readyTerminalSecurityBrowser: readySecurityTerminal(t), cancel: cancel}
	backend := terminalSecurityBackend(t, configuration, func(context.Context, browser.Options) (browserHandle, error) { return handle, nil })
	backend.app.stderr = io.Discard
	_, err := backend.activateAccount(ctx, configured.ID)
	active, published := backend.accounts[configured.ID]
	if published {
		defer func() { _ = closeSessionAccount(active) }()
	}
	if !errors.Is(err, context.Canceled) || published || !handle.closed {
		t.Fatalf("cancelled acquisition err=%v published=%v closed=%v", err, published, handle.closed)
	}
}
