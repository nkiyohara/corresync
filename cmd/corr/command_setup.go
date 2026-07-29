package main

import (
	"github.com/nkiyohara/corresync/internal/config"
)

// setupCommand is the provider-neutral first-run path. It creates only local,
// secret-free configuration, performs credential-free discovery, and delegates
// route validation and persistence to the same account use case as `account
// add`. Authentication remains a later explicit CLI action.
type setupCommand struct {
	Address string `arg:"" help:"Bare email address to discover and configure."`
	Alias   string `help:"Local account name; defaults to the address local part."`
	Default bool   `help:"Make this the default account when others already exist."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *setupCommand) Run(app *runtime) error {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	created, err := config.Create(app.context, path, config.Default())
	if err != nil {
		return err
	}
	if !created {
		if _, err := config.Load(path); err != nil {
			return err
		}
	}

	if created && !command.JSON {
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		if _, err := view.printf(
			"%s  %s\n   %s\n\n",
			view.success(),
			view.strong("Provider-neutral configuration created"),
			view.muted(path),
		); err != nil {
			return err
		}
	}
	return (&accountAddCommand{
		Address: command.Address,
		Alias:   command.Alias,
		Default: command.Default,
		JSON:    command.JSON,
	}).Run(app)
}
