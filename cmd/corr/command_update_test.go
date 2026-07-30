package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

func TestUpdateCheckProducesHumanAndMachineReadableStatus(t *testing.T) {
	result := updatecheck.Result{
		Status: updatecheck.StatusAvailable, CurrentVersion: "0.3.2",
		LatestVersion: "v0.4.0", UpdateAvailable: true,
		ReleaseURL: "https://github.com/nkiyohara/corresync/releases/tag/v0.4.0",
		CheckedAt:  time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	for _, jsonOutput := range []bool{false, true} {
		var stdout bytes.Buffer
		app := updateTestRuntime(t, &stdout, result)
		command := updateCheckCommand{JSON: jsonOutput}
		if err := command.Run(app); err != nil {
			t.Fatalf("Run(JSON=%v) error = %v", jsonOutput, err)
		}
		if jsonOutput {
			var report updateReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode JSON: %v", err)
			}
			if report.Status != updatecheck.StatusAvailable || strings.Contains(stdout.String(), "Update available:") {
				t.Fatalf("unexpected machine output: %s", stdout.String())
			}
		} else if !strings.Contains(stdout.String(), "Update available") ||
			!strings.Contains(stdout.String(), "0.3.2 → 0.4.0") ||
			!strings.Contains(stdout.String(), "brew upgrade nkiyohara/corresync/corresync") {
			t.Fatalf("unexpected human output: %s", stdout.String())
		}
	}
}

