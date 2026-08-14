package graphapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/windowszone"
)

const graphTaskSelect = "id,title,body,status,importance,startDateTime,dueDateTime," +
	"completedDateTime,isReminderOn,reminderDateTime,recurrence,categories"

type graphTaskList struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	IsOwner           bool   `json:"isOwner"`
	WellKnownListName string `json:"wellknownListName"`
}

type graphTask struct {
	ODataETag       string                `json:"@odata.etag"`
	Removed         json.RawMessage       `json:"@removed"`
	ID              string                `json:"id"`
	Title           string                `json:"title"`
	Body            graphItemBody         `json:"body"`
	Status          string                `json:"status"`
	Importance      string                `json:"importance"`
	Start           *graphDateTimeZone    `json:"startDateTime"`
	Due             *graphDateTimeZone    `json:"dueDateTime"`
	Completed       *graphDateTimeZone    `json:"completedDateTime"`
	ReminderOn      bool                  `json:"isReminderOn"`
	Reminder        *graphDateTimeZone    `json:"reminderDateTime"`
	Recurrence      *graphTaskRecurrence  `json:"recurrence"`
	Categories      []string              `json:"categories"`
	ChecklistItems  []graphChecklistItem  `json:"checklistItems"`
	LinkedResources []graphLinkedResource `json:"linkedResources"`
}

type graphChecklistItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Checked     bool   `json:"isChecked"`
}

type graphLinkedResource struct {
	ID              string `json:"id"`
	WebURL          string `json:"webUrl"`
	ApplicationName string `json:"applicationName"`
	DisplayName     string `json:"displayName"`
	ExternalID      string `json:"externalId"`
}

type graphTaskRecurrence struct {
	Pattern graphRecurrencePattern `json:"pattern"`
	Range   graphRecurrenceRange   `json:"range"`
}

type graphRecurrencePattern struct {
	Type           string   `json:"type"`
	Interval       int      `json:"interval"`
	Month          int      `json:"month,omitempty"`
	DayOfMonth     int      `json:"dayOfMonth,omitempty"`
	DaysOfWeek     []string `json:"daysOfWeek,omitempty"`
	FirstDayOfWeek string   `json:"firstDayOfWeek,omitempty"`
}

type graphRecurrenceRange struct {
	Type                string `json:"type"`
	StartDate           string `json:"startDate"`
	EndDate             string `json:"endDate,omitempty"`
	NumberOfOccurrences int    `json:"numberOfOccurrences,omitempty"`
	RecurrenceTimeZone  string `json:"recurrenceTimeZone,omitempty"`
}

type graphTaskPage struct {
	NextLink  string      `json:"@odata.nextLink"`
	DeltaLink string      `json:"@odata.deltaLink"`
	Value     []graphTask `json:"value"`
}

// TaskCapabilities reports the feature set confirmed by the successful
// account-scoped task-list probe in New. Unavailable canonical fields remain
// false and are rejected by the application service before adapter access.
func (client *Client) TaskCapabilities() application.TaskCapabilities {
	enabled := client != nil && client.tasks
	if !enabled {
		return application.TaskCapabilities{}
	}
	write := client.taskWrite
	return application.TaskCapabilities{
		Read:   true,
		Create: write, Update: write, Complete: write, Reopen: write, Delete: write,
		Reminders: true, Recurrence: true, Checklist: true,
		Labels: true, LinkedSources: true, ZonedDateTime: true,
		SyncModes: []application.TaskSyncMode{application.TaskSyncDelta},
	}
}

