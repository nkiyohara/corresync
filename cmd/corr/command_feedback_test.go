package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

func TestFeedbackPrintsCompleteReportBeforeOfferingActions(t *testing.T) {
	t.Parallel()

	app, stdout := newFeedbackTestRuntime(t)
	if err := (&feedbackCommand{}).Run(app); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	output := stdout.String()
	reportPosition := strings.Index(output, `"schema_version": 1`)
	actionPosition := strings.Index(output, "Nothing was uploaded")
	if reportPosition < 0 || actionPosition < reportPosition {
		t.Fatalf("report was not shown before actions:\n%s", output)
	}
	for _, want := range []string{
		`"automatic_upload": false`,
		`"mail_or_calendar_content_included": false`,
		`"id": "microsoft-owa"`,
		"requires a GitHub account",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("feedback output is missing %q:\n%s", want, output)
		}
	}
	for _, sensitive := range []string{
		"person@example.test",
		"private-alias",
		"acc_00000000000000000000000000000001",
		"/Users/private/browser",
		"/Users/private/helper",
		"credential-lookup-key",
	} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("feedback output retained config value %q:\n%s", sensitive, output)
		}
	}
	if shouldOfferAutomaticUpdateNotice([]string{"feedback"}) {
		t.Fatal("feedback would perform an automatic network update check")
	}
}

func TestFeedbackCopyRunsOnlyAfterWritingReviewedReport(t *testing.T) {
	t.Parallel()

	app, stdout := newFeedbackTestRuntime(t)
	var copied []byte
	app.runInputCommand = func(
		_ context.Context,
		stdin io.Reader,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		if !strings.Contains(stdout.String(), `"schema_version": 1`) {
			return errors.New("action ran before report output")
		}
		if name != "xclip" || strings.Join(arguments, " ") != "-selection clipboard" {
			return errors.New("unexpected clipboard command")
		}
		var err error
		copied, err = io.ReadAll(stdin)
		return err
	}
	if err := (&feedbackCommand{Copy: true}).Run(app); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !bytes.Contains(copied, []byte(`"generation": "local-only"`)) {
		t.Fatalf("clipboard did not receive the redacted report: %s", copied)
	}
}

func TestFeedbackOpenGitHubOnlyOpensPrefilledPage(t *testing.T) {
	t.Parallel()

	app, stdout := newFeedbackTestRuntime(t)
	var opened string
	app.runInputCommand = func(
		_ context.Context,
		_ io.Reader,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		if !strings.Contains(stdout.String(), `"schema_version": 1`) {
			return errors.New("action ran before report output")
		}
		if name != "xdg-open" || len(arguments) != 1 {
			return errors.New("unexpected browser command")
		}
		opened = arguments[0]
		return nil
	}
	if err := (&feedbackCommand{OpenGitHub: true}).Run(app); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	parsed, err := url.Parse(opened)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if parsed.Scheme != "https" ||
		parsed.Host != "github.com" ||
		parsed.Path != "/nkiyohara/corresync/issues/new" {
		t.Fatalf("unexpected issue URL: %s", opened)
	}
	body := parsed.Query().Get("body")
	if !strings.Contains(body, `"automatic_upload": false`) ||
		strings.Contains(strings.ToLower(body), "authorization") {
		t.Fatalf("unsafe or incomplete prefilled body: %s", body)
	}
	if !strings.Contains(stdout.String(), "nothing was submitted") {
		t.Fatalf("GitHub limitation was not explicit:\n%s", stdout.String())
	}
}

func TestSaveFeedbackReportNeverOverwrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "feedback.json")
	if err := saveFeedbackReport(path, []byte("{}\n")); err != nil {
		t.Fatalf("saveFeedbackReport() error = %v", err)
	}
	if err := saveFeedbackReport(path, []byte(`{"replacement":true}`)); err == nil {
		t.Fatal("saveFeedbackReport() overwrote an existing file")
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- test-owned path.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "{}\n" {
		t.Fatalf("existing feedback report changed: %q", contents)
	}
}

func TestRunRecordsOnlySanitizedLastError(t *testing.T) {
	stateDirectory := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", stateDirectory)

	privateConfigPath := filepath.Join(t.TempDir(), "person@example.test", "config.toml")
	privateQuery := "from:person@example.test confidential-subject"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{
			"--config", privateConfigPath,
			"mail", "search",
			"--account", "private-account",
			"--query", privateQuery,
		},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("failing command code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(
		context.Background(),
		[]string{"--config", privateConfigPath, "feedback", "--last-error"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("feedback code = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, `"path": "corr mail search"`) ||
		!strings.Contains(output, `"--account"`) ||
		!strings.Contains(output, `"--query"`) {
		t.Fatalf("last-error shape is incomplete:\n%s", output)
	}
	for _, sensitive := range []string{
		privateConfigPath,
		privateQuery,
		"private-account",
		"person@example.test",
		"confidential-subject",
	} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("feedback retained sensitive input %q:\n%s", sensitive, output)
		}
	}
}

func newFeedbackTestRuntime(t *testing.T) (*runtime, *bytes.Buffer) {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	account := configuration.Accounts["work"]
	account.Address = "person@example.test"
	account.Mail.OutlookWeb.Mailbox = "person@example.test"
	account.Calendar.OutlookWeb.Mailbox = "person@example.test"
	delete(configuration.Accounts, "work")
	configuration.Accounts["private-alias"] = account
	configuration.DefaultAccount = "private-alias"
	configuration.Browser.Executable = "/Users/private/browser"
	configuration.Credentials.Helper = []string{
		"/Users/private/helper",
		"credential-lookup-key",
	}
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}
	stdout := &bytes.Buffer{}
	app := newRuntime(
		context.Background(),
		configPath,
		stdout,
		io.Discard,
		buildinfo.Info{
			Version: "v0.7.0", Commit: "0123456789abcdef",
			BuildDate: "2026-07-28T12:00:00Z", GoVersion: "go1.25.1",
			OS: "linux", Arch: "amd64",
		},
	)
	app.installMethod = func() updatecheck.InstallMethod {
		return updatecheck.InstallDirect
	}
	app.lookupEnv = func(string) (string, bool) { return "", false }
	return app, stdout
}
