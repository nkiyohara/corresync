package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/localipc"
)

func TestDaemonStatusReportsBoundedUnavailableReason(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(root, "state"))
	configPath := filepath.Join(root, "private-config-name.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(
		t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current(),
	)
	command := daemonStatusCommand{JSON: true}
	err := command.Run(app)
	if err == nil || !strings.Contains(err.Error(), "missing_credential") {
		t.Fatalf("Run() error = %v, want missing credential category", err)
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), configPath) {
		t.Fatalf("Run() exposed private path: %v", err)
	}
	var result daemonUnavailableResult
	if decodeErr := json.Unmarshal(stdout.Bytes(), &result); decodeErr != nil {
		t.Fatalf("decode status JSON: %v; output=%q", decodeErr, stdout.String())
	}
	if result.State != "unavailable" || result.Reason != string(localipc.CredentialMissing) {
		t.Fatalf("status JSON = %+v", result)
	}
}

func TestDaemonUnavailableReasonDistinguishesAuthentication(t *testing.T) {
	if got := daemonUnavailableReason(&daemonapi.Error{
		Code: "unauthorized", Message: "daemon authorization failed",
	}); got != "authentication_failed" {
		t.Fatalf("daemonUnavailableReason() = %q", got)
	}
}
