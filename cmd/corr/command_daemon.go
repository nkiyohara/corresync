package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/localipc"
	"github.com/nkiyohara/corresync/internal/panicguard"
)

const (
	daemonProbeTimeout             = 500 * time.Millisecond
	daemonControlTimeout           = 3 * time.Second
	daemonStartupTimeout           = 5 * time.Second
	daemonShutdownTimeout          = 10 * time.Second
	daemonReplacementTimeout       = daemonShutdownTimeout + time.Second
	daemonPollInterval             = 50 * time.Millisecond
	daemonUnavailableConfirmations = 2
)

type daemonCommand struct {
	Start  daemonStartCommand  `cmd:"" help:"Start the local daemon in the background."`
	Serve  daemonServeCommand  `cmd:"" help:"Run the local daemon in this terminal until it is stopped."`
	Status daemonStatusCommand `cmd:"" help:"Show whether the local daemon is running."`
	Stop   daemonStopCommand   `cmd:"" help:"Stop the local daemon; your sign-ins end with it."`
}

func (*daemonCommand) Help() string {
	return `The local daemon is the background process that keeps your sign-ins and
talks to your mail, calendar, and task providers. It runs as you, accepts
connections only from this computer, and has one copy per configuration
file. Other corr commands start it when they need it, so you rarely need
these commands.

Examples:
  corr daemon status
  corr daemon stop
  corr daemon start`
}

type daemonStartCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type daemonServeCommand struct{}

type daemonStatusCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type daemonStopCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type daemonStopResult struct {
	Stopping bool `json:"stopping"`
	Owners   int  `json:"owners,omitempty"`
}

type daemonUnavailableResult struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type daemonStatusResult struct {
	State string `json:"state"`
	daemonapi.Status
}

func (command *daemonStartCommand) Run(app *runtime) (returnErr error) {
	client, status, err := app.openDaemonForMCP(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	return writeDaemonStatus(app, status, command.JSON)
}

func (*daemonServeCommand) Run(app *runtime) (returnErr error) {
	if err := app.requireCurrentExecutable(); err != nil {
		return err
	}
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		return err
	}
	if err := app.migratePreviousDaemon(app.context, configPath, configDigest); err != nil {
		return err
	}
	endpoint, err := app.endpoint(configPath)
	if err != nil {
		return err
	}
	listener, err := localipc.Listen(endpoint)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, listener.Close()) }()

	backend, err := newSessionBackend(app)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, backend.Close()) }()
	credential, err := localipc.IssueCredential(endpoint)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, credential.Close()) }()
	server, err := daemonapi.NewServer(backend, daemonapi.ServerOptions{
		Context: app.context,
		Version: app.info.Version, ProcessID: app.processID,
		StartedAt: time.Now(), Credential: credential.Value(), ConfigDigest: configDigest,
		AllowNoDefaultAccount: len(backend.configuration.Accounts) == 0,
		RecordPanic: func() {
			panicguard.Record(app.context, panicguard.BoundaryDaemonRequest)
		},
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		app.stderr,
		"The local daemon is running (namespace %s); it accepts connections from this computer only.\n",
		endpoint.ID,
	); err != nil {
		return err
	}

	serveDone := make(chan error, 1)
	panicguard.Go(app.context, panicguard.BoundaryDaemonServer, func() {
		serveDone <- server.Serve(listener)
	})
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
running:
	for {
		select {
		case err := <-serveDone:
			return err
		case <-app.context.Done():
			break running
		case <-server.Done():
			break running
		case <-ticker.C:
			if app.executableGeneration.changed() {
				break running
			}
		}
	}

	// Close the application boundary and its browsers before Shutdown releases
	// the singleton listener. A replacement daemon must never overlap ownership
	// of the same protected browser profile.
	server.Drain()
	backendErr := backend.Close()
	shutdownContext, cancel := context.WithTimeout(context.Background(), daemonShutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownContext)
	return errors.Join(backendErr, shutdownErr, <-serveDone)
}

func (*daemonStatusCommand) timeoutContext(app *runtime) (context.Context, context.CancelFunc) {
	return context.WithTimeout(app.context, 3*time.Second)
}

