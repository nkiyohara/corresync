// Package dispatch contains the explicit local notification and runner sink
// adapters. Neither adapter owns mailbox credentials or application use cases.
package dispatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

type commandRunner func(context.Context, []byte, string, ...string) error

func runCommand(ctx context.Context, stdin []byte, command string, arguments ...string) error {
	// #nosec G204 -- command and literal arguments are explicitly configured by
	// the local user, validated, and never derived from mailbox content.
	process := exec.CommandContext(ctx, command, arguments...)
	process.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	process.Stderr = &limitedWriter{target: &stderr, remaining: 4096}
	if err := process.Run(); err != nil {
		return fmt.Errorf("runner process failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type limitedWriter struct {
	target    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if writer.remaining > 0 {
		data = data[:min(len(data), writer.remaining)]
		_, _ = writer.target.Write(data)
		writer.remaining -= len(data)
	}
	return original, nil
}

// DesktopNotifier invokes only the platform notification utility and passes no
// message body, attachment, credential, or shell expression.
type DesktopNotifier struct {
	lookPath func(string) (string, error)
	run      commandRunner
	goos     string
}

func NewDesktopNotifier() *DesktopNotifier {
	return &DesktopNotifier{lookPath: exec.LookPath, run: runCommand, goos: runtime.GOOS}
}

func (notifier *DesktopNotifier) Notify(
	ctx context.Context,
	release application.MonitorRelease,
) error {
	if release.Destination != "desktop" {
		return errors.New("notification destination is not the configured desktop adapter")
	}
	title := "Corresync"
	summary := notificationSummary(release.Event)
	switch notifier.goos {
	case "linux":
		command, err := notifier.lookPath("notify-send")
		if err != nil {
			return errors.New("desktop notification requires notify-send")
		}
		return notifier.run(
			ctx,
			nil,
			command,
			"--app-name=Corresync",
			"--",
			title,
			summary,
		)
	case "darwin":
		command, err := notifier.lookPath("osascript")
		if err != nil {
			return errors.New("desktop notification requires osascript")
		}
		return notifier.run(
			ctx,
			nil,
			command,
			"-e",
			`on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`,
			title,
			summary,
		)
	case "windows":
		command, err := notifier.lookPath("powershell.exe")
		if err != nil {
			return errors.New("desktop notification requires Windows PowerShell")
		}
		payload, err := json.Marshal(struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		}{Title: title, Summary: summary})
		if err != nil {
			return fmt.Errorf("encode desktop notification: %w", err)
		}
		return notifier.run(
			ctx,
			[]byte(base64.StdEncoding.EncodeToString(payload)),
			command,
			"-NoLogo",
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			`$encoded=[Console]::In.ReadToEnd();`+
				`$json=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded));`+
				`$payload=$json|ConvertFrom-Json;`+
				`$template=[Windows.UI.Notifications.ToastTemplateType]::ToastText02;`+
				`$xml=[Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent($template);`+
				`$nodes=$xml.GetElementsByTagName('text');`+
				`[void]$nodes.Item(0).AppendChild($xml.CreateTextNode([string]$payload.title));`+
				`[void]$nodes.Item(1).AppendChild($xml.CreateTextNode([string]$payload.summary));`+
				`$toast=[Windows.UI.Notifications.ToastNotification]::new($xml);`+
				`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Corresync').Show($toast)`,
		)
	default:
		return errors.New("desktop notification adapter is unavailable on this platform")
	}
}

func notificationSummary(event map[string]any) string {
	parts := make([]string, 0, 3)
	if sender, ok := event["sender"].(application.MailAddress); ok {
		if sender.Address != "" {
			parts = append(parts, sender.Address)
		} else if sender.Name != "" {
			parts = append(parts, sender.Name)
		}
	}
	if subject, ok := event["subject"].(string); ok && subject != "" {
		parts = append(parts, subject)
	}
	if len(parts) == 0 {
		return "New monitored mail event"
	}
	return strings.Join(parts, " — ")
}

// Runner looks up the immutable account configuration again and verifies every
// disclosure field before executing the absolute path without a shell.
type Runner struct {
	lookup func(domain.AccountID) (config.Runner, error)
	run    commandRunner
}

func NewRunner(lookup func(domain.AccountID) (config.Runner, error)) (*Runner, error) {
	if lookup == nil {
		return nil, errors.New("runner configuration lookup is required")
	}
	return &Runner{lookup: lookup, run: runCommand}, nil
}

func (runner *Runner) Run(
	ctx context.Context,
	request application.MonitorRunnerRequest,
) error {
	configured, err := runner.lookup(request.Account)
	if err != nil {
		return err
	}
	if request.Destination != configured.Command ||
		request.Egress != configured.Egress ||
		!equalStrings(request.Fields, configured.Fields) {
		return errors.New("runner request exceeds its configured disclosure policy")
	}
	if request.Trust != application.MonitorTrustMarker ||
		len(request.AllowedEffects) != 1 ||
		request.AllowedEffects[0] != "read" {
		return errors.New("runner request does not enforce read-only untrusted input")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode runner request: %w", err)
	}
	runContext, cancel := context.WithTimeout(ctx, time.Duration(configured.Timeout))
	defer cancel()
	return runner.run(
		runContext,
		append(encoded, '\n'),
		configured.Command,
		configured.Arguments...,
	)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
