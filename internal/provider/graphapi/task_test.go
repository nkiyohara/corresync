package graphapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const graphTaskAccount domain.AccountID = "acc_00000000000000000000000000000071"

func TestMicrosoftTodoBindsEitherVerifiedGraphAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeGraphJSON(t, writer, map[string]string{
				"id": "user1", "mail": "alias@example.test",
				"userPrincipalName": "signin@example.test",
			})
		case "/me/todo/lists":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "signin@example.test",
		Tasks: true, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "other@example.test",
		Tasks: true, HTTP: server.Client(),
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched delegated identity error = %v", err)
	}
}

func TestMicrosoftTodoReadAndDeltaContracts(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	remote := graphTaskFixture(t, `W/"task-v1"`)
	deltaCalls := 0
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeGraphJSON(t, writer, map[string]string{"id": "user1", "mail": "reader@example.test"})
		case "/me/todo/lists":
			if request.URL.Query().Get("$top") == "1" {
				writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
				return
			}
			writeGraphJSON(t, writer, map[string]any{"value": []graphTaskList{{
				ID: "list1", DisplayName: "Synthetic tasks", IsOwner: true,
				WellKnownListName: "defaultList",
			}}})
		case "/me/todo/lists/list1/tasks":
			if request.URL.Query().Get("$filter") != "status eq 'notStarted'" ||
				request.URL.Query().Get("$expand") != "checklistItems,linkedResources" {
				t.Errorf("task list query = %q", request.URL.RawQuery)
			}
			writeGraphJSON(t, writer, graphTaskPage{Value: []graphTask{remote}})
		case "/me/todo/lists/list1/tasks/task1":
			writeGraphJSON(t, writer, remote)
		case "/me/todo/lists/list1/tasks/delta":
			deltaCalls++
			if request.URL.Query().Get("$deltatoken") == "next" {
				writeGraphJSON(t, writer, graphTaskPage{
					DeltaLink: server.URL + "/me/todo/lists/list1/tasks/delta?$deltatoken=done",
					Value:     []graphTask{{ID: "deleted1", Removed: json.RawMessage(`{"reason":"deleted"}`)}},
				})
				return
			}
			writeGraphJSON(t, writer, graphTaskPage{
				NextLink: server.URL + "/me/todo/lists/list1/tasks/delta?$deltatoken=next",
				Value:    []graphTask{remote},
			})
		default:
			http.Error(writer, "unexpected task fixture route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newMicrosoftTodoFixtureClient(t, server, true)
	defer func() { _ = client.Close() }()

	listID, err := encodeTaskListID("list1")
	if err != nil {
		t.Fatal(err)
	}
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{
		Account: graphTaskAccount, Limit: 10,
	})
	if err != nil || len(lists.Lists) != 1 || !lists.Lists[0].Default || !lists.Lists[0].Editable {
		t.Fatalf("ListTaskLists() = %+v, %v", lists, err)
	}
	tasks, err := client.ListTasks(t.Context(), application.TaskReadInput{
		Account: graphTaskAccount, ListID: listID,
		Status: application.TaskStatusNeedsAction, Limit: 10,
	})
	if err != nil || len(tasks.Tasks) != 1 || tasks.Tasks[0].Notes != "Synthetic notes" ||
		len(tasks.Tasks[0].Checklist) != 1 || len(tasks.Tasks[0].Sources) != 1 ||
		tasks.Tasks[0].Due == nil || tasks.Tasks[0].Due.TimeZone != "Europe/London" ||
		len(tasks.Tasks[0].Degradations) != 1 || tasks.Tasks[0].Degradations[0].Feature != "tasks.notes" ||
		!tasks.Tasks[0].Degradations[0].Lossy {
		t.Fatalf("ListTasks() = %+v, %v", tasks, err)
	}
	got, err := client.GetTask(t.Context(), application.TaskGetInput{
		Account: graphTaskAccount, ListID: listID, TaskID: tasks.Tasks[0].ID,
	})
	if err != nil || got.Version != encodeETag(`W/"task-v1"`) {
		t.Fatalf("GetTask() = %+v, %v", got, err)
	}
	first, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: graphTaskAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(first.Changes) != 1 || first.Cursor.Value == "" {
		t.Fatalf("SyncTasks(initial) = %+v, %v", first, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newMicrosoftTodoFixtureClient(t, server, true)
	defer func() { _ = restarted.Close() }()
	second, err := restarted.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: graphTaskAccount, ListID: listID, Cursor: &first.Cursor, Limit: 10,
	})
	if err != nil || len(second.Changes) != 1 ||
		second.Changes[0].Kind != application.TaskChangeDelete || deltaCalls != 2 {
		t.Fatalf("SyncTasks(next) = %+v, %v calls=%d", second, err, deltaCalls)
	}
	if _, _, err := client.taskContinuation(
		"https://attacker.invalid/v1.0/me/todo/lists/list1/tasks/delta?$deltatoken=x",
		"list1",
	); err == nil {
		t.Fatal("cross-origin delta cursor was accepted")
	}
	for _, suffix := range []string{
		"",
		"?$select=body",
		"?$deltatoken=x&$select=body",
		"?$deltatoken=x&$deltatoken=y",
		"?$deltatoken=x%0Ay",
	} {
		if _, _, err := restarted.taskContinuation(
			server.URL+"/me/todo/lists/list1/tasks/delta"+suffix,
			"list1",
		); err == nil {
			t.Errorf("non-delta cursor query %q was accepted", suffix)
		}
	}
}

