package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/browser"
	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
	"github.com/nkiyohara/owa-bridge/internal/paths"
	"github.com/nkiyohara/owa-bridge/internal/session"
	"github.com/nkiyohara/owa-bridge/internal/updatecheck"
)

type browserHandle interface {
	WaitForSession(context.Context) (session.Credentials, error)
	Apply(*http.Request) error
	Close() error
}

type terminalBrowserHandle interface {
	browserHandle
	CurrentSession() (session.Credentials, error)
	TerminalSnapshot(context.Context) (browser.TerminalView, error)
	TerminalAct(context.Context, browser.TerminalAction) error
}

type browserLauncher func(context.Context, browser.Options) (browserHandle, error)

type commandRunner func(context.Context, io.Writer, io.Writer, string, ...string) error

type runtime struct {
	context           context.Context
	configPath        string
	info              buildinfo.Info
	stdin             io.Reader
	stdout            io.Writer
	stderr            io.Writer
	launch            browserLauncher
	endpoint          func(string) (localipc.Endpoint, error)
	startDaemon       func(context.Context, string) error
	runCommand        commandRunner
	processID         int
	checkUpdate       func(context.Context) (updatecheck.Result, error)
	checkUpdateFresh  func(context.Context) (updatecheck.Result, error)
	installUpdate     func(context.Context, func(updatecheck.InstallProgress)) (updatecheck.InstallResult, error)
	installMethod     func() updatecheck.InstallMethod
	interactiveOutput func() bool
	interactiveStdout func() bool
	lookupEnv         func(string) (string, bool)
	migrationOnce     sync.Once
	migrationErr      error
}

func newRuntime(
	ctx context.Context,
	configPath string,
	stdout, stderr io.Writer,
	info buildinfo.Info,
) *runtime {
	app := &runtime{
		context:    ctx,
		configPath: configPath,
		info:       info,
		stdin:      os.Stdin,
		stdout:     stdout,
		stderr:     stderr,
		launch: func(ctx context.Context, options browser.Options) (browserHandle, error) {
			return browser.Launch(ctx, options)
		},
		endpoint:    localipc.Resolve,
		startDaemon: startDetachedDaemon,
		runCommand: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
			// #nosec G204 -- name and args come from typed setup plans or the
			// user's explicit editor selection.
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = os.Stdin
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		processID: os.Getpid(),
		lookupEnv: os.LookupEnv,
	}
	app.checkUpdate = func(ctx context.Context) (updatecheck.Result, error) {
		cachePath, err := paths.UpdateCachePath()
		if err != nil {
			return updatecheck.Result{}, err
		}
		return (updatecheck.Checker{
			CurrentVersion: app.info.Version,
			CachePath:      cachePath,
			Client:         &http.Client{Timeout: 5 * time.Second},
		}).Check(ctx)
	}
	app.checkUpdateFresh = func(ctx context.Context) (updatecheck.Result, error) {
		cachePath, err := paths.UpdateCachePath()
		if err != nil {
			return updatecheck.Result{}, err
		}
		return (updatecheck.Checker{
			CurrentVersion: app.info.Version,
			CachePath:      cachePath,
			Client:         &http.Client{Timeout: 15 * time.Second},
			Force:          true,
		}).Check(ctx)
	}
	app.installUpdate = func(
		ctx context.Context,
		progress func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		executable, err := os.Executable()
		if err != nil {
			return updatecheck.InstallResult{}, fmt.Errorf("resolve running executable: %w", err)
		}
		trustCachePath, err := paths.UpdateTrustCachePath()
		if err != nil {
			return updatecheck.InstallResult{}, err
		}
		return (updatecheck.Installer{
			CurrentVersion: app.info.Version,
			Executable:     executable,
			TrustCachePath: trustCachePath,
			Client:         &http.Client{Timeout: 2 * time.Minute},
			GOOS:           app.info.OS,
			GOARCH:         app.info.Arch,
			Progress:       progress,
		}).Install(ctx)
	}
	app.installMethod = func() updatecheck.InstallMethod {
		executable, err := os.Executable()
		if err != nil {
			return updatecheck.InstallDirect
		}
		return updatecheck.DetectInstallation(executable)
	}
	app.interactiveOutput = func() bool { return outputIsTerminal(app.stderr) }
	app.interactiveStdout = func() bool { return outputIsTerminal(app.stdout) }
	return app
}

