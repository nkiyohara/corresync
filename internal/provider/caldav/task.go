package caldav

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	webcaldav "github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maxCalDAVTasks = application.MaxTaskPageOffset + application.MaxTaskPageSize

type taskListInfo struct {
	writable bool
	sync     bool
}

type taskListProperties struct {
	Responses []struct {
		Href      string `xml:"href"`
		PropStats []struct {
			Status string `xml:"status"`
			Prop   struct {
				SyncToken  string `xml:"sync-token"`
				Privileges struct {
					Values []struct {
						Write        *struct{} `xml:"write"`
						WriteContent *struct{} `xml:"write-content"`
					} `xml:"privilege"`
				} `xml:"current-user-privilege-set"`
				Reports struct {
					Values []struct {
						Report struct {
							SyncCollection *struct{} `xml:"sync-collection"`
						} `xml:"report"`
					} `xml:"supported-report"`
				} `xml:"supported-report-set"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (client *Client) discoverTaskListInfo(
	ctx context.Context,
	calendarHome string,
	lists []webcaldav.Calendar,
) (map[string]taskListInfo, error) {
	result := make(map[string]taskListInfo, len(lists))
	for _, list := range lists {
		result[list.Path] = taskListInfo{}
	}
	target := *client.endpoint
	target.Path = calendarHome
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	const body = `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:"><D:prop>` +
		`<D:current-user-privilege-set/><D:supported-report-set/><D:sync-token/>` +
		`</D:prop></D:propfind>`
	request, err := http.NewRequestWithContext(
		ctx, "PROPFIND", target.String(), strings.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	request.Header.Set("Depth", "1")
	response, err := (*authorizedHTTPClient)(client).Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusMultiStatus {
		switch response.StatusCode {
		case http.StatusForbidden, http.StatusNotFound,
			http.StatusMethodNotAllowed, http.StatusNotImplemented:
			return result, nil
		}
		return nil, fmt.Errorf(
			"CalDAV task capability discovery returned HTTP %d",
			response.StatusCode,
		)
	}
	var properties taskListProperties
	decoder := xml.NewDecoder(response.Body)
	if err := decoder.Decode(&properties); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(lists))
	for _, item := range properties.Responses {
		responsePath, ok := client.davPath(item.Href)
		if !ok || !pathWithin(responsePath, calendarHome) &&
			path.Clean(responsePath) != path.Clean(calendarHome) {
			return nil, errors.New("CalDAV task capability response escaped the calendar home")
		}
		matchedPath := ""
		for _, list := range lists {
			if path.Clean(responsePath) == path.Clean(list.Path) {
				matchedPath = list.Path
				break
			}
		}
		if matchedPath == "" {
			continue
		}
		if seen[matchedPath] {
			return nil, errors.New("CalDAV task capability discovery returned a duplicate list")
		}
		seen[matchedPath] = true
		var info taskListInfo
		for _, propstat := range item.PropStats {
			if !strings.Contains(propstat.Status, " 200 ") {
				continue
			}
			for _, privilege := range propstat.Prop.Privileges.Values {
				info.writable = info.writable ||
					privilege.Write != nil ||
					privilege.WriteContent != nil
			}
			for _, supported := range propstat.Prop.Reports.Values {
				info.sync = info.sync || supported.Report.SyncCollection != nil
			}
			if token := strings.TrimSpace(propstat.Prop.SyncToken); token != "" {
				if !validSyncToken(token) {
					return nil, errors.New("CalDAV task list returned a malformed sync token")
				}
				info.sync = true
			}
		}
		result[matchedPath] = info
	}
	return result, nil
}

func validSyncToken(value string) bool {
	if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.String() == value
}

// TaskCapabilities reports only properties established by authenticated
// collection discovery and the RFC 5545 mapping implemented below.
func (client *Client) TaskCapabilities() application.TaskCapabilities {
	if client == nil || len(client.taskLists) == 0 {
		return application.TaskCapabilities{}
	}
	writable := true
	syncAvailable := true
	for _, taskList := range client.taskLists {
		info := client.taskListInfo[taskList.Path]
		writable = writable && info.writable
		syncAvailable = syncAvailable && info.sync
	}
	capabilities := application.TaskCapabilities{
		Read: true, CrossListRead: true, Search: true,
		Create: writable, Update: writable, Complete: writable,
		Reopen: writable, Delete: writable,
		OptimisticConcurrency: writable,
		Reminders:             true,
		Recurrence:            true,
		Subtasks:              true,
		Labels:                true,
		DateOnly:              true,
		FloatingDateTime:      true,
		ZonedDateTime:         true,
	}
	if syncAvailable {
		capabilities.SyncModes = []application.TaskSyncMode{application.TaskSyncToken}
	}
	return capabilities
}

// TaskDegradations keeps synthetic protocol evidence distinct from a live
// server observation and explains conservative account-wide capabilities.
func (client *Client) TaskDegradations() []domain.Degradation {
	if client == nil || len(client.taskLists) == 0 {
		return nil
	}
	result := []domain.Degradation{{
		Feature: "tasks.compatibility",
		Reason:  "the CalDAV VTODO route is covered by synthetic RFC fixtures and remains live-unobserved for this server",
	}}
	for _, taskList := range client.taskLists {
		info := client.taskListInfo[taskList.Path]
		if !info.writable && !slices.ContainsFunc(result, func(value domain.Degradation) bool {
			return value.Feature == "tasks.write"
		}) {
			result = append(result, domain.Degradation{
				Feature: "tasks.write",
				Reason:  "at least one discovered VTODO collection did not advertise DAV write privilege; task writes are disabled for this route",
			})
		}
		if !info.sync && !slices.ContainsFunc(result, func(value domain.Degradation) bool {
			return value.Feature == "tasks.sync"
		}) {
			result = append(result, domain.Degradation{
				Feature: "tasks.sync",
				Reason:  "at least one discovered VTODO collection did not advertise RFC 6578 sync; incremental task sync is disabled for this route",
			})
		}
	}
	return result
}

type taskReference struct {
	List string `json:"list"`
	Path string `json:"path,omitempty"`
	UID  string `json:"uid"`
}

func encodeTaskListID(taskListPath string) (string, error) {
	if !validDAVPath(taskListPath) {
		return "", errors.New("CalDAV task list path is malformed")
	}
	return "cdtl1_" + base64.RawURLEncoding.EncodeToString([]byte(taskListPath)), nil
}

func decodeTaskListID(value string) (string, error) {
	if !strings.HasPrefix(value, "cdtl1_") {
		return "", errors.New("task list ID is not a CalDAV identifier")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "cdtl1_"))
	if err != nil || !validDAVPath(string(decoded)) {
		return "", errors.New("CalDAV task list ID is malformed")
	}
	return string(decoded), nil
}

func encodeTaskID(reference taskReference) (string, error) {
	if !validDAVPath(reference.List) ||
		reference.Path == "" && reference.UID == "" ||
		len(reference.UID) > 1024 || strings.ContainsAny(reference.UID, "\r\n\x00") ||
		reference.Path != "" &&
			(!validDAVPath(reference.Path) || !pathWithin(reference.Path, reference.List)) {
		return "", errors.New("CalDAV task reference is malformed")
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		return "", err
	}
	return "cdtt1_" + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeTaskID(value string) (taskReference, error) {
	if !strings.HasPrefix(value, "cdtt1_") {
		return taskReference{}, errors.New("task ID is not a CalDAV identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "cdtt1_"))
	if err != nil || len(data) > 4096 {
		return taskReference{}, errors.New("CalDAV task ID is malformed")
	}
	var reference taskReference
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		return taskReference{}, errors.New("CalDAV task ID is malformed")
	}
	if _, err := encodeTaskID(reference); err != nil {
		return taskReference{}, errors.New("CalDAV task ID is malformed")
	}
	return reference, nil
}

func (client *Client) taskListFor(value string) (string, error) {
	if value == "" {
		return client.taskListPath, nil
	}
	taskListPath, err := decodeTaskListID(value)
	if err != nil {
		return "", err
	}
	if !client.hasTaskList(taskListPath) {
		return "", errors.New("CalDAV task list was not discovered for this account")
	}
	return taskListPath, nil
}

func (client *Client) ListTaskLists(
	_ context.Context,
	input application.TaskListInput,
) (application.TaskListPage, error) {
	start := min(input.Offset, len(client.taskLists))
	end := min(start+input.Limit, len(client.taskLists))
	page := application.TaskListPage{
		Lists:  make([]application.TaskList, 0, end-start),
		Offset: input.Offset, Limit: input.Limit, HasMore: end < len(client.taskLists),
	}
	for _, remote := range client.taskLists[start:end] {
		id, err := encodeTaskListID(remote.Path)
		if err != nil {
			return application.TaskListPage{}, err
		}
		name := remote.Name
		if name == "" {
			name = remote.Path
		}
		page.Lists = append(page.Lists, application.TaskList{
			ID: id, DisplayName: name,
			Editable: client.taskListInfo[remote.Path].writable,
			Default:  remote.Path == client.taskListPath,
		})
	}
	return page, nil
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
) (application.TaskPage, error) {
	tasks, err := client.readTaskViews(ctx, input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	if input.Status != "" {
		tasks = slices.DeleteFunc(tasks, func(task application.Task) bool {
			return task.Status != input.Status
		})
	}
	return taskPage(tasks, input.Offset, input.Limit), nil
}

func (client *Client) SearchTasks(
	ctx context.Context,
	input application.TaskSearchInput,
) (application.TaskPage, error) {
	tasks, err := client.readTaskViews(ctx, input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	query := strings.ToLower(input.Query)
	tasks = slices.DeleteFunc(tasks, func(task application.Task) bool {
		if strings.Contains(strings.ToLower(task.Title), query) ||
			strings.Contains(strings.ToLower(task.Notes), query) {
			return false
		}
		return !slices.ContainsFunc(task.Labels, func(label string) bool {
			return strings.Contains(strings.ToLower(label), query)
		})
	})
	return taskPage(tasks, input.Offset, input.Limit), nil
}

func taskPage(
	tasks []application.Task,
	offset, limit int,
) application.TaskPage {
	start := min(offset, len(tasks))
	end := min(start+limit, len(tasks))
	return application.TaskPage{
		Tasks:  append([]application.Task(nil), tasks[start:end]...),
		Offset: offset, Limit: limit, HasMore: end < len(tasks),
	}
}

func (client *Client) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
) (application.Task, error) {
	taskListPath, err := client.taskListFor(input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	reference, err := decodeTaskID(input.TaskID)
	if err != nil || reference.List != taskListPath {
		return application.Task{}, errors.New("CalDAV task does not belong to the selected list")
	}
	if reference.Path == "" {
		objects, err := client.queryTaskObjects(ctx, taskListPath)
		if err != nil {
			return application.Task{}, err
		}
		for index := range objects {
			component, componentErr := taskMaster(objects[index].Data, reference.UID)
			if componentErr == nil {
				return client.taskView(taskListPath, objects[index], component)
			}
			if !errors.Is(componentErr, errTaskMasterNotFound) {
				return application.Task{}, componentErr
			}
		}
		return application.Task{}, errors.New("CalDAV related task was not found")
	}
	object, err := client.dav.GetCalendarObject(ctx, reference.Path)
	if err != nil {
		return application.Task{}, err
	}
	if !validObjectETag(object.ETag) || !pathWithin(object.Path, taskListPath) {
		return application.Task{}, errors.New("CalDAV task object is malformed")
	}
	component, err := taskMaster(object.Data, reference.UID)
	if err != nil {
		return application.Task{}, err
	}
	return client.taskView(taskListPath, *object, component)
}

func (client *Client) readTaskViews(
	ctx context.Context,
	listID string,
) ([]application.Task, error) {
	lists := client.taskLists
	if listID != "" {
		selected, err := client.taskListFor(listID)
		if err != nil {
			return nil, err
		}
		lists = []webcaldav.Calendar{{Path: selected}}
	}
	tasks := make([]application.Task, 0)
	for _, taskList := range lists {
		objects, err := client.queryTaskObjects(ctx, taskList.Path)
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			component, err := taskMaster(object.Data, "")
			if err != nil {
				return nil, err
			}
			view, err := client.taskView(taskList.Path, object, component)
			if err != nil {
				return nil, err
			}
			tasks = append(tasks, view)
			if len(tasks) > maxCalDAVTasks {
				return nil, errors.New("CalDAV task query exceeds the configured limit")
			}
		}
	}
	sort.Slice(tasks, func(left, right int) bool {
		if tasks[left].ListID != tasks[right].ListID {
			return tasks[left].ListID < tasks[right].ListID
		}
		return tasks[left].ID < tasks[right].ID
	})
	return tasks, nil
}

func (client *Client) queryTaskObjects(
	ctx context.Context,
	taskListPath string,
) ([]webcaldav.CalendarObject, error) {
	objects, err := client.dav.QueryCalendar(ctx, taskListPath, &webcaldav.CalendarQuery{
		CompRequest: webcaldav.CalendarCompRequest{
			Name: ical.CompCalendar,
			Comps: []webcaldav.CalendarCompRequest{{
				Name: ical.CompToDo, AllProps: true,
			}},
		},
		CompFilter: webcaldav.CompFilter{
			Name:  ical.CompCalendar,
			Comps: []webcaldav.CompFilter{{Name: ical.CompToDo}},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objects) > maxCalDAVTasks {
		return nil, errors.New("CalDAV task collection exceeds the configured limit")
	}
	for index := range objects {
		if !validDAVPath(objects[index].Path) ||
			!pathWithin(objects[index].Path, taskListPath) ||
			!validObjectETag(objects[index].ETag) {
			return nil, errors.New("CalDAV task query returned an invalid object")
		}
	}
	sort.Slice(objects, func(left, right int) bool {
		return objects[left].Path < objects[right].Path
	})
	return objects, nil
}

var errTaskMasterNotFound = errors.New("CalDAV task object contains no matching VTODO master")

func taskMaster(calendar *ical.Calendar, uid string) (*ical.Component, error) {
	if calendar == nil {
		return nil, errors.New("CalDAV task object contains no calendar data")
	}
	var master *ical.Component
	for _, child := range calendar.Children {
		if child.Name != ical.CompToDo || child.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		childUID, err := child.Props.Text(ical.PropUID)
		if err != nil || childUID == "" {
			return nil, errors.New("CalDAV VTODO UID is malformed")
		}
		if uid != "" && childUID != uid {
			continue
		}
		if master != nil {
			return nil, errors.New("CalDAV task object contains multiple recurrence masters")
		}
		master = child
	}
	if master == nil {
		return nil, errTaskMasterNotFound
	}
	return master, nil
}

func (client *Client) taskView(
	taskListPath string,
	object webcaldav.CalendarObject,
	task *ical.Component,
) (application.Task, error) {
	uid, err := task.Props.Text(ical.PropUID)
	if err != nil || uid == "" {
		return application.Task{}, errors.New("CalDAV VTODO UID is missing")
	}
	id, err := encodeTaskID(taskReference{List: taskListPath, Path: object.Path})
	if err != nil {
		return application.Task{}, err
	}
	listID, err := encodeTaskListID(taskListPath)
	if err != nil {
		return application.Task{}, err
	}
	title, err := task.Props.Text(ical.PropSummary)
	if err != nil {
		return application.Task{}, err
	}
	degradations := make([]domain.Degradation, 0, 4)
	if title == "" {
		title = "Untitled task"
		degradations = append(degradations, domain.Degradation{
			Feature: "tasks.title",
			Reason:  "the VTODO omitted SUMMARY; Corresync displays a local placeholder without writing it back",
		})
	}
	notes, err := task.Props.Text(ical.PropDescription)
	if err != nil {
		return application.Task{}, err
	}
	status, completedAt, statusDegradations, err := calDAVTaskStatus(task)
	if err != nil {
		return application.Task{}, err
	}
	degradations = append(degradations, statusDegradations...)
	priority, err := calDAVTaskPriority(task.Props.Get(ical.PropPriority))
	if err != nil {
		return application.Task{}, err
	}
	start, err := calDAVTaskTemporal(task.Props.Get(ical.PropDateTimeStart))
	if err != nil {
		return application.Task{}, fmt.Errorf("parse VTODO DTSTART: %w", err)
	}
	due, err := calDAVTaskTemporal(task.Props.Get(ical.PropDue))
	if err != nil {
		return application.Task{}, fmt.Errorf("parse VTODO DUE: %w", err)
	}
	degradations = append(
		degradations,
		calDAVTaskTemporalDegradations(task, start, due)...,
	)
	reminders, reminderDegradations, err := calDAVTaskReminders(task.Children)
	if err != nil {
		return application.Task{}, err
	}
	degradations = append(degradations, reminderDegradations...)
	recurrence, err := calDAVTaskRecurrence(task.Props.Get(ical.PropRecurrenceRule))
	if err != nil {
		return application.Task{}, err
	}
	labels, err := calDAVTaskLabels(task.Props.Values(ical.PropCategories))
	if err != nil {
		return application.Task{}, err
	}
	parentID, relationshipDegradations, err := calDAVTaskParent(taskListPath, task)
	if err != nil {
		return application.Task{}, err
	}
	degradations = append(degradations, relationshipDegradations...)
	degradations = append(degradations, unsupportedTaskPropertyDegradations(task)...)
	return application.Task{
		ID: id, Version: object.ETag, ListID: listID, ParentID: parentID,
		Title: title, Notes: notes, Status: status, Priority: priority,
		Start: start, Due: due, CompletedAt: completedAt,
		Reminders: reminders, Recurrence: recurrence, Labels: labels,
		Degradations: degradations,
	}, nil
}

func calDAVTaskTemporalDegradations(
	task *ical.Component,
	start, due *application.TaskTemporal,
) []domain.Degradation {
	if err := validateCalDAVTaskTemporalContract(task, start, due); err == nil {
		return nil
	}
	return []domain.Degradation{{
		Feature: "tasks.temporal_consistency",
		Reason:  "the VTODO contains a non-portable DTSTART, DUE, DURATION, or recurrence combination that remains preserved only on exact unrelated updates",
	}}
}

func calDAVTaskStatus(
	task *ical.Component,
) (application.TaskStatus, *application.TaskTemporal, []domain.Degradation, error) {
	value, err := task.Props.Text(ical.PropStatus)
	if err != nil {
		return "", nil, nil, err
	}
	var status application.TaskStatus
	switch strings.ToUpper(value) {
	case "", "NEEDS-ACTION":
		status = application.TaskStatusNeedsAction
	case "IN-PROCESS":
		status = application.TaskStatusInProgress
	case "COMPLETED":
		status = application.TaskStatusCompleted
	case "CANCELLED":
		status = application.TaskStatusCancelled
	default:
		return "", nil, nil, errors.New("CalDAV VTODO status is unsupported")
	}
	completed, err := calDAVTaskTemporal(task.Props.Get(ical.PropCompleted))
	if err != nil {
		return "", nil, nil, err
	}
	degradations := []domain.Degradation(nil)
	if status == application.TaskStatusCompleted && completed == nil {
		degradations = append(degradations, domain.Degradation{
			Feature: "completion_time",
			Reason:  "the completed VTODO omitted its COMPLETED timestamp",
		})
	}
	if status != application.TaskStatusCompleted {
		completed = nil
	}
	return status, completed, degradations, nil
}

func calDAVTaskPriority(property *ical.Prop) (application.TaskPriority, error) {
	if property == nil {
		return application.TaskPriorityNone, nil
	}
	value, err := property.Int()
	if err != nil || value < 0 || value > 9 {
		return "", errors.New("CalDAV VTODO priority is malformed")
	}
	switch {
	case value == 0:
		return application.TaskPriorityNone, nil
	case value == 1:
		return application.TaskPriorityUrgent, nil
	case value <= 4:
		return application.TaskPriorityHigh, nil
	case value == 5:
		return application.TaskPriorityNormal, nil
	default:
		return application.TaskPriorityLow, nil
	}
}

func calDAVTaskTemporal(property *ical.Prop) (*application.TaskTemporal, error) {
	if property == nil {
		return nil, nil
	}
	if property.ValueType() == ical.ValueDate || len(property.Value) == len("20060102") {
		parsed, err := time.Parse("20060102", property.Value)
		if err != nil {
			return nil, err
		}
		return &application.TaskTemporal{
			Kind: application.TaskTemporalDate, Value: parsed.Format(time.DateOnly),
		}, nil
	}
	zone := property.Params.Get(ical.ParamTimezoneID)
	if zone == "" && !strings.HasSuffix(property.Value, "Z") {
		parsed, err := time.Parse("20060102T150405", property.Value)
		if err != nil {
			return nil, err
		}
		return &application.TaskTemporal{
			Kind:  application.TaskTemporalFloating,
			Value: parsed.Format("2006-01-02T15:04:05"),
		}, nil
	}
	parsed, err := property.DateTime(time.UTC)
	if err != nil {
		return nil, err
	}
	if zone == "" {
		zone = "UTC"
	}
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: parsed.Format(time.RFC3339), TimeZone: zone,
	}, nil
}

func calDAVTaskRecurrence(property *ical.Prop) (*application.TaskRecurrence, error) {
	if property == nil {
		return nil, nil
	}
	if len(property.Value) > 4096 || strings.ContainsAny(property.Value, "\r\n\x00") {
		return nil, errors.New("CalDAV VTODO recurrence is malformed")
	}
	component := ical.NewComponent(ical.CompToDo)
	component.Props.Set(property)
	if _, err := component.Props.RecurrenceRule(); err != nil {
		return nil, err
	}
	return &application.TaskRecurrence{
		Frequency:    application.TaskRecurrenceProvider,
		ProviderRule: property.Value,
	}, nil
}

func calDAVTaskLabels(properties []ical.Prop) ([]string, error) {
	labels := make([]string, 0)
	seen := make(map[string]bool)
	for index := range properties {
		values, err := properties[index].TextList()
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			key := strings.ToLower(value)
			if !seen[key] {
				labels = append(labels, value)
				seen[key] = true
			}
			if len(labels) > application.MaxTaskCollectionEntries {
				return nil, errors.New("CalDAV VTODO has too many categories")
			}
		}
	}
	return labels, nil
}

func calDAVTaskParent(
	taskListPath string,
	task *ical.Component,
) (string, []domain.Degradation, error) {
	var parentUID string
	degraded := false
	for _, property := range task.Props.Values(ical.PropRelatedTo) {
		relation := strings.ToUpper(property.Params.Get(ical.ParamRelationshipType))
		if relation != "" && relation != "PARENT" {
			degraded = true
			continue
		}
		uid, err := property.Text()
		if err != nil || uid == "" {
			return "", nil, errors.New("CalDAV VTODO relationship is malformed")
		}
		if parentUID != "" && parentUID != uid {
			return "", nil, errors.New("CalDAV VTODO has multiple parents")
		}
		parentUID = uid
	}
	parentID := ""
	if parentUID != "" {
		var err error
		parentID, err = encodeTaskID(taskReference{List: taskListPath, UID: parentUID})
		if err != nil {
			return "", nil, err
		}
	}
	if degraded {
		return parentID, []domain.Degradation{{
			Feature: "tasks.relationships",
			Reason:  "the VTODO contains a non-parent RELATED-TO relationship that remains preserved only on exact updates",
		}}, nil
	}
	return parentID, nil, nil
}

func calDAVTaskReminders(
	children []*ical.Component,
) ([]application.TaskReminder, []domain.Degradation, error) {
	reminders := make([]application.TaskReminder, 0)
	degraded := false
	for _, child := range children {
		if child.Name != ical.CompAlarm {
			continue
		}
		reminder, representable, err := calDAVTaskReminder(child)
		if err != nil {
			return nil, nil, err
		}
		if !representable {
			degraded = true
			continue
		}
		reminders = append(reminders, reminder)
		if len(reminders) > application.MaxTaskCollectionEntries {
			return nil, nil, errors.New("CalDAV VTODO has too many alarms")
		}
	}
	if degraded {
		return reminders, []domain.Degradation{{
			Feature: "tasks.reminders",
			Reason:  "at least one VTODO alarm cannot be represented without loss and remains preserved only on exact updates",
		}}, nil
	}
	return reminders, nil, nil
}

func calDAVTaskReminder(
	alarm *ical.Component,
) (application.TaskReminder, bool, error) {
	if alarm == nil || alarm.Name != ical.CompAlarm ||
		!canonicalCalDAVAlarmProperties(alarm.Props) {
		return application.TaskReminder{}, false, nil
	}
	action, err := alarm.Props.Text(ical.PropAction)
	if err != nil {
		return application.TaskReminder{}, false, err
	}
	if !strings.EqualFold(action, "DISPLAY") {
		return application.TaskReminder{}, false, nil
	}
	trigger := alarm.Props.Get(ical.PropTrigger)
	if trigger == nil {
		return application.TaskReminder{}, false, nil
	}
	if trigger.ValueType() == ical.ValueDateTime {
		if trigger.Params.Get(ical.ParamRelated) != "" ||
			trigger.Params.Get(ical.ParamTimezoneID) != "" ||
			!strings.HasSuffix(trigger.Value, "Z") {
			return application.TaskReminder{}, false, nil
		}
		at, err := calDAVTaskTemporal(trigger)
		if err != nil || at == nil || at.Kind == application.TaskTemporalDate {
			return application.TaskReminder{}, false,
				errors.New("CalDAV VTODO absolute alarm is malformed")
		}
		return application.TaskReminder{
			Kind: application.TaskReminderAbsolute, At: at,
		}, true, nil
	}
	duration, durationErr := trigger.Duration()
	if durationErr == nil && duration%time.Minute == 0 {
		var kind application.TaskReminderKind
		switch strings.ToUpper(trigger.Params.Get(ical.ParamRelated)) {
		case "", "START":
			kind = application.TaskReminderRelativeStart
		case "END":
			kind = application.TaskReminderRelativeDue
		default:
			return application.TaskReminder{}, false, nil
		}
		return application.TaskReminder{
			Kind: kind, OffsetMinutes: int(duration / time.Minute),
		}, true, nil
	}
	return application.TaskReminder{}, false, nil
}

func canonicalCalDAVAlarmProperties(properties ical.Props) bool {
	for name, values := range properties {
		switch name {
		case ical.PropAction, ical.PropDescription, ical.PropTrigger:
			if len(values) != 1 {
				return false
			}
		default:
			return false
		}
	}
	return properties.Get(ical.PropAction) != nil &&
		properties.Get(ical.PropTrigger) != nil
}

func unsupportedTaskPropertyDegradations(task *ical.Component) []domain.Degradation {
	recognized := map[string]bool{
		ical.PropUID: true, ical.PropDateTimeStamp: true,
		ical.PropCreated: true, ical.PropLastModified: true,
		ical.PropSequence: true, ical.PropSummary: true,
		ical.PropDescription: true, ical.PropStatus: true,
		ical.PropPercentComplete: true, ical.PropPriority: true,
		ical.PropDateTimeStart: true, ical.PropDue: true,
		ical.PropCompleted: true, ical.PropRecurrenceRule: true,
		ical.PropRecurrenceID: true, ical.PropRelatedTo: true,
		ical.PropCategories: true,
	}
	unsupported := slices.ContainsFunc(task.Children, func(child *ical.Component) bool {
		return child.Name != ical.CompAlarm
	})
	for name := range task.Props {
		if !recognized[name] {
			unsupported = true
			break
		}
	}
	result := make([]domain.Degradation, 0, 2)
	if task.Props.Get(ical.PropPercentComplete) != nil {
		result = append(result, domain.Degradation{
			Feature: "tasks.percent_complete",
			Reason:  "VTODO PERCENT-COMPLETE remains preserved on exact updates but is not represented by the canonical task model",
		})
	}
	if unsupported {
		result = append(result, domain.Degradation{
			Feature: "tasks.provider_extensions",
			Reason:  "the VTODO contains unsupported standard or extension properties that remain preserved only on exact updates",
		})
	}
	return result
}

func calDAVTaskPriorityValue(priority application.TaskPriority) (int, error) {
	switch priority {
	case application.TaskPriorityNone:
		return 0, nil
	case application.TaskPriorityLow:
		return 7, nil
	case application.TaskPriorityNormal:
		return 5, nil
	case application.TaskPriorityHigh:
		return 3, nil
	case application.TaskPriorityUrgent:
		return 1, nil
	default:
		return 0, errors.New("task priority is invalid")
	}
}

func setCalDAVTaskPriority(task *ical.Component, priority application.TaskPriority) error {
	value, err := calDAVTaskPriorityValue(priority)
	if err != nil {
		return err
	}
	property := ical.NewProp(ical.PropPriority)
	property.SetValueType(ical.ValueInt)
	property.Value = strconv.Itoa(value)
	task.Props.Set(property)
	return nil
}
