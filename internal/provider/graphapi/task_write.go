package graphapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func (client *Client) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
) (application.Task, error) {
	if err := client.requireTaskWrite(); err != nil {
		return application.Task{}, err
	}
	listID, err := decodeTaskListID(input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	if err := validateGraphChecklistCreate(input.Checklist); err != nil {
		return application.Task{}, err
	}
	payload, err := graphTaskCreatePayload(input)
	if err != nil {
		return application.Task{}, err
	}
	var created graphTask
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, graphTaskCollection(listID), nil,
		payload, &created, true, nil, http.StatusCreated,
	); err != nil {
		return application.Task{}, err
	}
	if !validGraphID(created.ID) || !validETag(created.ODataETag) {
		return application.Task{}, fmt.Errorf(
			"%w: graph task creation omitted its identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	if len(input.Checklist) != 0 {
		if err := client.createGraphChecklist(ctx, listID, created.ID, input.Checklist); err != nil {
			return application.Task{}, taskAssemblyError(err)
		}
	}
	current, err := client.getGraphTask(ctx, listID, created.ID)
	if err != nil {
		return application.Task{}, taskAssemblyError(err)
	}
	return graphTaskView(input.ListID, current)
}

func (client *Client) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
) (application.Task, error) {
	if err := client.requireTaskWrite(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeTaskRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return application.Task{}, err
	}
	current, err := client.exactGraphTask(ctx, listID, taskID, etag)
	if err != nil {
		return application.Task{}, err
	}
	var checklistReplacement *graphChecklistReplacement
	if input.ReplaceChecklist {
		prepared, prepareErr := prepareGraphChecklistReplacement(current.ChecklistItems, input.Checklist)
		if prepareErr != nil {
			return application.Task{}, prepareErr
		}
		checklistReplacement = &prepared
	}
	var sourceReplacement *graphSourceReplacement
	if input.ReplaceSources {
		prepared, prepareErr := prepareGraphSourceReplacement(current.LinkedResources, input.Sources)
		if prepareErr != nil {
			return application.Task{}, prepareErr
		}
		sourceReplacement = &prepared
	}
	payload, err := graphTaskUpdatePayload(input, current)
	if err != nil {
		return application.Task{}, err
	}
	if len(payload) != 0 {
		var updated graphTask
		if _, err := client.api.DoJSON(
			ctx, http.MethodPatch, graphTaskResource(listID, taskID), nil,
			payload, &updated, true, http.Header{"If-Match": {etag}}, http.StatusOK,
		); err != nil {
			return application.Task{}, err
		}
		if updated.ID != taskID || !validETag(updated.ODataETag) {
			return application.Task{}, fmt.Errorf(
				"%w: graph task update omitted its current identity",
				application.ErrWriteOutcomeUnknown,
			)
		}
		updated.ChecklistItems = current.ChecklistItems
		updated.LinkedResources = current.LinkedResources
		current = updated
	}
	if input.ReplaceChecklist {
		if err := client.applyGraphChecklistReplacement(ctx, listID, taskID, *checklistReplacement); err != nil {
			return application.Task{}, taskAssemblyError(err)
		}
	}
	if input.ReplaceSources {
		if err := client.applyGraphSourceReplacement(ctx, listID, taskID, *sourceReplacement); err != nil {
			return application.Task{}, taskAssemblyError(err)
		}
	}
	current, err = client.getGraphTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, taskAssemblyError(err)
	}
	return graphTaskView(input.ListID, current)
}

func (client *Client) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	return client.setGraphTaskStatus(ctx, input, "completed")
}

func (client *Client) ReopenTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	return client.setGraphTaskStatus(ctx, input, "notStarted")
}

