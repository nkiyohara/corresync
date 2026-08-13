package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/feedback"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

func TestAutomaticFeedbackSubmitsAllowlistedIssueOnce(t *testing.T) {
	stateDirectory := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", stateDirectory)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	configuration.Feedback.AutoSubmit = true
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	app := newRuntime(
		context.Background(),
		configPath,
		io.Discard,
		stderr,
		buildinfo.Info{
			Version: "v0.8.6-rc.2", Commit: "0123456789abcdef",
			BuildDate: "2026-08-03T12:00:00Z", GoVersion: "go1.25.1",
			OS: "linux", Arch: "amd64",
		},
	)
	app.interactiveOutput = func() bool { return true }
	app.installMethod = func() updatecheck.InstallMethod { return updatecheck.InstallDirect }
	var calls int
	var submitted []byte
	app.runInputCommand = func(
		_ context.Context,
		stdin io.Reader,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		calls++
		if name != "gh" || !slices.Equal(arguments[:4], []string{
			"issue", "create", "--repo", "github.com/nkiyohara/corresync",
		}) || !slices.Contains(arguments, "--body-file") {
			return errors.New("unexpected GitHub CLI invocation")
		}
		var err error
		submitted, err = io.ReadAll(stdin)
		return err
	}
	record := feedback.NewErrorRecord(
		errors.New("Bearer ghp_Synthetic person@example.test /home/private"),
		"mail search",
		[]string{"--account", "person@example.test", "--query=private-subject"},
	)
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	if calls != 1 {
		t.Fatalf("GitHub CLI calls = %d, want 1", calls)
	}
	for _, want := range []string{
		"Automatic error report",
		`"submission": "automatic-opt-in"`,
		`"path": "corr mail search"`,
		`"--account"`,
		`"--query"`,
	} {
		if !bytes.Contains(submitted, []byte(want)) {
			t.Fatalf("submitted issue is missing %q:\n%s", want, submitted)
		}
	}
	for _, forbidden := range []string{
		"ghp_Synthetic", "person@example.test", "/home/private", "private-subject",
	} {
		if bytes.Contains(submitted, []byte(forbidden)) {
			t.Fatalf("submitted issue retained %q:\n%s", forbidden, submitted)
		}
	}
	if !strings.Contains(stderr.String(), "public Corresync GitHub Issues") {
		t.Fatalf("automatic feedback result was not explicit: %q", stderr.String())
	}
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	if calls != 1 {
		t.Fatalf("duplicate GitHub CLI calls = %d, want 1", calls)
	}
}

func TestAutomaticFeedbackIsDefaultOffAndInteractiveOnly(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(
		context.Background(), configPath, io.Discard, io.Discard, buildinfo.Current(),
	)
	var calls int
	app.runInputCommand = func(
		context.Context, io.Reader, io.Writer, io.Writer, string, ...string,
	) error {
		calls++
		return nil
	}
	record := feedback.NewErrorRecord(os.ErrNotExist, "mail list", nil)
	app.interactiveOutput = func() bool { return true }
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	configuration, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Feedback.AutoSubmit = true
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	app.interactiveOutput = func() bool { return false }
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	app.interactiveOutput = func() bool { return true }
	app.maybeSubmitAutomaticFeedback(t.Context(), "feedback", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "config", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "settings", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "integrations", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "mcp", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "completion", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "daemon", feedback.NewErrorRecord(
		os.ErrNotExist, "daemon serve", nil,
	))
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", feedback.NewErrorRecord(
		os.ErrNotExist, "mail list", []string{"--json"},
	))
	if calls != 0 {
		t.Fatalf("unexpected automatic feedback calls = %d", calls)
	}
}

func TestAutomaticFeedbackFailureIsFixedAndNotRetried(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	configuration.Feedback.AutoSubmit = true
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app := newRuntime(
		t.Context(), configPath, io.Discard, &stderr, buildinfo.Current(),
	)
	app.interactiveOutput = func() bool { return true }
	var calls int
	app.runInputCommand = func(
		context.Context, io.Reader, io.Writer, io.Writer, string, ...string,
	) error {
		calls++
		return errors.New("ghp_SyntheticPrivateToken")
	}
	record := feedback.NewErrorRecord(os.ErrPermission, "mail list", nil)
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	app.maybeSubmitAutomaticFeedback(t.Context(), "mail", record)
	if calls != 1 {
		t.Fatalf("GitHub CLI calls = %d, want 1", calls)
	}
	if !strings.Contains(stderr.String(), "Automatic feedback was not submitted") ||
		strings.Contains(stderr.String(), "ghp_SyntheticPrivateToken") {
		t.Fatalf("automatic feedback failure = %q", stderr.String())
	}
}

func TestAutomaticFeedbackPrerequisiteDoesNotExposeGitHubCredential(t *testing.T) {
	app := newRuntime(
		context.Background(), "", io.Discard, io.Discard, buildinfo.Current(),
	)
	app.runCommand = func(
		_ context.Context,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		if name != "gh" || !slices.Equal(arguments, []string{
			"auth", "status", "--hostname", "github.com",
		}) {
			return errors.New("unexpected prerequisite command")
		}
		return nil
	}
	if err := validateAutomaticFeedbackPrerequisite(app); err != nil {
		t.Fatal(err)
	}
	app.runCommand = func(context.Context, io.Writer, io.Writer, string, ...string) error {
		return errors.New("ghp_SyntheticPrivateToken")
	}
	err := validateAutomaticFeedbackPrerequisite(app)
	if err == nil || strings.Contains(err.Error(), "ghp_SyntheticPrivateToken") {
		t.Fatalf("prerequisite error = %v", err)
	}
}
