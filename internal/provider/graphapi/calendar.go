package graphapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type graphDateTimeZone struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

type graphEvent struct {
	ODataETag string            `json:"@odata.etag"`
	ID        string            `json:"id"`
	ChangeKey string            `json:"changeKey"`
	Subject   string            `json:"subject"`
	Body      graphItemBody     `json:"body"`
	Start     graphDateTimeZone `json:"start"`
	End       graphDateTimeZone `json:"end"`
	Location  struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Organizer      graphRecipient `json:"organizer"`
	IsAllDay       bool           `json:"isAllDay"`
	IsOnline       bool           `json:"isOnlineMeeting"`
	IsOrganizer    bool           `json:"isOrganizer"`
	IsCancelled    bool           `json:"isCancelled"`
	ShowAs         string         `json:"showAs"`
	ResponseStatus struct {
		Response string `json:"response"`
	} `json:"responseStatus"`
	Attendees []struct {
		Type         string            `json:"type"`
		EmailAddress graphEmailAddress `json:"emailAddress"`
	} `json:"attendees"`
	OnlineMeetingProvider string `json:"onlineMeetingProvider"`
	OnlineMeeting         struct {
		JoinURL string `json:"joinUrl"`
	} `json:"onlineMeeting"`
}

func (client *Client) ListCalendarEvents(
	ctx context.Context,
	input application.CalendarListInput,
) (application.CalendarPage, error) {
	calendarID, err := decodeCalendar(input.Calendar)
	if err != nil {
		return application.CalendarPage{}, err
	}
	resource := graphCalendarResource(calendarID, "calendarView")
	var response struct {
		NextLink string       `json:"@odata.nextLink"`
		Value    []graphEvent `json:"value"`
	}
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		resource,
		url.Values{
			"startDateTime": {input.Start},
			"endDateTime":   {input.End},
			"$top":          {"1000"},
			"$select": {
				"id,changeKey,subject,start,end,location,organizer,isAllDay," +
					"isOnlineMeeting,isOrganizer,isCancelled,showAs,responseStatus",
			},
			"$orderby": {"start/dateTime"},
		},
		nil,
		&response,
		false,
		http.Header{"Prefer": {`outlook.timezone="UTC"`}},
		http.StatusOK,
	); err != nil {
		return application.CalendarPage{}, err
	}
	if response.NextLink != "" || len(response.Value) > 1000 {
		return application.CalendarPage{}, errors.New(
			"graph calendar window exceeds the bounded 1000-event page",
		)
	}
	page := application.CalendarPage{
		Events: make([]application.CalendarEvent, 0, len(response.Value)),
		Start:  input.Start,
		End:    input.End,
	}
	for _, event := range response.Value {
		view, err := graphEventView(calendarID, event)
		if err != nil {
			return application.CalendarPage{}, err
		}
		page.Events = append(page.Events, view)
	}
	return page, nil
}

func graphEventView(
	calendarID string,
	event graphEvent,
) (application.CalendarEvent, error) {
	if !validGraphID(event.ID) || !validETag(event.ODataETag) {
		return application.CalendarEvent{}, errors.New(
			"graph returned an invalid event identity",
		)
	}
	id, err := encodeEventID(calendarID, event.ID)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	start, err := graphReadTime(event.Start)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	end, err := graphReadTime(event.End)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	response := strings.ToLower(event.ResponseStatus.Response)
	if response == "none" || response == "notresponded" {
		response = "not_responded"
	}
	return application.CalendarEvent{
		ID: id, ChangeKey: encodeETag(event.ODataETag),
		Subject: event.Subject, Start: start, End: end,
		Location: event.Location.DisplayName,
		Organizer: application.MailAddress{
			Name:    event.Organizer.EmailAddress.Name,
			Address: event.Organizer.EmailAddress.Address,
		},
		IsAllDay: event.IsAllDay, IsOnlineMeeting: event.IsOnline,
		IsOrganizer: event.IsOrganizer, IsCancelled: event.IsCancelled,
		MyResponse: response, FreeBusy: graphFreeBusy(event.ShowAs),
	}, nil
}

func graphReadTime(value graphDateTimeZone) (string, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05",
	} {
		parsed, err := time.ParseInLocation(layout, value.DateTime, time.UTC)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errors.New("graph event date-time is malformed")
}

func graphFreeBusy(value string) string {
	switch strings.ToLower(value) {
	case "free":
		return "free"
	case "tentative":
		return "tentative"
	case "oof":
		return "oof"
	case "workingelsewhere":
		return "working_elsewhere"
	default:
		return "busy"
	}
}

