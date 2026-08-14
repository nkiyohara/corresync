package googletasks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const testAccount domain.AccountID = "acc_00000000000000000000000000000112"

func TestGoogleTasksReadWriteAndPollingContract(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	current := task{
		ID: "task-1", ETag: `"v1"`, Title: "Prepare release", Notes: "Synthetic",
		Updated: "2026-08-14T06:00:00Z", Status: "needsAction",
		Due: "2026-08-18T00:00:00.000Z", Position: "0001",
		Links: []taskLink{{Type: "email", Description: "Origin", Link: "https://mail.google.com/mail/u/0/#inbox/example"}},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/identity/v1/userinfo":
			writeJSON(t, writer, map[string]any{
				"sub": "google-user", "email": "person@example.com", "email_verified": true,
			})
		case request.URL.Path == "/api/tasks/v1/users/@me/lists":
			writeJSON(t, writer, taskListPage{Items: []taskList{{
				ID: "list-1", ETag: `"l1"`, Title: "My Tasks", Updated: "2026-08-14T05:00:00Z",
			}}})
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks" && request.Method == http.MethodGet:
			if request.URL.Query().Get("showDeleted") == "false" {
				assertTaskListQuery(t, request.URL.Query())
			} else if request.URL.Query().Get("showDeleted") != "true" {
				t.Errorf("showDeleted = %q", request.URL.Query().Get("showDeleted"))
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(t, writer, taskPage{Items: []task{current}})
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks/task-1" && request.Method == http.MethodGet:
			writeJSON(t, writer, current)
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks/task-1" && request.Method == http.MethodPatch:
			if request.Header.Get("If-Match") != current.ETag {
				writer.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			var payload map[string]any
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if title, ok := payload["title"].(string); ok {
				current.Title = title
			}
			if status, ok := payload["status"].(string); ok {
				current.Status = status
				if status == "completed" {
					current.Completed = "2026-08-14T07:00:00Z"
				} else {
					current.Completed = ""
				}
			}
			current.ETag = `"v2"`
			current.Updated = "2026-08-14T07:00:00Z"
			writeJSON(t, writer, current)
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks/task-1" && request.Method == http.MethodDelete:
			if request.Header.Get("If-Match") != current.ETag {
				writer.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(context.Background(), Options{
		APIBase: server.URL + "/api", IdentityBase: server.URL + "/identity",
		Address: "person@example.com", Account: testAccount,
		HTTP: server.Client(), Now: func() time.Time {
			return time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	capabilities := client.TaskCapabilities()
	if !capabilities.Read || !capabilities.Create || !capabilities.OptimisticConcurrency ||
		!capabilities.DateOnly || !capabilities.Subtasks || capabilities.Reminders {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
	lists, err := client.ListTaskLists(context.Background(), application.TaskListInput{
		Account: testAccount, Limit: 10,
	})
	if err != nil || len(lists.Lists) != 1 {
		t.Fatalf("ListTaskLists() = %+v, %v", lists, err)
	}
	listID := lists.Lists[0].ID
	page, err := client.ListTasks(context.Background(), application.TaskReadInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("ListTasks() = %+v, %v", page, err)
	}
	read := page.Tasks[0]
	if read.Title != "Prepare release" || read.Due == nil ||
		read.Due.Kind != application.TaskTemporalDate || len(read.Sources) != 1 {
		t.Fatalf("unexpected task: %+v", read)
	}
	newTitle := "Ship release"
	updated, err := client.UpdateTask(context.Background(), application.TaskUpdateInput{
		Account: testAccount, ListID: listID, TaskID: read.ID,
		Version: read.Version, Title: &newTitle,
	})
	if err != nil || updated.Title != newTitle || updated.Version == read.Version {
		t.Fatalf("UpdateTask() = %+v, %v", updated, err)
	}
	completed, err := client.CompleteTask(context.Background(), application.TaskStateInput{
		Account: testAccount, ListID: listID, TaskID: updated.ID, Version: updated.Version,
	})
	if err != nil || completed.Status != application.TaskStatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("CompleteTask() = %+v, %v", completed, err)
	}
	changes, err := client.SyncTasks(context.Background(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
	})
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Task == nil ||
		changes.Cursor.Provider != domain.ProviderGoogleTasks ||
		changes.Cursor.Mode != application.TaskSyncPolling {
		t.Fatalf("SyncTasks() = %+v, %v", changes, err)
	}
	if err := client.DeleteTask(context.Background(), application.TaskDeleteInput{
		Account: testAccount, ListID: listID, TaskID: completed.ID, Version: completed.Version,
	}); err != nil {
		t.Fatalf("DeleteTask() = %v", err)
	}
}

func TestGoogleTasksRejectsWrongIdentityAndStaleWrite(t *testing.T) {
	t.Parallel()
	identityMismatchServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/identity/v1/userinfo":
			writeJSON(t, writer, map[string]any{
				"sub": "google-user", "email": "other@example.com", "email_verified": true,
			})
		default:
			writeJSON(t, writer, taskListPage{})
		}
	}))
	defer identityMismatchServer.Close()
	_, err := New(context.Background(), Options{
		APIBase:      identityMismatchServer.URL + "/api",
		IdentityBase: identityMismatchServer.URL + "/identity",
		Address:      "person@example.com", Account: testAccount,
		HTTP: identityMismatchServer.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("New() error = %v", err)
	}

	current := task{
		ID: "task-1", ETag: `"new"`, Title: "Task",
		Updated: "2026-08-14T06:00:00Z", Status: "needsAction",
	}
	staleWriteServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/identity/v1/userinfo":
			writeJSON(t, writer, map[string]any{
				"sub": "google-user", "email": "person@example.com", "email_verified": true,
			})
		case "/api/tasks/v1/users/@me/lists":
			writeJSON(t, writer, taskListPage{})
		case "/api/tasks/v1/lists/list-1/tasks/task-1":
			writeJSON(t, writer, current)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer staleWriteServer.Close()
	client, err := New(context.Background(), Options{
		APIBase:      staleWriteServer.URL + "/api",
		IdentityBase: staleWriteServer.URL + "/identity",
		Address:      "person@example.com", Account: testAccount,
		HTTP: staleWriteServer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	listID, _ := encodeID("gtl1_", "list-1")
	taskID, _ := encodeID("gtt1_", "task-1")
	title := "Changed"
	_, err = client.UpdateTask(context.Background(), application.TaskUpdateInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
		Version: encodeETag(`"old"`), Title: &title,
	})
	if !errors.Is(err, restapi.ErrPrecondition) {
		t.Fatalf("UpdateTask() error = %v", err)
	}
}

func TestGoogleTasksRejectsAssignedTaskDeletionBeforeMutation(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	createCalls := 0
	assigned := task{
		ID: "assigned-1", ETag: `"assigned"`, Title: "Review document",
		Updated: "2026-08-14T06:00:00Z", Status: "needsAction",
		AssignmentInfo: &assignmentInfo{
			LinkToTask:  "https://docs.google.com/document/d/synthetic",
			SurfaceType: "DOCUMENT",
		},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/identity/v1/userinfo":
			writeJSON(t, writer, map[string]any{
				"sub": "google-user", "email": "person@example.com", "email_verified": true,
			})
		case request.URL.Path == "/api/tasks/v1/users/@me/lists":
			writeJSON(t, writer, taskListPage{})
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks/assigned-1" && request.Method == http.MethodGet:
			writeJSON(t, writer, assigned)
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks/assigned-1" && request.Method == http.MethodDelete:
			deleteCalls++
			writer.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/api/tasks/v1/lists/list-1/tasks" && request.Method == http.MethodPost:
			createCalls++
			writeJSON(t, writer, task{})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Options{
		APIBase: server.URL + "/api", IdentityBase: server.URL + "/identity",
		Address: "person@example.com", Account: testAccount, HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	listID, _ := encodeID("gtl1_", "list-1")
	taskID, _ := encodeID("gtt1_", "assigned-1")
	err = client.DeleteTask(context.Background(), application.TaskDeleteInput{
		Account: testAccount, ListID: listID, TaskID: taskID,
		Version: encodeETag(assigned.ETag),
	})
	if err == nil || !strings.Contains(err.Error(), "source task") || deleteCalls != 0 {
		t.Fatalf("DeleteTask() error = %v, mutation calls = %d", err, deleteCalls)
	}
	_, err = client.CreateTask(context.Background(), application.TaskCreateInput{
		Account: testAccount, ListID: listID, ParentID: taskID,
		Title: "Child", Priority: application.TaskPriorityNone,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be a parent") || createCalls != 0 {
		t.Fatalf("CreateTask() error = %v, mutation calls = %d", err, createCalls)
	}
}

func TestGoogleTasksPollingPaginatesAndResetsExpiredToken(t *testing.T) {
	t.Parallel()
	var requests []url.Values
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/identity/v1/userinfo":
			writeJSON(t, writer, map[string]any{
				"sub": "google-user", "email": "person@example.com", "email_verified": true,
			})
		case "/api/tasks/v1/users/@me/lists":
			writeJSON(t, writer, taskListPage{})
		default:
			requests = append(requests, request.URL.Query())
			if request.URL.Query().Get("pageToken") == "expired" {
				writer.WriteHeader(http.StatusGone)
				writeJSON(t, writer, map[string]any{"error": map[string]any{"code": 410}})
				return
			}
			writeJSON(t, writer, taskPage{Items: []task{{
				ID: "gone", Updated: "2026-08-14T08:00:00Z", Deleted: true,
			}}})
		}
	}))
	defer server.Close()
	client, err := New(context.Background(), Options{
		APIBase: server.URL + "/api", IdentityBase: server.URL + "/identity",
		Address: "person@example.com", Account: testAccount, HTTP: server.Client(),
		Now: func() time.Time { return time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := encodeCursor(pollingCursor{
		Version: 1, Watermark: "2026-08-14T07:00:00Z",
		PageToken: "expired", ScanStart: "2026-08-14T08:00:00Z", Pages: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	listID, _ := encodeID("gtl1_", "list-1")
	page, err := client.SyncTasks(context.Background(), application.TaskSyncInput{
		Account: testAccount, ListID: listID, Limit: 10,
		Cursor: &application.TaskCursor{
			Provider: domain.ProviderGoogleTasks, Account: testAccount,
			ListID: listID, Mode: application.TaskSyncPolling, Value: cursor,
		},
	})
	if err != nil || !page.Reset || len(page.Changes) != 1 ||
		page.Changes[0].Kind != application.TaskChangeDelete {
		t.Fatalf("SyncTasks() = %+v, %v", page, err)
	}
	if len(requests) != 2 || requests[0].Get("updatedMin") == "" ||
		requests[1].Get("updatedMin") != "" || requests[1].Get("showDeleted") != "true" {
		t.Fatalf("poll queries = %#v", requests)
	}
}

func TestGoogleTasksCursorAndFieldConstraints(t *testing.T) {
	t.Parallel()
	if _, err := decodeCursor("gtc1_e30"); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	due := application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: "2026-08-14T09:00:00Z", TimeZone: "UTC",
	}
	_, err := createPayload(application.TaskCreateInput{
		Title: "Task", Priority: application.TaskPriorityNone, Due: &due,
	})
	if err == nil || !strings.Contains(err.Error(), "date-only") {
		t.Fatalf("createPayload() error = %v", err)
	}
	if _, err := createPayload(application.TaskCreateInput{
		Title: "Task", Priority: application.TaskPriorityHigh,
	}); err == nil {
		t.Fatal("priority was silently accepted")
	}
	if _, err := createPayload(application.TaskCreateInput{
		Title: strings.Repeat("界", 1025), Priority: application.TaskPriorityNone,
	}); err == nil || !strings.Contains(err.Error(), "1024 characters") {
		t.Fatalf("oversized title error = %v", err)
	}
	if _, err := createPayload(application.TaskCreateInput{
		Title: "Task", Notes: strings.Repeat("界", 8193),
		Priority: application.TaskPriorityNone,
	}); err == nil || !strings.Contains(err.Error(), "8192 characters") {
		t.Fatalf("oversized notes error = %v", err)
	}
	if _, err := encodeCursor(pollingCursor{
		Version: 1, PageToken: "next", ScanStart: "2026-08-14T08:00:00Z",
	}); err == nil {
		t.Fatal("page cursor without a bounded page count was accepted")
	}
}

func assertTaskListQuery(t *testing.T, query url.Values) {
	t.Helper()
	want := map[string]string{
		"showAssigned": "true", "showCompleted": "true",
		"showDeleted": "false", "showHidden": "true",
	}
	for key, value := range want {
		if query.Get(key) != value {
			t.Fatalf("query %s = %q, want %q", key, query.Get(key), value)
		}
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if reflect.ValueOf(value).Kind() != reflect.Invalid {
		if err := json.NewEncoder(writer).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
}
