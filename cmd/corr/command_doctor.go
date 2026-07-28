package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

type doctorCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	Online  bool   `help:"Interactively validate the live mail and calendar contracts."`
	JSON    bool   `help:"Write a content-free machine-readable report."`
}

type doctorReport struct {
	Healthy bool          `json:"healthy"`
	Online  bool          `json:"online"`
	Version string        `json:"version"`
	Account string        `json:"account,omitempty"`
	Checks  []doctorCheck `json:"checks"`
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func (command *doctorCommand) Run(app *runtime) error {
	report := doctorReport{
		Healthy: true,
		Online:  command.Online,
		Version: app.info.Version,
		Checks:  make([]doctorCheck, 0, 11),
	}

	configuration, configPath, err := app.loadConfig()
	if err != nil {
		report.add("config", "fail", doctorError(err))
		return command.finish(app, report)
	}
	report.add("config", "pass", "strict secret-free configuration is valid")

	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		report.add("account", "fail", doctorError(err))
		return command.finish(app, report)
	}
	_, configured, exists := configuration.AccountByID(accountID)
	if !exists {
		report.add("account", "fail", "configured account route disappeared")
		return command.finish(app, report)
	}
	report.Account = string(accountID)
	report.add("account", "pass", "configured account identity and provider routes are valid")
	command.addUpdateStatus(app, configuration, &report)

	if hasOutlookRoute(configured) {
		executable, err := browser.ResolveExecutable(configuration.Browser.Executable)
		if err != nil {
			report.add("browser", "fail", doctorError(err))
		} else {
			report.add("browser", "pass", "resolved "+sanitizeCell(filepath.Base(executable), 80))
		}
	} else {
		report.add("browser", "skip", "not required by the selected provider routes")
	}

	endpoint, err := app.endpoint(configPath)
	if err != nil || endpoint.ID == "" || endpoint.Address == "" || endpoint.CredentialPath == "" {
		if err == nil {
			err = errors.New("local IPC endpoint is incomplete")
		}
		report.add("local_ipc", "fail", doctorError(err))
	} else {
		report.add("local_ipc", "pass", "config-scoped no-TCP endpoint is available")
		command.addDaemonStatus(app, configPath, &report)
	}

	if !command.Online {
		report.add(
			"live_provider_routes",
			"skip",
			"run with --online to authenticate and validate the selected routes",
		)
		return command.finish(app, report)
	}
	if !report.Healthy {
		report.add("live_provider_routes", "skip", "local prerequisites failed")
		return command.finish(app, report)
	}

	client, status, err := app.openDaemon(app.context)
	if err != nil {
		report.set("daemon", "fail", doctorError(err))
		report.add("live_provider_routes", "skip", "session owner is unavailable")
		return command.finish(app, report)
	}
	report.set(
		"daemon",
		"pass",
		fmt.Sprintf("protocol %d session owner is ready", status.ProtocolVersion),
	)

	if _, err := client.Login(app.context, accountID, app.caller()); err != nil {
		report.add("session", "fail", doctorError(err))
		if configured.Mail != nil {
			report.add("folder_contract", "skip", "provider authentication was not completed")
			report.add("mail_contract", "skip", "provider authentication was not completed")
		}
		if configured.Calendar != nil {
			report.add("calendar_folder_contract", "skip", "provider authentication was not completed")
			report.add("calendar_contract", "skip", "provider authentication was not completed")
		}
		closeErr := client.Close()
		if closeErr != nil {
			report.add("daemon_close", "fail", doctorError(closeErr))
		}
		return command.finish(app, report)
	}
	report.add("session", "pass", "selected provider routes authenticated in daemon memory")

	if configured.Mail == nil {
		report.add("folder_contract", "skip", "the account has no mail route")
		report.add("mail_contract", "skip", "the account has no mail route")
	} else {
		_, folderErr := client.ListMailFolders(app.context, application.MailFolderListInput{
			Account: accountID,
			Parent: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "msgfolderroot",
			},
			Traversal: application.MailFolderTraversalDeep,
			Limit:     1,
			TimeZone:  "UTC",
		}, app.caller())
		if folderErr != nil {
			report.add("folder_contract", "fail", doctorError(folderErr))
		} else {
			report.add("folder_contract", "pass", "metadata response accepted; no folder data emitted")
		}

		_, mailErr := client.ListMail(app.context, application.MailListInput{
			Account: accountID,
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "inbox",
			},
			Limit:    1,
			TimeZone: "UTC",
		}, app.caller())
		if mailErr != nil {
			report.add("mail_contract", "fail", doctorError(mailErr))
		} else {
			report.add("mail_contract", "pass", "metadata response accepted; no message data emitted")
		}
	}

	if configured.Calendar == nil {
		report.add("calendar_folder_contract", "skip", "the account has no calendar route")
		report.add("calendar_contract", "skip", "the account has no calendar route")
	} else {
		_, folderErr := client.ListCalendarFolders(
			app.context,
			application.CalendarFolderListInput{
				Account: accountID,
				Limit:   1,
			},
			app.caller(),
		)
		if folderErr != nil {
			report.add("calendar_folder_contract", "fail", doctorError(folderErr))
		} else {
			report.add(
				"calendar_folder_contract",
				"pass",
				"metadata response accepted; no calendar data emitted",
			)
		}

		start := time.Now().UTC().Truncate(time.Second)
		_, calendarErr := client.ListCalendar(app.context, application.CalendarListInput{
			Account: accountID,
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Start: start.Format(time.RFC3339),
			End:   start.Add(time.Hour).Format(time.RFC3339),
		}, app.caller())
		if calendarErr != nil {
			report.add("calendar_contract", "fail", doctorError(calendarErr))
		} else {
			report.add("calendar_contract", "pass", "metadata response accepted; no event data emitted")
		}
	}
	if err := client.Close(); err != nil {
		report.add("daemon_close", "fail", doctorError(err))
	}
	return command.finish(app, report)
}

