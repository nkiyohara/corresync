package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/feedback"
	"github.com/nkiyohara/corresync/internal/paths"
)

const (
	feedbackIssueURL   = "https://github.com/nkiyohara/corresync/issues/new"
	maximumFeedbackURL = 32 << 10
)

type feedbackCommand struct {
	LastError  bool   `help:"Include the most recent sanitized local command error and process crash, when available."`
	Copy       bool   `help:"Copy the reviewed report with the platform clipboard command."`
	Save       string `type:"path" placeholder:"PATH" help:"Save the reviewed report to a new owner-only file."`
	OpenGitHub bool   `name:"open-github" help:"Open a prefilled GitHub Issue page after showing the report; never submit it."`
}

func (command *feedbackCommand) Run(app *runtime) error {
	actions := 0
	for _, selected := range []bool{command.Copy, command.Save != "", command.OpenGitHub} {
		if selected {
			actions++
		}
	}
	if actions > 1 {
		return errors.New("choose only one of --copy, --save, or --open-github")
	}

	report, err := app.feedbackReport(command.LastError)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(
		app.stdout,
		"Generated a redacted diagnostic report locally. Review the complete report below:",
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(app.stdout); err != nil {
		return err
	}
	if _, err := app.stdout.Write(report); err != nil {
		return err
	}

	switch {
	case command.Copy:
		name, arguments, err := feedbackClipboardCommand(app.info.OS, app.lookupEnv)
		if err != nil {
			return err
		}
		if err := app.runInputCommand(
			app.context,
			bytes.NewReader(report),
			io.Discard,
			app.stderr,
			name,
			arguments...,
		); err != nil {
			return fmt.Errorf("copy redacted feedback report: %w", err)
		}
		_, err = fmt.Fprintln(app.stdout, "Copied the reviewed report to the clipboard.")
		return err
	case command.Save != "":
		if err := saveFeedbackReport(command.Save, report); err != nil {
			return err
		}
		_, err := fmt.Fprintf(app.stdout, "Saved the reviewed report to %s.\n", command.Save)
		return err
	case command.OpenGitHub:
		issueURL, err := prefilledFeedbackIssueURL(report)
		if err != nil {
			return err
		}
		name, arguments, err := feedbackBrowserCommand(app.info.OS, issueURL)
		if err != nil {
			return err
		}
		if err := app.runInputCommand(
			app.context,
			nil,
			io.Discard,
			app.stderr,
			name,
			arguments...,
		); err != nil {
			return fmt.Errorf("open prefilled GitHub Issue page: %w", err)
		}
		_, err = fmt.Fprintln(
			app.stdout,
			"Opened a prefilled GitHub Issue page. A GitHub account is required; nothing was submitted.",
		)
		return err
	default:
		_, err := fmt.Fprint(
			app.stdout,
			"\nNothing was uploaded. After review, explicitly choose one action:\n"+
				"  corr feedback --copy\n"+
				"  corr feedback --save ./corresync-feedback.json\n"+
				"  corr feedback --open-github  # requires a GitHub account; opens but never submits\n",
		)
		return err
	}
}

func (app *runtime) feedbackReport(includeLastError bool) ([]byte, error) {
	input := feedback.Input{
		Build: feedback.Build{
			Version:   app.info.Version,
			Commit:    app.info.Commit,
			BuildDate: app.info.BuildDate,
			GoVersion: app.info.GoVersion,
			Platform:  app.info.OS + "/" + app.info.Arch,
		},
		InstallMethod: string(app.installMethod()),
		Config:        feedback.ConfigStatus{Status: "degraded", Reason: "collection_failed"},
		LastError:     feedback.LastErrorStatus{Status: "not-requested"},
		LastCrash:     feedback.LastCrashStatus{Status: "not-requested"},
	}
	configuration, err := app.loadFeedbackConfig()
	if err == nil {
		input.Config = feedback.ConfigStatus{
			Status:        "ok",
			SchemaVersion: configuration.Version,
		}
		input.Providers = feedbackProviders(configuration)
	} else {
		input.ProviderReason = "config_invalid"
		if errors.Is(err, os.ErrNotExist) {
			input.Config.Reason = "config_missing"
			input.ProviderReason = "config_missing"
		} else {
			input.Config.Reason = "config_invalid"
		}
	}
	if includeLastError {
		input.LastError = app.feedbackLastError()
		input.LastCrash = app.feedbackLastCrash()
	}
	return feedback.Generate(input)
}

func (app *runtime) feedbackLastCrash() feedback.LastCrashStatus {
	path, err := paths.FeedbackCrashPath()
	if err != nil {
		return feedback.LastCrashStatus{Status: "degraded", Reason: "state_unavailable"}
	}
	record, err := (feedback.CrashStore{Path: path}).Load()
	switch {
	case err == nil:
		return feedback.LastCrashStatus{
			Status: "ok", ID: record.ID, RecordedAt: record.RecordedAt.Format(time.RFC3339),
			ProcessRole: record.ProcessRole, Boundary: record.Boundary,
			Build: &record.Build, Frames: record.Frames,
		}
	case errors.Is(err, feedback.ErrNoRecord):
		return feedback.LastCrashStatus{Status: "absent"}
	default:
		return feedback.LastCrashStatus{Status: "degraded", Reason: "diagnostic_invalid"}
	}
}

func (app *runtime) loadFeedbackConfig() (config.Config, error) {
	path := app.configPath
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return config.Config{}, err
		}
	} else if !filepath.IsAbs(path) {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			return config.Config{}, err
		}
	}
	return config.Load(filepath.Clean(path))
}

