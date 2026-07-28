package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) ListCalendarEvents(
	ctx context.Context,
	input application.CalendarListInput,
) (application.CalendarPage, error) {
	calendarPath, err := client.calendarFor(input.Calendar)
	if err != nil {
		return application.CalendarPage{}, err
	}
	start, _ := time.Parse(time.RFC3339, input.Start)
	end, _ := time.Parse(time.RFC3339, input.End)
	objects, err := client.dav.QueryCalendar(ctx, calendarPath, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name: ical.CompCalendar,
			Comps: []caldav.CalendarCompRequest{{
				Name: ical.CompEvent, AllProps: true,
			}},
			Expand: &caldav.CalendarExpandRequest{Start: start, End: end},
		},
		CompFilter: caldav.CompFilter{
			Name: ical.CompCalendar,
			Comps: []caldav.CompFilter{{
				Name: ical.CompEvent, Start: start, End: end,
			}},
		},
	})
	if err != nil {
		return application.CalendarPage{}, err
	}
	page := application.CalendarPage{
		Events: make([]application.CalendarEvent, 0, len(objects)),
		Start:  start.UTC().Format(time.RFC3339), End: end.UTC().Format(time.RFC3339),
	}
	for _, object := range objects {
		if !validDAVPath(object.Path) || !pathWithin(object.Path, calendarPath) {
			return application.CalendarPage{}, errors.New(
				"CalDAV server returned an object outside the selected calendar",
			)
		}
		if !validObjectETag(object.ETag) {
			return application.CalendarPage{}, errors.New("CalDAV event has no strong ETag")
		}
		events := object.Data.Events()
		if len(events) != 1 {
			return application.CalendarPage{}, errors.New(
				"CalDAV object must contain exactly one expanded event",
			)
		}
		event, err := client.eventView(calendarPath, object.Path, object.ETag, events[0])
		if err != nil {
			return application.CalendarPage{}, err
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}

func (client *Client) eventView(
	calendarPath, objectPath, etag string,
	event ical.Event,
) (application.CalendarEvent, error) {
	if event.Props.Get(ical.PropRecurrenceRule) != nil {
		return application.CalendarEvent{}, errors.New(
			"CalDAV server did not expand a recurring event in the requested window",
		)
	}
	uid, err := event.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return application.CalendarEvent{}, errors.New("CalDAV event UID is missing")
	}
	id, err := encodeEventID(eventReference{
		Calendar: calendarPath, Path: objectPath, UID: uid,
	})
	if err != nil {
		return application.CalendarEvent{}, err
	}
	start, err := event.DateTimeStart(time.UTC)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	end, err := event.DateTimeEnd(time.UTC)
	if err != nil {
		return application.CalendarEvent{}, err
	}
	subject, _ := event.Props.Text(ical.PropSummary)
	location, _ := event.Props.Text(ical.PropLocation)
	status, _ := event.Status()
	organizer := calendarAddress(event.Props.Get(ical.PropOrganizer))
	response := attendeeResponse(event.Props.Values(ical.PropAttendee), client.username)
	transparency, _ := event.Props.Text(ical.PropTransparency)
	freeBusy := "busy"
	if strings.EqualFold(transparency, "TRANSPARENT") {
		freeBusy = "free"
	}
	startProp := event.Props.Get(ical.PropDateTimeStart)
	return application.CalendarEvent{
		ID: id, ChangeKey: etag, Subject: subject,
		Start: start.UTC().Format(time.RFC3339), End: end.UTC().Format(time.RFC3339),
		Location: location,
		Organizer: application.MailAddress{
			Name: organizer.name, Address: organizer.address,
		},
		IsAllDay:    startProp != nil && startProp.ValueType() == ical.ValueDate,
		IsOrganizer: strings.EqualFold(organizer.address, client.username),
		IsCancelled: status == ical.EventCancelled,
		MyResponse:  response, FreeBusy: freeBusy,
	}, nil
}

type namedAddress struct {
	name, address string
}

