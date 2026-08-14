package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const taskNotesPreviewRunes = 500

type TaskCreateInput struct {
	Account     domain.AccountID         `json:"account"`
	ListID      string                   `json:"listId"`
	Title       string                   `json:"title"`
	Notes       string                   `json:"notes,omitempty"`
	Priority    TaskPriority             `json:"priority"`
	Start       *TaskTemporal            `json:"start,omitempty"`
	Due         *TaskTemporal            `json:"due,omitempty"`
	Reminders   []TaskReminder           `json:"reminders,omitempty"`
	Recurrence  *TaskRecurrence          `json:"recurrence,omitempty"`
	ParentID    string                   `json:"parentId,omitempty"`
	Checklist   []TaskChecklistItemInput `json:"checklist,omitempty"`
	Assignees   []TaskAssignee           `json:"assignees,omitempty"`
	Labels      []string                 `json:"labels,omitempty"`
	Attachments []TaskAttachmentLink     `json:"attachments,omitempty"`
	Sources     []TaskLinkedSource       `json:"sources,omitempty"`
}

func (input TaskCreateInput) Validate() error {
	if err := validateTaskMutationRoute(input.Account, input.ListID, "", ""); err != nil {
		return err
	}
	if err := validateTaskText("task title", input.Title, MaxTaskTitleBytes, false, false); err != nil {
		return err
	}
	if err := validateTaskText("task notes", input.Notes, MaxTaskNotesBytes, true, false); err != nil {
		return err
	}
	if err := input.Priority.Validate(); err != nil {
		return err
	}
	if err := validateTaskCreateFields(input); err != nil {
		return err
	}
	return validateTaskMutationCollections(
		input.Reminders, input.Checklist, input.Assignees, input.Labels,
		input.Attachments, input.Sources,
	)
}

type TaskUpdateInput struct {
	Account            domain.AccountID         `json:"account"`
	ListID             string                   `json:"listId"`
	TaskID             string                   `json:"taskId"`
	Version            string                   `json:"version"`
	Title              *string                  `json:"title,omitempty"`
	Notes              *string                  `json:"notes,omitempty"`
	Priority           *TaskPriority            `json:"priority,omitempty"`
	ParentID           *string                  `json:"parentId,omitempty"`
	Order              *string                  `json:"order,omitempty"`
	ReplaceStart       bool                     `json:"replaceStart,omitempty"`
	Start              *TaskTemporal            `json:"start,omitempty"`
	ReplaceDue         bool                     `json:"replaceDue,omitempty"`
	Due                *TaskTemporal            `json:"due,omitempty"`
	ReplaceReminders   bool                     `json:"replaceReminders,omitempty"`
	Reminders          []TaskReminder           `json:"reminders,omitempty"`
	ReplaceRecurrence  bool                     `json:"replaceRecurrence,omitempty"`
	Recurrence         *TaskRecurrence          `json:"recurrence,omitempty"`
	ReplaceChecklist   bool                     `json:"replaceChecklist,omitempty"`
	Checklist          []TaskChecklistItemInput `json:"checklist,omitempty"`
	ReplaceAssignees   bool                     `json:"replaceAssignees,omitempty"`
	Assignees          []TaskAssignee           `json:"assignees,omitempty"`
	ReplaceLabels      bool                     `json:"replaceLabels,omitempty"`
	Labels             []string                 `json:"labels,omitempty"`
	ReplaceAttachments bool                     `json:"replaceAttachments,omitempty"`
	Attachments        []TaskAttachmentLink     `json:"attachments,omitempty"`
	ReplaceSources     bool                     `json:"replaceSources,omitempty"`
	Sources            []TaskLinkedSource       `json:"sources,omitempty"`
}

