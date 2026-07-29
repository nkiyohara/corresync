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
	Folders calendarFoldersCommand `cmd:"" help:"Discover calendars and their opaque IDs."`
	List    calendarListCommand    `cmd:"" help:"List event metadata in an absolute time window."`
	Create  calendarCreateCommand  `cmd:"" help:"Review and create one typed calendar event."`
	Update  calendarUpdateCommand  `cmd:"" help:"Review a versioned event field update."`
	Cancel  calendarCancelCommand  `cmd:"" help:"Review and cancel one versioned event."`
}

type calendarFoldersCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	Offset  int    `default:"0" help:"Zero-based page offset."`
	Limit   int    `default:"100" help:"Calendars to return (1-100)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type calendarListCommand struct {
	Account    string `help:"Configured account alias; defaults to default_account."`
	CalendarID string `name:"calendar-id" help:"Opaque calendar ID; defaults to the primary calendar."`
	Start      string `help:"Inclusive RFC3339 window start (required)."`
	End        string `help:"Exclusive RFC3339 window end, at most 31 days later (required)."`
	JSON       bool   `help:"Write the stable machine-readable schema."`
}

type calendarCreateCommand struct {
	Account              string   `help:"Configured account alias; defaults to default_account."`
	CalendarID           string   `name:"calendar-id" help:"Opaque calendar ID; defaults to the primary calendar."`
	Subject              string   `help:"Event subject; CR/LF are rejected."`
	BodyFile             string   `name:"body-file" help:"Plain-text body file, or - for stdin."`
	Start                string   `help:"RFC3339 event start (required)."`
	End                  string   `help:"RFC3339 event end, at most 31 days later (required)."`
	Location             string   `help:"Plain-text location; CR/LF are rejected."`
	RequiredAttendees    []string `name:"required-attendee" help:"Bare required attendee address; repeat as needed."`
	OptionalAttendees    []string `name:"optional-attendee" help:"Bare optional attendee address; repeat as needed."`
	TeamsMeeting         bool     `name:"teams-meeting" help:"Create a Microsoft Teams online meeting link."`
	AllDay               bool     `name:"all-day" help:"Create an all-day event; start and end must be midnight in the reviewed time zone."`
	TimeZone             string   `name:"time-zone" help:"Exchange/Windows time-zone ID; defaults to UTC."`
	ReminderMinutes      *int     `name:"reminder-minutes" help:"Enable a reminder this many minutes before start; omit to disable."`
	Recurrence           string   `enum:"none,daily,weekly,monthly,yearly" default:"none" help:"Recurrence pattern."`
	RecurrenceInterval   int      `name:"recurrence-interval" default:"1" help:"Recurrence interval."`
	RecurrenceDay        []string `name:"recurrence-day" help:"Weekly weekday (for example Monday); repeat as needed."`
	RecurrenceDayOfMonth int      `name:"recurrence-day-of-month" help:"Monthly or yearly day of month."`
	RecurrenceMonth      string   `name:"recurrence-month" help:"Yearly month (for example January)."`
	RecurrenceEndDate    string   `name:"recurrence-end-date" help:"Inclusive YYYY-MM-DD recurrence end; mutually exclusive with count."`
	RecurrenceCount      int      `name:"recurrence-count" help:"Number of occurrences; mutually exclusive with end date."`
	Approve              bool     `help:"Create the exact preview generated from these arguments."`
	JSON                 bool     `help:"Write the stable machine-readable schema."`
}

type calendarUpdateCommand struct {
	Account              string   `help:"Configured account alias; defaults to default_account."`
	EventID              string   `name:"event-id" help:"Exact event ID returned by calendar list (required)."`
	ChangeKey            string   `name:"change-key" help:"Exact change key returned with the event ID (required)."`
	Subject              string   `help:"Non-empty replacement subject; use clear-subject to clear."`
	ClearSubject         bool     `name:"clear-subject" help:"Clear the event subject."`
	BodyFile             string   `name:"body-file" help:"Replacement plain-text body file, or - for stdin."`
	ClearBody            bool     `name:"clear-body" help:"Clear the event body."`
	Start                string   `help:"Replacement RFC3339 start; requires end."`
	End                  string   `help:"Replacement RFC3339 end; requires start."`
	TimeZone             string   `name:"time-zone" help:"Replacement Exchange/Windows time-zone ID; requires start and end."`
	Location             string   `help:"Non-empty replacement location; use clear-location to clear."`
	ClearLocation        bool     `name:"clear-location" help:"Clear the event location."`
	AllDay               string   `name:"all-day" enum:"unchanged,true,false" default:"unchanged" help:"Set or clear all-day status."`
	ReminderMinutes      *int     `name:"reminder-minutes" help:"Enable or replace a reminder this many minutes before start."`
	DisableReminder      bool     `name:"disable-reminder" help:"Disable the event reminder."`
	Recurrence           string   `enum:"unchanged,none,daily,weekly,monthly,yearly" default:"unchanged" help:"Replace recurrence, clear it with none, or leave it unchanged."`
	RecurrenceInterval   int      `name:"recurrence-interval" default:"1" help:"Replacement recurrence interval."`
	RecurrenceDay        []string `name:"recurrence-day" help:"Replacement weekly weekday; repeat as needed."`
	RecurrenceDayOfMonth int      `name:"recurrence-day-of-month" help:"Replacement monthly or yearly day of month."`
	RecurrenceMonth      string   `name:"recurrence-month" help:"Replacement yearly month."`
	RecurrenceEndDate    string   `name:"recurrence-end-date" help:"Replacement inclusive YYYY-MM-DD recurrence end."`
	RecurrenceCount      int      `name:"recurrence-count" help:"Replacement number of occurrences."`
	ReplaceAttendees     bool     `name:"replace-attendees" help:"Replace both attendee lists, including clearing them when no addresses are supplied."`
	RequiredAttendees    []string `name:"required-attendee" help:"Replacement required attendee address; repeat as needed."`
	OptionalAttendees    []string `name:"optional-attendee" help:"Replacement optional attendee address; repeat as needed."`
	Approve              bool     `help:"Apply the exact preview generated from these arguments."`
	JSON                 bool     `help:"Write the stable machine-readable schema."`
}

type calendarCancelCommand struct {
	Account   string `help:"Configured account alias; defaults to default_account."`
	EventID   string `name:"event-id" help:"Exact event ID returned by calendar list (required)."`
	ChangeKey string `name:"change-key" help:"Exact change key returned with the event ID (required)."`
	Approve   bool   `help:"Cancel the exact event version in the preview."`
	JSON      bool   `help:"Write the stable machine-readable schema."`
}

func (command *calendarFoldersCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
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
	return writeCalendarFolderTable(app, page)
}

func (command *calendarListCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
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
	return writeCalendarTable(app, page)
}

func writeCalendarFolderTable(app *runtime, page application.CalendarFolderPage) error {
	if len(page.Calendars) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No calendars.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tDEFAULT\tEDITABLE\tACCESS\tTIME ZONE\tID"); err != nil {
		return err
	}
	for _, calendar := range page.Calendars {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%t\t%t\t%s\t%s\t%s\n",
			sanitizeCell(calendar.DisplayName, 64),
			calendar.IsDefault,
			calendar.CanEdit,
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
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
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
		return writeCalendarCreateReview(app.stdout, access.Review, false)
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
	if access.Created.OnlineMeetingJoinURL != "" {
		_, err = fmt.Fprintf(
			app.stdout, "Teams join URL: %s\n", sanitizeCell(access.Created.OnlineMeetingJoinURL, 8192),
		)
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Created calendar event %s (change key %s); the network request was attempted once.\n",
		sanitizeCell(access.Created.ID, 4096), sanitizeCell(access.Created.ChangeKey, 4096),
	)
	return err
}

func (command *calendarUpdateCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	if command.ClearSubject && command.Subject != "" {
		return errors.New("subject and clear-subject are mutually exclusive")
	}
	if command.ClearBody && command.BodyFile != "" {
		return errors.New("body-file and clear-body are mutually exclusive")
	}
	if command.ClearLocation && command.Location != "" {
		return errors.New("location and clear-location are mutually exclusive")
	}
	if command.DisableReminder && command.ReminderMinutes != nil {
		return errors.New("reminder-minutes and disable-reminder are mutually exclusive")
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
		return err
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
		return writeCalendarUpdateReview(app.stdout, access.Review, false)
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
		_, err = fmt.Fprintln(app.stdout, "Updated calendar event; list the calendar to refresh its ID and change key.")
		return err
	}
	_, err = fmt.Fprintf(
		app.stdout, "Updated calendar event %s (change key %s); the network request was attempted once.\n",
		sanitizeCell(access.Updated.ID, 4096), sanitizeCell(access.Updated.ChangeKey, 4096),
	)
	return err
}

func (command *calendarCancelCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.CalendarCancelInput{
		Account: accountID, EventID: command.EventID, ChangeKey: command.ChangeKey,
	}
	if err := input.Validate(); err != nil {
		return err
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
		return writeCalendarCancelReview(app.stdout, access.Review, false)
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
		"Cancelled calendar event %s using the reviewed provider semantics; the network request was attempted once.\n",
		sanitizeCell(access.Cancelled.ID, 4096),
	)
	return err
}

func stringValuePointer(value string) *string { return &value }

func (command calendarCreateCommand) calendarRecurrence() (*application.CalendarRecurrence, error) {
	if command.Recurrence == "" || command.Recurrence == "none" {
		if len(command.RecurrenceDay) != 0 || command.RecurrenceDayOfMonth != 0 ||
			command.RecurrenceMonth != "" || command.RecurrenceEndDate != "" || command.RecurrenceCount != 0 {
			return nil, errors.New("recurrence details require a recurrence pattern")
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
			return false, nil, errors.New(
				"recurrence details require a replacement recurrence",
			)
		}
		return false, nil, nil
	case "none":
		if details {
			return false, nil, errors.New(
				"recurrence details cannot be used when clearing recurrence",
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

func writeCalendarCreateReview(
	writer io.Writer,
	review application.CalendarCreateReview,
	committing bool,
) error {
	action := "Preview only; no event was created. Rerun with --approve to create this exact event."
	if committing {
		action = "Committing this exact calendar event now."
	}
	calendar := review.Calendar.ID
	if review.Calendar.Kind == application.CalendarFolderDistinguished {
		calendar = "primary calendar"
	}
	invitations := "no"
	if review.InvitationsWillBeSent {
		invitations = "yes"
	}
	teamsMeeting := "no"
	if review.TeamsMeeting {
		teamsMeeting = "yes"
	}
	allDay := "no"
	if review.AllDay {
		allDay = "yes"
	}
	reminder := "disabled"
	if review.Reminder != nil && review.Reminder.Enabled {
		reminder = fmt.Sprintf("%d minutes before start", review.Reminder.MinutesBeforeStart)
	}
	recurrence := "none"
	if review.Recurrence != nil {
		recurrence = string(review.Recurrence.Pattern)
		if review.Recurrence.EndDate != "" {
			recurrence += " through " + review.Recurrence.EndDate
		} else {
			recurrence += fmt.Sprintf(" for %d occurrences", review.Recurrence.NumberOfOccurrences)
		}
	}
	_, err := fmt.Fprintf(
		writer,
		"%s\nCalendar: %s\nStart: %s\nEnd: %s\nTime zone: %s\nAll day: %s\nReminder: %s\nRecurrence: %s\nLocation: %s\nRequired: %s\nOptional: %s\nInvitations will be sent: %s\nTeams meeting link: %s\nSubject: %s\nBody (%d bytes, SHA-256 %s):\n%s\n",
		action,
		sanitizeCell(calendar, 4096),
		sanitizeCell(review.Start, 64),
		sanitizeCell(review.End, 64),
		sanitizeCell(review.TimeZone, 128),
		allDay,
		reminder,
		sanitizeCell(recurrence, 256),
		sanitizeCell(review.Location, application.MaxCalendarLocationBytes),
		sanitizeCell(joinReviewValues(review.RequiredAttendees), 8192),
		sanitizeCell(joinReviewValues(review.OptionalAttendees), 8192),
		invitations,
		teamsMeeting,
		sanitizeCell(review.Subject, application.MaxCalendarSubjectBytes),
		review.BodyBytes,
		sanitizeCell(review.BodySHA256, 64),
		sanitizeTerminalText(review.BodyPreview),
	)
	return err
}

func writeCalendarUpdateReview(
	writer io.Writer,
	review application.CalendarUpdateReview,
	committing bool,
) error {
	action := "Preview only; no event was updated. Rerun with --approve to apply this exact patch."
	if committing {
		action = "Committing this exact calendar update now."
	}
	if _, err := fmt.Fprintf(
		writer,
		"%s\nEvent ID: %s\nChange key: %s\nMeeting update mode: %s\nChanges:\n",
		action, sanitizeCell(review.EventID, 4096), sanitizeCell(review.ChangeKey, 4096),
		sanitizeCell(review.MeetingUpdateMode, 64),
	); err != nil {
		return err
	}
	if review.Subject != nil {
		if _, err := fmt.Fprintf(writer, "Subject: %s\n", reviewTextChange(*review.Subject)); err != nil {
			return err
		}
	}
	if review.Body != nil {
		bodyPreview := sanitizeTerminalText(review.Body.Preview)
		if review.Body.Bytes == 0 {
			bodyPreview = "(clear)"
		}
		if _, err := fmt.Fprintf(
			writer, "Body (%d bytes, SHA-256 %s):\n%s\n",
			review.Body.Bytes, sanitizeCell(review.Body.SHA256, 64),
			bodyPreview,
		); err != nil {
			return err
		}
	}
	if review.Start != nil {
		if _, err := fmt.Fprintf(
			writer, "Start: %s\nEnd: %s\n",
			sanitizeCell(*review.Start, 64), sanitizeCell(*review.End, 64),
		); err != nil {
			return err
		}
	}
	if review.TimeZone != nil {
		if _, err := fmt.Fprintf(writer, "Time zone: %s\n", sanitizeCell(*review.TimeZone, 128)); err != nil {
			return err
		}
	}
	if review.AllDay != nil {
		if _, err := fmt.Fprintf(writer, "All day: %t\n", *review.AllDay); err != nil {
			return err
		}
	}
	if review.Reminder != nil {
		value := "disabled"
		if review.Reminder.Enabled {
			value = fmt.Sprintf("%d minutes before start", review.Reminder.MinutesBeforeStart)
		}
		if _, err := fmt.Fprintf(writer, "Reminder: %s\n", value); err != nil {
			return err
		}
	}
	if review.ReplaceRecurrence {
		value := "none"
		if review.Recurrence != nil {
			value = string(review.Recurrence.Pattern)
			if review.Recurrence.EndDate != "" {
				value += " through " + review.Recurrence.EndDate
			} else {
				value += fmt.Sprintf(
					" for %d occurrences",
					review.Recurrence.NumberOfOccurrences,
				)
			}
		}
		if _, err := fmt.Fprintf(
			writer,
			"Recurrence: %s\n",
			sanitizeCell(value, 256),
		); err != nil {
			return err
		}
	}
	if review.ReplaceAttendees {
		if _, err := fmt.Fprintf(
			writer, "Required attendees: %s\nOptional attendees: %s\nAttendee updates may send invitations: yes\n",
			sanitizeCell(joinReviewValues(review.RequiredAttendees), 8192),
			sanitizeCell(joinReviewValues(review.OptionalAttendees), 8192),
		); err != nil {
			return err
		}
	}
	if review.Location != nil {
		if _, err := fmt.Fprintf(writer, "Location: %s\n", reviewTextChange(*review.Location)); err != nil {
			return err
		}
	}
	return nil
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
	action := "Preview only; nothing was cancelled. Rerun with --approve to cancel this exact event version."
	if committing {
		action = "Committing this destructive calendar cancellation now."
	}
	attendeeNotifications := "no"
	if review.AttendeeNotificationsMaySend {
		attendeeNotifications = "yes"
	}
	_, err := fmt.Fprintf(
		writer,
		"%s\nEvent ID: %s\nChange key: %s\nProvider disposition: %s\nCancellation mode: %s\nAttendee notifications may be sent: %s\n",
		action, sanitizeCell(review.EventID, 4096), sanitizeCell(review.ChangeKey, 4096),
		sanitizeCell(review.DeleteType, 64), sanitizeCell(review.CancellationMode, 64),
		attendeeNotifications,
	)
	return err
}

func joinReviewValues(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func writeCalendarTable(app *runtime, page application.CalendarPage) error {
	if len(page.Events) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No events.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "START\tEND\tSUBJECT\tLOCATION\tFLAGS\tID\tCHANGE_KEY"); err != nil {
		return err
	}
	for _, event := range page.Events {
		flags := ""
		if event.IsAllDay {
			flags += "A"
		}
		if event.IsOnlineMeeting {
			flags += "O"
		}
		if event.IsCancelled {
			flags += "C"
		}
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
	return writer.Flush()
}
