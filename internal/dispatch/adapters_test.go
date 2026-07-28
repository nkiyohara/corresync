package dispatch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

const dispatchTestAccount domain.AccountID = "acc_00000000000000000000000000000001"

func TestRunnerPassesBoundedReadOnlyJSONWithoutShell(t *testing.T) {
	t.Parallel()
	configured := config.NewRunner(
		"/synthetic/runner",
		[]string{"--literal", "$(not-a-shell)"},
		[]string{"account", "event_id", "subject", "trust"},
		"remote",
		true,
	)
	var gotCommand string
	var gotArguments []string
	var gotRequest application.MonitorRunnerRequest
	runner, err := NewRunner(func(account domain.AccountID) (config.Runner, error) {
		if account != dispatchTestAccount {
			t.Fatalf("lookup account = %q", account)
		}
		return configured, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.run = func(
		_ context.Context,
		stdin []byte,
		command string,
		arguments ...string,
	) error {
		gotCommand = command
		gotArguments = append([]string(nil), arguments...)
		return json.Unmarshal(stdin, &gotRequest)
	}
	request := application.MonitorRunnerRequest{
		SchemaVersion: 1, Account: dispatchTestAccount,
		Trust:          application.MonitorTrustMarker,
		AllowedEffects: []string{"read"},
		Destination:    configured.Command, Egress: configured.Egress,
		Fields: append([]string(nil), configured.Fields...),
		Events: []map[string]any{{
			"account":  dispatchTestAccount,
			"event_id": "evt_synthetic",
			"subject":  "Untrusted subject",
			"trust":    application.MonitorTrustMarker,
		}},
	}
	if err := runner.Run(t.Context(), request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotCommand != configured.Command ||
		!slices.Equal(gotArguments, configured.Arguments) {
		t.Fatalf("command = %q %+v", gotCommand, gotArguments)
	}
	if len(gotRequest.AllowedEffects) != 1 ||
		gotRequest.AllowedEffects[0] != "read" ||
		gotRequest.Trust != application.MonitorTrustMarker {
		t.Fatalf("runner request broadened policy: %+v", gotRequest)
	}
}

func TestRunnerRejectsFieldsBroaderThanConfiguration(t *testing.T) {
	t.Parallel()
	configured := config.NewRunner(
		"/synthetic/runner",
		nil,
		[]string{"event_id", "trust"},
		"local",
		false,
	)
	runner, err := NewRunner(func(domain.AccountID) (config.Runner, error) {
		return configured, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.run = func(context.Context, []byte, string, ...string) error {
		t.Fatal("runner executed after disclosure mismatch")
		return nil
	}
	err = runner.Run(t.Context(), application.MonitorRunnerRequest{
		SchemaVersion: 1, Account: dispatchTestAccount,
		Trust:          application.MonitorTrustMarker,
		AllowedEffects: []string{"read"},
		Destination:    configured.Command, Egress: configured.Egress,
		Fields: []string{"event_id", "subject", "trust"},
	})
	if err == nil {
		t.Fatal("Run() accepted broader fields")
	}
}

func TestDesktopNotifierReleasesOnlyRenderedMetadata(t *testing.T) {
	t.Parallel()
	notifier := &DesktopNotifier{
		goos: "linux",
		lookPath: func(name string) (string, error) {
			if name != "notify-send" {
				return "", errors.New("unexpected utility")
			}
			return "/usr/bin/notify-send", nil
		},
	}
	var arguments []string
	notifier.run = func(
		_ context.Context,
		stdin []byte,
		command string,
		values ...string,
	) error {
		if len(stdin) != 0 || command != "/usr/bin/notify-send" {
			t.Fatalf("notification command = %q stdin=%q", command, stdin)
		}
		arguments = append([]string(nil), values...)
		return nil
	}
	err := notifier.Notify(t.Context(), application.MonitorRelease{
		Destination: "desktop",
		Fields:      []string{"sender", "subject", "trust"},
		Event: map[string]any{
			"sender":  application.MailAddress{Address: "sender@example.invalid"},
			"subject": "Synthetic subject",
			"trust":   application.MonitorTrustMarker,
		},
	})
	if err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if !slices.Equal(arguments, []string{
		"--app-name=Corresync",
		"--",
		"Corresync",
		"sender@example.invalid — Synthetic subject",
	}) {
		t.Fatalf("notification arguments = %+v", arguments)
	}
}

func TestDesktopNotifierSeparatesUntrustedLinuxOptions(t *testing.T) {
	t.Parallel()
	notifier := &DesktopNotifier{
		goos: "linux",
		lookPath: func(string) (string, error) {
			return "/usr/bin/notify-send", nil
		},
	}
	var arguments []string
	notifier.run = func(
		_ context.Context,
		_ []byte,
		_ string,
		values ...string,
	) error {
		arguments = append([]string(nil), values...)
		return nil
	}
	if err := notifier.Notify(t.Context(), application.MonitorRelease{
		Destination: "desktop",
		Event: map[string]any{
			"subject": "--urgency=critical",
		},
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if !slices.Equal(arguments, []string{
		"--app-name=Corresync",
		"--",
		"Corresync",
		"--urgency=critical",
	}) {
		t.Fatalf("notification arguments = %+v", arguments)
	}
}

func TestDesktopNotifierKeepsWindowsMetadataOutOfCommandText(t *testing.T) {
	t.Parallel()
	notifier := &DesktopNotifier{
		goos: "windows",
		lookPath: func(name string) (string, error) {
			if name != "powershell.exe" {
				return "", errors.New("unexpected utility")
			}
			return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
		},
	}
	var stdin []byte
	var arguments []string
	notifier.run = func(
		_ context.Context,
		input []byte,
		_ string,
		values ...string,
	) error {
		stdin = append([]byte(nil), input...)
		arguments = append([]string(nil), values...)
		return nil
	}
	const subject = `'); Start-Process calc; #`
	if err := notifier.Notify(t.Context(), application.MonitorRelease{
		Destination: "desktop",
		Event: map[string]any{
			"subject": subject,
		},
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(arguments) != 5 || arguments[3] != "-Command" ||
		strings.Contains(arguments[4], subject) {
		t.Fatalf("unsafe PowerShell arguments = %+v", arguments)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(stdin))
	if err != nil {
		t.Fatalf("decode notification stdin: %v", err)
	}
	var payload struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("decode notification payload: %v", err)
	}
	if payload.Title != "Corresync" || payload.Summary != subject {
		t.Fatalf("notification payload = %+v", payload)
	}
}