func (input TaskUpdateInput) Validate() error {
	if err := validateTaskMutationRoute(input.Account, input.ListID, input.TaskID, input.Version); err != nil {
		return err
	}
	if input.TaskID == "" {
		return errors.New("task update requires an exact task identity and version")
	}
	if !input.hasChanges() {
		return errors.New("task update must change at least one supported field")
	}
	if input.Title != nil {
		if err := validateTaskText("task title", *input.Title, MaxTaskTitleBytes, false, false); err != nil {
			return err
		}
	}
	if input.Notes != nil {
		if err := validateTaskText("task notes", *input.Notes, MaxTaskNotesBytes, true, false); err != nil {
			return err
		}
	}
	if input.Priority != nil {
		if err := input.Priority.Validate(); err != nil {
			return err
		}
	}
	if input.ParentID != nil && *input.ParentID != "" {
		if err := validateOpaqueValue("parent task ID", *input.ParentID); err != nil {
			return err
		}
		if *input.ParentID == input.TaskID {
			return errors.New("task cannot be its own parent")
		}
	}
	if input.Order != nil {
		if err := validateTaskText("task order", *input.Order, 1024, true, true); err != nil {
			return err
		}
	}
	if err := validateOptionalTaskTemporal("start", input.ReplaceStart, input.Start); err != nil {
		return err
	}
	if err := validateOptionalTaskTemporal("due", input.ReplaceDue, input.Due); err != nil {
		return err
	}
	if err := validateOptionalTaskRecurrence(input.ReplaceRecurrence, input.Recurrence); err != nil {
		return err
	}
	if err := validateTaskReplacementFlags(input); err != nil {
		return err
	}
	return validateTaskMutationCollections(
		input.Reminders, input.Checklist, input.Assignees, input.Labels,
		input.Attachments, input.Sources,
	)
}

func (input TaskUpdateInput) hasChanges() bool {
	return input.Title != nil || input.Notes != nil || input.Priority != nil || input.ParentID != nil ||
		input.Order != nil || input.ReplaceStart || input.ReplaceDue || input.ReplaceReminders ||
		input.ReplaceRecurrence || input.ReplaceChecklist || input.ReplaceAssignees ||
		input.ReplaceLabels || input.ReplaceAttachments || input.ReplaceSources
}

type TaskStateInput struct {
	Account domain.AccountID `json:"account"`
	ListID  string           `json:"listId"`
	TaskID  string           `json:"taskId"`
	Version string           `json:"version"`
}

func (input TaskStateInput) Validate() error {
	if err := validateTaskMutationRoute(input.Account, input.ListID, input.TaskID, input.Version); err != nil {
		return err
	}
	if input.TaskID == "" {
		return errors.New("task mutation requires an exact task identity and version")
	}
	return nil
}

type TaskDeleteInput = TaskStateInput

// TaskTextReview binds the complete text by digest while keeping previews
// practical for terminals and MCP clients.
type TaskTextReview struct {
	Preview string `json:"preview,omitempty"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

type TaskWriteReview struct {
	Account            domain.AccountID         `json:"account"`
	Provider           domain.ProviderID        `json:"provider"`
	Capabilities       TaskCapabilities         `json:"capabilities"`
	Action             string                   `json:"action"`
	ListID             string                   `json:"listId"`
	TaskID             string                   `json:"taskId,omitempty"`
	Version            string                   `json:"version,omitempty"`
	Title              *string                  `json:"title,omitempty"`
	Notes              *TaskTextReview          `json:"notes,omitempty"`
	Priority           *TaskPriority            `json:"priority,omitempty"`
	ParentID           *string                  `json:"parentId,omitempty"`
	Order              *string                  `json:"order,omitempty"`
	ReplaceStart       bool                     `json:"replaceStart,omitempty"`
	Start              *TaskTemporal            `json:"start,omitempty"`
	ReplaceDue         bool                     `json:"replaceDue,omitempty"`
	Due                *TaskTemporal            `json:"due,omitempty"`
	ReplaceReminders   bool                     `json:"replaceReminders,omitempty"`
	Reminders          []TaskReminder           `json:"reminders,omitempty"`
	ReplaceRecurrence  bool                     `json:"replaceRecurrence,omitempty"`
	Recurrence         *TaskRecurrence          `json:"recurrence,omitempty"`
	ReplaceChecklist   bool                     `json:"replaceChecklist,omitempty"`
	Checklist          []TaskChecklistItemInput `json:"checklist,omitempty"`
	ReplaceAssignees   bool                     `json:"replaceAssignees,omitempty"`
	Assignees          []TaskAssignee           `json:"assignees,omitempty"`
	ReplaceLabels      bool                     `json:"replaceLabels,omitempty"`
	Labels             []string                 `json:"labels,omitempty"`
	ReplaceAttachments bool                     `json:"replaceAttachments,omitempty"`
	Attachments        []TaskAttachmentLink     `json:"attachments,omitempty"`
	ReplaceSources     bool                     `json:"replaceSources,omitempty"`
	Sources            []TaskLinkedSource       `json:"sources,omitempty"`
	Degradations       []domain.Degradation     `json:"degradations,omitempty"`
}

type TaskDeleteResult struct {
	ListID     string            `json:"listId"`
	TaskID     string            `json:"taskId"`
	Provenance domain.Provenance `json:"provenance"`
}

// TaskWriteAccess is either an immutable review or one completed mutation.
type TaskWriteAccess struct {
	Status  string            `json:"status"`
	Task    *Task             `json:"task,omitempty"`
	Deleted *TaskDeleteResult `json:"deleted,omitempty"`
	Review  TaskWriteReview   `json:"review"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

type TaskWriter interface {
	CreateTask(context.Context, TaskCreateInput) (Task, error)
	UpdateTask(context.Context, TaskUpdateInput) (Task, error)
	CompleteTask(context.Context, TaskStateInput) (Task, error)
	ReopenTask(context.Context, TaskStateInput) (Task, error)
	DeleteTask(context.Context, TaskDeleteInput) error
}

func (service *TaskService) Create(ctx context.Context, input TaskCreateInput, caller domain.Caller) (TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return TaskWriteAccess{}, err
	}
	if err := service.validateWrite(input.Account, "create", input); err != nil {
		return TaskWriteAccess{}, err
	}
	return service.prepareWrite(ctx, "task.create", domain.EffectExternalWrite, input.Account,
		taskListTarget(input.ListID), input, input.review(), caller)
}

