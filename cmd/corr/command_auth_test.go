package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

func TestSessionBackendReportsContentFreeAccountState(t *testing.T) {
	t.Parallel()

	capturedAt := time.Unix(42, 0).UTC()
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{
				"work":     {ID: "acc_00000000000000000000000000000001"},
				"personal": {ID: "acc_00000000000000000000000000000002"},
				"pending":  {ID: "acc_00000000000000000000000000000003"},
			},
		},
		accounts: map[domain.AccountID]sessionAccount{
			"acc_00000000000000000000000000000001": {captured: capturedAt},
		},
		terminalAccounts: map[domain.AccountID]string{
			"acc_00000000000000000000000000000003": "tls1_synthetic",
		},
	}
	result, err := backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if len(result.Accounts) != 3 ||
		result.Accounts[0].Account != "acc_00000000000000000000000000000003" ||
		result.Accounts[0].State != "pending" ||
		result.Accounts[1].Account != "acc_00000000000000000000000000000002" ||
		result.Accounts[1].State != "signed_out" ||
		result.Accounts[2].Account != "acc_00000000000000000000000000000001" ||
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
		report.Accounts[0].Account != adapterTestAccountID ||
		report.Accounts[0].Alias != "work" ||
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
