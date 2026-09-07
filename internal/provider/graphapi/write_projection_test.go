package graphapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestMicrosoftTodoWriteProjectionFailureReportsUnknownOutcome(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"create", "update", "complete", "reopen"} {
		t.Run(action, func(t *testing.T) {
			current := graphTask{ID: "task", ODataETag: "v1", Title: "Synthetic", Status: "notStarted"}
			writes := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := http.StatusOK
				if r.Method != http.MethodGet {
					writes++
					current.ODataETag = "v2"
				}
				if r.Method == http.MethodPost {
					status = http.StatusCreated
				}
				response := current
				if r.Method == http.MethodGet && writes != 0 {
					response.Due = &graphDateTimeZone{DateTime: "malformed", TimeZone: "UTC"}
				}
				writeGraphJSONStatus(t, w, response, status)
			}))
			defer server.Close()
			api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{api: api, tasks: true, taskWrite: true}
			defer func() { _ = client.Close() }()
			listID, _ := encodeTaskListID("list")
			taskID, _ := encodeTaskID("task")
			state := application.TaskStateInput{Account: graphTaskAccount, ListID: listID, TaskID: taskID, Version: encodeETag("v1")}
			switch action {
			case "create":
				_, err = client.CreateTask(t.Context(), application.TaskCreateInput{Account: graphTaskAccount, ListID: listID, Title: "Synthetic", Priority: application.TaskPriorityNone})
			case "update":
				title := "Updated"
				_, err = client.UpdateTask(t.Context(), application.TaskUpdateInput{Account: graphTaskAccount, ListID: listID, TaskID: taskID, Version: state.Version, Title: &title})
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