func TestMicrosoftTodoCreateUpdateStateAndDeleteContracts(t *testing.T) {
	t.Parallel()
	currentETag := `W/"task-v1"`
	currentStatus := "notStarted"
	checklist := []graphChecklistItem{
		{ID: "old-check", DisplayName: "Old", Checked: false},
		{ID: "remove-check", DisplayName: "Remove", Checked: false},
	}
	sources := graphTaskFixture(t, currentETag).LinkedResources
	createdChecklist := false
	updatedChecklist := false
	deletedChecklist := false
	createdReplacementChecklist := false
	replacedSource := false
	deleted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /me":
			writeGraphJSON(t, writer, map[string]string{"id": "user1", "mail": "reader@example.test"})
		case "GET /me/todo/lists":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
		case "POST /me/todo/lists/list1/tasks":
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			body, _ := payload["body"].(map[string]any)
			if payload["title"] != "Created" || payload["linkedResources"] == nil || payload["dueDateTime"] == nil ||
				body["contentType"] != "text" || body["content"] != "  Created notes\n" {
				t.Errorf("create payload = %#v", payload)
			}
			created := graphTaskFixture(t, `W/"created-v1"`)
			created.ID = "created1"
			created.Title = "Created"
			writeGraphJSONStatus(t, writer, created, http.StatusCreated)
		case "POST /me/todo/lists/list1/tasks/created1/checklistItems":
			createdChecklist = true
			writeGraphJSONStatus(t, writer, graphChecklistItem{ID: "created-check"}, http.StatusCreated)
		case "GET /me/todo/lists/list1/tasks/created1":
			created := graphTaskFixture(t, `W/"created-v2"`)
			created.ID = "created1"
			created.Title = "Created"
			created.Body = graphItemBody{ContentType: "text", Content: "  Created notes\n"}
			created.ChecklistItems = []graphChecklistItem{{ID: "created-check", DisplayName: "Step", Checked: false}}
			writeGraphJSON(t, writer, created)
		case "GET /me/todo/lists/list1/tasks/task1":
			remote := graphTaskFixture(t, currentETag)
			remote.Status = currentStatus
			if currentStatus == "completed" {
				remote.Completed = &graphDateTimeZone{DateTime: "2026-08-14T12:00:00", TimeZone: "UTC"}
			}
			remote.ChecklistItems = checklist
			remote.LinkedResources = sources
			writeGraphJSON(t, writer, remote)
		case "PATCH /me/todo/lists/list1/tasks/task1":
			requireGraphCondition(t, request, currentETag)
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if status, ok := payload["status"].(string); ok {
				currentStatus = status
			}
			currentETag = nextSyntheticETag(currentETag)
			remote := graphTaskFixture(t, currentETag)
			remote.Status = currentStatus
			if currentStatus == "completed" {
				remote.Completed = &graphDateTimeZone{DateTime: "2026-08-14T12:00:00", TimeZone: "UTC"}
			}
			writeGraphJSON(t, writer, remote)
		case "PATCH /me/todo/lists/list1/tasks/task1/checklistItems/old-check":
			var payload graphChecklistItem
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			payload.ID = "old-check"
			checklist[0] = payload
			updatedChecklist = true
			writeGraphJSON(t, writer, payload)
		case "DELETE /me/todo/lists/list1/tasks/task1/checklistItems/remove-check":
			deletedChecklist = true
			checklist = append(checklist[:1], checklist[2:]...)
			writer.WriteHeader(http.StatusNoContent)
		case "POST /me/todo/lists/list1/tasks/task1/checklistItems":
			createdReplacementChecklist = true
			created := graphChecklistItem{ID: "new-check", DisplayName: "New", Checked: true}
			checklist = append(checklist, created)
			writeGraphJSONStatus(t, writer, created, http.StatusCreated)
		case "DELETE /me/todo/lists/list1/tasks/task1/linkedResources/link1":
			replacedSource = true
			sources = nil
			writer.WriteHeader(http.StatusNoContent)
		case "POST /me/todo/lists/list1/tasks/task1/linkedResources":
			var source graphLinkedResource
			if err := json.NewDecoder(request.Body).Decode(&source); err != nil {
				t.Fatal(err)
			}
			source.ID = "link2"
			sources = []graphLinkedResource{source}
			writeGraphJSONStatus(t, writer, source, http.StatusCreated)
		case "DELETE /me/todo/lists/list1/tasks/task1":
			requireGraphCondition(t, request, currentETag)
			deleted = true
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected task write fixture route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newMicrosoftTodoFixtureClient(t, server, true)
	defer func() { _ = client.Close() }()
	listID, _ := encodeTaskListID("list1")
	taskID, _ := encodeTaskID("task1")
	due := &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: "2026-08-15T09:00:00+01:00", TimeZone: "Europe/London",
	}
	source := syntheticTaskSource("mail-new")
	created, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		Account: graphTaskAccount, ListID: listID, Title: "Created",
		Notes: "  Created notes\n", Priority: application.TaskPriorityHigh, Due: due,
		Checklist: []application.TaskChecklistItemInput{{Title: "Step"}},
		Sources:   []application.TaskLinkedSource{source},
	})
	if err != nil || created.ID == "" || created.Notes != "  Created notes\n" || !createdChecklist {
		t.Fatalf("CreateTask() = %+v, %v checklist=%t", created, err, createdChecklist)
	}
	newTitle := "Updated"
	oldChecklistID, _ := encodeChecklistID("old-check")
	updated, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: graphTaskAccount, ListID: listID, TaskID: taskID,
		Version: encodeETag(`W/"task-v1"`), Title: &newTitle,
		ReplaceChecklist: true,
		Checklist: []application.TaskChecklistItemInput{
			{ID: oldChecklistID, Title: "Updated", Completed: true},
			{Title: "New", Completed: true},
		},
		ReplaceSources: true, Sources: []application.TaskLinkedSource{source},
	})
	if err != nil || updated.ID != taskID || len(updated.Checklist) != 2 ||
		!updatedChecklist || !deletedChecklist || !createdReplacementChecklist || !replacedSource {
		t.Fatalf(
			"UpdateTask() = %+v, %v checklist(update=%t delete=%t create=%t) source=%t",
			updated, err, updatedChecklist, deletedChecklist, createdReplacementChecklist, replacedSource,
		)
	}
	completed, err := client.CompleteTask(t.Context(), application.TaskStateInput{
		Account: graphTaskAccount, ListID: listID, TaskID: taskID, Version: updated.Version,
	})
	if err != nil || completed.Status != application.TaskStatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("CompleteTask() = %+v, %v", completed, err)
	}
	reopened, err := client.ReopenTask(t.Context(), application.TaskStateInput{
		Account: graphTaskAccount, ListID: listID, TaskID: taskID, Version: completed.Version,
	})
	if err != nil || reopened.Status != application.TaskStatusNeedsAction {
		t.Fatalf("ReopenTask() = %+v, %v", reopened, err)
	}
	if err := client.DeleteTask(t.Context(), application.TaskDeleteInput{
		Account: graphTaskAccount, ListID: listID, TaskID: taskID, Version: reopened.Version,
	}); err != nil || !deleted {
		t.Fatalf("DeleteTask() error = %v deleted=%t", err, deleted)
	}
}

