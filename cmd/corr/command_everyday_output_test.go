package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

// everydayTestBackend adds the optional task extension to the adapter test
// daemon so task commands can run end to end.
type everydayTestBackend struct {
	*adapterTestBackend
}

var _ daemonapi.TaskBackend = everydayTestBackend{}

func everydayTestTask() application.Task {
	return application.Task{
		ID: "task-1", Version: "task-version-1", ListID: "list-1",
		Title: "Send the synthetic report", Status: application.TaskStatus("needs_action"),
		Priority: application.TaskPriority("high"),
		Due:      &application.TaskTemporal{Kind: application.TaskTemporalKind("date"), Value: "2026-12-01"},
		Provenance: domain.Provenance{
			AccountID: adapterTestAccountID, Provider: domain.ProviderTodoist,
			SourceObjectID: "task-1",
		},
	}
}

func everydayTaskReview(action string) application.TaskWriteReview {
	return application.TaskWriteReview{
		Account: adapterTestAccountID, Provider: domain.ProviderTodoist,
		Action: action, ListID: "list-1", TaskID: "task-1", Version: "task-version-1",
	}
}

func everydayTaskPreview(action string) application.TaskWriteAccess {
	return application.TaskWriteAccess{
		Status: "approval_required", Review: everydayTaskReview(action),
		Preview: &approval.Preview{
			Token: "opv1_synthetic", ExpiresAt: time.Now().Add(time.Minute), // gitleaks:allow -- synthetic fixture
			Operation: domain.OperationView{
				Name: "tasks." + action, Effect: domain.EffectReversibleWrite,
				Account: adapterTestAccountID, Digest: string(bytes.Repeat([]byte("9"), 64)),
			},
		},
	}
}

func (everydayTestBackend) ListTaskLists(context.Context, application.TaskListInput, domain.Caller) (application.TaskListPage, error) {
	return application.TaskListPage{Lists: []application.TaskList{{
		ID: "list-1", DisplayName: "Inbox", Editable: true, Default: true,
		Provenance: domain.Provenance{AccountID: adapterTestAccountID, Provider: domain.ProviderTodoist},
	}}, Limit: 50}, nil
}

func (everydayTestBackend) ListTasks(_ context.Context, input application.TaskReadInput, _ domain.Caller) (application.TaskPage, error) {
	return application.TaskPage{Tasks: []application.Task{everydayTestTask()}, Limit: input.Limit}, nil
}

func (everydayTestBackend) ListAllTasks(_ context.Context, input application.TaskProjectionInput, _ domain.Caller) (application.TaskProjectionPage, error) {
	return application.TaskProjectionPage{
		Tasks: []application.ProjectedTask{{AccountAlias: "work", Task: everydayTestTask()}},
		Failures: []application.ProjectionFailure{{
			Account: "acc_00000000000000000000000000000002", Alias: "home",
			Provider: domain.ProviderGoogle, Service: "tasks", Code: "provider_unavailable",
			Reason: "Synthetic provider outage.",
		}},
		Limit: input.Limit,
	}, nil
}

func (everydayTestBackend) GetTask(context.Context, application.TaskGetInput, domain.Caller) (application.Task, error) {
	return everydayTestTask(), nil
}

func (everydayTestBackend) SearchTasks(context.Context, application.TaskSearchInput, domain.Caller) (application.TaskPage, error) {
	return application.TaskPage{Limit: 50}, nil
}

func (everydayTestBackend) SyncTasks(context.Context, application.TaskSyncInput, domain.Caller) (application.TaskChangePage, error) {
	return application.TaskChangePage{}, nil
}

func (everydayTestBackend) CreateTask(context.Context, application.TaskCreateInput, domain.Caller) (application.TaskWriteAccess, error) {
	return everydayTaskPreview("create"), nil
}

func (everydayTestBackend) CommitTaskCreate(context.Context, string, domain.Caller) (application.TaskWriteAccess, error) {
	return application.TaskWriteAccess{Status: "created", Review: everydayTaskReview("create")}, nil
}

func (everydayTestBackend) UpdateTask(context.Context, application.TaskUpdateInput, domain.Caller) (application.TaskWriteAccess, error) {
	return everydayTaskPreview("update"), nil
}

func (everydayTestBackend) CommitTaskUpdate(context.Context, string, domain.Caller) (application.TaskWriteAccess, error) {
	return application.TaskWriteAccess{Status: "updated", Review: everydayTaskReview("update")}, nil
}

func (everydayTestBackend) CompleteTask(context.Context, application.TaskStateInput, domain.Caller) (application.TaskWriteAccess, error) {
	return everydayTaskPreview("complete"), nil
}

func (everydayTestBackend) CommitTaskComplete(context.Context, string, domain.Caller) (application.TaskWriteAccess, error) {
	return application.TaskWriteAccess{Status: "completed", Review: everydayTaskReview("complete")}, nil
}

func (everydayTestBackend) ReopenTask(context.Context, application.TaskStateInput, domain.Caller) (application.TaskWriteAccess, error) {
	return everydayTaskPreview("reopen"), nil
}

func (everydayTestBackend) CommitTaskReopen(context.Context, string, domain.Caller) (application.TaskWriteAccess, error) {
	return application.TaskWriteAccess{Status: "reopened", Review: everydayTaskReview("reopen")}, nil
}

func (everydayTestBackend) DeleteTask(context.Context, application.TaskDeleteInput, domain.Caller) (application.TaskWriteAccess, error) {
	return everydayTaskPreview("delete"), nil
}

