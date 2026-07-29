package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

type logoutTestCloser struct {
	calls atomic.Int32
}

func (closer *logoutTestCloser) Close() error {
	closer.calls.Add(1)
	return nil
}

func TestSessionBackendReportsContentFreeAccountState(t *testing.T) {
	t.Parallel()

	capturedAt := time.Unix(42, 0).UTC()
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{
				"work": {
					ID: "acc_00000000000000000000000000000001",
					Mail: &config.MailRoute{
						Provider: domain.ProviderIMAPSMTP,
					},
					Calendar: &config.CalendarRoute{
						Provider: domain.ProviderCalDAV,
					},
				},
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
		result.Accounts[2].Provider != domain.ProviderIMAPSMTP ||
		result.Accounts[2].MailProvider != domain.ProviderIMAPSMTP ||
		result.Accounts[2].CalendarProvider != domain.ProviderCalDAV ||
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
		report.Accounts[0].MailProvider != string(domain.ProviderMicrosoftOWA) ||
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

func TestAuthLogoutClosesOnlySelectedAccount(t *testing.T) {
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
		4444,
		digest,
	)
	t.Cleanup(daemon.stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, info)
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	if err := (&authLogoutCommand{Account: "work", JSON: true}).Run(app); err != nil {
		t.Fatalf("targeted auth logout error = %v", err)
	}
	var report struct {
		LoggedOut bool   `json:"loggedOut"`
		Scope     string `json:"scope"`
		Account   string `json:"account"`
		Alias     string `json:"alias"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode targeted auth logout: %v", err)
	}
	if !report.LoggedOut ||
		report.Scope != "account" ||
		report.Account != string(adapterTestAccountID) ||
		report.Alias != "work" ||
		daemon.shutdowns.Load() != 0 {
		t.Fatalf(
			"targeted auth logout = %+v; shutdowns = %d",
			report,
			daemon.shutdowns.Load(),
		)
	}
	select {
	case <-daemon.stopped:
		t.Fatal("targeted auth logout stopped the session owner")
	default:
	}
}

func TestSessionBackendLogoutIsolatesAccountsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	const (
		workID     domain.AccountID = "acc_00000000000000000000000000000001"
		personalID domain.AccountID = "acc_00000000000000000000000000000002"
	)
	workCloser := &logoutTestCloser{}
	personalCloser := &logoutTestCloser{}
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{
				"work":     {ID: workID},
				"personal": {ID: personalID},
			},
		},
		accounts: map[domain.AccountID]sessionAccount{
			workID: {
				closers:  []sessionCloser{workCloser},
				captured: time.Unix(1, 0).UTC(),
				usage:    newAccountUsage(),
			},
			personalID: {
				closers:  []sessionCloser{personalCloser},
				captured: time.Unix(2, 0).UTC(),
				usage:    newAccountUsage(),
			},
		},
		previews: map[string]sessionPreview{
			"work-preview": {
				account: workID, expiresAt: time.Now().Add(time.Minute),
			},
			"personal-preview": {
				account: personalID, expiresAt: time.Now().Add(time.Minute),
			},
		},
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	result, err := backend.Logout(t.Context(), workID, caller)
	if err != nil || result.Account != workID || !result.LoggedOut {
		t.Fatalf("Logout(work) = %+v, %v", result, err)
	}
	if workCloser.calls.Load() != 1 || personalCloser.calls.Load() != 0 {
		t.Fatalf(
			"closer calls: work=%d personal=%d",
			workCloser.calls.Load(),
			personalCloser.calls.Load(),
		)
	}
	if _, exists := backend.accounts[workID]; exists {
		t.Fatal("logged-out work account remains active")
	}
	if _, exists := backend.accounts[personalID]; !exists {
		t.Fatal("personal account was removed by work logout")
	}
	if _, exists := backend.previews["work-preview"]; exists {
		t.Fatal("work preview survived account logout")
	}
	if _, exists := backend.previews["personal-preview"]; !exists {
		t.Fatal("personal preview was removed by work logout")
	}
	status, err := backend.SessionStatus(t.Context(), caller)
	if err != nil || len(status.Accounts) != 2 ||
		status.Accounts[0].Alias != "personal" ||
		!status.Accounts[0].Authenticated ||
		status.Accounts[1].Alias != "work" ||
		status.Accounts[1].Authenticated {
		t.Fatalf("SessionStatus() = %+v, %v", status, err)
	}
	if _, err := backend.Logout(t.Context(), workID, caller); err != nil {
		t.Fatalf("repeated Logout(work) error = %v", err)
	}
	if workCloser.calls.Load() != 1 {
		t.Fatalf("repeated logout closed work %d times", workCloser.calls.Load())
	}
	_, err = backend.Logout(
		t.Context(),
		personalID,
		domain.Caller{Surface: "mcp", Instance: "client-1"},
	)
	if err == nil || !strings.Contains(err.Error(), "explicit local CLI") {
		t.Fatalf("MCP Logout(personal) error = %v", err)
	}
	if personalCloser.calls.Load() != 0 {
		t.Fatal("rejected MCP logout closed the personal account")
	}
}

func TestSessionBackendLogoutDrainsActiveAccountOperation(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	closer := &logoutTestCloser{}
	usage := newAccountUsage()
	if err := usage.begin(); err != nil {
		t.Fatal(err)
	}
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{"work": {ID: accountID}},
		},
		accounts: map[domain.AccountID]sessionAccount{
			accountID: {
				closers: []sessionCloser{closer},
				usage:   usage,
			},
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := backend.Logout(
			t.Context(),
			accountID,
			domain.Caller{Surface: "cli", Instance: "process-1"},
		)
		result <- err
	}()

	deadline := time.After(time.Second)
	for {
		err := usage.begin()
		if err != nil {
			if !strings.Contains(err.Error(), "logout is in progress") {
				t.Fatalf("begin during logout error = %v", err)
			}
			break
		}
		usage.end()
		select {
		case <-deadline:
			t.Fatal("logout did not close the account usage gate")
		default:
		}
	}
	if closer.calls.Load() != 0 {
		t.Fatal("account closed before its active operation drained")
	}
	select {
	case err := <-result:
		t.Fatalf("logout returned before active operation drained: %v", err)
	default:
	}
	usage.end()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Logout() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("logout did not finish after active operation drained")
	}
	if closer.calls.Load() != 1 {
		t.Fatalf("account closer calls = %d, want 1", closer.calls.Load())
	}
}
