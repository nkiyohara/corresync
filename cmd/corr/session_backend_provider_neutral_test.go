package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestSessionBackendSupportsProviderNeutralEmptyConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(root, "state"))
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(
		t.Context(),
		configPath,
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	backend, err := newSessionBackend(app)
	if err != nil {
		t.Fatalf("newSessionBackend() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if backend.DefaultAccount() != "" {
		t.Fatalf("DefaultAccount() = %q, want empty", backend.DefaultAccount())
	}
	status, err := backend.SessionStatus(t.Context(), app.caller())
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if len(status.Accounts) != 0 {
		t.Fatalf("SessionStatus() accounts = %+v, want empty", status.Accounts)
	}
}

func TestSessionBackendRejectsInvalidInputBeforeSessionResolution(t *testing.T) {
	backend := &sessionBackend{configuration: config.Default()}
	caller := domain.Caller{Surface: "mcp", Instance: "validation-test"}
	tests := []struct {
		name string
		call func() error
	}{
		{"mail read", func() error {
			_, err := backend.ListMail(t.Context(), application.MailListInput{}, caller)
			return err
		}},
		{"mail write", func() error {
			_, err := backend.CreateMailDraft(t.Context(), application.MailDraftInput{}, caller)
			return err
		}},
		{"calendar read", func() error {
			_, err := backend.ListCalendar(t.Context(), application.CalendarListInput{}, caller)
			return err
		}},
		{"calendar write", func() error {
			_, err := backend.CreateCalendar(t.Context(), application.CalendarCreateInput{}, caller)
			return err
		}},
		{"task read", func() error {
			_, err := backend.ListTasks(t.Context(), application.TaskReadInput{}, caller)
			return err
		}},
		{"task write", func() error {
			_, err := backend.CreateTask(t.Context(), application.TaskCreateInput{}, caller)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil || !strings.Contains(err.Error(), "account") {
				t.Fatalf("invalid input error = %v, want account validation", err)
			}
			if strings.Contains(err.Error(), "authentication") ||
				strings.Contains(err.Error(), "session") {
				t.Fatalf("invalid input reached session resolution: %v", err)
			}
		})
	}
}
