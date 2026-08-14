package ticktick

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	projectPageSize  = 200
	maximumPageCalls = 64
	providerTaskCap  = 200
)

func (client *Client) ListTaskLists(
	ctx context.Context,
	input application.TaskListInput,
) (application.TaskListPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskListPage{}, err
	}
	want := input.Offset + input.Limit + 1
	projects, err := client.projects(ctx, want)
	if err != nil {
		return application.TaskListPage{}, err
	}
	if !containsProject(projects, "inbox") {
		projects = append([]project{{ID: "inbox", Name: "Inbox", Permission: "write", Kind: "TASK"}}, projects...)
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
		id, _ := encodeID("ttl1_", remote.ID)
		result.Lists = append(result.Lists, application.TaskList{
			ID: id, DisplayName: remote.Name, Default: remote.ID == "inbox",
			Editable: !client.readOnly && !remote.Closed && remote.Permission == "write",
		})
	}
	return result, nil
}

func (client *Client) projects(ctx context.Context, want int) ([]project, error) {
	projects := make([]project, 0, max(want, projectPageSize))
	for offset, calls := 0, 0; want <= 0 || len(projects) < want; offset, calls = offset+projectPageSize, calls+1 {
		if calls >= maximumPageCalls {
			return nil, errors.New("ticktick project pagination exceeded the configured bound")
		}
		var page []project
		query := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(projectPageSize)}}
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, "open/v1/project", query, nil, &page,
			false, nil, http.StatusOK,
		); err != nil {
			return nil, err
		}
		if len(page) > projectPageSize {
			return nil, errors.New("ticktick returned an oversized project page")
		}
		for _, remote := range page {
			if err := validateProject(remote); err != nil {
				return nil, err
			}
			if remote.Kind == "TASK" {
				projects = append(projects, remote)
			}
		}
		if len(page) < projectPageSize {
			break
		}
	}
	return projects, nil
}

func validateProject(remote project) error {
	if !validProviderID(remote.ID) || !validProviderText(remote.Name, application.MaxTaskListNameBytes, false) {
		return errors.New("ticktick returned a malformed project")
	}
	switch remote.Kind {
	case "TASK", "NOTE":
	default:
		return errors.New("ticktick returned an unknown project kind")
	}
	switch remote.Permission {
	case "read", "write", "comment":
	default:
		return errors.New("ticktick returned an unknown project permission")
	}
	return nil
}

func containsProject(projects []project, id string) bool {
	for _, candidate := range projects {
		if candidate.ID == id {
			return true
		}
	}
	return false
}

func (client *Client) ListTasks(
	ctx context.Context,
	input application.TaskReadInput,
) (application.TaskPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskPage{}, err
	}
	if input.Status == application.TaskStatusInProgress {
		return application.TaskPage{Offset: input.Offset, Limit: input.Limit}, nil
	}
	projectID, err := decodeID("ttl1_", input.ListID)
	if err != nil {
		return application.TaskPage{}, err
	}
	body := map[string]any{"projectIds": []string{projectID}}
	if input.Status != "" {
		status, err := writeStatus(input.Status)
		if err != nil {
			return application.TaskPage{}, err
		}
		body["status"] = []int{status}
	}
	remotes, err := client.filterTasks(ctx, "open/v1/task/filter", body)
	if err != nil {
		return application.TaskPage{}, err
	}
	for _, remote := range remotes {
		if remote.ProjectID != projectID {
			return application.TaskPage{}, errors.New("ticktick returned a task outside the selected list")
		}
	}
	return client.taskPage(input.ListID, remotes, input.Offset, input.Limit)
}

func writeStatus(status application.TaskStatus) (int, error) {
	switch status {
	case application.TaskStatusNeedsAction:
		return 0, nil
	case application.TaskStatusCompleted:
		return 2, nil
	case application.TaskStatusCancelled:
		return -1, nil
	case application.TaskStatusInProgress:
		return 0, errors.New("ticktick cannot filter in-progress tasks")
	default:
		return 0, errors.New("ticktick cannot filter the requested task status")
	}
}

func (client *Client) SearchTasks(
	ctx context.Context,
	input application.TaskSearchInput,
) (application.TaskPage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskPage{}, err
	}
	body := map[string]any{"keywords": input.Query}
	projectID := ""
	if input.ListID != "" {
		var err error
		projectID, err = decodeID("ttl1_", input.ListID)
		if err != nil {
			return application.TaskPage{}, err
		}
		body["projectIds"] = []string{projectID}
	}
	remotes, err := client.filterTasks(ctx, "open/v1/task/search", body)
	if err != nil {
		return application.TaskPage{}, err
	}
	if projectID != "" {
		for _, remote := range remotes {
			if remote.ProjectID != projectID {
				return application.TaskPage{}, errors.New("ticktick search returned a task outside the selected list")
			}
		}
	}
	return client.taskPage(input.ListID, remotes, input.Offset, input.Limit)
}

func (client *Client) filterTasks(ctx context.Context, resource string, body any) ([]task, error) {
	var remotes []task
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, resource, nil, body, &remotes,
		false, nil, http.StatusOK,
	); err != nil {
		return nil, err
	}
	if len(remotes) > providerTaskCap {
		return nil, errors.New("ticktick returned an oversized task result")
	}
	for _, remote := range remotes {
		if err := validateTask(remote); err != nil {
			return nil, err
		}
	}
	return remotes, nil
}

func (client *Client) taskPage(
	requestedListID string,
	remotes []task,
	offset, limit int,
) (application.TaskPage, error) {
	if len(remotes) == providerTaskCap && offset >= len(remotes) {
		return application.TaskPage{}, errors.New(
			"ticktick task pagination reached the provider's unpageable 200-task limit",
		)
	}
	end := min(offset+limit, len(remotes))
	selected := []task(nil)
	if offset < len(remotes) {
		selected = remotes[offset:end]
	}
	result := application.TaskPage{
		Tasks: make([]application.Task, 0, len(selected)), Offset: offset, Limit: limit,
		HasMore: len(remotes) > offset+len(selected) || len(remotes) == providerTaskCap,
	}
	for _, remote := range selected {
		listID := requestedListID
		if listID == "" {
			var err error
			listID, err = encodeID("ttl1_", remote.ProjectID)
			if err != nil {
				return application.TaskPage{}, err
			}
		}
		view, err := client.taskView(listID, remote)
		if err != nil {
			return application.TaskPage{}, err
		}
		result.Tasks = append(result.Tasks, view)
	}
	return result, nil
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
	remote, err := client.getTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, err
	}
	return client.taskView(input.ListID, remote)
}

func (client *Client) getTask(ctx context.Context, projectID, taskID string) (task, error) {
	var remote task
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, taskResource(projectID, taskID), nil,
		nil, &remote, false, nil, http.StatusOK,
	); err != nil {
		return task{}, err
	}
	if err := validateTask(remote); err != nil || remote.ID != taskID || remote.ProjectID != projectID {
		return task{}, errors.New("ticktick returned a task outside the selected route")
	}
	return remote, nil
}
