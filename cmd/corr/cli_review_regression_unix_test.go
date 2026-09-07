//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestAccessibleSettingsRetainsTTYForTerminalLogin(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	app := &runtime{context: t.Context(), stdin: slave, lookupEnv: func(name string) (string, bool) { return "true", name == "CORRESYNC_ACCESSIBLE" }}
	restore := prepareAccessibleSettingsInput(app)
	defer restore()
	input, err := interactiveTerminalInput(app)
	if err != nil {
		t.Fatal(err)
	}
	if input != slave {
		t.Fatal("terminal login did not retain original terminal file")
	}
	app.stdin = newSettingsAccessibleReader(t.Context(), strings.NewReader("synthetic input"))
	if _, err := interactiveTerminalInput(app); err == nil {
		t.Fatal("accessible wrapper authorized piped login input")
	}
}

type accessibleTerminalTransitionClient struct {
	focused chan struct{}
	keys    []string
}

func (client *accessibleTerminalTransitionClient) TerminalLogin(_ context.Context, input daemonapi.TerminalLoginInput, _ domain.Caller) (daemonapi.TerminalLoginResult, error) {
	if input.Action != nil {
		switch input.Action.Type {
		case "focus":
			close(client.focused)
		case "key":
			client.keys = append(client.keys, input.Action.Key)
			if input.Action.Key == "enter" {
				return daemonapi.TerminalLoginResult{Account: input.Account, Status: "authenticated"}, nil
			}
		case "cancel":
			return daemonapi.TerminalLoginResult{Account: input.Account, Status: "cancelled"}, nil
		}
	}
	return daemonapi.TerminalLoginResult{Account: input.Account, Status: "pending", SessionID: "synthetic-session", View: &daemonapi.TerminalLoginView{
		Title: "Synthetic sign-in", Controls: []daemonapi.TerminalLoginControl{{ID: "password", Kind: "input", Name: "Password", Sensitive: true}},
	}}, nil
}

func TestAccessibleMenuHandsTTYToTerminalLoginAndResumes(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	app := &runtime{context: ctx, stdin: slave, stdout: &output, lookupEnv: func(name string) (string, bool) { return "true", name == "CORRESYNC_ACCESSIBLE" }}
	restore := prepareAccessibleSettingsInput(app)
	defer restore()
	client := &accessibleTerminalTransitionClient{focused: make(chan struct{})}
	terminalDone := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		action, selected, err := runSettingsSelect(app, "Synthetic account menu", "Choose an action.", []huh.Option[string]{huh.NewOption("Sign in here", "login"), huh.NewOption("Finish", "finish")})
		if err != nil || !selected || action != "login" {
			done <- fmt.Errorf("initial selection=%q selected=%t: %w", action, selected, err)
			return
		}
		// Canonical TTY reads return one line. The settings reader must not have
		// prefetched the next line, which is deliberately already queued below.
		if buffered := app.stdin.(*settingsAccessibleReader).reader.Buffered(); buffered != 0 {
			done <- fmt.Errorf("settings prefetched %d terminal bytes", buffered)
			return
		}
		if err := runTerminalLogin(app, client, "test-account"); err != nil {
			done <- err
			return
		}
		close(terminalDone)
		action, selected, err = runSettingsSelect(app, "Synthetic account menu", "Choose an action.", []huh.Option[string]{huh.NewOption("Sign in here", "login"), huh.NewOption("Finish", "finish")})
		if err != nil || !selected || action != "finish" {
			done <- fmt.Errorf("resumed selection=%q selected=%t: %w", action, selected, err)
			return
		}
		done <- nil
	}()
	// Queue the settings selection and terminal control selection together.
	// This catches prefetch/drop across the accessible -> raw-mode handoff.
	if _, err := master.Write([]byte("1\n1\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.focused:
	case err := <-done:
		t.Fatalf("terminal did not focus control: %v", err)
	case <-ctx.Done():
		t.Fatal("settings reader stole or dropped the queued control selection")
	}
	if _, err := master.Write([]byte("Synthetic!\x1b[D9\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminalDone:
	case err := <-done:
		t.Fatalf("terminal failed: %v", err)
	case <-ctx.Done():
		t.Fatal("terminal did not receive its field input")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal was not restored before resuming settings: %v", err)
	}
	if _, err := master.Write([]byte("2\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("accessible settings did not regain terminal input")
	}
	want := []string{"S", "y", "n", "t", "h", "e", "t", "i", "c", "!", "9", "enter"}
	if !reflect.DeepEqual(client.keys, want) {
		t.Fatalf("terminal keys=%q, want=%q", client.keys, want)
	}
	if strings.Contains(output.String(), "Synthetic!9") {
		t.Fatal("sensitive input was echoed in terminal output")
	}
}
