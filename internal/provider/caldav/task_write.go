package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	webcaldav "github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
) (application.Task, error) {
	taskListPath, err := client.taskListFor(input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	if !client.taskListInfo[taskListPath].writable {
		return application.Task{}, errors.New("the selected CalDAV task list is read-only")
	}
	uid, err := newUID()
	if err != nil {
		return application.Task{}, err
	}
	task := ical.NewComponent(ical.CompToDo)
	task.Props.SetText(ical.PropUID, uid)
	task.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	task.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	parentUID, err := client.taskParentUID(ctx, taskListPath, input.ParentID)
	if err != nil {
		return application.Task{}, err
	}
	if err := applyCalDAVTaskCreate(task, parentUID, input); err != nil {
		return application.Task{}, err
	}
	calendar := newTaskCalendar(task)
	objectPath := strings.TrimSuffix(taskListPath, "/") + "/" +
		url.PathEscape(uid) + "." + ical.Extension
	if _, err := client.conditionalRequest(
		ctx,
		http.MethodPut,
		taskListPath,
		objectPath,
		"If-None-Match",
		"*",
		calendar,
	); err != nil {
		return application.Task{}, err
	}
	created, err := client.dav.GetCalendarObject(ctx, objectPath)
	if err != nil {
		return application.Task{}, fmt.Errorf(
			"%w: retrieve created CalDAV task: %w",
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	if !validObjectETag(created.ETag) {
		return application.Task{}, fmt.Errorf(
			"%w: created CalDAV task has no strong ETag",
			application.ErrWriteOutcomeUnknown,
		)
	}
	master, err := taskMaster(created.Data, uid)
	if err != nil {
		return application.Task{}, fmt.Errorf(
			"%w: retrieve created CalDAV task: %w",
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	return client.taskView(taskListPath, *created, master)
}

func applyCalDAVTaskCreate(
	task *ical.Component,
	parentUID string,
	input application.TaskCreateInput,
) error {
	task.Props.SetText(ical.PropSummary, input.Title)
	if input.Notes != "" {
		task.Props.SetText(ical.PropDescription, input.Notes)
	}
	if err := setCalDAVTaskPriority(task, input.Priority); err != nil {
		return err
	}
	if err := setCalDAVTaskTemporal(task, ical.PropDateTimeStart, input.Start); err != nil {
		return err
	}
	if err := setCalDAVTaskTemporal(task, ical.PropDue, input.Due); err != nil {
		return err
	}
	if err := setCalDAVTaskRecurrence(task, input.Recurrence); err != nil {
		return err
	}
	setCalDAVTaskParentUID(task, parentUID)
	setCalDAVTaskLabels(task, input.Labels)
	if err := setCalDAVTaskReminders(task, input.Reminders); err != nil {
		return err
	}
	return validateCalDAVTaskWrite(task)
}

func newTaskCalendar(task *ical.Component) *ical.Calendar {
	calendar := ical.NewCalendar()
	calendar.Props.SetText(ical.PropProductID, "-//Corresync//EN")
	calendar.Props.SetText(ical.PropVersion, "2.0")
	calendar.Children = append(calendar.Children, task)
	return calendar
}

func (client *Client) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
) (application.Task, error) {
	reference, object, task, err := client.exactTask(
		ctx, input.ListID, input.TaskID, input.Version,
	)
	if err != nil {
		return application.Task{}, err
	}
	if input.Title != nil {
		task.Props.SetText(ical.PropSummary, *input.Title)
	}
	if input.Notes != nil {
		if *input.Notes == "" {
			task.Props.Del(ical.PropDescription)
		} else {
			task.Props.SetText(ical.PropDescription, *input.Notes)
		}
	}
	if input.Priority != nil {
		if err := setCalDAVTaskPriority(task, *input.Priority); err != nil {
			return application.Task{}, err
		}
	}
	if input.ReplaceStart {
		if err := setCalDAVTaskTemporal(task, ical.PropDateTimeStart, input.Start); err != nil {
			return application.Task{}, err
		}
	}
	if input.ReplaceDue {
		if err := setCalDAVTaskTemporal(task, ical.PropDue, input.Due); err != nil {
			return application.Task{}, err
		}
	}
	if input.ReplaceRecurrence {
		if err := setCalDAVTaskRecurrence(task, input.Recurrence); err != nil {
			return application.Task{}, err
		}
	}
	if input.ParentID != nil {
		parentUID, err := client.taskParentUID(ctx, reference.List, *input.ParentID)
		if err != nil {
			return application.Task{}, err
		}
		if *input.ParentID == input.TaskID {
			return application.Task{}, errors.New("CalDAV task cannot be its own parent")
		}
		setCalDAVTaskParentUID(task, parentUID)
	}
	if input.ReplaceLabels {
		setCalDAVTaskLabels(task, input.Labels)
	}
	if input.ReplaceReminders {
		if err := setCalDAVTaskReminders(task, input.Reminders); err != nil {
			return application.Task{}, err
		}
	}
	if input.ReplaceStart || input.ReplaceDue || input.ReplaceRecurrence ||
		input.ReplaceReminders {
		if err := validateCalDAVTaskWrite(task); err != nil {
			return application.Task{}, err
		}
	}
	return client.commitTaskObject(ctx, reference, object, task, input.Version)
}

func (client *Client) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	reference, object, task, err := client.exactTask(
		ctx, input.ListID, input.TaskID, input.Version,
	)
	if err != nil {
		return application.Task{}, err
	}
	status, err := task.Props.Text(ical.PropStatus)
	if err != nil {
		return application.Task{}, err
	}
	if strings.EqualFold(status, "COMPLETED") {
		return application.Task{}, errors.New("CalDAV task is already completed")
	}
	task.Props.SetText(ical.PropStatus, "COMPLETED")
	setTaskPercent(task, 100)
	task.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
	return client.commitTaskObject(ctx, reference, object, task, input.Version)
}

func (client *Client) ReopenTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	reference, object, task, err := client.exactTask(
		ctx, input.ListID, input.TaskID, input.Version,
	)
	if err != nil {
		return application.Task{}, err
	}
	status, err := task.Props.Text(ical.PropStatus)
	if err != nil {
		return application.Task{}, err
	}
	if !strings.EqualFold(status, "COMPLETED") {
		return application.Task{}, errors.New("CalDAV task is not completed")
	}
	task.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	setTaskPercent(task, 0)
	task.Props.Del(ical.PropCompleted)
	return client.commitTaskObject(ctx, reference, object, task, input.Version)
}

func (client *Client) DeleteTask(
	ctx context.Context,
	input application.TaskDeleteInput,
) error {
	reference, _, _, err := client.exactTask(
		ctx, input.ListID, input.TaskID, input.Version,
	)
	if err != nil {
		return err
	}
	_, err = client.conditionalRequest(
		ctx,
		http.MethodDelete,
		reference.List,
		reference.Path,
		"If-Match",
		strconv.Quote(input.Version),
		nil,
	)
	return err
}

func (client *Client) exactTask(
	ctx context.Context,
	listID, taskID, version string,
) (taskReference, webcaldav.CalendarObject, *ical.Component, error) {
	taskListPath, err := client.taskListFor(listID)
	if err != nil {
		return taskReference{}, webcaldav.CalendarObject{}, nil, err
	}
	if !client.taskListInfo[taskListPath].writable {
		return taskReference{}, webcaldav.CalendarObject{}, nil,
			errors.New("the selected CalDAV task list is read-only")
	}
	reference, err := decodeTaskID(taskID)
	if err != nil || reference.List != taskListPath || reference.Path == "" {
		return taskReference{}, webcaldav.CalendarObject{}, nil,
			errors.New("CalDAV task does not belong to the selected list")
	}
	object, err := client.dav.GetCalendarObject(ctx, reference.Path)
	if err != nil {
		return taskReference{}, webcaldav.CalendarObject{}, nil, err
	}
	if object.ETag != version || !validObjectETag(object.ETag) {
		return taskReference{}, webcaldav.CalendarObject{}, nil,
			errors.New("CalDAV task changed before write")
	}
	if object.Path != reference.Path || !pathWithin(object.Path, reference.List) {
		return taskReference{}, webcaldav.CalendarObject{}, nil,
			errors.New("CalDAV task object escaped the selected list")
	}
	task, err := taskMaster(object.Data, reference.UID)
	if err != nil {
		return taskReference{}, webcaldav.CalendarObject{}, nil, err
	}
	return reference, *object, task, nil
}

func (client *Client) commitTaskObject(
	ctx context.Context,
	reference taskReference,
	object webcaldav.CalendarObject,
	task *ical.Component,
	version string,
) (application.Task, error) {
	if err := touchCalDAVTask(task); err != nil {
		return application.Task{}, err
	}
	etag, err := client.conditionalRequest(
		ctx,
		http.MethodPut,
		reference.List,
		reference.Path,
		"If-Match",
		strconv.Quote(version),
		object.Data,
	)
	if err != nil {
		return application.Task{}, err
	}
	if !validObjectETag(etag) {
		return application.Task{}, fmt.Errorf(
			"%w: updated CalDAV task has no strong ETag",
			application.ErrWriteOutcomeUnknown,
		)
	}
	object.ETag = etag
	return client.taskView(reference.List, object, task)
}

func touchCalDAVTask(task *ical.Component) error {
	sequence := 0
	if property := task.Props.Get(ical.PropSequence); property != nil {
		value, err := property.Int()
		if err != nil || value < 0 || value == int(^uint(0)>>1) {
			return errors.New("CalDAV VTODO sequence is malformed")
		}
		sequence = value + 1
	}
	property := ical.NewProp(ical.PropSequence)
	property.SetValueType(ical.ValueInt)
	property.Value = strconv.Itoa(sequence)
	task.Props.Set(property)
	task.Props.SetDateTime(ical.PropLastModified, time.Now().UTC())
	task.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	return nil
}

func setTaskPercent(task *ical.Component, value int) {
	property := ical.NewProp(ical.PropPercentComplete)
	property.SetValueType(ical.ValueInt)
	property.Value = strconv.Itoa(value)
	task.Props.Set(property)
}

func setCalDAVTaskTemporal(
	task *ical.Component,
	name string,
	value *application.TaskTemporal,
) error {
	task.Props.Del(name)
	if value == nil {
		return nil
	}
	property := ical.NewProp(name)
	switch value.Kind {
	case application.TaskTemporalDate:
		parsed, err := time.Parse(time.DateOnly, value.Value)
		if err != nil {
			return err
		}
		property.SetDate(parsed)
	case application.TaskTemporalFloating:
		parsed, err := time.Parse("2006-01-02T15:04:05", value.Value)
		if err != nil {
			return err
		}
		property.SetValueType(ical.ValueDateTime)
		property.Value = parsed.Format("20060102T150405")
	case application.TaskTemporalZoned:
		instant, err := time.Parse(time.RFC3339, value.Value)
		if err != nil {
			return err
		}
		location, err := time.LoadLocation(value.TimeZone)
		if err != nil {
			return err
		}
		property.SetDateTime(instant.In(location))
	default:
		return errors.New("task temporal kind is invalid")
	}
	task.Props.Set(property)
	return nil
}

func setCalDAVTaskRecurrence(
	task *ical.Component,
	recurrence *application.TaskRecurrence,
) error {
	task.Props.Del(ical.PropRecurrenceRule)
	if recurrence == nil {
		return nil
	}
	rule, err := calDAVTaskRecurrenceRule(*recurrence)
	if err != nil {
		return err
	}
	property := ical.NewProp(ical.PropRecurrenceRule)
	property.SetValueType(ical.ValueRecurrence)
	property.Value = rule
	task.Props.Set(property)
	return nil
}

func calDAVTaskRecurrenceRule(recurrence application.TaskRecurrence) (string, error) {
	switch recurrence.Frequency {
	case application.TaskRecurrenceProvider:
		property := ical.NewProp(ical.PropRecurrenceRule)
		property.SetValueType(ical.ValueRecurrence)
		property.Value = recurrence.ProviderRule
		component := ical.NewComponent(ical.CompToDo)
		component.Props.Set(property)
		if _, err := component.Props.RecurrenceRule(); err != nil {
			return "", err
		}
		return recurrence.ProviderRule, nil
	case application.TaskRecurrenceDaily:
		// Encoded below.
	case application.TaskRecurrenceWeekly:
		// Encoded below.
	case application.TaskRecurrenceMonthly:
		// Encoded below.
	case application.TaskRecurrenceYearly:
		// Encoded below.
	default:
		return "", errors.New("task recurrence frequency is invalid")
	}
	frequency := map[application.TaskRecurrenceFrequency]string{
		application.TaskRecurrenceDaily: "DAILY", application.TaskRecurrenceWeekly: "WEEKLY",
		application.TaskRecurrenceMonthly: "MONTHLY", application.TaskRecurrenceYearly: "YEARLY",
	}[recurrence.Frequency]
	parts := []string{"FREQ=" + frequency}
	parts = append(parts, fmt.Sprintf("INTERVAL=%d", recurrence.Interval))
	if len(recurrence.DaysOfWeek) != 0 {
		days := make([]string, 0, len(recurrence.DaysOfWeek))
		for _, day := range recurrence.DaysOfWeek {
			encoded, ok := map[string]string{
				"monday": "MO", "tuesday": "TU", "wednesday": "WE",
				"thursday": "TH", "friday": "FR", "saturday": "SA", "sunday": "SU",
			}[day]
			if !ok {
				return "", errors.New("task recurrence weekday is invalid")
			}
			days = append(days, encoded)
		}
		parts = append(parts, "BYDAY="+strings.Join(days, ","))
	}
	if recurrence.Count > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", recurrence.Count))
	} else if recurrence.Until != nil {
		until := recurrence.Until
		switch until.Kind {
		case application.TaskTemporalDate:
			parsed, _ := time.Parse(time.DateOnly, until.Value)
			parts = append(parts, "UNTIL="+parsed.Format("20060102"))
		case application.TaskTemporalZoned:
			parsed, _ := time.Parse(time.RFC3339, until.Value)
			parts = append(parts, "UNTIL="+parsed.UTC().Format("20060102T150405Z"))
		case application.TaskTemporalFloating:
			return "", errors.New("floating recurrence end is not portable in CalDAV")
		default:
			return "", errors.New("task recurrence end is invalid")
		}
	}
	return strings.Join(parts, ";"), nil
}

func (client *Client) taskParentUID(
	ctx context.Context,
	taskListPath, parentID string,
) (string, error) {
	if parentID == "" {
		return "", nil
	}
	parent, err := decodeTaskID(parentID)
	if err != nil || parent.List != taskListPath {
		return "", errors.New("CalDAV parent task belongs to another list")
	}
	if parent.UID != "" {
		return parent.UID, nil
	}
	object, err := client.dav.GetCalendarObject(ctx, parent.Path)
	if err != nil {
		return "", err
	}
	if object.Path != parent.Path || !pathWithin(object.Path, taskListPath) ||
		!validObjectETag(object.ETag) {
		return "", errors.New("CalDAV parent task object is malformed")
	}
	master, err := taskMaster(object.Data, "")
	if err != nil {
		return "", err
	}
	uid, err := master.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return "", errors.New("CalDAV parent VTODO UID is missing")
	}
	return uid, nil
}

