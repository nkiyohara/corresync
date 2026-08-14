package todoist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const (
	providerPageSize = 200
	maximumPageCalls = 64
)

func (client *Client) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
) (application.TaskListPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskListPage{}, err
	}
	want := input.Offset + input.Limit + 1
	projects := make([]project, 0, want)
	cursor := ""
	for calls := 0; len(projects) < want; calls++ {
		if calls >= maximumPageCalls {
			return application.TaskListPage{}, errors.New("todoist project pagination exceeded the configured bound")
		}
		query := url.Values{"limit": {strconv.Itoa(providerPageSize)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response page[project]
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "projects", query, nil, &response,
			false, nil, http.StatusOK,
		); err != nil {
			return application.TaskListPage{}, err
		}
		if len(response.Results) > providerPageSize ||
			response.NextCursor != "" && response.NextCursor == cursor {
			return application.TaskListPage{}, errors.New("todoist returned an invalid project page")
		}
		for _, remote := range response.Results {
			if remote.Archived || remote.Deleted {
				continue
			}
			if !validID(remote.ID) || remote.Name == "" {
				return application.TaskListPage{}, errors.New("todoist returned an invalid project")
			}
			projects = append(projects, remote)
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
	}
	end := min(input.Offset+input.Limit, len(projects))
	selected := []project(nil)
	if input.Offset < len(projects) {
		selected = projects[input.Offset:end]
	}
	result := application.TaskListPage{
		Lists:  make([]application.TaskList, 0, len(selected)),
		Offset: input.Offset, Limit: input.Limit,
		HasMore: len(projects) > input.Offset+len(selected),
	}
	for _, remote := range selected {
		id, err := encodeID("tdl1_", remote.ID)
		if err != nil {
			return application.TaskListPage{}, err
		}
		result.Lists = append(result.Lists, application.TaskList{
			ID: id, DisplayName: remote.Name,
			Editable: !client.readOnly, Default: remote.Inbox,
		})
	}
	return result, nil
}

func (client *Client) getProject(
	ctx context.Context,
	projectID string,
) (project, error) {
	if !validID(projectID) {
		return project{}, errors.New("todoist project identity is malformed")
	}
	var remote project
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "projects/"+projectID, nil, nil, &remote,
		false, nil, http.StatusOK,
	); err != nil {
		return project{}, err
	}
	if remote.ID != projectID || remote.Name == "" || remote.Archived || remote.Deleted {
		return project{}, errors.New("todoist returned a project outside the selected route")
	}
	return remote, nil
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
) (application.TaskPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskPage{}, err
	}
	if input.Status != "" && input.Status != application.TaskStatusNeedsAction {
		return application.TaskPage{}, errors.New("todoist active-task listing supports only needs_action status")
	}
	projectID, err := decodeID("tdl1_", input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	want := input.Offset + input.Limit + 1
	remotes, err := client.listActiveTasks(ctx, projectID, want)
	if err != nil {
		return application.TaskPage{}, err
	}
	end := min(input.Offset+input.Limit, len(remotes))
	selected := []task(nil)
	if input.Offset < len(remotes) {
		selected = remotes[input.Offset:end]
	}
	selectedIDs := make([]string, 0, len(selected))
	for _, remote := range selected {
		selectedIDs = append(selectedIDs, remote.ID)
	}
	reminders, err := client.remindersForTasks(ctx, selectedIDs)
	if err != nil {
		return application.TaskPage{}, err
	}
	result := application.TaskPage{
		Tasks:  make([]application.Task, 0, len(selected)),
		Offset: input.Offset, Limit: input.Limit,
		HasMore: len(remotes) > input.Offset+len(selected),
	}
	for _, remote := range selected {
		view, err := client.taskView(input.ListID, remote, reminders[remote.ID])
		if err != nil {
			return application.TaskPage{}, err
		}
		result.Tasks = append(result.Tasks, view)
	}
	return result, nil
}

func (client *Client) listActiveTasks(
	ctx context.Context,
	projectID string,
	want int,
) ([]task, error) {
	results := make([]task, 0, want)
	cursor := ""
	for calls := 0; len(results) < want; calls++ {
		if calls >= maximumPageCalls {
			return nil, errors.New("todoist task pagination exceeded the configured bound")
		}
		query := url.Values{
			"project_id": {projectID}, "limit": {strconv.Itoa(providerPageSize)},
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response page[task]
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "tasks", query, nil, &response,
			false, nil, http.StatusOK,
		); err != nil {
			return nil, err
		}
		if len(response.Results) > providerPageSize ||
			response.NextCursor != "" && response.NextCursor == cursor {
			return nil, errors.New("todoist returned an invalid task page")
		}
		for _, remote := range response.Results {
			if !validTask(remote) || remote.ProjectID != projectID ||
				remote.Deleted || remote.Checked || remote.CompletedAt != "" {
				return nil, errors.New("todoist active task page violated its project or state boundary")
			}
			results = append(results, remote)
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
	}
	return results, nil
}

func (client *Client) SearchTasks(
	context.Context,
	application.TaskSearchInput,
) (application.TaskPage, error) {
	return application.TaskPage{}, errors.New("task search is unsupported by the Todoist adapter")
}

func (client *Client) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
) (application.Task, error) {
	if err := client.requireRead(); err != nil {
		return application.Task{}, err
	}
	projectID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	remote, err := client.getActiveTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, err
	}
	reminders, err := client.remindersByTask(ctx, taskID)
	if err != nil {
		return application.Task{}, err
	}
	return client.taskView(input.ListID, remote, reminders[taskID])
}

