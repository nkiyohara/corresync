package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/localipc"
)

func (app *runtime) activePreviousEndpoints(configPath string) ([]localipc.Endpoint, error) {
	if app.previousEndpoints == nil {
		return nil, nil
	}
	candidates, err := app.previousEndpoints(configPath)
	if err != nil {
		return nil, err
	}
	active := make([]localipc.Endpoint, 0, len(candidates))
	for _, candidate := range candidates {
		running, probeErr := localipc.EndpointActive(candidate)
		if probeErr != nil {
			return nil, fmt.Errorf("inspect previous session-owner endpoint: %w", probeErr)
		}
		if running {
			active = append(active, candidate)
		}
	}
	return active, nil
}

func (app *runtime) activeDaemonEndpoints(configPath string) ([]localipc.Endpoint, error) {
	current, err := app.endpoint(configPath)
	if err != nil {
		return nil, err
	}
	active := make([]localipc.Endpoint, 0, 2)
	currentActive, err := localipc.EndpointActive(current)
	if err != nil {
		return nil, fmt.Errorf("inspect canonical session-owner endpoint: %w", err)
	}
	if currentActive {
		active = append(active, current)
	}
	previous, err := app.activePreviousEndpoints(configPath)
	if err != nil {
		return nil, err
	}
	return append(active, previous...), nil
}

func splitSessionOwnersError() error {
	return errors.New(
		"multiple session owners use different Unix runtime locations; Corresync " +
			"refuses to guess which credential is authoritative; run `corr daemon " +
			"stop` once to stop every protected same-user owner, then retry",
	)
}

func (app *runtime) daemonControlEndpoint(configPath string) (localipc.Endpoint, error) {
	current, err := app.endpoint(configPath)
	if err != nil {
		return localipc.Endpoint{}, err
	}
	previous, err := app.activePreviousEndpoints(configPath)
	if err != nil {
		return localipc.Endpoint{}, err
	}
	if len(previous) == 0 {
		return current, nil
	}
	currentActive, err := localipc.EndpointActive(current)
	if err != nil {
		return localipc.Endpoint{}, fmt.Errorf("inspect canonical session-owner endpoint: %w", err)
	}
	if currentActive || len(previous) != 1 {
		return localipc.Endpoint{}, splitSessionOwnersError()
	}
	return previous[0], nil
}

func (app *runtime) migratePreviousDaemon(
	ctx context.Context,
	configPath,
	configDigest string,
) (returnErr error) {
	previous, err := app.activePreviousEndpoints(configPath)
	if err != nil {
		return err
	}
	if len(previous) == 0 {
		return nil
	}
	current, err := app.endpoint(configPath)
	if err != nil {
		return err
	}
	currentActive, err := localipc.EndpointActive(current)
	if err != nil {
		return fmt.Errorf("inspect canonical session-owner endpoint: %w", err)
	}
	if currentActive || len(previous) != 1 {
		return splitSessionOwnersError()
	}

	client, err := daemonapi.NewClient(previous[0])
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	probeContext, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	owner, inspectErr := client.InspectOwner(probeContext, app.caller())
	cancel()
	if owner.Status().ProcessID < 1 {
		if inspectErr == nil {
			inspectErr = errors.New("previous session owner returned no process metadata")
		}
		return fmt.Errorf(
			"authenticate the previous session owner before runtime migration: %w",
			inspectErr,
		)
	}
	var versionErr *daemonapi.ProtocolVersionError
	if inspectErr != nil && !errors.As(inspectErr, &versionErr) {
		return fmt.Errorf(
			"authenticate the previous session owner before runtime migration: %w",
			inspectErr,
		)
	}
	if err := app.validateDaemonConfig(owner.Status(), configDigest); err != nil {
		return err
	}

	shutdownContext, cancel := context.WithTimeout(ctx, daemonControlTimeout)
	shutdownErr := client.ShutdownOwner(shutdownContext, app.caller(), owner)
	cancel()
	if shutdownErr != nil {
		return fmt.Errorf("stop previous session owner before runtime migration: %w", shutdownErr)
	}
	status, running, waitErr := waitForDaemonExit(
		ctx,
		app,
		client,
		owner.Status(),
		daemonReplacementTimeout,
	)
	if waitErr != nil {
		return fmt.Errorf("wait for previous session owner to stop: %w", waitErr)
	}
	if running {
		return fmt.Errorf(
			"previous session owner changed to PID %d during runtime migration",
			status.ProcessID,
		)
	}
	return nil
}
