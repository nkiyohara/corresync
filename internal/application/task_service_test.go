package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const testTaskAccount = domain.AccountID("acc_00000000000000000000000000000001")

type fakeTaskPort struct {
	lists                                                             TaskListPage
	page                                                              TaskPage
	task                                                              Task
	changes                                                           TaskChangePage
	err                                                               error
	createInput                                                       TaskCreateInput
	updateInput                                                       TaskUpdateInput
	stateInput                                                        TaskStateInput
	deleteInput                                                       TaskDeleteInput
	listCalls, readCalls, getCalls, searchCalls, syncCalls            int
	createCalls, updateCalls, completeCalls, reopenCalls, deleteCalls int
}

func (port *fakeTaskPort) ListTaskLists(context.Context, TaskListInput) (TaskListPage, error) {
	port.listCalls++
	return port.lists, port.err
}

func (port *fakeTaskPort) ListTasks(context.Context, TaskReadInput) (TaskPage, error) {
	port.readCalls++
	return port.page, port.err
}

func (port *fakeTaskPort) GetTask(context.Context, TaskGetInput) (Task, error) {
	port.getCalls++
	return port.task, port.err
}

func (port *fakeTaskPort) SearchTasks(context.Context, TaskSearchInput) (TaskPage, error) {
	port.searchCalls++
	return port.page, port.err
}

func (port *fakeTaskPort) SyncTasks(context.Context, TaskSyncInput) (TaskChangePage, error) {
	port.syncCalls++
	return port.changes, port.err
}

func (port *fakeTaskPort) CreateTask(_ context.Context, input TaskCreateInput) (Task, error) {
	port.createCalls++
	port.createInput = input
	task := port.task
	if task.ID == "" {
		task = validTask("task-created")
	}
	return task, port.err
}

func (port *fakeTaskPort) UpdateTask(_ context.Context, input TaskUpdateInput) (Task, error) {
	port.updateCalls++
	port.updateInput = input
	task := port.task
	if task.ID == "" {
		task = validTask(input.TaskID)
	}
	return task, port.err
}

func (port *fakeTaskPort) CompleteTask(_ context.Context, input TaskStateInput) (Task, error) {
	port.completeCalls++
	port.stateInput = input
	task := validTask(input.TaskID)
	task.Status = TaskStatusCompleted
	task.CompletedAt = zonedTaskTime("2026-08-13T12:00:00Z")
	return task, port.err
}

func (port *fakeTaskPort) ReopenTask(_ context.Context, input TaskStateInput) (Task, error) {
	port.reopenCalls++
	port.stateInput = input
	return validTask(input.TaskID), port.err
}

func (port *fakeTaskPort) DeleteTask(_ context.Context, input TaskDeleteInput) error {
	port.deleteCalls++
	port.deleteInput = input
	return port.err
}

func testTaskCapabilities() TaskCapabilities {
	return TaskCapabilities{
		Read: true, CrossListRead: true, Search: true, Create: true, Update: true, Complete: true, Reopen: true, Delete: true,
		OptimisticConcurrency: true, Reminders: true, Recurrence: true, Subtasks: true, Checklist: true,
		Assignments: true, Labels: true, Attachments: true, LinkedSources: true, Ordering: true,
		DateOnly: true, FloatingDateTime: true, ZonedDateTime: true,
		SyncModes: []TaskSyncMode{TaskSyncDelta},
	}
}

func testTaskService(t *testing.T, port TaskPort) (*TaskService, *memoryAudit) {
	t.Helper()
	guard, audit := newTestGuard(t, policy.DefaultRules())
	service, err := NewTaskService(guard, port, TaskOptions{
		Provenance:   domain.Provenance{AccountID: testTaskAccount, Provider: domain.ProviderTodoist},
		Capabilities: testTaskCapabilities(),
		Degradations: []domain.Degradation{{
			Feature: "synthetic_route", Reason: "Synthetic fixture route used for contract tests.",
		}},
	})
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	return service, audit
}

func validTask(id string) Task {
	return Task{
		ID: id, Version: "version-1", ListID: "list-1", Title: "Synthetic task",
		Status: TaskStatusNeedsAction, Priority: TaskPriorityNone,
	}
}

