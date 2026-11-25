package main

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/audit"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/dispatch"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/eventqueue"
	"github.com/nkiyohara/corresync/internal/paths"
)

type monitorCommand struct {
	Status  monitorStatusCommand  `cmd:"" help:"Show an account's monitoring settings and queued events."`
	Enable  monitorEnableCommand  `cmd:"" help:"Turn on monitoring for an account, one mode at a time."`
	Disable monitorDisableCommand `cmd:"" help:"Turn off monitoring and keep or delete its queued events."`
}

func (*monitorCommand) Help() string {
	return `Monitoring is off until you turn it on for an account. It checks for new
mail and records each new message as an event in a queue on this computer.
In notify mode it also shows a desktop notification, in queue mode it only
records, and in agent mode it runs a program you choose for each event.
Turn on notify, then queue, then agent, one step at a time. Turning
monitoring on or off changes the configuration, so stop the local daemon
first. AI agents connected through MCP cannot turn monitoring on.

Examples:
  corr monitor status --all-accounts
  corr monitor enable --account work --mode notify --approve
  corr monitor disable --account work --retain-queue --approve`
}

type monitorStatusCommand struct {
	Account     string `help:"Account to show (alias); defaults to the default account."`
	AllAccounts bool   `name:"all-accounts" help:"Show every account; this does not start a sign-in."`
	JSON        bool   `help:"Write machine-readable JSON."`
}

type monitorEnableCommand struct {
	Account             string        `help:"Account to monitor (alias); defaults to the default account."`
	Mode                string        `required:"" enum:"notify,queue,agent" help:"Mode to turn on: notify, queue, or agent."`
	PollInterval        time.Duration `default:"1m" help:"How often to check for new mail (15s-1h)."`
	Debounce            time.Duration `default:"30s" help:"Shortest time between notifications or runner calls (0s-15m)."`
	Retention           time.Duration `default:"720h" help:"How long to keep handled events and the record of events already seen (1h-2160h)."`
	RateLimitHour       int           `name:"rate-limit-hour" default:"30" help:"Most events passed on per hour (1-1000)."`
	SenderDomains       []string      `name:"sender-domain" help:"Only match mail from this sender domain, in lower case; repeat for more."`
	SubjectContains     []string      `name:"subject-contains" help:"Only match subjects that contain this text; repeat for alternatives."`
	ImportantOnly       bool          `name:"important-only" help:"Only match messages the provider marks as high importance."`
	QuietStart          string        `help:"Start of quiet hours as HH:MM; needs --quiet-end and --quiet-time-zone."`
	QuietEnd            string        `help:"End of quiet hours as HH:MM."`
	QuietTimeZone       string        `help:"Time zone of the quiet hours, such as Europe/London."`
	NotificationFields  []string      `name:"notification-field" help:"Event field to show in notify mode; repeat."`
	Runner              string        `type:"path" help:"Full path of the program to run for each event; required for agent mode."`
	RunnerArguments     []string      `name:"runner-argument" help:"Argument passed to the runner as written, without a shell; repeat."`
	RunnerEgress        string        `default:"local" enum:"local,remote" help:"Where the runner may send event fields: local (this computer only) or remote."`
	RunnerFields        []string      `name:"runner-field" help:"Event field passed to the runner; repeat."`
	RunnerTimeout       time.Duration `default:"2m" help:"How long the runner may take (1s-5m)."`
	ApproveRemoteEgress bool          `name:"approve-remote-egress" help:"Confirm, separately, that the runner may send event fields off this computer."`
	Approve             bool          `help:"Confirm these monitoring settings for the account."`
	JSON                bool          `help:"Write machine-readable JSON."`
}

type monitorDisableCommand struct {
	Account     string `help:"Account to stop monitoring (alias); defaults to the default account."`
	RetainQueue bool   `name:"retain-queue" help:"Keep the queued events after turning monitoring off."`
	PurgeQueue  bool   `name:"purge-queue" help:"Permanently delete the queued events after turning monitoring off."`
	Approve     bool   `help:"Confirm turning monitoring off and what happens to the queued events."`
	JSON        bool   `help:"Write machine-readable JSON."`
}

