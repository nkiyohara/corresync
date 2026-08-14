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

	"github.com/nkiyohara/corresync/internal/accountstore"
	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/discovery"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/paths"
	"github.com/nkiyohara/corresync/internal/rollout"
	"github.com/nkiyohara/corresync/internal/session"
	"github.com/nkiyohara/corresync/internal/updatecheck"
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

type browserProber func(context.Context, string) (string, error)

type commandRunner func(context.Context, io.Writer, io.Writer, string, ...string) error

type directoryCommandRunner func(context.Context, io.Writer, io.Writer, string, string, ...string) error

type inputCommandRunner func(
	context.Context,
	io.Reader,
	io.Writer,
	io.Writer,
	string,
	...string,
) error

type runtime struct {
	context                        context.Context
	configPath                     string
	info                           buildinfo.Info
	stdin                          io.Reader
	stdout                         io.Writer
	stderr                         io.Writer
	launch                         browserLauncher
	probeBrowser                   browserProber
	endpoint                       func(string) (localipc.Endpoint, error)
	previousEndpoints              func(string) ([]localipc.Endpoint, error)
	stopEndpointOwner              func(context.Context, localipc.Endpoint) (int, error)
	startDaemon                    func(context.Context, string) error
	runCommand                     commandRunner
	runIntegrationCommand          commandRunner
	runIntegrationDirectoryCommand directoryCommandRunner
	runInputCommand                inputCommandRunner
	processID                      int
	checkUpdate                    func(context.Context) (updatecheck.Result, error)
	checkUpdateFresh               func(context.Context) (updatecheck.Result, error)
	installUpdate                  func(context.Context, func(updatecheck.InstallProgress)) (updatecheck.InstallResult, error)
	installMethod                  func() updatecheck.InstallMethod
	interactiveOutput              func() bool
	interactiveInput               func() bool
	interactiveStdout              func() bool
	lookupEnv                      func(string) (string, bool)
	userHomeDir                    func() (string, error)
	userConfigDir                  func() (string, error)
	workingDirectory               func() (string, error)
	integrationBundleDirectory     func(string) (string, error)
	accountDiscoverer              application.AccountDiscoverer
	agentHosts                     agenthost.Catalog
	detectAgentHosts               func(context.Context, agenthost.Request) (agenthost.Report, error)
	migrationOnce                  sync.Once
	migrationErr                   error
}

func newRuntime(
	ctx context.Context,
	configPath string,
	stdout, stderr io.Writer,
	info buildinfo.Info,
) *runtime {
	agentHosts := agenthost.DefaultCatalog()
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
		probeBrowser:      browser.Probe,
		endpoint:          localipc.Resolve,
		previousEndpoints: localipc.ResolvePrevious,
		stopEndpointOwner: localipc.StopEndpointOwner,
		startDaemon:       startDetachedDaemon,
		runCommand: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
			// #nosec G204 -- name and args come from typed setup plans or the
			// user's explicit editor selection.
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = os.Stdin
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		runIntegrationCommand: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
			// #nosec G204 -- name and args come from a typed integration plan.
			command := exec.CommandContext(ctx, name, args...)
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		runIntegrationDirectoryCommand: func(ctx context.Context, stdout, stderr io.Writer, directory, name string, args ...string) error {
			// #nosec G204 -- directory, name, and args are bound to a typed,
			// previewed integration plan. Host commands receive no terminal input.
			command := exec.CommandContext(ctx, name, args...)
			command.Dir = directory
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		runInputCommand: func(
			ctx context.Context,
			stdin io.Reader,
			stdout, stderr io.Writer,
			name string,
			args ...string,
		) error {
			// #nosec G204 -- feedback actions use fixed platform commands and
			// arguments derived only from the already reviewed redacted report.
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = stdin
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		},
		processID:                  os.Getpid(),
		lookupEnv:                  os.LookupEnv,
		userHomeDir:                os.UserHomeDir,
		userConfigDir:              os.UserConfigDir,
		workingDirectory:           os.Getwd,
		integrationBundleDirectory: findIntegrationBundleDirectory,
		accountDiscoverer:          discovery.New(discovery.Options{}),
		agentHosts:                 agentHosts,
	}
	var detectorOnce sync.Once
	var agentHostDetector *agenthost.Detector
	var detectorErr error
	app.detectAgentHosts = func(ctx context.Context, request agenthost.Request) (agenthost.Report, error) {
		detectorOnce.Do(func() {
			agentHostDetector, detectorErr = agenthost.NewDetector(agentHosts, agenthost.Options{
				GOOS: info.OS, GOARCH: info.Arch, LookupEnv: app.lookupEnv,
			})
		})
		if detectorErr != nil {
			return agenthost.Report{}, fmt.Errorf("initialize local agent-host detection: %w", detectorErr)
		}
		return agentHostDetector.Detect(ctx, request)
	}
	app.checkUpdate = func(ctx context.Context) (updatecheck.Result, error) {
		channel, err := app.updateChannel(ctx)
		if err != nil {
			return updatecheck.Result{}, err
		}
		cachePath, err := paths.UpdateCachePathForChannel(string(channel))
		if err != nil {
			return updatecheck.Result{}, err
		}
		return (updatecheck.Checker{
			CurrentVersion: app.info.Version,
			Channel:        channel,
			CachePath:      cachePath,
			Client:         &http.Client{Timeout: 5 * time.Second},
		}).Check(ctx)
	}
	app.checkUpdateFresh = func(ctx context.Context) (updatecheck.Result, error) {
		channel, err := app.updateChannel(ctx)
		if err != nil {
			return updatecheck.Result{}, err
		}
		cachePath, err := paths.UpdateCachePathForChannel(string(channel))
		if err != nil {
			return updatecheck.Result{}, err
		}
		return (updatecheck.Checker{
			CurrentVersion: app.info.Version,
			Channel:        channel,
			CachePath:      cachePath,
			Client:         &http.Client{Timeout: 15 * time.Second},
			Force:          true,
		}).Check(ctx)
	}
	app.installUpdate = func(
		ctx context.Context,
		progress func(updatecheck.InstallProgress),
	) (updatecheck.InstallResult, error) {
		channel, err := app.updateChannel(ctx)
		if err != nil {
			return updatecheck.InstallResult{}, err
		}
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
			Channel:        channel,
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
	app.interactiveInput = func() bool { return inputIsTerminal(app.stdin) }
	app.interactiveStdout = func() bool { return outputIsTerminal(app.stdout) }
	return app
}

func (app *runtime) updateChannel(ctx context.Context) (updatecheck.Channel, error) {
	configuration, _, err := app.loadConfigContext(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return updatecheck.ChannelStable, nil
	}
	if err != nil {
		return "", fmt.Errorf("load update channel: %w", err)
	}
	return updatecheck.Channel(configuration.Updates.Channel), nil
}

func (app *runtime) accountServices() (
	*application.AccountService,
	*application.AccountDiscoveryService,
	error,
) {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return nil, nil, err
	}
	store := accountstore.Store{
		ConfigPath:               path,
		DeleteOAuthAuthorization: oauthlocal.DeleteAuthorization,
	}
	available := []domain.ProviderID{
		domain.ProviderMicrosoftOWA,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftGraph,
	}
	if rollout.GoogleOAuthApproved {
		available = append(available, domain.ProviderGoogle)
	}
	accounts, err := application.NewAccountService(
		store,
		store,
		available,
		[]domain.ProviderID{
			domain.ProviderMicrosoftGraph, domain.ProviderTodoist,
			domain.ProviderCalDAV,
		},
	)
	if err != nil {
		return nil, nil, err
	}
	discoverer, err := application.NewAccountDiscoveryService(
		app.accountDiscoverer,
		available,
	)
	if err != nil {
		return nil, nil, err
	}
	return accounts, discoverer, nil
}

