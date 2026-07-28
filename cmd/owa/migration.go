package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
	"github.com/nkiyohara/owa-bridge/internal/paths"
)

func (app *runtime) migrateLegacyState(
	ctx context.Context,
	configuration config.Config,
) error {
	legacyConfigPath, err := config.LegacyDefaultPath()
	if err != nil {
		return err
	}
	legacyState, err := paths.LegacyStateDir()
	if err != nil {
		return err
	}
	if err := app.stopLegacyDaemon(ctx, legacyConfigPath, legacyState); err != nil {
		return err
	}
	accounts := make(map[string]domain.AccountID, len(configuration.Accounts))
	for alias, account := range configuration.Accounts {
		accounts[alias] = account.ID
	}
	migrated, err := paths.MigrateLegacyState(accounts)
	if err != nil {
		return fmt.Errorf("migrate owa-bridge state: %w", err)
	}
	if migrated {
		_, err = fmt.Fprintln(
			app.stderr,
			"Migrated local state and moved browser profiles to Corresync; rollback may require sign-in.",
		)
	}
	return err
}

func (app *runtime) stopLegacyDaemon(
	ctx context.Context,
	configPath,
	stateDirectory string,
) error {
	if _, err := os.Lstat(configPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect legacy config before migration: %w", err)
	}
	if _, err := os.Lstat(stateDirectory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect legacy state before migration: %w", err)
	}
	endpoint, err := localipc.ResolveInState(configPath, stateDirectory)
	if err != nil {
		return err
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	probeContext, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	owner, inspectErr := client.InspectOwner(probeContext, app.caller())
	cancel()
	if owner.Status().ProcessID < 1 {
		// A missing, stale, or unavailable endpoint means there is no
		// authenticated daemon generation we can safely target.
		return nil
	}
	var versionErr *daemonapi.ProtocolVersionError
	if inspectErr != nil && !errors.As(inspectErr, &versionErr) {
		return fmt.Errorf("inspect legacy session owner: %w", inspectErr)
	}

	shutdownContext, cancel := context.WithTimeout(ctx, daemonControlTimeout)
	shutdownErr := client.ShutdownOwner(shutdownContext, app.caller(), owner)
	cancel()
	if shutdownErr != nil {
		return fmt.Errorf("stop legacy session owner before state migration: %w", shutdownErr)
	}

	waitContext, cancel := context.WithTimeout(ctx, daemonReplacementTimeout)
	defer cancel()
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()
	unavailable := 0
	for {
		probeContext, probeCancel := context.WithTimeout(waitContext, daemonProbeTimeout)
		next, nextErr := client.InspectOwner(probeContext, app.caller())
		probeCancel()
		if next.Status().ProcessID < 1 {
			unavailable++
			if unavailable >= daemonUnavailableConfirmations {
				return nil
			}
		} else {
			unavailable = 0
			if nextErr == nil && daemonChanged(owner.Status(), next.Status()) {
				return errors.New(
					"a new legacy session owner started during migration; run `owa daemon stop` and retry",
				)
			}
		}
		select {
		case <-waitContext.Done():
			return fmt.Errorf(
				"legacy session owner PID %d did not stop: %w",
				owner.Status().ProcessID,
				waitContext.Err(),
			)
		case <-ticker.C:
		}
	}
}