func (everydayTestBackend) CommitTaskDelete(context.Context, string, domain.Caller) (application.TaskWriteAccess, error) {
	return application.TaskWriteAccess{Status: "deleted", Review: everydayTaskReview("delete")}, nil
}

// newEverydayTestRuntime starts the synthetic daemon and returns a runtime
// connected to it.
func newEverydayTestRuntime(t *testing.T) (*runtime, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.OutlookDefault()); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}
	endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatalf("config.Fingerprint() error = %v", err)
	}
	backend := everydayTestBackend{&adapterTestBackend{}}
	t.Cleanup(startAdapterTestDaemon(t, endpoint, configDigest, backend))
	var stdout, stderr bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &stderr, buildinfo.Current())
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("everyday command unexpectedly launched a browser")
		return nil, errors.New("unreachable")
	}
	return app, &stdout, &stderr
}

type everydayRunner interface{ Run(*runtime) error }

// previewExpiry and previewToken mask the two preview fields the synthetic
// daemon fills per run; every other byte is compared.
var (
	previewExpiry = regexp.MustCompile(`"expiresAt": "[^"]*"`)
	previewToken  = regexp.MustCompile(`"token": "[^"]*"`)
)

// TestEverydayJSONOutputIsUnchanged pins the --json output of one or more
// commands per group to the bytes produced before the help and output rewrite.
func TestEverydayJSONOutputIsUnchanged(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	app, stdout, _ := newEverydayTestRuntime(t)
	taskFile := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(taskFile, []byte(`{"listId":"list-1","title":"Synthetic","priority":"none"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		command everydayRunner
		golden  string
	}{
		{"mail list", &mailListCommand{Folder: "inbox", Limit: 25, TimeZone: "UTC", JSON: true}, "mail-list"},
		{"mail folders", &mailFoldersCommand{Parent: "msgfolderroot", Traversal: "deep", Limit: 100, TimeZone: "UTC", JSON: true}, "mail-folders"},
		{"mail search all accounts", &mailSearchCommand{AllAccounts: true, Folder: "inbox", Query: "from:ana@example.invalid", Limit: 25, TimeZone: "UTC", JSON: true}, "mail-search-all"},
		{"mail send preview", &mailSendCommand{To: []string{"ana@example.invalid"}, Subject: "Synthetic", BodyFormat: "text", Mode: "new", JSON: true}, "mail-send-preview"},
		{"calendar folders", &calendarFoldersCommand{Limit: 100, JSON: true}, "calendar-folders"},
		{"calendar create preview", &calendarCreateCommand{Subject: "Planning", Start: "2026-12-01T09:00:00Z", End: "2026-12-01T10:00:00Z", Recurrence: "none", RecurrenceInterval: 1, JSON: true}, "calendar-create-preview"},
		{"agenda list", &agendaListCommand{AllAccounts: true, Start: "2026-07-28T00:00:00Z", End: "2026-07-29T00:00:00Z", TimeZone: "UTC", Limit: 50, JSON: true}, "agenda-list"},
		{"tasks lists", &taskListsCommand{Limit: 50, JSON: true}, "task-lists"},
		{"tasks list", &taskListCommand{ListID: "list-1", Status: "any", Limit: 50, JSON: true}, "task-list"},
		{"tasks complete preview", &taskCompleteCommand{ListID: "list-1", TaskID: "task-1", TaskVersion: "task-version-1", JSON: true}, "task-complete-preview"},
		{"tasks create preview", &taskCreateCommand{File: taskFile, JSON: true}, "task-create-preview"},
	} {
		stdout.Reset()
		if err := test.command.Run(app); err != nil {
			t.Fatalf("%s: Run() error = %v", test.name, err)
		}
		got := previewExpiry.ReplaceAllString(stdout.String(), `"expiresAt": "<time>"`)
		compareEverydayGolden(t, test.golden, previewToken.ReplaceAllString(got, `"token": "<token>"`))
	}
}

// TestSavedQueryJSONOutputIsUnchanged pins `corr queries` --json output, which
// runs without the daemon, to the bytes produced before the rewrite.
func TestSavedQueryJSONOutputIsUnchanged(t *testing.T) {
	app, _, stdout := newAccountCommandRuntime(t, &accountDiscovererStub{})
	save := savedMailQueryCommand{
		Name: "from-ana", Query: "from:ana@example.invalid", Folder: "inbox",
		Limit: 25, TimeZone: "UTC", JSON: true,
	}
	for _, test := range []struct {
		golden  string
		command everydayRunner
	}{
		{"queries-save-preview", &save},
		{"queries-save", &savedMailQueryCommand{
			Name: save.Name, Query: save.Query, Folder: save.Folder,
			Limit: save.Limit, TimeZone: save.TimeZone, Approve: true, JSON: true,
		}},
		{"queries-list", &savedQueryListCommand{JSON: true}},
		{"queries-show", &savedQueryShowCommand{Name: "from-ana", JSON: true}},
		{"queries-purge-preview", &savedQueryPurgeCommand{JSON: true}},
	} {
		stdout.Reset()
		if err := test.command.Run(app); err != nil {
			t.Fatalf("%s: Run() error = %v", test.golden, err)
		}
		compareEverydayGolden(t, test.golden, stdout.String())
	}
}

// compareEverydayGolden compares got with testdata/everyday-json/NAME.json.
// The files were written by the code before the rewrite, with
// CORRESYNC_WRITE_EVERYDAY_GOLDEN=1.
func compareEverydayGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "everyday-json", name+".json")
	if os.Getenv("CORRESYNC_WRITE_EVERYDAY_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- fixed test fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s --json changed:\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}
