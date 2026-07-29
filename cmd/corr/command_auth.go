package main

import (
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/domain"
)

type authCommand struct {
	Login  loginCommand      `cmd:"" help:"Explicitly authenticate one configured provider route."`
	Status authStatusCommand `cmd:"" help:"Inspect content-free session state."`
	Logout authLogoutCommand `cmd:"" help:"Close one account session or all local sessions."`
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
	Account          string               `json:"account"`
	Alias            string               `json:"alias"`
	Provider         string               `json:"provider"`
	MailProvider     string               `json:"mailProvider,omitempty"`
	CalendarProvider string               `json:"calendarProvider,omitempty"`
	State            string               `json:"state"`
	Authenticated    bool                 `json:"authenticated"`
	CapturedAt       string               `json:"capturedAt,omitempty"`
	Capabilities     *domain.Capabilities `json:"capabilities,omitempty"`
	Degradations     []domain.Degradation `json:"degradations,omitempty"`
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
			Account:          string(account.Account),
			Alias:            account.Alias,
			Provider:         string(account.Provider),
			MailProvider:     string(account.MailProvider),
			CalendarProvider: string(account.CalendarProvider),
			State:            account.State,
			Authenticated:    account.Authenticated,
			Capabilities:     account.Capabilities,
			Degradations:     account.Degradations,
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
