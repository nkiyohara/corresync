package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	"github.com/teambition/rrule-go"

	"github.com/nkiyohara/corresync/internal/application"
)

const maxCalDAVExpandedEvents = 2500

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
		events, err := client.calendarObjectViews(
			calendarPath,
			object.Path,
			object.ETag,
			object.Data.Events(),
			start,
			end,
		)
		if err != nil {
			return application.CalendarPage{}, err
		}
		if len(page.Events)+len(events) > maxCalDAVExpandedEvents {
			return application.CalendarPage{}, errors.New(
				"CalDAV expanded event page exceeds the configured limit",
			)
		}
		page.Events = append(page.Events, events...)
	}
	slices.SortFunc(page.Events, func(left, right application.CalendarEvent) int {
		if order := strings.Compare(left.Start, right.Start); order != 0 {
			return order
		}
		return strings.Compare(left.ID, right.ID)
	})
	return page, nil
}

func (client *Client) eventView(
	calendarPath, objectPath, etag string,
	event ical.Event,
) (application.CalendarEvent, error) {
	return client.eventViewAt(
		calendarPath,
		objectPath,
		etag,
		event,
		time.Time{},
		eventRecurrenceID(event),
	)
}

func (client *Client) eventViewAt(
	calendarPath, objectPath, etag string,
	event ical.Event,
	occurrenceStart time.Time,
	recurrenceID string,
) (application.CalendarEvent, error) {
	uid, err := event.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return application.CalendarEvent{}, errors.New("CalDAV event UID is missing")
	}
	id, err := encodeEventID(eventReference{
		Calendar: calendarPath, Path: objectPath, UID: uid,
		RecurrenceID: recurrenceID,
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
	if !occurrenceStart.IsZero() {
		duration := end.Sub(start)
		if duration <= 0 || duration > application.MaxCalendarWindow {
			return application.CalendarEvent{}, errors.New(
				"CalDAV recurring event has an invalid duration",
			)
		}
		start = occurrenceStart
		end = occurrenceStart.Add(duration)
	}
	subject, _ := event.Props.Text(ical.PropSummary)
	location, _ := event.Props.Text(ical.PropLocation)
	status, _ := event.Status()
	organizer := calendarAddress(event.Props.Get(ical.PropOrganizer))
	response := attendeeResponse(
		event.Props.Values(ical.PropAttendee),
		client.calendarIdentity(),
	)
	transparency, _ := event.Props.Text(ical.PropTransparency)
	freeBusy := "busy"
	if strings.EqualFold(transparency, "TRANSPARENT") {
		freeBusy = "free"
	}
	startProp := event.Props.Get(ical.PropDateTimeStart)
	endProp := event.Props.Get(ical.PropDateTimeEnd)
	originalStart, startZone, startFloating := calDAVOriginalTime(
		startProp,
		start.UTC().Format(time.RFC3339),
	)
	originalEnd, endZone, endFloating := calDAVOriginalTime(
		endProp,
		end.UTC().Format(time.RFC3339),
	)
	if !occurrenceStart.IsZero() {
		originalStart = calDAVOccurrenceTime(startProp, start)
		originalEnd = calDAVOccurrenceTime(endProp, end)
	}
	return application.CalendarEvent{
		ID: id, ChangeKey: etag, Subject: subject,
		Start: start.UTC().Format(time.RFC3339), End: end.UTC().Format(time.RFC3339),
		OriginalStart: originalStart, OriginalEnd: originalEnd,
		OriginalStartTimeZone: startZone, OriginalEndTimeZone: endZone,
		OriginalStartFloating: startFloating, OriginalEndFloating: endFloating,
		Location: location,
		Organizer: application.MailAddress{
			Name: organizer.name, Address: organizer.address,
		},
		IsAllDay:    startProp != nil && startProp.ValueType() == ical.ValueDate,
		IsOrganizer: strings.EqualFold(organizer.address, client.calendarIdentity()),
		IsCancelled: status == ical.EventCancelled,
		MyResponse:  response, FreeBusy: freeBusy,
	}, nil
}

func (client *Client) calendarObjectViews(
	calendarPath, objectPath, etag string,
	events []ical.Event,
	windowStart, windowEnd time.Time,
) ([]application.CalendarEvent, error) {
	if len(events) == 0 {
		return nil, errors.New("CalDAV object contains no event")
	}
	masterIndex := -1
	for index := range events {
		if events[index].Props.Get(ical.PropRecurrenceRule) == nil {
			continue
		}
		if masterIndex != -1 {
			return nil, errors.New(
				"CalDAV object contains multiple recurrence masters",
			)
		}
		masterIndex = index
	}
	if masterIndex == -1 {
		result := make([]application.CalendarEvent, 0, len(events))
		seen := make(map[string]struct{}, len(events))
		for index, event := range events {
			recurrenceID := eventRecurrenceID(event)
			if recurrenceID == "" && len(events) > 1 {
				start, err := event.DateTimeStart(time.UTC)
				if err != nil {
					return nil, err
				}
				recurrenceID = start.UTC().Format(time.RFC3339Nano)
			}
			key := eventUID(event) + "\x00" + recurrenceID
			if _, exists := seen[key]; exists {
				return nil, errors.New(
					"CalDAV expansion returned a duplicate event instance",
				)
			}
			seen[key] = struct{}{}
			view, err := client.eventViewAt(
				calendarPath,
				objectPath,
				etag,
				event,
				time.Time{},
				recurrenceID,
			)
			if err != nil {
				return nil, err
			}
			result = append(result, view)
			_ = index
		}
		return result, nil
	}

	master := events[masterIndex]
	uid := eventUID(master)
	if uid == "" {
		return nil, errors.New("CalDAV recurrence master UID is missing")
	}
	exceptions := make(map[string]ical.Event, len(events)-1)
	for index, event := range events {
		if index == masterIndex {
			continue
		}
		if eventUID(event) != uid {
			return nil, errors.New(
				"CalDAV recurrence object contains a mismatched UID",
			)
		}
		key, err := recurrenceInstant(event)
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, errors.New(
				"CalDAV recurrence exception has no RECURRENCE-ID",
			)
		}
		if _, exists := exceptions[key]; exists {
			return nil, errors.New(
				"CalDAV recurrence object contains duplicate exceptions",
			)
		}
		exceptions[key] = event
	}
	occurrences, err := expandCalDAVRecurrence(
		master,
		windowStart,
		windowEnd,
	)
	if err != nil {
		return nil, err
	}
	result := make([]application.CalendarEvent, 0, len(occurrences))
	for _, occurrence := range occurrences {
		key := occurrence.UTC().Format(time.RFC3339Nano)
		event := master
		override := time.Time{}
		recurrenceID := key
		if exception, exists := exceptions[key]; exists {
			event = exception
			delete(exceptions, key)
			recurrenceID = eventRecurrenceID(exception)
		} else {
			override = occurrence
		}
		view, err := client.eventViewAt(
			calendarPath,
			objectPath,
			etag,
			event,
			override,
			recurrenceID,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	for _, exception := range exceptions {
		instanceStart, err := exception.DateTimeStart(time.UTC)
		if err != nil {
			return nil, err
		}
		instanceEnd, err := exception.DateTimeEnd(time.UTC)
		if err != nil {
			return nil, err
		}
		if !instanceStart.Before(windowEnd) || !instanceEnd.After(windowStart) {
			continue
		}
		view, err := client.eventView(
			calendarPath,
			objectPath,
			etag,
			exception,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func expandCalDAVRecurrence(
	event ical.Event,
	windowStart, windowEnd time.Time,
) ([]time.Time, error) {
	start, err := event.DateTimeStart(time.UTC)
	if err != nil {
		return nil, err
	}
	end, err := event.DateTimeEnd(time.UTC)
	if err != nil {
		return nil, err
	}
	duration := end.Sub(start)
	if duration <= 0 || duration > application.MaxCalendarWindow {
		return nil, errors.New("CalDAV recurring event has an invalid duration")
	}
	options, err := event.Props.RecurrenceRule()
	if err != nil || options == nil {
		return nil, errors.New("CalDAV recurrence rule is malformed")
	}
	if options.Freq > rrule.DAILY {
		return nil, errors.New(
			"CalDAV recurrence is too frequent for bounded local expansion",
		)
	}
	options.Dtstart = start
	rule, err := rrule.NewRRule(*options)
	if err != nil {
		return nil, fmt.Errorf("parse CalDAV recurrence: %w", err)
	}
	set := &rrule.Set{}
	set.DTStart(start)
	set.RRule(rule)
	for _, property := range event.Props.Values(ical.PropExceptionDates) {
		value, err := property.DateTime(start.Location())
		if err != nil {
			return nil, fmt.Errorf("parse CalDAV EXDATE: %w", err)
		}
		set.ExDate(value)
	}
	for _, property := range event.Props.Values(ical.PropRecurrenceDates) {
		value, err := property.DateTime(start.Location())
		if err != nil {
			return nil, fmt.Errorf("parse CalDAV RDATE: %w", err)
		}
		set.RDate(value)
	}
	occurrences := set.Between(windowStart.Add(-duration), windowEnd, true)
	filtered := occurrences[:0]
	for _, occurrence := range occurrences {
		if !occurrence.Before(windowEnd) ||
			!occurrence.Add(duration).After(windowStart) {
			continue
		}
		filtered = append(filtered, occurrence)
		if len(filtered) > maxCalDAVExpandedEvents {
			return nil, errors.New(
				"CalDAV local recurrence expansion exceeds the configured limit",
			)
		}
	}
	return filtered, nil
}

func eventUID(event ical.Event) string {
	uid, _ := event.Props.Text(ical.PropUID)
	return uid
}

func eventRecurrenceID(event ical.Event) string {
	property := event.Props.Get(ical.PropRecurrenceID)
	if property == nil {
		return ""
	}
	return property.Value
}

func recurrenceInstant(event ical.Event) (string, error) {
	property := event.Props.Get(ical.PropRecurrenceID)
	if property == nil {
		return "", nil
	}
	value, err := property.DateTime(time.UTC)
	if err != nil {
		return "", errors.New("CalDAV RECURRENCE-ID is malformed")
	}
	return value.UTC().Format(time.RFC3339Nano), nil
}

func calDAVOccurrenceTime(property *ical.Prop, value time.Time) string {
	if property == nil {
		return value.UTC().Format(time.RFC3339)
	}
	if property.ValueType() == ical.ValueDate {
		return value.Format("20060102")
	}
	if property.Params.Get(ical.ParamTimezoneID) != "" {
		return value.Format("20060102T150405")
	}
	return value.UTC().Format("20060102T150405Z")
}

func calDAVOriginalTime(
	property *ical.Prop,
	fallback string,
) (string, string, bool) {
	if property == nil {
		return fallback, "UTC", false
	}
	zone := property.Params.Get(ical.ParamTimezoneID)
	if property.ValueType() == ical.ValueDate {
		return property.Value, zone, false
	}
	floating := zone == "" &&
		!strings.HasSuffix(strings.ToUpper(property.Value), "Z")
	if !floating && zone == "" {
		zone = "UTC"
	}
	return property.Value, zone, floating
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
	if len(input.RequiredAttendees)+len(input.OptionalAttendees) != 0 &&
		!client.scheduling {
		return application.CalendarCreateResult{}, errors.New(
			"CalDAV attendee invitations require discovered RFC 6638 scheduling",
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
		ctx, http.MethodPut, calendarPath, objectPath,
		"If-None-Match", "*", calendar,
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
	setEventSequence(event, 0)
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
	if identity := client.calendarIdentity(); bareCalendarAddress(identity) {
		event.Props.Set(calendarAddressProp(
			ical.PropOrganizer,
			identity,
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

func (client *Client) calendarIdentity() string {
	if client.calendarUser != "" {
		return client.calendarUser
	}
	return client.username
}

func (client *Client) UpdateCalendarEvent(
	ctx context.Context,
	input application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	reference, object, event, err := client.exactEvent(ctx, input.EventID, input.ChangeKey)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	requiresScheduling := len(event.Props.Values(ical.PropAttendee)) != 0 ||
		input.ReplaceAttendees &&
			len(input.RequiredAttendees)+len(input.OptionalAttendees) != 0
	if requiresScheduling && !client.scheduling {
		return application.CalendarUpdateResult{}, errors.New(
			"CalDAV attendee updates require discovered RFC 6638 scheduling",
		)
	}
	headers := http.Header{"If-Match": {strconv.Quote(input.ChangeKey)}}
	if requiresScheduling {
		scheduleTag, err := client.scheduleTag(
			ctx,
			reference.Calendar,
			reference.Path,
		)
		if err != nil {
			return application.CalendarUpdateResult{}, err
		}
		headers.Set("If-Schedule-Tag-Match", strconv.Quote(scheduleTag))
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
	if input.ReplaceRecurrence {
		event.Props.Del(ical.PropRecurrenceRule)
		if input.Recurrence != nil {
			rule, err := recurrenceRule(*input.Recurrence)
			if err != nil {
				return application.CalendarUpdateResult{}, err
			}
			property := ical.NewProp(ical.PropRecurrenceRule)
			property.SetValueType(ical.ValueRecurrence)
			property.Value = rule
			event.Props.Set(property)
		}
	}
	if requiresScheduling {
		if err := incrementEventSequence(&event); err != nil {
			return application.CalendarUpdateResult{}, err
		}
	}
	event.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
	etag, err := client.conditionalRequestWithHeaders(
		ctx,
		http.MethodPut,
		reference.Calendar,
		reference.Path,
		headers,
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
	attendeeEvent := len(event.Props.Values(ical.PropAttendee)) != 0
	if attendeeEvent && !client.scheduling {
		return errors.New(
			"CalDAV attendee cancellation requires discovered RFC 6638 scheduling; event was not deleted",
		)
	}
	headers := http.Header{"If-Match": {strconv.Quote(input.ChangeKey)}}
	if attendeeEvent {
		scheduleTag, err := client.scheduleTag(
			ctx,
			reference.Calendar,
			reference.Path,
		)
		if err != nil {
			return err
		}
		headers.Set("If-Schedule-Tag-Match", strconv.Quote(scheduleTag))
	}
	_, err = client.conditionalRequestWithHeaders(
		ctx,
		http.MethodDelete,
		reference.Calendar,
		reference.Path,
		headers,
		nil,
	)
	return err
}

func setEventSequence(event *ical.Event, sequence int) {
	property := ical.NewProp(ical.PropSequence)
	property.SetValueType(ical.ValueInt)
	property.Value = strconv.Itoa(sequence)
	event.Props.Set(property)
}

func incrementEventSequence(event *ical.Event) error {
	sequence := 0
	if property := event.Props.Get(ical.PropSequence); property != nil {
		value, err := property.Int()
		if err != nil || value < 0 || value == int(^uint(0)>>1) {
			return errors.New("CalDAV event SEQUENCE is malformed")
		}
		sequence = value
	}
	setEventSequence(event, sequence+1)
	return nil
}

func (client *Client) exactEvent(
	ctx context.Context,
	id, etag string,
) (eventReference, *caldav.CalendarObject, ical.Event, error) {
	reference, err := decodeEventID(id)
	if err != nil {
		return eventReference{}, nil, ical.Event{}, err
	}
	if !client.hasCalendar(reference.Calendar) {
		return eventReference{}, nil, ical.Event{}, errors.New(
			"CalDAV event belongs to an undiscovered calendar",
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
	if reference.RecurrenceID != "" {
		for _, candidate := range events {
			if eventUID(candidate) == reference.UID &&
				eventRecurrenceID(candidate) == reference.RecurrenceID {
				return reference, object, candidate, nil
			}
		}
		return eventReference{}, nil, ical.Event{}, errors.New(
			"CalDAV recurrence instance is not materialized for a safe write",
		)
	}
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