func calendarAddress(property *ical.Prop) namedAddress {
	if property == nil {
		return namedAddress{}
	}
	raw := property.Value
	if parsed, err := url.Parse(raw); err == nil &&
		strings.EqualFold(parsed.Scheme, "mailto") {
		raw = parsed.Opaque
	}
	return namedAddress{
		name: property.Params.Get(ical.ParamCommonName), address: raw,
	}
}

func attendeeResponse(properties []ical.Prop, identity string) string {
	for index := range properties {
		address := calendarAddress(&properties[index])
		if !strings.EqualFold(address.address, identity) {
			continue
		}
		switch strings.ToUpper(properties[index].Params.Get(ical.ParamParticipationStatus)) {
		case "ACCEPTED":
			return "accepted"
		case "DECLINED":
			return "declined"
		case "TENTATIVE":
			return "tentative"
		default:
			return "not_responded"
		}
	}
	return ""
}

func (client *Client) CreateCalendarEvent(
	ctx context.Context,
	input application.CalendarCreateInput,
) (application.CalendarCreateResult, error) {
	if input.TeamsMeeting {
		return application.CalendarCreateResult{}, errors.New(
			"CalDAV cannot provision a Teams meeting",
		)
	}
	calendarPath, err := client.calendarFor(input.Calendar)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	uid, err := newUID()
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	calendar, err := client.newCalendar(uid, input)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	objectPath := strings.TrimSuffix(calendarPath, "/") + "/" +
		url.PathEscape(uid) + "." + ical.Extension
	etag, err := client.conditionalRequest(
		ctx, http.MethodPut, objectPath, "If-None-Match", "*", calendar,
	)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	if etag == "" {
		object, err := client.dav.GetCalendarObject(ctx, objectPath)
		if err != nil {
			return application.CalendarCreateResult{}, fmt.Errorf(
				"%w: retrieve created CalDAV event: %w",
				application.ErrWriteOutcomeUnknown,
				err,
			)
		}
		etag = object.ETag
	}
	if !validObjectETag(etag) {
		return application.CalendarCreateResult{}, fmt.Errorf(
			"%w: created CalDAV event has no strong ETag",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeEventID(eventReference{
		Calendar: calendarPath, Path: objectPath, UID: uid,
	})
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	return application.CalendarCreateResult{ID: id, ChangeKey: etag}, nil
}

func (client *Client) newCalendar(
	uid string,
	input application.CalendarCreateInput,
) (*ical.Calendar, error) {
	start, _ := time.Parse(time.RFC3339, input.Start)
	end, _ := time.Parse(time.RFC3339, input.End)
	start, end, err := calDAVEventTimes(start, end, input.TimeZone)
	if err != nil {
		return nil, err
	}
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if input.AllDay {
		event.Props.SetDate(ical.PropDateTimeStart, start)
		event.Props.SetDate(ical.PropDateTimeEnd, end)
	} else {
		event.Props.SetDateTime(ical.PropDateTimeStart, start)
		event.Props.SetDateTime(ical.PropDateTimeEnd, end)
	}
	event.Props.SetText(ical.PropSummary, input.Subject)
	event.Props.SetText(ical.PropDescription, input.Body)
	event.Props.SetText(ical.PropLocation, input.Location)
	if bareCalendarAddress(client.username) {
		event.Props.Set(calendarAddressProp(
			ical.PropOrganizer,
			client.username,
			"",
			"",
		))
	}
	setAttendees(event, input.RequiredAttendees, input.OptionalAttendees)
	if input.Reminder != nil && input.Reminder.Enabled {
		addReminder(event, input.Reminder.MinutesBeforeStart)
	}
	if input.Recurrence != nil {
		rule, err := recurrenceRule(*input.Recurrence)
		if err != nil {
			return nil, err
		}
		property := ical.NewProp(ical.PropRecurrenceRule)
		property.SetValueType(ical.ValueRecurrence)
		property.Value = rule
		event.Props.Set(property)
	}
	calendar := ical.NewCalendar()
	calendar.Props.SetText(ical.PropProductID, "-//Corresync//EN")
	calendar.Props.SetText(ical.PropVersion, "2.0")
	calendar.Children = append(calendar.Children, event.Component)
	return calendar, nil
}

func (client *Client) UpdateCalendarEvent(
	ctx context.Context,
	input application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	reference, object, event, err := client.exactEvent(ctx, input.EventID, input.ChangeKey)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if input.Subject != nil {
		event.Props.SetText(ical.PropSummary, *input.Subject)
	}
	if input.Body != nil {
		event.Props.SetText(ical.PropDescription, *input.Body)
	}
	if input.Location != nil {
		event.Props.SetText(ical.PropLocation, *input.Location)
	}
	startProperty := event.Props.Get(ical.PropDateTimeStart)
	wasAllDay := startProperty != nil && startProperty.ValueType() == ical.ValueDate
	allDay := wasAllDay
	if input.AllDay != nil {
		allDay = *input.AllDay
	}
	if input.Start != nil {
		start, _ := time.Parse(time.RFC3339, *input.Start)
		end, _ := time.Parse(time.RFC3339, *input.End)
		zone := ""
		if input.TimeZone != nil {
			zone = *input.TimeZone
		}
		start, end, err = calDAVEventTimes(start, end, zone)
		if err != nil {
			return application.CalendarUpdateResult{}, err
		}
		if allDay {
			event.Props.SetDate(ical.PropDateTimeStart, start)
			event.Props.SetDate(ical.PropDateTimeEnd, end)
		} else {
			event.Props.SetDateTime(ical.PropDateTimeStart, start)
			event.Props.SetDateTime(ical.PropDateTimeEnd, end)
		}
	} else if wasAllDay && !allDay {
		start, startErr := event.DateTimeStart(time.UTC)
		end, endErr := event.DateTimeEnd(time.UTC)
		if startErr != nil || endErr != nil {
			return application.CalendarUpdateResult{}, errors.Join(startErr, endErr)
		}
		event.Props.SetDateTime(ical.PropDateTimeStart, start.UTC())
		event.Props.SetDateTime(ical.PropDateTimeEnd, end.UTC())
	}
	if input.ReplaceAttendees {
		setAttendees(&event, input.RequiredAttendees, input.OptionalAttendees)
	}
	if input.Reminder != nil {
		removeAlarms(&event)
		if input.Reminder.Enabled {
			addReminder(&event, input.Reminder.MinutesBeforeStart)
		}
	}
	event.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
	etag, err := client.conditionalRequest(
		ctx,
		http.MethodPut,
		reference.Path,
		"If-Match",
		strconv.Quote(input.ChangeKey),
		object.Data,
	)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if etag == "" {
		return application.CalendarUpdateResult{}, fmt.Errorf(
			"%w: updated CalDAV event has no strong ETag",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.CalendarUpdateResult{
		ID: input.EventID, ChangeKey: etag,
	}, nil
}

func (client *Client) CancelCalendarEvent(
	ctx context.Context,
	input application.CalendarCancelInput,
) error {
	reference, _, event, err := client.exactEvent(ctx, input.EventID, input.ChangeKey)
	if err != nil {
		return err
	}
	if len(event.Props.Values(ical.PropAttendee)) != 0 {
		return errors.New(
			"CalDAV scheduling cancellation is unavailable; attendee event was not deleted",
		)
	}
	_, err = client.conditionalRequest(
		ctx,
		http.MethodDelete,
		reference.Path,
		"If-Match",
		strconv.Quote(input.ChangeKey),
		nil,
	)
	return err
}

func (client *Client) exactEvent(
	ctx context.Context,
	id, etag string,
) (eventReference, *caldav.CalendarObject, ical.Event, error) {
	reference, err := decodeEventID(id)
	if err != nil {
		return eventReference{}, nil, ical.Event{}, err
	}
	if reference.Calendar != client.calendarPath {
		return eventReference{}, nil, ical.Event{}, errors.New(
			"CalDAV event belongs to a different calendar",
		)
	}
	object, err := client.dav.GetCalendarObject(ctx, reference.Path)
	if err != nil {
		return eventReference{}, nil, ical.Event{}, err
	}
	if object.ETag != etag || !validObjectETag(object.ETag) {
		return eventReference{}, nil, ical.Event{}, errors.New(
			"CalDAV event changed before write",
		)
	}
	events := object.Data.Events()
	if len(events) != 1 {
		return eventReference{}, nil, ical.Event{}, errors.New(
			"CalDAV object does not map to one writable event",
		)
	}
	uid, _ := events[0].Props.Text(ical.PropUID)
	if uid != reference.UID {
		return eventReference{}, nil, ical.Event{}, errors.New("CalDAV event UID changed")
	}
	return reference, object, events[0], nil
}

func calendarAddressProp(name, address, role, commonName string) *ical.Prop {
	property := ical.NewProp(name)
	property.SetValueType(ical.ValueCalendarAddress)
	property.Value = "mailto:" + address
	if role != "" {
		property.Params.Set(ical.ParamRole, role)
	}
	if commonName != "" {
		property.Params.Set(ical.ParamCommonName, commonName)
	}
	if name == ical.PropAttendee {
		property.Params.Set(ical.ParamRSVP, "TRUE")
		property.Params.Set(ical.ParamParticipationStatus, "NEEDS-ACTION")
	}
	return property
}

func setAttendees(event *ical.Event, required, optional []string) {
	event.Props.Del(ical.PropAttendee)
	for _, address := range required {
		event.Props.Add(calendarAddressProp(ical.PropAttendee, address, "REQ-PARTICIPANT", ""))
	}
	for _, address := range optional {
		event.Props.Add(calendarAddressProp(ical.PropAttendee, address, "OPT-PARTICIPANT", ""))
	}
}

func addReminder(event *ical.Event, minutes int) {
	alarm := ical.NewComponent(ical.CompAlarm)
	alarm.Props.SetText(ical.PropAction, "DISPLAY")
	alarm.Props.SetText(ical.PropDescription, "Reminder")
	trigger := ical.NewProp(ical.PropTrigger)
	trigger.SetDuration(-time.Duration(minutes) * time.Minute)
	alarm.Props.Set(trigger)
	event.Children = append(event.Children, alarm)
}

func removeAlarms(event *ical.Event) {
	children := event.Children[:0]
	for _, child := range event.Children {
		if child.Name != ical.CompAlarm {
			children = append(children, child)
		}
	}
	event.Children = children
}

func recurrenceRule(recurrence application.CalendarRecurrence) (string, error) {
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
		parts = append(
			parts,
			"FREQ=MONTHLY",
			fmt.Sprintf("BYMONTHDAY=%d", recurrence.DayOfMonth),
		)
	case application.CalendarRecurrenceAbsoluteYearly:
		month, ok := map[string]int{
			"january": 1, "february": 2, "march": 3, "april": 4,
			"may": 5, "june": 6, "july": 7, "august": 8,
			"september": 9, "october": 10, "november": 11, "december": 12,
		}[strings.ToLower(recurrence.Month)]
		if !ok {
			return "", fmt.Errorf("unsupported recurrence month %q", recurrence.Month)
		}
		parts = append(
			parts,
			"FREQ=YEARLY",
			fmt.Sprintf("BYMONTH=%d", month),
			fmt.Sprintf("BYMONTHDAY=%d", recurrence.DayOfMonth),
		)
	default:
		return "", errors.New("unsupported CalDAV recurrence")
	}
	if recurrence.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", recurrence.Interval))
	}
	if recurrence.NumberOfOccurrences > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", recurrence.NumberOfOccurrences))
	} else if recurrence.EndDate != "" {
		end, err := time.Parse("2006-01-02", recurrence.EndDate)
		if err != nil {
			return "", err
		}
		parts = append(parts, "UNTIL="+end.UTC().Format("20060102T235959Z"))
	}
	return strings.Join(parts, ";"), nil
}

func bareCalendarAddress(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value &&
		strings.Contains(value, "@")
}

func calDAVEventTimes(start, end time.Time, zone string) (time.Time, time.Time, error) {
	if zone == "" || zone == "UTC" {
		return start.UTC(), end.UTC(), nil
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"CalDAV time zone must be an installed IANA name: %w",
			err,
		)
	}
	return start.In(location), end.In(location), nil
}
