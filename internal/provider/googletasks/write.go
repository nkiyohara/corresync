package googletasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

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
	listID, err := decodeID("gtl1_", input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	payload, err := createPayload(input)
	if err != nil {
		return application.Task{}, err
	}
	query := make(url.Values)
	if input.ParentID != "" {
		parentID, decodeErr := decodeID("gtt1_", input.ParentID)
		if decodeErr != nil {
			return application.Task{}, decodeErr
		}
		parent, getErr := client.getTask(ctx, listID, parentID)
		if getErr != nil {
			return application.Task{}, getErr
		}
		if parent.AssignmentInfo != nil || parent.Hidden {
			return application.Task{}, errors.New("the selected Google task cannot be a parent")
		}
		query.Set("parent", parentID)
	}
	var created task
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, taskCollection(listID), query,
		payload, &created, true, nil, http.StatusOK,
	); err != nil {
		return application.Task{}, err
	}
	if !validTask(created) || created.Deleted || created.ID == "" {
		return application.Task{}, fmt.Errorf(
			"%w: Google Tasks creation omitted its current identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	current, err := client.getTask(ctx, listID, created.ID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return client.taskView(input.ListID, current)
}

func createPayload(input application.TaskCreateInput) (map[string]any, error) {
	if err := validateUnsupportedCreate(input); err != nil {
		return nil, err
	}
	if err := validateGoogleWriteText("title", input.Title, 1024, false); err != nil {
		return nil, err
	}
	if err := validateGoogleWriteText("notes", input.Notes, 8192, true); err != nil {
		return nil, err
	}
	payload := map[string]any{"title": input.Title, "status": "needsAction"}
	if input.Notes != "" {
		payload["notes"] = input.Notes
	}
	if input.Due != nil {
		due, err := writeDue(input.Due)
		if err != nil {
			return nil, err
		}
		payload["due"] = due
	}
	return payload, nil
}

func validateUnsupportedCreate(input application.TaskCreateInput) error {
	if input.Priority != application.TaskPriorityNone {
		return errors.New("google Tasks does not support task priority")
	}
	if input.Start != nil {
		return errors.New("google Tasks does not support task start values")
	}
	if len(input.Reminders) != 0 || input.Recurrence != nil || len(input.Checklist) != 0 ||
		len(input.Assignees) != 0 || len(input.Labels) != 0 || len(input.Attachments) != 0 ||
		len(input.Sources) != 0 {
		return errors.New("google Tasks create contains unsupported canonical fields")
	}
	return nil
}

func (client *Client) UpdateTask(
	ctx context.Context,
	input application.TaskUpdateInput,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return application.Task{}, err
	}
	current, err := client.exactTask(ctx, listID, taskID, etag)
	if err != nil {
		return application.Task{}, err
	}
	payload, err := updatePayload(input, current)
	if err != nil {
		return application.Task{}, err
	}
	mutated := false
	if len(payload) != 0 {
		var updated task
		if _, err := client.api.DoJSON(
			ctx, http.MethodPatch, taskResource(listID, taskID), nil,
			payload, &updated, true, http.Header{"If-Match": {etag}}, http.StatusOK,
		); err != nil {
			return application.Task{}, err
		}
		if !validTask(updated) || updated.ID != taskID || updated.Deleted {
			return application.Task{}, fmt.Errorf(
				"%w: Google Tasks update omitted its current identity",
				application.ErrWriteOutcomeUnknown,
			)
		}
		mutated = true
		current = updated
		etag = updated.ETag
	}
	if input.ParentID != nil || input.Order != nil {
		if err := client.moveTask(ctx, listID, taskID, current, input, etag); err != nil {
			if mutated {
				return application.Task{}, writeAssemblyError(err)
			}
			return application.Task{}, err
		}
		mutated = true
	}
	if !mutated {
		return client.taskView(input.ListID, current)
	}
	current, err = client.getTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return client.taskView(input.ListID, current)
}

func updatePayload(input application.TaskUpdateInput, current task) (map[string]any, error) {
	if input.Priority != nil && *input.Priority != application.TaskPriorityNone {
		return nil, errors.New("google Tasks does not support task priority")
	}
	if input.Priority != nil || input.ReplaceStart || input.ReplaceReminders ||
		input.ReplaceRecurrence || input.ReplaceChecklist || input.ReplaceAssignees ||
		input.ReplaceLabels || input.ReplaceAttachments || input.ReplaceSources {
		return nil, errors.New("google Tasks update contains unsupported canonical fields")
	}
	if current.AssignmentInfo != nil && input.Notes != nil &&
		current.AssignmentInfo.SurfaceType == "DOCUMENT" {
		return nil, errors.New("a task assigned from Google Docs cannot update notes")
	}
	payload := make(map[string]any)
	if input.Title != nil {
		if err := validateGoogleWriteText("title", *input.Title, 1024, false); err != nil {
			return nil, err
		}
		payload["title"] = *input.Title
	}
	if input.Notes != nil {
		if err := validateGoogleWriteText("notes", *input.Notes, 8192, true); err != nil {
			return nil, err
		}
		payload["notes"] = *input.Notes
	}
	if input.ReplaceDue {
		due, err := writeDue(input.Due)
		if err != nil {
			return nil, err
		}
		payload["due"] = due
	}
	return payload, nil
}

