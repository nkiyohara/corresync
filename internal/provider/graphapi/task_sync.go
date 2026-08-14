package graphapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
) (application.TaskChangePage, error) {
	if err := client.requireTaskRead(); err != nil {
		return application.TaskChangePage{}, err
	}
	listID, err := decodeTaskListID(input.ListID)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	response, err := client.graphTaskDelta(ctx, listID, input)
	reset := false
	if err != nil && input.Cursor != nil && restapi.IsStatus(err, http.StatusGone) {
		fresh := input
		fresh.Cursor = nil
		response, err = client.graphTaskDelta(ctx, listID, fresh)
		reset = err == nil
	}
	if err != nil {
		return application.TaskChangePage{}, err
	}
	if len(response.Value) > input.Limit || (response.NextLink == "") == (response.DeltaLink == "") {
		return application.TaskChangePage{}, errors.New("graph returned an invalid task delta page")
	}
	cursorValue := response.DeltaLink
	if response.NextLink != "" {
		cursorValue = response.NextLink
	}
	if _, _, err := client.taskContinuation(cursorValue, listID); err != nil {
		return application.TaskChangePage{}, err
	}
	page := application.TaskChangePage{
		Changes: make([]application.TaskChange, 0, len(response.Value)),
		Cursor: application.TaskCursor{
			Provider: domain.ProviderMicrosoftGraph, Account: input.Account,
			ListID: input.ListID, Mode: application.TaskSyncDelta, Value: cursorValue,
		},
		Reset: reset,
	}
	for _, remote := range response.Value {
		if len(remote.Removed) != 0 && string(remote.Removed) != "null" {
			if !validGraphID(remote.ID) {
				return application.TaskChangePage{}, errors.New("graph returned an invalid task tombstone")
			}
			id, encodeErr := encodeTaskID(remote.ID)
			if encodeErr != nil {
				return application.TaskChangePage{}, encodeErr
			}
			page.Changes = append(page.Changes, application.TaskChange{
				Kind: application.TaskChangeDelete, TaskID: id,
				Version: graphTaskTombstoneVersion(remote.ID),
			})
			continue
		}
		task, viewErr := graphTaskView(input.ListID, remote)
		if viewErr != nil {
			return application.TaskChangePage{}, viewErr
		}
		page.Changes = append(page.Changes, application.TaskChange{
			Kind: application.TaskChangeUpsert, Task: &task,
		})
	}
	return page, nil
}

func (client *Client) graphTaskDelta(
	ctx context.Context,
	listID string,
	input application.TaskSyncInput,
) (graphTaskPage, error) {
	resource := graphTaskCollection(listID) + "/delta"
	query := url.Values{
		"$select": {graphTaskSelect}, "$expand": {"checklistItems,linkedResources"},
		"$top": {strconv.Itoa(input.Limit)},
	}
	if input.Cursor != nil {
		var err error
		resource, query, err = client.taskContinuation(input.Cursor.Value, listID)
		if err != nil {
			return graphTaskPage{}, err
		}
	}
	var response graphTaskPage
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, resource, query, nil, &response,
		false, nil, http.StatusOK,
	); err != nil {
		return graphTaskPage{}, err
	}
	return response, nil
}

func (client *Client) taskContinuation(value, listID string) (string, url.Values, error) {
	if client == nil || client.apiBase == nil || value == "" ||
		len(value) > application.MaxTaskCursorBytes || strings.ContainsAny(value, "\r\n\x00") {
		return "", nil, errors.New("graph task cursor is malformed")
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != client.apiBase.Scheme || target.Host != client.apiBase.Host ||
		target.User != nil || target.Fragment != "" {
		return "", nil, errors.New("graph task cursor escaped the configured origin")
	}
	basePath := strings.TrimSuffix(client.apiBase.EscapedPath(), "/") + "/"
	if !strings.HasPrefix(target.EscapedPath(), basePath) {
		return "", nil, errors.New("graph task cursor escaped the configured base path")
	}
	resource := strings.TrimPrefix(target.EscapedPath(), basePath)
	want := graphTaskCollection(listID) + "/delta"
	if resource != want {
		return "", nil, errors.New("graph task cursor escaped its selected task list")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return "", nil, errors.New("graph task cursor query is malformed")
	}
	if len(query) != 1 {
		return "", nil, errors.New("graph task cursor query is not a delta continuation")
	}
	for key, values := range query {
		if key != "$skiptoken" && key != "$deltatoken" || len(values) != 1 ||
			values[0] == "" || strings.ContainsAny(values[0], "\r\n\x00") {
			return "", nil, errors.New("graph task cursor query is not a delta continuation")
		}
	}
	return resource, query, nil
}