func (client *Client) CreateCalendarEvent(
	ctx context.Context,
	input application.CalendarCreateInput,
) (application.CalendarCreateResult, error) {
	calendarID, err := decodeCalendar(input.Calendar)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	request, err := graphCreateEvent(input)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	var created graphEvent
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		graphCalendarResource(calendarID, "events"),
		nil,
		request,
		&created,
		true,
		http.Header{"Prefer": {`outlook.timezone="UTC"`}},
		http.StatusCreated,
	); err != nil {
		return application.CalendarCreateResult{}, err
	}
	if !validGraphID(created.ID) || !validETag(created.ODataETag) {
		return application.CalendarCreateResult{}, fmt.Errorf(
			"%w: graph event create response omitted identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeEventID(calendarID, created.ID)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	joinURL, err := validJoinURL(created.OnlineMeeting.JoinURL)
	if err != nil {
		return application.CalendarCreateResult{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	provider := ""
	if created.IsOnline {
		provider = "teams"
	}
	return application.CalendarCreateResult{
		ID: id, ChangeKey: encodeETag(created.ODataETag),
		IsOnlineMeeting: created.IsOnline, OnlineMeetingProvider: provider,
		OnlineMeetingJoinURL: joinURL,
	}, nil
}

func graphCreateEvent(
	input application.CalendarCreateInput,
) (map[string]any, error) {
	start, err := graphWriteTime(input.Start, input.TimeZone)
	if err != nil {
		return nil, err
	}
	end, err := graphWriteTime(input.End, input.TimeZone)
	if err != nil {
		return nil, err
	}
	request := map[string]any{
		"subject": input.Subject,
		"body": map[string]string{
			"contentType": "text", "content": input.Body,
		},
		"start": start, "end": end,
		"location": map[string]string{"displayName": input.Location},
		"attendees": graphAttendees(
			input.RequiredAttendees,
			input.OptionalAttendees,
		),
		"isAllDay": input.AllDay,
	}
	if input.TeamsMeeting {
		request["isOnlineMeeting"] = true
		request["onlineMeetingProvider"] = "teamsForBusiness"
	}
	if input.Reminder != nil {
		request["isReminderOn"] = input.Reminder.Enabled
		request["reminderMinutesBeforeStart"] = input.Reminder.MinutesBeforeStart
	}
	if input.Recurrence != nil {
		recurrence, err := graphRecurrence(
			*input.Recurrence,
			input.Start,
			input.TimeZone,
		)
		if err != nil {
			return nil, err
		}
		request["recurrence"] = recurrence
	}
	return request, nil
}

func graphWriteTime(value, zone string) (map[string]string, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, errors.New("graph event time must be RFC3339")
	}
	if zone == "" {
		return map[string]string{
			"dateTime": parsed.UTC().Format("2006-01-02T15:04:05"),
			"timeZone": "UTC",
		}, nil
	}
	return map[string]string{
		"dateTime": parsed.Format("2006-01-02T15:04:05"),
		"timeZone": zone,
	}, nil
}

func graphAttendees(required, optional []string) []map[string]any {
	result := make([]map[string]any, 0, len(required)+len(optional))
	for _, address := range required {
		result = append(result, map[string]any{
			"type": "required",
			"emailAddress": map[string]string{
				"address": address,
			},
		})
	}
	for _, address := range optional {
		result = append(result, map[string]any{
			"type": "optional",
			"emailAddress": map[string]string{
				"address": address,
			},
		})
	}
	return result
}

func graphRecurrence(
	recurrence application.CalendarRecurrence,
	start, zone string,
) (map[string]any, error) {
	pattern := map[string]any{"interval": recurrence.Interval}
	switch recurrence.Pattern {
	case application.CalendarRecurrenceDaily:
		pattern["type"] = "daily"
	case application.CalendarRecurrenceWeekly:
		pattern["type"] = "weekly"
		days := make([]string, 0, len(recurrence.DaysOfWeek))
		for _, day := range recurrence.DaysOfWeek {
			days = append(days, strings.ToLower(day))
		}
		pattern["daysOfWeek"] = days
		pattern["firstDayOfWeek"] = "monday"
	case application.CalendarRecurrenceAbsoluteMonthly:
		pattern["type"] = "absoluteMonthly"
		pattern["dayOfMonth"] = recurrence.DayOfMonth
	case application.CalendarRecurrenceAbsoluteYearly:
		pattern["type"] = "absoluteYearly"
		pattern["dayOfMonth"] = recurrence.DayOfMonth
		month, ok := map[string]int{
			"january": 1, "february": 2, "march": 3, "april": 4,
			"may": 5, "june": 6, "july": 7, "august": 8,
			"september": 9, "october": 10, "november": 11, "december": 12,
		}[strings.ToLower(recurrence.Month)]
		if !ok {
			return nil, fmt.Errorf("unsupported recurrence month %q", recurrence.Month)
		}
		pattern["month"] = month
	default:
		return nil, errors.New("unsupported graph recurrence pattern")
	}
	startTime, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return nil, err
	}
	rangeValue := map[string]any{
		"startDate": startTime.Format("2006-01-02"),
	}
	if zone != "" {
		rangeValue["recurrenceTimeZone"] = zone
	}
	if recurrence.NumberOfOccurrences != 0 {
		rangeValue["type"] = "numbered"
		rangeValue["numberOfOccurrences"] = recurrence.NumberOfOccurrences
	} else {
		rangeValue["type"] = "endDate"
		rangeValue["endDate"] = recurrence.EndDate
	}
	return map[string]any{"pattern": pattern, "range": rangeValue}, nil
}

func (client *Client) UpdateCalendarEvent(
	ctx context.Context,
	input application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	reference, existing, etag, err := client.exactEvent(
		ctx,
		input.EventID,
		input.ChangeKey,
	)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if input.Body != nil && existing.IsOnline {
		return application.CalendarUpdateResult{}, errors.New(
			"graph online-meeting bodies cannot be replaced without losing the meeting blob",
		)
	}
	patch := make(map[string]any)
	if input.Subject != nil {
		patch["subject"] = *input.Subject
	}
	if input.Body != nil {
		patch["body"] = map[string]string{
			"contentType": "text", "content": *input.Body,
		}
	}
	if input.Location != nil {
		patch["location"] = map[string]string{"displayName": *input.Location}
	}
	if input.Start != nil {
		zone := ""
		if input.TimeZone != nil {
			zone = *input.TimeZone
		}
		start, err := graphWriteTime(*input.Start, zone)
		if err != nil {
			return application.CalendarUpdateResult{}, err
		}
		end, err := graphWriteTime(*input.End, zone)
		if err != nil {
			return application.CalendarUpdateResult{}, err
		}
		patch["start"] = start
		patch["end"] = end
	}
	if input.AllDay != nil {
		patch["isAllDay"] = *input.AllDay
	}
	if input.Reminder != nil {
		patch["isReminderOn"] = input.Reminder.Enabled
		patch["reminderMinutesBeforeStart"] = input.Reminder.MinutesBeforeStart
	}
	if input.ReplaceAttendees {
		patch["attendees"] = graphAttendees(
			input.RequiredAttendees,
			input.OptionalAttendees,
		)
	}
	var updated graphEvent
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPatch,
		graphEventResource(reference),
		nil,
		patch,
		&updated,
		true,
		http.Header{
			"If-Match": {etag},
			"Prefer":   {`outlook.timezone="UTC"`},
		},
		http.StatusOK,
	); err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if updated.ID != reference.Event || !validETag(updated.ODataETag) {
		return application.CalendarUpdateResult{}, fmt.Errorf(
			"%w: graph event update response omitted identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.CalendarUpdateResult{
		ID: input.EventID, ChangeKey: encodeETag(updated.ODataETag),
	}, nil
}

func (client *Client) CancelCalendarEvent(
	ctx context.Context,
	input application.CalendarCancelInput,
) error {
	reference, _, etag, err := client.exactEvent(
		ctx,
		input.EventID,
		input.ChangeKey,
	)
	if err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx,
		http.MethodDelete,
		graphEventResource(reference),
		nil,
		nil,
		nil,
		true,
		http.Header{"If-Match": {etag}},
		http.StatusNoContent,
	)
	return err
}

