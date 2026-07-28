package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/audit"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/eventqueue"
	"github.com/nkiyohara/corresync/internal/paths"
)

type monitorCommand struct {
	Status  monitorStatusCommand  `cmd:"" help:"Show consent boundaries and local queue health."`
	Enable  monitorEnableCommand  `cmd:"" help:"Enable one explicit mode for one account."`
	Disable monitorDisableCommand `cmd:"" help:"Disable collection and explicitly retain or purge its queue."`
}

type monitorStatusCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	AllAccounts bool   `name:"all-accounts" help:"Inspect every configured account without authenticating."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type monitorEnableCommand struct {
	Account             string        `help:"Configured account alias; defaults to default_account."`
	Mode                string        `required:"" enum:"notify,queue,agent" help:"Consent boundary to enable."`
	PollInterval        time.Duration `default:"1m" help:"Metadata poll interval (15s-1h)."`
	Debounce            time.Duration `default:"30s" help:"Minimum time between sink invocations (0s-15m)."`
	Retention           time.Duration `default:"720h" help:"Acknowledged-event and deduplication retention (1h-2160h)."`
	RateLimitHour       int           `name:"rate-limit-hour" default:"30" help:"Maximum released events per hour (1-1000)."`
	SenderDomains       []string      `name:"sender-domain" help:"Allowed lowercase sender domain; repeat as needed."`
	SubjectContains     []string      `name:"subject-contains" help:"Required subject fragment; repeat for alternatives."`
	ImportantOnly       bool          `name:"important-only" help:"Match only provider high-importance messages."`
	QuietStart          string        `help:"Quiet-hours start as HH:MM; requires quiet-end and quiet-time-zone."`
	QuietEnd            string        `help:"Quiet-hours end as HH:MM."`
	QuietTimeZone       string        `help:"IANA quiet-hours time zone."`
	NotificationFields  []string      `name:"notification-field" help:"Metadata field displayed by notify mode; repeat."`
	Runner              string        `type:"path" help:"Absolute agent runner executable; required for agent mode."`
	RunnerArguments     []string      `name:"runner-argument" help:"Literal runner argument; repeat (no shell)."`
	RunnerEgress        string        `default:"local" enum:"local,remote" help:"Declared runner egress boundary."`
	RunnerFields        []string      `name:"runner-field" help:"Metadata field released to the runner; repeat."`
	RunnerTimeout       time.Duration `default:"2m" help:"Runner timeout (1s-5m)."`
	ApproveRemoteEgress bool          `name:"approve-remote-egress" help:"Separately confirm that runner fields may leave this machine."`
	Approve             bool          `help:"Confirm this account-scoped monitoring configuration."`
	JSON                bool          `help:"Write the stable machine-readable schema."`
}

type monitorDisableCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	RetainQueue bool   `name:"retain-queue" help:"Keep the local event queue after disabling."`
	PurgeQueue  bool   `name:"purge-queue" help:"Permanently purge the local event queue after disabling."`
	Approve     bool   `help:"Confirm disabling collection and the selected queue treatment."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type eventsCommand struct {
	List        eventsListCommand        `cmd:"" help:"List bounded metadata from one local queue."`
	Acknowledge eventsAcknowledgeCommand `cmd:"" help:"Acknowledge one local queue event idempotently."`
	Purge       eventsPurgeCommand       `cmd:"" help:"Permanently purge one account's local queue."`
}

type eventsListCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	State   string `default:"all" enum:"all,pending,dispatched,acknowledged" help:"Queue state filter."`
	Offset  int    `default:"0" help:"Zero-based queue offset (0-10000)."`
	Limit   int    `default:"50" help:"Events to return (1-100)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type eventsAcknowledgeCommand struct {
	EventID string `arg:"" help:"Exact evt_ identifier returned by events list."`
	Account string `help:"Configured account alias; defaults to default_account."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type eventsPurgeCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	Approve bool   `help:"Confirm permanent deletion of this account's local event queue."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

func (command *monitorStatusCommand) Run(app *runtime) (returnErr error) {
	if command.AllAccounts && command.Account != "" {
		return errors.New("monitor status cannot combine --all-accounts and --account")
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accounts := make([]domain.AccountID, 0, len(configuration.Accounts))
	if command.AllAccounts {
		aliases := make([]string, 0, len(configuration.Accounts))
		for alias := range configuration.Accounts {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			accounts = append(accounts, configuration.Accounts[alias].ID)
		}
	} else {
		account, err := app.account(configuration, command.Account)
		if err != nil {
			return err
		}
		accounts = append(accounts, account)
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	statuses := make([]application.MonitorStatus, 0, len(accounts))
	for _, account := range accounts {
		status, err := client.MonitorStatus(app.context, account, app.caller())
		if err != nil {
			return err
		}
		statuses = append(statuses, status)
	}
	if command.JSON {
		if command.AllAccounts {
			return writeJSON(app.stdout, struct {
				Accounts []application.MonitorStatus `json:"accounts"`
			}{Accounts: statuses})
		}
		return writeJSON(app.stdout, statuses[0])
	}
	return writeMonitorStatuses(app, statuses)
}

func (command *monitorEnableCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New(
			"monitoring is disabled by default; review the account, mode, filters, fields, and egress, then pass --approve",
		)
	}
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, account, err := configuration.ResolveAccount(command.Account)
	if err != nil {
		return err
	}
	mode := domain.MonitorMode(command.Mode)
	currentMode := domain.MonitorOff
	if account.Monitor != nil {
		currentMode = account.Monitor.Mode
	}
	if !validMonitorTransition(currentMode, mode) {
		return fmt.Errorf(
			"monitor mode must advance one consent boundary at a time (current %s, requested %s); use off -> notify -> queue -> agent",
			currentMode,
			mode,
		)
	}
	if currentMode != domain.MonitorAgent && mode == domain.MonitorAgent &&
		command.RunnerEgress == "remote" {
		return errors.New(
			"enable the agent boundary with --runner-egress local first; remote egress is a separate later approval",
		)
	}
	monitor := config.NewMonitor(mode)
	monitor.PollInterval = config.Duration(command.PollInterval)
	monitor.Debounce = config.Duration(command.Debounce)
	monitor.Retention = config.Duration(command.Retention)
	monitor.RateLimitHour = command.RateLimitHour
	monitor.Filter = config.MonitorFilter{
		SenderDomains:   append([]string(nil), command.SenderDomains...),
		SubjectContains: append([]string(nil), command.SubjectContains...),
		ImportantOnly:   command.ImportantOnly,
	}
	quietValues := 0
	for _, value := range []string{command.QuietStart, command.QuietEnd, command.QuietTimeZone} {
		if value != "" {
			quietValues++
		}
	}
	if quietValues != 0 && quietValues != 3 {
		return errors.New("quiet hours require --quiet-start, --quiet-end, and --quiet-time-zone together")
	}
	if quietValues == 3 {
		monitor.QuietHours = &config.QuietHours{
			Start: command.QuietStart, End: command.QuietEnd,
			TimeZone: command.QuietTimeZone,
		}
	}
	switch mode {
	case domain.MonitorOff:
		return errors.New("use monitor disable to select off mode")
	case domain.MonitorNotify:
		if command.Runner != "" || len(command.RunnerArguments) > 0 ||
			len(command.RunnerFields) > 0 || command.ApproveRemoteEgress ||
			command.RunnerEgress != "local" {
			return errors.New("notify mode cannot configure a runner")
		}
		if len(command.NotificationFields) > 0 {
			monitor.Notification.Fields = append(
				[]string(nil),
				command.NotificationFields...,
			)
		}
	case domain.MonitorQueue:
		if len(command.NotificationFields) > 0 || command.Runner != "" ||
			len(command.RunnerArguments) > 0 || len(command.RunnerFields) > 0 ||
			command.ApproveRemoteEgress || command.RunnerEgress != "local" {
			return errors.New("queue mode has no notification or runner egress")
		}
	case domain.MonitorAgent:
		if command.Runner == "" {
			return errors.New("agent mode requires --runner with an absolute executable path")
		}
		if len(command.NotificationFields) > 0 {
			return errors.New("agent mode cannot configure notification fields")
		}
		fields := command.RunnerFields
		if len(fields) == 0 {
			fields = []string{
				"account", "event_id", "sender", "subject", "received_at", "trust",
			}
		}
		runner := config.NewRunner(
			command.Runner,
			append([]string(nil), command.RunnerArguments...),
			append([]string(nil), fields...),
			command.RunnerEgress,
			command.ApproveRemoteEgress,
		)
		runner.Timeout = config.Duration(command.RunnerTimeout)
		monitor.Runner = &runner
	}
	account.Monitor = &monitor
	if err := accountMonitorValidation(configuration, alias, account); err != nil {
		return err
	}
	if err := config.Update(app.context, path, func(latest *config.Config) error {
		latestAlias, latestAccount, err := latest.ResolveAccount(string(account.ID))
		if err != nil {
			return err
		}
		if latestAlias != alias {
			return errors.New("account alias changed while enabling monitoring; retry")
		}
		latestAccount.Monitor = &monitor
		latest.Accounts[latestAlias] = latestAccount
		return nil
	}); err != nil {
		return err
	}
	result := struct {
		Account  string             `json:"account"`
		ID       domain.AccountID   `json:"id"`
		Previous domain.MonitorMode `json:"previous"`
		Mode     domain.MonitorMode `json:"mode"`
		Egress   string             `json:"egress"`
		Fields   []string           `json:"fields"`
	}{
		Account: alias, ID: account.ID, Previous: currentMode, Mode: mode,
	}
	if monitor.Notification != nil {
		result.Egress = "local:" + monitor.Notification.Adapter
		result.Fields = monitor.Notification.Fields
	}
	if monitor.Runner != nil {
		result.Egress = monitor.Runner.Egress + ":" + monitor.Runner.Command
		result.Fields = monitor.Runner.Fields
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-12s %s\n  %-12s %s\n  %-12s %s\n\nRestart or log in through the session owner to begin collection.\n",
		view.success(),
		view.strong("Monitoring enabled for "+sanitizeCell(alias, 64)),
		"Mode", mode,
		"Egress", sanitizeCell(emptyLabel(result.Egress, "none"), 4096),
		"Fields", sanitizeCell(strings.Join(result.Fields, ", "), 512),
	)
	return err
}

func validMonitorTransition(current, requested domain.MonitorMode) bool {
	if current == requested {
		return current != domain.MonitorOff
	}
	switch current {
	case domain.MonitorOff:
		return requested == domain.MonitorNotify
	case domain.MonitorNotify:
		return requested == domain.MonitorQueue
	case domain.MonitorQueue:
		return requested == domain.MonitorAgent
	case domain.MonitorAgent:
		return false
	}
	return false
}

func accountMonitorValidation(
	configuration config.Config,
	alias string,
	account config.Account,
) error {
	configuration.Accounts[alias] = account
	return configuration.Validate()
}

func (command *monitorDisableCommand) Run(app *runtime) error {
	if command.RetainQueue == command.PurgeQueue {
		return errors.New("choose exactly one of --retain-queue or --purge-queue")
	}
	if !command.Approve {
		return errors.New("review the queue treatment and pass --approve to disable monitoring")
	}
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, account, err := configuration.ResolveAccount(command.Account)
	if err != nil {
		return err
	}
	if err := config.Update(app.context, path, func(latest *config.Config) error {
		latestAlias, latestAccount, err := latest.ResolveAccount(string(account.ID))
		if err != nil {
			return err
		}
		latestAccount.Monitor = nil
		latest.Accounts[latestAlias] = latestAccount
		return nil
	}); err != nil {
		return err
	}
	purged := 0
	if command.PurgeQueue {
		purged, err = purgeLocalEvents(app, account.ID)
		if err != nil {
			return fmt.Errorf(
				"monitoring is disabled, but the queue could not be purged safely: %w",
				err,
			)
		}
	}
	result := struct {
		Account  string `json:"account"`
		Disabled bool   `json:"disabled"`
		Queue    string `json:"queue"`
		Purged   int    `json:"purged"`
	}{
		Account: alias, Disabled: true,
		Queue:  map[bool]string{true: "purged", false: "retained"}[command.PurgeQueue],
		Purged: purged,
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  Monitoring disabled for %s; queue %s (%d events purged).\n",
		view.success(), sanitizeCell(alias, 64), result.Queue, purged,
	)
	return err
}

func (command *eventsListCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	account, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MonitorEventListInput{
		Account: account, State: command.State, Offset: command.Offset, Limit: command.Limit,
	}
	if input.State == "all" {
		input.State = ""
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListMonitorEvents(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMonitorEvents(app, page)
}

func (command *eventsAcknowledgeCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	account, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MonitorAcknowledgeInput{
		Account: account, EventID: command.EventID,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	event, err := client.AcknowledgeMonitorEvent(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, event)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  Acknowledged %s in the local queue.\n",
		view.success(), sanitizeCell(event.ID, 64),
	)
	return err
}

func (command *eventsPurgeCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New("event purge is permanent; pass --approve")
	}
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	account, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	count, err := purgeLocalEvents(app, account)
	if err != nil {
		return err
	}
	result := struct {
		Account domain.AccountID `json:"account"`
		Purged  int              `json:"purged"`
	}{Account: account, Purged: count}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf("%s  Purged %d local events.\n", view.success(), count)
	return err
}

type purgeCatalog struct{}

func (purgeCatalog) MonitorPolicy(
	context.Context,
	domain.AccountID,
) (application.MonitorPolicy, error) {
	return application.MonitorPolicy{}, errors.New("status is unavailable during local purge")
}

func purgeLocalEvents(app *runtime, account domain.AccountID) (returnCount int, returnErr error) {
	auditPath, err := paths.AuditPath()
	if err != nil {
		return 0, err
	}
	recorder, err := audit.NewFileRecorder(auditPath, audit.Options{})
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, recorder.Close()) }()
	service, err := application.NewMonitorService(purgeCatalog{}, eventqueue.New(), recorder)
	if err != nil {
		return 0, err
	}
	return service.Purge(app.context, account, app.caller())
}

func writeMonitorStatuses(app *runtime, statuses []application.MonitorStatus) error {
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(
		writer,
		"ACCOUNT\tMODE\tCOLLECTION\tPENDING\tSINK\tEGRESS\tFIELDS",
	); err != nil {
		return err
	}
	for _, status := range statuses {
		sink, egress, fields := "-", "-", "-"
		if status.Notification != nil {
			sink, egress = status.Notification.Destination, status.Notification.Egress
			fields = strings.Join(status.Notification.Fields, ",")
		}
		if status.Runner != nil {
			sink, egress = status.Runner.Destination, status.Runner.Egress
			fields = strings.Join(status.Runner.Fields, ",")
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%t\t%d\t%s\t%s\t%s\n",
			sanitizeCell(status.Alias, 64), status.Mode,
			status.CollectionEnabled, status.Queue.Pending,
			sanitizeCell(sink, 4096), egress, sanitizeCell(fields, 512),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeMonitorEvents(app *runtime, page application.MonitorEventPage) error {
	if len(page.Events) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No queued events.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(
		writer,
		"STATE\tRECEIVED\tSENDER\tSUBJECT\tDELIVERIES\tEVENT",
	); err != nil {
		return err
	}
	for _, event := range page.Events {
		sender := event.Sender.Address
		if sender == "" {
			sender = event.Sender.Name
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\t%s\n",
			event.State, sanitizeCell(event.ReceivedAt, 64),
			sanitizeCell(sender, 320), sanitizeCell(event.Subject, 80),
			event.DeliveryCount, sanitizeCell(event.ID, 64),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func emptyLabel(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
