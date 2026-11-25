//go:build live

// Shared runner for the opt-in live route harnesses (files named
// *_live_test.go in this directory). It is compiled only with `-tags live`
// and does nothing until a harness has passed livetest.Require.
//
// The runner opens the same session backend the daemon uses, in process, for
// one configured account alias. Sign-in goes through that backend's normal,
// human-driven path: a visible provider browser or the system browser for
// OAuth, and the OS keyring or credential helper for standards routes. The
// runner never types, reads, or stores a credential.
//
// It then performs bounded reads and, only with CORRESYNC_LIVE_WRITE, one
// reversible synthetic write per service on a target the operator named, and
// emits a content-free record. See docs/evidence/README.md.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/livetest"
	"github.com/nkiyohara/corresync/internal/localipc"
)

const (
	liveRunTimeout     = 20 * time.Minute
	liveCleanupTimeout = 2 * time.Minute
	liveReadLimit      = 10
	liveFolderLimit    = 50
	liveMessagingLimit = 5
	liveLookupPages    = 5
	liveSyntheticBody  = "Synthetic item created by an opt-in Corresync live harness. It is safe to delete."
)

type liveRun struct {
	t        *testing.T
	optIn    livetest.OptIn
	backend  *sessionBackend
	account  domain.AccountID
	route    config.Account
	caller   domain.Caller
	record   *livetest.Record
	marker   string
	created  int
	verified int
}

func runLiveRoute(t *testing.T, optIn livetest.OptIn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), liveRunTimeout)
	defer cancel()

	commit, err := livetest.SourceRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := syntheticMarker()
	if err != nil {
		t.Fatal(err)
	}
	run := &liveRun{
		t: t, optIn: optIn,
		caller: domain.Caller{Surface: "cli", Instance: fmt.Sprintf("live-harness-%d", os.Getpid())},
		record: livetest.NewRecord(optIn, commit, time.Now()),
		marker: marker,
	}
	if err := run.refuseRunningDaemon(ctx); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(ctx, optIn.ConfigPath, os.Stderr, os.Stderr, buildinfo.Current())
	backend, err := newSessionBackend(app)
	if err != nil {
		t.Fatalf("open the session backend: %s", livetest.Classify(err))
	}
	run.backend = backend
	defer func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close the session backend: %s", livetest.Classify(err))
		}
	}()

	configured, exists := backend.configuration.Accounts[optIn.Account]
	if !exists {
		t.Fatalf("%s does not name an account in %s", livetest.EnvAccount, livetest.EnvConfig)
	}
	if err := matchLiveRoute(optIn.Route, configured); err != nil {
		t.Fatal(err)
	}
	run.account, run.route = configured.ID, configured
	defer run.emit()

	started := time.Now()
	_, err = backend.Login(ctx, run.account, run.caller)
	if run.record.Observe("session.authenticate", started, -1, false, err) == livetest.OutcomeFail {
		run.markRemaining(livetest.OutcomeUnobserved, "authentication-failed")
		return
	}
	run.observeSession(ctx)
	run.observeMail(ctx)
	run.observeCalendar(ctx)
	run.observeTasks(ctx)
	run.observeMessaging(ctx)
	switch {
	case run.created == 0:
		run.record.Cleanup = livetest.CleanupNotApplicable
	case run.verified == run.created:
		run.record.Cleanup = livetest.CleanupConfirmed
	default:
		run.record.Cleanup = livetest.CleanupUnconfirmed
	}
}

// refuseRunningDaemon keeps the harness from becoming a second session owner
// next to a daemon already serving the same config.
func (run *liveRun) refuseRunningDaemon(ctx context.Context) error {
	endpoint, err := localipc.Resolve(run.optIn.ConfigPath)
	if err != nil {
		return nil
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return nil
	}
	defer func() { _ = client.Close() }()
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := client.InspectOwner(probe, run.caller); err == nil {
		return errors.New("a daemon is serving this config; stop it with `corr --config <path> daemon stop` first")
	}
	return nil
}

