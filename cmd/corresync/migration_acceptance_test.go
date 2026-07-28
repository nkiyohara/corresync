package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

const legacyV1ConfigFixture = `version = 1
default_account = "work"

[accounts.work]
origin = "https://outlook.cloud.microsoft"

[policy]
mode = "guarded"
preview_sensitive_reads = false
preview_reversible_writes = false
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m0s"

[updates]
disable_automatic_checks = false
`

func TestDefaultLoadMigratesV06ConfigAndStateOnce(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the end-to-end XDG migration fixture is Linux-specific")
	}

	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("CORRESYNC_CONFIG", "")
	t.Setenv("OWA_CONFIG", "")
	t.Setenv("CORRESYNC_STATE_DIR", "")
	t.Setenv("OWA_STATE_DIR", "")

	legacyConfigPath := filepath.Join(configHome, "owa-bridge", "config.toml")
	legacyConfig := []byte(legacyV1ConfigFixture)
	writeMigrationFixture(t, legacyConfigPath, legacyConfig)

	legacyState := filepath.Join(stateHome, "owa-bridge")
	legacyProfile := filepath.Join(legacyState, "profiles", migrationProfileKey("work"))
	writeMigrationFixture(t, filepath.Join(legacyProfile, "Cookies"), []byte("synthetic-browser-state"))
	writeMigrationFixture(t, filepath.Join(legacyState, "audit", "events.jsonl"), []byte("{\"synthetic\":true}\n"))
	writeMigrationFixture(t, filepath.Join(legacyState, "ipc", "legacy.token"), []byte("must-not-migrate"))

	var stderr bytes.Buffer
	app := newRuntime(t.Context(), "", &bytes.Buffer{}, &stderr, buildinfo.Current())
	configuration, path, err := app.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	wantPath := filepath.Join(configHome, "corresync", "config.toml")
	if path != wantPath || configuration.Version != config.CurrentVersion {
		t.Fatalf("loadConfig() = version %d path %q, want version %d path %q", configuration.Version, path, config.CurrentVersion, wantPath)
	}
	account := configuration.Accounts["work"]
	if account.PrimaryProvider() != domain.ProviderMicrosoftOWA || account.ID.ValidateOpaque() != nil {
		t.Fatalf("migrated account = %+v", account)
	}
	if original, readErr := os.ReadFile(legacyConfigPath); readErr != nil || !bytes.Equal(original, legacyConfig) { // #nosec G304 -- test path is below t.TempDir.
		t.Fatalf("legacy config changed: %q, %v", original, readErr)
	}

	newState := filepath.Join(stateHome, "corresync")
	newProfile := filepath.Join(newState, "profiles", migrationProfileKey(string(account.ID)))
	if got, readErr := os.ReadFile(filepath.Join(newProfile, "Cookies")); readErr != nil || // #nosec G304 -- test path is below t.TempDir.
		string(got) != "synthetic-browser-state" {
		t.Fatalf("migrated browser state = %q, %v", got, readErr)
	}
	if _, statErr := os.Lstat(legacyProfile); !os.IsNotExist(statErr) {
		t.Fatalf("legacy profile still exists after move: %v", statErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(newState, "audit", "events.jsonl")); readErr != nil || // #nosec G304 -- test path is below t.TempDir.
		string(got) != "{\"synthetic\":true}\n" {
		t.Fatalf("copied rollback-safe state = %q, %v", got, readErr)
	}
	if _, statErr := os.Lstat(filepath.Join(newState, "ipc")); !os.IsNotExist(statErr) {
		t.Fatalf("legacy IPC material migrated: %v", statErr)
	}
	if output := stderr.String(); !strings.Contains(output, "rollback copy preserved") ||
		!strings.Contains(output, "rollback may require sign-in") {
		t.Fatalf("migration guidance = %q", output)
	}

	second, _, err := newRuntime(
		t.Context(), "", &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current(),
	).loadConfig()
	if err != nil {
		t.Fatalf("second loadConfig() error = %v", err)
	}
	if second.Accounts["work"].ID != account.ID {
		t.Fatalf("idempotent migration changed account ID: %q -> %q", account.ID, second.Accounts["work"].ID)
	}
}

func TestLegacyDefaultConfigArgumentAlsoMigratesState(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the end-to-end XDG migration fixture is Linux-specific")
	}

	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("CORRESYNC_STATE_DIR", "")
	t.Setenv("OWA_STATE_DIR", "")

	legacyConfigPath := filepath.Join(configHome, "owa-bridge", "config.toml")
	writeMigrationFixture(t, legacyConfigPath, []byte(legacyV1ConfigFixture))
	legacyProfile := filepath.Join(
		stateHome, "owa-bridge", "profiles", migrationProfileKey("work"),
	)
	writeMigrationFixture(t, filepath.Join(legacyProfile, "Cookies"), []byte("synthetic-browser-state"))

	app := newRuntime(
		t.Context(), legacyConfigPath, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current(),
	)
	configuration, path, err := app.loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if path != filepath.Join(configHome, "corresync", "config.toml") {
		t.Fatalf("resolved config path = %q", path)
	}
	account := configuration.Accounts["work"]
	newProfile := filepath.Join(
		stateHome, "corresync", "profiles", migrationProfileKey(string(account.ID)),
	)
	if got, readErr := os.ReadFile(filepath.Join(newProfile, "Cookies")); readErr != nil || // #nosec G304 -- test path is below t.TempDir.
		string(got) != "synthetic-browser-state" {
		t.Fatalf("migrated browser state = %q, %v", got, readErr)
	}
}

func writeMigrationFixture(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func migrationProfileKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}
