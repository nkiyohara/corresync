package ticktick

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const (
	testAccount domain.AccountID = "acc_00000000000000000000000000000114"
	testProject string           = "project-1"
	testTask    string           = "task-1"
)

type tickTickFixture struct {
	t          *testing.T
	mu         sync.Mutex
	project    project
	tasks      map[string]task
	mutations  int
	createCode int
	filter     func() []task
}

func newTickTickFixture(t *testing.T) (*tickTickFixture, *httptest.Server) {
	t.Helper()
	fixture := &tickTickFixture{
		t: t,
		project: project{
			ID: testProject, Name: "Release", Permission: "write", Kind: "TASK",
		},
		tasks: map[string]task{
			testTask: {
				ID: testTask, ProjectID: testProject, Title: "Ship adapter",
				Content: "Provider content", Description: "Checklist notes",
				StartDate: "2026-08-17T09:00:00+0100",
				DueDate:   "2026-08-18T09:00:00+0100",
				TimeZone:  "Europe/London", Priority: 5, Status: 0,
				SortOrder: 42, Kind: "CHECKLIST", ParentID: "parent-1",
				ETag: "etag-1", Tags: []string{"release"},
				RepeatFlag: "RRULE:FREQ=DAILY;INTERVAL=1",
				Reminders:  []string{"TRIGGER:PT0S"},
				Items: []checklistItem{{
					ID: "item-1", Title: "Run tests", Status: 1, SortOrder: 7,
				}},
				AssigneeUsername: "member@example.test",
				FocusSummaries:   []json.RawMessage{json.RawMessage(`{"pomoCount":1}`)},
			},
		},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	return fixture, server
}

func (fixture *tickTickFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/open/v1/preference":
		writeTickTickJSON(fixture.t, writer, map[string]any{"timeZone": "Europe/London"})
	case request.Method == http.MethodGet && request.URL.Path == "/api/open/v1/project":
		if request.URL.Query().Get("offset") == "" || request.URL.Query().Get("limit") != "200" {
			fixture.t.Errorf("project query = %q", request.URL.RawQuery)
		}
		writeTickTickJSON(fixture.t, writer, []project{fixture.project})
	case request.Method == http.MethodPost &&
		(request.URL.Path == "/api/open/v1/task/filter" || request.URL.Path == "/api/open/v1/task/search"):
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			fixture.t.Error(err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		if request.URL.Path == "/api/open/v1/task/search" && len(body["keywords"]) == 0 {
			fixture.t.Error("search omitted keywords")
		}
		writeTickTickJSON(fixture.t, writer, fixture.filteredTasks())
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/open/v1/project/"):
		projectID, taskID, ok := parseTaskPath(request.URL.Path)
		if !ok {
			http.NotFound(writer, request)
			return
		}
		remote, exists := fixture.tasks[taskID]
		if !exists || remote.ProjectID != projectID {
			http.NotFound(writer, request)
			return
		}
		writeTickTickJSON(fixture.t, writer, remote)
	case request.Method == http.MethodPost && request.URL.Path == "/api/open/v1/task":
		fixture.mutations++
		if fixture.createCode == http.StatusCreated {
			writer.WriteHeader(http.StatusCreated)
			return
		}
		var created task
		if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
			fixture.t.Error(err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		created.ID = "created-1"
		created.ETag = "created-v1"
		created.Status = 0
		if len(created.Items) != 0 {
			created.Kind = "CHECKLIST"
			for index := range created.Items {
				created.Items[index].ID = "created-item-" + string(rune('1'+index))
			}
		} else {
			created.Kind = "TEXT"
		}
		fixture.tasks[created.ID] = created
		writeTickTickJSON(fixture.t, writer, created)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/open/v1/task/") &&
		request.URL.Path != "/api/open/v1/task/assign" &&
		request.URL.Path != "/api/open/v1/task/unassign":
		fixture.mutations++
		taskID := strings.TrimPrefix(request.URL.Path, "/api/open/v1/task/")
		remote, exists := fixture.tasks[taskID]
		if !exists {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&remote); err != nil {
			fixture.t.Error(err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		remote.ETag = "updated-v3"
		fixture.tasks[taskID] = remote
		writeTickTickJSON(fixture.t, writer, remote)
	case request.Method == http.MethodPost &&
		(request.URL.Path == "/api/open/v1/task/assign" || request.URL.Path == "/api/open/v1/task/unassign"):
		fixture.mutations++
		var body struct {
			ProjectID        string `json:"projectId"`
			TaskID           string `json:"taskId"`
			AssigneeUsername string `json:"assigneeUsername"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			fixture.t.Error(err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		remote := fixture.tasks[body.TaskID]
		remote.AssigneeUsername = body.AssigneeUsername
		if body.AssigneeUsername == "" {
			remote.ETag = "unassigned-v4"
		} else {
			remote.ETag = "assigned-v2"
		}
		fixture.tasks[body.TaskID] = remote
		writeTickTickJSON(fixture.t, writer, remote)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/complete"):
		fixture.mutations++
		projectID, taskID, ok := parseTaskPath(strings.TrimSuffix(request.URL.Path, "/complete"))
		if !ok {
			http.NotFound(writer, request)
			return
		}
		remote := fixture.tasks[taskID]
		if remote.ProjectID != projectID {
			http.NotFound(writer, request)
			return
		}
		remote.Status = 2
		remote.CompletedTime = "2026-08-14T12:00:00+0000"
		remote.ETag = "completed-v4"
		fixture.tasks[taskID] = remote
		writer.WriteHeader(http.StatusOK)
	case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/api/open/v1/project/"):
		fixture.mutations++
		_, taskID, ok := parseTaskPath(request.URL.Path)
		if !ok {
			http.NotFound(writer, request)
			return
		}
		delete(fixture.tasks, taskID)
		writer.WriteHeader(http.StatusOK)
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *tickTickFixture) filteredTasks() []task {
	if fixture.filter != nil {
		return fixture.filter()
	}
	result := make([]task, 0, len(fixture.tasks))
	for _, remote := range fixture.tasks {
		result = append(result, remote)
	}
	slices.SortFunc(result, func(left, right task) int { return strings.Compare(left.ID, right.ID) })
	return result
}

func parseTaskPath(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/open/v1/project/"), "/")
	if len(parts) != 3 || parts[1] != "task" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func openTickTickClient(t *testing.T, server *httptest.Server, readOnly bool) *Client {
	t.Helper()
	client, err := New(t.Context(), Options{
		APIBase: server.URL + "/api", Account: testAccount,
		ReadOnly: readOnly, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestTickTickMapsDocumentedTaskContract(t *testing.T) {
	t.Parallel()
	_, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)

	capabilities := client.TaskCapabilities()
	if !capabilities.Read || !capabilities.Search || !capabilities.CrossListRead ||
		!capabilities.Create || !capabilities.Checklist || !capabilities.Assignments ||
		capabilities.Reopen || capabilities.Reminders || capabilities.OptimisticConcurrency ||
		!slices.Equal(capabilities.SyncModes, []application.TaskSyncMode{application.TaskSyncPolling}) {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{
		Account: testAccount, Limit: 10,
	})
	if err != nil || len(lists.Lists) != 2 || !lists.Lists[0].Default ||
		lists.Lists[1].DisplayName != "Release" || !lists.Lists[1].Editable {
		t.Fatalf("ListTaskLists() = %+v, %v", lists, err)
	}
	listID := lists.Lists[1].ID
	page, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("ListTasks() = %+v, %v", page, err)
	}
	got := page.Tasks[0]
	if got.Title != "Ship adapter" || got.Notes != "Checklist notes" ||
		got.Priority != application.TaskPriorityHigh || got.Start == nil ||
		got.Start.Kind != application.TaskTemporalZoned || got.Start.TimeZone != "Europe/London" ||
		got.Due == nil || got.Due.Value != "2026-08-18T09:00:00+01:00" ||
		got.Recurrence == nil || len(got.Checklist) != 1 || !got.Checklist[0].Completed ||
		len(got.Assignees) != 1 || got.Order != "42" {
		t.Fatalf("mapped task = %+v", got)
	}
	for _, feature := range []string{"tasks.notes", "tasks.reminders", "tasks.focus"} {
		if !hasDegradation(got, feature) {
			t.Fatalf("task degradations = %+v, missing %s", got.Degradations, feature)
		}
	}
	search, err := client.SearchTasks(t.Context(), application.TaskSearchInput{
		Account: testAccount, Query: "adapter", Limit: 10,
	})
	if err != nil || len(search.Tasks) != 1 || search.Tasks[0].ListID != listID {
		t.Fatalf("SearchTasks() = %+v, %v", search, err)
	}
}

func TestTickTickCreateUpdateCompleteAndDelete(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	assigneeID, _ := encodeID("tta1_", "member@example.test")
	created, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Release RC",
		Notes: "Synthetic checklist", Priority: application.TaskPriorityNormal,
		Start:     &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: "2026-08-20"},
		Due:       &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: "2026-08-21"},
		Checklist: []application.TaskChecklistItemInput{{Title: "Publish", Completed: false}},
		Assignees: []application.TaskAssignee{{ID: assigneeID}}, Labels: []string{"release"},
	})
	if err != nil || created.Title != "Release RC" || created.Start == nil ||
		created.Start.Kind != application.TaskTemporalDate || len(created.Checklist) != 1 ||
		len(created.Assignees) != 1 {
		t.Fatalf("CreateTask() = %+v, %v", created, err)
	}
	updatedTitle := "Release candidate"
	updated, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: testAccount, ListID: listID, TaskID: created.ID,
		Version: created.Version, Title: &updatedTitle,
		ReplaceLabels: true, Labels: []string{"rc"}, ReplaceAssignees: true,
	})
	if err != nil || updated.Title != updatedTitle || len(updated.Labels) != 1 ||
		len(updated.Assignees) != 0 || updated.Version == created.Version {
		t.Fatalf("UpdateTask() = %+v, %v", updated, err)
	}
	completed, err := client.CompleteTask(t.Context(), application.TaskStateInput{
		Account: testAccount, ListID: listID, TaskID: updated.ID, Version: updated.Version,
	})
	if err != nil || completed.Status != application.TaskStatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("CompleteTask() = %+v, %v", completed, err)
	}
	if err := client.DeleteTask(t.Context(), application.TaskDeleteInput{
		Account: testAccount, ListID: listID, TaskID: completed.ID, Version: completed.Version,
	}); err != nil {
		t.Fatalf("DeleteTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.mutations != 6 {
		t.Fatalf("mutation calls = %d, want 6", fixture.mutations)
	}
}

func TestTickTickRevalidatesVersionAndProjectPermission(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	taskID, _ := encodeID("ttt1_", testTask)
	title := "Changed"
	_, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
		Version: "ttd1_stale", Title: &title,
	})
	if !errors.Is(err, restapi.ErrPrecondition) {
		t.Fatalf("stale UpdateTask() error = %v", err)
	}
	fixture.mu.Lock()
	fixture.project.Permission = "read"
	fixture.mu.Unlock()
	current, err := client.GetTask(t.Context(), application.TaskGetInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
		Version: current.Version, Title: &title,
	})
	if err == nil || !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("read-only-project UpdateTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.mutations != 0 {
		t.Fatalf("unsafe mutation calls = %d", fixture.mutations)
	}
}

func TestTickTickPollingProducesUpsertsAndTombstones(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	first, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(first.Changes) != 1 || first.Changes[0].Kind != application.TaskChangeUpsert ||
		first.Cursor.Provider != domain.ProviderTickTick || first.Cursor.Mode != application.TaskSyncPolling {
		t.Fatalf("first SyncTasks() = %+v, %v", first, err)
	}
	fixture.mu.Lock()
	delete(fixture.tasks, testTask)
	fixture.mu.Unlock()
	second, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10, Cursor: &first.Cursor,
	})
	if err != nil || len(second.Changes) != 1 || second.Changes[0].Kind != application.TaskChangeDelete ||
		second.Changes[0].TaskID == "" || second.Changes[0].Version == "" {
		t.Fatalf("second SyncTasks() = %+v, %v", second, err)
	}
}

func TestTickTickPollingBindsCursorToAccountAndList(t *testing.T) {
	t.Parallel()
	_, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	first, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherListID, _ := encodeID("ttl1_", "other-project")
	_, err = client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: otherListID, Limit: 10, Cursor: &first.Cursor,
	})
	if err == nil || !strings.Contains(err.Error(), "different account or list") {
		t.Fatalf("cross-list SyncTasks() error = %v", err)
	}
}

func TestTickTickPollingConvertsVanishedUpsertToTombstone(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	fixture.mu.Lock()
	fixture.filter = func() []task {
		remote := fixture.tasks[testTask]
		delete(fixture.tasks, testTask)
		fixture.filter = nil
		return []task{remote}
	}
	fixture.mu.Unlock()
	first, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(first.Changes) != 1 ||
		first.Changes[0].Kind != application.TaskChangeDelete {
		t.Fatalf("raced SyncTasks() = %+v, %v", first, err)
	}
	second, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10, Cursor: &first.Cursor,
	})
	if err != nil || len(second.Changes) != 0 {
		t.Fatalf("settled SyncTasks() = %+v, %v", second, err)
	}
}

func TestTickTickFailsClosedAtProviderBoundsAndUnknownWriteOutcome(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	fixture.mu.Lock()
	fixture.filter = func() []task {
		result := make([]task, providerTaskCap)
		for index := range result {
			result[index] = fixture.tasks[testTask]
			result[index].ID = "task-" + strings.Repeat("x", index/10) + string(rune('0'+index%10))
			result[index].ETag = "etag"
		}
		return result
	}
	fixture.mu.Unlock()
	page, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccount, ListID: listID, Offset: 190, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 10 || !page.HasMore {
		t.Fatalf("bounded ListTasks() = %+v, %v", page, err)
	}
	_, err = client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccount, ListID: listID, Offset: 200, Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "unpageable 200-task limit") {
		t.Fatalf("unpageable ListTasks() error = %v", err)
	}
	_, err = client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "200-task limit") {
		t.Fatalf("bounded SyncTasks() error = %v", err)
	}
	fixture.mu.Lock()
	fixture.filter = nil
	fixture.createCode = http.StatusCreated
	fixture.mu.Unlock()
	_, err = client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Unknown outcome",
		Priority: application.TaskPriorityNone,
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CreateTask() error = %v", err)
	}
}

func TestTickTickRejectsUnsupportedFieldCombinationsBeforeWriting(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	_, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Mixed time",
		Priority: application.TaskPriorityNone,
		Start:    &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: "2026-08-20"},
		Due: &application.TaskTemporal{
			Kind: application.TaskTemporalZoned, Value: "2026-08-21T10:00:00+01:00",
			TimeZone: "Europe/London",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot mix") {
		t.Fatalf("mixed-time CreateTask() error = %v", err)
	}
	_, err = client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Reminder",
		Priority:  application.TaskPriorityNone,
		Reminders: []application.TaskReminder{{Kind: application.TaskReminderRelativeDue}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("reminder CreateTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.mutations != 0 {
		t.Fatalf("invalid inputs submitted %d mutations", fixture.mutations)
	}
}

func TestTickTickRejectsUndocumentedFieldRemovalBeforeWriting(t *testing.T) {
	t.Parallel()
	fixture, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, false)
	listID, _ := encodeID("ttl1_", testProject)
	taskID, _ := encodeID("ttt1_", testTask)
	current, err := client.GetTask(t.Context(), application.TaskGetInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		input application.TaskUpdateInput
		want  string
	}{
		{name: "start", input: application.TaskUpdateInput{ReplaceStart: true}, want: "clearing a task start date"},
		{name: "due", input: application.TaskUpdateInput{ReplaceDue: true}, want: "clearing a task due date"},
		{name: "recurrence", input: application.TaskUpdateInput{ReplaceRecurrence: true}, want: "clearing task recurrence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			input.Account = testAccount
			input.ListID = listID
			input.TaskID = taskID
			input.Version = current.Version
			if _, err := client.UpdateTask(t.Context(), input); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("UpdateTask() error = %v", err)
			}
		})
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.mutations != 0 {
		t.Fatalf("undocumented field removal submitted %d mutations", fixture.mutations)
	}
}

func TestTickTickReadOnlyCapabilitiesAndProbeValidation(t *testing.T) {
	t.Parallel()
	_, server := newTickTickFixture(t)
	defer server.Close()
	client := openTickTickClient(t, server, true)
	if client.TaskCapabilities().Create || !client.TaskCapabilities().Read {
		t.Fatalf("read-only capabilities = %+v", client.TaskCapabilities())
	}

	badServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writeTickTickJSON(t, writer, map[string]any{"timeZone": "not/a-zone"})
	}))
	defer badServer.Close()
	_, err := New(t.Context(), Options{
		APIBase: badServer.URL, Account: testAccount, HTTP: badServer.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown IANA") {
		t.Fatalf("invalid preference New() error = %v", err)
	}
}

func hasDegradation(value application.Task, feature string) bool {
	for _, degradation := range value.Degradations {
		if degradation.Feature == feature {
			return true
		}
	}
	return false
}

func writeTickTickJSON(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
