package main

import (
	"errors"

	"github.com/nkiyohara/corresync/internal/config"
)

// setupCommand is the first-run path. Without an address it opens the shared
// interactive wizard; with an address it adds one account without prompting,
// for scripts and AI agents. Both paths delegate route validation and
// persistence to the same account use case as `account add`.
type setupCommand struct {
	Address string `arg:"" optional:"" help:"Email address to add without questions, for scripts. Leave it out to be guided step by step."`
	Alias   string `help:"Name for the account; defaults to the part of the address before @."`
	Default bool   `help:"Make this the default account when others already exist."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *setupCommand) Help() string {
	return `Without an address, setup asks for your email address, finds how to connect, and adds the account. It then offers tab completion and AI agent connections, and ends with the command that signs you in. Run it again at any time to add or repair an account.

With an address, setup adds that one account without asking anything and does not sign in. Use ` + "`corr account add ADDRESS --help`" + ` to choose the connection yourself.`
}

func (command *setupCommand) Run(app *runtime) error {
	if command.Address == "" {
		if command.JSON {
			return withHint(
				errors.New("guided setup has no JSON mode; pass an address to add one account without prompts"),
				"corr setup you@example.com --json",
			)
		}
		if command.Alias != "" || command.Default {
			return withHint(
				errors.New("--alias and --default need an email address"),
				"corr setup you@example.com --alias work",
			)
		}
		if !app.interactiveInput() || !app.interactiveStdout() {
			return withHint(
				errors.New("guided setup needs an interactive terminal"),
				"corr setup you@example.com",
			)
		}
	}
	created, err := ensureSetupConfig(app, command.JSON || command.Address == "")
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

// ensureSetupConfig creates an empty configuration when none exists. quiet
// suppresses the creation notice: JSON output stays one value, and guided
// setup names the file once, in its summary.
func ensureSetupConfig(app *runtime, quiet bool) (bool, error) {
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

	if created && !quiet {
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		if _, err := view.printf(
			"%s  %s\n   %s\n\n",
			view.success(),
			view.strong("Configuration created"),
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
