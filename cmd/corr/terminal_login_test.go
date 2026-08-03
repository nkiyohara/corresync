package main

import (
	"bytes"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
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