func (command *daemonStatusCommand) Run(app *runtime) (returnErr error) {
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	endpoint, err := app.daemonControlEndpoint(configPath)
	if err != nil {
		return err
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	ctx, cancel := command.timeoutContext(app)
	defer cancel()
	status, err := client.Status(ctx, app.caller())
	if err != nil {
		reason := daemonUnavailableReason(err)
		if command.JSON {
			if writeErr := writeJSON(app.stdout, daemonUnavailableResult{
				State: "unavailable", Reason: reason,
			}); writeErr != nil {
				return writeErr
			}
		}
		// The cause can name private paths, so only the reason is shown.
		return daemonUnavailableError(reason)
	}
	return writeDaemonStatus(app, status, command.JSON)
}

// daemonUnavailableError explains a reason from daemonUnavailableReason in
// words and keeps the reason code for support.
func daemonUnavailableError(reason string) error {
	message, hint := "the local daemon could not be reached", shellCommand("corr", "doctor")
	switch reason {
	case string(localipc.CredentialMissing):
		message, hint = "the local daemon is not running", shellCommand("corr", "daemon", "start")
	case string(localipc.CredentialMalformed):
		message = "the local daemon's access file is damaged"
	case string(localipc.CredentialPermissionDenied):
		message = "you do not have permission to read the local daemon's access file"
	case string(localipc.CredentialUnavailable):
		message = "the local daemon's access file cannot be read"
	case "authentication_failed":
		message = "the local daemon refused this command because its access check failed"
	case "incompatible_protocol":
		message, hint = "the local daemon is a different version of corr", shellCommand("corr", "daemon", "start")
	case "timeout":
		message, hint = "the local daemon did not answer within 3 seconds", shellCommand("corr", "daemon", "status")
	}
	return withHint(fmt.Errorf("%s (%s)", message, reason), hint)
}

// daemonFailure is a failed call to the local daemon, explained in words. Its
// message replaces the cause's text; errors.Is and errors.As still see it.
type daemonFailure struct {
	message string
	err     error
}

func (failure *daemonFailure) Error() string { return failure.message }

func (failure *daemonFailure) Unwrap() error { return failure.err }

// describeDaemonFailure says why a call to the local daemon failed: it is not
// running, is another version of corr, refused the command, or did not
// answer. Other errors, which the daemon keeps free of mail content, are
// shown as they are. action names what was attempted, e.g. "stop the local
// daemon".
func describeDaemonFailure(action string, err error) error {
	if err == nil {
		return nil
	}
	if reason, ok := localipc.CredentialLoadFailure(err); ok {
		failure := daemonUnavailableError(string(reason))
		hint, _ := errorHint(failure)
		return withHint(&daemonFailure{message: action + ": " + failure.Error(), err: err}, hint)
	}
	var remoteErr *daemonapi.Error
	var versionErr *daemonapi.ProtocolVersionError
	switch {
	case errors.As(err, &versionErr):
		return withHint(&daemonFailure{
			message: fmt.Sprintf(
				"%s: the local daemon is a different version of corr (protocol %d; this corr uses %d)",
				action, versionErr.DaemonVersion, versionErr.ClientVersion,
			),
			err: err,
		}, shellCommand("corr", "daemon", "start"))
	case errors.As(err, &remoteErr) && remoteErr.Code == "unauthorized":
		return withHint(&daemonFailure{
			message: action + ": the local daemon refused this command because its access check failed",
			err:     err,
		}, shellCommand("corr", "doctor"))
	case errors.As(err, &remoteErr):
		return fmt.Errorf("%s: %w", action, err)
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return withHint(&daemonFailure{
			message: action + ": the local daemon is not running",
			err:     err,
		}, shellCommand("corr", "daemon", "start"))
	case errors.Is(err, context.DeadlineExceeded):
		return withHint(&daemonFailure{
			message: action + ": the local daemon did not answer in time",
			err:     err,
		}, shellCommand("corr", "daemon", "status"))
	}
	return fmt.Errorf("%s: %w", action, err)
}

func daemonUnavailableReason(err error) string {
	if reason, ok := localipc.CredentialLoadFailure(err); ok {
		return string(reason)
	}
	var remoteErr *daemonapi.Error
	if errors.As(err, &remoteErr) && remoteErr.Code == "unauthorized" {
		return "authentication_failed"
	}
	var versionErr *daemonapi.ProtocolVersionError
	if errors.As(err, &versionErr) {
		return "incompatible_protocol"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "unreachable"
}

func (command *daemonStopCommand) Run(app *runtime) (returnErr error) {
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	active, err := app.activeDaemonEndpoints(configPath)
	if err != nil {
		return err
	}
	if len(active) > 1 {
		return command.stopSplitOwners(app, active)
	}
	endpoint, err := app.daemonControlEndpoint(configPath)
	if err != nil {
		return err
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	ctx, cancel := context.WithTimeout(app.context, 3*time.Second)
	defer cancel()
	if err := client.Shutdown(ctx, app.caller()); err != nil {
		if reason, ok := localipc.CredentialLoadFailure(err); ok && reason == localipc.CredentialMissing {
			return &daemonFailure{
				message: "the local daemon is not running, so there is nothing to stop (" + string(reason) + ")",
				err:     err,
			}
		}
		return describeDaemonFailure("stop the local daemon", err)
	}
	result := daemonStopResult{Stopping: true, Owners: 1}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("The local daemon is stopping"),
		view.muted("Your sign-ins end with it. The next corr command that needs it starts it again."),
	)
	return err
}

func (command *daemonStopCommand) stopSplitOwners(
	app *runtime,
	endpoints []localipc.Endpoint,
) error {
	ctx, cancel := context.WithTimeout(app.context, daemonReplacementTimeout)
	defer cancel()

	var failures []error
	for index, endpoint := range endpoints {
		processID, err := app.stopEndpointOwner(ctx, endpoint)
		if err != nil {
			failures = append(failures, fmt.Errorf(
				"stop extra local daemon %d of %d: %w",
				index+1,
				len(endpoints),
				err,
			))
			continue
		}
		if processID < 2 {
			failures = append(failures, fmt.Errorf(
				"stop extra local daemon %d of %d: it reported an invalid process ID",
				index+1,
				len(endpoints),
			))
		}
	}
	if err := waitForEndpointsInactive(ctx, endpoints); err != nil {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}

	result := daemonStopResult{Stopping: false, Owners: len(endpoints)}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong(fmt.Sprintf(
			"Stopped %d copies of the local daemon",
			len(endpoints),
		)),
		view.muted("Some used runtime folders from an older version. The next corr command that needs the daemon starts one copy."),
	)
	return err
}

func waitForEndpointsInactive(
	ctx context.Context,
	endpoints []localipc.Endpoint,
) error {
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()
	for {
		remaining := 0
		for _, endpoint := range endpoints {
			active, err := localipc.EndpointActive(endpoint)
			if err != nil {
				return fmt.Errorf("check that the extra local daemons stopped: %w", err)
			}
			if active {
				remaining++
			}
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"%d extra local daemon(s) did not stop: %w",
				remaining,
				ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func waitForDaemon(parent context.Context, app *runtime, client *daemonapi.Client, timeout time.Duration) (daemonapi.Status, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := client.Status(ctx, app.caller())
		if err == nil {
			return status, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return daemonapi.Status{}, fmt.Errorf(
				"the local daemon did not become ready: %w",
				errors.Join(ctx.Err(), lastErr),
			)
		case <-ticker.C:
		}
	}
}

func writeDaemonStatus(app *runtime, status daemonapi.Status, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(app.stdout, daemonStatusResult{
			State: "ready", Status: status,
		})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	defaultAccount := sanitizeCell(string(status.DefaultAccount), 64)
	if defaultAccount == "" {
		defaultAccount = "none"
	}
	_, err := view.printf(
		"%s  %s\n\n  %-16s %s\n  %-16s %d\n  %-16s %d\n  %-16s %s\n\n%s\n",
		view.success(),
		view.strong("The local daemon is running"),
		"Version",
		status.Version,
		"Process ID",
		status.ProcessID,
		"Protocol",
		status.ProtocolVersion,
		"Default account",
		defaultAccount,
		view.nextStep(shellCommand("corr", "auth", "status")),
	)
	return err
}