func (run *liveRun) emit() {
	rendered, err := run.record.Markdown()
	if err != nil {
		run.t.Errorf("render the observation record: %v", err)
		return
	}
	run.t.Logf("content-free observation record (suggested docs/evidence/%s):\n\n%s",
		run.record.SuggestedFileName(), rendered)
	if run.optIn.RecordPath != "" {
		if err := run.record.WriteFile(run.optIn.RecordPath); err != nil {
			run.t.Errorf("write %s: %v", livetest.EnvRecord, err)
		}
	}
	if run.record.Cleanup == livetest.CleanupUnconfirmed {
		run.t.Errorf("cleanup could not be confirmed: remove any item titled %q from the named test target by hand; do not rerun a write until it is gone", run.marker)
	}
	if run.record.Result() == livetest.OutcomeFail {
		run.t.Error("the observation recorded a failure; the record above is still content-free and may be committed as a failed observation")
	}
}

func (run *liveRun) markRemaining(outcome livetest.Outcome, note string) {
	for _, service := range []struct {
		configured bool
		operation  string
	}{
		{run.route.Mail != nil, "mail"},
		{run.route.Calendar != nil, "calendar"},
		{run.route.Tasks != nil, "tasks"},
		{run.route.Messages != nil, "messages"},
	} {
		if service.configured {
			run.record.Mark(service.operation, outcome, note)
		}
	}
}

func (run *liveRun) observeSession(ctx context.Context) {
	started := time.Now()
	status, err := run.backend.SessionStatus(ctx, run.caller)
	if err != nil {
		run.record.Observe("session.status", started, -1, false, err)
		return
	}
	for _, account := range status.Accounts {
		if account.Account != run.account {
			continue
		}
		if account.Capabilities != nil {
			run.record.AddCapabilities(*account.Capabilities)
		}
		run.record.AddDegradations(account.Degradations)
		run.record.Observe("session.status", started, -1, len(account.Degradations) != 0, nil)
		return
	}
	run.record.Mark("session.status", livetest.OutcomeFail, livetest.NoteMissingResult)
}

// matchLiveRoute requires every active service of the alias to belong to the
// confirmed route, so one run exercises exactly one route.
func matchLiveRoute(info livetest.RouteInfo, account config.Account) error {
	matches := 0
	check := func(service string, configured bool, provider, expected string) error {
		switch {
		case !configured:
			return nil
		case expected == "" || provider != expected:
			return fmt.Errorf("the alias has an active %s service outside route %s; use an alias with only this route or disable the other services", service, info.ID)
		default:
			matches++
			return nil
		}
	}
	messagingProvider := ""
	if account.Messages != nil {
		switch info.MessagingRoute {
		case domain.MessagingRouteTeamsGraph:
			if account.Messages.TeamsGraph != nil {
				messagingProvider = string(account.Messages.Provider)
			}
		case domain.MessagingRouteTeamsWeb:
			if account.Messages.TeamsWeb != nil {
				messagingProvider = string(account.Messages.Provider)
			}
		case domain.MessagingRouteMattermost:
			if account.Messages.Mattermost != nil {
				messagingProvider = string(account.Messages.Provider)
			}
		case domain.MessagingRouteSlackAPI:
		}
	}
	if err := errors.Join(
		check("mail", account.Mail != nil, string(account.MailProvider()), string(info.Mail)),
		check("calendar", account.Calendar != nil, string(account.CalendarProvider()), string(info.Calendar)),
		check("tasks", account.Tasks != nil, string(account.TaskProvider()), string(info.Tasks)),
		check("messages", account.Messages != nil, messagingProvider, string(info.Messaging)),
	); err != nil {
		return err
	}
	if matches == 0 {
		return fmt.Errorf("the alias has no active service on route %s", info.ID)
	}
	return nil
}

