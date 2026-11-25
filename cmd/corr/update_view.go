package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/updatecheck"
)

type updateView struct {
	consoleView
	width int
}

func newUpdateView(app *runtime, writer io.Writer, interactive bool) updateView {
	return updateView{consoleView: newConsoleView(app, writer, interactive), width: app.textWidth(writer)}
}

var releaseHTTPStatusPattern = regexp.MustCompile(`returned HTTP (\d{3})`)

// updateUnavailableReason says in words why release information is missing,
// for example "the release lookup timed out". Release lookups read public
// GitHub metadata only, so an unrecognised cause is shown as it is.
func updateUnavailableReason(report updateReport, err error) string {
	var netErr net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return "the release lookup timed out"
	case errors.As(err, &dnsErr) || errors.As(err, &opErr):
		return "GitHub could not be reached; check the network connection"
	}
	if err != nil {
		if match := releaseHTTPStatusPattern.FindStringSubmatch(err.Error()); match != nil {
			switch code, _ := strconv.Atoi(match[1]); {
			case code == http.StatusForbidden || code == http.StatusTooManyRequests:
				return "GitHub's limit on release lookups was reached; try again later"
			case code >= http.StatusInternalServerError:
				return fmt.Sprintf("GitHub answered with a server error (HTTP %d)", code)
			default:
				return fmt.Sprintf("GitHub refused the release lookup (HTTP %d)", code)
			}
		}
	}
	switch {
	case report.Cached && report.CheckedAt != "":
		return "the last automatic check, at " + report.CheckedAt + ", failed"
	case errors.Is(err, updatecheck.ErrUnavailable) && report.CheckedAt == "":
		return "another corr command is checking right now"
	case err != nil && !errors.Is(err, updatecheck.ErrUnavailable):
		return singleLine(err.Error())
	case err != nil:
		var reasons []string
		for line := range strings.Lines(err.Error()) {
			if line = strings.TrimSpace(line); line != "" && line != updatecheck.ErrUnavailable.Error() {
				reasons = append(reasons, line)
			}
		}
		if len(reasons) > 0 {
			return strings.Join(reasons, "; ")
		}
	}
	return "the release information could not be read"
}

func (view updateView) writeNotice(report updateReport) error {
	if err := view.writeReleaseSelection(report.Channel, report.LatestVersion); err != nil {
		return err
	}
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

// writeAutomaticInstallFailure reports why an automatic update did not
// install. Install errors name release files and local paths, never account
// data.
func (view updateView) writeAutomaticInstallFailure(current string, cause error) error {
	reason := ""
	if cause != nil {
		reason = " (" + sanitizeCell(singleLine(cause.Error()), 240) + ")"
	}
	_, err := view.printf(
		"%s  %s\n",
		view.warning(),
		"Automatic update failed"+reason+"; continuing with "+
			strings.TrimPrefix(current, "v")+". Run "+view.command("corr update")+" to try again.",
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
				" installed")+" · switching to the updated command",
		)
		return err
	case updatecheck.InstallStatusRepaired:
		_, err := view.printf(
			"%s  %s\n",
			view.success(),
			view.strong("corr "+strings.TrimPrefix(result.CurrentVersion, "v")+
				" installed")+" · switching to the updated command",
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

// writeCheck shows the result of `corr update check`; checkErr is the lookup
// error, used to say why the status is unavailable.
func (view updateView) writeCheck(report updateReport, checkErr error) error {
	if report.Status == updatecheck.StatusAvailable || report.Status == updatecheck.StatusCurrent {
		if err := view.writeReleaseSelection(report.Channel, report.LatestVersion); err != nil {
			return err
		}
	}
	switch report.Status {
	case updatecheck.StatusAvailable:
		if report.DirectOnly {
			_, err := view.printf(
				"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
				view.accent(),
				view.strong("Preview available"),
				"Version",
				versionPair(report.CurrentVersion, report.LatestVersion),
				"Tracking",
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
			"Tracking",
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
			view.muted("Latest "+string(releaseChannel(report.LatestVersion))+" "+strings.TrimPrefix(report.LatestVersion, "v")+" · checked "+report.CheckedAt),
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
			"%s  %s\n%s\n",
			view.warning(),
			view.strong("Could not check for updates"),
			wrapText(
				sentenceCase(updateUnavailableReason(report, checkErr))+
					". Normal Corresync commands still work, and corr update asks again each time you run it.",
				view.width,
				"   ",
			),
		)
		return err
	default:
		return fmt.Errorf("unknown update status %q", report.Status)
	}
}

func (view updateView) writeAction(report updateActionReport) error {
	version := report.LatestVersion
	if version == "" {
		version = report.CurrentVersion
	}
	if report.Status != string(updatecheck.StatusUnavailable) && report.Status != string(updatecheck.StatusDevelopment) {
		if err := view.writeReleaseSelection(report.Channel, version); err != nil {
			return err
		}
	}
	switch report.Status {
	case string(updatecheck.InstallStatusUpdated):
		_, err := view.printf(
			"\n%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n\n%s\n",
			view.success(),
			view.strong("Corresync updated"),
			"Version",
			versionPair(report.PreviousVersion, report.CurrentVersion),
			"Tracking",
			report.Channel,
			"Verified",
			"Sigstore identity · SHA-256 · version · platform",
			"Backup",
			report.BackupPath,
			view.muted("The local daemon for this configuration was restarted if it was running. Restart connected AI agents so they load the updated command."),
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

func (view updateView) writeReleaseSelection(channel updatecheck.Channel, version string) error {
	if channel != updatecheck.ChannelPreview || releaseChannel(version) != updatecheck.ChannelStable {
		return nil
	}
	_, err := view.printf("%s\n", view.muted("Release: stable (tracking preview). No newer preview was found in the checked releases."))
	return err
}
