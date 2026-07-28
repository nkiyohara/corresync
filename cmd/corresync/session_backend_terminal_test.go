package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/session"
)

type fakeTerminalBrowser struct {
	actions        []browser.TerminalAction
	closed         bool
	snapshotErr    error
	interactionErr error
}

func (*fakeTerminalBrowser) WaitForSession(context.Context) (session.Credentials, error) {
	return session.Credentials{}, session.ErrNotReady
}

func (*fakeTerminalBrowser) Apply(*http.Request) error { return session.ErrNotReady }

func (browser *fakeTerminalBrowser) Close() error {
	browser.closed = true
	return nil
}

func (*fakeTerminalBrowser) CurrentSession() (session.Credentials, error) {
	return session.Credentials{}, session.ErrNotReady
}

func (browserHandle *fakeTerminalBrowser) TerminalSnapshot(context.Context) (browser.TerminalView, error) {
	return browser.TerminalView{
		Origin: "https://login.example", Title: "Sign in", Text: "Continue",
		Controls: []browser.TerminalControl{{ID: "control-1", Kind: "input", Name: "Email"}},
	}, browserHandle.snapshotErr
}

func (browserHandle *fakeTerminalBrowser) TerminalAct(_ context.Context, action browser.TerminalAction) error {
	browserHandle.actions = append(browserHandle.actions, action)
	return browserHandle.interactionErr
}

func TestSessionBackendTerminalLoginStartsHeadlessAndBindsCaller(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	fakeBrowser := &fakeTerminalBrowser{}
	var launched browser.Options
	app := &runtime{launch: func(_ context.Context, options browser.Options) (browserHandle, error) {
		launched = options
		return fakeBrowser, nil
	}}
	backend := &sessionBackend{
		app: app, configuration: config.Default(), lifecycle: lifecycle, cancel: cancel,
		accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview),
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
	}
	accountID := backend.configuration.Accounts["work"].ID
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	result, err := backend.TerminalLogin(
		t.Context(),
		daemonapi.TerminalLoginInput{Account: accountID},
		caller,
	)
	if err != nil || result.Status != "pending" || result.View == nil {
		t.Fatalf("TerminalLogin(start) = %+v, %v", result, err)
	}
	if !launched.Headless || launched.Origin != config.Default().Accounts["work"].Origin {
		t.Fatalf("browser options = %+v", launched)
	}

	_, err = backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{
		Account: accountID, SessionID: result.SessionID,
		Action: &daemonapi.TerminalLoginAction{Type: "key", ControlID: "control-1", Key: "a"},
	}, domain.Caller{Surface: "cli", Instance: "process-2"})
	if err == nil || err.Error() != "invalid or expired terminal login session" {
		t.Fatalf("different caller error = %v", err)
	}

	result, err = backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{
		Account: accountID, SessionID: result.SessionID,
		Action: &daemonapi.TerminalLoginAction{Type: "key", ControlID: "control-1", Key: "a"},
	}, caller)
	if err != nil || result.Status != "pending" || len(fakeBrowser.actions) != 1 || fakeBrowser.actions[0].Key != "a" {
		t.Fatalf("TerminalLogin(key) = %+v, %v; actions=%+v", result, err, fakeBrowser.actions)
	}

	result, err = backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{
		Account: accountID, SessionID: result.SessionID,
		Action: &daemonapi.TerminalLoginAction{Type: "cancel"},
	}, caller)
	if err != nil || result.Status != "cancelled" || !fakeBrowser.closed || len(backend.terminalSessions) != 0 {
		t.Fatalf("TerminalLogin(cancel) = %+v, %v; closed=%v", result, err, fakeBrowser.closed)
	}
}
