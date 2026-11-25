package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/googleoauthclient"
)

type authCommand struct {
	Login        loginCommand        `cmd:"" help:"Sign in to an account."`
	Status       authStatusCommand   `cmd:"" help:"Show which accounts are signed in."`
	Logout       authLogoutCommand   `cmd:"" help:"Sign out of one account, or of all accounts."`
	GoogleClient googleClientCommand `cmd:"" help:"Store your own Google desktop OAuth client for Google sign-in."`
}

func (*authCommand) Help() string {
	return `The local daemon holds your sign-ins while it runs. corr auth logout ends
them; the browser data an Outlook Web sign-in saves stays until you add
--clear-browser.

Examples:
  corr auth login --account work
  corr auth status
  corr auth logout --account work`
}

type googleClientCommand struct {
	Import googleClientImportCommand `cmd:"" help:"Import a downloaded desktop client into the system keyring."`
}

type googleClientImportCommand struct {
	File    string `arg:"" type:"path" help:"Google desktop OAuth client JSON file you downloaded."`
	Key     string `required:"" help:"Name to store the client under in the system keyring; the account's settings refer to it."`
	Replace bool   `help:"Replace the value already stored under this keyring name."`
	JSON    bool   `help:"Write machine-readable JSON without the client secret."`

	store        func(string, []byte) error
	replaceStore func(string, []byte) error
}

type googleClientImportResult struct {
	Imported      bool   `json:"imported"`
	ClientID      string `json:"clientId"`
	CredentialKey string `json:"credentialKey"`
	RedirectURI   string `json:"redirectUri"`
}

func (command *googleClientImportCommand) Run(app *runtime) error {
	client, err := googleoauthclient.ParseFile(command.File)
	if err != nil {
		return err
	}
	defer client.Close()
	store := command.store
	if command.Replace {
		store = command.replaceStore
		if store == nil {
			store = credential.ReplaceOSKeyring
		}
	} else if store == nil {
		store = credential.StoreOSKeyring
	}
	if err := store(command.Key, client.Secret); err != nil {
		return fmt.Errorf("save the Google client in the system keyring: %w", err)
	}
	result := googleClientImportResult{
		Imported: true, ClientID: client.ID, CredentialKey: command.Key,
		RedirectURI: "http://127.0.0.1:0",
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-14s %s\n  %-14s %s\n  %-14s %s\n\n   %s\n",
		view.success(),
		view.strong("Google desktop OAuth client saved"),
		"Client ID", sanitizeCell(result.ClientID, 512),
		"Keyring name", sanitizeCell(result.CredentialKey, 256),
		"Redirect", result.RedirectURI,
		view.muted("The client secret is in the system keyring; it is not written to the Corresync configuration."),
	)
	return err
}

