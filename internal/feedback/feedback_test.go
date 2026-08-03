package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorRecordNeverRetainsValuesOrRawError(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"Bearer synthetic-access-token",
		"person@example.test",
		"/home/private-user/mail.eml",
		"credential-key-private",
		"confidential subject",
		"from:person@example.test",
	}
	record := NewErrorRecord(
		fmt.Errorf(
			"request failed: %s %s %s",
			secrets[0],
			secrets[1],
			secrets[2],
		),
		"mail search",
		[]string{
			"--account=" + secrets[1],
			"--query", secrets[5],
			"--config", secrets[2],
			"--credential-key=" + secrets[3],
			"--subject", secrets[4],
		},
	)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("record retained sensitive value %q: %s", secret, encoded)
		}
	}
	if record.Command.Path != "corr mail search" ||
		!record.Command.ValuesRedacted ||
		len(record.Command.Flags) != 5 {
		t.Fatalf("unexpected command shape: %+v", record.Command)
	}
}

func TestErrorRecordBoundsArgumentsAndIsDeterministic(t *testing.T) {
	t.Parallel()

	arguments := make([]string, maximumArguments+100)
	for index := range arguments {
		arguments[index] = fmt.Sprintf("--flag-%d=private-value-%d", index, index)
	}
	first := NewErrorRecord(context.DeadlineExceeded, "calendar list", arguments)
	second := NewErrorRecord(context.DeadlineExceeded, "calendar list", arguments)
	if first.ID != second.ID {
		t.Fatalf("deterministic IDs differ: %q != %q", first.ID, second.ID)
	}
	if !first.Command.ArgumentsCapped || len(first.Command.Flags) > maximumFlags {
		t.Fatalf("arguments were not bounded: %+v", first.Command)
	}
}

func TestStoreReplacesRatherThanAppending(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "diagnostics", "last-error.json")
	store := Store{Path: path}
	first := NewErrorRecord(errors.New("synthetic one"), "mail list", []string{"--limit=1"})
	second := NewErrorRecord(os.ErrPermission, "calendar list", []string{"--time-zone=UTC"})
	if err := store.Save(first); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	if err := store.Save(second); err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != second.ID {
		t.Fatalf("Load() ID = %q, want latest %q", loaded.ID, second.ID)
	}
	encoded, err := os.ReadFile(path) // #nosec G304 -- test-owned path.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Count(encoded, []byte{'\n'}) != 1 ||
		bytes.Contains(encoded, []byte(first.ID)) {
		t.Fatalf("store appended historical diagnostics: %s", encoded)
	}
}

func TestStoreRejectsMalformedOversizedAndSymlinkRecords(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"malformed": func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(`{"version":1,"extra":"private"}`), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
		},
		"oversized": func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maximumRecordBytes+1), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
		},
		"symlink": func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
				t.Fatalf("WriteFile(target) error = %v", err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "last-error.json")
			prepare(t, path)
			if _, err := (Store{Path: path}).Load(); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Load() error = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

func TestGenerateIsDeterministicAndAllowlisted(t *testing.T) {
	t.Parallel()

	record := NewErrorRecord(os.ErrNotExist, "mail list", []string{"--account=private"})
	input := Input{
		Build: Build{
			Version: "v0.7.0", Commit: "0123456789abcdef",
			BuildDate: "2026-07-28T12:00:00Z", GoVersion: "go1.25.1",
			Platform: "linux/amd64",
		},
		InstallMethod: "homebrew",
		Config:        ConfigStatus{Status: "ok", SchemaVersion: 3},
		Providers: []Provider{
			{ID: "microsoft-graph", Capabilities: []string{"mail", "calendar"}},
			{ID: "google", Capabilities: []string{"calendar"}},
		},
		LastError: LastErrorStatus{
			Status:  "ok",
			ID:      record.ID,
			Command: &record.Command,
			Classes: record.Classes,
		},
	}
	first, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate(first) error = %v", err)
	}
	second, err := Generate(input)
	if err != nil {
		t.Fatalf("Generate(second) error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Generate() is not deterministic:\n%s\n%s", first, second)
	}
	for _, want := range []string{
		`"generation": "local-only"`,
		`"automatic_upload": false`,
		`"mail_or_calendar_content_included": false`,
		`"id": "google"`,
		`"id": "microsoft-graph"`,
	} {
		if !bytes.Contains(first, []byte(want)) {
			t.Fatalf("report is missing %q:\n%s", want, first)
		}
	}
}

