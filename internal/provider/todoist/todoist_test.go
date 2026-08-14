package todoist

import (
	"compress/zlib"
	"encoding/base64"
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
)

const (
	testAccount = "acc_00000000000000000000000000000001"
	testAddress = "person@example.com"
	testUserID  = "user123"
	testProject = "project123"
	testTaskID  = "task123"
)

type todoistFixture struct {
	t          *testing.T
	mu         sync.Mutex
	task       task
	reminders  []reminder
	syncCalls  int
	commandLog [][]syncCommand
	status     func([]syncCommand) map[string]any
	tempID     func(string) string
	assignable bool
}

func newFixture(t *testing.T) (*todoistFixture, *httptest.Server) {
	t.Helper()
	fixture := &todoistFixture{t: t, assignable: true}
	fixture.task = task{
		ID: testTaskID, UserID: testUserID, ProjectID: testProject,
		SectionID: "section123", Content: "Ship the adapter",
		Description: "Use the current API", Priority: 4,
		Due: &due{
			Date: "2026-08-17T09:00:00.000000Z", TimeZone: "Europe/London",
			Recurring: true, String: "every monday at 10:00", Language: "en",
		},
		Deadline:   &deadline{Date: "2026-08-20", Language: "en"},
		ChildOrder: 3, Labels: []string{"release"},
		ResponsibleUID: testUserID, AddedAt: "2026-08-14T10:00:00Z",
		UpdatedAt: "2026-08-14T11:00:00Z",
		Duration:  &duration{Amount: 30, Unit: "minute"}, NoteCount: 2,
	}
	fixture.reminders = []reminder{{
		ID: "reminder123", ItemID: testTaskID, Type: "relative",
		MinuteOffset: 30,
	}}
	server := httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	return fixture, server
}