func (client *Client) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
) (application.TaskListPage, error) {
	if err := client.requireTaskRead(); err != nil {
		return application.TaskListPage{}, err
	}
	var response struct {
		NextLink string          `json:"@odata.nextLink"`
		Value    []graphTaskList `json:"value"`
	}
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		"me/todo/lists",
		url.Values{
			"$select": {"id,displayName,isOwner,wellknownListName"},
			"$top":    {strconv.Itoa(input.Limit)}, "$skip": {strconv.Itoa(input.Offset)},
		},
		nil,
		&response,
		false,
		nil,
		http.StatusOK,
	); err != nil {
		return application.TaskListPage{}, err
	}
	if len(response.Value) > input.Limit {
		return application.TaskListPage{}, errors.New("graph returned an oversized task-list page")
	}
	page := application.TaskListPage{
		Lists:  make([]application.TaskList, 0, len(response.Value)),
		Offset: input.Offset, Limit: input.Limit, HasMore: response.NextLink != "",
	}
	for _, list := range response.Value {
		if !validGraphID(list.ID) || list.DisplayName == "" {
			return application.TaskListPage{}, errors.New("graph returned an invalid task list")
		}
		id, err := encodeTaskListID(list.ID)
		if err != nil {
			return application.TaskListPage{}, err
		}
		page.Lists = append(page.Lists, application.TaskList{
			ID: id, DisplayName: list.DisplayName, Editable: list.IsOwner,
			Default: list.WellKnownListName == "defaultList",
		})
	}
	return page, nil
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
) (application.TaskPage, error) {
	if err := client.requireTaskRead(); err != nil {
		return application.TaskPage{}, err
	}
	listID, err := decodeTaskListID(input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	query := url.Values{
		"$select": {graphTaskSelect}, "$expand": {"checklistItems,linkedResources"},
		"$top": {strconv.Itoa(input.Limit)}, "$skip": {strconv.Itoa(input.Offset)},
	}
	if input.Status != "" {
		status, statusErr := graphWriteStatus(input.Status)
		if statusErr != nil {
			return application.TaskPage{}, statusErr
		}
		query.Set("$filter", "status eq '"+status+"'")
	}
	var response graphTaskPage
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, graphTaskCollection(listID), query, nil, &response,
		false, nil, http.StatusOK,
	); err != nil {
		return application.TaskPage{}, err
	}
	return client.graphTaskPage(input, response)
}

func (client *Client) SearchTasks(
	context.Context,
	application.TaskSearchInput,
) (application.TaskPage, error) {
	return application.TaskPage{}, errors.New("task search is unsupported by Microsoft Graph To Do")
}

func (client *Client) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
) (application.Task, error) {
	if err := client.requireTaskRead(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeTaskRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	task, err := client.getGraphTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, err
	}
	return graphTaskView(input.ListID, task)
}

func (client *Client) getGraphTask(ctx context.Context, listID, taskID string) (graphTask, error) {
	var task graphTask
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, graphTaskResource(listID, taskID),
		url.Values{"$select": {graphTaskSelect}, "$expand": {"checklistItems,linkedResources"}},
		nil, &task, false, nil, http.StatusOK,
	); err != nil {
		return graphTask{}, err
	}
	if !validGraphID(task.ID) || !validETag(task.ODataETag) {
		return graphTask{}, errors.New("graph returned an invalid task identity")
	}
	return task, nil
}

func (client *Client) graphTaskPage(
	input application.TaskReadInput,
	response graphTaskPage,
) (application.TaskPage, error) {
	if len(response.Value) > input.Limit {
		return application.TaskPage{}, errors.New("graph returned an oversized task page")
	}
	page := application.TaskPage{
		Tasks:  make([]application.Task, 0, len(response.Value)),
		Offset: input.Offset, Limit: input.Limit, HasMore: response.NextLink != "",
	}
	for _, remote := range response.Value {
		task, err := graphTaskView(input.ListID, remote)
		if err != nil {
			return application.TaskPage{}, err
		}
		page.Tasks = append(page.Tasks, task)
	}
	return page, nil
}

