package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/nkiyohara/corresync/internal/updatecheck"
)

type updateView struct {
	consoleView
}

func newUpdateView(app *runtime, writer io.Writer, interactive bool) updateView {
	return updateView{consoleView: newConsoleView(app, writer, interactive)}
}

func (view updateView) writeNotice(current, latest string) error {
	_, err := view.printf(
		"\n%s  %s\n   Run %s\n",
		view.accent(),
		view.strong("Update available")+"  "+view.muted(versionPair(current, latest)),
		view.command("corr update"),
	)
	return err
}

func (view updateView) writeProgress(progress updatecheck.InstallProgress) {
	if !view.interactive {
		return
	}
	_, _ = view.printf("  %s %s\n", view.success(), progress.Detail)
}

func (view updateView) writeCheck(report updateReport) error {
	switch report.Status {
	case updatecheck.StatusAvailable:
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n",
			view.accent(),
			view.strong("Update available"),
			"Version",
			versionPair(report.CurrentVersion, report.LatestVersion),
			"Managed by",
			installationLabel(report.InstallMethod),
			"Run",
			view.command(report.Upgrade),
			"Release",
			report.ReleaseURL,
		)
		return err
	case updatecheck.StatusCurrent:
		_, err := view.printf(
			"%s  %s\n   %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(report.CurrentVersion, "v")+" is up to date"),
			view.muted("Latest stable "+strings.TrimPrefix(report.LatestVersion, "v")+" · checked "+report.CheckedAt),
		)
		return err
	case updatecheck.StatusDevelopment:
		_, err := view.printf(
			"%s  Development build %s cannot be compared with stable releases.\n",
			view.muted("•"),
			report.CurrentVersion,
		)
		return err
	case updatecheck.StatusUnavailable:
		_, err := view.printf(
			"%s  Update status is temporarily unavailable; normal Corresync operations are unaffected.\n",
			view.muted("•"),
		)
		return err
	default:
		return fmt.Errorf("unknown update status %q", report.Status)
	}
}

func (view updateView) writeAction(report updateActionReport) error {
	switch report.Status {
	case string(updatecheck.InstallStatusUpdated):
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.success(),
			view.strong("Corresync updated"),
			"Version",
			versionPair(report.PreviousVersion, report.CurrentVersion),
			"Verified",
			"Sigstore identity · SHA-256 · version · platform",
			"Backup",
			report.BackupPath,
			view.muted("The running session owner will switch versions on the next Outlook command."),
		)
		return err
	case string(updatecheck.InstallStatusCurrent):
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(report.CurrentVersion, "v")+" is up to date"),
		)
		return err
	case "action_required":
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.accent(),
			view.strong("Update available"),
			"Version",
			versionPair(report.CurrentVersion, report.LatestVersion),
			"Managed by",
			installationLabel(report.InstallMethod),
			"Run",
			view.command(report.Command),
			view.muted("Corresync did not modify files owned by your package manager."),
		)
		return err
	case string(updatecheck.StatusDevelopment):
		_, err := view.printf(
			"%s  Development build %s cannot be compared with stable releases.\n",
			view.muted("•"),
			report.CurrentVersion,
		)
		return err
	default:
		return fmt.Errorf("unknown update action status %q", report.Status)
	}
}

func versionPair(current, latest string) string {
	return strings.TrimPrefix(current, "v") + " → " + strings.TrimPrefix(latest, "v")
}

func installationLabel(method updatecheck.InstallMethod) string {
	switch method {
	case updatecheck.InstallHomebrew:
		return "Homebrew"
	case updatecheck.InstallWinGet:
		return "WinGet"
	case updatecheck.InstallScoop:
		return "Scoop"
	case updatecheck.InstallDeb:
		return "deb package"
	case updatecheck.InstallRPM:
		return "RPM package"
	case updatecheck.InstallAPK:
		return "APK package"
	case updatecheck.InstallDirect:
		return "direct archive"
	default:
		return string(method)
	}
}