type authStatusCommand struct {
	Account string `help:"Show only this account (alias)."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type authStatusReport struct {
	DaemonVersion string              `json:"daemonVersion"`
	ProcessID     int                 `json:"processId"`
	Accounts      []sessionStatusView `json:"accounts"`
}

type sessionStatusView struct {
	Account           string                                    `json:"account"`
	Alias             string                                    `json:"alias"`
	Provider          string                                    `json:"provider"`
	MailProvider      string                                    `json:"mailProvider,omitempty"`
	CalendarProvider  string                                    `json:"calendarProvider,omitempty"`
	TaskProvider      string                                    `json:"taskProvider,omitempty"`
	MessagingProvider string                                    `json:"messagingProvider,omitempty"`
	State             string                                    `json:"state"`
	Authenticated     bool                                      `json:"authenticated"`
	Services          application.ServiceAuthenticationStatuses `json:"services"`
	CapturedAt        string                                    `json:"capturedAt,omitempty"`
	Capabilities      *domain.Capabilities                      `json:"capabilities,omitempty"`
	Degradations      []domain.Degradation                      `json:"degradations,omitempty"`
	PendingSignIn     *pendingSignInView                        `json:"pendingSignIn,omitempty"`
}

// pendingSignInView is an unfinished `corr auth login` for the account: how
// it is shown and when the local daemon ends it. Nothing else about the
// sign-in or its page is reported.
type pendingSignInView struct {
	Kind      daemonapi.PendingSignInKind `json:"kind"`
	ExpiresAt string                      `json:"expiresAt"`
}

func (command *authStatusCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	var selectedAccount string
	if command.Account != "" {
		accountID, accountErr := app.account(configuration, command.Account)
		if accountErr != nil {
			return accountErr
		}
		selectedAccount = string(accountID)
	}
	client, daemonStatus, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	result, err := client.SessionStatusWithSignIns(app.context, app.caller())
	if err != nil {
		return err
	}
	report := newAuthStatusReport(daemonStatus, result, selectedAccount)
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	return writeAuthStatus(app, report, time.Now())
}

func newAuthStatusReport(
	daemonStatus daemonapi.Status,
	result daemonapi.SessionStatusResult,
	selectedAccount string,
) authStatusReport {
	report := authStatusReport{
		DaemonVersion: daemonStatus.Version,
		ProcessID:     daemonStatus.ProcessID,
		Accounts:      make([]sessionStatusView, 0, len(result.Accounts)),
	}
	for _, account := range result.Accounts {
		if selectedAccount != "" && string(account.Account) != selectedAccount {
			continue
		}
		item := sessionStatusView{
			Account:           string(account.Account),
			Alias:             account.Alias,
			Provider:          string(account.Provider),
			MailProvider:      string(account.MailProvider),
			CalendarProvider:  string(account.CalendarProvider),
			TaskProvider:      string(account.TaskProvider),
			MessagingProvider: string(account.MessagingProvider),
			State:             account.State,
			Authenticated:     account.Authenticated,
			Services:          account.Services,
			Capabilities:      account.Capabilities,
			Degradations:      account.Degradations,
		}
		if account.CapturedAt != nil {
			item.CapturedAt = account.CapturedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		if pending, exists := result.PendingSignIn(account.Account); exists {
			item.PendingSignIn = &pendingSignInView{
				Kind:      pending.Kind,
				ExpiresAt: pending.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
		report.Accounts = append(report.Accounts, item)
	}
	return report
}

func writeAuthStatus(app *runtime, report authStatusReport, now time.Time) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	width := app.textWidth(app.stdout)
	if _, err := view.printf(
		"%s  %s  %s\n",
		view.info(),
		view.strong("Sign-in status"),
		view.muted(fmt.Sprintf("local daemon %s · PID %d", report.DaemonVersion, report.ProcessID)),
	); err != nil {
		return err
	}
	for _, account := range report.Accounts {
		detail := strings.ReplaceAll(account.State, "_", " ")
		icon := view.muted("–")
		switch account.State {
		case "authenticated":
			icon = view.success()
			detail = "signed in at " + account.CapturedAt
		case "pending":
			icon = view.warning()
			detail = "sign-in in progress"
		}
		pending := ""
		if account.PendingSignIn != nil {
			pending = pendingSignInLabel(*account.PendingSignIn, now)
			if account.State != "authenticated" {
				icon, detail, pending = view.warning(), pending, ""
			}
		}
		if _, err := view.printf(
			"   %s  %s %s\n",
			icon,
			view.strong(fmt.Sprintf("%-16s", account.Alias)),
			view.muted(detail),
		); err != nil {
			return err
		}
		if pending != "" {
			if _, err := view.printf("      %s  %s\n", view.warning(), pending); err != nil {
				return err
			}
		}
		if account.PendingSignIn != nil {
			if _, err := view.printf(
				"%s\n         %s\n",
				wrapText("Finish it where it was started, or start again:", width, "         "),
				view.nextStep(shellCommand("corr", "auth", "login", "--account", account.Alias)),
			); err != nil {
				return err
			}
		}
		for _, service := range account.Services.Values() {
			serviceIcon := view.muted("–")
			switch service.State {
			case application.AuthenticationStateAuthenticated:
				serviceIcon = view.success()
			case application.AuthenticationStatePending,
				application.AuthenticationStateReauthenticationNeeded:
				serviceIcon = view.warning()
			case application.AuthenticationStateSignedOut:
			}
			if _, err := view.printf(
				"      %s  %-8s %s · %s\n",
				serviceIcon,
				service.Service,
				service.Provider,
				serviceAuthenticationLabel(service.State),
			); err != nil {
				return err
			}
			if service.Action != nil &&
				service.State != application.AuthenticationStatePending {
				if _, err := view.printf(
					"         %s\n",
					view.nextStep(shellCommand(append(
						[]string{service.Action.NextAction.Command.Executable},
						service.Action.NextAction.Command.Args...,
					)...)),
				); err != nil {
					return err
				}
			}
		}
		for _, degradation := range account.Degradations {
			if _, err := view.printf(
				"      %s  %s: %s\n",
				view.warning(),
				sanitizeCell(degradation.Feature, 96),
				sanitizeCell(degradation.Reason, 512),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// pendingSignInLabel reads, for example, "Sign-in in progress (forwarded
// login view), expires in 3 min".
func pendingSignInLabel(pending pendingSignInView, now time.Time) string {
	line := "Sign-in in progress (" + pendingSignInKindLabel(pending.Kind) + ")"
	if expiresAt, err := time.Parse(time.RFC3339, pending.ExpiresAt); err == nil {
		line += ", " + expiryLabel(expiresAt.Sub(now))
	}
	return line
}

func pendingSignInKindLabel(kind daemonapi.PendingSignInKind) string {
	switch kind {
	case daemonapi.PendingSignInLoginView:
		return "forwarded login view"
	case daemonapi.PendingSignInTextRelay:
		return "text relay in a terminal"
	default:
		return string(kind)
	}
}

// expiryLabel says when an unfinished sign-in ends, rounded up to minutes.
func expiryLabel(remaining time.Duration) string {
	switch {
	case remaining <= 0:
		return "expiring now"
	case remaining < time.Minute:
		return "expires in less than a minute"
	default:
		minutes := int((remaining + time.Minute - 1) / time.Minute)
		return fmt.Sprintf("expires in %d min", minutes)
	}
}

func serviceAuthenticationLabel(state application.AuthenticationState) string {
	switch state {
	case application.AuthenticationStateAuthenticated:
		return "signed in"
	case application.AuthenticationStatePending:
		return "sign-in in progress"
	case application.AuthenticationStateSignedOut:
		return "signed out"
	case application.AuthenticationStateReauthenticationNeeded:
		return "needs to sign in again"
	default:
		return strings.ReplaceAll(string(state), "_", " ")
	}
}

type authLogoutCommand struct {
	ClearBrowser bool   `help:"Also delete the browser data this account saved when it signed in; needs --account. Single sign-on from your system or provider may still sign you in."`
	Account      string `help:"Sign out of only this account (alias or ID)."`
	JSON         bool   `help:"Write machine-readable JSON."`
}

func (command *authLogoutCommand) Run(app *runtime) (returnErr error) {
	if command.ClearBrowser && command.Account == "" {
		return withHint(
			errors.New("--clear-browser needs --account, because it deletes one account's browser data"),
			shellCommand("corr", "auth", "logout", "--account", "ALIAS", "--clear-browser"),
		)
	}
	if command.Account != "" {
		configuration, _, err := app.loadConfig()
		if err != nil {
			return err
		}
		alias, account, err := configuration.ResolveAccount(command.Account)
		if err != nil {
			return err
		}
		client, _, err := app.openDaemon(app.context)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
		if _, err := client.LogoutWithOptions(app.context, daemonapi.LogoutInput{Account: account.ID, ClearBrowser: command.ClearBrowser}, app.caller()); err != nil {
			return describeDaemonFailure("sign out of "+alias, err)
		}
		report := struct {
			BrowserSignInCleared bool   `json:"browserSignInCleared"`
			LoggedOut            bool   `json:"loggedOut"`
			Scope                string `json:"scope"`
			Account              string `json:"account"`
			Alias                string `json:"alias"`
		}{
			LoggedOut: true, BrowserSignInCleared: command.ClearBrowser,
			Scope:   "account",
			Account: string(account.ID),
			Alias:   alias,
		}
		if command.JSON {
			return writeJSON(app.stdout, report)
		}
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		width := app.textWidth(app.stdout)
		if command.ClearBrowser {
			_, err = view.printf(
				"%s  %s\n%s\n",
				view.success(),
				view.strong("Signed out of "+sanitizeCell(alias, 64)),
				view.muted(wrapText(
					"Deleted the browser data this account saved when it signed in. Single sign-on from your system or provider may still sign you in. Other accounts are unchanged.",
					width,
					"   ",
				)),
			)
			return err
		}
		_, err = view.printf(
			"%s  %s\n%s\n   %s\n",
			view.success(),
			view.strong("Signed out of "+sanitizeCell(alias, 64)),
			view.muted(wrapText(
				"The browser data saved when it signed in stays on this computer. To delete it:",
				width,
				"   ",
			)),
			view.nextStep(shellCommand("corr", "auth", "logout", "--account", alias, "--clear-browser")),
		)
		return err
	}

	client, status, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	if err := client.Shutdown(app.context, app.caller()); err != nil {
		return describeDaemonFailure("sign out of all accounts", err)
	}
	_, running, err := waitForDaemonExit(
		app.context,
		app,
		client,
		status,
		daemonShutdownTimeout,
	)
	if err != nil {
		return withHint(
			fmt.Errorf("the local daemon did not confirm that it stopped after signing out: %w", err),
			shellCommand("corr", "daemon", "status"),
		)
	}
	if running {
		return withHint(
			errors.New("the local daemon started again before sign-out finished; another corr command may have started it"),
			shellCommand("corr", "auth", "logout"),
		)
	}
	report := struct {
		LoggedOut bool   `json:"loggedOut"`
		Scope     string `json:"scope"`
	}{LoggedOut: true, Scope: "all"}
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n%s\n",
		view.success(),
		view.strong("Signed out of all accounts"),
		view.muted(wrapText(
			"The local daemon stopped and closed its browsers. The browser data they saved stays on this computer.",
			app.textWidth(app.stdout),
			"   ",
		)),
	)
	return err
}