func (client *Client) getActiveTask(
	ctx context.Context,
	projectID, taskID string,
) (task, error) {
	var remote task
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "tasks/"+taskID, nil, nil, &remote,
		false, nil, http.StatusOK,
	); err != nil {
		return task{}, err
	}
	if !validTask(remote) || remote.ID != taskID || remote.ProjectID != projectID ||
		remote.Deleted || remote.Checked || remote.CompletedAt != "" {
		return task{}, errors.New("todoist returned a task outside the selected active route")
	}
	return remote, nil
}

func (client *Client) remindersByTask(
	ctx context.Context,
	taskID string,
) (map[string][]reminder, error) {
	result := make(map[string][]reminder)
	if !client.plan.Reminders {
		return result, nil
	}
	if !validID(taskID) {
		return nil, errors.New("todoist reminder task identity is malformed")
	}
	cursor := ""
	count := 0
	for calls := 0; ; calls++ {
		if calls >= maximumPageCalls {
			return nil, errors.New("todoist reminder pagination exceeded the configured bound")
		}
		query := url.Values{
			"limit": {strconv.Itoa(providerPageSize)}, "task_id": {taskID},
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response page[reminder]
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "reminders", query, nil, &response,
			false, nil, http.StatusOK,
		); err != nil {
			return nil, err
		}
		if len(response.Results) > providerPageSize ||
			response.NextCursor != "" && response.NextCursor == cursor {
			return nil, errors.New("todoist returned an invalid reminder page")
		}
		for _, remote := range response.Results {
			if !validID(remote.ID) || !validID(remote.ItemID) || remote.ItemID != taskID {
				return nil, errors.New("todoist returned an invalid reminder route")
			}
			count++
			if count > application.MaxTaskCollectionEntries {
				return nil, errors.New("todoist task has too many reminders")
			}
			result[remote.ItemID] = append(result[remote.ItemID], remote)
		}
		cursor = response.NextCursor
		if cursor == "" {
			return result, nil
		}
	}
}

func (client *Client) remindersForTasks(
	ctx context.Context,
	taskIDs []string,
) (map[string][]reminder, error) {
	if len(taskIDs) > application.MaxTaskPageSize {
		return nil, errors.New("todoist reminder task batch exceeds the configured bound")
	}
	result := make(map[string][]reminder, len(taskIDs))
	if !client.plan.Reminders {
		return result, nil
	}
	for _, taskID := range taskIDs {
		values, err := client.remindersByTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if reminders := values[taskID]; len(reminders) != 0 {
			result[taskID] = reminders
		}
	}
	return result, nil
}

func decodeRoute(listValue, taskValue string) (string, string, error) {
	projectID, err := decodeID("tdl1_", listValue)
	if err != nil {
		return "", "", err
	}
	taskID, err := decodeID("tdt1_", taskValue)
	if err != nil {
		return "", "", err
	}
	return projectID, taskID, nil
}

func (client *Client) exactTask(
	ctx context.Context,
	projectID, taskID, version string,
) (task, error) {
	expected, err := decodeVersion(version)
	if err != nil {
		return task{}, err
	}
	if expected.ID != taskID || expected.ProjectID != projectID {
		return task{}, restapi.ErrPrecondition
	}
	var current task
	if expected.Checked || expected.CompletedAt != "" {
		current, err = client.getCompletedTask(ctx, projectID, taskID, expected.CompletedAt)
	} else {
		current, err = client.getActiveTask(ctx, projectID, taskID)
	}
	if err != nil {
		return task{}, err
	}
	actual, err := encodeVersion(current)
	if err != nil {
		return task{}, err
	}
	if actual != version {
		return task{}, restapi.ErrPrecondition
	}
	return current, nil
}

func (client *Client) getCompletedTask(
	ctx context.Context,
	projectID, taskID, completedAt string,
) (task, error) {
	instant, err := time.Parse(time.RFC3339Nano, completedAt)
	if err != nil {
		return task{}, errors.New("todoist completed task version has no valid completion time")
	}
	query := url.Values{
		"since":      {instant.Add(-time.Second).UTC().Format(time.RFC3339Nano)},
		"until":      {instant.Add(time.Second).UTC().Format(time.RFC3339Nano)},
		"project_id": {projectID}, "limit": {strconv.Itoa(providerPageSize)},
	}
	cursor := ""
	for calls := 0; ; calls++ {
		if calls >= maximumPageCalls {
			return task{}, errors.New("todoist completed-task pagination exceeded the configured bound")
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var response struct {
			Items      []task `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "tasks/completed/by_completion_date", query,
			nil, &response, false, nil, http.StatusOK,
		); err != nil {
			return task{}, err
		}
		if len(response.Items) > providerPageSize ||
			response.NextCursor != "" && response.NextCursor == cursor {
			return task{}, errors.New("todoist returned an invalid completed-task page")
		}
		for _, remote := range response.Items {
			if remote.ID == taskID {
				remote.Checked = true
				if !validTask(remote) || remote.ProjectID != projectID || remote.CompletedAt == "" {
					return task{}, errors.New("todoist returned an invalid completed task")
				}
				return remote, nil
			}
		}
		cursor = response.NextCursor
		if cursor == "" {
			return task{}, fmt.Errorf("%w: Todoist completed task changed or disappeared", restapi.ErrPrecondition)
		}
	}
}
