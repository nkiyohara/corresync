package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumTaskInputBytes = 4 << 20

type tasksCommand struct {
	Lists    taskListsCommand    `cmd:"" help:"List task lists for one account."`
	List     taskListCommand     `cmd:"" help:"List tasks for one account or an isolated cross-account projection."`
	Get      taskGetCommand      `cmd:"" help:"Get one task by exact list and task ID."`
	Search   taskSearchCommand   `cmd:"" help:"Search tasks in one account."`
	Sync     taskSyncCommand     `cmd:"" help:"Read bounded incremental changes for one task list."`
	Create   taskCreateCommand   `cmd:"" help:"Review and create a task from a strict JSON document."`
	Update   taskUpdateCommand   `cmd:"" help:"Review and update one versioned task from a strict JSON document."`
	Complete taskCompleteCommand `cmd:"" help:"Review and complete one versioned task."`
	Reopen   taskReopenCommand   `cmd:"" help:"Review and reopen one versioned task."`
	Delete   taskDeleteCommand   `cmd:"" help:"Review and delete one versioned task."`
}

type taskListsCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	Offset  int    `default:"0" help:"Zero-based page offset."`
	Limit   int    `default:"50" help:"Task lists to return (1-100)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type taskListCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	ListID      string `name:"list-id" help:"Opaque task-list ID; omit only for a provider with observed cross-list support."`
	Status      string `enum:"any,needs_action,in_progress,completed,cancelled" default:"any" help:"Task status filter."`
	AllAccounts bool   `name:"all-accounts" help:"Read every configured task account as an isolated projection."`
	Offset      int    `default:"0" help:"Zero-based page offset."`
	Limit       int    `default:"50" help:"Tasks to return (1-100; cross-account maximum 50)."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type taskGetCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	ListID  string `name:"list-id" help:"Exact task-list ID (required)."`
	TaskID  string `name:"task-id" help:"Exact task ID (required)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type taskSearchCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	ListID  string `name:"list-id" help:"Opaque task-list ID; omit only for a provider with observed cross-list support."`
	Query   string `help:"Provider-neutral text query (required)."`
	Offset  int    `default:"0" help:"Zero-based page offset."`
	Limit   int    `default:"50" help:"Tasks to return (1-100)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type taskSyncCommand struct {
	Account    string `help:"Configured account alias; defaults to default_account."`
	ListID     string `name:"list-id" help:"Exact task-list ID (required)."`
	CursorFile string `name:"cursor-file" type:"path" help:"Strict TaskCursor JSON file, or - for stdin."`
	Limit      int    `default:"50" help:"Changes to return (1-100)."`
	JSON       bool   `help:"Write the stable machine-readable schema."`
}

// Create and update intentionally accept the canonical bounded JSON contract.
// This avoids an expanding flag dialect that would silently omit provider
// capabilities such as checklist items, assignments, and linked sources.
type taskFileWriteCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	File    string `type:"path" required:"" help:"Strict canonical task JSON file, or - for stdin."`
	Approve bool   `help:"Apply the exact preview generated from the document."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type taskCreateCommand taskFileWriteCommand
type taskUpdateCommand taskFileWriteCommand

type taskStateCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	ListID      string `name:"list-id" help:"Exact task-list ID (required)."`
	TaskID      string `name:"task-id" help:"Exact task ID (required)."`
	TaskVersion string `name:"task-version" help:"Exact provider version or ETag returned by a read (required)."`
	Approve     bool   `help:"Apply the exact preview generated from these arguments."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type taskCompleteCommand taskStateCommand
type taskReopenCommand taskStateCommand
type taskDeleteCommand taskStateCommand