func (run *liveRun) observeMail(ctx context.Context) {
	if run.optIn.Route.Mail == "" {
		return
	}
	if run.route.Mail == nil {
		run.record.Mark("mail", livetest.OutcomeUnobserved, livetest.NoteNotConfigured)
		return
	}
	started := time.Now()
	folders, err := run.backend.ListMailFolders(ctx, application.MailFolderListInput{
		Account: run.account,
		Parent: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "msgfolderroot",
		},
		Traversal: application.MailFolderTraversalDeep,
		Limit:     liveFolderLimit,
		TimeZone:  "UTC",
	}, run.caller)
	run.record.Observe("mail.folders.list", started, len(folders.Folders), false, err)

	started = time.Now()
	inbox, err := run.backend.ListMail(ctx, application.MailListInput{
		Account: run.account,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit:    liveReadLimit,
		TimeZone: "UTC",
	}, run.caller)
	run.record.Observe("mail.messages.list", started, len(inbox.Messages), false, err)

	switch {
	case !run.optIn.Write:
		run.record.Mark("mail.draft.write", livetest.OutcomeSkipped, livetest.NoteWritesNotEnabled)
	case !run.optIn.Route.MailWrite:
		run.record.Mark("mail.draft.write", livetest.OutcomeSkipped, livetest.NoteNoReversibleWrite)
	default:
		run.writeMailDraft(ctx)
	}
}

func (run *liveRun) writeMailDraft(ctx context.Context) {
	started := time.Now()
	access, err := run.backend.CreateMailDraft(ctx, application.MailDraftInput{
		Account: run.account,
		Subject: run.marker,
		Body:    liveSyntheticBody,
	}, run.caller)
	if err == nil {
		access, err = commitPreview(access, access.Preview, func(token string) (application.MailDraftAccess, error) {
			return run.backend.CommitMailDraft(ctx, token, run.caller)
		})
	}
	if err == nil && access.Draft == nil {
		err = errors.New("draft commit returned no draft")
	}
	run.record.Observe("mail.draft.create", started, -1, false, err)
	if err != nil {
		if errors.Is(err, application.ErrWriteOutcomeUnknown) {
			run.created++
		}
		return
	}
	run.created++
	draftID, changeKey := access.Draft.ID, access.Draft.ChangeKey

	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	if summary, found, _ := run.findDraft(cleanup, draftID); found {
		changeKey = summary.ChangeKey
	}
	started = time.Now()
	deleted, err := run.backend.DeleteMail(cleanup, application.MailDeleteInput{
		Account: run.account, MessageID: draftID, ChangeKey: changeKey,
	}, run.caller)
	if err == nil {
		_, err = commitPreview(deleted, deleted.Preview, func(token string) (application.MailDeleteAccess, error) {
			return run.backend.CommitMailDelete(cleanup, token, run.caller)
		})
	}
	run.record.Observe("mail.draft.delete", started, -1, false, err)

	started = time.Now()
	_, found, complete := run.findDraft(cleanup, draftID)
	run.recordAbsence("mail.draft.absent", started, found, complete)
}

// findDraft pages the Drafts folder for one exact item. complete is false
// when the bounded pages ended before the folder did or a read failed.
func (run *liveRun) findDraft(ctx context.Context, id string) (application.MailSummary, bool, bool) {
	for page := range liveLookupPages {
		result, err := run.backend.ListMail(ctx, application.MailListInput{
			Account: run.account,
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished, ID: "drafts",
			},
			Offset:   page * application.MaxMailPageSize,
			Limit:    application.MaxMailPageSize,
			TimeZone: "UTC",
		}, run.caller)
		if err != nil {
			return application.MailSummary{}, false, false
		}
		for _, message := range result.Messages {
			if message.ID == id {
				return message, true, true
			}
		}
		if result.IncludesLastItem || len(result.Messages) == 0 {
			return application.MailSummary{}, false, true
		}
	}
	return application.MailSummary{}, false, false
}

