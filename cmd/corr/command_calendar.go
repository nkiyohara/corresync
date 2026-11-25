package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/nkiyohara/corresync/internal/application"
)

type calendarCommand struct {
	Folders calendarFoldersCommand `cmd:"" help:"List calendars and their IDs."`
	List    calendarListCommand    `cmd:"" help:"List events between two times."`
	Create  calendarCreateCommand  `cmd:"" help:"Create an event."`
	Update  calendarUpdateCommand  `cmd:"" help:"Change an event's details, such as its time or attendees."`
	Cancel  calendarCancelCommand  `cmd:"" help:"Cancel an event."`
}

var calendarHelpExamples = []helpExample{
	{"List this week's events:", []string{
		"calendar", "list", "--start", "2026-11-30T00:00:00Z", "--end", "2026-12-07T00:00:00Z",
	}},
	{"Create an event with a Teams or Google Meet link (the first run shows a preview):", []string{
		"calendar", "create", "--subject", "Design review", "--start", "2026-12-01T09:00:00Z",
		"--end", "2026-12-01T10:00:00Z", "--required-attendee", "ana@example.invalid", "--online-meeting",
	}},
	{"Move an event to a new time:", []string{
		"calendar", "update", "--event-id", "AAMkAGE1-event", "--change-key", "DwAAABYAAAA-key",
		"--start", "2026-12-01T14:00:00Z", "--end", "2026-12-01T15:00:00Z",
	}},
}

// Help is the detail text of `corr calendar --help`.
func (calendarCommand) Help() string {
	return helpWithExamples(`
create, update, and cancel always show a preview first and change nothing. Run
the same command again with --approve added to carry it out; the preview ends
with that command and says whether attendees will be notified.

Times use RFC 3339, such as 2026-12-01T09:00:00Z or 2026-12-01T10:00:00+01:00.
Event IDs and change keys are shown by corr calendar list. A change key names
one version of an event: if the event changed after you listed it, list it
again.`, calendarHelpExamples)
}

type calendarFoldersCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	Offset  int    `default:"0" help:"Skip this many results, for paging."`
	Limit   int    `default:"100" help:"Most calendars to show (1-100)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type calendarListCommand struct {
	Account    string `help:"Account alias (default: your default account)."`
	CalendarID string `name:"calendar-id" placeholder:"ID" help:"Calendar ID from corr calendar folders (default: main calendar)."`
	Start      string `placeholder:"TIME" help:"Start of the time range, such as 2026-12-01T00:00:00Z (required)."`
	End        string `placeholder:"TIME" help:"End of the time range, not included and at most 31 days after the start (required)."`
	JSON       bool   `help:"Print JSON for scripts and agents."`
}

var calendarCreateHelpExamples = []helpExample{
	{"A weekly meeting for ten weeks:", []string{
		"calendar", "create", "--subject", "Team sync", "--start", "2026-12-02T10:00:00Z",
		"--end", "2026-12-02T11:00:00Z", "--required-attendee", "ana@example.invalid",
		"--recurrence", "weekly", "--recurrence-day", "Wednesday", "--recurrence-count", "10",
	}},
}

type calendarCreateCommand struct {
	Account              string   `help:"Account alias (default: your default account)."`
	CalendarID           string   `name:"calendar-id" placeholder:"ID" help:"Calendar ID from corr calendar folders (default: main calendar)."`
	Subject              string   `help:"Title of the event, on one line."`
	BodyFile             string   `name:"body-file" placeholder:"FILE" help:"Description file (plain text), or - for standard input."`
	Start                string   `placeholder:"TIME" help:"Start time (required)."`
	End                  string   `placeholder:"TIME" help:"End time, at most 31 days after the start (required)."`
	Location             string   `help:"Where the event takes place, on one line."`
	AllDay               bool     `name:"all-day" help:"All-day event; start and end at midnight."`
	TimeZone             string   `name:"time-zone" help:"Windows time-zone ID (default: UTC)."`
	RequiredAttendees    []string `name:"required-attendee" placeholder:"ADDRESS" group:"Attendees and meeting link:" help:"Attendee who must attend; repeat for more."`
	OptionalAttendees    []string `name:"optional-attendee" placeholder:"ADDRESS" group:"Attendees and meeting link:" help:"Optional attendee; repeat for more."`
	OnlineMeeting        bool     `name:"online-meeting" group:"Attendees and meeting link:" help:"Add a Teams or Google Meet link."`
	TeamsMeeting         bool     `name:"teams-meeting" group:"Attendees and meeting link:" help:"Older form of --online-meeting; needs a Teams calendar."`
	ReminderMinutes      *int     `name:"reminder-minutes" placeholder:"MINUTES" group:"Reminder and repeat:" help:"Remind this many minutes before; none if left out."`
	Recurrence           string   `enum:"none,daily,weekly,monthly,yearly" default:"none" group:"Reminder and repeat:" help:"Repeat: none, daily, weekly, monthly, or yearly."`
	RecurrenceInterval   int      `name:"recurrence-interval" default:"1" group:"Reminder and repeat:" help:"Repeat every N days, weeks, months, or years."`
	RecurrenceDay        []string `name:"recurrence-day" placeholder:"DAY" group:"Reminder and repeat:" help:"Weekday of a weekly event, such as Monday; repeat for more."`
	RecurrenceDayOfMonth int      `name:"recurrence-day-of-month" placeholder:"N" group:"Reminder and repeat:" help:"Day of the month of a monthly or yearly event."`
	RecurrenceMonth      string   `name:"recurrence-month" placeholder:"MONTH" group:"Reminder and repeat:" help:"Month of a yearly event, such as January."`
	RecurrenceEndDate    string   `name:"recurrence-end-date" placeholder:"DATE" group:"Reminder and repeat:" help:"Last date to repeat on, as YYYY-MM-DD; not with --recurrence-count."`
	RecurrenceCount      int      `name:"recurrence-count" placeholder:"N" group:"Reminder and repeat:" help:"Number of times to repeat; not with --recurrence-end-date."`
	Approve              bool     `help:"Create the event instead of showing a preview."`
	JSON                 bool     `help:"Print JSON for scripts and agents."`
}

// Help is the detail text of `corr calendar create --help`.
func (calendarCreateCommand) Help() string {
	return helpWithExamples(`