func (client *Client) setGraphTaskStatus(
	ctx context.Context,
	input application.TaskStateInput,
	status string,
) (application.Task, error) {
	if err := client.requireTaskWrite(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeTaskRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if _, err := client.exactGraphTask(ctx, listID, taskID, etag); err != nil {
		return application.Task{}, err
	}
	var updated graphTask
	if _, err := client.api.DoJSON(
		ctx, http.MethodPatch, graphTaskResource(listID, taskID), nil,
		map[string]any{"status": status}, &updated, true,
		http.Header{"If-Match": {etag}}, http.StatusOK,
	); err != nil {
		return application.Task{}, err
	}
	if updated.ID != taskID || !validETag(updated.ODataETag) {
		return application.Task{}, fmt.Errorf(
			"%w: graph task state update omitted its current identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	current, err := client.getGraphTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, taskAssemblyError(err)
	}
	return graphTaskView(input.ListID, current)
}

func (client *Client) DeleteTask(ctx context.Context, input application.TaskDeleteInput) error {
	if err := client.requireTaskWrite(); err != nil {
		return err
	}
	listID, taskID, err := decodeTaskRoute(input.ListID, input.TaskID)
	if err != nil {
		return err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return err
	}
	if _, err := client.exactGraphTask(ctx, listID, taskID, etag); err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx, http.MethodDelete, graphTaskResource(listID, taskID), nil,
		nil, nil, true, http.Header{"If-Match": {etag}}, http.StatusNoContent,
	)
	return err
}

func (client *Client) exactGraphTask(
	ctx context.Context,
	listID, taskID, expectedETag string,
) (graphTask, error) {
	current, err := client.getGraphTask(ctx, listID, taskID)
	if err != nil {
		return graphTask{}, err
	}
	if current.ID != taskID || current.ODataETag != expectedETag {
		return graphTask{}, restapi.ErrPrecondition
	}
	return current, nil
}

func (client *Client) requireTaskWrite() error {
	if err := client.requireTaskRead(); err != nil {
		return err
	}
	if !client.taskWrite {
		return errors.New("the Microsoft To Do route was authorized read-only")
	}
	return nil
}

func (client *Client) requireTaskRead() error {
	if client == nil || !client.tasks {
		return errors.New("the Microsoft To Do service is not enabled for this Graph client")
	}
	return nil
}

func graphTaskCreatePayload(input application.TaskCreateInput) (map[string]any, error) {
	payload := map[string]any{"title": input.Title}
	if input.Notes != "" {
		payload["body"] = graphTaskBody(input.Notes)
	}
	importance, err := graphWritePriority(input.Priority)
	if err != nil {
		return nil, err
	}
	payload["importance"] = importance
	if input.Start != nil {
		value, convertErr := graphWriteTaskTime(input.Start)
		if convertErr != nil {
			return nil, convertErr
		}
		payload["startDateTime"] = value
	}
	if input.Due != nil {
		value, convertErr := graphWriteTaskTime(input.Due)
		if convertErr != nil {
			return nil, convertErr
		}
		payload["dueDateTime"] = value
	}
	if err := applyGraphReminder(payload, input.Reminders); err != nil {
		return nil, err
	}
	if input.Recurrence != nil {
		recurrence, recurrenceErr := graphWriteTaskRecurrence(input.Recurrence, input.Start, input.Due)
		if recurrenceErr != nil {
			return nil, recurrenceErr
		}
		payload["recurrence"] = recurrence
	}
	if len(input.Labels) != 0 {
		payload["categories"] = input.Labels
	}
	if len(input.Sources) != 0 {
		sources, sourceErr := graphWriteSources(input.Sources)
		if sourceErr != nil {
			return nil, sourceErr
		}
		payload["linkedResources"] = sources
	}
	return payload, nil
}

func graphTaskUpdatePayload(
	input application.TaskUpdateInput,
	current graphTask,
) (map[string]any, error) {
	payload := make(map[string]any)
	if input.Title != nil {
		payload["title"] = *input.Title
	}
	if input.Notes != nil {
		payload["body"] = graphTaskBody(*input.Notes)
	}
	if input.Priority != nil {
		importance, err := graphWritePriority(*input.Priority)
		if err != nil {
			return nil, err
		}
		payload["importance"] = importance
	}
	if input.ReplaceStart {
		payload["startDateTime"] = nil
		if input.Start != nil {
			value, err := graphWriteTaskTime(input.Start)
			if err != nil {
				return nil, err
			}
			payload["startDateTime"] = value
		}
	}
	if input.ReplaceDue {
		payload["dueDateTime"] = nil
		if input.Due != nil {
			value, err := graphWriteTaskTime(input.Due)
			if err != nil {
				return nil, err
			}
			payload["dueDateTime"] = value
		}
	}
	if input.ReplaceReminders {
		if err := applyGraphReminder(payload, input.Reminders); err != nil {
			return nil, err
		}
	}
	if input.ReplaceRecurrence {
		payload["recurrence"] = nil
		if input.Recurrence != nil {
			start := input.Start
			due := input.Due
			if start == nil && !input.ReplaceStart {
				var timeErr error
				start, timeErr = graphTaskTime(current.Start)
				if timeErr != nil {
					return nil, timeErr
				}
			}
			if due == nil && !input.ReplaceDue {
				var timeErr error
				due, timeErr = graphTaskTime(current.Due)
				if timeErr != nil {
					return nil, timeErr
				}
			}
			recurrence, err := graphWriteTaskRecurrence(input.Recurrence, start, due)
			if err != nil {
				return nil, err
			}
			payload["recurrence"] = recurrence
		}
	}
	if input.ReplaceLabels {
		payload["categories"] = input.Labels
	}
	return payload, nil
}

func graphTaskBody(value string) map[string]string {
	return map[string]string{
		"contentType": "text",
		"content":     value,
	}
}

func graphWritePriority(value application.TaskPriority) (string, error) {
	switch value {
	case application.TaskPriorityNone, application.TaskPriorityNormal:
		return "normal", nil
	case application.TaskPriorityLow:
		return "low", nil
	case application.TaskPriorityHigh:
		return "high", nil
	case application.TaskPriorityUrgent:
		return "", errors.New("urgent priority is unsupported by Microsoft To Do")
	default:
		return "", errors.New("task priority is invalid")
	}
}

func graphWriteStatus(value application.TaskStatus) (string, error) {
	switch value {
	case application.TaskStatusNeedsAction:
		return "notStarted", nil
	case application.TaskStatusInProgress:
		return "inProgress", nil
	case application.TaskStatusCompleted:
		return "completed", nil
	case application.TaskStatusCancelled:
		return "", errors.New("cancelled task status is unsupported by Microsoft To Do")
	default:
		return "", errors.New("task status is invalid")
	}
}

func graphWriteTaskTime(value *application.TaskTemporal) (graphDateTimeZone, error) {
	if value == nil || value.Kind != application.TaskTemporalZoned {
		return graphDateTimeZone{}, errors.New("a Microsoft To Do datetime must be zoned")
	}
	instant, err := time.Parse(time.RFC3339, value.Value)
	if err != nil {
		return graphDateTimeZone{}, errors.New("task datetime is malformed")
	}
	location, err := time.LoadLocation(value.TimeZone)
	if err != nil {
		return graphDateTimeZone{}, errors.New("task time zone is not an installed IANA name")
	}
	return graphDateTimeZone{
		DateTime: instant.In(location).Format("2006-01-02T15:04:05"),
		TimeZone: value.TimeZone,
	}, nil
}

func applyGraphReminder(payload map[string]any, reminders []application.TaskReminder) error {
	if len(reminders) == 0 {
		payload["isReminderOn"] = false
		payload["reminderDateTime"] = nil
		return nil
	}
	if len(reminders) != 1 || reminders[0].Kind != application.TaskReminderAbsolute || reminders[0].At == nil {
		return errors.New("only one absolute Microsoft To Do reminder is supported")
	}
	value, err := graphWriteTaskTime(reminders[0].At)
	if err != nil {
		return err
	}
	payload["isReminderOn"] = true
	payload["reminderDateTime"] = value
	return nil
}

func graphWriteTaskRecurrence(
	recurrence *application.TaskRecurrence,
	start, due *application.TaskTemporal,
) (*graphTaskRecurrence, error) {
	if recurrence == nil || recurrence.Frequency == application.TaskRecurrenceProvider {
		return nil, errors.New("a Microsoft To Do write requires portable recurrence")
	}
	anchor := start
	if anchor == nil {
		anchor = due
	}
	if anchor == nil || anchor.Kind != application.TaskTemporalZoned {
		return nil, errors.New("a Microsoft To Do recurrence requires a zoned start or due date")
	}
	instant, err := time.Parse(time.RFC3339, anchor.Value)
	if err != nil {
		return nil, errors.New("task recurrence anchor is malformed")
	}
	location, err := time.LoadLocation(anchor.TimeZone)
	if err != nil {
		return nil, errors.New("task recurrence time zone is invalid")
	}
	local := instant.In(location)
	result := &graphTaskRecurrence{
		Pattern: graphRecurrencePattern{Interval: recurrence.Interval},
		Range: graphRecurrenceRange{
			Type: "noEnd", StartDate: local.Format(time.DateOnly),
			RecurrenceTimeZone: anchor.TimeZone,
		},
	}
	switch recurrence.Frequency {
	case application.TaskRecurrenceDaily:
		result.Pattern.Type = "daily"
	case application.TaskRecurrenceWeekly:
		result.Pattern.Type = "weekly"
		result.Pattern.DaysOfWeek = append([]string(nil), recurrence.DaysOfWeek...)
		result.Pattern.FirstDayOfWeek = "monday"
		if len(result.Pattern.DaysOfWeek) == 0 {
			result.Pattern.DaysOfWeek = []string{strings.ToLower(local.Weekday().String())}
		}
	case application.TaskRecurrenceMonthly:
		result.Pattern.Type = "absoluteMonthly"
		result.Pattern.DayOfMonth = local.Day()
	case application.TaskRecurrenceYearly:
		result.Pattern.Type = "absoluteYearly"
		result.Pattern.DayOfMonth = local.Day()
		result.Pattern.Month = int(local.Month())
	case application.TaskRecurrenceProvider:
		return nil, errors.New("the recurrence frequency is unsupported by Microsoft To Do")
	default:
		return nil, errors.New("the recurrence frequency is unsupported by Microsoft To Do")
	}
	if recurrence.Count > 0 {
		result.Range.Type = "numbered"
		result.Range.NumberOfOccurrences = recurrence.Count
	}
	if recurrence.Until != nil {
		until, untilErr := graphRecurrenceDate(recurrence.Until)
		if untilErr != nil {
			return nil, untilErr
		}
		result.Range.Type = "endDate"
		result.Range.EndDate = until
	}
	return result, nil
}

func graphReadTaskRecurrence(
	remote *graphTaskRecurrence,
	start, due *application.TaskTemporal,
) (*application.TaskRecurrence, error) {
	if remote == nil {
		return nil, nil
	}
	if !portableGraphTaskRecurrence(remote, start, due) {
		return graphProviderTaskRecurrence(remote)
	}
	result := &application.TaskRecurrence{Interval: remote.Pattern.Interval}
	switch remote.Pattern.Type {
	case "daily":
		result.Frequency = application.TaskRecurrenceDaily
	case "weekly":
		result.Frequency = application.TaskRecurrenceWeekly
		result.DaysOfWeek = append([]string(nil), remote.Pattern.DaysOfWeek...)
	case "absoluteMonthly":
		result.Frequency = application.TaskRecurrenceMonthly
	case "absoluteYearly":
		result.Frequency = application.TaskRecurrenceYearly
	default:
		return graphProviderTaskRecurrence(remote)
	}
	switch remote.Range.Type {
	case "noEnd":
	case "numbered":
		result.Count = remote.Range.NumberOfOccurrences
	case "endDate":
		result.Until = &application.TaskTemporal{
			Kind: application.TaskTemporalDate, Value: remote.Range.EndDate,
		}
	default:
		return graphProviderTaskRecurrence(remote)
	}
	if err := result.Validate(); err != nil {
		return graphProviderTaskRecurrence(remote)
	}
	return result, nil
}

func portableGraphTaskRecurrence(
	remote *graphTaskRecurrence,
	start, due *application.TaskTemporal,
) bool {
	anchor := start
	if anchor == nil {
		anchor = due
	}
	if anchor == nil || anchor.Kind != application.TaskTemporalZoned {
		return false
	}
	instant, err := time.Parse(time.RFC3339, anchor.Value)
	if err != nil {
		return false
	}
	location, err := time.LoadLocation(anchor.TimeZone)
	if err != nil {
		return false
	}
	local := instant.In(location)
	if remote.Range.StartDate != local.Format(time.DateOnly) {
		return false
	}
	if remote.Range.RecurrenceTimeZone != "" {
		zone, _, zoneErr := canonicalGraphTaskZone(remote.Range.RecurrenceTimeZone)
		if zoneErr != nil || zone != anchor.TimeZone {
			return false
		}
	}
	switch remote.Range.Type {
	case "noEnd":
		if remote.Range.EndDate != "" || remote.Range.NumberOfOccurrences != 0 {
			return false
		}
	case "numbered":
		if remote.Range.EndDate != "" || remote.Range.NumberOfOccurrences < 1 {
			return false
		}
	case "endDate":
		if remote.Range.EndDate == "" || remote.Range.NumberOfOccurrences != 0 {
			return false
		}
	default:
		return false
	}
	switch remote.Pattern.Type {
	case "daily":
		return remote.Pattern.Month == 0 && remote.Pattern.DayOfMonth == 0 &&
			len(remote.Pattern.DaysOfWeek) == 0 && remote.Pattern.FirstDayOfWeek == ""
	case "weekly":
		return len(remote.Pattern.DaysOfWeek) != 0 &&
			remote.Pattern.FirstDayOfWeek == "monday" &&
			remote.Pattern.Month == 0 && remote.Pattern.DayOfMonth == 0
	case "absoluteMonthly":
		return remote.Pattern.DayOfMonth == local.Day() && remote.Pattern.Month == 0 &&
			len(remote.Pattern.DaysOfWeek) == 0 && remote.Pattern.FirstDayOfWeek == ""
	case "absoluteYearly":
		return remote.Pattern.DayOfMonth == local.Day() &&
			remote.Pattern.Month == int(local.Month()) &&
			len(remote.Pattern.DaysOfWeek) == 0 && remote.Pattern.FirstDayOfWeek == ""
	default:
		return false
	}
}

func graphProviderTaskRecurrence(
	remote *graphTaskRecurrence,
) (*application.TaskRecurrence, error) {
	encoded, err := json.Marshal(remote)
	if err != nil || len(encoded) > 4096 {
		return nil, errors.New("graph task recurrence is malformed")
	}
	return &application.TaskRecurrence{
		Frequency:    application.TaskRecurrenceProvider,
		ProviderRule: string(encoded),
	}, nil
}

func graphRecurrenceDate(value *application.TaskTemporal) (string, error) {
	switch value.Kind {
	case application.TaskTemporalDate:
		return value.Value, nil
	case application.TaskTemporalZoned:
		instant, err := time.Parse(time.RFC3339, value.Value)
		if err != nil {
			return "", errors.New("task recurrence end is malformed")
		}
		location, err := time.LoadLocation(value.TimeZone)
		if err != nil {
			return "", errors.New("task recurrence end time zone is invalid")
		}
		return instant.In(location).Format(time.DateOnly), nil
	case application.TaskTemporalFloating:
		return "", errors.New("a Microsoft To Do recurrence end requires a date or zoned datetime")
	default:
		return "", errors.New("a Microsoft To Do recurrence end requires a date or zoned datetime")
	}
}

func graphWriteSources(sources []application.TaskLinkedSource) ([]graphLinkedResource, error) {
	result := make([]graphLinkedResource, 0, len(sources))
	for _, source := range sources {
		envelope, err := json.Marshal(graphLinkedSourceEnvelope{Version: 1, Source: source})
		if err != nil || len(envelope) > 4096 {
			return nil, errors.New("task linked source metadata is too large")
		}
		externalID := "corr1_" + base64.RawURLEncoding.EncodeToString(envelope)
		if len(externalID) > 8192 {
			return nil, errors.New("task linked source metadata is too large")
		}
		result = append(result, graphLinkedResource{
			WebURL: source.URL, ApplicationName: "Corresync",
			DisplayName: "Corresync " + string(source.Kind), ExternalID: externalID,
		})
	}
	return result, nil
}

func (client *Client) createGraphChecklist(
	ctx context.Context,
	listID, taskID string,
	items []application.TaskChecklistItemInput,
) error {
	resource := graphTaskResource(listID, taskID) + "/checklistItems"
	if err := validateGraphChecklistCreate(items); err != nil {
		return err
	}
	for _, item := range items {
		var created graphChecklistItem
		if _, err := client.api.DoJSON(
			ctx, http.MethodPost, resource, nil,
			map[string]any{"displayName": item.Title, "isChecked": item.Completed},
			&created, true, nil, http.StatusCreated,
		); err != nil {
			return err
		}
		if !validGraphID(created.ID) {
			return errors.New("graph checklist creation omitted its identity")
		}
	}
	return nil
}

func validateGraphChecklistCreate(items []application.TaskChecklistItemInput) error {
	for _, item := range items {
		if item.ID != "" || item.Order != "" {
			return errors.New("a new Microsoft To Do checklist cannot accept identities or ordering")
		}
	}
	return nil
}

type graphChecklistUpdate struct {
	ID   string
	Item application.TaskChecklistItemInput
}

type graphChecklistReplacement struct {
	updates []graphChecklistUpdate
	creates []application.TaskChecklistItemInput
	deletes []string
}

func prepareGraphChecklistReplacement(
	current []graphChecklistItem,
	replacement []application.TaskChecklistItemInput,
) (graphChecklistReplacement, error) {
	currentByID := make(map[string]graphChecklistItem, len(current))
	for _, item := range current {
		if !validGraphID(item.ID) || currentByID[item.ID].ID != "" {
			return graphChecklistReplacement{}, errors.New("graph returned an invalid checklist identity")
		}
		currentByID[item.ID] = item
	}
	result := graphChecklistReplacement{
		updates: make([]graphChecklistUpdate, 0, len(replacement)),
		creates: make([]application.TaskChecklistItemInput, 0, len(replacement)),
		deletes: make([]string, 0, len(current)),
	}
	retained := make(map[string]bool, len(replacement))
	for _, item := range replacement {
		if item.Order != "" {
			return graphChecklistReplacement{}, errors.New("checklist ordering is not exposed by Microsoft To Do")
		}
		if item.ID == "" {
			result.creates = append(result.creates, item)
			continue
		}
		remoteID, err := decodeChecklistID(item.ID)
		if err != nil {
			return graphChecklistReplacement{}, err
		}
		if retained[remoteID] {
			return graphChecklistReplacement{}, errors.New("replacement checklist duplicates an item")
		}
		remote, exists := currentByID[remoteID]
		if !exists {
			return graphChecklistReplacement{}, errors.New("replacement checklist item does not belong to the selected task")
		}
		retained[remoteID] = true
		if remote.DisplayName != item.Title || remote.Checked != item.Completed {
			result.updates = append(result.updates, graphChecklistUpdate{ID: remoteID, Item: item})
		}
	}
	for _, item := range current {
		if !retained[item.ID] {
			result.deletes = append(result.deletes, item.ID)
		}
	}
	return result, nil
}

func (client *Client) applyGraphChecklistReplacement(
	ctx context.Context,
	listID, taskID string,
	replacement graphChecklistReplacement,
) error {
	resource := graphTaskResource(listID, taskID) + "/checklistItems"
	for _, change := range replacement.updates {
		var updated graphChecklistItem
		if _, err := client.api.DoJSON(
			ctx, http.MethodPatch, resource+"/"+escaped(change.ID), nil,
			map[string]any{"displayName": change.Item.Title, "isChecked": change.Item.Completed},
			&updated, true, nil, http.StatusOK,
		); err != nil {
			return err
		}
		if updated.ID != change.ID {
			return errors.New("graph checklist update omitted its identity")
		}
	}
	if err := client.createGraphChecklist(ctx, listID, taskID, replacement.creates); err != nil {
		return err
	}
	for _, id := range replacement.deletes {
		if _, err := client.api.DoJSON(
			ctx, http.MethodDelete, resource+"/"+escaped(id), nil,
			nil, nil, true, nil, http.StatusNoContent,
		); err != nil {
			return err
		}
	}
	return nil
}

type graphSourceReplacement struct {
	deletes []string
	creates []graphLinkedResource
}

func prepareGraphSourceReplacement(
	current []graphLinkedResource,
	replacement []application.TaskLinkedSource,
) (graphSourceReplacement, error) {
	encoded, err := graphWriteSources(replacement)
	if err != nil {
		return graphSourceReplacement{}, err
	}
	result := graphSourceReplacement{
		deletes: make([]string, 0, len(current)), creates: encoded,
	}
	for _, source := range current {
		_, isOwned, decodeErr := graphLinkedSource(source)
		if decodeErr != nil {
			return graphSourceReplacement{}, decodeErr
		}
		if !isOwned {
			continue
		}
		if !validGraphID(source.ID) {
			return graphSourceReplacement{}, errors.New("graph returned an invalid linked-resource identity")
		}
		result.deletes = append(result.deletes, source.ID)
	}
	return result, nil
}

func (client *Client) applyGraphSourceReplacement(
	ctx context.Context,
	listID, taskID string,
	replacement graphSourceReplacement,
) error {
	resource := graphTaskResource(listID, taskID) + "/linkedResources"
	for _, id := range replacement.deletes {
		if _, err := client.api.DoJSON(
			ctx, http.MethodDelete, resource+"/"+escaped(id), nil,
			nil, nil, true, nil, http.StatusNoContent,
		); err != nil {
			return err
		}
	}
	for _, source := range replacement.creates {
		var created graphLinkedResource
		if _, err := client.api.DoJSON(
			ctx, http.MethodPost, resource, nil, source, &created,
			true, nil, http.StatusCreated,
		); err != nil {
			return err
		}
		if !validGraphID(created.ID) {
			return errors.New("graph linked-resource creation omitted its identity")
		}
	}
	return nil
}

func taskAssemblyError(err error) error {
	if errors.Is(err, application.ErrWriteOutcomeUnknown) {
		return err
	}
	return fmt.Errorf(
		"%w: graph task exists but related-resource assembly was not confirmed: %w",
		application.ErrWriteOutcomeUnknown,
		err,
	)
}

func graphTaskTombstoneVersion(id string) string {
	digest := sha256.Sum256([]byte(id))
	return encodeETag("deleted:" + hex.EncodeToString(digest[:]))
}
