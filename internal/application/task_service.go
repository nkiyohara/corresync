package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const MaxTaskResultBytes = 8 << 20

// TaskListReader discovers account-scoped task containers.
type TaskListReader interface {
	ListTaskLists(context.Context, TaskListInput) (TaskListPage, error)
}

// TaskReader is the provider-neutral read surface. ListID may be empty only
// for a provider that advertises a safe cross-list read.
type TaskReader interface {
	ListTasks(context.Context, TaskReadInput) (TaskPage, error)
	GetTask(context.Context, TaskGetInput) (Task, error)
	SearchTasks(context.Context, TaskSearchInput) (TaskPage, error)
}

type TaskSyncInput struct {
	Account domain.AccountID `json:"account"`
	ListID  string           `json:"listId"`
	Cursor  *TaskCursor      `json:"cursor,omitempty"`
	Limit   int              `json:"limit"`
}

func (input TaskSyncInput) Validate(provider domain.ProviderID) error {
	if err := input.ValidateRoute(); err != nil {
		return err
	}
	if input.Cursor != nil && input.Cursor.Provider != provider {
		return errors.New("task cursor does not match the selected provider")
	}
	return nil
}

// ValidateRoute validates the account/list/cursor boundary without needing
// the session-owned provider selection. Daemon clients use it before IPC.
func (input TaskSyncInput) ValidateRoute() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateOpaqueValue("task sync list ID", input.ListID); err != nil {
		return err
	}
	if input.Limit < 1 || input.Limit > MaxTaskPageSize {
		return fmt.Errorf("task sync limit must be between 1 and %d", MaxTaskPageSize)
	}
	if input.Cursor == nil {
		return nil
	}
	if err := input.Cursor.Validate(); err != nil {
		return err
	}
	if input.Cursor.Account != input.Account || input.Cursor.ListID != input.ListID {
		return errors.New("task cursor does not match the selected account and list")
	}
	return nil
}

type TaskIncrementalReader interface {
	SyncTasks(context.Context, TaskSyncInput) (TaskChangePage, error)
}

// TaskPort exposes only typed task use cases; it has no arbitrary provider
// action or generic property mutation.
type TaskPort interface {
	TaskListReader
	TaskReader
	TaskIncrementalReader
	TaskWriter
}

type TaskOptions struct {
	Provenance   domain.Provenance
	Capabilities TaskCapabilities
	Degradations []domain.Degradation
}

// TaskService applies the same policy, preview, account-isolation, and audit
// boundaries used for mail and calendar.
type TaskService struct {
	guard        *Guard
	port         TaskPort
	provenance   domain.Provenance
	capabilities TaskCapabilities
	degradations []domain.Degradation
}

func NewTaskService(guard *Guard, port TaskPort, options TaskOptions) (*TaskService, error) {
	if guard == nil {
		return nil, errors.New("task guard is required")
	}
	if port == nil {
		return nil, errors.New("task port is required")
	}
	if err := options.Provenance.AccountID.ValidateOpaque(); err != nil {
		return nil, fmt.Errorf("task provenance account: %w", err)
	}
	if err := options.Provenance.Provider.Validate(); err != nil {
		return nil, fmt.Errorf("task provenance provider: %w", err)
	}
	if options.Provenance.MailboxID != "" || options.Provenance.CalendarID != "" ||
		options.Provenance.TaskListID != "" || options.Provenance.SourceObjectID != "" {
		return nil, errors.New("task service provenance must not preselect a provider object")
	}
	if err := options.Capabilities.Validate(); err != nil {
		return nil, err
	}
	if !options.Capabilities.Read {
		return nil, errors.New("task service requires an observed read capability")
	}
	if err := validateTaskDegradations(options.Degradations); err != nil {
		return nil, err
	}
	return &TaskService{
		guard: guard, port: port, provenance: options.Provenance,
		capabilities: options.Capabilities,
		degradations: append([]domain.Degradation(nil), options.Degradations...),
	}, nil
}

func (service *TaskService) validateAccount(account domain.AccountID) error {
	if account != service.provenance.AccountID {
		return errors.New("task operation account does not match the routed service")
	}
	return nil
}

