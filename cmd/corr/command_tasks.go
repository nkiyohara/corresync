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
	Lists    taskListsCommand    `cmd:"" help:"List your task lists and their IDs."`
	List     taskListCommand     `cmd:"" help:"List tasks in a list, or in every account."`
	Get      taskGetCommand      `cmd:"" help:"Show one task."`
	Search   taskSearchCommand   `cmd:"" help:"Search tasks in one account."`
	Sync     taskSyncCommand     `cmd:"" help:"Show what changed in a task list since the last sync."`
	Create   taskCreateCommand   `cmd:"" help:"Create a task described in a JSON file."`
	Update   taskUpdateCommand   `cmd:"" help:"Change a task as described in a JSON file."`
	Complete taskCompleteCommand `cmd:"" help:"Mark a task as completed."`
	Reopen   taskReopenCommand   `cmd:"" help:"Reopen a completed task."`
	Delete   taskDeleteCommand   `cmd:"" help:"Delete a task."`
}

var tasksHelpExamples = []helpExample{
	{"See your task lists, then the open tasks in one of them:", []string{"tasks", "lists"}},
	{"", []string{"tasks", "list", "--list-id", "AQMkADAwATM-list", "--status", "needs_action"}},
	{"Complete a task (the first run shows a preview):", []string{
		"tasks", "complete", "--list-id", "AQMkADAwATM-list", "--task-id", "AAMkAGVm-task",
		"--task-version", "W/\"DwAAABYAAAA\"",
	}},
	{"Create a task from a file such as {\"listId\": \"AQMkADAwATM-list\", \"title\": \"Book travel\", \"priority\": \"normal\"}:", []string{
		"tasks", "create", "--file", "task.json",
	}},
}

// Help is the detail text of `corr tasks --help`.
func (tasksCommand) Help() string {
	return helpWithExamples(`
create, update, complete, reopen, and delete always show a preview first and
change nothing. Run the same command again with --approve added to carry it
out; the preview ends with that command.

List IDs come from corr tasks lists. Task IDs and versions come from corr tasks
list. A version names one state of a task: if the task changed after you listed
it, list it again. create and update read the task as JSON, from a file or
standard input; docs/tasks.md describes the fields.`, tasksHelpExamples)
}

type taskListsCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	Offset  int    `default:"0" help:"Skip this many results, for paging."`
	Limit   int    `default:"50" help:"Most task lists to show (1-100)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type taskListCommand struct {
	Account     string `help:"Account alias (default: your default account)."`
	ListID      string `name:"list-id" placeholder:"ID" help:"Task list ID, from corr tasks lists; needed unless the provider can list across lists."`
	Status      string `enum:"any,needs_action,in_progress,completed,cancelled" default:"any" help:"Show only tasks with this status."`
	AllAccounts bool   `name:"all-accounts" help:"List tasks from every task account; read-only, and results stay labelled by account."`
	Offset      int    `default:"0" help:"Skip this many results, for paging."`
	Limit       int    `default:"50" help:"Most tasks to show (1-100; 1-50 with --all-accounts)."`
	JSON        bool   `help:"Print JSON for scripts and agents."`
}

type taskGetCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	ListID  string `name:"list-id" placeholder:"ID" help:"Task list ID, from corr tasks lists (required)."`
	TaskID  string `name:"task-id" placeholder:"ID" help:"Task ID, from corr tasks list (required)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type taskSearchCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	ListID  string `name:"list-id" placeholder:"ID" help:"Task list ID, from corr tasks lists; needed unless the provider can search across lists."`
	Query   string `help:"Words to search for (required)."`
	Offset  int    `default:"0" help:"Skip this many results, for paging."`
	Limit   int    `default:"50" help:"Most tasks to show (1-100)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type taskSyncCommand struct {
	Account    string `help:"Account alias (default: your default account)."`
	ListID     string `name:"list-id" placeholder:"ID" help:"Task list ID, from corr tasks lists (required)."`
	CursorFile string `name:"cursor-file" type:"path" placeholder:"FILE" help:"Cursor JSON printed by the previous sync, or - to read standard input; leave out to start over."`
	Limit      int    `default:"50" help:"Most changes to show (1-100)."`
	JSON       bool   `help:"Print JSON for scripts and agents."`
}

// Create and update intentionally accept the canonical bounded JSON contract.
// This avoids an expanding flag dialect that would silently omit provider
// capabilities such as checklist items, assignments, and linked sources.
type taskFileWriteCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	File    string `type:"path" required:"" placeholder:"FILE" help:"JSON file describing the task, or - to read standard input; see docs/tasks.md."`
	Approve bool   `help:"Make the change instead of showing a preview."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type taskCreateCommand taskFileWriteCommand