func zonedTaskTime(value string) *TaskTemporal {
	return &TaskTemporal{Kind: TaskTemporalZoned, Value: value, TimeZone: "UTC"}
}

func validTaskCreateInput() TaskCreateInput {
	return TaskCreateInput{
		Account: testTaskAccount, ListID: "list-1", Title: "Synthetic task", Notes: "Fixture notes",
		Priority: TaskPriorityNormal, Due: zonedTaskTime("2026-08-14T09:00:00Z"),
		Labels: []string{"fixture"},
	}
}

func validTaskStateInput() TaskStateInput {
	return TaskStateInput{Account: testTaskAccount, ListID: "list-1", TaskID: "task-1", Version: "version-1"}
}

func TestTaskReadsNormalizeRoutesAndPreserveDegradations(t *testing.T) {
	t.Parallel()

	item := validTask("task-1")
	item.Degradations = []domain.Degradation{{
		Feature: "completion_time", Reason: "The synthetic provider omitted a completion timestamp.", Lossy: true,
	}}
	port := &fakeTaskPort{
		lists: TaskListPage{Lists: []TaskList{{ID: "list-1", DisplayName: "Synthetic"}}, Limit: 25},
		page:  TaskPage{Tasks: []Task{item}, Limit: 25},
		task:  item,
	}
	service, audit := testTaskService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}

	lists, err := service.ListLists(t.Context(), TaskListInput{Account: testTaskAccount, Limit: 25}, caller)
	if err != nil {
		t.Fatalf("ListLists() error = %v", err)
	}
	page, err := service.List(t.Context(), TaskReadInput{Account: testTaskAccount, ListID: "list-1", Limit: 25}, caller)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	got, err := service.Get(t.Context(), TaskGetInput{Account: testTaskAccount, ListID: "list-1", TaskID: "task-1"}, caller)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(lists.Lists) != 1 || len(page.Tasks) != 1 || got.ID != "task-1" ||
		got.Provenance.AccountID != testTaskAccount || got.Provenance.Provider != domain.ProviderTodoist ||
		got.Provenance.TaskListID != "list-1" || got.Provenance.SourceObjectID != "task-1" ||
		len(got.Degradations) != 2 {
		t.Fatalf("unexpected normalized results: lists=%+v page=%+v got=%+v", lists, page, got)
	}
	if port.listCalls != 1 || port.readCalls != 1 || port.getCalls != 1 || len(audit.events) != 6 {
		t.Fatalf("unexpected calls or audit count: port=%+v audit=%+v", port, audit.events)
	}
}

func TestTaskDegradationMergeDoesNotHideLossiness(t *testing.T) {
	t.Parallel()
	reason := "The provider returned different fidelity for this item."
	merged := mergeTaskDegradations(
		[]domain.Degradation{{Feature: "due", Reason: reason}},
		[]domain.Degradation{{Feature: "due", Reason: reason, Lossy: true}},
	)
	if len(merged) != 2 || merged[0].Lossy || !merged[1].Lossy {
		t.Fatalf("merged degradations = %+v", merged)
	}
}

