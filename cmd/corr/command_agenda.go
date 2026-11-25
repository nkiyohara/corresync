package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/nkiyohara/corresync/internal/application"
)

type agendaCommand struct {
	List agendaListCommand `cmd:"" help:"List events from every calendar account between two times."`
}

var agendaHelpExamples = []helpExample{
	{"Everything on your calendars this week, shown in London time:", []string{
		"agenda", "list", "--all-accounts", "--start", "2026-11-30T00:00:00Z",
		"--end", "2026-12-07T00:00:00Z", "--time-zone", "Europe/London",
	}},
	{"The next page of the same list:", []string{
		"agenda", "list", "--all-accounts", "--start", "2026-11-30T00:00:00Z",
		"--end", "2026-12-07T00:00:00Z", "--offset", "50",
	}},
}

// Help is the detail text of `corr agenda --help`.
func (agendaCommand) Help() string {
	return helpWithExamples(`
The agenda only reads: each event is labelled with its account, and an account
that could not be read is named under the list. To change an event, use corr
calendar with that account.`, agendaHelpExamples)
}

type agendaListCommand struct {
	AllAccounts bool   `name:"all-accounts" help:"Read every calendar account; required."`
	Start       string `placeholder:"TIME" help:"Start of the time range, such as 2026-12-01T00:00:00Z (required)."`
	End         string `placeholder:"TIME" help:"End of the time range, not included and at most 31 days after the start (required)."`
	TimeZone    string `name:"time-zone" default:"UTC" help:"Time zone to show times in, such as Europe/London."`
	Offset      int    `default:"0" help:"Skip this many events across all accounts, for paging (0-400)."`
	Limit       int    `default:"50" help:"Most events to show (1-100)."`
	JSON        bool   `help:"Print JSON for scripts and agents."`
}

func (command *agendaListCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	if !command.AllAccounts {
		suggested := *command
		suggested.AllAccounts = true
		flags, _ := commandLineWords(&suggested)
		return withHint(
			errors.New("agenda list reads every account, so it needs --all-accounts"),
			printableCommand(append([]string{commandName, "agenda", "list"}, flags...)...),
		)
	}
	input := application.AgendaProjectionInput{
		Start: command.Start, End: command.End,
		DisplayTimeZone: command.TimeZone,
		Offset:          command.Offset, Limit: command.Limit,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListAgenda(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeAgendaProjectionTable(app, page, emptyResult{message: fmt.Sprintf(
		"No events from %s to %s in any account.",
		sanitizeCell(command.Start, 64), sanitizeCell(command.End, 64),
	)})
}

func writeAgendaProjectionTable(
	app *runtime,
	page application.AgendaProjectionPage,
	empty emptyResult,
) error {
	if len(page.Events) == 0 {
		if err := app.writeEmpty(empty); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(
			writer,
			"ACCOUNT\tPROVIDER\tSTART\tEND\tSUBJECT\tLOCATION\tFLAGS\tEVENT ID",
		); err != nil {
			return err
		}
		flagged := false
		for _, projected := range page.Events {
			event := projected.Event
			flags := calendarFlags(event, true)
			flagged = flagged || flags != ""
			if _, err := fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sanitizeCell(projected.AccountAlias, 64),
				sanitizeCell(string(event.Provenance.Provider), 64),
				sanitizeCell(projected.DisplayStart, 35),
				sanitizeCell(projected.DisplayEnd, 35),
				sanitizeCell(event.Subject, 64),
				sanitizeCell(event.Location, 40),
				flags,
				sanitizeCell(event.ID, 4096),
			); err != nil {
				return err
			}
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if err := writeCalendarFlagLegend(app, flagged, true); err != nil {
			return err
		}
	}
	return writeProjectionNotices(app, page.Accounts, page.Failures)
}