func setCalDAVTaskParentUID(task *ical.Component, parentUID string) {
	properties := task.Props.Values(ical.PropRelatedTo)
	kept := make([]ical.Prop, 0, len(properties))
	for index := range properties {
		relation := strings.ToUpper(properties[index].Params.Get(ical.ParamRelationshipType))
		if relation != "" && relation != "PARENT" {
			kept = append(kept, properties[index])
		}
	}
	if parentUID != "" {
		property := ical.NewProp(ical.PropRelatedTo)
		property.SetText(parentUID)
		property.Params.Set(ical.ParamRelationshipType, "PARENT")
		kept = append(kept, *property)
	}
	if len(kept) == 0 {
		task.Props.Del(ical.PropRelatedTo)
	} else {
		task.Props[ical.PropRelatedTo] = kept
	}
}

func setCalDAVTaskLabels(task *ical.Component, labels []string) {
	task.Props.Del(ical.PropCategories)
	if len(labels) == 0 {
		return
	}
	property := ical.NewProp(ical.PropCategories)
	property.SetTextList(labels)
	task.Props.Set(property)
}

func setCalDAVTaskReminders(
	task *ical.Component,
	reminders []application.TaskReminder,
) error {
	children := task.Children[:0]
	for _, child := range task.Children {
		if child.Name != ical.CompAlarm {
			children = append(children, child)
			continue
		}
		_, representable, err := calDAVTaskReminder(child)
		if err != nil || !representable {
			children = append(children, child)
		}
	}
	task.Children = children
	for _, reminder := range reminders {
		alarm := ical.NewComponent(ical.CompAlarm)
		alarm.Props.SetText(ical.PropAction, "DISPLAY")
		alarm.Props.SetText(ical.PropDescription, "Reminder")
		trigger := ical.NewProp(ical.PropTrigger)
		switch reminder.Kind {
		case application.TaskReminderAbsolute:
			if reminder.At == nil || reminder.At.Kind != application.TaskTemporalZoned {
				return errors.New("CalDAV absolute task reminder requires a zoned datetime")
			}
			instant, err := time.Parse(time.RFC3339, reminder.At.Value)
			if err != nil {
				return err
			}
			trigger.SetDateTime(instant.UTC())
		case application.TaskReminderRelativeStart, application.TaskReminderRelativeDue:
			trigger.SetDuration(time.Duration(reminder.OffsetMinutes) * time.Minute)
			if reminder.Kind == application.TaskReminderRelativeDue {
				trigger.Params.Set(ical.ParamRelated, "END")
			} else {
				trigger.Params.Set(ical.ParamRelated, "START")
			}
		default:
			return errors.New("task reminder kind is invalid")
		}
		alarm.Props.Set(trigger)
		task.Children = append(task.Children, alarm)
	}
	return nil
}