func (client *Client) moveTask(
	ctx context.Context,
	listID, taskID string,
	current task,
	input application.TaskUpdateInput,
	etag string,
) error {
	query := make(url.Values)
	effectiveParent := current.Parent
	if input.ParentID != nil && *input.ParentID != "" {
		if current.AssignmentInfo != nil || current.Hidden {
			return errors.New("an assigned or hidden Google task cannot become a subtask")
		}
		parentID, err := decodeID("gtt1_", *input.ParentID)
		if err != nil {
			return err
		}
		if parentID == taskID {
			return errors.New("task cannot be its own parent")
		}
		parent, err := client.getTask(ctx, listID, parentID)
		if err != nil {
			return err
		}
		if parent.AssignmentInfo != nil || parent.Hidden {
			return errors.New("the selected Google task cannot be a parent")
		}
		effectiveParent = parentID
		query.Set("parent", parentID)
	} else if input.ParentID != nil {
		effectiveParent = ""
	}
	if input.Order != nil && *input.Order != "" {
		if current.Hidden {
			return errors.New("a hidden Google task can move only to the first position")
		}
		previousID, err := decodeID("gtt1_", *input.Order)
		if err != nil {
			return errors.New("google task order must be empty or an exact preceding Google task ID")
		}
		if previousID == taskID {
			return errors.New("a task cannot follow itself")
		}
		previous, err := client.getTask(ctx, listID, previousID)
		if err != nil {
			return err
		}
		if previous.Hidden || previous.Parent != effectiveParent {
			return errors.New("the preceding Google task must be a visible sibling")
		}
		query.Set("previous", previousID)
	}
	var moved task
	_, err := client.api.DoJSON(
		ctx, http.MethodPost, taskResource(listID, taskID)+"/move", query,
		nil, &moved, true, http.Header{"If-Match": {etag}}, http.StatusOK,
	)
	if err != nil {
		return err
	}
	if !validTask(moved) || moved.ID != taskID || moved.Deleted {
		return fmt.Errorf("%w: Google Tasks move omitted its current identity", application.ErrWriteOutcomeUnknown)
	}
	return nil
}

func (client *Client) CompleteTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	return client.setStatus(ctx, input, "completed")
}

func (client *Client) ReopenTask(
	ctx context.Context,
	input application.TaskStateInput,
) (application.Task, error) {
	return client.setStatus(ctx, input, "needsAction")
}

func (client *Client) setStatus(
	ctx context.Context,
	input application.TaskStateInput,
	status string,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	listID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return application.Task{}, err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if _, err := client.exactTask(ctx, listID, taskID, etag); err != nil {
		return application.Task{}, err
	}
	var updated task
	if _, err := client.api.DoJSON(
		ctx, http.MethodPatch, taskResource(listID, taskID), nil,
		map[string]any{"status": status}, &updated, true,
		http.Header{"If-Match": {etag}}, http.StatusOK,
	); err != nil {
		return application.Task{}, err
	}
	if !validTask(updated) || updated.ID != taskID || updated.Deleted {
		return application.Task{}, fmt.Errorf(
			"%w: Google Tasks state update omitted its current identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	current, err := client.getTask(ctx, listID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return client.taskView(input.ListID, current)
}

func (client *Client) DeleteTask(ctx context.Context, input application.TaskDeleteInput) error {
	if err := client.requireWrite(); err != nil {
		return err
	}
	listID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return err
	}
	etag, err := decodeETag(input.Version)
	if err != nil {
		return err
	}
	current, err := client.exactTask(ctx, listID, taskID, etag)
	if err != nil {
		return err
	}
	if current.AssignmentInfo != nil {
		return errors.New("deleting an assigned Google task would also delete its source task; unassign it in the source surface instead")
	}
	_, err = client.api.DoJSON(
		ctx, http.MethodDelete, taskResource(listID, taskID), nil,
		nil, nil, true, http.Header{"If-Match": {etag}}, http.StatusNoContent,
	)
	return err
}

func (client *Client) exactTask(
	ctx context.Context,
	listID, taskID, expectedETag string,
) (task, error) {
	current, err := client.getTask(ctx, listID, taskID)
	if err != nil {
		return task{}, err
	}
	if current.ETag != expectedETag {
		return task{}, restapi.ErrPrecondition
	}
	return current, nil
}
