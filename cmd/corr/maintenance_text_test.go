package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

func textTestRuntime(t *testing.T) (*runtime, *bytes.Buffer) {
	t.Helper()
	var stdout bytes.Buffer
	return newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current()), &stdout
}

func TestUpdateUnavailableReasonSaysWhy(t *testing.T) {
	t.Parallel()

	fresh := updateReport{Result: updatecheck.Result{Status: updatecheck.StatusUnavailable, CheckedAt: "2026-11-25T10:00:00Z"}}
	for _, test := range []struct {
		name   string
		report updateReport
		err    error
		want   string
	}{
		{"timeout", fresh, errors.Join(updatecheck.ErrUnavailable, context.DeadlineExceeded), "timed out"},
		{"no network", fresh, errors.Join(updatecheck.ErrUnavailable, &net.DNSError{Err: "no such host", Name: "api.github.com"}), "could not be reached"},
		{"rate limit", fresh, errors.Join(updatecheck.ErrUnavailable, errors.New("release endpoint returned HTTP 403")), "limit on release lookups"},
		{"too many requests", fresh, errors.Join(updatecheck.ErrUnavailable, errors.New("release endpoint returned HTTP 429")), "limit on release lookups"},
		{"server error", fresh, errors.Join(updatecheck.ErrUnavailable, errors.New("release endpoint returned HTTP 502")), "server error (HTTP 502)"},
		{"another check", updateReport{Result: updatecheck.Result{Status: updatecheck.StatusUnavailable}}, updatecheck.ErrUnavailable, "another corr command is checking"},
		{
			"cached failure",
			updateReport{Result: updatecheck.Result{Status: updatecheck.StatusUnavailable, CheckedAt: "2026-11-25T09:00:00Z", Cached: true}},
			updatecheck.ErrUnavailable,
			"the last automatic check, at 2026-11-25T09:00:00Z, failed",
		},
		{"other cause", fresh, errors.Join(updatecheck.ErrUnavailable, errors.New("decode release metadata: unexpected EOF")), "decode release metadata: unexpected EOF"},
		{"nothing known", fresh, nil, "could not be read"},
	} {
		if got := updateUnavailableReason(test.report, test.err); !strings.Contains(got, test.want) {
			t.Errorf("%s: updateUnavailableReason() = %q, want it to contain %q", test.name, got, test.want)
		}
	}
}

func TestUpdateCheckShowsWhyStatusIsUnavailable(t *testing.T) {
	t.Parallel()

	app, stdout := textTestRuntime(t)
	report := updateReport{Result: updatecheck.Result{Status: updatecheck.StatusUnavailable, CheckedAt: "2026-11-25T10:00:00Z"}}
	err := errors.Join(updatecheck.ErrUnavailable, errors.New("release endpoint returned HTTP 429"))
	if err := newUpdateView(app, app.stdout, false).writeCheck(report, err); err != nil {
		t.Fatalf("writeCheck() error = %v", err)
	}
	output := strings.Join(strings.Fields(stdout.String()), " ")
	for _, want := range []string{
		"Could not check for updates",
		"GitHub's limit on release lookups was reached",
		"corr update asks again each time you run it",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("update check output is missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(output, "temporarily unavailable") {
		t.Fatalf("update check output still hides the reason:\n%s", stdout.String())
	}
}

func TestDaemonFailuresSayWhatHappened(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		err      error
		message  string
		hint     string
		wrapsErr bool
	}{
		{"not running", fmt.Errorf("dial: %w", os.ErrNotExist), "stop: the local daemon is not running", "corr daemon start", true},
		{
			"older version",
			&daemonapi.ProtocolVersionError{ClientVersion: daemonapi.ProtocolVersion, DaemonVersion: 30},
			"stop: the local daemon is a different version of corr (protocol 30",
			"corr daemon start",
			true,
		},
		{"refused", &daemonapi.Error{Code: "unauthorized", Message: "daemon authorization failed"}, "refused this command", "corr doctor", true},
		{"no answer", context.DeadlineExceeded, "did not answer in time", "corr daemon status", true},
		{"daemon reason", &daemonapi.Error{Code: "invalid_request", Message: "account is not configured"}, "stop: account is not configured", "", true},
	} {
		err := describeDaemonFailure("stop", test.err)
		if !strings.HasPrefix(err.Error(), test.message) && !strings.Contains(err.Error(), test.message) {
			t.Errorf("%s: error = %q, want %q", test.name, err, test.message)
		}
		if hint, _ := errorHint(err); hint != test.hint {
			t.Errorf("%s: hint = %q, want %q", test.name, hint, test.hint)
		}
		if test.wrapsErr && !errors.Is(err, test.err) {
			t.Errorf("%s: error no longer wraps its cause", test.name)
		}
	}
	if err := describeDaemonFailure("stop", nil); err != nil {
		t.Fatalf("describeDaemonFailure(nil) = %v", err)
	}
}

