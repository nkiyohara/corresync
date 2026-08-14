package main

import (
	"errors"

	"github.com/nkiyohara/corresync/internal/config"
)

// setupCommand is the provider-neutral first-run path. Without an address it
// opens the shared interactive wizard; the address form stays deterministic
// for scripts. Both paths delegate route validation and persistence to the same
// account use case as `account add`.
type setupCommand struct {
	Address string `arg:"" optional:"" help:"Bare email address for deterministic setup; omit in an interactive terminal for guided setup."`
	Alias   string `help:"Local account name; defaults to the address local part."`
	Default bool   `help:"Make this the default account when others already exist."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *setupCommand) Run(app *runtime) error {
	if command.Address == "" {
		if command.JSON {
			return errors.New("guided setup has no JSON mode; pass an address for deterministic setup")
		}
		if command.Alias != "" || command.Default {
			return errors.New("--alias and --default require an address in deterministic setup")
		}
		if !app.interactiveInput() || !app.interactiveStdout() {
			return errors.New(
				"guided setup requires an interactive terminal; use `corr setup ADDRESS` in scripts",
			)
		}
	}
	created, err := ensureSetupConfig(app, command.JSON)
	if err != nil {
		return err
	}
	if command.Address == "" {
		restoreInput := prepareAccessibleSettingsInput(app)
		defer restoreInput()
		return runGuidedAccountSetup(app, created)
	}
	return (&accountAddCommand{
		Address: command.Address,
		Alias:   command.Alias,
		Default: command.Default,
		JSON:    command.JSON,
	}).Run(app)
}

func ensureSetupConfig(app *runtime, jsonOutput bool) (bool, error) {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return false, err
	}
	created, err := config.Create(app.context, path, config.Default())
	if err != nil {
		return false, err
	}
	if !created {
		if _, err := config.Load(path); err != nil {
			return false, err
		}
	}

	if created && !jsonOutput {
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		if _, err := view.printf(
			"%s  %s\n   %s\n\n",
			view.success(),
			view.strong("Provider-neutral configuration created"),
			view.muted(path),
		); err != nil {
			return false, err
		}
	}
	return created, nil
}

func prepareAccessibleSettingsInput(app *runtime) func() {
	if !settingsAccessible(app) {
		return func() {}
	}
	originalInput := app.stdin
	app.stdin = newSettingsAccessibleReader(app.context, originalInput)
	return func() { app.stdin = originalInput }
}
