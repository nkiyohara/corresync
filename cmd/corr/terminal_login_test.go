package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestWriteTerminalLoginViewMarksSensitiveInputs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := &runtime{stdout: &stdout}
	err := writeTerminalLoginView(app, daemonapi.TerminalLoginView{
		Origin: "https://login.example",
		Title:  "Sign in",
		Text:   "Continue to Outlook",
		Controls: []daemonapi.TerminalLoginControl{
			{ID: "control-1", Kind: "input", Name: "Password", Sensitive: true},
			{ID: "control-2", Kind: "activate", Name: "Next"},
		},
	})
	if err != nil {
		t.Fatalf("writeTerminalLoginView() error = %v", err)
	}
	output := stdout.String()
	for _, expected := range []string{
		"Sign in", "Origin: https://login.example", "[1] Password (input, hidden input)", "[2] Next (activate)",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output missing %q:\n%s", expected, output)
		}
	}
}

func TestLoginRejectsTerminalJSONBeforeLoadingConfig(t *testing.T) {
	t.Parallel()

	command := loginCommand{Terminal: true, JSON: true}
	err := command.Run(&runtime{})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestLoginExplainsTerminalFallbackWithoutGraphicalSession(t *testing.T) {
	if stdruntime.GOOS != "linux" {
		t.Skip("graphical display environment variables are Linux-specific")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(
		t.Context(),
		configPath,
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	err := (&loginCommand{Account: "work"}).Run(app)
	if err == nil {
		t.Fatal("Run() unexpectedly started a visible browser without a display")
	}
	for _, expected := range []string{
		"DISPLAY and WAYLAND_DISPLAY are both unset",
		"corr auth login --account 'work' --terminal",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Run() error missing %q: %v", expected, err)
		}
	}
}

func TestTerminalLoginViewUnchanged(t *testing.T) {
	t.Parallel()

	view := daemonapi.TerminalLoginView{
		Origin: "https://login.example", Title: "Sign in", Text: "Pick an account",
		Controls: []daemonapi.TerminalLoginControl{{
			ID: "control-1", Kind: "activate", Name: "Work account",
		}},
	}
	if !terminalLoginViewUnchanged(&view, daemonapi.TerminalLoginResult{
		Status: "pending", View: &view,
	}) {
		t.Fatal("identical pending terminal view was not recognized")
	}
	changed := view
	changed.Title = "Enter password"
	if terminalLoginViewUnchanged(&view, daemonapi.TerminalLoginResult{
		Status: "pending", View: &changed,
	}) {
		t.Fatal("changed terminal view was reported as unchanged")
	}
}

func inputFromText(text string) *terminalInput {
	events := make(chan terminalRune, len([]rune(text)))
	for _, value := range text {
		events <- terminalRune{value: value}
	}
	close(events)
	return &terminalInput{events: events}
}

type terminalLoginClientStub struct {
	actions []daemonapi.TerminalLoginInput
	fail    string
}

func (client *terminalLoginClientStub) TerminalLogin(_ context.Context, input daemonapi.TerminalLoginInput, _ domain.Caller) (daemonapi.TerminalLoginResult, error) {
	client.actions = append(client.actions, input)
	if input.Action != nil && input.Action.Type == client.fail {
		return daemonapi.TerminalLoginResult{}, errors.New("synthetic action failure")
	}
	if input.Action != nil && input.Action.Type == "cancel" {
		return daemonapi.TerminalLoginResult{Status: "cancelled", Account: input.Account}, nil
	}
	return daemonapi.TerminalLoginResult{Account: input.Account, Status: "pending", SessionID: "test-session", View: &daemonapi.TerminalLoginView{
		Title: "Password", Controls: []daemonapi.TerminalLoginControl{{ID: "control-1", Kind: "input", Name: "Password", Sensitive: true}},
	}}, nil
}

func TestTerminalPasswordTypingDoesNotExitOnArrowOrLeakKeys(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	client := &terminalLoginClientStub{}
	app := &runtime{context: t.Context(), stdout: terminalOutput{&output}}
	err := runTerminalLoginLoop(app, client, "test-account", inputFromText("1\rAb!\x1b[D9\x7fZ\x1b"))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("loop error = %v", err)
	}
	var keys []string
	for _, call := range client.actions {
		if call.Action != nil && call.Action.Type == "key" {
			keys = append(keys, call.Action.Key)
		}
	}
	if !reflect.DeepEqual(keys, []string{"A", "b", "!", "9", "backspace", "Z"}) {
		t.Fatalf("relayed keys = %#v", keys)
	}
	if strings.Contains(output.String(), "Ab!") || strings.Contains(output.String(), "[D") {
		t.Fatal("password or escape suffix leaked into terminal output")
	}
	if strings.Contains(strings.ReplaceAll(output.String(), "\r\n", ""), "\n") {
		t.Fatal("raw output retained a bare newline")
	}
	last := client.actions[len(client.actions)-1]
	if last.Action.Type != "cancel" || last.SessionID != "test-session" {
		t.Fatalf("cleanup=%+v", last)
	}
}

func TestTerminalLoginCancelsOriginalSessionAfterFailedResponse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ action, input string }{{"focus", "1\r"}, {"key", "1\rX"}, {"refresh", "r\r"}, {"cancel", "q\r"}} {
		t.Run(test.action, func(t *testing.T) {
			client := &terminalLoginClientStub{fail: test.action}
			app := &runtime{context: t.Context(), stdout: io.Discard}
			if err := runTerminalLoginLoop(app, client, "test-account", inputFromText(test.input)); err == nil {
				t.Fatal("failed action succeeded")
			}
			last := client.actions[len(client.actions)-1]
			if last.Action == nil || last.Action.Type != "cancel" || last.SessionID != "test-session" || last.Account != "test-account" {
				t.Fatalf("lost cleanup identity: %+v", last)
			}
		})
	}
}

