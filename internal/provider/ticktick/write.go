package ticktick

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func (client *Client) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	projectID, err := decodeID("ttl1_", input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	if err := client.requireWritableProject(ctx, projectID); err != nil {
		return application.Task{}, err
	}
	payload, err := client.createPayload(input, projectID)
	if err != nil {
		return application.Task{}, err
	}
	var created task
	result, err := client.api.DoJSON(
		ctx, http.MethodPost, "open/v1/task", nil, payload, &created,
		true, nil, http.StatusOK, http.StatusCreated,
	)
	if err != nil {
		return application.Task{}, err
	}
	if result.Status == http.StatusCreated || !validProviderID(created.ID) {
		return application.Task{}, fmt.Errorf("%w: TickTick creation omitted the stable task identity", application.ErrWriteOutcomeUnknown)
	}
	if created.ProjectID != projectID {
		return application.Task{}, fmt.Errorf("%w: TickTick created a task outside the selected list", application.ErrWriteOutcomeUnknown)
	}
	if len(input.Assignees) != 0 {
		if err := client.replaceAssignee(ctx, projectID, created.ID, input.Assignees); err != nil {
			return application.Task{}, writeAssemblyError(err)
		}
	}
	current, err := client.getTask(ctx, projectID, created.ID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return client.taskView(input.ListID, current)
}

func (client *Client) createPayload(input application.TaskCreateInput, projectID string) (map[string]any, error) {
	if len(input.Reminders) != 0 || len(input.Attachments) != 0 || len(input.Sources) != 0 {
		return nil, errors.New("ticktick create contains unsupported canonical fields")
	}
	priority, err := writePriority(input.Priority)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"title": input.Title, "projectId": projectID, "priority": priority,
	}
	if input.ParentID != "" {
		parentID, err := decodeID("ttt1_", input.ParentID)
		if err != nil {
			return nil, err
		}
		payload["parentId"] = parentID
	}
	if len(input.Labels) != 0 {
		payload["tags"] = append([]string(nil), input.Labels...)
	}
	if len(input.Checklist) != 0 {
		payload["desc"] = input.Notes
		items, err := writeChecklist(input.Checklist)
		if err != nil {
			return nil, err
		}
		payload["items"] = items
	} else if input.Notes != "" {
		payload["content"] = input.Notes
	}
	temporal, err := validateTemporalPair(input.Start, input.Due, client.timeZone)
	if err != nil {
		return nil, err
	}
	for name, value := range temporal {
		if value != nil {
			payload[name] = value
		}
	}
	if input.Recurrence != nil {
		if input.Start == nil && input.Due == nil {
			return nil, errors.New("ticktick recurrence requires a start or due value")
		}
		rule, err := writeRecurrence(input.Recurrence)
		if err != nil {
			return nil, err
		}
		payload["repeatFlag"] = rule
	}
	if len(input.Assignees) > 1 {
		return nil, errors.New("ticktick supports at most one task assignee")
	}
	return payload, nil
}

func (client *Client) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	projectID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	if err := client.requireWritableProject(ctx, projectID); err != nil {
		return application.Task{}, err
	}
	current, err := client.exactTask(ctx, projectID, taskID, input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if current.Kind == "NOTE" {
		return application.Task{}, errors.New("ticktick note tasks are read-only through the canonical task contract")
	}
	if current.Status != 0 {
		return application.Task{}, errors.New("only an active TickTick task can be updated")
	}
	payload, changed, err := client.updatePayload(input, current)
	if err != nil {
		return application.Task{}, err
	}
	if changed {
		var updated task
		result, err := client.api.DoJSON(
			ctx, http.MethodPost, "open/v1/task/"+url.PathEscape(taskID), nil,
			payload, &updated, true, nil, http.StatusOK, http.StatusCreated,
		)
		if err != nil {
			return application.Task{}, err
		}
		if result.Status == http.StatusCreated || !validProviderID(updated.ID) || updated.ID != taskID {
			return application.Task{}, fmt.Errorf("%w: TickTick update omitted its current identity", application.ErrWriteOutcomeUnknown)
		}
	}
	if input.ReplaceAssignees {
		if err := client.replaceAssignee(ctx, projectID, taskID, input.Assignees); err != nil {
			if changed {
				return application.Task{}, writeAssemblyError(err)
			}
			return application.Task{}, err
		}
		changed = true
	}
	if !changed {
		return client.taskView(input.ListID, current)
	}
	current, err = client.getTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return client.taskView(input.ListID, current)
}

