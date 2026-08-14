package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestMessageSendContractFixtureUsesExplicitAccountRouting(t *testing.T) {
	t.Parallel()

	var input application.MessageSendInput
	if err := readMessageJSON(
		&runtime{},
		filepath.Join("..", "..", "testdata", "contracts", "message-send-v1.json"),
		&input,
	); err != nil {
		t.Fatal(err)
	}
	if input.Account != "" {
		t.Fatal("message fixture selected an account instead of leaving CLI routing explicit")
	}
	account := domain.AccountID("acc_00000000000000000000000000000001")
	if err := bindMessageInputAccount(&input, account); err != nil {
		t.Fatal(err)
	}
	if input.Account != account {
		t.Fatalf("bound account = %q, want %q", input.Account, account)
	}
	if err := input.Validate(); err != nil {
		t.Fatalf("fixture validation error = %v", err)
	}

	unknown := bytes.NewBufferString(`{"workspaceId":"workspace-1","providerAction":{}}`)
	if err := readMessageJSON(
		&runtime{stdin: unknown}, "-", &application.MessageSendInput{},
	); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("strict message JSON error = %v", err)
	}
}

func TestMessageJSONCannotOverrideCLIAccount(t *testing.T) {
	t.Parallel()

	input := application.MessageDeleteInput{MessageWriteRoute: application.MessageWriteRoute{
		Account: "acc_00000000000000000000000000000002",
	}}
	err := bindMessageInputAccount(
		&input,
		"acc_00000000000000000000000000000001",
	)
	if err == nil || !strings.Contains(err.Error(), "must omit account") {
		t.Fatalf("account override error = %v", err)
	}
}

func TestWritePrivateAttachmentIsPrivateAndNeverOverwrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "synthetic.txt")
	content := application.MessageAttachmentContent{
		Metadata: application.MessageAttachment{
			ID: "attachment-1", Name: "synthetic.txt", Size: 9, SizeKnown: true, Downloadable: true,
		},
		Data: []byte("synthetic"),
	}
	if err := writePrivateAttachment(path, content); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("attachment permissions = %o, want 600", permissions)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "synthetic" { // #nosec G304 -- test-owned temporary path.
		t.Fatalf("attachment data = %q, error = %v", data, err)
	}
	if err := writePrivateAttachment(path, application.MessageAttachmentContent{Data: []byte("replaced")}); err == nil {
		t.Fatal("attachment output overwrote an existing file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "synthetic" { // #nosec G304 -- test-owned temporary path.
		t.Fatalf("existing attachment changed to %q, error = %v", data, err)
	}
}

func TestMessageTextOutputNeutralizesTerminalControls(t *testing.T) {
	t.Parallel()

	message := application.Message{
		Summary: application.MessageSummary{
			ID: "message-1", ConversationID: "conversation-1",
			Author:    application.MessageActor{ID: "actor-1", DisplayName: "Sender\x1b[31m"},
			CreatedAt: "2026-08-14T00:00:00Z",
		},
		Content: application.MessageContent{Format: application.MessageFormatPlain, Text: "hello\x1b]8;;https://evil.example\a link"},
	}
	var output bytes.Buffer
	if err := writeMessage(&output, message); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(output.String(), '\x1b') || strings.ContainsRune(output.String(), '\a') {
		t.Fatalf("terminal controls survived rendering: %q", output.String())
	}
}