func TestTerminalInputConsumesSequencesAndBoundsMalformedInput(t *testing.T) {
	t.Parallel()
	for _, sequence := range []string{"\x1b[D", "\x1bOA", "\x1b[1;5C", "\x1b[[A", "\x1b[[B", "\x1b[[C", "\x1b[[D", "\x1b[[E"} {
		input := inputFromText(sequence + "x")
		key, err := input.readKey(t.Context())
		if key != 0 || err != nil {
			t.Fatalf("sequence key=%q err=%v", key, err)
		}
		key, err = input.readKey(t.Context())
		if key != 'x' || err != nil {
			t.Fatalf("next key=%q err=%v", key, err)
		}
	}
	for _, sequence := range []string{"\x1b[", "\x1b[" + strings.Repeat("1", 40)} {
		if _, err := inputFromText(sequence).readKey(t.Context()); err == nil {
			t.Fatal("malformed sequence succeeded")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	input := &terminalInput{events: make(chan terminalRune)}
	if _, err := input.readKey(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

type closeOnlyTerminalReader struct {
	started chan struct{}
	closed  chan struct{}
}

func (reader *closeOnlyTerminalReader) Read([]byte) (int, error) {
	close(reader.started)
	<-reader.closed
	return 0, io.EOF
}
func (*closeOnlyTerminalReader) Cancel() bool        { return false }
func (reader *closeOnlyTerminalReader) Close() error { close(reader.closed); return nil }

func TestTerminalInputClosesUncancellableReaderBeforeJoining(t *testing.T) {
	t.Parallel()
	source := &closeOnlyTerminalReader{started: make(chan struct{}), closed: make(chan struct{})}
	_, cleanup := startTerminalInput(source, func() {})
	<-source.started
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup could not stop reader: %v", err)
	}
}

type finiteTerminalReader struct{ io.Reader }

func (finiteTerminalReader) Cancel() bool { return true }
func (finiteTerminalReader) Close() error { return nil }

func TestTerminalInputDetectsInterruptWithoutConsumer(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"queued-key\x03", strings.Repeat("x", 65)} {
		ctx, cancel := context.WithCancel(t.Context())
		_, cleanup := startTerminalInput(finiteTerminalReader{strings.NewReader(text)}, cancel)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Error("queued input blocked cancellation")
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}