func TestCurrentUpdateJSONOmitsUpgradeInstructions(t *testing.T) {
	result := updatecheck.Result{
		Status:          updatecheck.StatusCurrent,
		CurrentVersion:  "0.4.2",
		LatestVersion:   "v0.4.2",
		UpdateAvailable: false,
		ReleaseURL:      "https://github.com/nkiyohara/corresync/releases/tag/v0.4.2",
		CheckedAt:       time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	if err := (&updateCheckCommand{JSON: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if _, exists := report["upgrade"]; exists {
		t.Fatalf("current report contains upgrade instruction: %s", stdout.String())
	}
	if _, exists := report["installMethod"]; exists {
		t.Fatalf("current report contains install method: %s", stdout.String())
	}
}

func TestUpdateUsesPackageManagerWithoutChangingFiles(t *testing.T) {
	result := updatecheck.Result{
		Status:          updatecheck.StatusAvailable,
		CurrentVersion:  "0.4.1",
		LatestVersion:   "v0.4.2",
		UpdateAvailable: true,
		ReleaseURL:      "https://github.com/nkiyohara/corresync/releases/tag/v0.4.2",
	}
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallHomebrew }
	app.installUpdate = func(
		context.Context,
		func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		t.Fatal("package-manager installation attempted direct replacement")
		return updatecheck.InstallResult{}, nil
	}
	if err := (&updateApplyCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Managed by Homebrew") ||
		!strings.Contains(stdout.String(), "brew upgrade nkiyohara/corresync/corresync") ||
		!strings.Contains(stdout.String(), "did not modify files") {
		t.Fatalf("unexpected package-manager output: %q", stdout.String())
	}
}

func TestUpdateDirectInstallProducesStableJSON(t *testing.T) {
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, updatecheck.Result{})
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	app.installUpdate = func(
		_ context.Context,
		progress func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		if progress != nil {
			t.Fatal("JSON update enabled progress output")
		}
		return updatecheck.InstallResult{
			Status:          updatecheck.InstallStatusUpdated,
			PreviousVersion: "0.4.1",
			CurrentVersion:  "0.4.2",
			LatestVersion:   "v0.4.2",
			ReleaseURL:      "https://github.com/nkiyohara/corresync/releases/tag/v0.4.2",
			Archive:         "corresync_0.4.2_linux_amd64.tar.gz",
			BackupPath:      "/synthetic/corresync.backup-0.4.1",
		}, nil
	}
	if err := (&updateApplyCommand{JSON: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	var report updateActionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "updated" || !report.Updated ||
		report.InstallMethod != updatecheck.InstallDirect ||
		len(report.Verification) != 4 ||
		strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("unexpected direct update JSON: %s", stdout.String())
	}
}

func TestUpdateRepairsMissingPrimaryCommand(t *testing.T) {
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, updatecheck.Result{})
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	app.installUpdate = func(
		_ context.Context,
		progress func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		if progress != nil {
			t.Fatal("JSON repair enabled progress output")
		}
		return updatecheck.InstallResult{
			Status:         updatecheck.InstallStatusRepaired,
			CurrentVersion: "0.8.0",
			LatestVersion:  "v0.8.0",
			ReleaseURL:     "https://github.com/nkiyohara/corresync/releases/tag/v0.8.0",
			Archive:        "corresync_0.8.0_linux_amd64.tar.gz",
			CanonicalPath:  "/synthetic/corr",
		}, nil
	}
	if err := (&updateApplyCommand{JSON: true}).Run(app); err != nil {
		t.Fatal(err)
	}
	var report updateActionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "repaired" || report.Updated ||
		report.CurrentVersion != "0.8.0" ||
		report.CanonicalPath != "/synthetic/corr" ||
		len(report.Verification) != 4 ||
		report.BackupPath != "" {
		t.Fatalf("unexpected direct repair JSON: %s", stdout.String())
	}
}

func TestAutomaticUpdateNoticeIsTTYOnlyAndHonorsOptOuts(t *testing.T) {
	result := updatecheck.Result{
		Status: updatecheck.StatusAvailable, CurrentVersion: "0.3.2",
		LatestVersion: "v0.4.0", UpdateAvailable: true,
	}
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	var stderr bytes.Buffer
	app.stderr = &stderr
	app.interactiveOutput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	app.installUpdate = func(
		context.Context,
		func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		t.Fatal("default update check attempted automatic installation")
		return updatecheck.InstallResult{}, nil
	}
	app.maybeHandleAutomaticUpdate(t.Context())
	if !strings.Contains(stderr.String(), "Update available") ||
		!strings.Contains(stderr.String(), "Run corr update") {
		t.Fatalf("TTY notice missing: %q", stderr.String())
	}

	stderr.Reset()
	app.interactiveOutput = func() bool { return false }
	app.maybeHandleAutomaticUpdate(t.Context())
	if stderr.Len() != 0 {
		t.Fatalf("non-TTY emitted notice: %q", stderr.String())
	}
	app.interactiveOutput = func() bool { return true }
	app.interactiveStdout = func() bool { return false }
	app.maybeHandleAutomaticUpdate(t.Context())
	if stderr.Len() != 0 {
		t.Fatalf("piped stdout emitted notice: %q", stderr.String())
	}
	app.interactiveStdout = func() bool { return true }

	configuration := config.OutlookDefault()
	configuration.Updates.DisableAutomaticChecks = true
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.configPath = configPath
	stderr.Reset()
	app.maybeHandleAutomaticUpdate(t.Context())
	if stderr.Len() != 0 {
		t.Fatalf("config opt-out emitted notice: %q", stderr.String())
	}

	configuration.Updates.DisableAutomaticChecks = false
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.lookupEnv = func(name string) (string, bool) {
		if name == "CORRESYNC_NO_UPDATE_CHECK" {
			return "1", true
		}
		return "", false
	}
	app.maybeHandleAutomaticUpdate(t.Context())
	if stderr.Len() != 0 {
		t.Fatalf("environment opt-out emitted notice: %q", stderr.String())
	}
}

func TestAutomaticUpdateInstallsOnlyAnOptedInDirectBinary(t *testing.T) {
	result := updatecheck.Result{
		Status: updatecheck.StatusAvailable, CurrentVersion: "0.8.1",
		LatestVersion: "v0.8.2", UpdateAvailable: true,
	}
	var stdout, stderr bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	app.stderr = &stderr
	app.interactiveOutput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	configuration := config.OutlookDefault()
	configuration.Updates.AutoInstall = true
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.configPath = configPath
	installCalls := 0
	app.installUpdate = func(
		_ context.Context,
		progress func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		installCalls++
		if progress != nil {
			t.Fatal("automatic install exposed progress output")
		}
		return updatecheck.InstallResult{
			Status:          updatecheck.InstallStatusUpdated,
			PreviousVersion: "0.8.1",
			CurrentVersion:  "0.8.2",
			LatestVersion:   "v0.8.2",
			BackupPath:      "/synthetic/corr.backup-0.8.1",
		}, nil
	}

	app.maybeHandleAutomaticUpdate(t.Context())

	if installCalls != 1 ||
		!strings.Contains(stderr.String(), "installing verified standalone update") ||
		!strings.Contains(stderr.String(), "Corresync 0.8.2 installed") ||
		!strings.Contains(stderr.String(), "active on the next corr start") {
		t.Fatalf("automatic direct update = calls %d, output %q", installCalls, stderr.String())
	}
}

func TestAutomaticUpdateNeverRunsAPackageManager(t *testing.T) {
	result := updatecheck.Result{
		Status: updatecheck.StatusAvailable, CurrentVersion: "0.8.1",
		LatestVersion: "v0.8.2", UpdateAvailable: true,
	}
	var stdout, stderr bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	app.stderr = &stderr
	app.interactiveOutput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	configuration := config.OutlookDefault()
	configuration.Updates.AutoInstall = true
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.configPath = configPath
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallHomebrew }
	app.installUpdate = func(
		context.Context,
		func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		t.Fatal("automatic update attempted to modify a package-managed installation")
		return updatecheck.InstallResult{}, nil
	}

	app.maybeHandleAutomaticUpdate(t.Context())

	if !strings.Contains(stderr.String(), "Update available") ||
		!strings.Contains(stderr.String(), "brew upgrade nkiyohara/corresync/corresync") ||
		strings.Contains(stderr.String(), "installing verified standalone update") {
		t.Fatalf("managed automatic update output = %q", stderr.String())
	}
}

func TestAutomaticUpdateFailureDoesNotBlockTheRequestedCommand(t *testing.T) {
	result := updatecheck.Result{
		Status: updatecheck.StatusAvailable, CurrentVersion: "0.8.1",
		LatestVersion: "v0.8.2", UpdateAvailable: true,
	}
	var stdout, stderr bytes.Buffer
	app := updateTestRuntime(t, &stdout, result)
	app.stderr = &stderr
	app.interactiveOutput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	configuration := config.OutlookDefault()
	configuration.Updates.AutoInstall = true
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.configPath = configPath
	app.installUpdate = func(
		context.Context,
		func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		return updatecheck.InstallResult{}, context.DeadlineExceeded
	}

	app.maybeHandleAutomaticUpdate(t.Context())

	if !strings.Contains(stderr.String(), "Automatic update failed") ||
		!strings.Contains(stderr.String(), "continuing with 0.8.1") ||
		!strings.Contains(stderr.String(), "corr update") {
		t.Fatalf("automatic update failure output = %q", stderr.String())
	}
}

func TestMachineSurfacesNeverHandleAutomaticUpdates(t *testing.T) {
	tests := [][]string{
		{"mcp", "serve"},
		{"--config", "/synthetic/config.toml", "mcp", "serve"},
		{"mcp", "config", "codex"},
		{"completion", "bash"},
		{"version", "--json"},
		{"doctor", "--json"},
		{"update", "check"},
		{"config", "set", "updates.auto_install", "false"},
		{"daemon", "run"},
	}
	for _, arguments := range tests {
		if shouldHandleAutomaticUpdate(arguments) {
			t.Errorf("shouldHandleAutomaticUpdate(%q) = true", arguments)
		}
	}
	if !shouldHandleAutomaticUpdate([]string{"mail", "list"}) {
		t.Fatal("human-facing mail command did not allow a quiet notice")
	}
}

func TestUpdateViewHonorsNoColor(t *testing.T) {
	var stdout bytes.Buffer
	app := updateTestRuntime(t, &stdout, updatecheck.Result{})
	app.interactiveOutput = func() bool { return true }
	app.lookupEnv = func(name string) (string, bool) {
		if name == "NO_COLOR" {
			return "1", true
		}
		return "", false
	}
	view := newUpdateView(app, app.stdout, true)
	if err := view.writeAction(updateActionReport{
		Status:         "action_required",
		CurrentVersion: "0.4.1",
		LatestVersion:  "v0.4.2",
		InstallMethod:  updatecheck.InstallScoop,
		Command:        "scoop update corresync",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("NO_COLOR output contains ANSI escapes: %q", stdout.String())
	}
}

func updateTestRuntime(t *testing.T, stdout *bytes.Buffer, result updatecheck.Result) *runtime {
	t.Helper()
	app := newRuntime(context.Background(), filepath.Join(t.TempDir(), "missing.toml"), stdout, &bytes.Buffer{}, buildinfo.Current())
	app.checkUpdate = func(context.Context) (updatecheck.Result, error) { return result, nil }
	app.checkUpdateFresh = func(context.Context) (updatecheck.Result, error) { return result, nil }
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallHomebrew }
	return app
}