Times use RFC 3339, such as 2026-12-01T09:00:00Z. --time-zone is the event's
own time zone, as a Windows ID such as "W. Europe Standard Time"; an all-day
event runs from midnight to midnight in it. --online-meeting adds the
calendar provider's own meeting link. The preview says whether attendees will
be sent an invitation.`, calendarCreateHelpExamples)
}

var calendarUpdateHelpExamples = []helpExample{
	{"Rename an event and clear its location:", []string{
		"calendar", "update", "--event-id", "AAMkAGE1-event", "--change-key", "DwAAABYAAAA-key",
		"--subject", "Design review (moved)", "--clear-location",
	}},
}

type calendarUpdateCommand struct {
	Account              string   `help:"Account alias (default: your default account)."`
	EventID              string   `name:"event-id" placeholder:"ID" help:"Event ID, from corr calendar list (required)."`
	ChangeKey            string   `name:"change-key" placeholder:"KEY" help:"Change key of that event, from the same list (required)."`
	Subject              string   `help:"New title."`
	ClearSubject         bool     `name:"clear-subject" help:"Remove the title."`
	BodyFile             string   `name:"body-file" placeholder:"FILE" help:"New description file (plain text), or - for standard input."`
	ClearBody            bool     `name:"clear-body" help:"Remove the description."`
	Start                string   `placeholder:"TIME" help:"New start time; give --end too."`
	End                  string   `placeholder:"TIME" help:"New end time; give --start too."`
	TimeZone             string   `name:"time-zone" help:"New Windows time-zone ID; give --start and --end too."`
	Location             string   `help:"New location."`
	ClearLocation        bool     `name:"clear-location" help:"Remove the location."`
	AllDay               string   `name:"all-day" enum:"unchanged,true,false" default:"unchanged" help:"Make it an all-day event (true) or not (false)."`
	ReminderMinutes      *int     `name:"reminder-minutes" placeholder:"MINUTES" group:"Reminder and repeat:" help:"Set a reminder this many minutes before the start."`
	DisableReminder      bool     `name:"disable-reminder" group:"Reminder and repeat:" help:"Turn the reminder off."`
	Recurrence           string   `enum:"unchanged,none,daily,weekly,monthly,yearly" default:"unchanged" group:"Reminder and repeat:" help:"New repeat pattern; none stops repeating."`
	RecurrenceInterval   int      `name:"recurrence-interval" default:"1" group:"Reminder and repeat:" help:"Repeat every N days, weeks, months, or years."`
	RecurrenceDay        []string `name:"recurrence-day" placeholder:"DAY" group:"Reminder and repeat:" help:"Weekday of a weekly event; repeat for more."`
	RecurrenceDayOfMonth int      `name:"recurrence-day-of-month" placeholder:"N" group:"Reminder and repeat:" help:"Day of the month of a monthly or yearly event."`
	RecurrenceMonth      string   `name:"recurrence-month" placeholder:"MONTH" group:"Reminder and repeat:" help:"Month of a yearly event."`
	RecurrenceEndDate    string   `name:"recurrence-end-date" placeholder:"DATE" group:"Reminder and repeat:" help:"Last date to repeat on, as YYYY-MM-DD."`
	RecurrenceCount      int      `name:"recurrence-count" placeholder:"N" group:"Reminder and repeat:" help:"Number of times to repeat."`
	ReplaceAttendees     bool     `name:"replace-attendees" group:"Attendees:" help:"Replace both attendee lists with the addresses below; none given clears them."`
	RequiredAttendees    []string `name:"required-attendee" placeholder:"ADDRESS" group:"Attendees:" help:"Attendee who must attend; repeat for more."`
	OptionalAttendees    []string `name:"optional-attendee" placeholder:"ADDRESS" group:"Attendees:" help:"Optional attendee; repeat for more."`
	Approve              bool     `help:"Change the event instead of showing a preview."`
	JSON                 bool     `help:"Print JSON for scripts and agents."`
}

// Help is the detail text of `corr calendar update --help`.
func (calendarUpdateCommand) Help() string {
	return helpWithExamples(`
