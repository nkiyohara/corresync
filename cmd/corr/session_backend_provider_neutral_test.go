package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
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