func (service *TaskService) ListLists(
	ctx context.Context,
	input TaskListInput,
	caller domain.Caller,
) (TaskListPage, error) {
	if err := input.Validate(); err != nil {
		return TaskListPage{}, err
	}
	if err := service.validateAccount(input.Account); err != nil {
		return TaskListPage{}, err
	}
	operation, err := domain.NewOperation("task.lists", domain.EffectRead, input.Account, input)
	if err != nil {
		return TaskListPage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return TaskListPage{}, err
	}
	page, callErr := service.port.ListTaskLists(ctx, input)
	if callErr == nil {
		callErr = service.normalizeListPage(input, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *TaskService) List(
	ctx context.Context,
	input TaskReadInput,
	caller domain.Caller,
) (TaskPage, error) {
	if err := input.Validate(); err != nil {
		return TaskPage{}, err
	}
	if err := service.validateAccount(input.Account); err != nil {
		return TaskPage{}, err
	}
	if input.ListID == "" && !service.capabilities.CrossListRead {
		return TaskPage{}, errors.New("the selected task provider does not support cross-list reads")
	}
	operation, err := domain.NewOperation("task.list", domain.EffectRead, input.Account, input)
	if err != nil {
		return TaskPage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return TaskPage{}, err
	}
	page, callErr := service.port.ListTasks(ctx, input)
	if callErr == nil {
		callErr = service.normalizeTaskPage(input.Offset, input.Limit, input.ListID, input.Status, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *TaskService) Get(
	ctx context.Context,
	input TaskGetInput,
	caller domain.Caller,
) (Task, error) {
	if err := input.Validate(); err != nil {
		return Task{}, err
	}
	if err := service.validateAccount(input.Account); err != nil {
		return Task{}, err
	}
	operation, err := domain.NewOperation("task.get", domain.EffectRead, input.Account, input)
	if err != nil {
		return Task{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return Task{}, err
	}
	task, callErr := service.port.GetTask(ctx, input)
	if callErr == nil {
		service.normalizeTask(input.ListID, &task)
		if task.ID != input.TaskID {
			callErr = errors.New("task provider returned a different task")
		} else {
			callErr = task.Validate()
			if callErr == nil {
				callErr = validateTaskEncodedSize("task result", task, MaxTaskResultBytes)
			}
		}
	}
	return task, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *TaskService) Search(
	ctx context.Context,
	input TaskSearchInput,
	caller domain.Caller,
) (TaskPage, error) {
	if err := input.Validate(); err != nil {
		return TaskPage{}, err
	}
	if err := service.validateAccount(input.Account); err != nil {
		return TaskPage{}, err
	}
	if !service.capabilities.Search {
		return TaskPage{}, errors.New("the selected task provider does not support search")
	}
	if input.ListID == "" && !service.capabilities.CrossListRead {
		return TaskPage{}, errors.New("the selected task provider does not support cross-list search")
	}
	operation, err := domain.NewOperation("task.search", domain.EffectRead, input.Account, input)
	if err != nil {
		return TaskPage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return TaskPage{}, err
	}
	page, callErr := service.port.SearchTasks(ctx, input)
	if callErr == nil {
		callErr = service.normalizeTaskPage(input.Offset, input.Limit, input.ListID, "", &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *TaskService) Sync(
	ctx context.Context,
	input TaskSyncInput,
	caller domain.Caller,
) (TaskChangePage, error) {
	if err := input.Validate(service.provenance.Provider); err != nil {
		return TaskChangePage{}, err
	}
	if err := service.validateAccount(input.Account); err != nil {
		return TaskChangePage{}, err
	}
	if len(service.capabilities.SyncModes) == 0 {
		return TaskChangePage{}, errors.New("the selected task provider has no incremental sync mode")
	}
	if input.Cursor != nil && !slices.Contains(service.capabilities.SyncModes, input.Cursor.Mode) {
		return TaskChangePage{}, errors.New("task cursor uses an unadvertised sync mode")
	}
	operation, err := domain.NewOperation("task.sync", domain.EffectRead, input.Account, input)
	if err != nil {
		return TaskChangePage{}, err
	}
	if err := service.allowRead(ctx, operation, caller); err != nil {
		return TaskChangePage{}, err
	}
	page, callErr := service.port.SyncTasks(ctx, input)
	if callErr == nil {
		callErr = service.normalizeChangePage(input, &page)
	}
	return page, errors.Join(callErr, service.recordRead(ctx, operation, caller, callErr))
}

func (service *TaskService) allowRead(ctx context.Context, operation domain.Operation, caller domain.Caller) error {
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return errors.New("task read operation was not allowed for immediate execution")
	}
	return nil
}

func (service *TaskService) recordRead(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	callErr error,
) error {
	return service.guard.RecordExecution(context.WithoutCancel(ctx), operation, caller, callErr)
}

func (service *TaskService) normalizeListPage(input TaskListInput, page *TaskListPage) error {
	if page.Offset != input.Offset || page.Limit != input.Limit || len(page.Lists) > input.Limit {
		return errors.New("task provider returned an invalid list page boundary")
	}
	seen := make(map[string]bool, len(page.Lists))
	for index := range page.Lists {
		list := &page.Lists[index]
		list.Capabilities = service.capabilities
		list.Degradations = mergeTaskDegradations(service.degradations, list.Degradations)
		list.Provenance = service.taskProvenance(list.ID, "")
		if seen[list.ID] {
			return errors.New("task provider returned a duplicate list")
		}
		seen[list.ID] = true
		if err := list.Validate(); err != nil {
			return fmt.Errorf("validate task list result: %w", err)
		}
	}
	return validateTaskEncodedSize("task-list page", page, MaxTaskResultBytes)
}

func (service *TaskService) normalizeTaskPage(offset, limit int, listID string, status TaskStatus, page *TaskPage) error {
	if page.Offset != offset || page.Limit != limit || len(page.Tasks) > limit {
		return errors.New("task provider returned an invalid task page boundary")
	}
	seen := make(map[string]bool, len(page.Tasks))
	for index := range page.Tasks {
		task := &page.Tasks[index]
		service.normalizeTask(task.ListID, task)
		if listID != "" && task.ListID != listID {
			return errors.New("task provider returned an item from another list")
		}
		if status != "" && task.Status != status {
			return errors.New("task provider returned an item outside the requested status")
		}
		key := task.ListID + "\x00" + task.ID
		if seen[key] {
			return errors.New("task provider returned a duplicate task")
		}
		seen[key] = true
		if err := task.Validate(); err != nil {
			return fmt.Errorf("validate task result: %w", err)
		}
	}
	return validateTaskEncodedSize("task page", page, MaxTaskResultBytes)
}

func (service *TaskService) normalizeTask(listID string, task *Task) {
	if task.ListID == "" {
		task.ListID = listID
	}
	task.Capabilities = service.capabilities
	task.Degradations = mergeTaskDegradations(service.degradations, task.Degradations)
	task.Provenance = service.taskProvenance(task.ListID, task.ID)
}

func (service *TaskService) normalizeChangePage(input TaskSyncInput, page *TaskChangePage) error {
	if len(page.Changes) > input.Limit {
		return errors.New("task provider returned too many incremental changes")
	}
	if err := page.Cursor.Validate(); err != nil {
		return err
	}
	if page.Cursor.Account != input.Account || page.Cursor.Provider != service.provenance.Provider ||
		page.Cursor.ListID != input.ListID {
		return errors.New("task provider returned a cursor for another route")
	}
	if !slices.Contains(service.capabilities.SyncModes, page.Cursor.Mode) {
		return errors.New("task provider returned an unadvertised cursor mode")
	}
	if input.Cursor != nil && page.Cursor.Mode != input.Cursor.Mode {
		return errors.New("task provider changed cursor mode within one sync stream")
	}
	seen := make(map[string]bool, len(page.Changes))
	for index := range page.Changes {
		change := &page.Changes[index]
		if change.Task != nil {
			service.normalizeTask(input.ListID, change.Task)
			if change.Task.ListID != input.ListID {
				return errors.New("task change escaped its selected list")
			}
		}
		if err := change.Validate(); err != nil {
			return err
		}
		key := change.TaskID
		if change.Task != nil {
			key = change.Task.ID
		}
		if seen[key] {
			return errors.New("task provider returned duplicate incremental changes")
		}
		seen[key] = true
	}
	return validateTaskEncodedSize("task change page", page, MaxTaskResultBytes)
}

func (service *TaskService) taskProvenance(listID, objectID string) domain.Provenance {
	return domain.Provenance{
		AccountID:      service.provenance.AccountID,
		Provider:       service.provenance.Provider,
		TaskListID:     listID,
		SourceObjectID: objectID,
	}
}

func taskListTarget(listID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetTaskList, ID: listID}
}

func taskTarget(listID, taskID string) domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetTask, ID: fmt.Sprintf("%d:%s%s", len(listID), listID, taskID)}
}

func mergeTaskDegradations(route, item []domain.Degradation) []domain.Degradation {
	merged := make([]domain.Degradation, 0, len(route)+len(item))
	seen := make(map[domain.Degradation]struct{}, len(route)+len(item))
	for _, degradation := range append(append([]domain.Degradation(nil), route...), item...) {
		if _, exists := seen[degradation]; !exists {
			merged = append(merged, degradation)
			seen[degradation] = struct{}{}
		}
	}
	return merged
}

func taskEncodedSize(value any) (int, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("encode task result: %w", err)
	}
	return len(encoded), nil
}

func validateTaskEncodedSize(name string, value any, maximum int) error {
	size, err := taskEncodedSize(value)
	if err != nil {
		return err
	}
	if size > maximum {
		return fmt.Errorf("%s exceeds %d encoded bytes", name, maximum)
	}
	return nil
}