func (client *Client) updatePayload(input application.TaskUpdateInput, current task) (map[string]any, bool, error) {
	if input.ReplaceReminders || input.ReplaceAttachments || input.ReplaceSources {
		return nil, false, errors.New("ticktick update contains unsupported canonical fields")
	}
	payload := map[string]any{"id": current.ID, "projectId": current.ProjectID}
	changed := false
	if input.Title != nil {
		payload["title"] = *input.Title
		changed = true
	}
	if input.Notes != nil {
		if current.Kind == "CHECKLIST" {
			payload["desc"] = *input.Notes
		} else {
			payload["content"] = *input.Notes
		}
		changed = true
	}
	if input.Priority != nil {
		priority, err := writePriority(*input.Priority)
		if err != nil {
			return nil, false, err
		}
		payload["priority"] = priority
		changed = true
	}
	if input.ParentID != nil {
		parentID := ""
		if *input.ParentID != "" {
			var err error
			parentID, err = decodeID("ttt1_", *input.ParentID)
			if err != nil {
				return nil, false, err
			}
		}
		payload["parentId"] = parentID
		changed = true
	}
	if input.Order != nil {
		order, err := strconv.ParseInt(*input.Order, 10, 64)
		if err != nil {
			return nil, false, errors.New("ticktick task order must be one signed 64-bit integer")
		}
		payload["sortOrder"] = order
		changed = true
	}
	if input.ReplaceLabels {
		payload["tags"] = append([]string(nil), input.Labels...)
		changed = true
	}
	if input.ReplaceChecklist {
		items, err := writeChecklist(input.Checklist)
		if err != nil {
			return nil, false, err
		}
		payload["items"] = items
		changed = true
	}
	if input.ReplaceStart && input.Start == nil && current.StartDate != "" {
		return nil, false, errors.New("TickTick Open API does not document clearing a task start date")
	}
	if input.ReplaceDue && input.Due == nil && current.DueDate != "" {
		return nil, false, errors.New("TickTick Open API does not document clearing a task due date")
	}
	temporalChanged := input.ReplaceStart && input.Start != nil ||
		input.ReplaceDue && input.Due != nil
	if temporalChanged {
		start, err := readTemporal(current.StartDate, current.IsAllDay, current.TimeZone)
		if err != nil {
			return nil, false, err
		}
		due, err := readTemporal(current.DueDate, current.IsAllDay, current.TimeZone)
		if err != nil {
			return nil, false, err
		}
		if input.ReplaceStart {
			start = input.Start
		}
		if input.ReplaceDue {
			due = input.Due
		}
		temporal, err := validateTemporalPair(start, due, client.timeZone)
		if err != nil {
			return nil, false, err
		}
		for name, value := range temporal {
			if value != nil {
				payload[name] = value
			}
		}
		changed = true
	}
	if input.ReplaceRecurrence {
		if input.Recurrence == nil {
			if current.RepeatFlag != "" {
				return nil, false, errors.New("TickTick Open API does not document clearing task recurrence")
			}
		} else {
			hasStart := current.StartDate != ""
			hasDue := current.DueDate != ""
			if input.ReplaceStart {
				hasStart = input.Start != nil
			}
			if input.ReplaceDue {
				hasDue = input.Due != nil
			}
			if !hasStart && !hasDue {
				return nil, false, errors.New("ticktick recurrence requires a start or due value")
			}
			rule, err := writeRecurrence(input.Recurrence)
			if err != nil {
				return nil, false, err
			}
			payload["repeatFlag"] = rule
			changed = true
		}
	}
	if len(input.Assignees) > 1 {
		return nil, false, errors.New("ticktick supports at most one task assignee")
	}
	return payload, changed, nil
}

func writeRecurrence(value *application.TaskRecurrence) (any, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	if value.Frequency != application.TaskRecurrenceProvider || !strings.HasPrefix(value.ProviderRule, "RRULE:") {
		return nil, errors.New("ticktick recurrence requires an exact RRULE provider rule")
	}
	return value.ProviderRule, nil
}