func TestGenerateDoesNotReflectMalformedDiagnosticInputs(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"ghp_SyntheticTokenValue",
		"person@example.test",
		"/Users/private/profile",
		"confidential-calendar-subject",
	}
	report, err := Generate(Input{
		Build: Build{
			Version: secrets[0], Commit: secrets[0], BuildDate: secrets[1],
			GoVersion: secrets[2], Platform: secrets[3],
		},
		InstallMethod: secrets[0],
		Config:        ConfigStatus{Status: "degraded", Reason: secrets[1]},
		Providers: []Provider{{
			ID: secrets[1], Capabilities: []string{secrets[3]},
		}},
		LastError: LastErrorStatus{
			Status: "degraded", Reason: secrets[2], ID: secrets[0],
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(report, []byte(secret)) {
			t.Fatalf("report reflected sensitive input %q:\n%s", secret, report)
		}
	}
	if strings.Count(string(report), `"status": "degraded"`) < 4 {
		t.Fatalf("collection failures were not visible:\n%s", report)
	}
}

func TestGenerateAutomaticUsesOnlyClosedAllowlist(t *testing.T) {
	t.Parallel()

	record := NewErrorRecord(
		os.ErrPermission,
		"mail search",
		[]string{"--account", "person@example.test", "--query=confidential-subject"},
	)
	report, err := GenerateAutomatic(AutomaticInput{
		Build: Build{
			Version: "v0.8.6-rc.2", Commit: "0123456789abcdef",
			BuildDate: "2026-08-03T12:00:00Z", GoVersion: "go1.25.1",
			Platform: "linux/amd64",
		},
		InstallMethod: "direct",
		LastError:     record,
	})
	if err != nil {
		t.Fatalf("GenerateAutomatic() error = %v", err)
	}
	for _, want := range []string{
		`"submission": "automatic-opt-in"`,
		`"destination": "public-github-issue"`,
		`"raw_error_included": false`,
		`"argument_values_included": false`,
		`"account_data_included": false`,
		`"mail_or_calendar_content_included": false`,
		`"path": "corr mail search"`,
		`"--account"`,
		`"--query"`,
	} {
		if !bytes.Contains(report, []byte(want)) {
			t.Fatalf("automatic report is missing %q:\n%s", want, report)
		}
	}
	for _, forbidden := range []string{
		"person@example.test",
		"confidential-subject",
		"config",
		"providers",
	} {
		if bytes.Contains(report, []byte(forbidden)) {
			t.Fatalf("automatic report retained forbidden value %q:\n%s", forbidden, report)
		}
	}
}

func TestGenerateAutomaticRejectsInvalidRecord(t *testing.T) {
	t.Parallel()

	if _, err := GenerateAutomatic(AutomaticInput{
		Build:         Build{Version: "dev", Commit: "none", BuildDate: "unknown"},
		InstallMethod: "direct",
	}); err == nil {
		t.Fatal("GenerateAutomatic() accepted an invalid record")
	}
}

func TestGenerateAutomaticDoesNotReflectMalformedBuildInput(t *testing.T) {
	t.Parallel()

	record := NewErrorRecord(os.ErrNotExist, "mail list", nil)
	secrets := []string{
		"person@example.test", "/home/private-user", "ghp_SyntheticToken", "private-subject",
	}
	report, err := GenerateAutomatic(AutomaticInput{
		Build: Build{
			Version: secrets[0], Commit: secrets[1], BuildDate: secrets[2],
			GoVersion: secrets[3], Platform: secrets[0],
		},
		InstallMethod: secrets[2],
		LastError:     record,
	})
	if err != nil {
		t.Fatalf("GenerateAutomatic() error = %v", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(report, []byte(secret)) {
			t.Fatalf("automatic report reflected %q:\n%s", secret, report)
		}
	}
}

func TestSubmissionStoreClaimsOneAttemptPerBuildAndError(t *testing.T) {
	t.Parallel()

	store := SubmissionStore{Directory: filepath.Join(t.TempDir(), "attempts")}
	build := Build{Version: "v0.8.6-rc.2", Commit: "0123456789abcdef"}
	record := NewErrorRecord(os.ErrNotExist, "mail list", []string{"--account", "private"})
	claimed, err := store.Claim(build, record)
	if err != nil || !claimed {
		t.Fatalf("Claim(first) = %t, %v", claimed, err)
	}
	claimed, err = store.Claim(build, record)
	if err != nil || claimed {
		t.Fatalf("Claim(second) = %t, %v", claimed, err)
	}
	other := NewErrorRecord(os.ErrPermission, "mail list", []string{"--account", "private"})
	claimed, err = store.Claim(build, other)
	if err != nil || !claimed {
		t.Fatalf("Claim(other) = %t, %v", claimed, err)
	}
}
