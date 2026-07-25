package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
)

func TestSessionBackendReportsContentFreeAccountState(t *testing.T) {
	t.Parallel()

	capturedAt := time.Unix(42, 0).UTC()
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{
				"work":     {Origin: "https://outlook.cloud.microsoft"},
				"personal": {Origin: "https://outlook.office.com"},
				"pending":  {Origin: "https://outlook.cloud.microsoft"},
			},
		},
		accounts: map[domain.AccountID]sessionAccount{
			"work": {captured: capturedAt},
		},
		terminalAccounts: map[domain.AccountID]string{
			"pending": "tls1_synthetic",
		},
	}
	result, err := backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if len(result.Accounts) != 3 ||
		result.Accounts[0].Account != "pending" ||
		result.Accounts[0].State != "pending" ||
		result.Accounts[1].Account != "personal" ||
		result.Accounts[1].State != "signed_out" ||
		result.Accounts[2].Account != "work" ||
		result.Accounts[2].State != "authenticated" ||
		result.Accounts[2].CapturedAt == nil ||
		!result.Accounts[2].CapturedAt.Equal(capturedAt) {
		t.Fatalf("SessionStatus() = %+v", result)
	}
}

func TestAuthStatusReportsContentFreeDaemonState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	digest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	info := buildinfo.Current()
	daemon := startLifecycleTestDaemon(
		t.Context(),
		t,
		endpoint,
		daemonapi.ProtocolVersion,
		info.Version,
		4242,
		digest,
	)
	t.Cleanup(daemon.stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, info)
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	if err := (&authStatusCommand{JSON: true}).Run(app); err != nil {
		t.Fatalf("auth status error = %v", err)
	}
	var report authStatusReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode auth status: %v", err)
	}
	if report.ProcessID != 4242 ||
		len(report.Accounts) != 1 ||
		report.Accounts[0].Account != "work" ||
		report.Accounts[0].State != "signed_out" ||
		report.Accounts[0].CapturedAt != "" {
		t.Fatalf("auth status = %+v", report)
	}
}

func TestAuthLogoutStopsSessionOwner(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	digest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	info := buildinfo.Current()
	daemon := startLifecycleTestDaemon(
		t.Context(),
		t,
		endpoint,
		daemonapi.ProtocolVersion,
		info.Version,
		4343,
		digest,
	)
	t.Cleanup(daemon.stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, info)
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	if err := (&authLogoutCommand{JSON: true}).Run(app); err != nil {
		t.Fatalf("auth logout error = %v", err)
	}
	var report struct {
		LoggedOut bool   `json:"loggedOut"`
		Scope     string `json:"scope"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode auth logout: %v", err)
	}
	if !report.LoggedOut || report.Scope != "all" || daemon.shutdowns.Load() != 1 {
		t.Fatalf("auth logout = %+v; shutdowns = %d", report, daemon.shutdowns.Load())
	}
}
