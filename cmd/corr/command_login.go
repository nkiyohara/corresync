package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/browser"
)

type loginCommand struct {
	Account  string `help:"Configured account alias; defaults to default_account."`
	JSON     bool   `help:"Write machine-readable JSON."`
	Web      bool   `help:"Show the Outlook Web sign-in page through a temporary loopback login view that you open over SSH port forwarding. Used automatically when this host has no display."`
	Host     string `help:"SSH host whose account to sign in; run this on your own computer to open the login view in its browser."`
	WebPort  int    `help:"Local loopback port for the temporary login view; defaults to an available port."`
	WebStdio bool   `hidden:""`
	Terminal bool   `help:"Experimentally complete browser sign-in through a text-only terminal relay."`
}

func (command *loginCommand) Run(app *runtime) (returnErr error) {
	if command.Terminal && command.JSON {
		return errors.New("--terminal and --json cannot be used together")
	}
	modes := 0
	for _, enabled := range []bool{command.Terminal, command.Web, command.WebStdio, command.Host != ""} {
		if enabled {
			modes++
		}
	}
	if modes > 1 || modes > 0 && command.JSON || command.WebPort != 0 && !command.Web && command.Host == "" {
		return errors.New("choose only one login mode; --web-port requires --web or --host, and graphical/terminal modes cannot use --json")
	}
	if command.Host != "" {
		return runSSHGraphicalLogin(app, command.Host, command.Account, command.WebPort)
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, configured, err := configuration.ResolveAccount(command.Account)
	if err != nil {
		return err
	}
	configured = configured.ActiveServices()
	accountID := configured.ID
	if !command.Terminal && !command.Web && !command.WebStdio && hasOutlookRoute(configured) {
		if err := browser.RequireGraphicalSession(); err != nil {
			if err := command.useForwardedViewWithoutDisplay(app, alias, err); err != nil {
				return err
			}
		}
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
	if command.Web || command.WebStdio {
		return runGraphicalLogin(app, client, accountID, command.WebPort, command.WebStdio, time.Duration(configuration.Browser.LoginTimeout))
	}
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
	if err == nil && hasOutlookRoute(configured) {
		_, err = view.printf(
			"   %s\n",
			view.muted(
				"The dedicated Outlook browser maintains this account's session, with its window minimized when supported; auth logout or daemon shutdown closes it.",
			),
		)
	}
	return err
}

// useForwardedViewWithoutDisplay switches a default login to the forwarded
// login view when this host cannot show a browser window. --json has no such
// fallback because the view prints a private link instead of one JSON value.
func (command *loginCommand) useForwardedViewWithoutDisplay(app *runtime, alias string, displayErr error) error {
	if command.JSON {
		return fmt.Errorf(
			"%w; --json needs a visible browser on this host. Run `corr auth login --account %s` without --json to sign in through the forwarded login view",
			displayErr,
			shellSingleQuote(alias),
		)
	}
	if _, err := fmt.Fprintln(app.stderr, "This host has no display, so the sign-in page is shown through a forwarded login view (same as --web)."); err != nil {
		return err
	}
	command.Web = true
	return nil
}