func TestMicrosoftTodoReadOnlyCapabilitiesRejectWrites(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeGraphJSON(t, writer, map[string]string{"id": "user1", "mail": "reader@example.test"})
		case "/me/todo/lists":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
		default:
			t.Errorf("read-only client attempted %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := newMicrosoftTodoFixtureClient(t, server, false)
	defer func() { _ = client.Close() }()
	capabilities := client.TaskCapabilities()
	if !capabilities.Read || capabilities.Create || capabilities.Update || capabilities.Delete ||
		capabilities.OptimisticConcurrency {
		t.Fatalf("read-only capabilities = %+v", capabilities)
	}
	if _, err := client.CreateTask(t.Context(), application.TaskCreateInput{}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("CreateTask(read-only) error = %v", err)
	}
}

func TestMicrosoftTodoReplacementPreflightsBeforeCoreMutation(t *testing.T) {
	t.Parallel()
	mutated := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /me":
			writeGraphJSON(t, writer, map[string]string{"id": "user1", "mail": "reader@example.test"})
		case "GET /me/todo/lists":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
		case "GET /me/todo/lists/list1/tasks/task1":
			writeGraphJSON(t, writer, graphTaskFixture(t, `W/"task-v1"`))
		default:
			mutated = true
			http.Error(writer, "unexpected mutation", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := newMicrosoftTodoFixtureClient(t, server, true)
	defer func() { _ = client.Close() }()
	listID, _ := encodeTaskListID("list1")
	taskID, _ := encodeTaskID("task1")
	foreignChecklistID, _ := encodeChecklistID("foreign-check")
	title := "must not be written"
	_, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		Account: graphTaskAccount, ListID: listID, TaskID: taskID,
		Version: encodeETag(`W/"task-v1"`), Title: &title,
		ReplaceChecklist: true,
		Checklist: []application.TaskChecklistItemInput{{
			ID: foreignChecklistID, Title: "Foreign",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not belong") || mutated {
		t.Fatalf("preflight error = %v mutated=%t", err, mutated)
	}
}

func TestDisabledMicrosoftTodoClientAdvertisesNothingAndRejectsReads(t *testing.T) {
	t.Parallel()
	client := &Client{}
	if got := client.TaskCapabilities(); !reflect.DeepEqual(got, application.TaskCapabilities{}) {
		t.Fatalf("disabled capabilities = %+v", got)
	}
	listID, _ := encodeTaskListID("list1")
	taskID, _ := encodeTaskID("task1")
	reads := []func() error{
		func() error {
			_, err := client.ListTaskLists(t.Context(), application.TaskListInput{})
			return err
		},
		func() error {
			_, err := client.ListTasks(t.Context(), application.TaskReadInput{ListID: listID})
			return err
		},
		func() error {
			_, err := client.GetTask(t.Context(), application.TaskGetInput{ListID: listID, TaskID: taskID})
			return err
		},
		func() error {
			_, err := client.SyncTasks(t.Context(), application.TaskSyncInput{ListID: listID})
			return err
		},
	}
	for _, read := range reads {
		if err := read(); err == nil || !strings.Contains(err.Error(), "not enabled") {
			t.Fatalf("disabled task read error = %v", err)
		}
	}
}

func TestMicrosoftTodoLinkedSourceRejectsTrailingJSON(t *testing.T) {
	t.Parallel()
	linked, err := graphWriteSources([]application.TaskLinkedSource{syntheticTaskSource("mail1")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(linked[0].ExternalID, "corr1_"))
	if err != nil {
		t.Fatal(err)
	}
	linked[0].ExternalID = "corr1_" + base64.RawURLEncoding.EncodeToString(append(raw, []byte(` {}`)...))
	if _, _, err := graphLinkedSource(linked[0]); err == nil {
		t.Fatal("linked source metadata with trailing JSON was accepted")
	}
}

func TestMicrosoftTodoExpiredDeltaCursorResetsFromFreshSnapshot(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	deltaCalls := 0
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			writeGraphJSON(t, writer, map[string]string{"id": "user1", "mail": "reader@example.test"})
		case "/me/todo/lists":
			writeGraphJSON(t, writer, map[string]any{"value": []map[string]string{{"id": "list1"}}})
		case "/me/todo/lists/list1/tasks/delta":
			deltaCalls++
			if request.URL.Query().Get("$deltatoken") == "expired" {
				writer.WriteHeader(http.StatusGone)
				_, _ = writer.Write([]byte(`{"error":{"code":"syncStateNotFound"}}`))
				return
			}
			writeGraphJSON(t, writer, graphTaskPage{
				DeltaLink: server.URL + "/me/todo/lists/list1/tasks/delta?$deltatoken=fresh",
			})
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newMicrosoftTodoFixtureClient(t, server, false)
	defer func() { _ = client.Close() }()
	listID, _ := encodeTaskListID("list1")
	cursor := application.TaskCursor{
		Provider: domain.ProviderMicrosoftGraph, Account: graphTaskAccount,
		ListID: listID, Mode: application.TaskSyncDelta,
		Value: server.URL + "/me/todo/lists/list1/tasks/delta?$deltatoken=expired",
	}
	page, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: graphTaskAccount, ListID: listID, Cursor: &cursor, Limit: 10,
	})
	if err != nil || !page.Reset || deltaCalls != 2 || !strings.Contains(page.Cursor.Value, "fresh") {
		t.Fatalf("SyncTasks(expired) = %+v, %v calls=%d", page, err, deltaCalls)
	}
}

func TestMicrosoftTodoCanonicalizesWindowsTaskZonesWithoutChangingTheInstant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		remote   graphDateTimeZone
		wantTime string
	}{
		{
			name: "local Graph datetime",
			remote: graphDateTimeZone{
				DateTime: "2026-08-15T09:00:00", TimeZone: "Pacific Standard Time",
			},
			wantTime: "2026-08-15T09:00:00-07:00",
		},
		{
			name: "absolute Graph datetime",
			remote: graphDateTimeZone{
				DateTime: "2026-08-15T16:00:00Z", TimeZone: "Pacific Standard Time",
			},
			wantTime: "2026-08-15T09:00:00-07:00",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := graphTaskTime(&test.remote)
			if err != nil {
				t.Fatal(err)
			}
			if got.TimeZone != "America/Los_Angeles" || got.Value != test.wantTime {
				t.Fatalf("graphTaskTime() = %+v", got)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("canonical task time is invalid: %v", err)
			}
		})
	}
}

