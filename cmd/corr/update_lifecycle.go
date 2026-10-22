package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

// Verification precedes any daemon shutdown. The authenticated owner snapshot
// binds shutdown to one generation; no process enumeration or provider login.
func (app *runtime) installWithDaemonLifecycle(ctx context.Context, executable string, progress func(updatecheck.InstallProgress), channel updatecheck.Channel, trustCache string) (updatecheck.InstallResult, error) {
	var restart bool
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return updatecheck.InstallResult{}, err
	}
	installer := updatecheck.Installer{
		CurrentVersion: app.info.Version, Channel: channel, Executable: executable,
		TrustCachePath: trustCache, Client: &http.Client{Timeout: 2 * time.Minute},
		GOOS: app.info.OS, GOARCH: app.info.Arch, Progress: progress,
		BeforeReplace: func(ctx context.Context) error {
			var stopErr error
			restart, stopErr = app.stopUpdateOwner(ctx, configPath)
			return stopErr
		},
	}
	result, installErr := installer.Install(ctx)
	if !restart {
		return result, installErr
	}
	// Use the saved installed path, never os.Executable after replacement (which
	// can resolve to the rollback copy on Linux). A failed install restores it.
	if result.CanonicalPath != "" {
		executable = result.CanonicalPath
	}
	restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), daemonReplacementTimeout+daemonStartupTimeout)
	defer cancel()
	restartErr := app.runInputCommand(restartCtx, strings.NewReader(""), io.Discard, app.stderr,
		executable, "--config", configPath, "daemon", "start", "--json")
	if restartErr != nil {
		restartErr = fmt.Errorf("restart session owner after update; run corr daemon start: %w", restartErr)
	}
	return result, errors.Join(installErr, restartErr)
}

func (app *runtime) stopUpdateOwner(ctx context.Context, configPath string) (bool, error) {
	endpoints, err := app.activeDaemonEndpoints(configPath)
	if err != nil {
		return false, err
	}
	if len(endpoints) == 0 {
		return false, nil
	}
	if len(endpoints) != 1 {
		return false, errors.New("multiple session owners; run corr daemon stop before updating")
	}
	client, err := daemonapi.NewClient(endpoints[0])
	if err != nil {
		return false, err
	}
	defer func() { _ = client.Close() }()
	controlCtx, cancel := context.WithTimeout(ctx, daemonReplacementTimeout)
	defer cancel()
	owner, err := client.InspectOwner(controlCtx, app.caller())
	// InspectOwner validates identity even for a recognized protocol mismatch.
	var mismatch *daemonapi.ProtocolVersionError
	if err != nil && !errors.As(err, &mismatch) {
		return false, err
	}
	if err := rejectNewerOwner(app.info.Version, owner.Status().Version); err != nil {
		return false, err
	}
	if err := client.ShutdownOwner(controlCtx, app.caller(), owner); err != nil {
		return false, err
	}
	if err := waitForEndpointsInactive(controlCtx, endpoints); err != nil {
		return false, err
	}
	return true, nil
}

func rejectNewerOwner(current, owner string) error {
	current = "v" + strings.TrimPrefix(current, "v")
	owner = "v" + strings.TrimPrefix(owner, "v")
	if semver.IsValid(current) && semver.IsValid(owner) && semver.Compare(owner, current) > 0 {
		return errors.New("a newer session owner is running; restart corr using the updated executable")
	}
	return nil
}

// Returning the child's exit status prevents replaying a provider command on
// the old loaded image. The check is skipped once in the child, not persisted.
func (app *runtime) runUpdatedCommand(ctx context.Context, arguments []string, result updatecheck.InstallResult) int {
	executable := app.executablePath
	if result.CanonicalPath != "" {
		executable = result.CanonicalPath
	}
	if executable == "" {
		_, _ = fmt.Fprintln(app.stderr, "updated executable path is unavailable; restart corr")
		return 1
	}
	command := exec.CommandContext(ctx, executable, arguments...) // #nosec G204,G702 -- installed, verified executable; original CLI arguments are passed directly without a shell.
	command.Stdin, command.Stdout, command.Stderr = app.stdin, app.stdout, app.stderr
	command.Env = append(os.Environ(), "CORRESYNC_NO_UPDATE_CHECK=1")
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(app.stderr, "start updated corr: %v\n", err)
		return 1
	}
	return 0
}
