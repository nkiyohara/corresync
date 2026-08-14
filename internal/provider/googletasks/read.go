package googletasks

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	taskListPageSize = 1000
	taskPageSize     = 100
	maximumPageCalls = 128
)

func (client *Client) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
) (application.TaskListPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskListPage{}, err
	}
	want := input.Offset + input.Limit + 1
	remotes := make([]taskList, 0, want)
	pageToken := ""
	for calls := 0; len(remotes) < want; calls++ {
		if calls >= maximumPageCalls {
			return application.TaskListPage{}, errors.New("google task-list pagination exceeded the configured bound")
		}
		query := queryValues("maxResults", strconv.Itoa(taskListPageSize))
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		var response taskListPage
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "tasks/v1/users/@me/lists", query,
			nil, &response, false, nil, http.StatusOK,
		); err != nil {
			return application.TaskListPage{}, err
		}
		if len(response.Items) > taskListPageSize ||
			response.NextPageToken != "" &&
				(!validPageToken(response.NextPageToken) || response.NextPageToken == pageToken) {
			return application.TaskListPage{}, errors.New("google Tasks returned an invalid task-list page")
		}
		for _, remote := range response.Items {
			if !validTaskList(remote) {
				return application.TaskListPage{}, errors.New("google Tasks returned an invalid task list")
			}
			remotes = append(remotes, remote)
		}
		pageToken = response.NextPageToken
		if pageToken == "" {
			break
		}
	}
	end := min(input.Offset+input.Limit, len(remotes))
	selected := []taskList(nil)
	if input.Offset < len(remotes) {
		selected = remotes[input.Offset:end]
	}
	result := application.TaskListPage{
		Lists:  make([]application.TaskList, 0, len(selected)),
		Offset: input.Offset, Limit: input.Limit,
		HasMore: len(remotes) > input.Offset+len(selected),
	}
	for _, remote := range selected {
		id, _ := encodeID("gtl1_", remote.ID)
		result.Lists = append(result.Lists, application.TaskList{
			ID: id, Version: encodeETag(remote.ETag),
			DisplayName: remote.Title, Editable: !client.readOnly,
		})
	}
	return result, nil
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
) (application.TaskPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskPage{}, err
	}
	if input.Status != "" && input.Status != application.TaskStatusNeedsAction &&
		input.Status != application.TaskStatusCompleted {
		return application.TaskPage{}, errors.New("google Tasks can filter only needs_action or completed status")
	}
	listID, err := decodeID("gtl1_", input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	want := input.Offset + input.Limit + 1
	remotes := make([]task, 0, want)
	pageToken := ""
	for calls := 0; len(remotes) < want; calls++ {
		if calls >= maximumPageCalls {
			return application.TaskPage{}, errors.New("google task pagination exceeded the configured bound")
		}
		query := taskReadQuery(taskPageSize, pageToken)
		var response taskPage
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, taskCollection(listID), query,
			nil, &response, false, nil, http.StatusOK,
		); err != nil {
			return application.TaskPage{}, err
		}
		if len(response.Items) > taskPageSize ||
			response.NextPageToken != "" &&
				(!validPageToken(response.NextPageToken) || response.NextPageToken == pageToken) {
			return application.TaskPage{}, errors.New("google Tasks returned an invalid task page")
		}
		for _, remote := range response.Items {
			if !validTask(remote) || remote.Deleted {
				return application.TaskPage{}, errors.New("google Tasks returned an invalid active task page")
			}
			status, statusErr := readStatus(remote.Status)
			if statusErr != nil {
				return application.TaskPage{}, statusErr
			}
			if input.Status == "" || status == input.Status {
				remotes = append(remotes, remote)
			}
		}
		pageToken = response.NextPageToken
		if pageToken == "" {
			break
		}
	}
	end := min(input.Offset+input.Limit, len(remotes))
	selected := []task(nil)
	if input.Offset < len(remotes) {
		selected = remotes[input.Offset:end]
	}
	result := application.TaskPage{
		Tasks:  make([]application.Task, 0, len(selected)),
		Offset: input.Offset, Limit: input.Limit,
		HasMore: len(remotes) > input.Offset+len(selected),
	}
	for _, remote := range selected {
		view, err := client.taskView(input.ListID, remote)
		if err != nil {
			return application.TaskPage{}, err
		}
		result.Tasks = append(result.Tasks, view)
	}
	return result, nil
}

func taskReadQuery(limit int, pageToken string) url.Values {
	query := queryValues(
		"maxResults", strconv.Itoa(limit),
		"showAssigned", "true",
		"showCompleted", "true",
		"showDeleted", "false",
		"showHidden", "true",
	)
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	return query
}

func (client *Client) SearchTasks(
	context.Context,
	application.TaskSearchInput,
) (application.TaskPage, error) {
	return application.TaskPage{}, errors.New("task search is unsupported by Google Tasks")
}

func (client *Client) GetTask(
	ctx context.Context,
	input application.TaskGetInput,
) (application.Task, error) {
	if err := client.requireRead(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	remote, err := client.getTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, err
	}
	return client.taskView(input.ListID, remote)
}

func (client *Client) getTask(ctx context.Context, listID, taskID string) (task, error) {
	var remote task
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, taskResource(listID, taskID), nil,
		nil, &remote, false, nil, http.StatusOK,
	); err != nil {
		return task{}, err
	}
	if !validTask(remote) || remote.ID != taskID || remote.Deleted {
		return task{}, errors.New("google Tasks returned a task outside the selected route")
	}
	return remote, nil
}
