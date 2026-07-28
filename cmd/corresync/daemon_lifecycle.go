package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/daemonapi"
)

func (app *runtime) replaceDaemon(
	ctx context.Context,
	client *daemonapi.Client,
	previous daemonapi.OwnerSnapshot,
	configPath string,
	configDigest string,
) (daemonapi.Status, error) {
	shutdownContext, cancel := context.WithTimeout(ctx, daemonControlTimeout)
	shutdownErr := client.ShutdownOwner(shutdownContext, app.caller(), previous)
	cancel()

	status, running, waitErr := waitForDaemonExit(
		ctx,
		app,
		client,
		previous.Status(),
		daemonReplacementTimeout,
	)
	if waitErr != nil {
		return daemonapi.Status{}, errors.Join(
			fmt.Errorf("replace incompatible session owner: %w", waitErr),
			shutdownErr,
		)
	}
	if running {
		// Another updater won the race. Its validated owner is ready to use.
		if err := app.validateDaemonStatus(status, configDigest); err != nil {
			return daemonapi.Status{}, err
		}
		return status, nil
	}

	// A failed targeted shutdown is harmless once the exact old generation is
	// gone; another updater may have stopped it first.
	if err := app.startDaemon(ctx, configPath); err != nil {
		return daemonapi.Status{}, errors.Join(
			fmt.Errorf("start replacement session owner: %w", err),
			shutdownErr,
		)
	}
	status, err := waitForDaemon(ctx, app, client, daemonStartupTimeout)
	if err != nil {
		return daemonapi.Status{}, errors.Join(err, shutdownErr)
	}
	if err := app.validateDaemonStatus(status, configDigest); err != nil {
		return daemonapi.Status{}, err
	}
	return status, nil
}

func waitForDaemonExit(
	parent context.Context,
	app *runtime,
	client *daemonapi.Client,
	previous daemonapi.Status,
	timeout time.Duration,
) (daemonapi.Status, bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	unavailable := 0
	var lastErr error
	for {
		owner, err := client.InspectOwner(ctx, app.caller())
		status := owner.Status()
		if status.ProcessID > 0 {
			if daemonChanged(previous, status) {
				return status, true, nil
			}
			unavailable = 0
			lastErr = err
		} else {
			unavailable++
			lastErr = err
			// Confirm absence twice so a transient busy or closing response
			// cannot race a replacement start against the singleton lock.
			if unavailable >= daemonUnavailableConfirmations {
				return daemonapi.Status{}, false, nil
			}
		}
		select {
		case <-ctx.Done():
			return daemonapi.Status{}, false, fmt.Errorf(
				"session owner PID %d did not stop: %w",
				previous.ProcessID,
				errors.Join(ctx.Err(), lastErr),
			)
		case <-ticker.C:
		}
	}
}

func daemonChanged(previous, current daemonapi.Status) bool {
	return current.ProcessID != previous.ProcessID ||
		!current.StartedAt.Equal(previous.StartedAt) ||
		current.Version != previous.Version ||
		current.ProtocolVersion != previous.ProtocolVersion
}

func (app *runtime) validateDaemonConfig(status daemonapi.Status, configDigest string) error {
	if status.ConfigDigest != configDigest {
		return errors.New(
			"session owner has stale configuration; run `corresync daemon stop` and retry",
		)
	}
	return nil
}

func (app *runtime) validateDaemonStatus(status daemonapi.Status, configDigest string) error {
	if err := app.validateDaemonConfig(status, configDigest); err != nil {
		return err
	}
	if status.ProtocolVersion != daemonapi.ProtocolVersion {
		return fmt.Errorf(
			"replacement session owner protocol %d differs from CLI protocol %d",
			status.ProtocolVersion,
			daemonapi.ProtocolVersion,
		)
	}
	if status.Version != app.info.Version {
		return fmt.Errorf(
			"replacement session owner version %s differs from CLI %s",
			status.Version,
			app.info.Version,
		)
	}
	return nil
}
