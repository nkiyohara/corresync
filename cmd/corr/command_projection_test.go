package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestProjectionCommandsRequireExplicitReadOnlyScope(t *testing.T) {
	t.Parallel()

	app := newRuntime(
		context.Background(),
		"",
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	if err := (&agendaListCommand{
		Start:    "2026-07-28T00:00:00Z",
		End:      "2026-07-29T00:00:00Z",
		TimeZone: "UTC", Limit: 50,
	}).Run(app); err == nil || !strings.Contains(err.Error(), "--all-accounts") {
		t.Fatalf("agenda without explicit scope error = %v", err)
	}
	if err := (&mailSearchCommand{
		Account: "work", AllAccounts: true,
		Folder: "inbox", Query: "synthetic",
		Limit: 25, TimeZone: "UTC",
	}).Run(app); err == nil || !strings.Contains(err.Error(), "cannot select one account") {
		t.Fatalf("ambiguous mail projection error = %v", err)
	}
}

func TestMutationCLIHasNoAllAccountsFlag(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{
			"mail", "move", "--all-accounts",
			"--message-id", "message-1",
			"--change-key", "change-1",
		},
		&bytes.Buffer{},
		&stderr,
	)
	if code != 2 || !strings.Contains(stderr.String(), "unknown flag") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}