func (service *TaskService) Update(ctx context.Context, input TaskUpdateInput, caller domain.Caller) (TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return TaskWriteAccess{}, err
	}
	if err := service.validateWrite(input.Account, "update", input); err != nil {
		return TaskWriteAccess{}, err
	}
	return service.prepareWrite(ctx, "task.update", domain.EffectExternalWrite, input.Account,
		taskTarget(input.ListID, input.TaskID), input, input.review(), caller)
}

func (service *TaskService) Complete(ctx context.Context, input TaskStateInput, caller domain.Caller) (TaskWriteAccess, error) {
	return service.prepareStateWrite(ctx, "complete", input, caller)
}

func (service *TaskService) Reopen(ctx context.Context, input TaskStateInput, caller domain.Caller) (TaskWriteAccess, error) {
	return service.prepareStateWrite(ctx, "reopen", input, caller)
}

func (service *TaskService) Delete(ctx context.Context, input TaskDeleteInput, caller domain.Caller) (TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return TaskWriteAccess{}, err
	}
	if err := service.validateWrite(input.Account, "delete", input); err != nil {
		return TaskWriteAccess{}, err
	}
	return service.prepareWrite(ctx, "task.delete", domain.EffectDestructiveWrite, input.Account,
		taskTarget(input.ListID, input.TaskID), input, input.review("delete"), caller)
}

func (service *TaskService) prepareStateWrite(
	ctx context.Context,
	action string,
	input TaskStateInput,
	caller domain.Caller,
) (TaskWriteAccess, error) {
	if err := input.Validate(); err != nil {
		return TaskWriteAccess{}, err
	}
	if err := service.validateWrite(input.Account, action, input); err != nil {
		return TaskWriteAccess{}, err
	}
	return service.prepareWrite(ctx, "task."+action, domain.EffectExternalWrite, input.Account,
		taskTarget(input.ListID, input.TaskID), input, input.review(action), caller)
}