func graphTaskView(listID string, remote graphTask) (application.Task, error) {
	if !validGraphID(remote.ID) || !validETag(remote.ODataETag) {
		return application.Task{}, errors.New("graph returned an invalid task identity")
	}
	id, err := encodeTaskID(remote.ID)
	if err != nil {
		return application.Task{}, err
	}
	status, statusDegradation, err := graphReadStatus(remote.Status)
	if err != nil {
		return application.Task{}, err
	}
	priority, err := graphReadPriority(remote.Importance)
	if err != nil {
		return application.Task{}, err
	}
	notes := remote.Body.Content
	var notesDegradation *domain.Degradation
	switch strings.ToLower(remote.Body.ContentType) {
	case "html":
		notes, err = graphHTMLText(notes)
		if err != nil {
			return application.Task{}, err
		}
		notesDegradation = &domain.Degradation{
			Feature: "tasks.notes",
			Reason:  "the provider HTML task body is represented as plain text",
			Lossy:   true,
		}
	case "text":
	case "":
		if notes != "" {
			return application.Task{}, errors.New("graph task body omitted its content type")
		}
	default:
		return application.Task{}, errors.New("graph task body has an unknown content type")
	}
	start, err := graphTaskTime(remote.Start)
	if err != nil {
		return application.Task{}, fmt.Errorf("graph task start: %w", err)
	}
	due, err := graphTaskTime(remote.Due)
	if err != nil {
		return application.Task{}, fmt.Errorf("graph task due: %w", err)
	}
	completed, err := graphTaskTime(remote.Completed)
	if err != nil {
		return application.Task{}, fmt.Errorf("graph task completion: %w", err)
	}
	reminders := []application.TaskReminder(nil)
	if remote.ReminderOn {
		reminder, reminderErr := graphTaskTime(remote.Reminder)
		if reminderErr != nil || reminder == nil {
			return application.Task{}, errors.New("graph task reminder is malformed")
		}
		reminders = []application.TaskReminder{{Kind: application.TaskReminderAbsolute, At: reminder}}
	}
	recurrence, err := graphReadTaskRecurrence(remote.Recurrence, start, due)
	if err != nil {
		return application.Task{}, err
	}
	checklist := make([]application.TaskChecklistItem, 0, len(remote.ChecklistItems))
	checklistIDs := make(map[string]bool, len(remote.ChecklistItems))
	for _, item := range remote.ChecklistItems {
		if !validGraphID(item.ID) || item.DisplayName == "" || checklistIDs[item.ID] {
			return application.Task{}, errors.New("graph returned an invalid checklist item")
		}
		checklistIDs[item.ID] = true
		itemID, encodeErr := encodeChecklistID(item.ID)
		if encodeErr != nil {
			return application.Task{}, encodeErr
		}
		checklist = append(checklist, application.TaskChecklistItem{
			ID: itemID, Title: item.DisplayName, Completed: item.Checked,
		})
	}
	sources := make([]application.TaskLinkedSource, 0, len(remote.LinkedResources))
	for _, linked := range remote.LinkedResources {
		source, ok, decodeErr := graphLinkedSource(linked)
		if decodeErr != nil {
			return application.Task{}, decodeErr
		}
		if ok {
			sources = append(sources, source)
		}
	}
	task := application.Task{
		ID: id, Version: encodeETag(remote.ODataETag), ListID: listID,
		Title: remote.Title, Notes: notes, Status: status, Priority: priority,
		Start: start, Due: due, CompletedAt: completed, Reminders: reminders,
		Recurrence: recurrence, Checklist: checklist,
		Labels: append([]string(nil), remote.Categories...), Sources: sources,
	}
	if statusDegradation != nil {
		task.Degradations = append(task.Degradations, *statusDegradation)
	}
	if status == application.TaskStatusCompleted && completed == nil {
		task.Degradations = append(task.Degradations, domain.Degradation{
			Feature: "completion_time",
			Reason:  "the provider marked the task completed without a completion time",
		})
	} else if status != application.TaskStatusCompleted && completed != nil {
		return application.Task{}, errors.New("graph returned a completion time for an incomplete task")
	}
	if notesDegradation != nil {
		task.Degradations = append(task.Degradations, *notesDegradation)
	}
	if recurrence != nil && recurrence.Frequency == application.TaskRecurrenceProvider {
		task.Degradations = append(task.Degradations, domain.Degradation{
			Feature: "tasks.recurrence",
			Reason:  "the Graph recurrence cannot be represented portably and is preserved as a provider rule",
		})
	}
	return task, nil
}

func graphTaskTime(remote *graphDateTimeZone) (*application.TaskTemporal, error) {
	if remote == nil {
		return nil, nil
	}
	zone, location, err := canonicalGraphTaskZone(remote.TimeZone)
	if err != nil {
		return nil, err
	}
	var local time.Time
	if instant, absoluteErr := time.Parse(time.RFC3339Nano, remote.DateTime); absoluteErr == nil {
		local = instant.In(location)
		err = nil
	} else {
		for _, layout := range []string{
			"2006-01-02T15:04:05.9999999", "2006-01-02T15:04:05",
		} {
			local, err = time.ParseInLocation(layout, remote.DateTime, location)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, errors.New("graph task datetime is malformed")
	}
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: local.Format(time.RFC3339), TimeZone: zone,
	}, nil
}

