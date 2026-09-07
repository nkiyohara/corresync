package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

// Synchronize cancellation after the entry check returns nil, while the
// operation is waiting for the session transition lock. Later checks use the
// actual canceled context. No sleeps or scheduler timing assumptions are used.
type queuedAuthenticationContext struct {
	context.Context
	checked chan struct{}
	checks  atomic.Int32
}

func (ctx *queuedAuthenticationContext) Err() error {
	if ctx.checks.Add(1) == 1 {
		close(ctx.checked)
		return nil
	}
	return ctx.Context.Err()
}

func TestAuthenticationCancellationAfterActivationWaitPreservesSession(t *testing.T) {
	for _, method := range []string{"terminal login", "login", "logout"} {
		t.Run(method, func(t *testing.T) {
			configuration := config.OutlookDefault()
			configured := configuration.Accounts["work"]
			if method == "terminal login" {
				configured.Calendar = &config.CalendarRoute{Provider: domain.ProviderCalDAV}
				configuration.Accounts["work"] = configured
			}
			backend := terminalSecurityBackend(t, configuration, nil)
			closer := &terminalDrainSecurityCloser{backend: backend}
			existing := sessionAccount{calendar: new(application.CalendarService), closers: []sessionCloser{closer}, captured: time.Now()}
			if method != "terminal login" {
				existing.mail = new(application.MailService)
			}
			existing = leasedSessionAccount(existing)
			backend.accounts[configured.ID] = existing
			backend.previews["retained"] = sessionPreview{account: configured.ID, service: application.AuthenticationServiceCalendar, expiresAt: time.Now().Add(time.Minute)}
			caller := domain.Caller{Surface: "cli", Instance: "queued-cancellation"}
			underlying, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := underlying
			var checked <-chan struct{}
			if method == "login" {
				// Login delegates to activateAccount without an entry context check.
				cancel()
			} else {
				observed := &queuedAuthenticationContext{Context: underlying, checked: make(chan struct{})}
				ctx = observed
				checked = observed.checked
			}
			backend.activationMu.Lock()
			done := make(chan error, 1)
			go func() {
				var err error
				switch method {
				case "terminal login":
					_, err = backend.TerminalLogin(ctx, daemonapi.TerminalLoginInput{Account: configured.ID}, caller)
				case "login":
					_, err = backend.Login(ctx, configured.ID, caller)
				case "logout":
					_, err = backend.Logout(ctx, configured.ID, caller)
				}
				done <- err
			}()
			if checked != nil {
				<-checked
			}
			cancel()
			backend.activationMu.Unlock()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s returned %v after cancellation", method, err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled authentication did not finish")
			}
			retained, exists := backend.accounts[configured.ID]
			if !exists || retained.calendarLease != existing.calendarLease || closer.closed.Load() != 0 {
				t.Fatalf("%s mutated its existing session after cancellation: exists=%v closes=%d", method, exists, closer.closed.Load())
			}
			if _, exists := backend.previews["retained"]; !exists {
				t.Fatal("canceled authentication removed a preview")
			}
			if len(backend.terminalSessions) != 0 {
				t.Fatal("canceled authentication started terminal interaction")
			}
			if err := closeSessionAccount(existing); err != nil {
				t.Fatal(err)
			}
		})
	}
}