func (app *runtime) requireDaemonStopped() error {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	previous, err := app.activePreviousEndpoints(path)
	if err != nil {
		return err
	}
	if len(previous) > 0 {
		return errors.New(
			"account changes require every session owner to be stopped; run `corr daemon stop`",
		)
	}
	endpoint, err := app.endpoint(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(endpoint.CredentialPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect session owner credential: %w", err)
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(app.context, daemonProbeTimeout)
	defer cancel()
	owner, inspectErr := client.InspectOwner(ctx, app.caller())
	if owner.Status().ProcessID > 0 {
		return errors.New("account changes require a stopped session owner; run `corr daemon stop`")
	}
	if inspectErr != nil {
		return fmt.Errorf(
			"cannot safely determine whether the session owner is stopped; run `corr daemon stop`: %w",
			inspectErr,
		)
	}
	return nil
}

// openDaemon connects to the config-scoped session owner, starting it when
// absent. It never receives provider authorization material.
func (app *runtime) openDaemon(ctx context.Context) (*daemonapi.Client, daemonapi.Status, error) {
	return app.openDaemonWithOptions(ctx, false)
}

// openDaemonForMCP permits the provider-neutral onboarding state so MCP
// clients and registries can inspect the complete catalog before an account is
// configured. Ordinary CLI operations continue to require an account.
func (app *runtime) openDaemonForMCP(
	ctx context.Context,
) (*daemonapi.Client, daemonapi.Status, error) {
	return app.openDaemonWithOptions(ctx, true)
}

func (app *runtime) openDaemonWithOptions(
	ctx context.Context,
	allowNoAccounts bool,
) (*daemonapi.Client, daemonapi.Status, error) {
	configuration, _, err := app.loadConfigContext(ctx)
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	if len(configuration.Accounts) == 0 && !allowNoAccounts {
		return nil, daemonapi.Status{}, errors.New(
			"no account is configured; run `corr setup <email-address>` first",
		)
	}
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		return nil, daemonapi.Status{}, err
	}
	if err := app.migratePreviousDaemon(ctx, configPath, configDigest); err != nil {
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
	path := filepath.Clean(app.configPath)
	defaultPath, defaultErr := config.DefaultPath()
	if app.configPath == "" || defaultErr == nil && path == filepath.Clean(defaultPath) {
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
	absolute = filepath.Clean(absolute)
	legacyPath, legacyErr := config.LegacyDefaultPath()
	if legacyErr == nil && absolute == filepath.Clean(legacyPath) {
		path, migrated, ensureErr := config.EnsureDefaultPath()
		if ensureErr != nil {
			return "", ensureErr
		}
		if migrated {
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
	return absolute, nil
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
	defaultPath, defaultErr := config.DefaultPath()
	if defaultErr == nil && path == filepath.Clean(defaultPath) {
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
	web, ok := account.OutlookWeb()
	if !ok {
		return nil, session.Credentials{}, errors.New(
			"account routes do not use one shared Outlook Web browser session",
		)
	}
	profileDirectory, err := paths.ProfileDir(accountID)
	if err != nil {
		return nil, session.Credentials{}, err
	}
	if _, err := fmt.Fprintf(app.stderr, "Opening Outlook Web for account %q; complete sign-in in the browser.\n", accountID); err != nil {
		return nil, session.Credentials{}, err
	}
	handle, err := app.launch(ctx, browser.Options{
		Origin:     web.Origin,
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
