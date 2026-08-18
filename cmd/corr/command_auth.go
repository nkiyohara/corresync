package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/googleoauthclient"
)

type authCommand struct {
	Login        loginCommand        `cmd:"" help:"Explicitly authenticate one configured provider route."`
	Status       authStatusCommand   `cmd:"" help:"Inspect content-free session state."`
	Logout       authLogoutCommand   `cmd:"" help:"Close one account session or all local sessions."`
	GoogleClient googleClientCommand `cmd:"" help:"Securely prepare a user-owned Google Desktop OAuth client."`
}

type googleClientCommand struct {
	Import googleClientImportCommand `cmd:"" help:"Import a downloaded Desktop client into the OS keyring."`
}

type googleClientImportCommand struct {
	File    string `arg:"" type:"path" help:"Downloaded Google Desktop OAuth client JSON file."`
	Key     string `required:"" help:"OS-keyring handle to record in the account route."`
	Replace bool   `help:"Replace the existing OS-keyring value at this exact handle."`
	JSON    bool   `help:"Write machine-readable non-secret metadata."`

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
		return fmt.Errorf("store Google OAuth client credential: %w", err)
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
		view.strong("Google Desktop OAuth client ready"),
		"Client ID", sanitizeCell(result.ClientID, 512),
		"Keyring handle", sanitizeCell(result.CredentialKey, 256),
		"Loopback", result.RedirectURI,
		view.muted("The generated client credential is in the OS keyring and never entered Corresync configuration."),
	)
	return err
}

type authStatusCommand struct {
	Account string `help:"Show only one configured account alias."`
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
	result, err := client.SessionStatus(app.context, app.caller())
	if err != nil {
		return err
	}
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
		report.Accounts = append(report.Accounts, item)
	}
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s  %s\n",
		view.info(),
		view.strong("Provider sessions"),
		view.muted(fmt.Sprintf("daemon %s · PID %d", report.DaemonVersion, report.ProcessID)),
	); err != nil {
		return err
	}
	for _, account := range report.Accounts {
		detail := account.State
		icon := view.muted("–")
		switch account.State {
		case "authenticated":
			icon = view.success()
			detail = "authenticated · captured " + account.CapturedAt
		case "pending":
			icon = view.warning()
			detail = "interactive sign-in pending"
		}
		if _, err := view.printf(
			"   %s  %s %s\n",
			icon,
			view.strong(fmt.Sprintf("%-16s", account.Alias)),
			view.muted(detail),
		); err != nil {
			return err
		}
		for _, service := range account.Services.Values() {
			serviceIcon := view.muted("–")
			detail := strings.ReplaceAll(string(service.State), "_", " ")
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
				detail,
			); err != nil {
				return err
			}
			if service.Action != nil &&
				service.State != application.AuthenticationStatePending {
				if _, err := view.printf(
					"         %s\n",
					view.muted("Next: "+strings.Join(
						append(
							[]string{service.Action.NextAction.Command.Executable},
							service.Action.NextAction.Command.Args...,
						),
						" ",
					)),
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

type authLogoutCommand struct {
	Account string `help:"Close only this configured account alias or ID."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *authLogoutCommand) Run(app *runtime) (returnErr error) {
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
		if _, err := client.Logout(app.context, account.ID, app.caller()); err != nil {
			return fmt.Errorf("clear account session: %w", err)
		}
		report := struct {
			LoggedOut bool   `json:"loggedOut"`
			Scope     string `json:"scope"`
			Account   string `json:"account"`
			Alias     string `json:"alias"`
		}{
			LoggedOut: true,
			Scope:     "account",
			Account:   string(account.ID),
			Alias:     alias,
		}
		if command.JSON {
			return writeJSON(app.stdout, report)
		}
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		_, err = view.printf(
			"%s  %s\n   %s\n",
			view.success(),
			view.strong("Local session cleared for "+sanitizeCell(alias, 64)),
			view.muted("Other account sessions and the local session owner remain active."),
		)
		return err
	}

	client, status, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	if err := client.Shutdown(app.context, app.caller()); err != nil {
		return fmt.Errorf("clear sessions: %w", err)
	}
	_, running, err := waitForDaemonExit(
		app.context,
		app,
		client,
		status,
		daemonShutdownTimeout,
	)
	if err != nil {
		return fmt.Errorf("wait for sessions to close: %w", err)
	}
	if running {
		return errors.New("a new session owner started before logout completed")
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
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("All local sessions cleared"),
		view.muted("Dedicated browsers were closed; browser profiles remain on this device."),
	)
	return err
}