func validateCalDAVTaskWrite(task *ical.Component) error {
	start, err := calDAVTaskTemporal(task.Props.Get(ical.PropDateTimeStart))
	if err != nil {
		return fmt.Errorf("parse VTODO DTSTART before write: %w", err)
	}
	due, err := calDAVTaskTemporal(task.Props.Get(ical.PropDue))
	if err != nil {
		return fmt.Errorf("parse VTODO DUE before write: %w", err)
	}
	if err := validateCalDAVTaskTemporalContract(task, start, due); err != nil {
		return err
	}
	for _, child := range task.Children {
		reminder, representable, err := calDAVTaskReminder(child)
		if err != nil || !representable {
			continue
		}
		switch reminder.Kind {
		case application.TaskReminderRelativeStart:
			if start == nil {
				return errors.New("CalDAV start-relative reminder requires DTSTART")
			}
		case application.TaskReminderRelativeDue:
			if due == nil && task.Props.Get(ical.PropDuration) == nil {
				return errors.New("CalDAV due-relative reminder requires DUE or DURATION")
			}
		case application.TaskReminderAbsolute:
			// The UTC constraint is enforced by calDAVTaskReminder and the writer.
		default:
			return errors.New("CalDAV task reminder kind is invalid")
		}
	}
	return nil
}

