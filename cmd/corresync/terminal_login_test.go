package main

import (
	"bytes"
	"strings"
	"testing"

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
