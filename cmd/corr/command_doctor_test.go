package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

type doctorConnectionBackend struct {
	adapterTestBackend
	contentCalls int
}

type doctorTaskBackend struct {
	adapterTestBackend
	daemonapi.TaskBackend
	taskListCalls int
}

const doctorTaskAccountID domain.AccountID = "acc_00000000000000000000000000000002"

func (*doctorTaskBackend) SessionStatus(
	context.Context,
	domain.Caller,
) (daemonapi.SessionStatusResult, error) {
	capabilities := domain.Capabilities{Tasks: true}
	capturedAt := time.Unix(1, 0).UTC()
	return daemonapi.SessionStatusResult{Accounts: []daemonapi.SessionStatus{{
		Account: doctorTaskAccountID, Alias: "tasks",
		Provider: domain.ProviderCalDAV, TaskProvider: domain.ProviderCalDAV,
		State: "authenticated", Authenticated: true,
		CapturedAt: &capturedAt, Capabilities: &capabilities,
		Services: testAuthenticationStatuses(
			doctorTaskAccountID,
			"tasks",
			"",
			"",
			domain.ProviderCalDAV,
			true,
		),
	}}}, nil
}

func (backend *doctorTaskBackend) ListTaskLists(
	_ context.Context,
	input application.TaskListInput,
	_ domain.Caller,
) (application.TaskListPage, error) {
	backend.taskListCalls++
	if input.Account != doctorTaskAccountID || input.Limit != 1 {
		return application.TaskListPage{}, errors.New("unexpected task-list diagnostic input")
	}
	return application.TaskListPage{
		Lists: []application.TaskList{{ID: "synthetic-list", DisplayName: "private"}},
		Limit: 1,
	}, nil
}

func (*doctorConnectionBackend) SessionStatus(
	context.Context,
	domain.Caller,
) (daemonapi.SessionStatusResult, error) {
	capabilities := domain.Capabilities{Mail: true, Calendar: true, Tasks: true}
	capturedAt := time.Unix(1, 0).UTC()
	return daemonapi.SessionStatusResult{Accounts: []daemonapi.SessionStatus{{
		Account: adapterTestAccountID, Alias: "work",
		Provider:     domain.ProviderIMAPSMTP,
		MailProvider: domain.ProviderIMAPSMTP, CalendarProvider: domain.ProviderCalDAV,
		TaskProvider: domain.ProviderCalDAV,
		State:        "authenticated", Authenticated: true,
		CapturedAt: &capturedAt, Capabilities: &capabilities,
		Services: testAuthenticationStatuses(
			adapterTestAccountID,
			"work",
			domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV,
			domain.ProviderCalDAV,
			true,
		),
	}}}, nil
}

func (backend *doctorConnectionBackend) ListTaskLists(
	context.Context,
	application.TaskListInput,
	domain.Caller,
) (application.TaskListPage, error) {
	backend.contentCalls++
	return application.TaskListPage{Limit: 1}, errors.New("content-free check requested task lists")
}

func (backend *doctorConnectionBackend) ListMailFolders(
	context.Context,
	application.MailFolderListInput,
	domain.Caller,
) (application.MailFolderPage, error) {
	backend.contentCalls++
	return application.MailFolderPage{}, errors.New("content-free check requested mail folders")
}

func (backend *doctorConnectionBackend) ListMail(
	context.Context,
	application.MailListInput,
	domain.Caller,
) (application.MailPage, error) {
	backend.contentCalls++
	return application.MailPage{}, errors.New("content-free check requested mail")
}

func (backend *doctorConnectionBackend) ListCalendarFolders(
	context.Context,
	application.CalendarFolderListInput,
	domain.Caller,
) (application.CalendarFolderPage, error) {
	backend.contentCalls++
	return application.CalendarFolderPage{}, errors.New(
		"content-free check requested calendar folders",
	)
}

func (backend *doctorConnectionBackend) ListCalendar(
	context.Context,
	application.CalendarListInput,
	domain.Caller,
) (application.CalendarPage, error) {
	backend.contentCalls++
	return application.CalendarPage{}, errors.New("content-free check requested events")
}

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

