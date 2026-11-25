package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

func parseEverydayCommandLine(t *testing.T, words []string) *kong.Context {
	t.Helper()
	var commandLine cli
	parser, err := kong.New(
		&commandLine,
		kong.Name(commandName),
		kong.Exit(func(int) { t.Fatalf("parsing %q exited", words) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	context, err := parser.Parse(words)
	if err != nil {
		t.Fatalf("parse %q: %v", words, err)
	}
	return context
}

func TestEverydayHelpExamplesParseAndAreShown(t *testing.T) {
	t.Setenv("CORRESYNC_CONFIG", "")
	for _, group := range []struct {
		help     string
		examples []helpExample
	}{
		{mailCommand{}.Help(), mailHelpExamples},
		{calendarCommand{}.Help(), calendarHelpExamples},
		{calendarCreateCommand{}.Help(), calendarCreateHelpExamples},
		{calendarUpdateCommand{}.Help(), calendarUpdateHelpExamples},
		{agendaCommand{}.Help(), agendaHelpExamples},
		{tasksCommand{}.Help(), tasksHelpExamples},
		{queriesCommand{}.Help(), queriesHelpExamples},
	} {
		for _, example := range group.examples {
			parseEverydayCommandLine(t, example.words)
			if !strings.Contains(group.help, "\n    "+example.command()+"\n") &&
				!strings.HasSuffix(group.help, "\n    "+example.command()) {
				t.Errorf("help does not show %q on its own line:\n%s", example.command(), group.help)
			}
			for _, word := range example.words {
				if strings.Contains(word, "@") && !strings.HasSuffix(strings.TrimPrefix(word, "from:"), "@example.invalid") {
					t.Errorf("example address %q is not under example.invalid", word)
				}
			}
		}
	}
}

func TestApprovalCommandRepeatsTheSameArguments(t *testing.T) {
	t.Setenv("CORRESYNC_CONFIG", "")
	for _, test := range []struct {
		path  []string
		words []string
	}{
		{[]string{"mail", "send"}, []string{
			"--account", "work", "--to", "ana@example.invalid", "--to", "ben@example.invalid",
			"--cc", "cy@example.invalid", "--subject", "It's done; really", "--body-file", "-",
			"--body-format", "html", "--attachment", "report.pdf",
		}},
		{[]string{"mail", "draft"}, []string{
			"--mode", "reply-all", "--reference-message-id", "AAMk=", "--reference-change-key", "CQA+/=",
			"--body-file", "reply.txt",
		}},
		{[]string{"mail", "move"}, []string{"--message-id", "m-1", "--change-key", "k-1", "--destination-id", "f-1"}},
		{[]string{"mail", "mark"}, []string{"--message-id", "m-1", "--change-key", "k-1", "--state", "unread"}},
		{[]string{"mail", "delete"}, []string{"--message-id=-m-1", "--change-key", "k 1"}},
		{[]string{"mail", "body"}, []string{"--message-id", "m-1"}},
		{[]string{"mail", "send-draft"}, []string{"--draft-id", "d-1", "--draft-change-key", "dk-1"}},
		{[]string{"calendar", "create"}, []string{
			"--subject", "Team sync", "--start", "2026-12-02T10:00:00Z", "--end", "2026-12-02T11:00:00Z",
			"--required-attendee", "ana@example.invalid", "--online-meeting", "--reminder-minutes", "0",
			"--recurrence", "weekly", "--recurrence-day", "Wednesday", "--recurrence-count", "10",
			"--time-zone", "W. Europe Standard Time",
		}},
		{[]string{"calendar", "update"}, []string{
			"--event-id", "e-1", "--change-key", "k-1", "--all-day", "false", "--clear-location",
			"--disable-reminder", "--recurrence", "none",
		}},
		{[]string{"calendar", "cancel"}, []string{"--event-id", "e-1", "--change-key", "k-1"}},
		{[]string{"tasks", "complete"}, []string{"--list-id", "l-1", "--task-id", "t-1", "--task-version", `W/"1"`}},
		{[]string{"tasks", "create"}, []string{"--file", "-", "--account", "home"}},
		{[]string{"queries", "save", "mail"}, []string{"--folder", "archive", "--", "from-ana", "-from:ana@example.invalid"}},
		{[]string{"queries", "save", "calendar"}, []string{"next-week", "--window", "2h0m0s", "--start-offset", "30m"}},
		{[]string{"queries", "delete"}, []string{"from-ana"}},
		{[]string{"queries", "purge"}, []string{"--account", "home"}},
	} {
		t.Run(strings.Join(test.path, " "), func(t *testing.T) {
			typed := parseEverydayCommandLine(t, append(append([]string{}, test.path...), test.words...))
			want := typed.Selected().Target
			app := &runtime{}
			approval := app.approvalWords(want.Addr().Interface(), test.path...)
			if approval[0] != commandName || !slices.Contains(approval, "--approve") {
				t.Fatalf("approval command = %q", approval)
			}
			approved := parseEverydayCommandLine(t, approval[1:]).Selected().Target
			want.FieldByName("Approve").SetBool(true)
			if !reflect.DeepEqual(approved.Interface(), want.Interface()) {
				t.Errorf("approval command %q parses to\n%#v\nwant\n%#v", approval, approved.Interface(), want.Interface())
			}
		})
	}
}

func TestApprovalCommandAddsConfigAndRefusesControls(t *testing.T) {
	app := &runtime{configPath: "/tmp/other config.toml", lookupEnv: func(string) (string, bool) { return "", false }}
	got := app.approvalCommand(&mailMarkCommand{MessageID: "m-1", ChangeKey: "k-1", State: "read"}, "mail", "mark")
	want := "corr mail mark --message-id m-1 --change-key k-1 --state read --config '/tmp/other config.toml' --approve"
	if got != want {
		t.Fatalf("approvalCommand() = %q, want %q", got, want)
	}
	app.lookupEnv = func(string) (string, bool) { return "/tmp/other config.toml", true }
	if got := app.approvalCommand(&mailBodyCommand{MessageID: "m-1"}, "mail", "body"); strings.Contains(got, "--config") {
		t.Fatalf("approvalCommand() repeated the config from the environment: %q", got)
	}
	got = app.approvalCommand(&mailSendCommand{Subject: "Hi\x1b[2J", To: []string{"ana@example.invalid"}}, "mail", "send")
	if strings.ContainsRune(got, '\x1b') || got != "run the same command again with --approve added" {
		t.Fatalf("approvalCommand() with a control character = %q", got)
	}
}

func TestEverydayPreviewsEndWithTheApprovalCommand(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	app, stdout, stderr := newEverydayTestRuntime(t)
	for _, test := range []struct {
		command everydayRunner
		want    []string
	}{
		{&mailSendCommand{To: []string{"ana@example.invalid"}, Subject: "Lunch", BodyFormat: "text", Mode: "new"}, []string{
			"Preview: this message has not been sent.", "Will send: yes", "To: ana@example.invalid", "Bcc: (none)",
			"Body size: 0 bytes (SHA-256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855)",
			"Next: corr mail send --to ana@example.invalid --subject Lunch --config",
		}},
		{&mailDraftCommand{To: []string{"ana@example.invalid"}, BodyFormat: "text", Mode: "new"}, []string{
			"Preview: no draft has been saved.", "Will send: no", "Next: corr mail draft --to ana@example.invalid",
		}},
		{&calendarCreateCommand{
			Subject: "Planning", Start: "2026-12-01T09:00:00Z", End: "2026-12-01T10:00:00Z",
			RequiredAttendees: []string{"ana@example.invalid"}, Recurrence: "none", RecurrenceInterval: 1,
		}, []string{
			"Preview: no event has been created.", "Sends invitations: yes", "Required: ana@example.invalid",
			"Next: corr calendar create --subject Planning --start 2026-12-01T09:00:00Z",
		}},
		{&taskCompleteCommand{ListID: "list-1", TaskID: "task-1", TaskVersion: "task-version-1"}, []string{
			"Preview: the task has not been marked completed.", "Task ID: task-1", `"taskId": "task-1"`,
			"Next: corr tasks complete --list-id list-1 --task-id task-1 --task-version task-version-1 --config",
		}},
	} {
		stdout.Reset()
		if err := test.command.Run(app); err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(stdout.String()), " ")
		for _, want := range test.want {
			if !strings.Contains(text, want) {
				t.Errorf("preview is missing %q:\n%s", want, stdout.String())
			}
		}
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		if last := lines[len(lines)-1]; !strings.HasPrefix(last, "Next: corr ") || !strings.HasSuffix(last, " --approve") {
			t.Errorf("preview does not end with the approval command: %q", last)
		}
	}

	stdout.Reset()
	stderr.Reset()
	approved := mailSendCommand{
		To: []string{"ana@example.invalid"}, Subject: "Lunch", BodyFormat: "text", Mode: "new", Approve: true,
	}
	if err := approved.Run(app); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Sent the message to 1 recipient. The request was made once and is not retried.\n" ||
		!strings.HasPrefix(stderr.String(), "Sending this message now:\n") ||
		!strings.Contains(stderr.String(), "To:           ana@example.invalid\n") ||
		strings.Contains(stderr.String(), "Next:") {
		t.Fatalf("approved send stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestEverydayEmptyResultsSayWhatWasSearched(t *testing.T) {
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	if err := writeMailTable(app, application.MailPage{}, emptyResult{
		message: "No messages in inbox in account work match \"plan\".",
		next:    printableCommand("corr", "mail", "search", "--all-accounts", "--query", "plan"),
	}); err != nil {
		t.Fatal(err)
	}
	want := "No messages in inbox in account work match \"plan\".\nNext: corr mail search --all-accounts --query plan\n"
	if stdout.String() != want {
		t.Fatalf("empty result = %q, want %q", stdout.String(), want)
	}
	stdout.Reset()
	if err := writeMailTable(app, application.MailPage{}, emptyResult{
		message: "No messages.",
		next:    printableCommand("corr", "mail", "search", "--query", "a\x1b[2J"),
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "No messages.\n" {
		t.Fatalf("a suggestion with a control character was shown: %q", stdout.String())
	}
}

func TestEverydayTablesExplainTheirFlags(t *testing.T) {
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	if err := writeMailTable(app, application.MailPage{Messages: []application.MailSummary{{
		ID: "message-1", HasAttachments: true,
	}}}, emptyResult{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "MESSAGE ID") || !strings.Contains(stdout.String(), "Flags: U unread, A has attachments.") {
		t.Fatalf("mail table = %q", stdout.String())
	}
	stdout.Reset()
	if err := writeMailTable(app, application.MailPage{Messages: []application.MailSummary{{ID: "m", IsRead: true}}}, emptyResult{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "Flags:") {
		t.Fatalf("mail table without flags printed a legend: %q", stdout.String())
	}
}

func TestProjectionNoticesNameMissingAccounts(t *testing.T) {
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateSignedOut, application.AuthenticationReasonNeverAuthenticated,
		"acc_00000000000000000000000000000002", "home", application.AuthenticationServiceMail, domain.ProviderGoogle,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProjectionNotices(app, nil, []application.ProjectionFailure{{
		Alias: "home", Provider: domain.ProviderGoogle, Reason: "Synthetic sign-in needed.", Authentication: &action,
	}}); err != nil {
		t.Fatal(err)
	}
	want := "!  Missing results from home (google): Synthetic sign-in needed.\nNext: corr auth login --account home\n"
	if stdout.String() != want {
		t.Fatalf("notices = %q, want %q", stdout.String(), want)
	}
}

func TestEverydayErrorsCarryNextSteps(t *testing.T) {
	configuration := config.OutlookDefault()
	if _, _, err := resolveAccount(configuration, "missing"); err == nil {
		t.Fatal("resolveAccount accepted an unknown alias")
	} else if hint, _ := errorHint(err); hint != "corr account list" || err.Error() != `account "missing" is not configured` {
		t.Fatalf("unknown alias error = %q, hint %q", err, hint)
	}
	if _, _, err := resolveAccount(config.Config{}, ""); err == nil {
		t.Fatal("resolveAccount accepted an empty configuration")
	} else if hint, _ := errorHint(err); hint != "corr setup" {
		t.Fatalf("no-account hint = %q", hint)
	}

	agenda := agendaListCommand{Start: "2026-12-01T00:00:00Z", End: "2026-12-02T00:00:00Z", TimeZone: "UTC", Limit: 50}
	err := agenda.Run(&runtime{})
	if hint, _ := errorHint(err); hint != "corr agenda list --all-accounts --start 2026-12-01T00:00:00Z --end 2026-12-02T00:00:00Z" {
		t.Fatalf("agenda hint = %q (error %v)", hint, err)
	}

	draft := mailDraftCommand{
		To: []string{"ana@example.invalid"}, Mode: "reply", BodyFormat: "text", BodyFile: "reply.txt",
		ReferenceMessageID: "AAMk-1", ReferenceChangeKey: "CQA-1",
	}
	input := application.MailDraftInput{
		Account: "acc_00000000000000000000000000000001", To: draft.To,
		ComposeMode: application.MailComposeReply, ReferenceMessageID: "AAMk-1", ReferenceChangeKey: "CQA-1",
	}
	validation := input.Validate(50)
	if validation == nil {
		t.Fatal("reply draft with recipients validated")
	}
	hint, _ := errorHint(draft.explainValidation(validation, input, 50))
	if hint != "corr mail draft --body-file reply.txt --mode reply-all --reference-message-id AAMk-1 --reference-change-key CQA-1" {
		t.Fatalf("reply draft hint = %q", hint)
	}

	send := mailSendCommand{Mode: "reply-all", BodyFormat: "text", ReferenceMessageID: "AAMk-1", ReferenceChangeKey: "CQA-1"}
	refused := &daemonapi.Error{Code: "operation_failed", Message: "reply and reply-all sends require a saved draft recipient review"}
	explained := send.explainSendFailure(refused)
	if hint, _ := errorHint(explained); hint != "corr mail draft --mode reply-all --reference-message-id AAMk-1 --reference-change-key CQA-1" ||
		explained.Error() != refused.Error() {
		t.Fatalf("reply send error = %q, hint %q", explained, hint)
	}
	if got := (&mailSendCommand{Mode: "new"}).explainSendFailure(refused); !sameError(got, refused) {
		t.Fatalf("new-message send failure gained a hint: %v", got)
	}
}

// sameError reports whether got is want passed through untouched: same
// identity, same text, and no hint.
func sameError(got, want error) bool {
	_, hinted := errorHint(got)
	return errors.Is(got, want) && got.Error() == want.Error() && !hinted
}

func TestSignInErrorsBecomeASentenceWithTheLoginCommand(t *testing.T) {
	action, err := application.NewAuthenticationActionRequired(
		application.AuthenticationStateReauthenticationNeeded, application.AuthenticationReasonSessionExpired,
		"acc_00000000000000000000000000000001", "work", application.AuthenticationServiceMail, domain.ProviderMicrosoftOWA,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	remote := &daemonapi.Error{Code: "operation_failed", Message: string(encoded)}
	failure := errors.Join(remote, errors.New("close"))
	if got := explainCommandError(failure, true); !sameError(got, failure) {
		t.Fatalf("--json run changed the error: %v", got)
	}
	explained := explainCommandError(failure, false)
	var stderr bytes.Buffer
	printCommandError(&stderr, explained, true)
	want := "Account work needs you to sign in before it can use mail (microsoft-owa): the sign-in has expired\n" +
		"Try: corr auth login --account work\n"
	if stderr.String() != want {
		t.Fatalf("printed error = %q, want %q", stderr.String(), want)
	}
	var unwrapped *daemonapi.Error
	if !errors.As(explained, &unwrapped) || unwrapped != remote {
		t.Fatal("explained error no longer wraps the daemon error")
	}
	other := &daemonapi.Error{Code: "operation_failed", Message: "provider unavailable"}
	if got := explainCommandError(other, false); !sameError(got, other) {
		t.Fatalf("unrelated daemon error changed: %v", got)
	}
}

func TestUnknownAccountPrintsTheAccountListCommand(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", path, "mail", "list", "--account", "missing"}, &stdout, &stderr)
	want := "Account \"missing\" is not configured\nTry: corr account list\n"
	if code != 1 || stderr.String() != want {
		t.Fatalf("code = %d, stderr = %q, want %q", code, stderr.String(), want)
	}
	stderr.Reset()
	code = run(context.Background(), []string{"--config", path, "mail", "list", "--account", "missing", "--json"}, &stdout, &stderr)
	if code != 1 || stderr.String() != "account \"missing\" is not configured\n" {
		t.Fatalf("--json code = %d, stderr = %q", code, stderr.String())
	}
}