func (command *taskListsCommand) Run(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListTaskLists(app.context, application.TaskListInput{
		Account: account, Offset: command.Offset, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	if len(page.Lists) == 0 {
		_, err = fmt.Fprintln(app.stdout, "No task lists.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tDEFAULT\tEDITABLE\tPROVIDER\tID"); err != nil {
		return err
	}
	for _, list := range page.Lists {
		if _, err := fmt.Fprintf(writer, "%s\t%t\t%t\t%s\t%s\n",
			sanitizeCell(list.DisplayName, 64), list.Default, list.Editable,
			sanitizeCell(string(list.Provenance.Provider), 64), sanitizeCell(list.ID, 4096)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (command *taskListCommand) Run(app *runtime) (returnErr error) {
	status := application.TaskStatus(command.Status)
	if command.Status == "any" {
		status = ""
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	if command.AllAccounts {
		if command.Account != "" || command.ListID != "" {
			return errors.New("all-accounts cannot be combined with account or list-id")
		}
		page, err := client.ListAllTasks(app.context, application.TaskProjectionInput{
			Status: status, Offset: command.Offset, Limit: command.Limit,
		}, app.caller())
		if err != nil {
			return err
		}
		if command.JSON {
			return writeJSON(app.stdout, page)
		}
		return writeTaskProjectionTable(app, page)
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	account, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	page, err := client.ListTasks(app.context, application.TaskReadInput{
		Account: account, ListID: command.ListID, Status: status,
		Offset: command.Offset, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeTaskTable(app, page.Tasks, nil)
}

func (command *taskGetCommand) Run(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	task, err := client.GetTask(app.context, application.TaskGetInput{
		Account: account, ListID: command.ListID, TaskID: command.TaskID,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, task)
	}
	return writeTaskTable(app, []application.Task{task}, nil)
}

func (command *taskSearchCommand) Run(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.SearchTasks(app.context, application.TaskSearchInput{
		Account: account, ListID: command.ListID, Query: command.Query,
		Offset: command.Offset, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeTaskTable(app, page.Tasks, nil)
}

func (command *taskSyncCommand) Run(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	input := application.TaskSyncInput{Account: account, ListID: command.ListID, Limit: command.Limit}
	if command.CursorFile != "" {
		var cursor application.TaskCursor
		if err := readTaskJSON(app, command.CursorFile, &cursor); err != nil {
			return err
		}
		input.Cursor = &cursor
	}
	page, err := client.SyncTasks(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	if err := writeTaskChanges(app, page); err != nil {
		return err
	}
	encodedCursor, err := json.Marshal(page.Cursor)
	if err != nil {
		return fmt.Errorf("encode next task cursor: %w", err)
	}
	_, err = fmt.Fprintf(app.stdout, "Next cursor: %s\n", encodedCursor)
	return err
}

func (command *taskCreateCommand) Run(app *runtime) error {
	return (*taskFileWriteCommand)(command).runCreate(app)
}

func (command *taskUpdateCommand) Run(app *runtime) error {
	return (*taskFileWriteCommand)(command).runUpdate(app)
}

func (command *taskCompleteCommand) Run(app *runtime) error {
	return (*taskStateCommand)(command).run(app, "complete")
}

func (command *taskReopenCommand) Run(app *runtime) error {
	return (*taskStateCommand)(command).run(app, "reopen")
}

func (command *taskDeleteCommand) Run(app *runtime) error {
	return (*taskStateCommand)(command).run(app, "delete")
}

func (command *taskFileWriteCommand) runCreate(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	var input application.TaskCreateInput
	if err := readTaskJSON(app, command.File, &input); err != nil {
		return err
	}
	if input.Account != "" {
		return errors.New("task JSON must omit account; use the --account routing option")
	}
	input.Account = account
	return runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) {
			return client.CreateTask(app.context, input, app.caller())
		},
		func(token string) (application.TaskWriteAccess, error) {
			return client.CommitTaskCreate(app.context, token, app.caller())
		})
}

func (command *taskFileWriteCommand) runUpdate(app *runtime) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	var input application.TaskUpdateInput
	if err := readTaskJSON(app, command.File, &input); err != nil {
		return err
	}
	if input.Account != "" {
		return errors.New("task JSON must omit account; use the --account routing option")
	}
	input.Account = account
	return runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) {
			return client.UpdateTask(app.context, input, app.caller())
		},
		func(token string) (application.TaskWriteAccess, error) {
			return client.CommitTaskUpdate(app.context, token, app.caller())
		})
}

func (command *taskStateCommand) run(app *runtime, action string) (returnErr error) {
	account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	input := application.TaskStateInput{
		Account: account, ListID: command.ListID, TaskID: command.TaskID, Version: command.TaskVersion,
	}
	prepare := client.CompleteTask
	commit := client.CommitTaskComplete
	switch action {
	case "reopen":
		prepare, commit = client.ReopenTask, client.CommitTaskReopen
	case "delete":
		prepare, commit = client.DeleteTask, client.CommitTaskDelete
	}
	return runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) { return prepare(app.context, input, app.caller()) },
		func(token string) (application.TaskWriteAccess, error) {
			return commit(app.context, token, app.caller())
		})
}

func runTaskWrite(
	app *runtime,
	approve, jsonOutput bool,
	prepare func() (application.TaskWriteAccess, error),
	commit func(string) (application.TaskWriteAccess, error),
) error {
	access, err := prepare()
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("task write did not produce its mandatory preview")
	}
	if !approve {
		if jsonOutput {
			return writeJSON(app.stdout, access)
		}
		return writeTaskReview(app.stdout, access.Review, false)
	}
	if err := writeTaskReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = commit(access.Preview.Token)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(app.stdout, "Task %s; the provider request was attempted once.\n", access.Status)
	return err
}

func taskClient(app *runtime, reference string) (domain.AccountID, *daemonapi.Client, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", nil, err
	}
	account, err := app.account(configuration, reference)
	if err != nil {
		return "", nil, err
	}
	client, _, err := app.openDaemon(app.context)
	return account, client, err
}

func readTaskJSON(app *runtime, path string, destination any) error {
	reader := app.stdin
	var file *os.File
	var err error
	if path != "-" {
		file, err = os.Open(path) // #nosec G304 -- explicit local CLI input.
		if err != nil {
			return fmt.Errorf("open task JSON: %w", err)
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximumTaskInputBytes+1))
	if err != nil {
		return fmt.Errorf("read task JSON: %w", err)
	}
	if len(data) > maximumTaskInputBytes {
		return fmt.Errorf("task JSON exceeds %d bytes", maximumTaskInputBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode task JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("task JSON must contain exactly one value")
	}
	return nil
}

func writeTaskTable(app *runtime, tasks []application.Task, aliases map[string]string) error {
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No tasks.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ACCOUNT\tSTATUS\tDUE\tPRIORITY\tTITLE\tLIST\tVERSION\tID"); err != nil {
		return err
	}
	for _, task := range tasks {
		account := string(task.Provenance.AccountID)
		if alias := aliases[account]; alias != "" {
			account = alias
		}
		due := ""
		if task.Due != nil {
			due = task.Due.Value
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(account, 128), task.Status, sanitizeCell(due, 64), task.Priority,
			sanitizeCell(task.Title, 80), sanitizeCell(task.ListID, 4096),
			sanitizeCell(task.Version, 4096), sanitizeCell(task.ID, 4096)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeTaskProjectionTable(app *runtime, page application.TaskProjectionPage) error {
	aliases := make(map[string]string, len(page.Accounts))
	tasks := make([]application.Task, 0, len(page.Tasks))
	for _, projected := range page.Tasks {
		aliases[string(projected.Task.Provenance.AccountID)] = projected.AccountAlias
		tasks = append(tasks, projected.Task)
	}
	if err := writeTaskTable(app, tasks, aliases); err != nil {
		return err
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	for _, failure := range page.Failures {
		if _, err := view.printf("%s  Incomplete: %s (%s) · %s\n", view.warning(),
			sanitizeCell(failure.Alias, 64), sanitizeCell(string(failure.Provider), 64),
			sanitizeCell(failure.Reason, 512)); err != nil {
			return err
		}
	}
	return nil
}

func writeTaskChanges(app *runtime, page application.TaskChangePage) error {
	upserts := make([]application.Task, 0, len(page.Changes))
	deletions := make([]application.TaskChange, 0, len(page.Changes))
	for _, change := range page.Changes {
		if change.Task != nil {
			upserts = append(upserts, *change.Task)
		} else {
			deletions = append(deletions, change)
		}
	}
	if len(upserts) > 0 {
		if err := writeTaskTable(app, upserts, nil); err != nil {
			return err
		}
	}
	for _, change := range deletions {
		if _, err := fmt.Fprintf(app.stdout, "Deleted task %s (version %s).\n",
			sanitizeCell(change.TaskID, 4096), sanitizeCell(change.Version, 4096)); err != nil {
			return err
		}
	}
	return nil
}

func writeTaskReview(writer io.Writer, review application.TaskWriteReview, committing bool) error {
	verb := "Preview"
	if committing {
		verb = "Approved"
	}
	if _, err := fmt.Fprintf(writer, "%s task %s — exact typed review follows.\n",
		verb, review.Action); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(review)
}
