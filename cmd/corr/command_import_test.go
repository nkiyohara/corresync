package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
)

func TestImportScanRequiresPrivacyApprovalBeforeLocalAccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(
		context.Background(),
		filepath.Join(t.TempDir(), "missing-config.toml"),
		&stdout,
		&stderr,
		buildinfo.Current(),
	)
	command := importScanCommand{
		Source: filepath.Join(t.TempDir(), "missing.eml"),
		Format: string(application.ImportFormatEML),
	}
	err := command.Run(app)
	if err == nil || !strings.Contains(err.Error(), "--approve-read") ||
		!strings.Contains(err.Error(), "will not read credential stores") {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf(
			"command performed output before approval: stdout=%q stderr=%q",
			stdout.String(),
			stderr.String(),
		)
	}
}

func TestImportScanAndPurgeJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(root, "state"))
	configPath := filepath.Join(root, "config.toml")
	configuration := config.Default()
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "synthetic.eml")
	if err := os.WriteFile(source, []byte(
		"Message-ID: <cli@example.test>\r\n"+
			"Date: Thu, 04 Jan 2024 05:06:07 +0000\r\n\r\nbody\r\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(
		t.Context(),
		configPath,
		&stdout,
		&stderr,
		buildinfo.Current(),
	)
	command := importScanCommand{
		Source: source, Format: string(application.ImportFormatAuto),
		ApproveRead: true, JSON: true,
	}
	if err := command.Run(app); err != nil {
		t.Fatalf("scan Run() error = %v", err)
	}
	var plan application.ImportPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if plan.Format != application.ImportFormatEML ||
		plan.StagedItems != 1 || plan.ContentTrust != "untrusted_data" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	if !strings.Contains(stderr.String(), "will not read credential stores") ||
		!strings.Contains(stderr.String(), source) {
		t.Fatalf("privacy explanation = %q", stderr.String())
	}

	if err := (&importPurgeCommand{}).Run(app); err == nil {
		t.Fatal("purge succeeded without approval")
	}
	stdout.Reset()
	purge := importPurgeCommand{Approve: true, JSON: true}
	if err := purge.Run(app); err != nil {
		t.Fatalf("purge Run() error = %v", err)
	}
	var result struct {
		Purged bool `json:"purged"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode purge result: %v", err)
	}
	if !result.Purged {
		t.Fatalf("unexpected purge result: %+v", result)
	}
}
