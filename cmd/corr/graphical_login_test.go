package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
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
	if len(args) != 5 || args[4] != graphicalRemoteCommand+shellSingleQuote("work'; echo bad; '") {
		t.Fatal(args)
	}
}

// A non-interactive SSH command does not read the login profile, so corr in
// the installer's default directory is not on PATH. Run the exact remote
// command line through sh with such a PATH and a hostile account alias.
func TestGraphicalRemoteCommandFindsInstalledCorrAndQuotesAccount(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the remote helper command targets a POSIX shell")
	}
	home := t.TempDir()
	directory := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor argument in \"$@\"; do printf '[%s]' \"$argument\"; done\n"
	if err := os.WriteFile(filepath.Join(directory, "corr"), []byte(script), 0o700); err != nil { // #nosec G306 -- synthetic executable in a private test directory.
		t.Fatal(err)
	}
	account := "work'; echo bad; '$(echo bad)"
	args, err := graphicalSSHArguments("host", account)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", args[4]) // #nosec G204 -- the command line under test, with a synthetic account.
	command.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	output, err := command.CombinedOutput()
	if want := "[auth][login][--web-stdio][--account][" + account + "]"; err != nil || string(output) != want {
		t.Fatalf("remote command output = %q, %v; want %q", output, err, want)
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

// runGraphicalUIForTest serves the login view and returns a function posting
// one action as the paired browser tab, plus the command's eventual result.
func runGraphicalUIForTest(t *testing.T, exchange loginui.Exchange, sshHost string, lifetime time.Duration) (post func(string) int, stdout, stderr *lockedBuffer, done chan error) {
	t.Helper()
	stdout, stderr = &lockedBuffer{}, &lockedBuffer{}
	app := &runtime{context: t.Context(), stdout: stdout, stderr: stderr}
	done = make(chan error, 1)
	// Port 0 lets the view pick a free port; the printed link reveals it.
	go func() { done <- serveGraphicalUI(t.Context(), app, exchange, 0, sshHost, lifetime) }()
	link := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/#([a-f0-9]{64})`)
	var match []string
	for deadline := time.Now().Add(5 * time.Second); match == nil; {
		if match = link.FindStringSubmatch(stdout.String()); match == nil {
			if time.Now().After(deadline) {
				t.Fatalf("no login link printed: %q", stdout.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	base := "http://127.0.0.1:" + match[1]
	post = func(body string) int {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/action", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", base)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+match[2])
		request.Header.Set("X-Login-Controller", strings.Repeat("a", 32))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}
	return post, stdout, stderr, done
}

func TestGraphicalUIFailedStartEndsTheCommandWithItsReason(t *testing.T) {
	failure := errors.New("synthetic start failure")
	post, _, stderr, done := runGraphicalUIForTest(t, func(context.Context, *application.GraphicalLoginAction) (loginui.Result, error) {
		return loginui.Result{}, failure
	}, "", time.Minute)
	if status := post(`{"type":"refresh"}`); status != http.StatusBadGateway {
		t.Fatalf("status = %d", status)
	}
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatalf("command error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command kept serving a login that never started")
	}
	// The command returns the reason itself; it must not also print it.
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestGraphicalUIPrintsForwardingStepsFailuresAndOutcome(t *testing.T) {
	for _, test := range []struct{ name, sshHost, final, outcome string }{
		{"remote host signs in", "", `{"type":"text","text":"synthetic input"}`, "Signed in. The account session stays on this host."},
		{"local helper cancels", "nvidia7", `{"type":"cancel"}`, "Login cancelled."},
	} {
		t.Run(test.name, func(t *testing.T) {
			post, stdout, stderr, done := runGraphicalUIForTest(t, func(_ context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
				switch {
				case action == nil:
					return loginui.Result{Status: "pending"}, nil
				case action.Type == "refresh":
					return loginui.Result{}, errors.New("synthetic read failure")
				case action.Type == "cancel":
					return loginui.Result{Status: "cancelled"}, nil
				}
				return loginui.Result{Status: "authenticated"}, nil
			}, test.sshHost, 5*time.Minute)
			for _, step := range []struct {
				body string
				want int
			}{{`{"type":"refresh"}`, 200}, {`{"type":"refresh"}`, 502}, {`{"type":"refresh"}`, 502}, {test.final, 200}} {
				if status := post(step.body); status != step.want {
					t.Fatalf("%s status = %d, want %d", step.body, status, step.want)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("command did not finish")
			}
			printed := stdout.String()
			expected := []string{"expires in", "5 minutes", test.outcome}
			if test.sshHost == "" {
				expected = append(expected, "ssh -N -L 127.0.0.1:", "your own computer")
			} else {
				expected = append(expected, "The account session stays on nvidia7.")
			}
			for _, text := range expected {
				if !strings.Contains(printed, text) {
					t.Errorf("stdout missing %q:\n%s", text, printed)
				}
			}
			if strings.Contains(printed, "synthetic input") || strings.Count(stderr.String(), "login view: synthetic read failure") != 1 {
				t.Fatalf("stdout=%q stderr=%q", printed, stderr.String())
			}
		})
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

// A --host login must survive an unreadable screen or a rejected input the
// way a local view does. Only a failed start or cancellation ends the helper.
func TestGraphicalWireKeepsTheLoginOpenAfterAFailedInteraction(t *testing.T) {
	failure := errors.New("synthetic unreadable screen")
	for _, test := range []struct {
		name     string
		requests []string
		want     []bool // Failed flag of each response
		ends     error
	}{
		{"failed start ends the helper", []string{"null", `{"type":"refresh"}`}, []bool{true}, failure},
		{"failed read and input keep it open", []string{"null", `{"type":"refresh"}`, `{"type":"click","x":1,"y":1}`, `{"type":"text","text":"synthetic"}`}, []bool{false, true, true, false}, nil},
		{"failed cancel ends the helper", []string{"null", `{"type":"cancel"}`, `{"type":"refresh"}`}, []bool{false, true}, failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input strings.Builder
			for _, action := range test.requests {
				input.WriteString(`{"version":1,"action":` + action + "}\n")
			}
			exchange := func(_ context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
				switch {
				case action == nil && test.want[0], action != nil && (action.Type == "refresh" || action.Type == "click" || action.Type == "cancel"):
					return loginui.Result{}, failure
				case action != nil && action.Type == "text":
					return loginui.Result{Status: "authenticated"}, nil
				}
				return loginui.Result{Status: "pending"}, nil
			}
			var output bytes.Buffer
			if err := serveGraphicalStdio(t.Context(), strings.NewReader(input.String()), &output, exchange); !errors.Is(err, test.ends) {
				t.Fatalf("helper ended with %v, want %v", err, test.ends)
			}
			decoder := json.NewDecoder(&output)
			for index, failed := range test.want {
				var got graphicalWireResponse
				if err := decoder.Decode(&got); err != nil || got.Failed != failed {
					t.Fatalf("response %d = %+v, %v; want failed=%v", index, got, err, failed)
				}
			}
			if decoder.More() {
				t.Fatal("helper answered after it should have ended")
			}
		})
	}
}

type fakeGraphicalBrowser struct {
	fakeTerminalBrowser
	inputs     int
	navigating bool
}

func (browser *fakeGraphicalBrowser) GraphicalLogin(_ context.Context, action application.GraphicalLoginAction) (application.GraphicalLoginFrame, error) {
	browser.inputs++
	if browser.navigating {
		return application.GraphicalLoginFrame{}, action.Validate()
	}
	return application.GraphicalLoginFrame{JPEG: []byte("synthetic"), Origin: "https://login.example", Width: 1280, Height: 900}, action.Validate()
}

func newGraphicalTestBackend(t *testing.T) (*sessionBackend, *[]*fakeGraphicalBrowser) {
	t.Helper()
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	lifecycle, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	handles := &[]*fakeGraphicalBrowser{}
	app := &runtime{launch: func(context.Context, browser.Options) (browserHandle, error) {
		handle := &fakeGraphicalBrowser{}
		*handles = append(*handles, handle)
		return handle, nil
	}}
	return &sessionBackend{app: app, configuration: config.OutlookDefault(), lifecycle: lifecycle, cancel: cancel, accounts: make(map[domain.AccountID]sessionAccount), previews: make(map[string]sessionPreview), terminalSessions: make(map[string]*terminalLoginSession), terminalAccounts: make(map[domain.AccountID]string)}, handles
}

// A lost terminal or SSH connection leaves an unfinished login behind. A new
// explicit start closes it and begins again; it never inherits the browser.
func TestNewLoginStartReplacesUnfinishedLoginWithoutInheritingIt(t *testing.T) {
	for _, test := range []struct {
		name      string
		graphical bool
	}{{"graphical replaces graphical", true}, {"terminal replaces graphical", false}} {
		t.Run(test.name, func(t *testing.T) {
			backend, handles := newGraphicalTestBackend(t)
			account := backend.configuration.Accounts["work"].ID
			lost := domain.Caller{Surface: "cli", Instance: "process-1"}
			first, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: true}, lost)
			if err != nil {
				t.Fatal(err)
			}
			next, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: test.graphical}, domain.Caller{Surface: "cli", Instance: "process-2"})
			if err != nil || next.Status != "pending" || next.SessionID == first.SessionID {
				t.Fatalf("replacement = %+v, %v", next, err)
			}
			if len(*handles) != 2 || !(*handles)[0].closed || (*handles)[1].closed {
				t.Fatalf("browsers = %d, first closed = %v", len(*handles), (*handles)[0].closed)
			}
			stale := daemonapi.TerminalLoginInput{Account: account, SessionID: first.SessionID, Graphical: true, GraphicalAction: &application.GraphicalLoginAction{Type: "text", Text: "synthetic input"}}
			if _, err := backend.TerminalLogin(t.Context(), stale, lost); err == nil || (*handles)[1].inputs > 1 {
				t.Fatalf("earlier caller reached the replacement browser: %v", err)
			}
			if len(backend.terminalSessions) != 1 || backend.terminalAccounts[account] != next.SessionID {
				t.Fatalf("sessions = %d", len(backend.terminalSessions))
			}
		})
	}
}

func TestGraphicalLoginStaysPendingWithoutFrameWhilePageNavigates(t *testing.T) {
	backend, handles := newGraphicalTestBackend(t)
	account := backend.configuration.Accounts["work"].ID
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	start, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, Graphical: true}, caller)
	if err != nil || start.Frame == nil {
		t.Fatalf("start = %+v, %v", start, err)
	}
	(*handles)[0].navigating = true
	result, err := backend.TerminalLogin(t.Context(), daemonapi.TerminalLoginInput{Account: account, SessionID: start.SessionID, Graphical: true, GraphicalAction: &application.GraphicalLoginAction{Type: "key", Key: "Enter"}}, caller)
	if err != nil || result.Status != "pending" || result.Frame != nil || result.SessionID != start.SessionID || (*handles)[0].closed {
		t.Fatalf("navigating = %+v, %v", result, err)
	}
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