func TestTaskSyncBindsCursorToExactRouteAndAdvertisedMode(t *testing.T) {
	t.Parallel()

	port := &fakeTaskPort{changes: TaskChangePage{
		Changes: []TaskChange{{Kind: TaskChangeUpsert, Task: taskPointer(validTask("task-1"))}},
		Cursor:  TaskCursor{Provider: domain.ProviderTodoist, Account: testTaskAccount, ListID: "list-1", Mode: TaskSyncDelta, Value: "cursor-2"},
	}}
	service, _ := testTaskService(t, port)
	caller := domain.Caller{Surface: "daemon", Instance: "session-1"}
	input := TaskSyncInput{Account: testTaskAccount, ListID: "list-1", Limit: 25}
	page, err := service.Sync(t.Context(), input, caller)
	if err != nil || len(page.Changes) != 1 || page.Changes[0].Task.Provenance.AccountID != testTaskAccount {
		t.Fatalf("Sync() = %+v, %v", page, err)
	}

	port.changes.Cursor.Mode = TaskSyncPolling
	if _, err := service.Sync(t.Context(), input, caller); err == nil {
		t.Fatal("Sync() accepted an unadvertised cursor mode")
	}
	foreign := TaskCursor{Provider: domain.ProviderTodoist, Account: testTaskAccount, ListID: "list-2", Mode: TaskSyncDelta, Value: "cursor"}
	input.Cursor = &foreign
	if _, err := service.Sync(t.Context(), input, caller); err == nil {
		t.Fatal("Sync() accepted a cursor from another list")
	}
	foreign.ListID = "list-1"
	foreign.Mode = TaskSyncPolling
	before := port.syncCalls
	if _, err := service.Sync(t.Context(), input, caller); err == nil {
		t.Fatal("Sync() accepted an input cursor with an unadvertised mode")
	}
	if port.syncCalls != before {
		t.Fatal("Sync() contacted the provider for an unadvertised cursor mode")
	}
}

func TestTaskReadRejectsProviderRouteEscape(t *testing.T) {
	t.Parallel()

	foreign := validTask("task-1")
	foreign.ListID = "list-2"
	port := &fakeTaskPort{page: TaskPage{Tasks: []Task{foreign}, Limit: 25}}
	service, _ := testTaskService(t, port)
	_, err := service.List(t.Context(), TaskReadInput{Account: testTaskAccount, ListID: "list-1", Limit: 25},
		domain.Caller{Surface: "cli", Instance: "process-1"})
	if err == nil {
		t.Fatal("List() accepted a task from another list")
	}
}

func TestTaskReadRejectsOversizedEncodedPage(t *testing.T) {
	t.Parallel()

	tasks := make([]Task, 9)
	for index := range tasks {
		tasks[index] = validTask(fmt.Sprintf("task-%d", index))
		tasks[index].Notes = strings.Repeat("x", MaxTaskNotesBytes)
	}
	port := &fakeTaskPort{page: TaskPage{Tasks: tasks, Limit: len(tasks)}}
	service, _ := testTaskService(t, port)
	_, err := service.List(t.Context(), TaskReadInput{
		Account: testTaskAccount, ListID: "list-1", Limit: len(tasks),
	}, domain.Caller{Surface: "cli", Instance: "process-1"})
	if err == nil || !strings.Contains(err.Error(), "encoded bytes") {
		t.Fatalf("List() error = %v", err)
	}
}

func taskPointer(task Task) *Task { return &task }

func TestTaskCreatePreviewCommitsImmutablePayloadOnce(t *testing.T) {
	t.Parallel()

	port := &fakeTaskPort{}
	service, audit := testTaskService(t, port)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	input := validTaskCreateInput()
	access, err := service.Create(t.Context(), input, caller)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || port.createCalls != 0 ||
		access.Preview.Operation.Effect != domain.EffectExternalWrite || access.Preview.Operation.Target == nil ||
		access.Preview.Operation.Target.Kind != domain.TargetTaskList || access.Review.Notes == nil ||
		len(access.Review.Notes.SHA256) != 64 || access.Review.Account != testTaskAccount ||
		access.Review.Provider != domain.ProviderTodoist || !access.Review.Capabilities.OptimisticConcurrency ||
		len(access.Review.Degradations) != 1 {
		t.Fatalf("unsafe create preview: %+v", access)
	}
	input.Title = "Mutated after preview"
	input.Labels[0] = "mutated"
	input.Due.Value = "2027-01-01T00:00:00Z"
	committed, err := service.CommitCreate(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitCreate() error = %v", err)
	}
	if committed.Status != "created" || committed.Task == nil || port.createCalls != 1 ||
		port.createInput.Title != "Synthetic task" || port.createInput.Labels[0] != "fixture" ||
		port.createInput.Due.Value != "2026-08-14T09:00:00Z" || *access.Review.Title != "Synthetic task" ||
		access.Review.Labels[0] != "fixture" || access.Review.Due.Value != "2026-08-14T09:00:00Z" {
		t.Fatalf("mutable create commit: committed=%+v input=%+v review=%+v", committed, port.createInput, access.Review)
	}
	if _, err := service.CommitCreate(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitCreate() replay unexpectedly succeeded")
	}
	if len(audit.events) != 3 || audit.events[2].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", audit.events)
	}
}

