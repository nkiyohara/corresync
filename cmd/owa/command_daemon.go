package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
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
	Start  daemonStartCommand  `cmd:"" help:"Start the session owner in the background."`
	Serve  daemonServeCommand  `cmd:"" help:"Run the session owner in the foreground."`
	Status daemonStatusCommand `cmd:"" help:"Inspect a running session owner."`
	Stop   daemonStopCommand   `cmd:"" help:"Stop the session owner gracefully."`
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

func (command *daemonStartCommand) Run(app *runtime) (returnErr error) {
	client, status, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	return writeDaemonStatus(app, status, command.JSON)
}

func (*daemonServeCommand) Run(app *runtime) (returnErr error) {
	configPath, err := app.resolvedConfigPath()
	if err != nil {
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

	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		return err
	}
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
		Version: app.info.Version, ProcessID: app.processID,
		StartedAt: time.Now(), Credential: credential.Value(), ConfigDigest: configDigest,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		app.stderr,
		"OWA session owner ready (namespace %s, local IPC only).\n",
		endpoint.ID,
	); err != nil {
		return err
	}

	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case err := <-serveDone:
		return err
	case <-app.context.Done():
	case <-server.Done():
	}
	// Close the application boundary and its browsers before Shutdown releases
	// the singleton listener. A replacement daemon must never overlap ownership
	// of the same protected browser profile.
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
	endpoint, err := app.endpoint(configPath)
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
		return fmt.Errorf("session owner is unavailable: %w", err)
	}
	return writeDaemonStatus(app, status, command.JSON)
}

func (command *daemonStopCommand) Run(app *runtime) (returnErr error) {
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	endpoint, err := app.endpoint(configPath)
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
		return fmt.Errorf("stop session owner: %w", err)
	}
	result := struct {
		Stopping bool `json:"stopping"`
	}{Stopping: true}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n",
		view.success(),
		view.strong("OWA session owner is stopping"),
	)
	return err
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
				"session owner did not become ready: %w",
				errors.Join(ctx.Err(), lastErr),
			)
		case <-ticker.C:
		}
	}
}

func writeDaemonStatus(app *runtime, status daemonapi.Status, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(app.stdout, status)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := view.printf(
		"%s  %s\n\n  %-10s %s\n  %-10s %d\n  %-10s %d\n  %-10s %s\n",
		view.success(),
		view.strong("OWA session owner is ready"),
		"Version",
		status.Version,
		"PID",
		status.ProcessID,
		"Protocol",
		status.ProtocolVersion,
		"Account",
		sanitizeCell(string(status.DefaultAccount), 64),
	)
	return err
}
