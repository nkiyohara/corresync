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
	snapshots      []browser.TerminalView
	snapshotStages [][]browser.TerminalView
	stageCalls     map[int]int
	snapshotCalls  int
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
	view := browser.TerminalView{
		Origin: "https://login.example", Title: "Sign in", Text: "Continue",
		Controls: []browser.TerminalControl{{ID: "control-1", Kind: "input", Name: "Email"}},
	}
	if len(browserHandle.snapshotStages) > 0 {
		stage := len(browserHandle.actions)
		if stage >= len(browserHandle.snapshotStages) {
			stage = len(browserHandle.snapshotStages) - 1
		}
		views := browserHandle.snapshotStages[stage]
		if browserHandle.stageCalls == nil {
			browserHandle.stageCalls = make(map[int]int)
		}
		index := browserHandle.stageCalls[stage]
		if index >= len(views) {
			index = len(views) - 1
		}
		if index >= 0 {
			view = views[index]
		}
		browserHandle.stageCalls[stage]++
	} else if len(browserHandle.snapshots) > 0 {
		index := browserHandle.snapshotCalls
		if index >= len(browserHandle.snapshots) {
			index = len(browserHandle.snapshots) - 1
		}
		view = browserHandle.snapshots[index]
	}
	browserHandle.snapshotCalls++
	return view, browserHandle.snapshotErr
}

func (browserHandle *fakeTerminalBrowser) TerminalAct(_ context.Context, action browser.TerminalAction) error {
	browserHandle.actions = append(browserHandle.actions, action)
	return browserHandle.interactionErr
}

func TestSessionBackendTerminalActivateWaitsForPageChange(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	initial := browser.TerminalView{
		Origin: "https://login.microsoftonline.com", Title: "Sign in",
		Text: "Pick an account",
		Controls: []browser.TerminalControl{{
			ID: "control-1", Kind: "activate", Name: "Sign in with work account",
		}},
	}
	next := browser.TerminalView{
		Origin: "https://login.microsoftonline.com", Title: "Enter password",
		Text: "Enter password",
		Controls: []browser.TerminalControl{{
			ID: "control-1", Kind: "input", Name: "Password", Sensitive: true,
		}},
	}
	transient := browser.TerminalView{
		Origin: "https://login.microsoftonline.com", Title: "Sign in to your account",
		Text: "Trying to sign you in\nCancel",
		Controls: []browser.TerminalControl{{
			ID: "control-1", Kind: "activate", Name: "Cancel",
		}},
	}
	fakeBrowser := &fakeTerminalBrowser{snapshotStages: [][]browser.TerminalView{
		{initial},
		{transient, next},
	}}
	app := &runtime{launch: func(_ context.Context, _ browser.Options) (browserHandle, error) {
		return fakeBrowser, nil
	}}
	backend := &sessionBackend{
		app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel,
		accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview),
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
	}
	accountID := backend.configuration.Accounts["work"].ID
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	result, err := backend.TerminalLogin(
		t.Context(), daemonapi.TerminalLoginInput{Account: accountID}, caller,
	)
	if err != nil {
		t.Fatalf("TerminalLogin(start) error = %v", err)
	}
	result, err = backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{
		Account: accountID, SessionID: result.SessionID,
		Action: &daemonapi.TerminalLoginAction{Type: "activate", ControlID: "control-1"},
	}, caller)
	if err != nil {
		t.Fatalf("TerminalLogin(activate) error = %v", err)
	}
	if result.View == nil || result.View.Title != "Enter password" ||
		len(fakeBrowser.actions) != 1 || fakeBrowser.snapshotCalls < 4 {
		t.Fatalf(
			"TerminalLogin(activate) = %+v; actions=%+v snapshots=%d",
			result,
			fakeBrowser.actions,
			fakeBrowser.snapshotCalls,
		)
	}
}

func TestSessionBackendTerminalLoginWaitsForInitialInteractivePage(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	bootstrap := browser.TerminalView{
		Origin: "https://outlook.office.com", Title: "Outlook",
	}
	interactive := browser.TerminalView{
		Origin: "https://login.microsoftonline.com", Title: "Sign in", Text: "Sign in",
		Controls: []browser.TerminalControl{{
			ID: "control-1", Kind: "input", Name: "Email",
		}},
	}
	fakeBrowser := &fakeTerminalBrowser{snapshotStages: [][]browser.TerminalView{{
		bootstrap, interactive,
	}}}
	app := &runtime{launch: func(_ context.Context, _ browser.Options) (browserHandle, error) {
		return fakeBrowser, nil
	}}
	backend := &sessionBackend{
		app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel,
		accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview),
		terminalSessions: make(map[string]*terminalLoginSession),
		terminalAccounts: make(map[domain.AccountID]string),
	}
	accountID := backend.configuration.Accounts["work"].ID
	result, err := backend.TerminalLogin(
		t.Context(), daemonapi.TerminalLoginInput{Account: accountID},
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("TerminalLogin(start) error = %v", err)
	}
	if result.View == nil || result.View.Title != "Sign in" || len(result.View.Controls) != 1 {
		t.Fatalf("TerminalLogin(start) = %+v; snapshots=%d", result, fakeBrowser.snapshotCalls)
	}
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
		app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel,
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
	web, _ := config.OutlookDefault().Accounts["work"].OutlookWeb()
	if !launched.Headless || launched.Origin != web.Origin {
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
