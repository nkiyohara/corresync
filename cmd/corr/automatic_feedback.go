package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/nkiyohara/corresync/internal/feedback"
	"github.com/nkiyohara/corresync/internal/paths"
)

const automaticFeedbackTimeout = 15 * time.Second

const automaticFeedbackConsentNotice = "Public feedback consent:\n" +
	"  • Your GitHub username and the generated issue will be public.\n" +
	"  • Included: Corresync version, OS/CPU, install method, command/flag names, and fixed error classes.\n" +
	"  • Excluded: raw errors, values, paths, accounts, credentials, mail, and calendar data.\n" +
	"  • Each build/error fingerprint is attempted once; disable this setting to stop future reports."

func (app *runtime) maybeSubmitAutomaticFeedback(
	executionContext context.Context,
	root string,
	record feedback.ErrorRecord,
) {
	if root == "feedback" || root == "config" || root == "settings" ||
		root == "integrations" || root == "mcp" || root == "completion" ||
		record.Command.Path == "corr daemon serve" ||
		slices.Contains(record.Command.Flags, "--json") || !app.interactiveOutput() ||
		slices.Contains(record.Classes, "canceled") {
		return
	}
	configuration, err := app.loadFeedbackConfig()
	if err != nil || !configuration.Feedback.AutoSubmit {
		return
	}
	build := feedback.Build{
		Version: app.info.Version, Commit: app.info.Commit,
		BuildDate: app.info.BuildDate, GoVersion: app.info.GoVersion,
		Platform: app.info.OS + "/" + app.info.Arch,
	}
	report, err := feedback.GenerateAutomatic(feedback.AutomaticInput{
		Build: build, InstallMethod: string(app.installMethod()), LastError: record,
	})
	if err != nil {
		app.writeAutomaticFeedbackFailure()
		return
	}
	markerDirectory, err := paths.FeedbackSubmissionDir()
	if err != nil {
		app.writeAutomaticFeedbackFailure()
		return
	}
	claimed, err := (feedback.SubmissionStore{Directory: markerDirectory}).Claim(build, record)
	if err != nil {
		app.writeAutomaticFeedbackFailure()
		return
	}
	if !claimed {
		return
	}
	body := automaticFeedbackIssueBody(report)
	ctx, cancel := context.WithTimeout(executionContext, automaticFeedbackTimeout)
	defer cancel()
	err = app.runInputCommand(
		ctx,
		bytes.NewReader(body),
		io.Discard,
		io.Discard,
		"gh",
		"issue", "create",
		"--repo", "github.com/nkiyohara/corresync",
		"--title", "Automatic Corresync error report "+record.ID,
		"--body-file", "-",
	)
	if err != nil {
		app.writeAutomaticFeedbackFailure()
		return
	}
	_, _ = fmt.Fprintf(
		app.stderr,
		"Submitted opt-in privacy-filtered error report %s to the public Corresync GitHub Issues.\n",
		record.ID,
	)
}

func automaticFeedbackIssueBody(report []byte) []byte {
	body := []byte(
		"## Automatic error report\n\n" +
			"Corresync submitted this public issue after the signed-in user explicitly enabled automatic feedback. " +
			"The payload is built from a closed allowlist; it contains no raw error, argument values, paths, " +
			"environment values, account identifiers, credentials, mail, or calendar content.\n\n" +
			"```json\n",
	)
	body = append(body, report...)
	body = append(body, []byte("```\n")...)
	return body
}

func writeAutomaticFeedbackConsent(writer io.Writer) error {
	_, err := fmt.Fprintln(writer, automaticFeedbackConsentNotice)
	return err
}

func (app *runtime) writeAutomaticFeedbackFailure() {
	_, _ = fmt.Fprintln(
		app.stderr,
		"Automatic feedback was not submitted; the original command error is unchanged. Run `corr feedback --last-error` to review the local report.",
	)
}

func validateAutomaticFeedbackPrerequisite(app *runtime) error {
	ctx, cancel := context.WithTimeout(app.context, 5*time.Second)
	defer cancel()
	if err := app.runCommand(
		ctx,
		io.Discard,
		io.Discard,
		"gh",
		"auth", "status", "--hostname", "github.com",
	); err != nil {
		return errors.New(
			"automatic feedback requires GitHub CLI (`gh`) signed in to github.com; Corresync never reads or stores its token",
		)
	}
	return nil
}