type eventsCommand struct {
	List        eventsListCommand        `cmd:"" help:"List the events monitoring has queued for an account."`
	Acknowledge eventsAcknowledgeCommand `cmd:"" help:"Mark one queued event as handled."`
	Purge       eventsPurgeCommand       `cmd:"" help:"Permanently delete all queued events for an account."`
}

func (*eventsCommand) Help() string {
	return `Events are new messages that monitoring found, kept on this computer in a
queue per account. An event holds details such as the sender, subject, and
time, never the message body. Turn monitoring on with corr monitor enable.

Examples:
  corr events list --account work --state pending
  corr events acknowledge evt_0123456789abcdef0123456789abcdef --account work
  corr events purge --account work --approve`
}

type eventsListCommand struct {
	Account string `help:"Account whose events to list (alias); defaults to the default account."`
	State   string `default:"all" enum:"all,pending,dispatched,acknowledged" help:"Show only events in this state."`
	Offset  int    `default:"0" help:"Number of events to skip (0-10000)."`
	Limit   int    `default:"50" help:"Number of events to show (1-100)."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type eventsAcknowledgeCommand struct {
	EventID string `arg:"" help:"Event ID (starts with evt_), as shown by corr events list."`
	Account string `help:"Account the event belongs to (alias); defaults to the default account."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type eventsPurgeCommand struct {
	Account string `help:"Account whose queued events to delete (alias); defaults to the default account."`
	Approve bool   `help:"Confirm permanently deleting this account's queued events."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *monitorStatusCommand) Run(app *runtime) (returnErr error) {
	if command.AllAccounts && command.Account != "" {
		return errors.New("use either --all-accounts or --account, not both")
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
			"monitoring stays off until you approve it; check the account, mode, filters, fields, and where events go, then add --approve",
		)
	}
	if err := validateMonitorNotificationPlatform(
		domain.MonitorMode(command.Mode),
		goruntime.GOOS,
	); err != nil {
		return err
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
		err := fmt.Errorf(
			"turn monitoring on one mode at a time, off then notify then queue then agent (it is %s now, and you asked for %s)",
			currentMode,
			mode,
		)
		if next, ok := nextMonitorMode(currentMode); ok {
			return withHint(err, shellCommand(
				"corr", "monitor", "enable", "--account", alias, "--mode", string(next), "--approve",
			))
		}
		return err
	}
	if currentMode != domain.MonitorAgent && mode == domain.MonitorAgent &&
		command.RunnerEgress == "remote" {
		return errors.New(
			"turn on agent mode with --runner-egress local first; allowing remote is a separate, later step",
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
		return errors.New("quiet hours need --quiet-start, --quiet-end, and --quiet-time-zone together")
	}
	if quietValues == 3 {
		monitor.QuietHours = &config.QuietHours{
			Start: command.QuietStart, End: command.QuietEnd,
			TimeZone: command.QuietTimeZone,
		}
	}
	switch mode {
	case domain.MonitorOff:
		return withHint(errors.New("to turn monitoring off, use corr monitor disable"), shellCommand("corr", "monitor", "disable", "--help"))
	case domain.MonitorNotify:
		if command.Runner != "" || len(command.RunnerArguments) > 0 ||
			len(command.RunnerFields) > 0 || command.ApproveRemoteEgress ||
			command.RunnerEgress != "local" {
			return errors.New("notify mode does not run a program; remove the --runner options")
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
			return errors.New("queue mode only records events; remove the --notification-field and --runner options")
		}
	case domain.MonitorAgent:
		if command.Runner == "" {
			return errors.New("agent mode needs --runner with the full path of the program to run")
		}
		if len(command.NotificationFields) > 0 {
			return errors.New("agent mode does not show notifications; remove --notification-field")
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
			return errors.New("the account was renamed while monitoring was being turned on; run the command again")
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
		"%s  %s\n\n  %-12s %s\n  %-12s %s\n  %-12s %s\n\n%s\n%s\n",
		view.success(),
		view.strong("Monitoring turned on for "+sanitizeCell(alias, 64)),
		"Mode", mode,
		"Sends to", sanitizeCell(emptyLabel(result.Egress, "none"), 4096),
		"Fields", sanitizeCell(emptyLabel(strings.Join(result.Fields, ", "), "none"), 512),
		wrapText("It starts checking for mail when the local daemon starts again and the account is signed in.", app.textWidth(app.stdout), ""),
		view.nextStep(shellCommand("corr", "auth", "login", "--account", alias)),
	)
	return err
}

func validateMonitorNotificationPlatform(
	mode domain.MonitorMode,
	goos string,
) error {
	if mode != domain.MonitorNotify {
		return nil
	}
	return dispatch.ValidateDesktopNotificationPlatform(goos)
}

// nextMonitorMode is the mode enable accepts after current, if any.
func nextMonitorMode(current domain.MonitorMode) (domain.MonitorMode, bool) {
	for _, next := range []domain.MonitorMode{domain.MonitorNotify, domain.MonitorQueue, domain.MonitorAgent} {
		if next != current && validMonitorTransition(current, next) {
			return next, true
		}
	}
	return "", false
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
		return errors.New("choose one of --retain-queue or --purge-queue")
	}
	if !command.Approve {
		return errors.New("check what happens to the queued events, then add --approve to turn monitoring off")
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
				"monitoring is off, but the queued events could not be deleted safely: %w",
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
	queue := "Queued events were kept."
	if command.PurgeQueue {
		queue = fmt.Sprintf("Deleted %d queued events.", purged)
	}
	_, err = view.printf(
		"%s  Monitoring turned off for %s. %s\n",
		view.success(), sanitizeCell(alias, 64), queue,
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
		"%s  Marked %s as handled.\n",
		view.success(), sanitizeCell(event.ID, 64),
	)
	return err
}

func (command *eventsPurgeCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New("deleting the queued events is permanent; add --approve to confirm")
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
	_, err = view.printf("%s  Deleted %d queued events.\n", view.success(), count)
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
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	next := ""
	for index, status := range statuses {
		if index > 0 {
			if _, err := fmt.Fprintln(app.stdout); err != nil {
				return err
			}
		}
		icon, state := view.muted("–"), "off"
		if status.Mode != domain.MonitorOff {
			icon, state = view.warning(), string(status.Mode)+" · not checking for mail now"
			if status.CollectionEnabled {
				icon, state = view.success(), string(status.Mode)+" · checking for new mail"
			}
		}
		if _, err := view.printf(
			"%s  %s %s\n",
			icon,
			view.strong(fmt.Sprintf("%-16s", sanitizeCell(status.Alias, 64))),
			view.muted(state),
		); err != nil {
			return err
		}
		rows := [][2]string{{"Pending events", strconv.Itoa(status.Queue.Pending)}}
		for _, sink := range []struct {
			label string
			view  *application.MonitorSinkView
		}{{"Notifies with", status.Notification}, {"Runs", status.Runner}} {
			if sink.view == nil {
				continue
			}
			rows = append(rows,
				[2]string{sink.label, sanitizeCell(sink.view.Destination, 4096) + " (" + monitorEgressLabel(sink.view.Egress) + ")"},
				[2]string{"Fields", sanitizeCell(emptyLabel(strings.Join(sink.view.Fields, ", "), "none"), 512)},
			)
		}
		for _, row := range rows {
			if _, err := view.printf("   %-16s %s\n", row[0], row[1]); err != nil {
				return err
			}
		}
		if next == "" && status.Queue.Pending > 0 {
			next = shellCommand("corr", "events", "list", "--account", status.Alias, "--state", "pending")
		}
	}
	if next == "" {
		return nil
	}
	_, err := view.printf("\n%s\n", view.nextStep(next))
	return err
}

func monitorEgressLabel(egress string) string {
	switch egress {
	case "local":
		return "stays on this computer"
	case "remote":
		return "may leave this computer"
	default:
		return emptyLabel(egress, "unknown")
	}
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
	next := ""
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
		if next == "" && event.State != "acknowledged" {
			next = shellCommand("corr", "events", "acknowledge", event.ID, "--account", event.AccountAlias)
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if next == "" {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := view.printf("\n%s\n", view.nextStep(next))
	return err
}

func emptyLabel(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