type taskUpdateCommand taskFileWriteCommand

type taskStateCommand struct {
	Account     string `help:"Account alias (default: your default account)."`
	ListID      string `name:"list-id" placeholder:"ID" help:"Task list ID, from corr tasks lists (required)."`
	TaskID      string `name:"task-id" placeholder:"ID" help:"Task ID, from corr tasks list (required)."`
	TaskVersion string `name:"task-version" placeholder:"VERSION" help:"Task version, from corr tasks list (required)."`
	Approve     bool   `help:"Make the change instead of showing a preview."`
	JSON        bool   `help:"Print JSON for scripts and agents."`
}

type taskCompleteCommand taskStateCommand
type taskReopenCommand taskStateCommand
type taskDeleteCommand taskStateCommand

func (command *taskListsCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	alias, account, client, err := taskClient(app, command.Account)
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
		return app.writeEmpty(emptyResult{
			message: fmt.Sprintf("No task lists in account %s.", sanitizeCell(alias, 64)),
		})
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tDEFAULT\tCAN EDIT\tPROVIDER\tLIST ID"); err != nil {
		return err
	}
	for _, list := range page.Lists {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(list.DisplayName, 64), previewYesNo(list.Default), previewYesNo(list.Editable),
			sanitizeCell(string(list.Provenance.Provider), 64), sanitizeCell(list.ID, 4096)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (command *taskListCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
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
			return withHint(
				errors.New("all-accounts cannot be combined with account or list-id"),
				"Leave out --account and --list-id, or leave out --all-accounts to list one account.",
			)
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
		return writeTaskProjectionTable(app, page, emptyResult{
			message: "No " + taskStatusLabel(command.Status) + "tasks in any account.",
		})
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, account, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	page, err := client.ListTasks(app.context, application.TaskReadInput{
		Account: account, ListID: command.ListID, Status: status,
		Offset: command.Offset, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return withTaskListHint(err, command.Account, command.ListID)
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeTaskTable(app, page.Tasks, map[string]string{string(account): alias}, emptyResult{
		message: fmt.Sprintf(
			"No %stasks in %s of account %s.",
			taskStatusLabel(command.Status), taskListLabel(command.ListID), sanitizeCell(alias, 64),
		),
	})
}

func taskStatusLabel(status string) string {
	if status == "" || status == "any" {
		return ""
	}
	return sanitizeCell(status, 32) + " "
}

func taskListLabel(listID string) string {
	if listID == "" {
		return "any list"
	}
	return "list " + sanitizeCell(listID, 80)
}

// withTaskListHint points to corr tasks lists when a read that needs a list
// ID was run without one.
func withTaskListHint(err error, account, listID string) error {
	if listID != "" {
		return err
	}
	return withHint(err, printableCommand(withAccount([]string{commandName, "tasks", "lists"}, account)...))
}

func (command *taskGetCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	alias, account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	task, err := client.GetTask(app.context, application.TaskGetInput{
		Account: account, ListID: command.ListID, TaskID: command.TaskID,
	}, app.caller())
	if err != nil {
		return withTaskListHint(err, command.Account, command.ListID)
	}
	if command.JSON {
		return writeJSON(app.stdout, task)
	}
	return writeTaskTable(app, []application.Task{task}, map[string]string{string(account): alias}, emptyResult{})
}

func (command *taskSearchCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	alias, account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.SearchTasks(app.context, application.TaskSearchInput{
		Account: account, ListID: command.ListID, Query: command.Query,
		Offset: command.Offset, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return withTaskListHint(err, command.Account, command.ListID)
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeTaskTable(app, page.Tasks, map[string]string{string(account): alias}, emptyResult{
		message: fmt.Sprintf(
			"No tasks in %s of account %s match %s.",
			taskListLabel(command.ListID), sanitizeCell(alias, 64), quoteForSentence(command.Query),
		),
	})
}

func (command *taskSyncCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	alias, account, client, err := taskClient(app, command.Account)
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
		return withTaskListHint(err, command.Account, command.ListID)
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	if err := writeTaskChanges(app, page, alias, account); err != nil {
		return err
	}
	encodedCursor, err := json.Marshal(page.Cursor)
	if err != nil {
		return fmt.Errorf("encode next task cursor: %w", err)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Next cursor (save it to a file and pass it with --cursor-file next time): %s\n",
		encodedCursor,
	)
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

// taskAccountInJSONHint is the next step when a task JSON file names its own
// account.
const taskAccountInJSONHint = `Remove "account" from the JSON file and choose the account with --account.`

func (command *taskFileWriteCommand) runCreate(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	_, account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	var input application.TaskCreateInput
	if err := readTaskJSON(app, command.File, &input); err != nil {
		return err
	}
	if input.Account != "" {
		return withHint(errors.New("task JSON must omit account; use the --account routing option"), taskAccountInJSONHint)
	}
	input.Account = account
	return runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) {
			return client.CreateTask(app.context, input, app.caller())
		},
		func(token string) (application.TaskWriteAccess, error) {
			return client.CommitTaskCreate(app.context, token, app.caller())
		},
		app.approvalCommand(command, "tasks", "create"))
}

func (command *taskFileWriteCommand) runUpdate(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	_, account, client, err := taskClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	var input application.TaskUpdateInput
	if err := readTaskJSON(app, command.File, &input); err != nil {
		return err
	}
	if input.Account != "" {
		return withHint(errors.New("task JSON must omit account; use the --account routing option"), taskAccountInJSONHint)
	}
	input.Account = account
	return runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) {
			return client.UpdateTask(app.context, input, app.caller())
		},
		func(token string) (application.TaskWriteAccess, error) {
			return client.CommitTaskUpdate(app.context, token, app.caller())
		},
		app.approvalCommand(command, "tasks", "update"))
}

func (command *taskStateCommand) run(app *runtime, action string) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	_, account, client, err := taskClient(app, command.Account)
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
	err = runTaskWrite(app, command.Approve, command.JSON,
		func() (application.TaskWriteAccess, error) { return prepare(app.context, input, app.caller()) },
		func(token string) (application.TaskWriteAccess, error) {
			return commit(app.context, token, app.caller())
		},
		app.approvalCommand(command, "tasks", action))
	if err != nil && (command.ListID == "" || command.TaskID == "" || command.TaskVersion == "") {
		return withHint(err, "List IDs come from corr tasks lists; task IDs and versions from corr tasks list --list-id ID.")
	}
	return err
}

func runTaskWrite(
	app *runtime,
	approve, jsonOutput bool,
	prepare func() (application.TaskWriteAccess, error),
	commit func(string) (application.TaskWriteAccess, error),
	approvalCommand string,
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
		if err := writeTaskReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		_, err := fmt.Fprintln(app.stdout, view.nextStep(approvalCommand))
		return err
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
	_, err = fmt.Fprintf(
		app.stdout, "Task %s. The request was made once and is not retried.\n", sanitizeCell(access.Status, 32),
	)
	return err
}

func taskClient(app *runtime, reference string) (string, domain.AccountID, *daemonapi.Client, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", "", nil, err
	}
	alias, account, err := resolveAccount(configuration, reference)
	if err != nil {
		return "", "", nil, err
	}
	client, _, err := app.openDaemon(app.context)
	return alias, account, client, err
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

func writeTaskTable(
	app *runtime,
	tasks []application.Task,
	aliases map[string]string,
	empty emptyResult,
) error {
	if len(tasks) == 0 {
		return app.writeEmpty(empty)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ACCOUNT\tSTATUS\tDUE\tPRIORITY\tTITLE\tLIST ID\tVERSION\tTASK ID"); err != nil {
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

func writeTaskProjectionTable(app *runtime, page application.TaskProjectionPage, empty emptyResult) error {
	aliases := make(map[string]string, len(page.Accounts))
	tasks := make([]application.Task, 0, len(page.Tasks))
	for _, projected := range page.Tasks {
		aliases[string(projected.Task.Provenance.AccountID)] = projected.AccountAlias
		tasks = append(tasks, projected.Task)
	}
	if err := writeTaskTable(app, tasks, aliases, empty); err != nil {
		return err
	}
	return writeProjectionNotices(app, nil, page.Failures)
}

func writeTaskChanges(
	app *runtime,
	page application.TaskChangePage,
	alias string,
	account domain.AccountID,
) error {
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
		if err := writeTaskTable(app, upserts, map[string]string{string(account): alias}, emptyResult{}); err != nil {
			return err
		}
	}
	for _, change := range deletions {
		if _, err := fmt.Fprintf(app.stdout, "Deleted: task %s (version %s).\n",
			sanitizeCell(change.TaskID, 4096), sanitizeCell(change.Version, 4096)); err != nil {
			return err
		}
	}
	if len(page.Changes) == 0 {
		if _, err := fmt.Fprintln(app.stdout, "No changes since the last sync."); err != nil {
			return err
		}
	}
	return nil
}

func writeTaskReview(writer io.Writer, review application.TaskWriteReview, committing bool) error {
	previews := map[string]string{
		"create":   "Preview: no task has been created.",
		"update":   "Preview: the task has not been changed.",
		"complete": "Preview: the task has not been marked completed.",
		"reopen":   "Preview: the task has not been reopened.",
		"delete":   "Preview: nothing has been deleted.",
	}
	action := previews[review.Action]
	if action == "" {
		action = "Preview: the task has not been changed."
	}
	if committing {
		action = map[string]string{
			"create":   "Creating this task now:",
			"update":   "Changing this task now:",
			"complete": "Marking this task completed now:",
			"reopen":   "Reopening this task now:",
			"delete":   "Deleting this task now:",
		}[review.Action]
		if action == "" {
			action = "Changing this task now:"
		}
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	fields := []reviewField{
		{"Action", sanitizeCell(review.Action, 32)},
		{"Account", sanitizeCell(string(review.Account), 128)},
		{"Provider", sanitizeCell(string(review.Provider), 64)},
		{"List ID", sanitizeCell(review.ListID, 4096)},
	}
	if review.TaskID != "" {
		fields = append(fields, reviewField{"Task ID", sanitizeCell(review.TaskID, 4096)})
	}
	if review.Version != "" {
		fields = append(fields, reviewField{"Version", sanitizeCell(review.Version, 4096)})
	}
	if review.Title != nil {
		fields = append(fields, reviewField{"Title", sanitizeCell(*review.Title, 1024)})
	}
	for _, degradation := range review.Degradations {
		lossy := ""
		if degradation.Lossy {
			lossy = " (some detail will be lost)"
		}
		fields = append(fields, reviewField{"Limitation", sanitizeCell(degradation.Reason, 512) + lossy})
	}
	if err := writeReviewFields(writer, fields); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "All details of the change:"); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(review)
}