func TestTaskUpdatePreviewCannotAuthorizeDelete(t *testing.T) {
	t.Parallel()

	port := &fakeTaskPort{}
	service, _ := testTaskService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	title := "Updated synthetic task"
	input := TaskUpdateInput{
		Account: testTaskAccount, ListID: "list-1", TaskID: "task-1", Version: "version-1", Title: &title,
	}
	access, err := service.Update(t.Context(), input, caller)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if access.Preview.Operation.Target == nil || access.Preview.Operation.Target.Kind != domain.TargetTask {
		t.Fatalf("update target = %+v", access.Preview.Operation.Target)
	}
	if _, err := service.CommitDelete(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitDelete() accepted an update preview")
	}
	committed, err := service.CommitUpdate(t.Context(), access.Preview.Token, caller)
	if err != nil || committed.Task == nil || port.updateCalls != 1 || port.deleteCalls != 0 {
		t.Fatalf("CommitUpdate() = %+v, %v calls=%d/%d", committed, err, port.updateCalls, port.deleteCalls)
	}
}

func TestTaskStateAndDeleteWritesAlwaysPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    func(*TaskService, context.Context, TaskStateInput, domain.Caller) (TaskWriteAccess, error)
		commit     func(*TaskService, context.Context, string, domain.Caller) (TaskWriteAccess, error)
		wantStatus string
		wantEffect domain.Effect
	}{
		{"complete", (*TaskService).Complete, (*TaskService).CommitComplete, "completed", domain.EffectExternalWrite},
		{"reopen", (*TaskService).Reopen, (*TaskService).CommitReopen, "reopened", domain.EffectExternalWrite},
		{"delete", (*TaskService).Delete, (*TaskService).CommitDelete, "deleted", domain.EffectDestructiveWrite},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			port := &fakeTaskPort{}
			service, _ := testTaskService(t, port)
			caller := domain.Caller{Surface: "cli", Instance: "process-1"}
			preview, err := test.prepare(service, t.Context(), validTaskStateInput(), caller)
			if err != nil || preview.Status != "approval_required" || preview.Preview == nil ||
				preview.Preview.Operation.Effect != test.wantEffect {
				t.Fatalf("prepare = %+v, %v", preview, err)
			}
			committed, err := test.commit(service, t.Context(), preview.Preview.Token, caller)
			if err != nil || committed.Status != test.wantStatus {
				t.Fatalf("commit = %+v, %v", committed, err)
			}
		})
	}
}

func TestTaskWritesRejectUnsupportedAndSelfReferentialFields(t *testing.T) {
	t.Parallel()

	port := &fakeTaskPort{}
	service, _ := testTaskService(t, port)
	service.capabilities.LinkedSources = false
	input := validTaskCreateInput()
	input.Sources = []TaskLinkedSource{{
		Kind: TaskSourceMail, Account: testTaskAccount, Provider: domain.ProviderJMAP, ObjectID: "message-1",
	}}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	if _, err := service.Create(t.Context(), input, caller); err == nil || !strings.Contains(err.Error(), "linked sources") {
		t.Fatalf("Create() unsupported source error = %v", err)
	}

	service.capabilities.LinkedSources = true
	update := TaskUpdateInput{
		Account: testTaskAccount, ListID: "list-1", TaskID: "task-1", Version: "version-1",
		ReplaceSources: true,
		Sources: []TaskLinkedSource{{
			Kind: TaskSourceTask, Account: testTaskAccount, Provider: domain.ProviderTodoist, ObjectID: "task-1",
		}},
	}
	if _, err := service.Update(t.Context(), update, caller); err == nil || !strings.Contains(err.Error(), "self-loop") {
		t.Fatalf("Update() self-loop error = %v", err)
	}
	if port.createCalls != 0 || port.updateCalls != 0 {
		t.Fatalf("provider calls = create %d update %d", port.createCalls, port.updateCalls)
	}
}

func TestTaskWriteInvalidProviderResultIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	port := &fakeTaskPort{task: validTask("different-task")}
	service, audit := testTaskService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	title := "Updated"
	preview, err := service.Update(t.Context(), TaskUpdateInput{
		Account: testTaskAccount, ListID: "list-1", TaskID: "task-1", Version: "version-1", Title: &title,
	}, caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitUpdate(t.Context(), preview.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitUpdate() error = %v", err)
	}
	last := audit.events[len(audit.events)-1]
	if last.Outcome != AuditOutcomeUnknown || last.Reason != "outcome_unknown" {
		t.Fatalf("audit event = %+v", last)
	}
}

func TestTaskCreateRejectsProviderResultFromAnotherListAsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	returned := validTask("task-created")
	returned.ListID = "list-other"
	port := &fakeTaskPort{task: returned}
	service, _ := testTaskService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	preview, err := service.Create(t.Context(), validTaskCreateInput(), caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitCreate(t.Context(), preview.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitCreate() error = %v", err)
	}
}

func TestTaskValidationBoundsUntrustedInputs(t *testing.T) {
	t.Parallel()

	valid := validTaskCreateInput()
	tests := []TaskCreateInput{
		{},
		func() TaskCreateInput { value := valid; value.ListID = ""; return value }(),
		func() TaskCreateInput { value := valid; value.Title = "bad\x00title"; return value }(),
		func() TaskCreateInput {
			value := valid
			value.Notes = strings.Repeat("x", MaxTaskNotesBytes+1)
			return value
		}(),
		func() TaskCreateInput { value := valid; value.Priority = "critical"; return value }(),
		func() TaskCreateInput {
			value := valid
			value.Due = nil
			value.Reminders = []TaskReminder{{Kind: TaskReminderRelativeDue, OffsetMinutes: -10}}
			return value
		}(),
		func() TaskCreateInput {
			value := valid
			value.Labels = []string{"duplicate", "DUPLICATE"}
			return value
		}(),
	}
	for _, input := range tests {
		if err := input.Validate(); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", input)
		}
	}

	update := TaskUpdateInput{Account: testTaskAccount, ListID: "list-1", TaskID: "task-1", Version: "version-1"}
	if err := update.Validate(); err == nil {
		t.Fatal("empty task update unexpectedly validated")
	}
	update.Reminders = []TaskReminder{{Kind: TaskReminderRelativeDue, OffsetMinutes: -5}}
	if err := update.Validate(); err == nil {
		t.Fatal("task update collection without replace flag unexpectedly validated")
	}

	valid.Checklist = []TaskChecklistItemInput{{Title: "Provider assigns the ID"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("create with provider-assigned checklist ID: %v", err)
	}
	valid.Checklist[0].ID = "existing-checklist-item"
	if err := valid.Validate(); err != nil {
		t.Fatalf("create with caller-supplied checklist ID: %v", err)
	}
}

func TestCompletedTaskRequiresTimestampOrExplicitDegradation(t *testing.T) {
	t.Parallel()

	task := validTask("task-1")
	task.Status = TaskStatusCompleted
	task.Capabilities = testTaskCapabilities()
	task.Provenance = domain.Provenance{
		AccountID: testTaskAccount, Provider: domain.ProviderTodoist, TaskListID: "list-1", SourceObjectID: "task-1",
	}
	if err := task.Validate(); err == nil {
		t.Fatal("completed task without timestamp unexpectedly validated")
	}
	task.Degradations = []domain.Degradation{{
		Feature: "completion_time", Reason: "The provider still claims an exact timestamp.",
	}}
	if err := task.Validate(); err == nil {
		t.Fatal("completed task accepted a non-lossy excuse for a missing timestamp")
	}
	task.Degradations = []domain.Degradation{{
		Feature: "completion_time", Reason: "The provider does not expose a completion timestamp.", Lossy: true,
	}}
	if err := task.Validate(); err != nil {
		t.Fatalf("completed task with explicit degradation: %v", err)
	}
}