func (client *Client) exactEvent(
	ctx context.Context,
	eventID, changeKey string,
) (graphEventReference, graphEvent, string, error) {
	reference, err := decodeEventID(eventID)
	if err != nil {
		return graphEventReference{}, graphEvent{}, "", err
	}
	etag, err := decodeETag(changeKey)
	if err != nil {
		return graphEventReference{}, graphEvent{}, "", err
	}
	var event graphEvent
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		graphEventResource(reference),
		url.Values{
			"$select": {
				"id,changeKey,body,isOnlineMeeting,attendees,start,end",
			},
		},
		nil,
		&event,
		false,
		http.Header{"Prefer": {`outlook.timezone="UTC"`}},
		http.StatusOK,
	); err != nil {
		return graphEventReference{}, graphEvent{}, "", err
	}
	if event.ID != reference.Event || event.ODataETag != etag {
		return graphEventReference{}, graphEvent{}, "", errors.New(
			"graph event changed before write",
		)
	}
	return reference, event, etag, nil
}

func decodeCalendar(folder application.CalendarFolder) (string, error) {
	if folder.Kind == application.CalendarFolderDistinguished {
		return "primary", nil
	}
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(folder.ID, "mgc1_", &reference); err != nil ||
		!validGraphID(reference.ID) {
		return "", errors.New("calendar ID is not a Graph identifier")
	}
	return reference.ID, nil
}

func graphCalendarResource(calendarID, child string) string {
	if calendarID == "primary" {
		return "me/" + child
	}
	return "me/calendars/" + escaped(calendarID) + "/" + child
}

func graphEventResource(reference graphEventReference) string {
	return graphCalendarResource(
		reference.Calendar,
		"events/"+escaped(reference.Event),
	)
}

func validJoinURL(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" ||
		len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("graph returned a malformed online-meeting join URL")
	}
	return value, nil
}