// openDaemon connects to the config-scoped session owner, starting it when
// absent. It never receives Outlook authorization material.
func (app *runtime) openDaemon(ctx context.Context) (*daemonapi.Client, daemonapi.Status, error) {
	if _, _, err := app.loadConfigContext(ctx); err != nil {
		return nil, daemonapi.Status{}, err
	}
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	endpoint, err := app.endpoint(configPath)
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	probeContext, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	owner, statusErr := client.InspectOwner(probeContext, app.caller())
	cancel()
	status := owner.Status()
	if statusErr == nil {
		if err := app.validateDaemonConfig(status, configDigest); err != nil {
			return nil, daemonapi.Status{}, errors.Join(err, client.Close())
		}
		if status.Version != app.info.Version {
			status, err = app.replaceDaemon(ctx, client, owner, configPath, configDigest)
			if err != nil {
				return nil, daemonapi.Status{}, errors.Join(err, client.Close())
			}
		}
		return client, status, nil
	}
	var versionErr *daemonapi.ProtocolVersionError
	if errors.As(statusErr, &versionErr) {
		if status.ProcessID < 1 || status.ProtocolVersion != versionErr.DaemonVersion {
			return nil, daemonapi.Status{}, errors.Join(
				fmt.Errorf("inspect incompatible session owner: %w", statusErr),
				client.Close(),
			)
		}
		if err := app.validateDaemonConfig(status, configDigest); err != nil {
			return nil, daemonapi.Status{}, errors.Join(err, client.Close())
		}
		status, err = app.replaceDaemon(ctx, client, owner, configPath, configDigest)
		if err != nil {
			return nil, daemonapi.Status{}, errors.Join(err, client.Close())
		}
		return client, status, nil
	}
	if err := app.startDaemon(ctx, configPath); err != nil {
		return nil, daemonapi.Status{}, errors.Join(err, client.Close())
	}
	status, err = waitForDaemon(ctx, app, client, daemonStartupTimeout)
	if err != nil {
		return nil, daemonapi.Status{}, errors.Join(err, client.Close())
	}
	if err := app.validateDaemonStatus(status, configDigest); err != nil {
		return nil, daemonapi.Status{}, errors.Join(err, client.Close())
	}
	return client, status, nil
}

func (app *runtime) resolvedConfigPath() (string, error) {
	if app.configPath == "" {
		path, migrated, err := config.EnsureDefaultPath()
		if err != nil {
			return "", err
		}
		if migrated {
			legacyPath, legacyErr := config.LegacyDefaultPath()
			if legacyErr != nil {
				return "", legacyErr
			}
			if _, writeErr := fmt.Fprintf(
				app.stderr,
				"Migrated configuration to %s; rollback copy preserved at %s.\n",
				path,
				legacyPath,
			); writeErr != nil {
				return "", writeErr
			}
		}
		return path, nil
	}
	absolute, err := filepath.Abs(app.configPath)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func (app *runtime) loadConfig() (config.Config, string, error) {
	return app.loadConfigContext(app.context)
}

func (app *runtime) loadConfigContext(
	ctx context.Context,
) (config.Config, string, error) {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return config.Config{}, "", err
	}
	configuration, err := config.Load(path)
	if err != nil {
		return config.Config{}, path, err
	}
	if app.configPath == "" {
		app.migrationOnce.Do(func() {
			app.migrationErr = app.migrateLegacyState(ctx, configuration)
		})
		if app.migrationErr != nil {
			return config.Config{}, path, app.migrationErr
		}
	}
	return configuration, path, nil
}

func (app *runtime) account(
	configuration config.Config,
	requested string,
) (domain.AccountID, error) {
	_, account, err := configuration.ResolveAccount(requested)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}

func (app *runtime) authenticate(
	ctx context.Context,
	configuration config.Config,
	accountID domain.AccountID,
	account config.Account,
) (browserHandle, session.Credentials, error) {
	profileDirectory, err := paths.ProfileDir(accountID)
	if err != nil {
		return nil, session.Credentials{}, err
	}
	if _, err := fmt.Fprintf(app.stderr, "Opening Outlook Web for account %q; complete sign-in in the browser.\n", accountID); err != nil {
		return nil, session.Credentials{}, err
	}
	handle, err := app.launch(ctx, browser.Options{
		Origin:     account.Origin,
		ProfileDir: profileDirectory,
		Executable: configuration.Browser.Executable,
	})
	if err != nil {
		return nil, session.Credentials{}, err
	}
	waitContext, cancel := context.WithTimeout(ctx, time.Duration(configuration.Browser.LoginTimeout))
	defer cancel()
	credentials, err := handle.WaitForSession(waitContext)
	if err != nil {
		closeErr := handle.Close()
		return nil, session.Credentials{}, errors.Join(err, closeErr)
	}
	return handle, credentials, nil
}

func (app *runtime) caller() domain.Caller {
	return domain.Caller{Surface: "cli", Instance: fmt.Sprintf("process-%d", app.processID)}
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
