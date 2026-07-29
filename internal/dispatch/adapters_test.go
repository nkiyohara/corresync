package dispatch

import (
	"context"
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
		runContext context.Context,
		stdin []byte,
		command string,
		values ...string,
	) error {
		if len(stdin) != 0 || command != "/usr/bin/notify-send" {
			t.Fatalf("notification command = %q stdin=%q", command, stdin)
		}
		if _, ok := runContext.Deadline(); !ok {
			t.Fatal("notification command has no timeout")
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
			"subject": "--urgency=critical <b>unsafe & urgent</b>",
		},
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if !slices.Equal(arguments, []string{
		"--app-name=Corresync",
		"--",
		"Corresync",
		"--urgency=critical &lt;b&gt;unsafe &amp; urgent&lt;/b&gt;",
	}) {
		t.Fatalf("notification arguments = %+v", arguments)
	}
}

func TestDesktopNotifierPassesMacOSMetadataAsData(t *testing.T) {
	t.Parallel()
	notifier := &DesktopNotifier{
		goos: "darwin",
		lookPath: func(name string) (string, error) {
			if name != "osascript" {
				return "", errors.New("unexpected utility")
			}
			return "/usr/bin/osascript", nil
		},
	}
	var arguments []string
	notifier.run = func(
		_ context.Context,
		stdin []byte,
		_ string,
		values ...string,
	) error {
		if len(stdin) != 0 {
			t.Fatalf("notification stdin = %q", stdin)
		}
		arguments = append([]string(nil), values...)
		return nil
	}
	const subject = `-e 'do shell script "open unsafe"'`
	if err := notifier.Notify(t.Context(), application.MonitorRelease{
		Destination: "desktop",
		Event: map[string]any{
			"subject": subject,
		},
	}); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(arguments) != 4 || arguments[0] != "-e" ||
		arguments[2] != "Corresync" || arguments[3] != subject ||
		strings.Contains(arguments[1], subject) {
		t.Fatalf("unsafe osascript arguments = %+v", arguments)
	}
}

func TestDesktopNotifierRejectsUnavailablePlatformsBeforeExecution(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"windows", "freebsd"} {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()
			notifier := &DesktopNotifier{
				goos: goos,
				lookPath: func(string) (string, error) {
					t.Fatal("looked up a utility for an unavailable platform")
					return "", nil
				},
				run: func(context.Context, []byte, string, ...string) error {
					t.Fatal("executed a utility for an unavailable platform")
					return nil
				},
			}
			err := notifier.Notify(t.Context(), application.MonitorRelease{
				Destination: "desktop",
				Event: map[string]any{
					"subject": `'); Start-Process calc; #`,
				},
			})
			if err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("Notify() error = %v", err)
			}
		})
	}
}
