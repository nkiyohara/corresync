package daemonapi

import (
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestTerminalLoginInputValidation(t *testing.T) {
	t.Parallel()

	sessionID := "tls1_" + strings.Repeat("a", 32)
	valid := []TerminalLoginInput{
		{Account: "work"},
		{Account: "work", SessionID: sessionID, Action: &TerminalLoginAction{Type: "refresh"}},
		{Account: "work", SessionID: sessionID, Action: &TerminalLoginAction{
			Type: "activate", ControlID: "control-1",
		}},
		{Account: "work", SessionID: sessionID, Action: &TerminalLoginAction{
			Type: "key", ControlID: "control-1", Key: "ø",
		}},
	}
	for _, input := range valid {
		if err := input.validate(); err != nil {
			t.Fatalf("validate(%+v) error = %v", input, err)
		}
	}

	invalid := []TerminalLoginInput{
		{},
		{Account: "work", Action: &TerminalLoginAction{Type: "refresh"}},
		{Account: "work", SessionID: "invalid", Action: &TerminalLoginAction{Type: "refresh"}},
		{Account: "work", SessionID: sessionID},
		{Account: "work", SessionID: sessionID, Action: &TerminalLoginAction{
			Type: "key", ControlID: "control-1", Key: "secret",
		}},
		{Account: "work", SessionID: sessionID, Action: &TerminalLoginAction{
			Type: "key", ControlID: "../../control-1", Key: "a",
		}},
	}
	for _, input := range invalid {
		if err := input.validate(); err == nil {
			t.Fatalf("validate(%+v) unexpectedly succeeded", input)
		}
	}
}

func TestValidateTerminalLoginResult(t *testing.T) {
	t.Parallel()

	sessionID := "tls1_" + strings.Repeat("b", 32)
	start := TerminalLoginInput{Account: "work"}
	pending := TerminalLoginResult{
		Account: "work", SessionID: sessionID, Status: "pending",
		View: &TerminalLoginView{Controls: []TerminalLoginControl{{
			ID: "control-1", Kind: "input", Name: "Email",
		}}},
	}
	if err := validateTerminalLoginResult(start, pending); err != nil {
		t.Fatalf("pending result error = %v", err)
	}
	authenticated := TerminalLoginResult{
		Account: "work", Status: "authenticated", CapturedAt: time.Unix(2, 0),
	}
	if err := validateTerminalLoginResult(start, authenticated); err != nil {
		t.Fatalf("authenticated result error = %v", err)
	}

	pending.View.Controls[0].ID = "invalid"
	if err := validateTerminalLoginResult(start, pending); err == nil {
		t.Fatal("invalid terminal control unexpectedly accepted")
	}
	if err := validateTerminalLoginResult(start, TerminalLoginResult{
		Account: "other", Status: "authenticated", CapturedAt: time.Unix(2, 0),
	}); err == nil {
		t.Fatal("different account unexpectedly accepted")
	}
}

func TestValidateGraphicalLoginResult(t *testing.T) {
	t.Parallel()

	sessionID := "tls1_" + strings.Repeat("c", 32)
	frame := func(mutate func(*application.GraphicalLoginFrame)) *application.GraphicalLoginFrame {
		result := &application.GraphicalLoginFrame{
			JPEG: []byte("synthetic"), Origin: "https://login.example",
			Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight,
		}
		if mutate != nil {
			mutate(result)
		}
		return result
	}
	view := &TerminalLoginView{Controls: []TerminalLoginControl{}}
	graphical := TerminalLoginInput{Account: "work", Graphical: true}
	terminal := TerminalLoginInput{Account: "work"}
	for _, test := range []struct {
		name   string
		input  TerminalLoginInput
		result TerminalLoginResult
		valid  bool
	}{
		{"frame without view", graphical, TerminalLoginResult{Status: "pending", Frame: frame(nil)}, true},
		{"no frame while navigating", graphical, TerminalLoginResult{Status: "pending"}, true},
		{"graphical with text view", graphical, TerminalLoginResult{Status: "pending", Frame: frame(nil), View: view}, false},
		{"terminal with frame", terminal, TerminalLoginResult{Status: "pending", Frame: frame(nil), View: view}, false},
		{"terminal without view", terminal, TerminalLoginResult{Status: "pending"}, false},
		{"empty frame", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) { f.JPEG = nil })}, false},
		{"oversized frame", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) {
			f.JPEG = make([]byte, application.MaxGraphicalLoginFrameBytes+1)
		})}, false},
		{"plain HTTP origin", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) { f.Origin = "http://login.example" })}, false},
		{"origin with path", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) { f.Origin = "https://login.example/authorize?code=1" })}, false},
		{"origin with user", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) { f.Origin = "https://user@login.example" })}, false},
		{"wrong viewport", graphical, TerminalLoginResult{Status: "pending", Frame: frame(func(f *application.GraphicalLoginFrame) { f.Width = 1 })}, false},
		{"authenticated with frame", graphical, TerminalLoginResult{Status: "authenticated", CapturedAt: time.Unix(2, 0), Frame: frame(nil)}, false},
		{"cancelled with frame", graphical, TerminalLoginResult{Status: "cancelled", Frame: frame(nil)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.result.Account = "work"
			if test.result.Status == "pending" {
				test.result.SessionID = sessionID
			}
			if err := validateTerminalLoginResult(test.input, test.result); (err == nil) != test.valid {
				t.Fatalf("validateTerminalLoginResult() error = %v, want valid=%v", err, test.valid)
			}
		})
	}
}