func (run *liveRun) observeCalendar(ctx context.Context) {
	if run.optIn.Route.Calendar == "" {
		return
	}
	if run.route.Calendar == nil {
		run.record.Mark("calendar", livetest.OutcomeUnobserved, livetest.NoteNotConfigured)
		return
	}
	started := time.Now()
	calendars, err := run.backend.ListCalendarFolders(ctx, application.CalendarFolderListInput{
		Account: run.account, Limit: application.MaxCalendarFolderPageSize,
	}, run.caller)
	run.record.Observe("calendar.calendars.list", started, len(calendars.Calendars), false, err)

	now := time.Now().UTC().Truncate(time.Second)
	started = time.Now()
	events, err := run.backend.ListCalendar(ctx, application.CalendarListInput{
		Account: run.account,
		Calendar: application.CalendarFolder{
			Kind: application.CalendarFolderDistinguished, ID: "calendar",
		},
		Start: now.Format(time.RFC3339),
		End:   now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}, run.caller)
	run.record.Observe("calendar.events.list", started, len(events.Events), false, err)

	if !run.optIn.Write {
		run.record.Mark("calendar.event.write", livetest.OutcomeSkipped, livetest.NoteWritesNotEnabled)
		return
	}
	if run.optIn.CalendarName == "" {
		run.record.Mark("calendar.event.write", livetest.OutcomeSkipped, livetest.NoteTargetNotNamed)
		return
	}
	target, note := selectCalendar(calendars.Calendars, run.optIn.CalendarName)
	if note != "" {
		run.record.Mark("calendar.event.write", livetest.OutcomeSkipped, note)
		return
	}
	run.writeCalendarEvent(ctx, application.CalendarFolder{
		Kind: application.CalendarFolderOpaque, ID: target.ID,
	})
}

func selectCalendar(calendars []application.CalendarFolderSummary, name string) (application.CalendarFolderSummary, string) {
	var matches []application.CalendarFolderSummary
	for _, calendar := range calendars {
		if calendar.DisplayName == name {
			matches = append(matches, calendar)
		}
	}
	switch {
	case len(matches) == 0:
		return application.CalendarFolderSummary{}, livetest.NoteTargetNotFound
	case len(matches) > 1:
		return application.CalendarFolderSummary{}, livetest.NoteTargetAmbiguous
	case !matches[0].CanEdit:
		return application.CalendarFolderSummary{}, livetest.NoteTargetReadOnly
	default:
		return matches[0], ""
	}
}

func (run *liveRun) writeCalendarEvent(ctx context.Context, calendar application.CalendarFolder) {
	start := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour)
	end := start.Add(15 * time.Minute)
	started := time.Now()
	access, err := run.backend.CreateCalendar(ctx, application.CalendarCreateInput{
		Account:  run.account,
		Calendar: calendar,
		Subject:  run.marker,
		Body:     liveSyntheticBody,
		Start:    start.Format(time.RFC3339),
		End:      end.Format(time.RFC3339),
		TimeZone: "UTC",
	}, run.caller)
	if err == nil {
		access, err = commitPreview(access, access.Preview, func(token string) (application.CalendarCreateAccess, error) {
			return run.backend.CommitCalendarCreate(ctx, token, run.caller)
		})
	}
	if err == nil && access.Created == nil {
		err = errors.New("calendar commit returned no event")
	}
	run.record.Observe("calendar.event.create", started, -1, false, err)
	if err != nil {
		if errors.Is(err, application.ErrWriteOutcomeUnknown) {
			run.created++
		}
		return
	}
	run.created++
	eventID, changeKey := access.Created.ID, access.Created.ChangeKey

	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	window := application.CalendarListInput{
		Account: run.account, Calendar: calendar,
		Start: start.Add(-time.Hour).Format(time.RFC3339),
		End:   end.Add(time.Hour).Format(time.RFC3339),
	}
	if page, err := run.backend.ListCalendar(cleanup, window, run.caller); err == nil {
		for _, event := range page.Events {
			if event.ID == eventID && event.ChangeKey != "" {
				changeKey = event.ChangeKey
			}
		}
	}
	started = time.Now()
	cancelled, err := run.backend.CancelCalendar(cleanup, application.CalendarCancelInput{
		Account: run.account, EventID: eventID, ChangeKey: changeKey,
	}, run.caller)
	if err == nil {
		_, err = commitPreview(cancelled, cancelled.Preview, func(token string) (application.CalendarCancelAccess, error) {
			return run.backend.CommitCalendarCancel(cleanup, token, run.caller)
		})
	}
	run.record.Observe("calendar.event.cancel", started, -1, false, err)

	started = time.Now()
	page, err := run.backend.ListCalendar(cleanup, window, run.caller)
	found := false
	for _, event := range page.Events {
		found = found || event.ID == eventID
	}
	run.recordAbsence("calendar.event.absent", started, found, err == nil)
}