func TestMicrosoftTodoTombstoneVersionsBindTheRemoteIdentity(t *testing.T) {
	t.Parallel()
	first := graphTaskTombstoneVersion("task-a")
	second := graphTaskTombstoneVersion("task-b")
	if first == second || first == "" || second == "" {
		t.Fatalf("tombstone versions = %q, %q", first, second)
	}
}

func TestMicrosoftTodoReportsMissingCompletionTime(t *testing.T) {
	t.Parallel()
	remote := graphTaskFixture(t, `W/"task-v1"`)
	remote.Status = "completed"
	remote.Completed = nil
	task, err := graphTaskView("list", remote)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, degradation := range task.Degradations {
		found = found || degradation.Feature == "completion_time"
	}
	if !found {
		t.Fatalf("completed task degradations = %+v", task.Degradations)
	}
}

func TestMicrosoftTodoPreservesNonPortableGraphRecurrenceRules(t *testing.T) {
	t.Parallel()
	anchor := &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: "2026-08-15T09:00:00-07:00", TimeZone: "America/Los_Angeles",
	}
	tests := []struct {
		name       string
		pattern    graphRecurrencePattern
		rangeValue *graphRecurrenceRange
		portable   bool
	}{
		{
			name: "canonical monthly",
			pattern: graphRecurrencePattern{
				Type: "absoluteMonthly", Interval: 1, DayOfMonth: 15,
			},
			portable: true,
		},
		{
			name: "monthly day differs from anchor",
			pattern: graphRecurrencePattern{
				Type: "absoluteMonthly", Interval: 1, DayOfMonth: 20,
			},
		},
		{
			name: "weekly boundary differs from canonical model",
			pattern: graphRecurrencePattern{
				Type: "weekly", Interval: 2, DaysOfWeek: []string{"saturday"},
				FirstDayOfWeek: "sunday",
			},
		},
		{
			name: "daily rule contains nonportable provider fields",
			pattern: graphRecurrencePattern{
				Type: "daily", Interval: 1, DayOfMonth: 15,
			},
		},
		{
			name:    "provider returned an unknown range",
			pattern: graphRecurrencePattern{Type: "daily", Interval: 1},
			rangeValue: &graphRecurrenceRange{
				Type: "futureRange", StartDate: "2026-08-15",
				RecurrenceTimeZone: "Pacific Standard Time",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rangeValue := graphRecurrenceRange{
				Type: "noEnd", StartDate: "2026-08-15",
				RecurrenceTimeZone: "Pacific Standard Time",
			}
			if test.rangeValue != nil {
				rangeValue = *test.rangeValue
			}
			remote := &graphTaskRecurrence{
				Pattern: test.pattern,
				Range:   rangeValue,
			}
			got, err := graphReadTaskRecurrence(remote, nil, anchor)
			if err != nil {
				t.Fatal(err)
			}
			if portable := got.Frequency != application.TaskRecurrenceProvider; portable != test.portable {
				t.Fatalf("graphReadTaskRecurrence() = %+v, portable=%t", got, portable)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("recurrence is invalid: %v", err)
			}
		})
	}
}

