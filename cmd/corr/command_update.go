package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

type updateCommand struct {
	Action *string `arg:"" optional:"" name:"action" enum:"check" help:"Use check to report the latest stable release without installing."`
	JSON   bool    `help:"Write machine-readable JSON without terminal styling."`
}

type updateCheckCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type updateApplyCommand struct {
	JSON bool `help:"Write machine-readable JSON without progress output."`
}

type updateReport struct {
	updatecheck.Result
	InstallMethod updatecheck.InstallMethod `json:"installMethod,omitempty"`
	Upgrade       string                    `json:"upgrade,omitempty"`
}

type updateActionReport struct {
	Status          string                    `json:"status"`
	PreviousVersion string                    `json:"previousVersion,omitempty"`
	CurrentVersion  string                    `json:"currentVersion"`
	LatestVersion   string                    `json:"latestVersion,omitempty"`
	Updated         bool                      `json:"updated"`
	InstallMethod   updatecheck.InstallMethod `json:"installMethod"`
	Command         string                    `json:"command,omitempty"`
	ReleaseURL      string                    `json:"releaseUrl,omitempty"`
	Archive         string                    `json:"archive,omitempty"`
	BackupPath      string                    `json:"backupPath,omitempty"`
	CanonicalPath   string                    `json:"canonicalPath,omitempty"`
	Verification    []string                  `json:"verification,omitempty"`
}

func (command *updateCommand) Run(app *runtime) error {
	if command.Action == nil {
		return (&updateApplyCommand{JSON: command.JSON}).Run(app)
	}
	switch *command.Action {
	case "check":
		return (&updateCheckCommand{JSON: command.JSON}).Run(app)
	default:
		return fmt.Errorf(
			"unknown update action %q; use \"corr update\" or \"corr update check\"",
			*command.Action,
		)
	}
}

func (command *updateCheckCommand) Run(app *runtime) error {
	ctx, cancel := context.WithTimeout(app.context, 5*time.Second)
	defer cancel()
	report, _ := app.updateReport(ctx)
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	view := newUpdateView(app, app.stdout, app.interactiveStdout())
	return view.writeCheck(report)
}

func (command *updateApplyCommand) Run(app *runtime) error {
	ctx, cancel := context.WithTimeout(app.context, 2*time.Minute)
	defer cancel()
	method := app.installMethod()
	view := newUpdateView(app, app.stdout, !command.JSON && app.interactiveStdout())
	if method == updatecheck.InstallDirect {
		var progress func(updatecheck.InstallProgress)
		if !command.JSON {
			progress = view.writeProgress
		}
		result, err := app.installUpdate(ctx, progress)
		if err != nil {
			return err
		}
		report := updateActionReport{
			Status:         string(result.Status),
			CurrentVersion: result.CurrentVersion,
			LatestVersion:  result.LatestVersion,
			Updated:        result.Status == updatecheck.InstallStatusUpdated,
			InstallMethod:  method,
			ReleaseURL:     result.ReleaseURL,
			Archive:        result.Archive,
			BackupPath:     result.BackupPath,
			CanonicalPath:  result.CanonicalPath,
		}
		if report.Updated || result.Status == updatecheck.InstallStatusRepaired {
			report.PreviousVersion = result.PreviousVersion
			report.Verification = []string{"sigstore", "sha256", "version", "platform"}
		}
		if command.JSON {
			return writeJSON(app.stdout, report)
		}
		return view.writeAction(report)
	}

	report, err := app.updateReportFresh(ctx)
	if err != nil {
		return fmt.Errorf("check latest stable release: %w", err)
	}
	action := updateActionReport{
		Status:         string(report.Status),
		CurrentVersion: report.CurrentVersion,
		LatestVersion:  report.LatestVersion,
		Updated:        false,
		InstallMethod:  method,
		ReleaseURL:     report.ReleaseURL,
	}
	if report.Status == updatecheck.StatusAvailable {
		action.Status = "action_required"
		action.Command = report.Upgrade
	}
	if command.JSON {
		return writeJSON(app.stdout, action)
	}
	return view.writeAction(action)
}

func (app *runtime) updateReport(ctx context.Context) (updateReport, error) {
	return app.updateReportWith(ctx, app.checkUpdate)
}

func (app *runtime) updateReportFresh(ctx context.Context) (updateReport, error) {
	return app.updateReportWith(ctx, app.checkUpdateFresh)
}

func (app *runtime) updateReportWith(
	ctx context.Context,
	check func(context.Context) (updatecheck.Result, error),
) (updateReport, error) {
	result, err := check(ctx)
	report := updateReport{Result: result}
	if result.Status == updatecheck.StatusAvailable {
		report.InstallMethod = app.installMethod()
		report.Upgrade = updatecheck.UpgradeAdvice(report.InstallMethod, result.LatestVersion)
	}
	return report, err
}

func (app *runtime) maybeNotifyUpdate(parent context.Context) {
	if app.interactiveOutput == nil ||
		!app.interactiveOutput() ||
		!app.automaticUpdateChecksEnabled(parent, nil) {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 750*time.Millisecond)
	defer cancel()
	report, err := app.updateReport(ctx)
	if err != nil || report.Status != updatecheck.StatusAvailable {
		return
	}
	view := newUpdateView(app, app.stderr, true)
	_ = view.writeNotice(report.CurrentVersion, report.LatestVersion)
}

func (app *runtime) automaticUpdateChecksEnabled(
	ctx context.Context,
	configuration *config.Config,
) bool {
	if value, exists := app.lookupEnv("CORRESYNC_NO_UPDATE_CHECK"); exists &&
		disablesUpdateCheck(value) {
		return false
	}
	// Keep the v0.6 environment override as a migration-only compatibility
	// input. It is intentionally not advertised by Corresync.
	if value, exists := app.lookupEnv("OWA_NO_UPDATE_CHECK"); exists && disablesUpdateCheck(value) {
		return false
	}
	if configuration != nil {
		return !configuration.Updates.DisableAutomaticChecks
	}
	loaded, _, err := app.loadConfigContext(ctx)
	if err == nil {
		return !loaded.Updates.DisableAutomaticChecks
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return false
}

func disablesUpdateCheck(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func shouldOfferAutomaticUpdateNotice(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--json" || strings.HasPrefix(argument, "--json=") {
			return false
		}
	}
	command := rootCommand(arguments)
	switch command {
	case "", "completion", "daemon", "doctor", "feedback", "mcp", "update":
		return false
	default:
		return !completionEnvironmentActive()
	}
}

func rootCommand(arguments []string) string {
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--config" {
			index++
			continue
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		return argument
	}
	return ""
}
