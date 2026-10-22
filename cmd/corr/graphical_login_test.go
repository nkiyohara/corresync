package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/loginui"
)

func TestGraphicalSSHArguments(t *testing.T) {
	for _, host := range []string{"-oProxyCommand=bad", "host;echo bad", "host\nother", "host name"} {
		if _, err := graphicalSSHArguments(host, "work"); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	args, err := graphicalSSHArguments("me@nvidia7", "work'; echo bad; '")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 5 || args[4] != "corr auth login --web-stdio --account "+shellSingleQuote("work'; echo bad; '") {
		t.Fatal(args)
	}
}
func TestGraphicalWireRejectsBadVersionWithoutStarting(t *testing.T) {
	calls := 0
	exchange := func(_ context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
		if action == nil {
			calls++
		}
		return loginui.Result{}, nil
	}
	var output bytes.Buffer
	err := serveGraphicalStdio(t.Context(), strings.NewReader("{\"version\":999,\"action\":null}\n"), &output, exchange)
	if err == nil || calls != 0 || output.Len() != 0 {
		t.Fatalf("err=%v starts=%d output=%s", err, calls, output.String())
	}
}
func TestGraphicalWireBoundsAndCompletion(t *testing.T) {
	var output bytes.Buffer
	requests := strings.NewReader("{\"version\":1,\"action\":null}\n{\"version\":1,\"action\":{\"type\":\"text\",\"text\":\"synthetic input\"}}\n")
	starts, inputs := 0, 0
	exchange := func(_ context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
		if action == nil {
			starts++
			return loginui.Result{Status: "pending"}, nil
		}
		if action.Type == "text" {
			inputs++
			return loginui.Result{Status: "authenticated"}, nil
		}
		return loginui.Result{}, nil
	}
	if err := serveGraphicalStdio(t.Context(), requests, &output, exchange); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || inputs != 1 || strings.Contains(output.String(), "synthetic input") {
		t.Fatal("input leaked or repeated")
	}
	decoder := json.NewDecoder(&output)
	for _, status := range []string{"pending", "authenticated"} {
		var got graphicalWireResponse
		if err := decoder.Decode(&got); err != nil || got.Result.Status != status {
			t.Fatalf("%+v %v", got, err)
		}
	}
}

type fakeGraphicalBrowser struct {
	fakeTerminalBrowser
	inputs int
}

func (browser *fakeGraphicalBrowser) GraphicalLogin(_ context.Context, action application.GraphicalLoginAction) (application.GraphicalLoginFrame, error) {
	browser.inputs++
	return application.GraphicalLoginFrame{JPEG: []byte("synthetic"), Origin: "https://login.example", Width: 1280, Height: 900}, action.Validate()
}
func TestGraphicalLoginAccountCallerAndModeIsolation(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(t.Context())
	defer cancel()
	handle := &fakeGraphicalBrowser{}
	app := &runtime{launch: func(context.Context, browser.Options) (browserHandle, error) { return handle, nil }}
	backend := &sessionBackend{app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel, accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview), terminalSessions: make(map[string]*terminalLoginSession), terminalAccounts: make(map[domain.AccountID]string)}
	account := backend.configuration.Accounts["work"].ID
	caller := domain.Caller{Surface: "cli", Instance: "graphical-1"}
	start, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: true}, caller)
	if err != nil || start.Frame == nil || start.View != nil {
		t.Fatalf("start=%+v err=%v", start, err)
	}
	input := daemonapi.TerminalLoginInput{Account: account, SessionID: start.SessionID, Graphical: true, GraphicalAction: &application.GraphicalLoginAction{Type: "refresh"}}
	other := caller
	other.Instance = "graphical-2"
	if _, err := backend.TerminalLogin(t.Context(), input, other); err == nil {
		t.Fatal("other caller accepted")
	}
	input.Graphical = false
	input.GraphicalAction = nil
	input.Action = &daemonapi.TerminalLoginAction{Type: "refresh"}
	if _, err := backend.TerminalLogin(t.Context(), input, caller); err == nil {
		t.Fatal("mode switch accepted")
	}
	if handle.inputs != 1 {
		t.Fatal("invalid request reached browser")
	}
	input.Graphical = true
	input.Action = nil
	input.GraphicalAction = &application.GraphicalLoginAction{Type: "cancel"}
	result, err := backend.TerminalLogin(t.Context(), input, caller)
	if err != nil || result.Status != "cancelled" || !handle.closed {
		t.Fatalf("cancel=%+v %v", result, err)
	}
	if _, err := backend.TerminalLogin(t.Context(), input, caller); err == nil {
		t.Fatal("expired replay accepted")
	}
}