func TestDaemonUnavailableErrorExplainsReason(t *testing.T) {
	t.Parallel()

	for reason, want := range map[string]string{
		string(localipc.CredentialMissing): "the local daemon is not running (missing_credential)",
		"incompatible_protocol":            "the local daemon is a different version of corr (incompatible_protocol)",
		"authentication_failed":            "refused this command",
		"timeout":                          "did not answer within 3 seconds",
		"unreachable":                      "could not be reached (unreachable)",
	} {
		err := daemonUnavailableError(reason)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("daemonUnavailableError(%q) = %q, want %q", reason, err, want)
		}
		if _, hinted := errorHint(err); !hinted {
			t.Errorf("daemonUnavailableError(%q) has no next step", reason)
		}
	}
}

func TestLoginFailureNamesAccountAndNextStep(t *testing.T) {
	t.Parallel()

	cause := errors.New("browser executable was not found")
	err := loginFailure("work", cause, "corr doctor --account work")
	if err.Error() != "sign in to work: browser executable was not found" || !errors.Is(err, cause) {
		t.Fatalf("loginFailure() = %v", err)
	}
	if hint, _ := errorHint(err); hint != "corr doctor --account work" {
		t.Fatalf("hint = %q", hint)
	}
	hinted := withHint(errors.New("the login view expired"), "corr auth login --account work --web")
	if hint, _ := errorHint(loginFailure("work", hinted, "corr doctor")); hint != "corr auth login --account work --web" {
		t.Fatalf("existing hint was replaced: %q", hint)
	}
	if err := loginFailure("work", context.Canceled, "corr doctor"); err != context.Canceled { //nolint:errorlint // the unchanged value is the point
		t.Fatalf("cancelled login = %v, want context.Canceled unchanged", err)
	}
}

func TestMonitorStatusShowsStateInWords(t *testing.T) {
	t.Parallel()

	app, stdout := textTestRuntime(t)
	err := writeMonitorStatuses(app, []application.MonitorStatus{
		{Alias: "personal", Mode: domain.MonitorOff},
		{
			Alias: "work", Mode: domain.MonitorAgent, CollectionEnabled: true,
			Runner: &application.MonitorSinkView{Destination: "/opt/runner", Egress: "local", Fields: []string{"sender", "subject"}},
			Queue:  application.MonitorQueueStatus{Pending: 3},
		},
	})
	if err != nil {
		t.Fatalf("writeMonitorStatuses() error = %v", err)
	}
	for _, want := range []string{
		"personal", "off",
		"work", "agent · checking for new mail",
		"Pending events   3",
		"Runs             /opt/runner (stays on this computer)",
		"Fields           sender, subject",
		"Next: corr events list --account work --state pending",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("monitor status is missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestEventsListSuggestsAcknowledgingFirstOpenEvent(t *testing.T) {
	t.Parallel()

	app, stdout := textTestRuntime(t)
	err := writeMonitorEvents(app, application.MonitorEventPage{Events: []application.MonitorEvent{
		{ID: "evt_done", AccountAlias: "work", State: "acknowledged"},
		{ID: "evt_open", AccountAlias: "work", State: "pending"},
	}})
	if err != nil {
		t.Fatalf("writeMonitorEvents() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "Next: corr events acknowledge evt_open --account work") {
		t.Fatalf("events list output:\n%s", stdout.String())
	}
}

func TestIntegrationDetectionUsesPlainLabels(t *testing.T) {
	t.Parallel()

	app, stdout := textTestRuntime(t)
	codex, _ := app.agentHosts.Lookup("codex")
	err := writeAgentHostDetection(app, agenthost.Report{
		Context: agenthost.RuntimeContext{Kind: "local", OS: "linux", Arch: "amd64"},
		Cache:   agenthost.CacheFresh,
		Hosts: []agenthost.Detection{{
			Host: codex, Status: agenthost.StatusConfirmed, ConnectionStatus: agenthost.ConnectionNotInspected,
		}},
	})
	if err != nil {
		t.Fatalf("writeAgentHostDetection() error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{"installed", "connection not checked", "just checked", "Next: corr integrations plan codex"} {
		if !strings.Contains(output, want) {
			t.Fatalf("detection output is missing %q:\n%s", want, output)
		}
	}
	for _, internal := range []string{"not_inspected", "confirmed", "bounded"} {
		if strings.Contains(output, internal) {
			t.Fatalf("detection output shows internal label %q:\n%s", internal, output)
		}
	}
}

func TestDaemonStatusShowsNextStep(t *testing.T) {
	t.Parallel()

	app, stdout := textTestRuntime(t)
	err := writeDaemonStatus(app, daemonapi.Status{
		Version: "0.9.6", ProcessID: 4242, ProtocolVersion: daemonapi.ProtocolVersion, StartedAt: time.Now(),
	}, false)
	if err != nil {
		t.Fatalf("writeDaemonStatus() error = %v", err)
	}
	for _, want := range []string{"The local daemon is running", "Process ID", "Default account  none", "Next: corr auth status"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("daemon status is missing %q:\n%s", want, stdout.String())
		}
	}
}