func validateCalDAVTaskTemporalContract(
	task *ical.Component,
	start, due *application.TaskTemporal,
) error {
	if start != nil && due != nil {
		if start.Kind != due.Kind {
			return errors.New("CalDAV VTODO DTSTART and DUE must use the same temporal kind")
		}
		ordered, err := calDAVTaskDueAfterStart(*start, *due)
		if err != nil {
			return err
		}
		if !ordered {
			return errors.New("CalDAV VTODO DUE must be later than DTSTART")
		}
	}
	if task.Props.Get(ical.PropRecurrenceRule) != nil && start == nil {
		return errors.New("CalDAV recurring VTODO requires DTSTART")
	}
	if task.Props.Get(ical.PropDue) != nil && task.Props.Get(ical.PropDuration) != nil {
		return errors.New("CalDAV VTODO cannot contain both DUE and DURATION")
	}
	if recurrence := task.Props.Get(ical.PropRecurrenceRule); recurrence != nil {
		untilKind, hasUntil, err := calDAVTaskRecurrenceUntilKind(recurrence.Value)
		if err != nil {
			return err
		}
		if hasUntil && untilKind != start.Kind {
			return errors.New("CalDAV VTODO recurrence end must match DTSTART's temporal kind")
		}
	}
	return nil
}