func (command *doctorCommand) addDaemonStatus(
	app *runtime,
	configPath string,
	report *doctorReport,
) {
	endpoint, err := app.endpoint(configPath)
	if err != nil {
		report.add("daemon", "fail", doctorError(err))
		return
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		report.add("daemon", "fail", doctorError(err))
		return
	}
	defer func() {
		if err := client.Close(); err != nil {
			report.add("daemon_close", "fail", doctorError(err))
		}
	}()
	ctx, cancel := context.WithTimeout(app.context, daemonProbeTimeout)
	defer cancel()
	owner, statusErr := client.InspectOwner(ctx, app.caller())
	status := owner.Status()
	if status.ProcessID > 0 {
		digest, fingerprintErr := config.Fingerprint(configPath)
		if fingerprintErr == nil {
			fingerprintErr = app.validateDaemonConfig(status, digest)
		}
		if fingerprintErr != nil {
			report.add("daemon", "fail", doctorError(fingerprintErr))
			return
		}
	}
	if statusErr == nil {
		if status.ProtocolVersion != daemonapi.ProtocolVersion ||
			status.Version != app.info.Version {
			report.add(
				"daemon",
				"fail",
				fmt.Sprintf(
					"running version %s uses protocol %d; run `corr daemon start` to replace it",
					status.Version,
					status.ProtocolVersion,
				),
			)
			return
		}
		report.add(
			"daemon",
			"pass",
			fmt.Sprintf("version %s protocol %d is ready", status.Version, status.ProtocolVersion),
		)
		return
	}
	var versionErr *daemonapi.ProtocolVersionError
	if errors.As(statusErr, &versionErr) && status.ProcessID > 0 {
		report.add(
			"daemon",
			"fail",
			fmt.Sprintf(
				"running version %s uses protocol %d; run `corr daemon start` to replace it",
				status.Version,
				status.ProtocolVersion,
			),
		)
		return
	}
	if errors.Is(statusErr, os.ErrNotExist) {
		report.add("daemon", "skip", "not running; it will start on the first provider command")
		return
	}
	report.add("daemon", "fail", doctorError(statusErr))
}

func (command *doctorCommand) addUpdateStatus(app *runtime, configuration config.Config, report *doctorReport) {
	if !app.automaticUpdateChecksEnabled(app.context, &configuration) {
		report.add("update", "skip", "automatic stable-release checks are disabled")
		return
	}
	ctx, cancel := context.WithTimeout(app.context, 750*time.Millisecond)
	defer cancel()
	update, err := app.updateReport(ctx)
	if err != nil {
		report.add("update", "skip", "stable-release status is temporarily unavailable")
		return
	}
	switch update.Status {
	case updatecheck.StatusAvailable:
		report.add("update", "pass", fmt.Sprintf("%s is available; %s", update.LatestVersion, update.Upgrade))
	case updatecheck.StatusCurrent:
		report.add("update", "pass", update.LatestVersion+" is the latest stable release")
	case updatecheck.StatusDevelopment:
		report.add("update", "skip", "development build; stable-release comparison skipped")
	case updatecheck.StatusUnavailable:
		report.add("update", "skip", "stable-release status is temporarily unavailable")
	}
}

func (report *doctorReport) add(name, status, detail string) {
	report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Detail: detail})
	if status == "fail" {
		report.Healthy = false
	}
}

func (report *doctorReport) set(name, status, detail string) {
	for index := range report.Checks {
		if report.Checks[index].Name == name {
			report.Checks[index] = doctorCheck{Name: name, Status: status, Detail: detail}
			if status == "fail" {
				report.Healthy = false
			}
			return
		}
	}
	report.add(name, status, detail)
}

func (command *doctorCommand) finish(app *runtime, report doctorReport) error {
	var writeErr error
	if command.JSON {
		writeErr = writeJSON(app.stdout, report)
	} else {
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		state := "Healthy"
		icon := view.success()
		if !report.Healthy {
			state = "Needs attention"
			icon = view.failure()
		}
		_, writeErr = view.printf("%s  %s\n", icon, view.strong("Corresync · "+state))
		for _, check := range report.Checks {
			if _, err := view.printf(
				"   %s  %s %s\n",
				view.status(check.Status),
				view.strong(fmt.Sprintf("%-16s", sanitizeCell(check.Name, 40))),
				sanitizeCell(check.Detail, 240),
			); err != nil {
				writeErr = errors.Join(writeErr, err)
			}
		}
	}
	if !report.Healthy {
		return errors.Join(writeErr, errors.New("doctor found one or more failing checks"))
	}
	return writeErr
}

func doctorError(err error) string {
	if err == nil {
		return "unknown failure"
	}
	return sanitizeCell(err.Error(), 240)
}
