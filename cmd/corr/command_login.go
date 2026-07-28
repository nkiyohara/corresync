package main

import (
	"errors"
	"fmt"
)

type loginCommand struct {
	Account  string `help:"Configured account alias; defaults to default_account."`
	JSON     bool   `help:"Write machine-readable JSON."`
	Terminal bool   `help:"Experimentally complete browser sign-in through a text-only terminal relay."`
}

func (command *loginCommand) Run(app *runtime) (returnErr error) {
	if command.Terminal && command.JSON {
		return errors.New("--terminal and --json cannot be used together")
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	_, configured, exists := configuration.AccountByID(accountID)
	if !exists {
		return errors.New("configured account route disappeared")
	}
	if command.Terminal {
		if _, err := interactiveTerminalInput(app); err != nil {
			return err
		}
	}
	if err := writeOAuthConsentNotice(app, configured); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	if command.Terminal {
		return runTerminalLogin(app, client, accountID)
	}
	result, err := client.Login(app.context, accountID, app.caller())
	if err != nil {
		return err
	}

	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Authenticated provider routes"),
		view.muted(fmt.Sprintf("Account %s · captured %s", accountID, result.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"))),
	)
	return err
}
