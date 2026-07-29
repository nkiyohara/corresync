package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/nkiyohara/corresync/internal/application"
)

type agendaCommand struct {
	List agendaListCommand `cmd:"" help:"List event metadata across isolated calendar accounts."`
}

type agendaListCommand struct {
	AllAccounts bool   `name:"all-accounts" help:"Read every configured calendar account; required."`
	Start       string `help:"Inclusive RFC3339 window start (required)."`
	End         string `help:"Exclusive RFC3339 window end, at most 31 days later (required)."`
	TimeZone    string `name:"time-zone" default:"UTC" help:"IANA display time zone, for example Europe/London."`
	Offset      int    `default:"0" help:"Zero-based global page offset (0-400)."`
	Limit       int    `default:"50" help:"Events to return (1-100)."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

func (command *agendaListCommand) Run(app *runtime) (returnErr error) {
	if !command.AllAccounts {
		return errors.New(
			"agenda list is a read-only projection; pass --all-accounts explicitly",
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
	return writeAgendaProjectionTable(app, page)
}

func writeAgendaProjectionTable(
	app *runtime,
	page application.AgendaProjectionPage,
) error {
	if len(page.Events) == 0 {
		if _, err := fmt.Fprintln(app.stdout, "No events."); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(
			writer,
			"ACCOUNT\tPROVIDER\tSTART\tEND\tSUBJECT\tLOCATION\tFLAGS\tID",
		); err != nil {
			return err
		}
		for _, projected := range page.Events {
			event := projected.Event
			flags := ""
			if event.IsAllDay {
				flags += "A"
			}
			if event.OriginalStartFloating {
				flags += "F"
			}
			if event.IsOnlineMeeting {
				flags += "O"
			}
			if event.IsCancelled {
				flags += "C"
			}
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
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	for _, status := range page.Accounts {
		for _, degradation := range status.Degradations {
			if _, err := view.printf(
				"%s  %s (%s): %s\n",
				view.warning(),
				sanitizeCell(status.Alias, 64),
				sanitizeCell(string(status.Provider), 64),
				sanitizeCell(degradation.Reason, 512),
			); err != nil {
				return err
			}
		}
	}
	for _, failure := range page.Failures {
		if _, err := view.printf(
			"%s  Incomplete: %s (%s) · %s\n",
			view.warning(),
			sanitizeCell(failure.Alias, 64),
			sanitizeCell(string(failure.Provider), 64),
			sanitizeCell(failure.Reason, 512),
		); err != nil {
			return err
		}
	}
	return nil
}
