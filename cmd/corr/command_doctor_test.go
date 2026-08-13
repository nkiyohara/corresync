package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

func allowDoctorBrowserProbe(t *testing.T, app *runtime, expected string) {
	t.Helper()
	app.probeBrowser = func(_ context.Context, configured string) (string, error) {
		if configured != expected {
			t.Fatalf("browser probe configured path = %q, want %q", configured, expected)
		}
		return expected, nil
	}
}

func TestDoctorOfflineProducesContentFreeHealthyReport(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	executable := filepath.Join(root, "chromium")
	// #nosec G306 -- the owner-only test fixture must be executable.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Browser.Executable = executable
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	app := newRuntime(context.Background(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	allowDoctorBrowserProbe(t, app, executable)
	app.endpoint = func(path string) (localipc.Endpoint, error) {
		return localipc.ResolveInState(path, filepath.Join(root, "state"))
	}
	command := doctorCommand{JSON: true}
	if err := command.Run(app); err != nil {
		t.Fatalf("doctor.Run() error = %v", err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if !report.Healthy || report.Online ||
		report.Account != string(configuration.Accounts[configuration.DefaultAccount].ID) {
		t.Fatalf("unexpected doctor report: %+v", report)
	}
	if len(report.Checks) != 7 ||
		report.Checks[2].Name != "update" ||
		report.Checks[5].Name != "daemon" ||
		report.Checks[5].Status != "skip" ||
		report.Checks[6].Status != "skip" {
		t.Fatalf("unexpected doctor checks: %+v", report.Checks)
	}
}

func TestDoctorDoesNotRequireBrowserForStandardsRoutes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configuration := config.OutlookDefault()
	account := configuration.Accounts[configuration.DefaultAccount]
	account.Address = "reader@example.invalid"
	account.Mail = &config.MailRoute{
		Provider: domain.ProviderJMAP,
		JMAP: &config.JMAPRoute{
			SessionURL: "https://mail.example.invalid/.well-known/jmap",
			Username:   "reader@example.invalid",
			Credential: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     "test-jmap",
				Consent: true,
			},
		},
	}
	account.Calendar = nil
	configuration.Accounts[configuration.DefaultAccount] = account
	configuration.Browser.Executable = filepath.Join(root, "missing-browser")
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	app := newRuntime(
		context.Background(),
		configPath,
		&stdout,
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	app.probeBrowser = func(context.Context, string) (string, error) {
		t.Fatal("standards route unexpectedly probed Chromium")
		return "", nil
	}
	app.endpoint = func(path string) (localipc.Endpoint, error) {
		return localipc.ResolveInState(path, filepath.Join(root, "state"))
	}
	if err := (&doctorCommand{JSON: true}).Run(app); err != nil {
		t.Fatalf("doctor.Run() error = %v", err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if !report.Healthy {
		t.Fatalf("doctor report = %+v", report)
	}
	for _, check := range report.Checks {
		if check.Name == "browser" {
			if check.Status != "skip" {
				t.Fatalf("browser check = %+v", check)
			}
			return
		}
	}
	t.Fatalf("doctor report lacks browser check: %+v", report.Checks)
}

func TestDoctorOfflineRejectsIncompatibleRunningDaemon(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		protocol int
		version  string
	}{
		{name: "protocol", protocol: daemonapi.ProtocolVersion - 1, version: "0.5.0"},
		{name: "binary", protocol: daemonapi.ProtocolVersion, version: "0.5.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			executable := filepath.Join(root, "chromium")
			// #nosec G306 -- the owner-only test fixture must be executable.
			if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
				t.Fatal(err)
			}
			configuration := config.OutlookDefault()
			configuration.Browser.Executable = executable
			configPath := filepath.Join(root, "config.toml")
			if err := config.Save(configPath, configuration); err != nil {
				t.Fatal(err)
			}
			configDigest, err := config.Fingerprint(configPath)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			previous := startLifecycleTestDaemon(
				t.Context(),
				t,
				endpoint,
				test.protocol,
				test.version,
				321,
				configDigest,
			)
			t.Cleanup(previous.stop)

			var stdout bytes.Buffer
			app := newRuntime(
				t.Context(),
				configPath,
				&stdout,
				&bytes.Buffer{},
				buildinfo.Current(),
			)
			allowDoctorBrowserProbe(t, app, executable)
			app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
			command := doctorCommand{JSON: true}
			if err := command.Run(app); err == nil {
				t.Fatal("doctor unexpectedly accepted an incompatible daemon")
			}
			var report doctorReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode doctor output: %v", err)
			}
			if report.Healthy {
				t.Fatalf("doctor report is unexpectedly healthy: %+v", report)
			}
			for _, check := range report.Checks {
				if check.Name == "daemon" && check.Status == "fail" {
					return
				}
			}
			t.Fatalf("doctor report lacks a daemon failure: %+v", report.Checks)
		})
	}
}

func TestDoctorReportsSplitOwnersWithRunnableRecovery(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "chromium")
	// #nosec G306 -- the owner-only test fixture must be executable.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Browser.Executable = executable
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	stateDirectory := filepath.Join(root, "state")
	current, err := localipc.ResolveInState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	previous := previousRuntimeTestEndpoint(t, configPath, stateDirectory)
	oldOwner := startLifecycleTestDaemon(
		t.Context(), t, previous, daemonapi.ProtocolVersion, "0.8.3", 123, configDigest,
	)
	t.Cleanup(oldOwner.stop)
	canonicalOwner := startLifecycleTestDaemon(
		t.Context(), t, current, daemonapi.ProtocolVersion, "0.8.3", 456, configDigest,
	)
	t.Cleanup(canonicalOwner.stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	allowDoctorBrowserProbe(t, app, executable)
	app.endpoint = func(string) (localipc.Endpoint, error) { return current, nil }
	app.previousEndpoints = func(string) ([]localipc.Endpoint, error) {
		return []localipc.Endpoint{previous}, nil
	}
	if err := (&doctorCommand{JSON: true}).Run(app); err == nil {
		t.Fatal("doctor unexpectedly accepted split session owners")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Name == "daemon" {
			if check.Status != "fail" ||
				!strings.Contains(check.Detail, "multiple session owners") ||
				!strings.Contains(check.Detail, "corr daemon stop") {
				t.Fatalf("daemon check = %+v", check)
			}
			return
		}
	}
	t.Fatalf("doctor report lacks daemon check: %+v", report.Checks)
}

func TestDoctorReportsInvalidConfigBeforeOnlineWork(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(
		context.Background(),
		filepath.Join(t.TempDir(), "missing.toml"),
		&stdout,
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	command := doctorCommand{Online: true, JSON: true}
	if err := command.Run(app); err == nil {
		t.Fatal("doctor.Run() unexpectedly accepted a missing config")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Healthy || len(report.Checks) != 1 || report.Checks[0].Name != "config" {
		t.Fatalf("unexpected doctor failure report: %+v", report)
	}
}

func TestDoctorOnlineRequiresAnExistingSessionWithoutStartingLogin(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	executable := filepath.Join(root, "chromium")
	// #nosec G306 -- the owner-only synthetic executable is intentional.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Browser.Executable = executable
	configuration.Updates.DisableAutomaticChecks = true
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := localipc.ResolveInState(
		configPath,
		filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(
		t.Context(),
		configPath,
		&stdout,
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	allowDoctorBrowserProbe(t, app, executable)
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	daemon := startLifecycleTestDaemon(
		t.Context(),
		t,
		endpoint,
		daemonapi.ProtocolVersion,
		app.info.Version,
		321,
		configDigest,
	)
	t.Cleanup(daemon.stop)

	if err := (&doctorCommand{Online: true, JSON: true}).Run(app); err == nil {
		t.Fatal("doctor online unexpectedly accepted a signed-out session")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	for _, check := range report.Checks {
		if check.Name != "session" {
			continue
		}
		if check.Status != "fail" ||
			!bytes.Contains([]byte(check.Detail), []byte("corr auth login --account work")) ||
			bytes.Contains([]byte(check.Detail), []byte("unsupported lifecycle test method")) {
			t.Fatalf("session check = %+v", check)
		}
		return
	}
	t.Fatalf("doctor report lacks session check: %+v", report.Checks)
}

func TestDoctorRejectsChromiumWithoutLinuxSandbox(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	executable := filepath.Join(root, "private", "chrome")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	// #nosec G306 -- the owner-only synthetic executable is intentional.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.OutlookDefault()
	configuration.Browser.Executable = executable
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.probeBrowser = func(context.Context, string) (string, error) {
		return "", browser.ErrLinuxSandboxUnavailable
	}
	app.endpoint = func(path string) (localipc.Endpoint, error) {
		return localipc.ResolveInState(path, filepath.Join(root, "state"))
	}
	if err := (&doctorCommand{JSON: true}).Run(app); err == nil {
		t.Fatal("doctor accepted a browser that cannot start its sandbox")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if check.Name != "browser" {
			continue
		}
		if check.Status != "fail" ||
			!strings.Contains(check.Detail, "AppArmor policy") ||
			!strings.Contains(check.Detail, "will not disable the sandbox") ||
			strings.Contains(check.Detail, executable) {
			t.Fatalf("browser check = %+v", check)
		}
		return
	}
	t.Fatalf("doctor report lacks browser failure: %+v", report.Checks)
}