func writeChecklist(values []application.TaskChecklistItemInput) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item := map[string]any{"title": value.Title, "status": 0}
		if value.Completed {
			item["status"] = 1
		}
		if value.ID != "" {
			id, err := decodeID("tti1_", value.ID)
			if err != nil {
				return nil, err
			}
			item["id"] = id
		}
		if value.Order != "" {
			order, err := strconv.ParseInt(value.Order, 10, 64)
			if err != nil {
				return nil, errors.New("ticktick checklist order must be one signed 64-bit integer")
			}
			item["sortOrder"] = order
		}
		result = append(result, item)
	}
	return result, nil
}

func (client *Client) replaceAssignee(
	ctx context.Context,
	projectID, taskID string,
	assignees []application.TaskAssignee,
) error {
	if len(assignees) > 1 {
		return errors.New("ticktick supports at most one task assignee")
	}
	resource := "open/v1/task/unassign"
	payload := map[string]any{"projectId": projectID, "taskId": taskID}
	if len(assignees) == 1 {
		username, err := decodeID("tta1_", assignees[0].ID)
		if err != nil {
			return err
		}
		resource = "open/v1/task/assign"
		payload["assigneeUsername"] = username
	}
	var updated task
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, resource, nil, payload, &updated,
		true, nil, http.StatusOK,
	); err != nil {
		return err
	}
	if err := validateTask(updated); err != nil || updated.ID != taskID || updated.ProjectID != projectID {
		return fmt.Errorf("%w: TickTick assignment omitted its current identity", application.ErrWriteOutcomeUnknown)
	}
	return nil
}

func (client *Client) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	projectID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	if err := client.requireWritableProject(ctx, projectID); err != nil {
		return application.Task{}, err
	}
	current, err := client.exactTask(ctx, projectID, taskID, input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if current.Status != 0 {
		return application.Task{}, errors.New("only an active TickTick task can be completed")
	}
	if current.RepeatFlag != "" {
		return application.Task{}, errors.New("ticktick recurring completion semantics are not exposed without a documented canonical result")
	}
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, taskResource(projectID, taskID)+"/complete", nil,
		nil, nil, true, nil, http.StatusOK, http.StatusCreated,
	); err != nil {
		return application.Task{}, err
	}
	current, err = client.getTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	if current.Status != 2 {
		return application.Task{}, fmt.Errorf("%w: TickTick did not expose the completed task state", application.ErrWriteOutcomeUnknown)
	}
	return client.taskView(input.ListID, current)
}

func (client *Client) ReopenTask(context.Context, application.TaskStateInput) (application.Task, error) {
	return application.Task{}, errors.New("task reopen is unsupported by TickTick")
}

func (client *Client) DeleteTask(ctx context.Context, input application.TaskDeleteInput) error {
	if err := client.requireWrite(); err != nil {
		return err
	}
	projectID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return err
	}
	if err := client.requireWritableProject(ctx, projectID); err != nil {
		return err
	}
	if _, err := client.exactTask(ctx, projectID, taskID, input.Version); err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx, http.MethodDelete, taskResource(projectID, taskID), nil,
		nil, nil, true, nil, http.StatusOK, http.StatusCreated,
	)
	return err
}

func (client *Client) exactTask(ctx context.Context, projectID, taskID, version string) (task, error) {
	current, err := client.getTask(ctx, projectID, taskID)
	if err != nil {
		return task{}, err
	}
	if encodeVersion(current) != version {
		return task{}, restapi.ErrPrecondition
	}
	return current, nil
}

func (client *Client) requireWritableProject(ctx context.Context, projectID string) error {
	if projectID == "inbox" {
		return nil
	}
	projects, err := client.projects(ctx, 0)
	if err != nil {
		return err
	}
	for _, remote := range projects {
		if remote.ID == projectID {
			if remote.Closed || remote.Kind != "TASK" || remote.Permission != "write" {
				return errors.New("the selected TickTick project is not writable")
			}
			return nil
		}
	}
	return errors.New("the selected TickTick project was not returned by the account grant")
}