func (fixture *todoistFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/sync":
		fixture.sync(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/projects":
		writeFixtureJSON(fixture.t, writer, page[project]{Results: []project{{
			ID: testProject, Name: "Inbox", Inbox: true, CanAssignTasks: fixture.assignable,
		}}})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/projects/"+testProject:
		writeFixtureJSON(fixture.t, writer, project{
			ID: testProject, Name: "Inbox", Inbox: true, CanAssignTasks: fixture.assignable,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/tasks/completed/by_completion_date":
		items := []task(nil)
		if fixture.task.CompletedAt != "" {
			items = append(items, fixture.task)
		}
		writeFixtureJSON(fixture.t, writer, map[string]any{
			"items": items, "next_cursor": nil,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/tasks":
		if projectID := request.URL.Query().Get("project_id"); projectID != "" && projectID != testProject {
			fixture.t.Errorf("project_id = %q", projectID)
		}
		ids := request.URL.Query().Get("ids")
		results := []task{fixture.task}
		if ids != "" {
			results = nil
			for _, id := range strings.Split(ids, ",") {
				switch id {
				case fixture.task.ID:
					results = append(results, fixture.task)
				case "task456":
					other := fixture.task
					other.ID = id
					other.Content = "Second selected task"
					results = append(results, other)
				}
			}
		}
		writeFixtureJSON(fixture.t, writer, page[task]{Results: results})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/tasks/"+fixture.task.ID:
		writeFixtureJSON(fixture.t, writer, fixture.task)
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/reminders":
		values := fixture.reminders
		if taskID := request.URL.Query().Get("task_id"); taskID != "" && taskID != fixture.task.ID {
			values = nil
		}
		writeFixtureJSON(fixture.t, writer, page[reminder]{Results: values})
	default:
		http.NotFound(writer, request)
	}
}

func (fixture *todoistFixture) sync(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		fixture.t.Errorf("sync Content-Type = %q", request.Header.Get("Content-Type"))
	}
	if err := request.ParseForm(); err != nil {
		fixture.t.Error(err)
		http.Error(writer, "bad form", http.StatusBadRequest)
		return
	}
	if commandsJSON := request.Form.Get("commands"); commandsJSON != "" {
		var commands []syncCommand
		if err := json.Unmarshal([]byte(commandsJSON), &commands); err != nil {
			fixture.t.Error(err)
			http.Error(writer, "bad commands", http.StatusBadRequest)
			return
		}
		fixture.commandLog = append(fixture.commandLog, commands)
		statuses := make(map[string]any, len(commands))
		if fixture.status != nil {
			statuses = fixture.status(commands)
		} else {
			for _, command := range commands {
				statuses[command.UUID] = "ok"
			}
		}
		mapping := map[string]string{}
		for _, command := range commands {
			if command.Type == "item_add" {
				stableID := testTaskID
				if fixture.tempID != nil {
					stableID = fixture.tempID(command.TempID)
				}
				mapping[command.TempID] = stableID
			}
			if command.Type == "item_complete" {
				completedAt, _ := command.Args["date_completed"].(string)
				fixture.task.Checked = true
				fixture.task.CompletedAt = completedAt
			}
		}
		writeFixtureJSON(fixture.t, writer, map[string]any{
			"sync_status": statuses, "temp_id_mapping": mapping,
		})
		return
	}
	resourceTypes := request.Form.Get("resource_types")
	if strings.Contains(resourceTypes, `"user"`) {
		writeFixtureJSON(fixture.t, writer, map[string]any{
			"user": map[string]any{"id": testUserID, "email": testAddress},
			"user_plan_limits": map[string]any{"current": planLimits{
				PlanName: "pro", Deadlines: true, Labels: true, Reminders: true,
			}},
		})
		return
	}
	fixture.syncCalls++
	writeFixtureJSON(fixture.t, writer, syncResponse{
		SyncToken: "sync-next", FullSync: request.Form.Get("sync_token") == "*",
		Items: []task{
			fixture.task,
			{ID: "task456", UserID: testUserID, ProjectID: testProject,
				Content: "Second selected task", Priority: 1,
				AddedAt: "2026-08-14T10:00:00Z", UpdatedAt: "2026-08-14T11:00:00Z"},
			{ID: "otherTask", ProjectID: "otherProject", Content: "isolated", Priority: 1,
				AddedAt: "2026-08-14T10:00:00Z", UpdatedAt: "2026-08-14T11:00:00Z"},
		},
	})
}

func writeFixtureJSON(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func openFixtureClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(t.Context(), Options{
		APIBase: server.URL + "/api/v1", Address: testAddress,
		HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestTodoistMapsObservedPlanAndTaskSemantics(t *testing.T) {
	t.Parallel()
	_, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)

	capabilities := client.TaskCapabilities()
	if !capabilities.Read || !capabilities.Create || !capabilities.Reminders ||
		!capabilities.Labels || !capabilities.Recurrence ||
		!slices.Equal(capabilities.SyncModes, []application.TaskSyncMode{application.TaskSyncToken}) ||
		capabilities.Search || capabilities.OptimisticConcurrency || capabilities.Checklist {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{
		Account: testAccount, Limit: 10,
	})
	if err != nil || len(lists.Lists) != 1 || !lists.Lists[0].Default {
		t.Fatalf("ListTaskLists() = %+v, %v", lists, err)
	}
	page, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccount, ListID: lists.Lists[0].ID, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("ListTasks() = %+v, %v", page, err)
	}
	got := page.Tasks[0]
	if got.Status != application.TaskStatusNeedsAction ||
		got.Priority != application.TaskPriorityUrgent ||
		got.Start == nil || got.Start.Kind != application.TaskTemporalZoned ||
		got.Start.TimeZone != "Europe/London" ||
		got.Due == nil || got.Due.Kind != application.TaskTemporalDate ||
		got.Due.Value != "2026-08-20" ||
		got.Recurrence == nil || got.Recurrence.Frequency != application.TaskRecurrenceProvider ||
		len(got.Reminders) != 1 || got.Reminders[0].Kind != application.TaskReminderRelativeStart ||
		got.Reminders[0].OffsetMinutes != -30 || len(got.Assignees) != 1 || !got.Assignees[0].Self {
		t.Fatalf("mapped task = %+v", got)
	}
	for _, feature := range []string{"tasks.sections", "tasks.duration", "tasks.comments"} {
		found := false
		for _, degradation := range got.Degradations {
			found = found || degradation.Feature == feature
		}
		if !found {
			t.Fatalf("task degradations = %+v, missing %s", got.Degradations, feature)
		}
	}
}

func TestTodoistCreateUsesIdempotentTempIDBatchWithoutLeakingIt(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	listID, _ := encodeID("tdl1_", testProject)
	assigneeID, _ := encodeID("tda1_", testUserID)
	created, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Ship the adapter",
		Priority: application.TaskPriorityUrgent,
		Start: &application.TaskTemporal{
			Kind:  application.TaskTemporalZoned,
			Value: "2026-08-17T10:00:00+01:00", TimeZone: "Europe/London",
		},
		Due:       &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: "2026-08-20"},
		Assignees: []application.TaskAssignee{{ID: assigneeID, Self: true}},
		Labels:    []string{"release"},
		Reminders: []application.TaskReminder{{
			Kind: application.TaskReminderRelativeStart, OffsetMinutes: -30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || strings.Contains(created.ID, "tmp") || created.ID != mustEncodeID(t, "tdt1_", testTaskID) {
		t.Fatalf("created task = %+v", created)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 1 || len(fixture.commandLog[0]) != 2 {
		t.Fatalf("commands = %+v", fixture.commandLog)
	}
	add, reminder := fixture.commandLog[0][0], fixture.commandLog[0][1]
	if add.Type != "item_add" || !validID(add.TempID) ||
		reminder.Type != "reminder_add" || reminder.Args["item_id"] != add.TempID ||
		add.UUID == reminder.UUID || add.Args["project_id"] != testProject ||
		add.Args["priority"] != float64(4) && add.Args["priority"] != 4 {
		t.Fatalf("commands = %+v", fixture.commandLog[0])
	}
}

func TestTodoistCreateNeverReusesTemporaryIDAsProviderIdentity(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	fixture.mu.Lock()
	fixture.tempID = func(value string) string { return value }
	fixture.mu.Unlock()
	listID, _ := encodeID("tdl1_", testProject)
	_, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Reject the temp ID",
		Priority: application.TaskPriorityNone,
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CreateTask() error = %v", err)
	}
}

func TestTodoistRelativeReminderRequiresScheduledTimeBeforeWriting(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	listID, _ := encodeID("tdl1_", testProject)
	_, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Missing anchor",
		Priority: application.TaskPriorityNone,
		Reminders: []application.TaskReminder{{
			Kind: application.TaskReminderRelativeStart, OffsetMinutes: -30,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "scheduled datetime") {
		t.Fatalf("CreateTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 0 {
		t.Fatalf("invalid reminder submitted commands = %+v", fixture.commandLog)
	}
}

func TestTodoistUpdateRejectsClearingStartWithoutRemovingRecurrence(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	_, err := client.updateCommands(t.Context(), application.TaskUpdateInput{
		ReplaceStart: true,
	}, fixture.task)
	if err == nil || !strings.Contains(err.Error(), "explicit recurrence removal") {
		t.Fatalf("updateCommands() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 0 {
		t.Fatalf("invalid recurring update submitted commands = %+v", fixture.commandLog)
	}
}

func TestTodoistUpdateEncodesAnEmptyLabelReplacementAsAnArray(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	commands, err := client.updateCommands(t.Context(), application.TaskUpdateInput{
		ReplaceLabels: true,
	}, fixture.task)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "item_update" {
		t.Fatalf("commands = %+v", commands)
	}
	labels, ok := commands[0].Args["labels"].([]string)
	if !ok || labels == nil || len(labels) != 0 {
		t.Fatalf("labels = %#v", commands[0].Args["labels"])
	}
}

func TestTodoistAssignmentChecksTheSelectedProjectBeforeWriting(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	fixture.mu.Lock()
	fixture.assignable = false
	fixture.mu.Unlock()
	listID, _ := encodeID("tdl1_", testProject)
	assigneeID, _ := encodeID("tda1_", testUserID)
	_, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Do not submit",
		Priority:  application.TaskPriorityNone,
		Assignees: []application.TaskAssignee{{ID: assigneeID, Self: true}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not permit") {
		t.Fatalf("CreateTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 0 {
		t.Fatalf("assignment rejection submitted commands = %+v", fixture.commandLog)
	}
}

func TestTodoistParentMustResolveInsideTheSelectedProject(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	listID, _ := encodeID("tdl1_", testProject)
	foreignParent, _ := encodeID("tdt1_", "foreignTask")
	_, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, Title: "Do not move lists",
		Priority: application.TaskPriorityNone, ParentID: foreignParent,
	})
	if err == nil || !strings.Contains(err.Error(), "validate Todoist parent") {
		t.Fatalf("CreateTask() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 0 {
		t.Fatalf("foreign parent submitted commands = %+v", fixture.commandLog)
	}
}

func TestTodoistRecurringCompletionReturnsAnArchivedCanonicalTask(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	listID, _ := encodeID("tdl1_", testProject)
	page, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("ListTasks() = %+v, %v", page, err)
	}
	completed, err := client.CompleteTask(t.Context(), application.TaskStateInput{
		Account: testAccount, ListID: listID,
		TaskID: page.Tasks[0].ID, Version: page.Tasks[0].Version,
	})
	if err != nil || completed.Status != application.TaskStatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("CompleteTask() = %+v, %v", completed, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 1 || len(fixture.commandLog[0]) != 1 ||
		fixture.commandLog[0][0].Type != "item_complete" {
		t.Fatalf("completion commands = %+v", fixture.commandLog)
	}
}

func TestTodoistSyncCursorKeepsProjectIsolationAndDrainsWithoutResync(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	listID, _ := encodeID("tdl1_", testProject)
	first, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 1 || first.Changes[0].Task == nil || first.Reset {
		t.Fatalf("first sync = %+v", first)
	}
	state, err := decodeCursor(first.Cursor.Value)
	if err != nil || !slices.Equal(state.Members, []string{testTaskID, "task456"}) || len(state.Pending) != 1 {
		t.Fatalf("cursor = %+v, %v", state, err)
	}
	second, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 1, Cursor: &first.Cursor,
	})
	if err != nil || len(second.Changes) != 1 {
		t.Fatalf("second sync = %+v, %v", second, err)
	}
	fixture.mu.Lock()
	calls := fixture.syncCalls
	fixture.mu.Unlock()
	if calls != 1 {
		t.Fatalf("sync calls = %d, pending page unexpectedly advanced remote sync", calls)
	}
}

func TestTodoistMixedCommandResultIsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	fixture.mu.Lock()
	fixture.status = func(commands []syncCommand) map[string]any {
		return map[string]any{
			commands[0].UUID: "ok",
			commands[1].UUID: map[string]any{"error_code": 15},
		}
	}
	fixture.mu.Unlock()
	_, err := client.runCommands(t.Context(), []syncCommand{
		newCommand("item_update", "", map[string]any{"id": testTaskID}),
		newCommand("item_move", "", map[string]any{"id": testTaskID, "project_id": testProject}),
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("runCommands() error = %v", err)
	}
}

func TestTodoistMalformedCommandResultIsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	fixture.mu.Lock()
	fixture.status = func(commands []syncCommand) map[string]any {
		return map[string]any{commands[0].UUID: map[string]any{}}
	}
	fixture.mu.Unlock()
	_, err := client.runCommands(t.Context(), []syncCommand{
		newCommand("item_update", "", map[string]any{"id": testTaskID}),
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("runCommands() error = %v", err)
	}
}

func TestTodoistTransientCommandResultIsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway} {
		status, err := json.Marshal(map[string]any{
			"error_code": 1, "error_tag": "TRANSIENT", "http_code": code,
		})
		if err != nil {
			t.Fatal(err)
		}
		if ok, rejected := classifyCommandStatus(status); ok || rejected {
			t.Fatalf("classifyCommandStatus(http_code=%d) = %t, %t", code, ok, rejected)
		}
	}
}

func TestTodoistCommandBatchHonorsTheProviderLimitBeforeWriting(t *testing.T) {
	t.Parallel()
	fixture, server := newFixture(t)
	defer server.Close()
	client := openFixtureClient(t, server)
	commands := make([]syncCommand, maximumCommandsPerBatch+1)
	for index := range commands {
		commands[index] = newCommand("item_update", "", map[string]any{"id": testTaskID})
	}
	if _, err := client.runCommands(t.Context(), commands); err == nil ||
		!strings.Contains(err.Error(), "batch is empty or too large") {
		t.Fatalf("runCommands() error = %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.commandLog) != 0 {
		t.Fatalf("oversized batch reached Todoist = %+v", fixture.commandLog)
	}
}

func TestTodoistCursorRejectsOversizedExpansion(t *testing.T) {
	t.Parallel()
	var compressed strings.Builder
	compressed.WriteString("tdc1_")
	compressed.WriteString(base64OfZlib(t, strings.Repeat("x", maximumCursorJSONBytes+1)))
	if _, err := decodeCursor(compressed.String()); err == nil {
		t.Fatal("decodeCursor accepted oversized expanded state")
	}
}

func base64OfZlib(t *testing.T, value string) string {
	t.Helper()
	var body strings.Builder
	writer := zlibWriter(t, &body)
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(body.String()))
}

func zlibWriter(t *testing.T, writer io.Writer) io.WriteCloser {
	t.Helper()
	result, err := zlib.NewWriterLevel(writer, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustEncodeID(t *testing.T, prefix, value string) string {
	t.Helper()
	encoded, err := encodeID(prefix, value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