func newMicrosoftTodoFixtureClient(t *testing.T, server *httptest.Server, write bool) *Client {
	t.Helper()
	client, err := New(t.Context(), Options{
		APIBase: server.URL, Address: "reader@example.test",
		Tasks: true, TaskWrite: write, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func graphTaskFixture(t *testing.T, etag string) graphTask {
	t.Helper()
	source := syntheticTaskSource("mail1")
	linked, err := graphWriteSources([]application.TaskLinkedSource{source})
	if err != nil {
		t.Fatal(err)
	}
	linked[0].ID = "link1"
	return graphTask{
		ODataETag: etag, ID: "task1", Title: "Synthetic task",
		Body:   graphItemBody{ContentType: "html", Content: "<pre>Synthetic notes</pre>"},
		Status: "notStarted", Importance: "high",
		Due:        &graphDateTimeZone{DateTime: "2026-08-15T09:00:00", TimeZone: "Europe/London"},
		ReminderOn: true,
		Reminder:   &graphDateTimeZone{DateTime: "2026-08-15T08:30:00", TimeZone: "Europe/London"},
		Recurrence: &graphTaskRecurrence{
			Pattern: graphRecurrencePattern{Type: "daily", Interval: 1},
			Range:   graphRecurrenceRange{Type: "noEnd", StartDate: "2026-08-15", RecurrenceTimeZone: "Europe/London"},
		},
		Categories:      []string{"Synthetic"},
		ChecklistItems:  []graphChecklistItem{{ID: "old-check", DisplayName: "Old", Checked: false}},
		LinkedResources: linked,
	}
}

func syntheticTaskSource(object string) application.TaskLinkedSource {
	return application.TaskLinkedSource{
		Kind: application.TaskSourceMail, Account: graphTaskAccount,
		Provider: domain.ProviderMicrosoftGraph, ObjectID: object,
		URL: "https://outlook.office.com/mail/deeplink/read/synthetic",
	}
}

func nextSyntheticETag(current string) string {
	switch current {
	case `W/"task-v1"`:
		return `W/"task-v2"`
	case `W/"task-v2"`:
		return `W/"task-v3"`
	case `W/"task-v3"`:
		return `W/"task-v4"`
	default:
		return `W/"task-v5"`
	}
}