func (run *liveRun) observeTasks(ctx context.Context) {
	if run.optIn.Route.Tasks == "" {
		return
	}
	if run.route.Tasks == nil {
		run.record.Mark("tasks", livetest.OutcomeUnobserved, livetest.NoteNotConfigured)
		return
	}
	started := time.Now()
	lists, err := run.backend.ListTaskLists(ctx, application.TaskListInput{
		Account: run.account, Limit: application.MaxTaskPageSize,
	}, run.caller)
	run.record.Observe("tasks.lists.list", started, len(lists.Lists), false, err)
	for _, list := range lists.Lists {
		run.record.AddDegradations(list.Degradations)
	}
	if err == nil && len(lists.Lists) == 0 {
		run.record.Mark("tasks.tasks.list", livetest.OutcomeUnobserved, livetest.NoteEmpty)
	} else if err == nil {
		started = time.Now()
		tasks, err := run.backend.ListTasks(ctx, application.TaskReadInput{
			Account: run.account, ListID: lists.Lists[0].ID, Limit: liveReadLimit,
		}, run.caller)
		run.record.Observe("tasks.tasks.list", started, len(tasks.Tasks), false, err)
	}

	if !run.optIn.Write {
		run.record.Mark("tasks.task.write", livetest.OutcomeSkipped, livetest.NoteWritesNotEnabled)
		return
	}
	if run.optIn.TaskListName == "" {
		run.record.Mark("tasks.task.write", livetest.OutcomeSkipped, livetest.NoteTargetNotNamed)
		return
	}
	var matches []application.TaskList
	for _, list := range lists.Lists {
		if list.DisplayName == run.optIn.TaskListName {
			matches = append(matches, list)
		}
	}
	switch {
	case len(matches) == 0:
		run.record.Mark("tasks.task.write", livetest.OutcomeSkipped, livetest.NoteTargetNotFound)
	case len(matches) > 1:
		run.record.Mark("tasks.task.write", livetest.OutcomeSkipped, livetest.NoteTargetAmbiguous)
	case !matches[0].Editable:
		run.record.Mark("tasks.task.write", livetest.OutcomeSkipped, livetest.NoteTargetReadOnly)
	default:
		run.writeTask(ctx, matches[0].ID)
	}
}

func (run *liveRun) writeTask(ctx context.Context, listID string) {
	started := time.Now()
	access, err := run.backend.CreateTask(ctx, application.TaskCreateInput{
		Account: run.account, ListID: listID,
		Title: run.marker, Notes: liveSyntheticBody,
		Priority: application.TaskPriorityNone,
	}, run.caller)
	if err == nil {
		access, err = commitPreview(access, access.Preview, func(token string) (application.TaskWriteAccess, error) {
			return run.backend.CommitTaskCreate(ctx, token, run.caller)
		})
	}
	if err == nil && access.Task == nil {
		err = errors.New("task commit returned no task")
	}
	run.record.Observe("tasks.task.create", started, -1, false, err)
	if err != nil {
		if errors.Is(err, application.ErrWriteOutcomeUnknown) {
			run.created++
		}
		return
	}
	run.created++
	task := *access.Task

	cleanup, cancel := cleanupContext(ctx)
	defer cancel()
	started = time.Now()
	deleted, err := run.backend.DeleteTask(cleanup, application.TaskDeleteInput{
		Account: run.account, ListID: task.ListID, TaskID: task.ID, Version: task.Version,
	}, run.caller)
	if err == nil {
		_, err = commitPreview(deleted, deleted.Preview, func(token string) (application.TaskWriteAccess, error) {
			return run.backend.CommitTaskDelete(cleanup, token, run.caller)
		})
	}
	run.record.Observe("tasks.task.delete", started, -1, false, err)

	started = time.Now()
	found, complete := false, false
	for page := range liveLookupPages {
		result, err := run.backend.ListTasks(cleanup, application.TaskReadInput{
			Account: run.account, ListID: task.ListID,
			Offset: page * application.MaxTaskPageSize, Limit: application.MaxTaskPageSize,
		}, run.caller)
		if err != nil {
			break
		}
		for _, candidate := range result.Tasks {
			found = found || candidate.ID == task.ID
		}
		if found || !result.HasMore {
			complete = true
			break
		}
	}
	run.recordAbsence("tasks.task.absent", started, found, complete)
}