Only what you pass changes; everything else stays as it is. Use the --clear-
flags to remove a title, description, or location. A new repeat pattern
replaces the old one and takes the same --recurrence- flags as calendar
create. Changing attendees may send them an update.`, calendarUpdateHelpExamples)
}

type calendarCancelCommand struct {
	Account   string `help:"Account alias (default: your default account)."`
	EventID   string `name:"event-id" placeholder:"ID" help:"Event ID, from corr calendar list (required)."`
	ChangeKey string `name:"change-key" placeholder:"KEY" help:"Change key of that event, from the same list (required)."`
	Approve   bool   `help:"Cancel the event instead of showing a preview."`
	JSON      bool   `help:"Print JSON for scripts and agents."`
}

func (command *calendarFoldersCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.CalendarFolderListInput{
		Account: accountID, Offset: command.Offset, Limit: command.Limit,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListCalendarFolders(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeCalendarFolderTable(app, page, emptyResult{
		message: fmt.Sprintf("No calendars found in account %s.", sanitizeCell(alias, 64)),
	})
}

func (command *calendarListCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	calendar := application.CalendarFolder{
		Kind: application.CalendarFolderDistinguished,
		ID:   "calendar",
	}
	if command.CalendarID != "" {
		calendar = application.CalendarFolder{Kind: application.CalendarFolderOpaque, ID: command.CalendarID}
	}
	input := application.CalendarListInput{
		Account:  accountID,
		Calendar: calendar,
		Start:    command.Start,
		End:      command.End,
	}
	if err := input.Validate(); err != nil {
		return err
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListCalendar(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeCalendarTable(app, page, emptyResult{message: fmt.Sprintf(
		"No events from %s to %s in %s of account %s.",
		sanitizeCell(command.Start, 64), sanitizeCell(command.End, 64),
		calendarLabel(calendar), sanitizeCell(alias, 64),
	)})
}

// calendarLabel names a calendar in a sentence.
func calendarLabel(calendar application.CalendarFolder) string {
	if calendar.Kind == application.CalendarFolderDistinguished {
		return "the main calendar"
	}
	return "calendar " + sanitizeCell(calendar.ID, 80)
}

func writeCalendarFolderTable(app *runtime, page application.CalendarFolderPage, empty emptyResult) error {
	if len(page.Calendars) == 0 {
		return app.writeEmpty(empty)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tDEFAULT\tCAN EDIT\tACCESS\tTIME ZONE\tCALENDAR ID"); err != nil {
		return err
	}
	for _, calendar := range page.Calendars {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(calendar.DisplayName, 64),
			previewYesNo(calendar.IsDefault),
			previewYesNo(calendar.CanEdit),
			sanitizeCell(calendar.AccessRole, 16),
			sanitizeCell(calendar.TimeZone, 64),
			sanitizeCell(calendar.ID, 4096),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (command *calendarCreateCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	body, err := readPlainTextBody(app, command.BodyFile, "calendar event")
	if err != nil {
		return err
	}
	calendar := application.CalendarFolder{
		Kind: application.CalendarFolderDistinguished,
		ID:   "calendar",
	}
	if command.CalendarID != "" {
		calendar = application.CalendarFolder{Kind: application.CalendarFolderOpaque, ID: command.CalendarID}
	}
	recurrence, err := command.calendarRecurrence()
	if err != nil {
		return err
	}
	var reminder *application.CalendarReminder
	if command.ReminderMinutes != nil {
		reminder = &application.CalendarReminder{
			Enabled: true, MinutesBeforeStart: *command.ReminderMinutes,
		}
	}
	input := application.CalendarCreateInput{
		Account: accountID, Calendar: calendar,
		Subject: command.Subject, Body: body,
		Start: command.Start, End: command.End, Location: command.Location,
		RequiredAttendees: command.RequiredAttendees,
		OptionalAttendees: command.OptionalAttendees,
		OnlineMeeting:     command.OnlineMeeting,
		TeamsMeeting:      command.TeamsMeeting,
		AllDay:            command.AllDay, TimeZone: command.TimeZone,
		Reminder: reminder, Recurrence: recurrence,
	}
	if err := input.Validate(configuration.Policy.MaxAttendees); err != nil {
		return err
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.CreateCalendar(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("calendar create did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		if err := writeCalendarCreateReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "calendar", "create")
	}
	if err := writeCalendarCreateReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitCalendarCreate(app.context, access.Preview.Token, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "created" || access.Created == nil {
		return errors.New("calendar create commit completed without created status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	if _, err := fmt.Fprintln(app.stdout, "Created the event. The request was made once and is not retried."); err != nil {
		return err
	}
	fields := []reviewField{
		{"Event ID", sanitizeCell(access.Created.ID, 4096)},
		{"Change key", sanitizeCell(access.Created.ChangeKey, 4096)},
	}
	if access.Created.OnlineMeetingJoinURL != "" {
		fields = append(fields, reviewField{"Join link", fmt.Sprintf(
			"%s (%s)",
			sanitizeCell(access.Created.OnlineMeetingJoinURL, 8192),
			sanitizeCell(access.Created.OnlineMeetingProvider, 64),
		)})
	}
	return writeReviewFields(app.stdout, fields)
}

func (command *calendarUpdateCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	for _, conflict := range []struct {
		value, clear bool
		flag, other  string
	}{
		{command.Subject != "", command.ClearSubject, "subject", "clear-subject"},
		{command.BodyFile != "", command.ClearBody, "body-file", "clear-body"},
		{command.Location != "", command.ClearLocation, "location", "clear-location"},
		{command.ReminderMinutes != nil, command.DisableReminder, "reminder-minutes", "disable-reminder"},
	} {
		if conflict.value && conflict.clear {
			return withHint(
				fmt.Errorf("%s and %s are mutually exclusive", conflict.flag, conflict.other),
				fmt.Sprintf("Use either --%s or --%s, not both.", conflict.flag, conflict.other),
			)
		}
	}
	input := application.CalendarUpdateInput{
		Account: accountID, EventID: command.EventID, ChangeKey: command.ChangeKey,
	}
	if command.Subject != "" || command.ClearSubject {
		input.Subject = stringValuePointer(command.Subject)
	}
	if command.BodyFile != "" {
		body, err := readPlainTextBody(app, command.BodyFile, "calendar event")
		if err != nil {
			return err
		}
		input.Body = &body
	} else if command.ClearBody {
		input.Body = stringValuePointer("")
	}
	if command.Start != "" {
		input.Start = stringValuePointer(command.Start)
	}
	if command.End != "" {
		input.End = stringValuePointer(command.End)
	}
	if command.TimeZone != "" {
		input.TimeZone = stringValuePointer(command.TimeZone)
	}
	if command.Location != "" || command.ClearLocation {
		input.Location = stringValuePointer(command.Location)
	}
	if command.AllDay != "" && command.AllDay != "unchanged" {
		value := command.AllDay == "true"
		input.AllDay = &value
	}
	if command.ReminderMinutes != nil {
		input.Reminder = &application.CalendarReminder{
			Enabled: true, MinutesBeforeStart: *command.ReminderMinutes,
		}
	} else if command.DisableReminder {
		input.Reminder = &application.CalendarReminder{}
	}
	replaceRecurrence, recurrence, err := command.calendarRecurrence()
	if err != nil {
		return err
	}
	input.ReplaceRecurrence = replaceRecurrence
	input.Recurrence = recurrence
	input.ReplaceAttendees = command.ReplaceAttendees
	input.RequiredAttendees = command.RequiredAttendees
	input.OptionalAttendees = command.OptionalAttendees
	if err := input.ValidateWithAttendeeLimit(configuration.Policy.MaxAttendees); err != nil {
		return withEventIDHint(err, command.EventID, command.ChangeKey)
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.UpdateCalendar(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("calendar update did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		if err := writeCalendarUpdateReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "calendar", "update")
	}
	if err := writeCalendarUpdateReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitCalendarUpdate(app.context, access.Preview.Token, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "updated" || access.Updated == nil {
		return errors.New("calendar update commit completed without updated status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	if access.Updated.ID == "" || access.Updated.ChangeKey == "" {
		_, err = fmt.Fprintln(
			app.stdout,
			"Updated the event. The provider did not return its new ID and change key; list the calendar to see them.",
		)
		return err
	}
	if _, err := fmt.Fprintln(app.stdout, "Updated the event. The request was made once and is not retried."); err != nil {
		return err
	}
	return writeReviewFields(app.stdout, []reviewField{
		{"Event ID", sanitizeCell(access.Updated.ID, 4096)},
		{"New change key", sanitizeCell(access.Updated.ChangeKey, 4096)},
	})
}

// withEventIDHint says where event IDs and change keys come from when a
// command was run without them.
func withEventIDHint(err error, eventID, changeKey string) error {
	if eventID != "" && changeKey != "" {
		return err
	}
	return withHint(err, "Event IDs and change keys are shown by corr calendar list --start TIME --end TIME.")
}

func (command *calendarCancelCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.CalendarCancelInput{
		Account: accountID, EventID: command.EventID, ChangeKey: command.ChangeKey,
	}
	if err := input.Validate(); err != nil {
		return withEventIDHint(err, command.EventID, command.ChangeKey)
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.CancelCalendar(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("calendar cancel did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		if err := writeCalendarCancelReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "calendar", "cancel")
	}
	if err := writeCalendarCancelReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitCalendarCancel(app.context, access.Preview.Token, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "cancelled" || access.Cancelled == nil {
		return errors.New("calendar cancel commit completed without cancelled status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Cancelled event %s as the preview described. The request was made once and is not retried.\n",
		sanitizeCell(access.Cancelled.ID, 4096),
	)
	return err
}

func stringValuePointer(value string) *string { return &value }

// recurrenceDetailsHint is the next step when --recurrence-* flags were given
// without a pattern to apply them to.
const recurrenceDetailsHint = "Add --recurrence daily, weekly, monthly, or yearly."

func (command calendarCreateCommand) calendarRecurrence() (*application.CalendarRecurrence, error) {
	if command.Recurrence == "" || command.Recurrence == "none" {
		if len(command.RecurrenceDay) != 0 || command.RecurrenceDayOfMonth != 0 ||
			command.RecurrenceMonth != "" || command.RecurrenceEndDate != "" || command.RecurrenceCount != 0 {
			return nil, withHint(errors.New("recurrence details require a recurrence pattern"), recurrenceDetailsHint)
		}
		return nil, nil
	}
	patterns := map[string]application.CalendarRecurrencePattern{
		"daily":   application.CalendarRecurrenceDaily,
		"weekly":  application.CalendarRecurrenceWeekly,
		"monthly": application.CalendarRecurrenceAbsoluteMonthly,
		"yearly":  application.CalendarRecurrenceAbsoluteYearly,
	}
	return &application.CalendarRecurrence{
		Pattern: patterns[command.Recurrence], Interval: command.RecurrenceInterval,
		DaysOfWeek: command.RecurrenceDay, DayOfMonth: command.RecurrenceDayOfMonth,
		Month: command.RecurrenceMonth, EndDate: command.RecurrenceEndDate,
		NumberOfOccurrences: command.RecurrenceCount,
	}, nil
}

func (command calendarUpdateCommand) calendarRecurrence() (
	bool,
	*application.CalendarRecurrence,
	error,
) {
	details := len(command.RecurrenceDay) != 0 ||
		command.RecurrenceDayOfMonth != 0 ||
		command.RecurrenceMonth != "" ||
		command.RecurrenceEndDate != "" ||
		command.RecurrenceCount != 0
	switch command.Recurrence {
	case "", "unchanged":
		if details {
			return false, nil, withHint(
				errors.New("recurrence details require a replacement recurrence"),
				recurrenceDetailsHint,
			)
		}
		return false, nil, nil
	case "none":
		if details {
			return false, nil, withHint(
				errors.New("recurrence details cannot be used when clearing recurrence"),
				"Leave out the other --recurrence- flags when you use --recurrence none.",
			)
		}
		return true, nil, nil
	default:
		recurrence, err := (calendarCreateCommand{
			Recurrence:           command.Recurrence,
			RecurrenceInterval:   command.RecurrenceInterval,
			RecurrenceDay:        command.RecurrenceDay,
			RecurrenceDayOfMonth: command.RecurrenceDayOfMonth,
			RecurrenceMonth:      command.RecurrenceMonth,
			RecurrenceEndDate:    command.RecurrenceEndDate,
			RecurrenceCount:      command.RecurrenceCount,
		}).calendarRecurrence()
		return true, recurrence, err
	}
}

func recurrenceLabel(recurrence *application.CalendarRecurrence) string {
	if recurrence == nil {
		return "no"
	}
	value := string(recurrence.Pattern)
	if recurrence.EndDate != "" {
		value += " through " + recurrence.EndDate
	} else {
		value += fmt.Sprintf(" for %d occurrences", recurrence.NumberOfOccurrences)
	}
	return sanitizeCell(value, 256)
}

func reminderLabel(reminder *application.CalendarReminder) string {
	if reminder == nil || !reminder.Enabled {
		return "off"
	}
	return fmt.Sprintf("%d minutes before the start", reminder.MinutesBeforeStart)
}

func writeCalendarCreateReview(
	writer io.Writer,
	review application.CalendarCreateReview,
	committing bool,
) error {
	action := "Preview: no event has been created."
	if committing {
		action = "Creating this event now:"
	}
	calendar := review.Calendar.ID
	if review.Calendar.Kind == application.CalendarFolderDistinguished {
		calendar = "main calendar"
	}
	onlineMeeting := "no"
	if review.OnlineMeeting {
		onlineMeeting = review.OnlineMeetingProvider
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	if err := writeReviewFields(writer, []reviewField{
		{"Calendar", sanitizeCell(calendar, 4096)},
		{"Subject", sanitizeCell(review.Subject, application.MaxCalendarSubjectBytes)},
		{"Start", sanitizeCell(review.Start, 64)},
		{"End", sanitizeCell(review.End, 64)},
		{"Time zone", sanitizeCell(review.TimeZone, 128)},
		{"All day", previewYesNo(review.AllDay)},
		{"Reminder", reminderLabel(review.Reminder)},
		{"Repeats", recurrenceLabel(review.Recurrence)},
		{"Location", sanitizeCell(review.Location, application.MaxCalendarLocationBytes)},
		{"Required", sanitizeCell(joinReviewValues(review.RequiredAttendees), 8192)},
		{"Optional", sanitizeCell(joinReviewValues(review.OptionalAttendees), 8192)},
		{"Sends invitations", previewYesNo(review.InvitationsWillBeSent)},
		{"Online meeting", sanitizeCell(onlineMeeting, 64)},
		{"Description", bodySizeLabel(review.BodyBytes, review.BodySHA256)},
	}); err != nil {
		return err
	}
	return writeReviewBody(writer, review.BodyPreview)
}

func writeCalendarUpdateReview(
	writer io.Writer,
	review application.CalendarUpdateReview,
	committing bool,
) error {
	action := "Preview: the event has not been changed."
	if committing {
		action = "Changing this event now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	if err := writeReviewFields(writer, []reviewField{
		{"Event ID", sanitizeCell(review.EventID, 4096)},
		{"Change key", sanitizeCell(review.ChangeKey, 4096)},
		{"Meeting updates", sanitizeCell(review.MeetingUpdateMode, 64)},
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "Changes:"); err != nil {
		return err
	}
	var changes []reviewField
	if review.Subject != nil {
		changes = append(changes, reviewField{"Subject", reviewTextChange(*review.Subject)})
	}
	if review.Body != nil {
		changes = append(changes, reviewField{"Description", bodySizeLabel(review.Body.Bytes, review.Body.SHA256)})
	}
	if review.Start != nil {
		changes = append(changes,
			reviewField{"Start", sanitizeCell(*review.Start, 64)},
			reviewField{"End", sanitizeCell(*review.End, 64)},
		)
	}
	if review.TimeZone != nil {
		changes = append(changes, reviewField{"Time zone", sanitizeCell(*review.TimeZone, 128)})
	}
	if review.AllDay != nil {
		changes = append(changes, reviewField{"All day", previewYesNo(*review.AllDay)})
	}
	if review.Reminder != nil {
		changes = append(changes, reviewField{"Reminder", reminderLabel(review.Reminder)})
	}
	if review.ReplaceRecurrence {
		changes = append(changes, reviewField{"Repeats", recurrenceLabel(review.Recurrence)})
	}
	if review.ReplaceAttendees {
		changes = append(changes,
			reviewField{"Required", sanitizeCell(joinReviewValues(review.RequiredAttendees), 8192)},
			reviewField{"Optional", sanitizeCell(joinReviewValues(review.OptionalAttendees), 8192)},
			reviewField{"May notify attendees", "yes"},
		)
	}
	if review.Location != nil {
		changes = append(changes, reviewField{"Location", reviewTextChange(*review.Location)})
	}
	if err := writeReviewFields(writer, changes); err != nil {
		return err
	}
	if review.Body == nil {
		return nil
	}
	if review.Body.Bytes == 0 {
		_, err := fmt.Fprintln(writer, "Description: (clear)")
		return err
	}
	return writeReviewBody(writer, review.Body.Preview)
}

func reviewTextChange(value string) string {
	if value == "" {
		return "(clear)"
	}
	return sanitizeCell(value, application.MaxCalendarLocationBytes)
}

func writeCalendarCancelReview(
	writer io.Writer,
	review application.CalendarCancelReview,
	committing bool,
) error {
	action := "Preview: nothing has been cancelled."
	if committing {
		action = "Cancelling this event now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{
		{"Event ID", sanitizeCell(review.EventID, 4096)},
		{"Change key", sanitizeCell(review.ChangeKey, 4096)},
		{"Provider action", sanitizeCell(review.DeleteType, 64)},
		{"Cancellation mode", sanitizeCell(review.CancellationMode, 64)},
		{"May notify attendees", previewYesNo(review.AttendeeNotificationsMaySend)},
	})
}

func joinReviewValues(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func writeCalendarTable(app *runtime, page application.CalendarPage, empty emptyResult) error {
	if len(page.Events) == 0 {
		return app.writeEmpty(empty)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "START\tEND\tSUBJECT\tLOCATION\tFLAGS\tEVENT ID\tCHANGE KEY"); err != nil {
		return err
	}
	flagged := false
	for _, event := range page.Events {
		flags := calendarFlags(event, false)
		flagged = flagged || flags != ""
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(event.Start, 30),
			sanitizeCell(event.End, 30),
			sanitizeCell(event.Subject, 64),
			sanitizeCell(event.Location, 40),
			flags,
			sanitizeCell(event.ID, 4096),
			sanitizeCell(event.ChangeKey, 4096),
		); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return writeCalendarFlagLegend(app, flagged, false)
}

// calendarFlags marks all-day (A), floating start (F, agenda only), online
// meeting (O), and cancelled (C) events.
func calendarFlags(event application.CalendarEvent, floating bool) string {
	flags := ""
	if event.IsAllDay {
		flags += "A"
	}
	if floating && event.OriginalStartFloating {
		flags += "F"
	}
	if event.IsOnlineMeeting {
		flags += "O"
	}
	if event.IsCancelled {
		flags += "C"
	}
	return flags
}

func writeCalendarFlagLegend(app *runtime, flagged, floating bool) error {
	if !flagged {
		return nil
	}
	legend := "Flags: A all day, O online meeting, C cancelled."
	if floating {
		legend = "Flags: A all day, F floating time (no time zone), O online meeting, C cancelled."
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.muted(legend))
	return err
}
