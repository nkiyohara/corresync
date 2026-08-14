package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxTaskProjectionPageSize    = 50
	MaxTaskProjectionResultBytes = 12 << 20
	maxTaskProjectionSourceBytes = 2 << 20
)

// TaskProjectionInput selects a bounded global page without merging provider
// storage or authorizing a cross-account write.
type TaskProjectionInput struct {
	Status TaskStatus `json:"status,omitempty"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
}

func (input TaskProjectionInput) Validate() error {
	if input.Status != "" {
		if err := input.Status.Validate(); err != nil {
			return err
		}
	}
	if input.Offset < 0 || input.Offset > MaxProjectionOffset {
		return fmt.Errorf("task projection offset must be between 0 and %d", MaxProjectionOffset)
	}
	if input.Limit < 1 || input.Limit > MaxTaskProjectionPageSize {
		return fmt.Errorf("task projection limit must be between 1 and %d", MaxTaskProjectionPageSize)
	}
	return nil
}

type ProjectedTask struct {
	AccountAlias string `json:"accountAlias"`
	Task         Task   `json:"task"`
}

type TaskProjectionPage struct {
	Tasks      []ProjectedTask           `json:"tasks"`
	Accounts   []ProjectionAccountStatus `json:"accounts"`
	Failures   []ProjectionFailure       `json:"failures"`
	Offset     int                       `json:"offset"`
	Limit      int                       `json:"limit"`
	NextOffset int                       `json:"nextOffset,omitempty"`
	HasMore    bool                      `json:"hasMore"`
	Complete   bool                      `json:"complete"`
}

type taskProjectionSource struct {
	status ProjectionAccountStatus
	tasks  []ProjectedTask
}

// ListAllTasks reads each account through its guarded TaskService and produces
// a deterministic projection. It never shares cursors, sessions, or writes.
func (service *ProjectionService) ListAllTasks(
	ctx context.Context,
	input TaskProjectionInput,
	caller domain.Caller,
) (TaskProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return TaskProjectionPage{}, err
	}
	if err := caller.Validate(); err != nil {
		return TaskProjectionPage{}, err
	}
	accounts, err := service.accounts(ctx)
	if err != nil {
		return TaskProjectionPage{}, err
	}
	taskAccounts := make([]ProjectionAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.TaskProvider != "" {
			taskAccounts = append(taskAccounts, account)
		}
	}
	if len(taskAccounts) == 0 {
		return TaskProjectionPage{}, errors.New("no configured account has a task route")
	}
	sources := make([]taskProjectionSource, len(taskAccounts))
	for index, account := range taskAccounts {
		if err := ctx.Err(); err != nil {
			return TaskProjectionPage{}, err
		}
		sources[index] = service.listProjectionAccountTasks(ctx, account, input, caller)
	}
	if err := ctx.Err(); err != nil {
		return TaskProjectionPage{}, err
	}

	statuses := make([]ProjectionAccountStatus, 0, len(sources))
	tasks := make([]ProjectedTask, 0, len(sources)*(input.Offset+input.Limit+1))
	sourceHasMore := false
	for _, source := range sources {
		statuses = append(statuses, source.status)
		if !source.status.Complete {
			continue
		}
		if !source.status.Exhausted {
			sourceHasMore = true
		}
		tasks = append(tasks, source.tasks...)
	}
	slices.SortStableFunc(tasks, compareProjectedTask)
	hasMore := sourceHasMore || len(tasks) > input.Offset+input.Limit
	pageTasks := projectionTaskWindow(tasks, input.Offset, input.Limit)
	failures := projectionFailures(statuses)
	page := TaskProjectionPage{
		Tasks: pageTasks, Accounts: statuses, Failures: failures,
		Offset: input.Offset, Limit: input.Limit,
		HasMore: hasMore, Complete: len(failures) == 0,
	}
	if hasMore {
		page.NextOffset = input.Offset + len(pageTasks)
	}
	if err := page.Validate(); err != nil {
		return TaskProjectionPage{}, fmt.Errorf("validate task projection: %w", err)
	}
	return page, nil
}

func (service *ProjectionService) listProjectionAccountTasks(
	ctx context.Context,
	account ProjectionAccount,
	input TaskProjectionInput,
	caller domain.Caller,
) taskProjectionSource {
	status := newProjectionStatus(account, projectionServiceTasks)
	if !account.Authenticated {
		return taskProjectionSource{status: projectionUnavailableStatus(account, projectionServiceTasks)}
	}
	target := input.Offset + input.Limit + 1
	tasks := make([]ProjectedTask, 0, target)
	seen := make(map[string]bool, target)
	sourceOffset := 0
	sourceBytes := 0
	for len(tasks) < target {
		limit := min(MaxTaskPageSize, target-len(tasks))
		page, err := service.reader.ListTasks(ctx, TaskReadInput{
			Account: account.Account, Status: input.Status, Offset: sourceOffset, Limit: limit,
		}, caller)
		if err != nil {
			status.FetchedItems = len(tasks)
			return taskProjectionSource{status: failProjectionStatus(
				status, "provider_error", "the account task read did not complete; inspect account status and retry",
			)}
		}
		if err := validateTaskProjectionSourcePage(page, account, input.Status, sourceOffset, limit); err != nil {
			status.FetchedItems = len(tasks)
			return taskProjectionSource{status: failProjectionStatus(
				status, "invalid_result", "the account returned an invalid bounded task page",
			)}
		}
		for _, task := range page.Tasks {
			key := task.ListID + "\x00" + task.ID
			if seen[key] {
				status.FetchedItems = len(tasks)
				return taskProjectionSource{status: failProjectionStatus(
					status, "invalid_result", "the account returned duplicate task identities across pages",
				)}
			}
			seen[key] = true
			size, err := taskEncodedSize(task)
			if err != nil || sourceBytes+size > maxTaskProjectionSourceBytes {
				status.FetchedItems = len(tasks)
				return taskProjectionSource{status: failProjectionStatus(
					status, "invalid_result", "the account exceeded the bounded task projection size",
				)}
			}
			sourceBytes += size
			tasks = append(tasks, ProjectedTask{AccountAlias: account.Alias, Task: task})
		}
		sourceOffset += len(page.Tasks)
		if !page.HasMore {
			status.Exhausted = true
			break
		}
		if len(page.Tasks) == 0 {
			status.FetchedItems = len(tasks)
			return taskProjectionSource{status: failProjectionStatus(
				status, "invalid_result", "the account returned an empty non-terminal task page",
			)}
		}
	}
	status.Complete = true
	status.FetchedItems = len(tasks)
	return taskProjectionSource{status: status, tasks: tasks}
}

func validateTaskProjectionSourcePage(
	page TaskPage,
	account ProjectionAccount,
	status TaskStatus,
	offset, limit int,
) error {
	if page.Offset != offset || page.Limit != limit || len(page.Tasks) > limit {
		return errors.New("task projection source page is unbounded")
	}
	for _, task := range page.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if task.Provenance.AccountID != account.Account ||
			task.Provenance.Provider != account.TaskProvider ||
			task.Provenance.TaskListID != task.ListID ||
			task.Provenance.SourceObjectID != task.ID {
			return errors.New("task projection source provenance is invalid")
		}
		if status != "" && task.Status != status {
			return errors.New("task projection source status is invalid")
		}
	}
	return nil
}

func compareProjectedTask(left, right ProjectedTask) int {
	if compared := compareTaskTemporal(left.Task.Due, right.Task.Due); compared != 0 {
		return compared
	}
	if compared := strings.Compare(left.AccountAlias, right.AccountAlias); compared != 0 {
		return compared
	}
	if compared := strings.Compare(left.Task.ListID, right.Task.ListID); compared != 0 {
		return compared
	}
	return strings.Compare(left.Task.ID, right.Task.ID)
}

func compareTaskTemporal(left, right *TaskTemporal) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if compared := strings.Compare(string(left.Kind), string(right.Kind)); compared != 0 {
		return compared
	}
	if compared := strings.Compare(left.Value, right.Value); compared != 0 {
		return compared
	}
	return strings.Compare(left.TimeZone, right.TimeZone)
}

func projectionTaskWindow(tasks []ProjectedTask, offset, limit int) []ProjectedTask {
	if offset >= len(tasks) {
		return []ProjectedTask{}
	}
	end := min(len(tasks), offset+limit)
	return append([]ProjectedTask(nil), tasks[offset:end]...)
}

func (page TaskProjectionPage) Validate() error {
	if err := (TaskProjectionInput{Offset: page.Offset, Limit: page.Limit}).Validate(); err != nil {
		return err
	}
	if len(page.Tasks) > page.Limit {
		return errors.New("task projection page is unbounded")
	}
	if err := validateProjectionEnvelope(page.Accounts, page.Failures, page.Complete); err != nil {
		return err
	}
	accounts := make(map[domain.AccountID]ProjectionAccountStatus, len(page.Accounts))
	for _, account := range page.Accounts {
		if account.Service != projectionServiceTasks {
			return errors.New("task projection contains a non-task account status")
		}
		if _, exists := accounts[account.Account]; exists {
			return errors.New("task projection contains a duplicate account identity")
		}
		accounts[account.Account] = account
	}
	seen := make(map[string]struct{}, len(page.Tasks))
	for _, projected := range page.Tasks {
		status, exists := accounts[projected.Task.Provenance.AccountID]
		if !exists || !status.Complete || status.Alias != projected.AccountAlias ||
			status.Provider != projected.Task.Provenance.Provider {
			return errors.New("projected task has inconsistent account provenance")
		}
		if err := validateTaskProjectionSourcePage(
			TaskPage{Tasks: []Task{projected.Task}, Offset: 0, Limit: 1},
			ProjectionAccount{Account: status.Account, Alias: status.Alias, TaskProvider: status.Provider},
			"", 0, 1,
		); err != nil {
			return err
		}
		identity := string(projected.Task.Provenance.AccountID) + "\x00" +
			projected.Task.ListID + "\x00" + projected.Task.ID
		if _, exists := seen[identity]; exists {
			return errors.New("task projection contains a duplicate task identity")
		}
		seen[identity] = struct{}{}
	}
	if !slices.IsSortedFunc(page.Tasks, compareProjectedTask) {
		return errors.New("projected tasks are not stably ordered")
	}
	if err := validateTaskEncodedSize("task projection", page, MaxTaskProjectionResultBytes); err != nil {
		return err
	}
	if page.HasMore {
		if page.NextOffset != page.Offset+len(page.Tasks) {
			return errors.New("task projection next offset is invalid")
		}
	} else if page.NextOffset != 0 {
		return errors.New("terminal task projection has a next offset")
	}
	return nil
}
