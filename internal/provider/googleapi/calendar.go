package googleapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type googleEventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type googleEventPerson struct {
	Email          string `json:"email"`
	DisplayName    string `json:"displayName,omitempty"`
	Self           bool   `json:"self,omitempty"`
	Optional       bool   `json:"optional,omitempty"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

type googleEvent struct {
	ID           string              `json:"id"`
	ETag         string              `json:"etag"`
	Status       string              `json:"status"`
	Summary      string              `json:"summary"`
	Description  string              `json:"description"`
	Location     string              `json:"location"`
	Start        googleEventTime     `json:"start"`
	End          googleEventTime     `json:"end"`
	Organizer    googleEventPerson   `json:"organizer"`
	Attendees    []googleEventPerson `json:"attendees,omitempty"`
	Transparency string              `json:"transparency,omitempty"`
	Recurrence   []string            `json:"recurrence,omitempty"`
	Reminders    struct {
		UseDefault bool `json:"useDefault"`
		Overrides  []struct {
			Method  string `json:"method"`
			Minutes int    `json:"minutes"`
		} `json:"overrides,omitempty"`
	} `json:"reminders,omitempty"`
	HangoutLink    string `json:"hangoutLink,omitempty"`
	ConferenceData struct {
		EntryPoints []struct {
			EntryPointType string `json:"entryPointType"`
			URI            string `json:"uri"`
		} `json:"entryPoints"`
	} `json:"conferenceData,omitempty"`
}

type googleCalendarListEntry struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	SummaryOverride string `json:"summaryOverride"`
	TimeZone        string `json:"timeZone"`
	AccessRole      string `json:"accessRole"`
	Primary         bool   `json:"primary"`
	Deleted         bool   `json:"deleted"`
}

func (client *Client) ListCalendarFolders(
	ctx context.Context,
	input application.CalendarFolderListInput,
) (application.CalendarFolderPage, error) {
	const maximumPages = 42
	entries := make([]googleCalendarListEntry, 0, input.Limit)
	remainingOffset := input.Offset
	pageToken := ""
	totalSeen := 0
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber >= maximumPages {
			return application.CalendarFolderPage{}, errors.New(
				"google Calendar pagination exceeded the bounded offset window",
			)
		}
		requestSize := 250
		query := url.Values{
			"maxResults":  {strconv.Itoa(requestSize)},
			"showDeleted": {"false"},
			"showHidden":  {"true"},
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var response struct {
			Items         []googleCalendarListEntry `json:"items"`
			NextPageToken string                    `json:"nextPageToken"`
		}
		if _, err := client.api.DoJSON(
			ctx,
			http.MethodGet,
			"calendar/v3/users/me/calendarList",
			query,
			nil,
			&response,
			false,
			nil,
			http.StatusOK,
		); err != nil {
			return application.CalendarFolderPage{}, err
		}
		totalSeen += len(response.Items)
		if totalSeen >
			application.MaxCalendarFolderOffset+application.MaxCalendarFolderPageSize {
			return application.CalendarFolderPage{}, errors.New(
				"google Calendar collection exceeds the bounded offset window",
			)
		}
		start := min(remainingOffset, len(response.Items))
		remainingOffset -= start
		if len(entries) < input.Limit {
			available := response.Items[start:]
			take := min(input.Limit-len(entries), len(available))
			entries = append(entries, available[:take]...)
		}
		if response.NextPageToken == "" {
			break
		}
		if len(response.NextPageToken) > 8192 ||
			strings.ContainsAny(response.NextPageToken, "\r\n\x00") ||
			response.NextPageToken == pageToken {
			return application.CalendarFolderPage{}, errors.New(
				"google Calendar returned an invalid pagination token",
			)
		}
		pageToken = response.NextPageToken
	}
	result := application.CalendarFolderPage{
		Calendars:        make([]application.CalendarFolderSummary, 0, len(entries)),
		TotalCalendars:   totalSeen,
		IncludesLastItem: input.Offset+len(entries) >= totalSeen,
	}
	for _, entry := range entries {
		if !validGoogleID(entry.ID) || entry.Deleted {
			return application.CalendarFolderPage{}, errors.New(
				"google Calendar returned an invalid calendar identity",
			)
		}
		id, err := encodeReference("ggc1_", struct {
			ID string `json:"id"`
		}{ID: entry.ID})
		if err != nil {
			return application.CalendarFolderPage{}, err
		}
		name := entry.SummaryOverride
		if name == "" {
			name = entry.Summary
		}
		role := googleCalendarAccessRole(entry.AccessRole)
		result.Calendars = append(
			result.Calendars,
			application.CalendarFolderSummary{
				ID: id, DisplayName: name, IsDefault: entry.Primary,
				CanEdit:    role == "owner" || role == "writer",
				AccessRole: role, TimeZone: entry.TimeZone,
			},
		)
	}
	return result, nil
}

func googleCalendarAccessRole(value string) string {
	switch value {
	case "owner":
		return "owner"
	case "writer":
		return "writer"
	case "writerWithoutPrivateAccess":
		return "writer"
	case "reader":
		return "reader"
	case "freeBusyReader":
		return "free_busy"
	default:
		return "unknown"
	}
}

func (client *Client) ListCalendarEvents(
	ctx context.Context,
	input application.CalendarListInput,
) (application.CalendarPage, error) {
	calendarID, err := client.calendarID(input.Calendar)
	if err != nil {
		return application.CalendarPage{}, err
	}
	query := url.Values{
		"timeMin":      {input.Start},
		"timeMax":      {input.End},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {"2500"},
		"showDeleted":  {"false"},
	}
	var response struct {
		Items         []googleEvent `json:"items"`
		NextPageToken string        `json:"nextPageToken"`
	}
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet,
		"calendar/v3/calendars/"+escaped(calendarID)+"/events",
		query, nil, &response, false, nil, http.StatusOK,
	); err != nil {
		return application.CalendarPage{}, err
	}
	if response.NextPageToken != "" {
		return application.CalendarPage{}, errors.New(
			"google Calendar window exceeds the bounded 2500-event page",
		)
	}
	page := application.CalendarPage{
		Events: make([]application.CalendarEvent, 0, len(response.Items)),
		Start:  input.Start, End: input.End,
	}
	for _, event := range response.Items {
		view, err := client.googleEventView(calendarID, event)
		if err != nil {
			return application.CalendarPage{}, err
		}
		page.Events = append(page.Events, view)
	}
	return page, nil
}

func (client *Client) googleEventView(
	calendarID string,
	event googleEvent,
) (application.CalendarEvent, error) {
	if !validGoogleID(event.ID) || !validETag(event.ETag) {
		return application.CalendarEvent{}, errors.New(
			"google Calendar returned an invalid event identity",
		)
	}
	id, err := encodeEventID(calendarID, event.ID)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	start, allDay, err := googleEventTimeValue(event.Start)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	end, _, err := googleEventTimeValue(event.End)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	response := ""
	for _, attendee := range event.Attendees {
		if attendee.Self {
			response = googleResponse(attendee.ResponseStatus)
			break
		}
	}
	freeBusy := "busy"
	if event.Transparency == "transparent" {
		freeBusy = "free"
	}
	return application.CalendarEvent{
		ID: id, ChangeKey: encodeETag(event.ETag), Subject: event.Summary,
		Start: start, End: end, Location: event.Location,
		OriginalStart:         googleOriginalEventTime(event.Start),
		OriginalEnd:           googleOriginalEventTime(event.End),
		OriginalStartTimeZone: event.Start.TimeZone,
		OriginalEndTimeZone:   event.End.TimeZone,
		Organizer: application.MailAddress{
			Name: event.Organizer.DisplayName, Address: event.Organizer.Email,
		},
		IsAllDay: allDay, IsOnlineMeeting: event.HangoutLink != "" ||
			len(event.ConferenceData.EntryPoints) != 0,
		IsOrganizer: event.Organizer.Self,
		IsCancelled: event.Status == "cancelled",
		MyResponse:  response, FreeBusy: freeBusy,
	}, nil
}

func googleOriginalEventTime(value googleEventTime) string {
	if value.Date != "" {
		return value.Date
	}
	return value.DateTime
}

func googleEventTimeValue(value googleEventTime) (string, bool, error) {
	if value.Date != "" {
		date, err := time.Parse("2006-01-02", value.Date)
		if err != nil {
			return "", false, errors.New("google all-day event date is malformed")
		}
		return date.UTC().Format(time.RFC3339), true, nil
	}
	parsed, err := time.Parse(time.RFC3339, value.DateTime)
	if err != nil {
		return "", false, errors.New("google event date-time is malformed")
	}
	return parsed.UTC().Format(time.RFC3339), false, nil
}

func googleResponse(value string) string {
	switch value {
	case "accepted":
		return "accepted"
	case "declined":
		return "declined"
	case "tentative":
		return "tentative"
	case "needsAction":
		return "not_responded"
	default:
		return ""
	}
}

func (client *Client) CreateCalendarEvent(
	ctx context.Context,
	input application.CalendarCreateInput,
) (application.CalendarCreateResult, error) {
	if input.TeamsMeeting {
		return application.CalendarCreateResult{}, errors.New(
			"google Calendar cannot provision a Teams meeting",
		)
	}
	calendarID, err := client.calendarID(input.Calendar)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	event, err := googleCreateEvent(input)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	query := url.Values{}
	if len(input.RequiredAttendees)+len(input.OptionalAttendees) != 0 {
		query.Set("sendUpdates", "all")
	}
	var created googleEvent
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost,
		"calendar/v3/calendars/"+escaped(calendarID)+"/events",
		query, event, &created, true, nil, http.StatusOK,
	); err != nil {
		return application.CalendarCreateResult{}, err
	}
	if !validGoogleID(created.ID) || !validETag(created.ETag) {
		return application.CalendarCreateResult{}, fmt.Errorf(
			"%w: Google Calendar create response omitted identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeEventID(calendarID, created.ID)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	return application.CalendarCreateResult{
		ID: id, ChangeKey: encodeETag(created.ETag),
		IsOnlineMeeting: created.HangoutLink != "" ||
			len(created.ConferenceData.EntryPoints) != 0,
		OnlineMeetingProvider: func() string {
			if created.HangoutLink != "" || len(created.ConferenceData.EntryPoints) != 0 {
				return "google-meet"
			}
			return ""
		}(),
	}, nil
}

func googleCreateEvent(
	input application.CalendarCreateInput,
) (map[string]any, error) {
	event := map[string]any{
		"summary": input.Subject, "description": input.Body, "location": input.Location,
		"start": googleWriteTime(input.Start, input.TimeZone, input.AllDay),
		"end":   googleWriteTime(input.End, input.TimeZone, input.AllDay),
	}
	attendees := googleAttendees(input.RequiredAttendees, input.OptionalAttendees)
	if len(attendees) != 0 {
		event["attendees"] = attendees
	}
	if input.Reminder != nil {
		event["reminders"] = googleReminders(*input.Reminder)
	}
	if input.Recurrence != nil {
		rule, err := googleRecurrence(*input.Recurrence)
		if err != nil {
			return nil, err
		}
		event["recurrence"] = []string{"RRULE:" + rule}
	}
	return event, nil
}

func googleWriteTime(value, zone string, allDay bool) map[string]any {
	parsed, _ := time.Parse(time.RFC3339, value)
	if allDay {
		return map[string]any{"date": parsed.Format("2006-01-02")}
	}
	result := map[string]any{"dateTime": value}
	if zone != "" {
		result["timeZone"] = zone
	}
	return result
}

func googleAttendees(required, optional []string) []map[string]any {
	result := make([]map[string]any, 0, len(required)+len(optional))
	for _, address := range required {
		result = append(result, map[string]any{"email": address})
	}
	for _, address := range optional {
		result = append(result, map[string]any{"email": address, "optional": true})
	}
	return result
}

func googleReminders(reminder application.CalendarReminder) map[string]any {
	result := map[string]any{"useDefault": false, "overrides": []map[string]any{}}
	if reminder.Enabled {
		result["overrides"] = []map[string]any{{
			"method": "popup", "minutes": reminder.MinutesBeforeStart,
		}}
	}
	return result
}

func (client *Client) UpdateCalendarEvent(
	ctx context.Context,
	input application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	reference, existing, etag, err := client.exactGoogleEvent(
		ctx, input.EventID, input.ChangeKey,
	)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	patch := make(map[string]any)
	if input.Subject != nil {
		patch["summary"] = *input.Subject
	}
	if input.Body != nil {
		patch["description"] = *input.Body
	}
	if input.Location != nil {
		patch["location"] = *input.Location
	}
	allDay := existing.Start.Date != ""
	if input.AllDay != nil {
		allDay = *input.AllDay
	}
	if input.Start != nil {
		zone := ""
		if input.TimeZone != nil {
			zone = *input.TimeZone
		}
		patch["start"] = googleWriteTime(*input.Start, zone, allDay)
		patch["end"] = googleWriteTime(*input.End, zone, allDay)
	} else if existing.Start.Date != "" && !allDay {
		patch["start"] = map[string]any{
			"dateTime": existing.Start.Date + "T00:00:00Z",
		}
		patch["end"] = map[string]any{
			"dateTime": existing.End.Date + "T00:00:00Z",
		}
	}
	if input.ReplaceAttendees {
		patch["attendees"] = googleAttendees(
			input.RequiredAttendees, input.OptionalAttendees,
		)
	}
	if input.Reminder != nil {
		patch["reminders"] = googleReminders(*input.Reminder)
	}
	if input.ReplaceRecurrence {
		recurrence := []string{}
		if input.Recurrence != nil {
			rule, err := googleRecurrence(*input.Recurrence)
			if err != nil {
				return application.CalendarUpdateResult{}, err
			}
			recurrence = append(recurrence, "RRULE:"+rule)
		}
		patch["recurrence"] = recurrence
	}
	headers := http.Header{"If-Match": []string{etag}}
	query := url.Values{}
	if input.ReplaceAttendees || len(existing.Attendees) != 0 {
		query.Set("sendUpdates", "all")
	}
	var updated googleEvent
	if _, err := client.api.DoJSON(
		ctx, http.MethodPatch,
		"calendar/v3/calendars/"+escaped(reference.Calendar)+
			"/events/"+escaped(reference.Event),
		query, patch, &updated, true, headers, http.StatusOK,
	); err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if !validETag(updated.ETag) {
		return application.CalendarUpdateResult{}, fmt.Errorf(
			"%w: Google Calendar update response omitted ETag",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.CalendarUpdateResult{
		ID: input.EventID, ChangeKey: encodeETag(updated.ETag),
	}, nil
}

func (client *Client) CancelCalendarEvent(
	ctx context.Context,
	input application.CalendarCancelInput,
) error {
	reference, _, etag, err := client.exactGoogleEvent(
		ctx, input.EventID, input.ChangeKey,
	)
	if err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx, http.MethodDelete,
		"calendar/v3/calendars/"+escaped(reference.Calendar)+
			"/events/"+escaped(reference.Event),
		url.Values{"sendUpdates": {"all"}}, nil, nil, true,
		http.Header{"If-Match": []string{etag}},
		http.StatusNoContent,
	)
	return err
}

func (client *Client) exactGoogleEvent(
	ctx context.Context,
	id, changeKey string,
) (googleCalendarReference, googleEvent, string, error) {
	reference, err := decodeEventID(id)
	if err != nil {
		return googleCalendarReference{}, googleEvent{}, "", err
	}
	etag, err := decodeETag(changeKey)
	if err != nil {
		return googleCalendarReference{}, googleEvent{}, "", err
	}
	var event googleEvent
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet,
		"calendar/v3/calendars/"+escaped(reference.Calendar)+
			"/events/"+escaped(reference.Event),
		nil, nil, &event, false, nil, http.StatusOK,
	); err != nil {
		return googleCalendarReference{}, googleEvent{}, "", err
	}
	if event.ID != reference.Event || event.ETag != etag {
		return googleCalendarReference{}, googleEvent{}, "", errors.New(
			"google Calendar event changed before write",
		)
	}
	return reference, event, etag, nil
}

func (client *Client) calendarID(
	folder application.CalendarFolder,
) (string, error) {
	if folder.Kind == application.CalendarFolderDistinguished {
		return "primary", nil
	}
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(folder.ID, "ggc1_", &reference); err != nil ||
		!validGoogleID(reference.ID) {
		return "", errors.New("calendar ID is not a Google identifier")
	}
	return reference.ID, nil
}

func googleRecurrence(recurrence application.CalendarRecurrence) (string, error) {
	parts := []string{}
	switch recurrence.Pattern {
	case application.CalendarRecurrenceDaily:
		parts = append(parts, "FREQ=DAILY")
	case application.CalendarRecurrenceWeekly:
		parts = append(parts, "FREQ=WEEKLY")
		days := make([]string, 0, len(recurrence.DaysOfWeek))
		for _, day := range recurrence.DaysOfWeek {
			value, ok := map[string]string{
				"monday": "MO", "tuesday": "TU", "wednesday": "WE",
				"thursday": "TH", "friday": "FR", "saturday": "SA", "sunday": "SU",
			}[strings.ToLower(day)]
			if !ok {
				return "", fmt.Errorf("unsupported recurrence weekday %q", day)
			}
			days = append(days, value)
		}
		parts = append(parts, "BYDAY="+strings.Join(days, ","))
	case application.CalendarRecurrenceAbsoluteMonthly:
		parts = append(parts, "FREQ=MONTHLY",
			fmt.Sprintf("BYMONTHDAY=%d", recurrence.DayOfMonth))
	case application.CalendarRecurrenceAbsoluteYearly:
		month, ok := map[string]int{
			"january": 1, "february": 2, "march": 3, "april": 4,
			"may": 5, "june": 6, "july": 7, "august": 8,
			"september": 9, "october": 10, "november": 11, "december": 12,
		}[strings.ToLower(recurrence.Month)]
		if !ok {
			return "", fmt.Errorf("unsupported recurrence month %q", recurrence.Month)
		}
		parts = append(parts, "FREQ=YEARLY",
			fmt.Sprintf("BYMONTH=%d", month),
			fmt.Sprintf("BYMONTHDAY=%d", recurrence.DayOfMonth))
	default:
		return "", errors.New("unsupported Google Calendar recurrence")
	}
	if recurrence.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", recurrence.Interval))
	}
	if recurrence.NumberOfOccurrences > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", recurrence.NumberOfOccurrences))
	} else {
		end, err := time.Parse("2006-01-02", recurrence.EndDate)
		if err != nil {
			return "", err
		}
		parts = append(parts, "UNTIL="+end.UTC().Format("20060102T235959Z"))
	}
	return strings.Join(parts, ";"), nil
}
