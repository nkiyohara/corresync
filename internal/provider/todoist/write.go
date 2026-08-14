package todoist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	maximumCommandFormBytes = 1 << 20
	maximumCommandsPerBatch = 100
)

type syncCommand struct {
	Type   string         `json:"type"`
	UUID   string         `json:"uuid"`
	TempID string         `json:"temp_id,omitempty"`
	Args   map[string]any `json:"args"`
}

type syncResponse struct {
	SyncToken     string                     `json:"sync_token"`
	FullSync      bool                       `json:"full_sync"`
	Items         []task                     `json:"items"`
	Reminders     []reminder                 `json:"reminders"`
	SyncStatus    map[string]json.RawMessage `json:"sync_status"`
	TempIDMapping map[string]string          `json:"temp_id_mapping"`
}

func (client *Client) CreateTask(
	ctx context.Context,
	input application.TaskCreateInput,
) (application.Task, error) {
	if err := client.requireWrite(); err != nil {
		return application.Task{}, err
	}
	projectID, err := decodeID("tdl1_", input.ListID)
	if err != nil {
		return application.Task{}, err
	}
	if err := client.requireParentInProject(ctx, projectID, input.ParentID); err != nil {
		return application.Task{}, err
	}
	args, err := client.createArgs(input, projectID)
	if err != nil {
		return application.Task{}, err
	}
	if len(input.Assignees) != 0 {
		if err := client.requireAssignableProject(ctx, projectID); err != nil {
			return application.Task{}, err
		}
	}
	tempID := uuid.NewString()
	reminderCommands, err := client.addReminderCommands(tempID, input.Start, input.Reminders)
	if err != nil {
		return application.Task{}, err
	}
	commands := make([]syncCommand, 0, 1+len(reminderCommands))
	commands = append(commands, newCommand("item_add", tempID, args))
	commands = append(commands, reminderCommands...)
	response, err := client.runCommands(ctx, commands)
	if err != nil {
		return application.Task{}, err
	}
	createdID := response.TempIDMapping[tempID]
	if !validID(createdID) || createdID == tempID {
		return application.Task{}, fmt.Errorf(
			"%w: Todoist creation omitted the stable task identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	remote, err := client.getActiveTask(ctx, projectID, createdID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	reminders, err := client.remindersByTask(ctx, createdID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	view, err := client.taskView(input.ListID, remote, reminders[createdID])
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return view, nil
}

func (client *Client) createArgs(
	input application.TaskCreateInput,
	projectID string,
) (map[string]any, error) {
	priority, err := writePriority(input.Priority)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"content": input.Title, "project_id": projectID,
		"priority": priority,
	}
	if input.Notes != "" {
		args["description"] = input.Notes
	}
	if input.ParentID != "" {
		parentID, err := decodeID("tdt1_", input.ParentID)
		if err != nil {
			return nil, err
		}
		args["parent_id"] = parentID
	}
	if len(input.Assignees) != 0 {
		if err := client.applyAssignees(args, input.Assignees); err != nil {
			return nil, err
		}
	}
	if len(input.Labels) != 0 {
		if !client.plan.Labels {
			return nil, errors.New("the current Todoist plan does not enable labels")
		}
		args["labels"] = append([]string{}, input.Labels...)
	}
	providerDue, err := writeDue(input.Start, input.Recurrence)
	if err != nil {
		return nil, err
	}
	if providerDue != nil {
		args["due"] = providerDue
	}
	if input.Due != nil {
		providerDeadline, err := client.writeDeadline(input.Due)
		if err != nil {
			return nil, err
		}
		args["deadline"] = providerDeadline
	}
	return args, nil
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
	current, err := client.exactTask(ctx, projectID, taskID, input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if current.Checked || current.CompletedAt != "" {
		return application.Task{}, errors.New("a completed Todoist task must be reopened before updating")
	}
	if input.ReplaceAssignees && len(input.Assignees) != 0 {
		if err := client.requireAssignableProject(ctx, projectID); err != nil {
			return application.Task{}, err
		}
	}
	commands, err := client.updateCommands(ctx, input, current)
	if err != nil {
		return application.Task{}, err
	}
	if len(commands) == 0 {
		reminders, reminderErr := client.remindersByTask(ctx, taskID)
		if reminderErr != nil {
			return application.Task{}, reminderErr
		}
		return client.taskView(input.ListID, current, reminders[taskID])
	}
	if _, err := client.runCommands(ctx, commands); err != nil {
		return application.Task{}, err
	}
	updated, err := client.getActiveTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	reminders, err := client.remindersByTask(ctx, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	view, err := client.taskView(input.ListID, updated, reminders[taskID])
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return view, nil
}

func (client *Client) updateCommands(
	ctx context.Context,
	input application.TaskUpdateInput,
	current task,
) ([]syncCommand, error) {
	args := map[string]any{"id": current.ID}
	if input.Title != nil {
		args["content"] = *input.Title
	}
	if input.Notes != nil {
		args["description"] = *input.Notes
	}
	if input.Priority != nil {
		priority, err := writePriority(*input.Priority)
		if err != nil {
			return nil, err
		}
		args["priority"] = priority
	}
	if input.ReplaceLabels {
		if !client.plan.Labels {
			return nil, errors.New("the current Todoist plan does not enable labels")
		}
		args["labels"] = append([]string{}, input.Labels...)
	}
	if input.ReplaceAssignees {
		if err := client.applyAssignees(args, input.Assignees); err != nil {
			return nil, err
		}
	}
	currentStart, err := readDue(current.Due)
	if err != nil {
		return nil, fmt.Errorf("todoist task due: %w", err)
	}
	if input.ReplaceStart || input.ReplaceRecurrence {
		start := input.Start
		if !input.ReplaceStart {
			start = currentStart
		}
		recurrence := input.Recurrence
		if !input.ReplaceRecurrence && current.Due != nil && current.Due.Recurring {
			rule, err := encodeRecurrence(*current.Due)
			if err != nil {
				return nil, err
			}
			recurrence = &application.TaskRecurrence{
				Frequency:    application.TaskRecurrenceProvider,
				ProviderRule: rule,
			}
		}
		if input.ReplaceStart && input.Start == nil && recurrence != nil {
			return nil, errors.New("clearing a recurring Todoist start requires explicit recurrence removal")
		}
		providerDue, err := writeDue(start, recurrence)
		if err != nil {
			return nil, err
		}
		args["due"] = providerDue
	}
	if input.ReplaceDue {
		providerDeadline, err := client.writeDeadline(input.Due)
		if err != nil {
			return nil, err
		}
		args["deadline"] = providerDeadline
	}
	mutations := make([]syncCommand, 0, 2)
	if len(args) > 1 {
		mutations = append(mutations, newCommand("item_update", "", args))
	}
	if input.ParentID != nil {
		move := map[string]any{"id": current.ID}
		if *input.ParentID == "" {
			move["project_id"] = current.ProjectID
		} else {
			if err := client.requireParentInProject(ctx, current.ProjectID, *input.ParentID); err != nil {
				return nil, err
			}
			parentID, err := decodeID("tdt1_", *input.ParentID)
			if err != nil {
				return nil, err
			}
			move["parent_id"] = parentID
		}
		mutations = append(mutations, newCommand("item_move", "", move))
	}
	reminderDeletes := make([]syncCommand, 0)
	reminderAdds := make([]syncCommand, 0, len(input.Reminders))
	if input.ReplaceReminders {
		existing, err := client.remindersByTask(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		for _, value := range existing[current.ID] {
			if value.Type == "relative" || value.Type == "absolute" {
				reminderDeletes = append(reminderDeletes, newCommand(
					"reminder_delete", "", map[string]any{"id": value.ID},
				))
			}
		}
		effectiveStart := currentStart
		if input.ReplaceStart {
			effectiveStart = input.Start
		}
		added, err := client.addReminderCommands(current.ID, effectiveStart, input.Reminders)
		if err != nil {
			return nil, err
		}
		reminderAdds = append(reminderAdds, added...)
	}
	if input.ReplaceStart && !input.ReplaceReminders &&
		(input.Start == nil || input.Start.Kind == application.TaskTemporalDate) {
		existing, err := client.remindersByTask(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		for _, value := range existing[current.ID] {
			if value.Type == "relative" && !value.Deleted {
				return nil, errors.New("clearing a todoist reminder time requires explicit reminder replacement")
			}
		}
	}
	commands := make([]syncCommand, 0, len(reminderDeletes)+len(mutations)+len(reminderAdds))
	commands = append(commands, reminderDeletes...)
	commands = append(commands, mutations...)
	commands = append(commands, reminderAdds...)
	return commands, nil
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
	current, err := client.exactTask(ctx, projectID, taskID, input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if current.Checked || current.CompletedAt != "" {
		return application.Task{}, errors.New("todoist task is already completed")
	}
	completedAt := time.Now().UTC()
	if _, err := client.runCommands(ctx, []syncCommand{
		newCommand("item_complete", "", map[string]any{
			"id": taskID, "date_completed": completedAt.Format(time.RFC3339Nano),
		}),
	}); err != nil {
		return application.Task{}, err
	}
	completed, err := client.getCompletedTask(
		ctx, projectID, taskID, completedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	view, err := client.taskView(input.ListID, completed, nil)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return view, nil
}

func (client *Client) ReopenTask(
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
	current, err := client.exactTask(ctx, projectID, taskID, input.Version)
	if err != nil {
		return application.Task{}, err
	}
	if !current.Checked && current.CompletedAt == "" {
		return application.Task{}, errors.New("todoist task is not completed")
	}
	if _, err := client.runCommands(ctx, []syncCommand{
		newCommand("item_uncomplete", "", map[string]any{"id": taskID}),
	}); err != nil {
		return application.Task{}, err
	}
	reopened, err := client.getActiveTask(ctx, projectID, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	reminders, err := client.remindersByTask(ctx, taskID)
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	view, err := client.taskView(input.ListID, reopened, reminders[taskID])
	if err != nil {
		return application.Task{}, writeAssemblyError(err)
	}
	return view, nil
}

func (client *Client) DeleteTask(
	ctx context.Context,
	input application.TaskDeleteInput,
) error {
	if err := client.requireWrite(); err != nil {
		return err
	}
	projectID, taskID, err := decodeRoute(input.ListID, input.TaskID)
	if err != nil {
		return err
	}
	if _, err := client.exactTask(ctx, projectID, taskID, input.Version); err != nil {
		return err
	}
	_, err = client.runCommands(ctx, []syncCommand{
		newCommand("item_delete", "", map[string]any{"id": taskID}),
	})
	return err
}

func (client *Client) applyAssignees(args map[string]any, values []application.TaskAssignee) error {
	if len(values) > 1 {
		return errors.New("todoist supports exactly one responsible assignee")
	}
	if len(values) == 0 {
		args["responsible_uid"] = nil
		return nil
	}
	userID, err := decodeID("tda1_", values[0].ID)
	if err != nil {
		return err
	}
	args["responsible_uid"] = userID
	return nil
}

func (client *Client) requireAssignableProject(ctx context.Context, projectID string) error {
	project, err := client.getProject(ctx, projectID)
	if err != nil {
		return err
	}
	if !project.CanAssignTasks {
		return errors.New("the selected Todoist project does not permit task assignment")
	}
	return nil
}

func (client *Client) requireParentInProject(
	ctx context.Context,
	projectID, parentValue string,
) error {
	if parentValue == "" {
		return nil
	}
	parentID, err := decodeID("tdt1_", parentValue)
	if err != nil {
		return err
	}
	if _, err := client.getActiveTask(ctx, projectID, parentID); err != nil {
		return fmt.Errorf("validate Todoist parent in selected project: %w", err)
	}
	return nil
}

func (client *Client) writeDeadline(value *application.TaskTemporal) (any, error) {
	if !client.plan.Deadlines {
		return nil, errors.New("the current Todoist plan does not enable deadlines")
	}
	if value == nil {
		return nil, nil
	}
	if value.Kind != application.TaskTemporalDate {
		return nil, errors.New("todoist deadlines support date-only values")
	}
	return map[string]any{"date": value.Value}, nil
}

func writeDue(
	value *application.TaskTemporal,
	recurrence *application.TaskRecurrence,
) (any, error) {
	if recurrence != nil {
		if recurrence.Frequency != application.TaskRecurrenceProvider {
			return nil, errors.New("todoist recurrence writes require an exact read-derived provider rule")
		}
		rule, err := decodeRecurrence(recurrence.ProviderRule)
		if err != nil {
			return nil, err
		}
		result := map[string]any{"string": rule.String, "date": rule.Date}
		if rule.Language != "" {
			result["lang"] = rule.Language
		}
		if rule.TimeZone != "" {
			result["timezone"] = rule.TimeZone
		}
		if value != nil {
			date, zone, err := writeTemporalDate(value)
			if err != nil {
				return nil, err
			}
			result["date"] = date
			if zone != "" {
				result["timezone"] = zone
			} else {
				delete(result, "timezone")
			}
		}
		return result, nil
	}
	if value == nil {
		return nil, nil
	}
	date, zone, err := writeTemporalDate(value)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"date": date}
	if zone != "" {
		result["timezone"] = zone
	}
	return result, nil
}

func writeTemporalDate(value *application.TaskTemporal) (string, string, error) {
	switch value.Kind {
	case application.TaskTemporalDate:
		return value.Value, "", nil
	case application.TaskTemporalFloating:
		return value.Value + ".000000", "", nil
	case application.TaskTemporalZoned:
		instant, err := time.Parse(time.RFC3339, value.Value)
		if err != nil {
			return "", "", errors.New("zoned task datetime is malformed")
		}
		return instant.UTC().Format("2006-01-02T15:04:05.000000Z"), value.TimeZone, nil
	default:
		return "", "", errors.New("task temporal kind is invalid")
	}
}

func (client *Client) addReminderCommands(
	taskID string,
	start *application.TaskTemporal,
	values []application.TaskReminder,
) ([]syncCommand, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if !client.plan.Reminders {
		return nil, errors.New("the current Todoist plan does not enable reminders")
	}
	result := make([]syncCommand, 0, len(values))
	for _, value := range values {
		args := map[string]any{"item_id": taskID}
		switch value.Kind {
		case application.TaskReminderRelativeStart:
			if start == nil || start.Kind == application.TaskTemporalDate {
				return nil, errors.New("todoist relative reminder requires a scheduled datetime")
			}
			if value.OffsetMinutes > 0 {
				return nil, errors.New("todoist relative reminders must occur at or before the task start")
			}
			args["type"] = "relative"
			args["minute_offset"] = -value.OffsetMinutes
		case application.TaskReminderAbsolute:
			if value.At == nil || value.At.Kind == application.TaskTemporalDate {
				return nil, errors.New("todoist absolute reminder requires a datetime")
			}
			date, zone, err := writeTemporalDate(value.At)
			if err != nil {
				return nil, err
			}
			providerDue := map[string]any{"date": date}
			if zone != "" {
				providerDue["timezone"] = zone
			}
			args["type"] = "absolute"
			args["due"] = providerDue
		case application.TaskReminderRelativeDue:
			return nil, errors.New("todoist reminders are relative to its scheduling date, not its deadline")
		default:
			return nil, errors.New("task reminder kind is invalid")
		}
		result = append(result, newCommand("reminder_add", uuid.NewString(), args))
	}
	return result, nil
}

func newCommand(kind, tempID string, args map[string]any) syncCommand {
	return syncCommand{Type: kind, UUID: uuid.NewString(), TempID: tempID, Args: args}
}

func (client *Client) runCommands(
	ctx context.Context,
	commands []syncCommand,
) (syncResponse, error) {
	if len(commands) == 0 || len(commands) > maximumCommandsPerBatch {
		return syncResponse{}, errors.New("todoist command batch is empty or too large")
	}
	encoded, err := marshalJSON(commands)
	if err != nil {
		return syncResponse{}, err
	}
	form := url.Values{"commands": {encoded}}
	if len(form.Encode()) > maximumCommandFormBytes {
		return syncResponse{}, errors.New("todoist command batch exceeds the provider request limit")
	}
	var response syncResponse
	if _, err := client.api.DoForm(
		ctx, http.MethodPost, "sync", nil,
		form, &response,
		true, nil, http.StatusOK,
	); err != nil {
		return syncResponse{}, err
	}
	if len(response.SyncStatus) != len(commands) {
		return syncResponse{}, fmt.Errorf(
			"%w: Todoist omitted a command result",
			application.ErrWriteOutcomeUnknown,
		)
	}
	succeeded := 0
	rejected := 0
	for _, command := range commands {
		status, exists := response.SyncStatus[command.UUID]
		if !exists {
			return syncResponse{}, fmt.Errorf(
				"%w: Todoist omitted a command result",
				application.ErrWriteOutcomeUnknown,
			)
		}
		ok, knownRejection := classifyCommandStatus(status)
		if ok {
			succeeded++
		} else if knownRejection {
			rejected++
		}
	}
	if succeeded == len(commands) {
		return response, nil
	}
	if succeeded != 0 || rejected != len(commands) {
		return syncResponse{}, fmt.Errorf(
			"%w: Todoist command batch did not return one complete known outcome",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return syncResponse{}, errors.New("todoist rejected the command batch")
}

func classifyCommandStatus(status json.RawMessage) (ok, knownRejection bool) {
	if strings.TrimSpace(string(status)) == `"ok"` {
		return true, false
	}
	var failure struct {
		ErrorCode *int   `json:"error_code"`
		ErrorTag  string `json:"error_tag"`
		HTTPCode  *int   `json:"http_code"`
	}
	if json.Unmarshal(status, &failure) != nil ||
		failure.ErrorCode == nil && failure.ErrorTag == "" ||
		len(failure.ErrorTag) > 128 || strings.ContainsAny(failure.ErrorTag, "\r\n\x00") ||
		failure.HTTPCode != nil && (*failure.HTTPCode == http.StatusRequestTimeout ||
			*failure.HTTPCode == http.StatusTooManyRequests || *failure.HTTPCode >= 500) {
		return false, false
	}
	return false, true
}

func writeAssemblyError(err error) error {
	return fmt.Errorf(
		"%w: read Todoist state after accepted write: %w",
		application.ErrWriteOutcomeUnknown,
		err,
	)
}
