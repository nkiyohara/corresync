package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/browser"
)

type loginCommand struct {
	Account  string `help:"Account to sign in (alias); defaults to the default account."`
	JSON     bool   `help:"Write machine-readable JSON."`
	Web      bool   `help:"Show the Outlook Web sign-in page in a temporary login view that you open in your own browser through SSH port forwarding. Used automatically when this host has no display."`
	Host     string `help:"Sign in to an account on this SSH host. Run it on your own computer; the login view opens in your browser here."`
	WebPort  int    `help:"Local port for the login view; defaults to a free port."`
	WebStdio bool   `hidden:""`
	Terminal bool   `help:"Sign in through a text-only copy of the sign-in page in this terminal (experimental)."`
}

func (*loginCommand) Help() string {
	return `By default a browser window opens on this computer and you sign in there.
On a host without a display, such as a server you reach over SSH, the
sign-in page is shown in a login view instead: open it in the browser on your
own computer, or run corr auth login --host on your own computer to set up
the connection for you. Either way the sign-in itself happens on the
provider's own page.

Examples:
  corr auth login --account work
  corr auth login --account work --host devbox
  corr auth login --account work --terminal`
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
		return errors.New("choose only one of --web, --host, or --terminal; --web-port needs --web or --host, and none of them works with --json")
	}
	if command.Host != "" {
		err := runSSHGraphicalLogin(app, command.Host, command.Account, command.WebPort)
		if command.Account == "" {
			return err
		}
		return loginFailure(
			command.Account+" on "+command.Host,
			err,
			shellCommand("corr", "auth", "login", "--account", command.Account, "--host", command.Host),
		)
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
	if command.WebStdio {
		// The SSH helper's errors go back to the computer that ran --host.
		return runGraphicalLogin(app, client, accountID, command.WebPort, true, time.Duration(configuration.Browser.LoginTimeout))
	}
	if command.Web {
		err := runGraphicalLogin(app, client, accountID, command.WebPort, false, time.Duration(configuration.Browser.LoginTimeout))
		return loginFailure(alias, err, localLoginHint(alias, err))
	}
	if command.Terminal {
		err := runTerminalLogin(app, client, accountID)
		return loginFailure(alias, err, localLoginHint(alias, err))
	}
	result, err := client.Login(app.context, accountID, app.caller())
	if err != nil {
		return loginFailure(alias, err, localLoginHint(alias, err))
	}

	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	width := app.textWidth(app.stdout)
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Signed in to "+sanitizeCell(alias, 64)),
		view.muted(fmt.Sprintf("Account %s · signed in at %s", accountID, result.CapturedAt.UTC().Format("2006-01-02T15:04:05Z"))),
	)
	if err == nil && hasOutlookRoute(configured) {
		_, err = view.printf(
			"%s\n",
			view.muted(wrapText(
				"A separate Outlook browser keeps this account signed in, minimized where the system allows. corr auth logout or stopping the local daemon closes it.",
				width,
				"   ",
			)),
		)
	}
	if err == nil {
		_, err = view.printf("%s\n", view.nextStep(shellCommand("corr", "auth", "status", "--account", alias)))
	}
	return err
}

// localLoginHint is the next step after a sign-in on this host failed: try
// again after a timeout, otherwise let doctor check the browser and route.
func localLoginHint(alias string, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return shellCommand("corr", "auth", "login", "--account", alias)
	}
	return shellCommand("corr", "doctor", "--account", alias)
}

// loginFailure names the account in a failed sign-in and says what to try.
// The cause is kept: the local daemon's errors carry no mail content or
// sign-in secrets. Ctrl-C stays as it is, and a hint already attached to err
// wins.
func loginFailure(account string, err error, hint string) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	wrapped := fmt.Errorf("sign in to %s: %w", account, err)
	if existing, hinted := errorHint(err); hinted {
		hint = existing
	}
	return withHint(wrapped, hint)
}

// useForwardedViewWithoutDisplay switches a default login to the forwarded
// login view when this host cannot show a browser window. --json has no such
// fallback because the view prints a private link instead of one JSON value.
func (command *loginCommand) useForwardedViewWithoutDisplay(app *runtime, alias string, displayErr error) error {
	if command.JSON {
		return withHint(
			fmt.Errorf(
				"%w; --json needs a browser window on this host. Without --json, the sign-in page is shown in a login view you open from your own computer",
				displayErr,
			),
			shellCommand("corr", "auth", "login", "--account", alias),
		)
	}
	if _, err := fmt.Fprintln(app.stderr, wrapText(
		"This host has no display, so the sign-in page is shown in a login view that you open from your own computer (the same as --web).",
		app.textWidth(app.stderr),
		"",
	)); err != nil {
		return err
	}
	command.Web = true
	return nil
}