func TestGraphicalLoginExpiresWithoutAnotherCLIRequest(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(t.Context())
	defer cancel()
	handles := []*fakeGraphicalBrowser{}
	app := &runtime{launch: func(context.Context, browser.Options) (browserHandle, error) {
		handle := &fakeGraphicalBrowser{}
		handles = append(handles, handle)
		return handle, nil
	}}
	configuration := config.OutlookDefault()
	configuration.Browser.LoginTimeout = config.Duration(100 * time.Millisecond)
	backend := &sessionBackend{app: app, configuration: configuration, lifecycle: lifecycle, cancel: cancel, accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview), terminalSessions: make(map[string]*terminalLoginSession), terminalAccounts: make(map[domain.AccountID]string)}
	account := configuration.Accounts["work"].ID
	first, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: true}, domain.Caller{Surface: "cli", Instance: "disconnected-cli"})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	old := backend.terminalSessions[first.SessionID]
	backend.mu.Unlock()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		backend.mu.Lock()
		expired := len(backend.terminalSessions) == 0 && handles[0].closed
		backend.mu.Unlock()
		if expired {
			break
		}
		select {
		case <-deadline:
			t.Fatal("daemon retained abandoned login")
		case <-ticker.C:
		}
	}
	backend.configuration.Browser.LoginTimeout = config.Duration(time.Minute)
	next, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: true}, domain.Caller{Surface: "cli", Instance: "new-cli"})
	if err != nil || next.SessionID == first.SessionID {
		t.Fatalf("replacement=%+v %v", next, err)
	}
	// An already queued old expiry cannot close the replacement interaction.
	backend.expireTerminalInteraction(old)
	backend.mu.Lock()
	if handles[1].closed {
		t.Error("old timer closed replacement")
	}
	interaction := backend.terminalSessions[next.SessionID]
	if interaction == nil {
		backend.mu.Unlock()
		t.Fatal("replacement disappeared")
	}
	if err := backend.dropTerminalInteraction(interaction, false); err != nil {
		t.Error(err)
	}
	backend.mu.Unlock()
	backend.expireTerminalInteraction(interaction)
	if handles[1].closed {
		t.Fatal("completed login was closed by expiry")
	}
}

func TestInteractiveLoginDropsExistingObservationFailure(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	for _, graphical := range []bool{false, true} {
		t.Run(strconv.FormatBool(graphical), func(t *testing.T) {
			lifecycle, cancel := context.WithCancel(t.Context())
			defer cancel()
			handle := &fakeGraphicalBrowser{}
			app := &runtime{launch: func(context.Context, browser.Options) (browserHandle, error) { return handle, nil }}
			backend := &sessionBackend{app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel, accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview), terminalSessions: make(map[string]*terminalLoginSession), terminalAccounts: make(map[domain.AccountID]string)}
			account := backend.configuration.Accounts["work"].ID
			caller := domain.Caller{Surface: "cli", Instance: "observer-test"}
			start, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: graphical}, caller)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("synthetic exact-origin observation failure")
			handle.sessionErr = failure
			input := daemonapi.TerminalLoginInput{Account: account, SessionID: start.SessionID, Graphical: graphical}
			if !graphical {
				input.Action = &daemonapi.TerminalLoginAction{Type: "refresh"}
			}
			result, err := backend.TerminalLogin(t.Context(), input, caller)
			if !errors.Is(err, failure) || result.Status == "pending" || !handle.closed || len(backend.terminalAccounts) != 0 || len(backend.terminalSessions) != 0 {
				t.Fatalf("observation failure retained pending login: %+v %v", result, err)
			}
		})
	}
}