func canonicalGraphTaskZone(value string) (string, *time.Location, error) {
	if value == "Etc/UTC" {
		value = "UTC"
	}
	location, err := time.LoadLocation(value)
	if err == nil {
		return value, location, nil
	}
	iana, ok := windowszone.IANA(value)
	if !ok {
		return "", nil, errors.New("graph task time zone is neither a known IANA nor Windows identifier")
	}
	location, err = time.LoadLocation(iana)
	if err != nil {
		return "", nil, errors.New("mapped graph task time zone is unavailable")
	}
	return iana, location, nil
}

func graphReadStatus(value string) (application.TaskStatus, *domain.Degradation, error) {
	switch value {
	case "notStarted":
		return application.TaskStatusNeedsAction, nil, nil
	case "inProgress":
		return application.TaskStatusInProgress, nil, nil
	case "completed":
		return application.TaskStatusCompleted, nil, nil
	case "waitingOnOthers", "deferred":
		return application.TaskStatusInProgress, &domain.Degradation{
			Feature: "tasks.status", Reason: "Microsoft To Do status " + value + " is represented as in_progress", Lossy: true,
		}, nil
	default:
		return "", nil, errors.New("graph returned an unknown task status")
	}
}

func graphReadPriority(value string) (application.TaskPriority, error) {
	switch value {
	case "low":
		return application.TaskPriorityLow, nil
	case "normal", "":
		return application.TaskPriorityNormal, nil
	case "high":
		return application.TaskPriorityHigh, nil
	default:
		return "", errors.New("graph returned an unknown task importance")
	}
}

func encodeTaskListID(id string) (string, error) {
	return encodeReference("mgtl1_", struct {
		ID string `json:"id"`
	}{ID: id})
}

func decodeTaskListID(value string) (string, error) {
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(value, "mgtl1_", &reference); err != nil || !validGraphID(reference.ID) {
		return "", errors.New("task list ID is not a Graph identifier")
	}
	return reference.ID, nil
}

func encodeTaskID(id string) (string, error) {
	return encodeReference("mgtt1_", struct {
		ID string `json:"id"`
	}{ID: id})
}

func decodeTaskID(value string) (string, error) {
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(value, "mgtt1_", &reference); err != nil || !validGraphID(reference.ID) {
		return "", errors.New("task ID is not a Graph identifier")
	}
	return reference.ID, nil
}

func encodeChecklistID(id string) (string, error) {
	return encodeReference("mgtc1_", struct {
		ID string `json:"id"`
	}{ID: id})
}

func decodeChecklistID(value string) (string, error) {
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(value, "mgtc1_", &reference); err != nil || !validGraphID(reference.ID) {
		return "", errors.New("checklist item ID is not a Graph identifier")
	}
	return reference.ID, nil
}

func decodeTaskRoute(listValue, taskValue string) (string, string, error) {
	listID, err := decodeTaskListID(listValue)
	if err != nil {
		return "", "", err
	}
	taskID, err := decodeTaskID(taskValue)
	if err != nil {
		return "", "", err
	}
	return listID, taskID, nil
}

func graphTaskCollection(listID string) string {
	return "me/todo/lists/" + escaped(listID) + "/tasks"
}

func graphTaskResource(listID, taskID string) string {
	return graphTaskCollection(listID) + "/" + escaped(taskID)
}

type graphLinkedSourceEnvelope struct {
	Version int                          `json:"version"`
	Source  application.TaskLinkedSource `json:"source"`
}

func graphLinkedSource(remote graphLinkedResource) (application.TaskLinkedSource, bool, error) {
	if remote.ApplicationName != "Corresync" || !strings.HasPrefix(remote.ExternalID, "corr1_") {
		return application.TaskLinkedSource{}, false, nil
	}
	if len(remote.ExternalID) > 8192 {
		return application.TaskLinkedSource{}, false, errors.New("graph linked source metadata is malformed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(remote.ExternalID, "corr1_"))
	if err != nil || len(raw) > 4096 {
		return application.TaskLinkedSource{}, false, errors.New("graph linked source metadata is malformed")
	}
	var envelope graphLinkedSourceEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil || envelope.Version != 1 {
		return application.TaskLinkedSource{}, false, errors.New("graph linked source metadata is malformed")
	}
	if remote.WebURL != "" {
		envelope.Source.URL = remote.WebURL
	}
	if err := envelope.Source.Validate(); err != nil {
		return application.TaskLinkedSource{}, false, errors.New("graph linked source metadata is invalid")
	}
	return envelope.Source, true, nil
}