func calDAVTaskRecurrenceUntilKind(
	rule string,
) (application.TaskTemporalKind, bool, error) {
	for _, part := range strings.Split(rule, ";") {
		name, value, found := strings.Cut(part, "=")
		if !found || !strings.EqualFold(name, "UNTIL") {
			continue
		}
		switch {
		case len(value) == len("20060102"):
			if _, err := time.Parse("20060102", value); err != nil {
				return "", false, errors.New("CalDAV VTODO recurrence end is malformed")
			}
			return application.TaskTemporalDate, true, nil
		case strings.HasSuffix(value, "Z"):
			if _, err := time.Parse("20060102T150405Z", value); err != nil {
				return "", false, errors.New("CalDAV VTODO recurrence end is malformed")
			}
			return application.TaskTemporalZoned, true, nil
		default:
			if _, err := time.Parse("20060102T150405", value); err != nil {
				return "", false, errors.New("CalDAV VTODO recurrence end is malformed")
			}
			return application.TaskTemporalFloating, true, nil
		}
	}
	return "", false, nil
}

func calDAVTaskDueAfterStart(
	start, due application.TaskTemporal,
) (bool, error) {
	var layout string
	switch start.Kind {
	case application.TaskTemporalDate:
		layout = time.DateOnly
	case application.TaskTemporalFloating:
		layout = "2006-01-02T15:04:05"
	case application.TaskTemporalZoned:
		layout = time.RFC3339
	default:
		return false, errors.New("CalDAV VTODO temporal kind is invalid")
	}
	startTime, err := time.Parse(layout, start.Value)
	if err != nil {
		return false, err
	}
	dueTime, err := time.Parse(layout, due.Value)
	if err != nil {
		return false, err
	}
	return dueTime.After(startTime), nil
}