func TestDoctorConnectionOnlyReportsServicesWithoutRemoteItemReads(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configuration := config.OutlookDefault()
	configuration.Updates.DisableAutomaticChecks = true
	account := configuration.Accounts[configuration.DefaultAccount]
	account.Address = "reader@icloud.com"
	shared := config.CredentialRef{
		Backend: config.CredentialOSKeyring, Key: "icloud-shared", Consent: true,
	}
	account.Mail = &config.MailRoute{
		Provider: domain.ProviderIMAPSMTP,
		IMAPSMTP: &config.IMAPSMTPRoute{
			IMAP: config.TLSEndpoint{
				Host: "imap.mail.me.com", Port: 993, Mode: config.TLSImplicit,
			},
			SMTP: config.TLSEndpoint{
				Host: "smtp.mail.me.com", Port: 587, Mode: config.TLSStartTLS,
			},
			Username: "reader@icloud.com", Credential: shared,
		},
	}
	account.Calendar = &config.CalendarRoute{
		Provider: domain.ProviderCalDAV,
		CalDAV: &config.CalDAVRoute{
			Endpoint: "https://caldav.icloud.com:443/",
			Username: "reader@icloud.com", Credential: shared,
		},
	}
	account.Tasks = &config.TaskRoute{
		Provider: domain.ProviderCalDAV,
		CalDAV: &config.CalDAVTaskRoute{
			Endpoint: "https://caldav.icloud.com:443/", TaskListPath: "/tasks/",
			Username: "reader@icloud.com", Credential: shared,
		},
	}
	configuration.Accounts[configuration.DefaultAccount] = account
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
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
	backend := &doctorConnectionBackend{}
	stop := startAdapterTestDaemon(t, endpoint, digest, backend)
	t.Cleanup(stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	runErr := (&doctorCommand{
		Online: true, ConnectionOnly: true, JSON: true,
	}).Run(app)
	if backend.contentCalls != 0 {
		t.Fatalf("connection-only doctor made %d remote item reads", backend.contentCalls)
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if runErr != nil || !report.Healthy {
		t.Fatalf("connection-only report = %+v, error = %v", report, runErr)
	}
	checks := make(map[string]doctorCheck)
	for _, check := range report.Checks {
		checks[check.Name] = check
	}
	for _, name := range []string{"mail_connection", "calendar_connection", "task_connection"} {
		if checks[name].Status != "pass" ||
			!strings.Contains(checks[name].Detail, "read no") {
			t.Fatalf("%s check = %+v", name, checks[name])
		}
	}
	for _, forbidden := range []string{
		"folder_contract", "mail_contract", "calendar_folder_contract", "calendar_contract",
		"task_list_contract",
	} {
		if _, exists := checks[forbidden]; exists {
			t.Fatalf("connection-only report contains %s: %+v", forbidden, report.Checks)
		}
	}
}

func TestDoctorOnlineValidatesTaskListContractWithoutEmittingProviderData(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configuration := config.Default()
	configuration.DefaultAccount = "tasks"
	configuration.Updates.DisableAutomaticChecks = true
	configuration.Accounts["tasks"] = config.Account{
		ID: doctorTaskAccountID,
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &config.CalDAVTaskRoute{
				Endpoint: "https://dav.example.invalid/", TaskListPath: "/tasks/",
				Username: "reader",
				Credential: config.CredentialRef{
					Backend: config.CredentialOSKeyring, Key: "tasks", Consent: true,
				},
			},
		},
	}
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
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
	backend := &doctorTaskBackend{}
	stop := startAdapterTestDaemon(t, endpoint, digest, backend)
	t.Cleanup(stop)

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	runErr := (&doctorCommand{Online: true, JSON: true}).Run(app)
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if runErr != nil || !report.Healthy || backend.taskListCalls != 1 {
		t.Fatalf("task doctor report = %+v, calls=%d, error=%v", report, backend.taskListCalls, runErr)
	}
	for _, check := range report.Checks {
		if check.Name != "task_list_contract" {
			continue
		}
		if check.Status != "pass" || strings.Contains(stdout.String(), "private") {
			t.Fatalf("task-list check = %+v, output=%s", check, stdout.String())
		}
		return
	}
	t.Fatalf("doctor report lacks task-list contract: %+v", report.Checks)
}

func TestDoctorConnectionOnlyRequiresOnline(t *testing.T) {
	t.Parallel()
	app := newRuntime(
		t.Context(),
		filepath.Join(t.TempDir(), "missing.toml"),
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	if err := (&doctorCommand{ConnectionOnly: true}).Run(app); err == nil ||
		!strings.Contains(err.Error(), "requires --online") {
		t.Fatalf("connection-only validation error = %v", err)
	}
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
