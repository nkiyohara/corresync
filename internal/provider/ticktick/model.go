package ticktick

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumProviderCollection = 256

type project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Closed     bool   `json:"closed"`
	Permission string `json:"permission"`
	Kind       string `json:"kind"`
	SortOrder  int64  `json:"sortOrder"`
}

type checklistItem struct {
	ID            string `json:"id,omitempty"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	CompletedTime string `json:"completedTime,omitempty"`
	IsAllDay      bool   `json:"isAllDay,omitempty"`
	SortOrder     int64  `json:"sortOrder,omitempty"`
	StartDate     string `json:"startDate,omitempty"`
	TimeZone      string `json:"timeZone,omitempty"`
}

type task struct {
	ID               string            `json:"id"`
	ProjectID        string            `json:"projectId"`
	Title            string            `json:"title"`
	Content          string            `json:"content"`
	Description      string            `json:"desc"`
	IsAllDay         bool              `json:"isAllDay"`
	StartDate        string            `json:"startDate"`
	DueDate          string            `json:"dueDate"`
	TimeZone         string            `json:"timeZone"`
	Reminders        []string          `json:"reminders"`
	Tags             []string          `json:"tags"`
	RepeatFlag       string            `json:"repeatFlag"`
	RepeatFrom       string            `json:"repeatFrom"`
	Priority         int               `json:"priority"`
	Status           int               `json:"status"`
	CompletedTime    string            `json:"completedTime"`
	SortOrder        int64             `json:"sortOrder"`
	AssigneeUsername string            `json:"assigneeUsername"`
	Kind             string            `json:"kind"`
	ParentID         string            `json:"parentId"`
	ETag             string            `json:"etag"`
	FocusSummaries   []json.RawMessage `json:"focusSummaries"`
	Items            []checklistItem   `json:"items"`
}

func (client *Client) taskView(listID string, remote task) (application.Task, error) {
	if err := validateTask(remote); err != nil {
		return application.Task{}, err
	}
	id, _ := encodeID("ttt1_", remote.ID)
	parentID := ""
	var err error
	if remote.ParentID != "" {
		parentID, err = encodeID("ttt1_", remote.ParentID)
		if err != nil {
			return application.Task{}, err
		}
	}
	status, err := readStatus(remote.Status)
	if err != nil {
		return application.Task{}, err
	}
	priority, err := readPriority(remote.Priority)
	if err != nil {
		return application.Task{}, err
	}
	start, err := readTemporal(remote.StartDate, remote.IsAllDay, remote.TimeZone)
	if err != nil {
		return application.Task{}, fmt.Errorf("ticktick task start: %w", err)
	}
	due, err := readTemporal(remote.DueDate, remote.IsAllDay, remote.TimeZone)
	if err != nil {
		return application.Task{}, fmt.Errorf("ticktick task due: %w", err)
	}
	completed, err := readCompleted(remote.CompletedTime)
	if err != nil {
		return application.Task{}, err
	}
	notes := remote.Content
	if remote.Kind == "CHECKLIST" {
		notes = remote.Description
	}
	result := application.Task{
		ID: id, Version: encodeVersion(remote), ListID: listID,
		ParentID: parentID, Title: remote.Title, Notes: notes,
		Status: status, Priority: priority, Start: start, Due: due,
		CompletedAt: completed, Labels: append([]string(nil), remote.Tags...),
		Order: strconv.FormatInt(remote.SortOrder, 10),
	}
	if result.Title == "" {
		result.Title = "Untitled TickTick task"
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.title", Reason: "an empty TickTick task title is displayed with a local placeholder", Lossy: true,
		})
	}
	if status == application.TaskStatusCompleted && completed == nil {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "completion_time", Reason: "TickTick marked the task completed without a completion timestamp",
		})
	}
	if status != application.TaskStatusCompleted && completed != nil {
		return application.Task{}, errors.New("ticktick returned a completion time for an incomplete task")
	}
	if remote.RepeatFlag != "" {
		if !strings.HasPrefix(remote.RepeatFlag, "RRULE:") {
			return application.Task{}, errors.New("ticktick returned an unsupported recurrence rule")
		}
		result.Recurrence = &application.TaskRecurrence{
			Frequency:    application.TaskRecurrenceProvider,
			ProviderRule: remote.RepeatFlag,
		}
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.recurrence", Reason: "TickTick recurrence is preserved as an exact provider rule",
		})
	}
	if len(remote.Reminders) != 0 {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.reminders", Reason: "TickTick reminder strings remain provider-owned",
		})
	}
	if remote.Kind == "NOTE" {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.kind", Reason: "the TickTick note kind is readable but not writable through the canonical task contract",
		})
	}
	if remote.Kind == "CHECKLIST" && remote.Content != "" && remote.Content != remote.Description ||
		remote.Kind != "CHECKLIST" && remote.Description != "" && remote.Description != remote.Content {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.notes", Reason: "TickTick returned distinct content and checklist description fields; the noncanonical field remains provider-owned", Lossy: true,
		})
	}
	if len(remote.FocusSummaries) != 0 {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.focus", Reason: "TickTick focus summaries are not represented by the canonical task contract",
		})
	}
	for _, item := range remote.Items {
		itemID, encodeErr := encodeID("tti1_", item.ID)
		if encodeErr != nil {
			return application.Task{}, encodeErr
		}
		result.Checklist = append(result.Checklist, application.TaskChecklistItem{
			ID: itemID, Title: item.Title, Completed: item.Status == 1,
			Order: strconv.FormatInt(item.SortOrder, 10),
		})
	}
	if remote.AssigneeUsername != "" {
		assigneeID, encodeErr := encodeID("tta1_", remote.AssigneeUsername)
		if encodeErr != nil {
			return application.Task{}, encodeErr
		}
		result.Assignees = []application.TaskAssignee{{ID: assigneeID}}
	}
	return result, nil
}

func validateTask(remote task) error {
	if !validProviderID(remote.ID) || !validProviderID(remote.ProjectID) ||
		remote.ParentID != "" && !validProviderID(remote.ParentID) ||
		remote.AssigneeUsername != "" && !validProviderText(remote.AssigneeUsername, 1024, false) ||
		!validProviderText(remote.Title, application.MaxTaskTitleBytes, true) ||
		!validProviderText(remote.Content, application.MaxTaskNotesBytes, true) ||
		!validProviderText(remote.Description, application.MaxTaskNotesBytes, true) ||
		len(remote.Tags) > application.MaxTaskCollectionEntries ||
		len(remote.Items) > application.MaxTaskCollectionEntries ||
		len(remote.Reminders) > maximumProviderCollection ||
		len(remote.FocusSummaries) > maximumProviderCollection {
		return errors.New("ticktick returned a malformed or oversized task")
	}
	switch remote.Kind {
	case "", "TEXT", "NOTE", "CHECKLIST":
	default:
		return errors.New("ticktick returned an unknown task kind")
	}
	for _, value := range append(append([]string{}, remote.Tags...), remote.Reminders...) {
		if !validProviderText(value, 4096, false) {
			return errors.New("ticktick returned a malformed task collection")
		}
	}
	for _, item := range remote.Items {
		if !validProviderID(item.ID) || !validProviderText(item.Title, application.MaxTaskTitleBytes, false) ||
			item.Status != 0 && item.Status != 1 {
			return errors.New("ticktick returned a malformed checklist item")
		}
	}
	return nil
}

func readStatus(value int) (application.TaskStatus, error) {
	switch value {
	case -1:
		return application.TaskStatusCancelled, nil
	case 0:
		return application.TaskStatusNeedsAction, nil
	case 2:
		return application.TaskStatusCompleted, nil
	default:
		return "", errors.New("ticktick returned an unknown task status")
	}
}

func readPriority(value int) (application.TaskPriority, error) {
	switch value {
	case 0:
		return application.TaskPriorityNone, nil
	case 1:
		return application.TaskPriorityLow, nil
	case 3:
		return application.TaskPriorityNormal, nil
	case 5:
		return application.TaskPriorityHigh, nil
	default:
		return "", errors.New("ticktick returned an unknown task priority")
	}
}

func writePriority(value application.TaskPriority) (int, error) {
	switch value {
	case application.TaskPriorityNone:
		return 0, nil
	case application.TaskPriorityLow:
		return 1, nil
	case application.TaskPriorityNormal:
		return 3, nil
	case application.TaskPriorityHigh:
		return 5, nil
	case application.TaskPriorityUrgent:
		return 0, errors.New("ticktick has no exact urgent-priority value")
	default:
		return 0, errors.New("task priority is invalid")
	}
}

func readTemporal(value string, allDay bool, zone string) (*application.TaskTemporal, error) {
	if value == "" {
		return nil, nil
	}
	instant, err := parseProviderTime(value)
	if err != nil {
		return nil, errors.New("datetime is malformed")
	}
	if allDay {
		date := value
		if len(date) >= len(time.DateOnly) {
			date = date[:len(time.DateOnly)]
		}
		parsed, dateErr := time.Parse(time.DateOnly, date)
		if dateErr != nil || parsed.Format(time.DateOnly) != date {
			return nil, errors.New("date-only value is malformed")
		}
		return &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: date}, nil
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" {
		return nil, errors.New("datetime has an unknown IANA time zone")
	}
	localized := instant.In(location)
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: localized.Format(time.RFC3339), TimeZone: zone,
	}, nil
}

func readCompleted(value string) (*application.TaskTemporal, error) {
	if value == "" {
		return nil, nil
	}
	instant, err := parseProviderTime(value)
	if err != nil {
		return nil, errors.New("ticktick task completion time is malformed")
	}
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: instant.UTC().Format(time.RFC3339), TimeZone: "UTC",
	}, nil
}

func parseProviderTime(value string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-0700",
		time.RFC3339Nano,
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid TickTick datetime")
}

func writeTemporal(value *application.TaskTemporal) (any, bool, string, error) {
	if value == nil {
		return nil, false, "", nil
	}
	if err := value.Validate(); err != nil {
		return nil, false, "", err
	}
	switch value.Kind {
	case application.TaskTemporalDate:
		return value.Value + "T00:00:00+0000", true, "", nil
	case application.TaskTemporalZoned:
		instant, _ := time.Parse(time.RFC3339, value.Value)
		return instant.Format("2006-01-02T15:04:05-0700"), false, value.TimeZone, nil
	case application.TaskTemporalFloating:
		return nil, false, "", errors.New("ticktick does not support floating task datetimes")
	default:
		return nil, false, "", errors.New("task datetime kind is invalid")
	}
}

func validateTemporalPair(start, due *application.TaskTemporal, defaultZone string) (map[string]any, error) {
	startValue, startAllDay, startZone, err := writeTemporal(start)
	if err != nil {
		return nil, err
	}
	dueValue, dueAllDay, dueZone, err := writeTemporal(due)
	if err != nil {
		return nil, err
	}
	if start != nil && due != nil && startAllDay != dueAllDay {
		return nil, errors.New("ticktick cannot mix date-only and zoned start/due values on one task")
	}
	allDay := start != nil && startAllDay || due != nil && dueAllDay
	zone := startZone
	if zone == "" {
		zone = dueZone
	}
	if startZone != "" && dueZone != "" && startZone != dueZone {
		return nil, errors.New("ticktick requires one IANA time zone for both start and due")
	}
	if zone == "" {
		zone = defaultZone
	}
	return map[string]any{
		"startDate": startValue, "dueDate": dueValue,
		"isAllDay": allDay, "timeZone": zone,
	}, nil
}

func encodeVersion(remote task) string {
	if validETag(remote.ETag) {
		return "ttv1_" + base64.RawURLEncoding.EncodeToString([]byte(remote.ETag))
	}
	copy := remote
	copy.ETag = ""
	raw, _ := json.Marshal(copy)
	digest := sha256.Sum256(raw)
	return "ttd1_" + hex.EncodeToString(digest[:])
}

func encodeID(prefix, value string) (string, error) {
	if !validProviderID(value) {
		return "", errors.New("ticktick identifier is malformed")
	}
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(value)), nil
}

func decodeID(prefix, value string) (string, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("identifier does not belong to the TickTick route")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || !validProviderID(string(decoded)) {
		return "", errors.New("ticktick identifier is malformed")
	}
	return string(decoded), nil
}

func decodeRoute(listValue, taskValue string) (string, string, error) {
	listID, err := decodeID("ttl1_", listValue)
	if err != nil {
		return "", "", err
	}
	taskID, err := decodeID("ttt1_", taskValue)
	if err != nil {
		return "", "", err
	}
	return listID, taskID, nil
}

func validProviderID(value string) bool {
	return value != "" && len(value) <= 2048 && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/?#\r\n\x00")
}

func validETag(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\r\n\x00")
}

func validProviderText(value string, maximumBytes int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && len(value) <= maximumBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func taskResource(projectID, taskID string) string {
	return "open/v1/project/" + url.PathEscape(projectID) + "/task/" + url.PathEscape(taskID)
}

func writeAssemblyError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: assemble TickTick write result: %w", application.ErrWriteOutcomeUnknown, err)
}