func (run *liveRun) observeMessaging(ctx context.Context) {
	if run.optIn.Route.Messaging == "" {
		return
	}
	if run.route.Messages == nil {
		run.record.Mark("messages", livetest.OutcomeUnobserved, livetest.NoteNotConfigured)
		return
	}
	workspace := messagingWorkspace(run.route.Messages)
	started := time.Now()
	conversations, err := run.backend.ListConversations(ctx, application.ConversationListInput{
		Account: run.account, WorkspaceID: workspace, Limit: liveMessagingLimit,
	}, run.caller)
	run.record.Observe("messages.conversations.list", started, len(conversations.Conversations), conversations.Partial, err)
	run.record.AddDegradations(conversations.Degradations)
	if err == nil && len(conversations.Conversations) == 0 {
		run.record.Mark("messages.messages.list", livetest.OutcomeUnobserved, livetest.NoteEmpty)
	} else if err == nil {
		started = time.Now()
		messages, err := run.backend.ListMessages(ctx, application.MessageListInput{
			Account: run.account, WorkspaceID: workspace,
			ConversationID: conversations.Conversations[0].ID, Limit: liveMessagingLimit,
		}, run.caller)
		run.record.Observe("messages.messages.list", started, len(messages.Messages), messages.Partial, err)
		run.record.AddDegradations(messages.Degradations)
	}
	// A posted message notifies other members and a deleted one leaves a
	// visible trace, so no messaging write is reversible.
	run.record.Mark("messages.message.write", livetest.OutcomeSkipped, livetest.NoteNoReversibleWrite)
}

func messagingWorkspace(route *config.MessagingRoute) string {
	switch {
	case route.TeamsGraph != nil:
		return route.TeamsGraph.WorkspaceID
	case route.TeamsWeb != nil:
		return route.TeamsWeb.WorkspaceID
	case route.Mattermost != nil:
		return route.Mattermost.WorkspaceID
	default:
		return ""
	}
}

// recordAbsence turns a lookup after cleanup into the evidence that the
// synthetic item is gone. Only a complete lookup that did not find it counts.
func (run *liveRun) recordAbsence(operation string, started time.Time, found, complete bool) {
	switch {
	case found:
		run.record.Mark(operation, livetest.OutcomeFail, livetest.NoteStillPresent)
	case !complete:
		run.record.Mark(operation, livetest.OutcomeUnobserved, livetest.NoteAbsenceUnverified)
	default:
		run.record.Observe(operation, started, -1, false, nil)
		run.verified++
	}
}

// commitPreview commits the exact preview the harness itself just prepared
// for its own synthetic item. The operator approved it in advance by setting
// CORRESYNC_LIVE_WRITE for this route and alias.
func commitPreview[T any](access T, preview *approval.Preview, commit func(string) (T, error)) (T, error) {
	if preview == nil {
		return access, nil
	}
	return commit(preview.Token)
}

// cleanupContext keeps cleanup bounded even when the run's own deadline has
// already passed, so a created item is never abandoned for lack of time.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), liveCleanupTimeout)
}

func syntheticMarker() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "Corresync live harness " + hex.EncodeToString(random), nil
}
