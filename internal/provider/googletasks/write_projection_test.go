package googletasks

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestGoogleTasksOrderOnlyMovePreservesParent(t *testing.T) {
	t.Parallel()
	for _, afterSibling := range []bool{false, true} {
		name := "first"
		if afterSibling {
			name = "after_sibling"
		}
		t.Run(name, func(t *testing.T) {
			current := task{ID: "task", ETag: "v1", Title: "Synthetic", Status: "needsAction", Updated: "2026-09-07T00:00:00Z", Parent: "parent"}
			moves := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tasks/v1/lists/list/tasks/previous" {
					previous := current
					previous.ID = "previous"
					writeJSON(t, w, previous)
					return
				}
				if r.Method == http.MethodPost {
					moves++
					if r.URL.Query().Get("parent") != "parent" {
						t.Errorf("move parent = %q, want existing parent", r.URL.Query().Get("parent"))
					}
					expectedPrevious := ""
					if afterSibling {
						expectedPrevious = "previous"
					}
					if r.URL.Query().Get("previous") != expectedPrevious {
						t.Errorf("move previous = %q, want %q", r.URL.Query().Get("previous"), expectedPrevious)
					}
					current.Parent = r.URL.Query().Get("parent")
					current.ETag = "v2"
				}
				writeJSON(t, w, current)
			}))
			defer server.Close()
			api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{api: api, account: testAccount}
			defer func() { _ = client.Close() }()
			listID, _ := encodeID("gtl1_", "list")
			taskID, _ := encodeID("gtt1_", "task")
			parentID, _ := encodeID("gtt1_", "parent")
			order := ""
			if afterSibling {
				order, _ = encodeID("gtt1_", "previous")
			}
			updated, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{Account: testAccount, ListID: listID, TaskID: taskID, Version: encodeETag("v1"), Order: &order})
			if err != nil || updated.ParentID != parentID || moves != 1 {
				t.Fatalf("order-only update = %+v, %v; moves = %d", updated, err, moves)
			}
		})
	}
}

func TestGoogleTasksWriteProjectionFailureReportsUnknownOutcome(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"create", "update", "complete", "reopen"} {
		t.Run(action, func(t *testing.T) {
			current := task{ID: "task", ETag: "v1", Title: "Synthetic", Status: "needsAction", Updated: "2026-09-07T00:00:00Z"}
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
					current.ETag = "v2"
				}
				response := current
				if r.Method == http.MethodGet && writes != 0 {
					response.Due = "malformed"
				}
				writeJSON(t, w, response)
			}))
			defer server.Close()
			api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{api: api, account: testAccount}
			defer func() { _ = client.Close() }()
			listID, _ := encodeID("gtl1_", "list")
			taskID, _ := encodeID("gtt1_", "task")
			state := application.TaskStateInput{Account: testAccount, ListID: listID, TaskID: taskID, Version: encodeETag("v1")}
			switch action {
			case "create":
				_, err = client.CreateTask(t.Context(), application.TaskCreateInput{Account: testAccount, ListID: listID, Title: "Synthetic", Priority: application.TaskPriorityNone})
			case "update":
				title := "Updated"
				_, err = client.UpdateTask(t.Context(), application.TaskUpdateInput{Account: testAccount, ListID: listID, TaskID: taskID, Version: state.Version, Title: &title})
			case "complete":
				_, err = client.CompleteTask(t.Context(), state)
			case "reopen":
				_, err = client.ReopenTask(t.Context(), state)
			}
			if writes != 1 || !errors.Is(err, application.ErrWriteOutcomeUnknown) {
				t.Fatalf("%s dispatched %d writes; projection error = %v, want unknown outcome", action, writes, err)
			}
		})
	}
}

func TestGoogleTasksCreateSubtaskPreservesParentQuery(t *testing.T) {
	t.Parallel()
	current := task{ID: "child", ETag: "v1", Title: "Synthetic", Status: "needsAction", Updated: "2026-09-07T00:00:00Z", Parent: "parent"}
	creates := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tasks/v1/lists/list/tasks/parent" {
			parent := current
			parent.ID = "parent"
			parent.Parent = ""
			writeJSON(t, w, parent)
			return
		}
		if r.Method == http.MethodPost {
			creates++
			if r.URL.Query().Get("parent") != "parent" {
				t.Errorf("create parent=%q", r.URL.Query().Get("parent"))
			}
			current.Parent = r.URL.Query().Get("parent")
		}
		writeJSON(t, w, current)
	}))
	defer server.Close()
	api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{api: api, account: testAccount}
	defer func() { _ = client.Close() }()
	listID, _ := encodeID("gtl1_", "list")
	parentID, _ := encodeID("gtt1_", "parent")
	result, err := client.CreateTask(t.Context(), application.TaskCreateInput{Account: testAccount, ListID: listID, ParentID: parentID, Title: "Synthetic", Priority: application.TaskPriorityNone})
	if err != nil || result.ParentID != parentID || creates != 1 {
		t.Fatalf("create subtask=%+v, err=%v, creates=%d", result, err, creates)
	}
}