func (service *TaskService) prepareWrite(
	ctx context.Context,
	name string,
	effect domain.Effect,
	account domain.AccountID,
	target domain.TargetRef,
	payload any,
	review TaskWriteReview,
	caller domain.Caller,
) (TaskWriteAccess, error) {
	review = service.decorateTaskReview(review)
	operation, err := domain.NewTargetedOperation(name, effect, account, target, payload)
	if err != nil {
		return TaskWriteAccess{}, fmt.Errorf("create %s operation: %w", name, err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return TaskWriteAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return TaskWriteAccess{Status: "approval_required", Review: review, Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return TaskWriteAccess{}, fmt.Errorf("%s operation was denied", name)
	case policy.VerdictAllow:
		return TaskWriteAccess{}, fmt.Errorf("%s policy attempted to bypass mandatory preview", name)
	default:
		return TaskWriteAccess{}, fmt.Errorf("%s operation received an unknown policy verdict", name)
	}
}

func (service *TaskService) CommitCreate(ctx context.Context, token string, caller domain.Caller) (TaskWriteAccess, error) {
	var input TaskCreateInput
	operation, err := service.committedWrite(ctx, token, caller, "task.create", domain.EffectExternalWrite, &input)
	if err != nil {
		return TaskWriteAccess{}, err
	}
	created, err := service.port.CreateTask(ctx, input)
	if err := service.finishTaskWrite(ctx, operation, caller, input.ListID, "", &created, err); err != nil {
		return TaskWriteAccess{}, err
	}
	return TaskWriteAccess{Status: "created", Task: &created, Review: service.decorateTaskReview(input.review())}, nil
}

func (service *TaskService) CommitUpdate(ctx context.Context, token string, caller domain.Caller) (TaskWriteAccess, error) {
	var input TaskUpdateInput
	operation, err := service.committedWrite(ctx, token, caller, "task.update", domain.EffectExternalWrite, &input)
	if err != nil {
		return TaskWriteAccess{}, err
	}
	updated, err := service.port.UpdateTask(ctx, input)
	if err := service.finishTaskWrite(ctx, operation, caller, input.ListID, input.TaskID, &updated, err); err != nil {
		return TaskWriteAccess{}, err
	}
	return TaskWriteAccess{Status: "updated", Task: &updated, Review: service.decorateTaskReview(input.review())}, nil
}

func (service *TaskService) CommitComplete(ctx context.Context, token string, caller domain.Caller) (TaskWriteAccess, error) {
	return service.commitStateWrite(ctx, token, caller, "complete", TaskStatusCompleted, service.port.CompleteTask)
}

func (service *TaskService) CommitReopen(ctx context.Context, token string, caller domain.Caller) (TaskWriteAccess, error) {
	return service.commitStateWrite(ctx, token, caller, "reopen", TaskStatusNeedsAction, service.port.ReopenTask)
}

func (service *TaskService) CommitDelete(ctx context.Context, token string, caller domain.Caller) (TaskWriteAccess, error) {
	var input TaskDeleteInput
	operation, err := service.committedWrite(ctx, token, caller, "task.delete", domain.EffectDestructiveWrite, &input)
	if err != nil {
		return TaskWriteAccess{}, err
	}
	callErr := service.port.DeleteTask(ctx, input)
	if err := service.finishWriteAudit(ctx, operation, caller, callErr); err != nil {
		return TaskWriteAccess{}, err
	}
	return TaskWriteAccess{
		Status: "deleted",
		Deleted: &TaskDeleteResult{ListID: input.ListID, TaskID: input.TaskID,
			Provenance: service.taskProvenance(input.ListID, input.TaskID)},
		Review: service.decorateTaskReview(input.review("delete")),
	}, nil
}

type taskStateWriter func(context.Context, TaskStateInput) (Task, error)

func (service *TaskService) commitStateWrite(
	ctx context.Context,
	token string,
	caller domain.Caller,
	action string,
	expected TaskStatus,
	write taskStateWriter,
) (TaskWriteAccess, error) {
	var input TaskStateInput
	operation, err := service.committedWrite(ctx, token, caller, "task."+action, domain.EffectExternalWrite, &input)
	if err != nil {
		return TaskWriteAccess{}, err
	}
	task, callErr := write(ctx, input)
	if callErr == nil && task.Status != expected {
		callErr = errors.Join(ErrWriteOutcomeUnknown, fmt.Errorf("task provider did not confirm %s", action))
	}
	if err := service.finishTaskWrite(ctx, operation, caller, input.ListID, input.TaskID, &task, callErr); err != nil {
		return TaskWriteAccess{}, err
	}
	status := "completed"
	if action == "reopen" {
		status = "reopened"
	}
	return TaskWriteAccess{Status: status, Task: &task, Review: service.decorateTaskReview(input.review(action))}, nil
}

func (service *TaskService) decorateTaskReview(review TaskWriteReview) TaskWriteReview {
	review.Account = service.provenance.AccountID
	review.Provider = service.provenance.Provider
	review.Capabilities = service.capabilities
	review.Degradations = append([]domain.Degradation(nil), service.degradations...)
	return review
}

func (service *TaskService) committedWrite(
	ctx context.Context,
	token string,
	caller domain.Caller,
	name string,
	effect domain.Effect,
	destination any,
) (domain.Operation, error) {
	operation, err := service.guard.CommitFor(ctx, token, caller, name, effect)
	if err != nil {
		return domain.Operation{}, err
	}
	if err := operation.DecodePayload(destination); err != nil {
		return domain.Operation{}, err
	}
	if operation.Account() != service.provenance.AccountID {
		return domain.Operation{}, errors.New("task operation account does not match the routed service")
	}
	switch input := destination.(type) {
	case *TaskCreateInput:
		err = input.Validate()
	case *TaskUpdateInput:
		err = input.Validate()
	case *TaskStateInput:
		err = input.Validate()
	default:
		return domain.Operation{}, errors.New("task operation has an unsupported payload")
	}
	if err != nil {
		return domain.Operation{}, err
	}
	return operation, service.validateWrite(operation.Account(), name[len("task."):], destination)
}

func (service *TaskService) finishTaskWrite(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	listID string,
	taskID string,
	task *Task,
	callErr error,
) error {
	if callErr == nil {
		service.normalizeTask(listID, task)
		if task.ListID != listID {
			callErr = errors.Join(ErrWriteOutcomeUnknown, errors.New("task provider returned a task from another list"))
		} else if taskID != "" && task.ID != taskID {
			callErr = errors.Join(ErrWriteOutcomeUnknown, errors.New("task provider returned a different task"))
		} else if err := task.Validate(); err != nil {
			callErr = errors.Join(ErrWriteOutcomeUnknown, fmt.Errorf("validate task write result: %w", err))
		} else if err := validateTaskEncodedSize("task write result", task, MaxTaskResultBytes); err != nil {
			callErr = errors.Join(ErrWriteOutcomeUnknown, err)
		}
	}
	return service.finishWriteAudit(ctx, operation, caller, callErr)
}

func (service *TaskService) finishWriteAudit(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	callErr error,
) error {
	outcome, reason := taskWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	return errors.Join(callErr, auditErr)
}

func (service *TaskService) validateWrite(account domain.AccountID, action string, input any) error {
	if err := service.validateAccount(account); err != nil {
		return err
	}
	supported := map[string]bool{
		"create": service.capabilities.Create, "update": service.capabilities.Update,
		"complete": service.capabilities.Complete, "reopen": service.capabilities.Reopen,
		"delete": service.capabilities.Delete,
	}
	if !supported[action] {
		return fmt.Errorf("the selected task provider does not support %s", action)
	}
	switch value := input.(type) {
	case TaskCreateInput:
		return service.validateCreateCapabilities(value)
	case TaskUpdateInput:
		return service.validateUpdateCapabilities(value)
	case *TaskCreateInput:
		return service.validateCreateCapabilities(*value)
	case *TaskUpdateInput:
		return service.validateUpdateCapabilities(*value)
	}
	return nil
}

func (service *TaskService) validateCreateCapabilities(input TaskCreateInput) error {
	return service.validateFeatureUse(taskFeatureUse{
		reminders: len(input.Reminders) > 0, recurrence: input.Recurrence != nil,
		subtasks: input.ParentID != "", checklist: len(input.Checklist) > 0,
		assignments: len(input.Assignees) > 0, labels: len(input.Labels) > 0,
		attachments: len(input.Attachments) > 0, temporals: []*TaskTemporal{input.Start, input.Due},
		sources: len(input.Sources) > 0,
	})
}

func (service *TaskService) validateUpdateCapabilities(input TaskUpdateInput) error {
	for _, source := range input.Sources {
		if source.Kind == TaskSourceTask && source.Account == input.Account &&
			source.Provider == service.provenance.Provider && source.ObjectID == input.TaskID {
			return errors.New("task source linkage forms a self-loop")
		}
	}
	return service.validateFeatureUse(taskFeatureUse{
		reminders: input.ReplaceReminders, recurrence: input.ReplaceRecurrence,
		subtasks: input.ParentID != nil, checklist: input.ReplaceChecklist,
		assignments: input.ReplaceAssignees, labels: input.ReplaceLabels,
		attachments: input.ReplaceAttachments, temporals: []*TaskTemporal{input.Start, input.Due},
		sources: input.ReplaceSources, ordering: input.Order != nil,
	})
}

type taskFeatureUse struct {
	reminders, recurrence, subtasks, checklist, assignments, labels, attachments bool
	sources, ordering                                                            bool
	temporals                                                                    []*TaskTemporal
}

func (service *TaskService) validateFeatureUse(use taskFeatureUse) error {
	checks := []struct {
		used, supported bool
		name            string
	}{
		{use.reminders, service.capabilities.Reminders, "reminders"},
		{use.recurrence, service.capabilities.Recurrence, "recurrence"},
		{use.subtasks, service.capabilities.Subtasks, "subtasks"},
		{use.checklist, service.capabilities.Checklist, "checklists"},
		{use.assignments, service.capabilities.Assignments, "assignments"},
		{use.labels, service.capabilities.Labels, "labels"},
		{use.attachments, service.capabilities.Attachments, "attachments"},
		{use.sources, service.capabilities.LinkedSources, "linked sources"},
		{use.ordering, service.capabilities.Ordering, "ordering"},
	}
	for _, check := range checks {
		if check.used && !check.supported {
			return fmt.Errorf("the selected task provider does not support %s", check.name)
		}
	}
	for _, temporal := range use.temporals {
		if temporal == nil {
			continue
		}
		var supported bool
		switch temporal.Kind {
		case TaskTemporalDate:
			supported = service.capabilities.DateOnly
		case TaskTemporalFloating:
			supported = service.capabilities.FloatingDateTime
		case TaskTemporalZoned:
			supported = service.capabilities.ZonedDateTime
		}
		if !supported {
			return fmt.Errorf("the selected task provider does not support %s", temporal.Kind)
		}
	}
	return nil
}

func validateTaskMutationRoute(account domain.AccountID, listID, taskID, version string) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateOpaqueValue("task list ID", listID); err != nil {
		return err
	}
	for name, value := range map[string]string{"task ID": taskID, "task version": version} {
		if value != "" {
			if err := validateOpaqueValue(name, value); err != nil {
				return err
			}
		}
	}
	if (taskID == "") != (version == "") {
		return errors.New("task identity and version must be supplied together")
	}
	return nil
}

func validateTaskCreateFields(input TaskCreateInput) error {
	if input.ParentID != "" {
		if err := validateOpaqueValue("parent task ID", input.ParentID); err != nil {
			return err
		}
	}
	for _, temporal := range []*TaskTemporal{input.Start, input.Due} {
		if temporal != nil {
			if err := temporal.Validate(); err != nil {
				return err
			}
		}
	}
	for _, reminder := range input.Reminders {
		if reminder.Kind == TaskReminderRelativeStart && input.Start == nil {
			return errors.New("relative-start task reminder requires a start")
		}
		if reminder.Kind == TaskReminderRelativeDue && input.Due == nil {
			return errors.New("relative-due task reminder requires a due value")
		}
	}
	if input.Recurrence != nil {
		return input.Recurrence.Validate()
	}
	return nil
}

func validateOptionalTaskTemporal(name string, replace bool, temporal *TaskTemporal) error {
	if temporal != nil && !replace {
		flag := "replaceStart"
		if name == "due" {
			flag = "replaceDue"
		}
		return fmt.Errorf("task %s requires %s=true", name, flag)
	}
	if temporal != nil {
		return temporal.Validate()
	}
	return nil
}

func validateOptionalTaskRecurrence(replace bool, recurrence *TaskRecurrence) error {
	if recurrence != nil && !replace {
		return errors.New("task recurrence requires replaceRecurrence=true")
	}
	if recurrence != nil {
		return recurrence.Validate()
	}
	return nil
}

func validateTaskReplacementFlags(input TaskUpdateInput) error {
	checks := []struct {
		replace bool
		length  int
		name    string
	}{
		{input.ReplaceReminders, len(input.Reminders), "reminders"},
		{input.ReplaceChecklist, len(input.Checklist), "checklist"},
		{input.ReplaceAssignees, len(input.Assignees), "assignees"},
		{input.ReplaceLabels, len(input.Labels), "labels"},
		{input.ReplaceAttachments, len(input.Attachments), "attachments"},
		{input.ReplaceSources, len(input.Sources), "sources"},
	}
	for _, check := range checks {
		if !check.replace && check.length > 0 {
			return fmt.Errorf("task %s require an explicit replace flag", check.name)
		}
	}
	return nil
}

func (input TaskCreateInput) review() TaskWriteReview {
	title := input.Title
	priority := input.Priority
	return TaskWriteReview{
		Action: "create", ListID: input.ListID, Title: &title, Notes: taskTextReview(input.Notes),
		Priority: &priority, ParentID: optionalString(input.ParentID),
		ReplaceStart: input.Start != nil, Start: cloneTaskTemporal(input.Start),
		ReplaceDue: input.Due != nil, Due: cloneTaskTemporal(input.Due),
		ReplaceReminders: len(input.Reminders) > 0, Reminders: cloneTaskReminders(input.Reminders),
		ReplaceRecurrence: input.Recurrence != nil, Recurrence: cloneTaskRecurrence(input.Recurrence),
		ReplaceChecklist: len(input.Checklist) > 0, Checklist: append([]TaskChecklistItemInput(nil), input.Checklist...),
		ReplaceAssignees: len(input.Assignees) > 0, Assignees: append([]TaskAssignee(nil), input.Assignees...),
		ReplaceLabels: len(input.Labels) > 0, Labels: append([]string(nil), input.Labels...),
		ReplaceAttachments: len(input.Attachments) > 0, Attachments: append([]TaskAttachmentLink(nil), input.Attachments...),
		ReplaceSources: len(input.Sources) > 0, Sources: append([]TaskLinkedSource(nil), input.Sources...),
	}
}

func (input TaskUpdateInput) review() TaskWriteReview {
	return TaskWriteReview{
		Action: "update", ListID: input.ListID, TaskID: input.TaskID, Version: input.Version,
		Title: cloneString(input.Title), Notes: optionalTaskTextReview(input.Notes), Priority: cloneTaskPriority(input.Priority),
		ParentID: cloneString(input.ParentID), Order: cloneString(input.Order),
		ReplaceStart: input.ReplaceStart, Start: cloneTaskTemporal(input.Start),
		ReplaceDue: input.ReplaceDue, Due: cloneTaskTemporal(input.Due),
		ReplaceReminders: input.ReplaceReminders, Reminders: cloneTaskReminders(input.Reminders),
		ReplaceRecurrence: input.ReplaceRecurrence, Recurrence: cloneTaskRecurrence(input.Recurrence),
		ReplaceChecklist: input.ReplaceChecklist, Checklist: append([]TaskChecklistItemInput(nil), input.Checklist...),
		ReplaceAssignees: input.ReplaceAssignees, Assignees: append([]TaskAssignee(nil), input.Assignees...),
		ReplaceLabels: input.ReplaceLabels, Labels: append([]string(nil), input.Labels...),
		ReplaceAttachments: input.ReplaceAttachments, Attachments: append([]TaskAttachmentLink(nil), input.Attachments...),
		ReplaceSources: input.ReplaceSources, Sources: append([]TaskLinkedSource(nil), input.Sources...),
	}
}

func (input TaskStateInput) review(action string) TaskWriteReview {
	return TaskWriteReview{Action: action, ListID: input.ListID, TaskID: input.TaskID, Version: input.Version}
}

func taskTextReview(value string) *TaskTextReview {
	digest := sha256.Sum256([]byte(value))
	return &TaskTextReview{Preview: prefixRunes(value, taskNotesPreviewRunes), Bytes: len(value), SHA256: hex.EncodeToString(digest[:])}
}

func optionalTaskTextReview(value *string) *TaskTextReview {
	if value == nil {
		return nil
	}
	return taskTextReview(*value)
}

func cloneTaskPriority(value *TaskPriority) *TaskPriority {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTaskTemporal(value *TaskTemporal) *TaskTemporal {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTaskRecurrence(value *TaskRecurrence) *TaskRecurrence {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.DaysOfWeek = append([]string(nil), value.DaysOfWeek...)
	cloned.Until = cloneTaskTemporal(value.Until)
	return &cloned
}

func cloneTaskReminders(values []TaskReminder) []TaskReminder {
	cloned := append([]TaskReminder(nil), values...)
	for index := range cloned {
		cloned[index].At = cloneTaskTemporal(cloned[index].At)
	}
	return cloned
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func prefixRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum]) + "…"
}

func taskWriteAuditOutcome(callErr error) (AuditOutcome, string) {
	if callErr == nil {
		return AuditOutcomeSuccess, "completed"
	}
	if errors.Is(callErr, ErrWriteOutcomeUnknown) {
		return AuditOutcomeUnknown, "outcome_unknown"
	}
	return AuditOutcomeFailure, "transport_error"
}