func feedbackProviders(configuration config.Config) []feedback.Provider {
	capabilities := make(map[string]map[string]struct{})
	for _, account := range configuration.Accounts {
		if provider := account.MailProvider(); provider != "" {
			if capabilities[string(provider)] == nil {
				capabilities[string(provider)] = make(map[string]struct{})
			}
			capabilities[string(provider)]["mail"] = struct{}{}
		}
		if provider := account.CalendarProvider(); provider != "" {
			if capabilities[string(provider)] == nil {
				capabilities[string(provider)] = make(map[string]struct{})
			}
			capabilities[string(provider)]["calendar"] = struct{}{}
		}
		if provider := account.TaskProvider(); provider != "" {
			if capabilities[string(provider)] == nil {
				capabilities[string(provider)] = make(map[string]struct{})
			}
			capabilities[string(provider)]["tasks"] = struct{}{}
		}
	}
	providers := make([]feedback.Provider, 0, len(capabilities))
	for provider, capabilitySet := range capabilities {
		values := make([]string, 0, len(capabilitySet))
		for capability := range capabilitySet {
			values = append(values, capability)
		}
		sort.Strings(values)
		providers = append(providers, feedback.Provider{
			ID:           provider,
			Capabilities: values,
		})
	}
	sort.Slice(providers, func(left, right int) bool {
		return providers[left].ID < providers[right].ID
	})
	return providers
}

func (app *runtime) feedbackLastError() feedback.LastErrorStatus {
	path, err := paths.FeedbackErrorPath()
	if err != nil {
		return feedback.LastErrorStatus{Status: "degraded", Reason: "state_unavailable"}
	}
	record, err := (feedback.Store{Path: path}).Load()
	switch {
	case err == nil:
		return feedback.LastErrorStatus{
			Status:  "ok",
			ID:      record.ID,
			Command: &record.Command,
			Classes: record.Classes,
		}
	case errors.Is(err, feedback.ErrNoRecord):
		return feedback.LastErrorStatus{Status: "absent"}
	default:
		return feedback.LastErrorStatus{Status: "degraded", Reason: "diagnostic_invalid"}
	}
}

func saveFeedbackReport(path string, report []byte) error {
	if len(report) == 0 || len(report) > 16<<10 {
		return errors.New("redacted feedback report has an invalid size")
	}
	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	) // #nosec G304 -- --save is an explicit user-selected path and never overwrites.
	if err != nil {
		return fmt.Errorf("create new feedback report: %w", err)
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(report); err != nil {
		return fmt.Errorf("write feedback report: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync feedback report: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close feedback report: %w", err)
	}
	removeOnFailure = false
	return nil
}

func prefilledFeedbackIssueURL(report []byte) (string, error) {
	if len(report) == 0 || len(report) > 16<<10 {
		return "", errors.New("redacted feedback report has an invalid size")
	}
	parsed, err := url.Parse(feedbackIssueURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("title", "Feedback: Corresync diagnostic report")
	query.Set(
		"body",
		"## What happened?\n\n<!-- Describe the problem without private mail or calendar data. -->\n\n"+
			"## Redacted diagnostics\n\n```json\n"+
			string(report)+
			"```\n\nThis report was reviewed locally before opening this page. Corresync did not submit it.",
	)
	parsed.RawQuery = query.Encode()
	result := parsed.String()
	if len(result) > maximumFeedbackURL {
		return "", errors.New("prefilled GitHub Issue URL exceeds its size limit")
	}
	return result, nil
}

func feedbackClipboardCommand(
	goos string,
	lookup func(string) (string, bool),
) (string, []string, error) {
	switch goos {
	case "darwin":
		return "pbcopy", nil, nil
	case "windows":
		return "clip.exe", nil, nil
	case "linux":
		if value, exists := lookup("WAYLAND_DISPLAY"); exists && value != "" {
			return "wl-copy", nil, nil
		}
		return "xclip", []string{"-selection", "clipboard"}, nil
	default:
		return "", nil, fmt.Errorf("clipboard action is unsupported on %s", goos)
	}
}

func feedbackBrowserCommand(goos, issueURL string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{issueURL}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", issueURL}, nil
	case "linux":
		return "xdg-open", []string{issueURL}, nil
	default:
		return "", nil, fmt.Errorf("browser action is unsupported on %s", goos)
	}
}
