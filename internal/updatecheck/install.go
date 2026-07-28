package updatecheck

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstallMethod identifies the owner of the running executable when it can be
// determined without modifying the installation.
type InstallMethod string

const (
	InstallHomebrew InstallMethod = "homebrew"
	InstallWinGet   InstallMethod = "winget"
	InstallScoop    InstallMethod = "scoop"
	InstallDeb      InstallMethod = "deb"
	InstallRPM      InstallMethod = "rpm"
	InstallAPK      InstallMethod = "apk"
	InstallDirect   InstallMethod = "direct"
)

// DetectInstallation uses path conventions first and read-only Linux package
// ownership queries second. Unknown installations remain direct downloads.
func DetectInstallation(executable string) InstallMethod {
	return DetectInstallationContext(context.Background(), executable)
}

// DetectInstallationContext is DetectInstallation with cancellation applied
// to bounded Linux package-ownership probes.
func DetectInstallationContext(ctx context.Context, executable string) InstallMethod {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	executable = strings.ReplaceAll(executable, `\`, "/")
	cleaned := filepath.ToSlash(strings.ToLower(filepath.Clean(executable)))
	switch {
	case strings.Contains(cleaned, "/cellar/corresync/"),
		strings.Contains(cleaned, "/homebrew/corresync/"),
		strings.Contains(cleaned, "/cellar/owa-bridge/"),
		strings.Contains(cleaned, "/homebrew/owa-bridge/"):
		return InstallHomebrew
	case strings.Contains(cleaned, "/scoop/apps/corresync/"),
		strings.Contains(cleaned, "/scoop/apps/owa-bridge/"):
		return InstallScoop
	case strings.Contains(cleaned, "/winget/packages/nkiyohara.corresync_"),
		strings.Contains(cleaned, "/winget/packages/nkiyohara.owabridge_"):
		return InstallWinGet
	}
	if runtime.GOOS != "linux" ||
		(cleaned != "/usr/bin/corr" && cleaned != "/usr/bin/corresync" && cleaned != "/usr/bin/owa") {
		return InstallDirect
	}
	queries := []struct {
		method  InstallMethod
		command string
		args    []string
	}{
		{InstallDeb, "dpkg-query", []string{"-S", executable}},
		{InstallRPM, "rpm", []string{"-qf", executable}},
		{InstallAPK, "apk", []string{"info", "--who-owns", executable}},
	}
	for _, query := range queries {
		queryContext, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		command := exec.CommandContext(queryContext, query.command, query.args...) // #nosec G204 -- fixed read-only package query with the running executable path.
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		err := command.Run()
		cancel()
		if err == nil {
			return query.method
		}
	}
	return InstallDirect
}

// UpgradeAdvice returns the safe next action. Package-manager-owned binaries
// remain delegated to their owner; direct installs route through Corresync.
func UpgradeAdvice(method InstallMethod, version string) string {
	releaseVersion := strings.TrimPrefix(version, "v")
	switch method {
	case InstallHomebrew:
		return "brew upgrade nkiyohara/corresync/corresync"
	case InstallWinGet:
		return "winget upgrade --id nkiyohara.Corresync --exact"
	case InstallScoop:
		return "scoop update corresync"
	case InstallDeb:
		return "download and verify the new .deb, then run: sudo apt install ./corresync_" + releaseVersion + "-*_*.deb"
	case InstallRPM:
		return "download and verify the new .rpm, then run: sudo dnf install ./corresync-" + releaseVersion + "-*.rpm"
	case InstallAPK:
		return "download and verify the new .apk, then run: sudo apk add ./corresync_" + releaseVersion + "-r*_*.apk"
	case InstallDirect:
		return "corr update"
	}
	return "review the verified release and use the original installation surface"
}
