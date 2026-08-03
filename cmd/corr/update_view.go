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

func (view updateView) writeNotice(report updateReport) error {
	if report.DirectOnly {
		_, err := view.printf(
			"\n%s  %s\n   %s\n",
			view.accent(),
			view.strong("Preview available")+"  "+view.muted(versionPair(report.CurrentVersion, report.LatestVersion)),
			view.muted("Preview releases are direct-install only: "+report.ReleaseURL),
		)
		return err
	}
	_, err := view.printf(
		"\n%s  %s\n   Run %s\n",
		view.accent(),
		view.strong("Update available")+"  "+view.muted(versionPair(report.CurrentVersion, report.LatestVersion)),
		view.command(report.Upgrade),
	)
	return err
}

func (view updateView) writeAutomaticInstallStart(current, latest string) error {
	_, err := view.printf(
		"\n%s  %s\n",
		view.accent(),
		view.strong("Update available")+"  "+
			view.muted(versionPair(current, latest))+" · installing verified standalone update…",
	)
	return err
}

func (view updateView) writeAutomaticInstallFailure(current string) error {
	_, err := view.printf(
		"%s  %s\n",
		view.warning(),
		"Automatic update failed; continuing with "+
			strings.TrimPrefix(current, "v")+". Run "+view.command("corr update")+" to retry.",
	)
	return err
}

func (view updateView) writeAutomaticInstallResult(result updatecheck.InstallResult) error {
	switch result.Status {
	case updatecheck.InstallStatusUpdated:
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("Corresync "+strings.TrimPrefix(result.CurrentVersion, "v")+
				" installed")+" · active on the next corr start",
		)
		return err
	case updatecheck.InstallStatusRepaired:
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(result.CurrentVersion, "v")+
				" installed")+" · active on the next corr start",
		)
		return err
	case updatecheck.InstallStatusCurrent:
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(result.CurrentVersion, "v")+" is already current"),
		)
		return err
	default:
		return fmt.Errorf("unknown automatic update status %q", result.Status)
	}
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
		if report.DirectOnly {
			_, err := view.printf(
				"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
				view.accent(),
				view.strong("Preview available"),
				"Version",
				versionPair(report.CurrentVersion, report.LatestVersion),
				"Channel",
				report.Channel,
				"Managed by",
				installationLabel(report.InstallMethod),
				"Release",
				report.ReleaseURL,
				view.muted("Preview releases are signed direct downloads and are not published to package-manager catalogs."),
			)
			return err
		}
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n",
			view.accent(),
			view.strong("Update available"),
			"Version",
			versionPair(report.CurrentVersion, report.LatestVersion),
			"Channel",
			report.Channel,
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
			view.muted("Latest "+string(report.Channel)+" "+strings.TrimPrefix(report.LatestVersion, "v")+" · checked "+report.CheckedAt),
		)
		return err
	case updatecheck.StatusDevelopment:
		_, err := view.printf(
			"%s  Development build %s cannot be compared with %s releases.\n",
			view.muted("•"),
			report.CurrentVersion,
			report.Channel,
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
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.success(),
			view.strong("Corresync updated"),
			"Version",
			versionPair(report.PreviousVersion, report.CurrentVersion),
			"Channel",
			report.Channel,
			"Verified",
			"Sigstore identity · SHA-256 · version · platform",
			"Backup",
			report.BackupPath,
			view.muted("Running session owners switch on the next provider command. If doctor reports duplicates, run `corr daemon stop` once."),
		)
		return err
	case string(updatecheck.InstallStatusCurrent):
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(report.CurrentVersion, "v")+" is up to date"),
		)
		return err
	case string(updatecheck.InstallStatusRepaired):
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.success(),
			view.strong("corr command installed"),
			"Version",
			strings.TrimPrefix(report.CurrentVersion, "v"),
			"Verified",
			"Sigstore identity · SHA-256 · version · platform",
			"Command",
			report.CanonicalPath,
			view.muted("Your existing corresync compatibility command was left unchanged."),
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
	case "preview_available":
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.accent(),
			view.strong("Preview available"),
			"Version",
			versionPair(report.CurrentVersion, report.LatestVersion),
			"Managed by",
			installationLabel(report.InstallMethod),
			"Release",
			report.ReleaseURL,
			view.muted("Preview releases are not installed over files owned by a package manager. Use a signed direct installation to follow preview."),
		)
		return err
	case string(updatecheck.StatusDevelopment):
		_, err := view.printf(
			"%s  Development build %s cannot be compared with %s releases.\n",
			view.muted("•"),
			report.CurrentVersion,
			report.Channel,
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
