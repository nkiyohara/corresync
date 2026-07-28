package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestStateDirByPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		goos      string
		home      string
		config    string
		cache     string
		xdg       string
		wantParts []string
	}{
		{"linux default", "linux", "/home/test", "/config", "/cache", "", []string{"home", "test", ".local", "state", "corresync"}},
		{"linux XDG", "linux", "/home/test", "/config", "/cache", "/state", []string{"state", "corresync"}},
		{"macOS", "darwin", "/Users/test", "/Users/test/Library/Application Support", "/cache", "", []string{"Application Support", "corresync"}},
		{"Windows", "windows", `C:\Users\test`, `C:\Config`, `C:\Local`, "", []string{"Local", "corresync"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := stateDir(
				test.goos,
				test.home,
				test.config,
				test.cache,
				nil,
				nil,
				test.xdg,
				"corresync",
			)
			if err != nil {
				t.Fatalf("stateDir() error = %v", err)
			}
			for _, part := range test.wantParts {
				if !strings.Contains(got, part) {
					t.Fatalf("stateDir() = %q, want part %q", got, part)
				}
			}
		})
	}
}

func TestStateDirRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	if _, err := stateDir("linux", "/home/test", "", "", nil, nil, "relative", "corresync"); err == nil {
		t.Fatal("stateDir() unexpectedly accepted relative XDG_STATE_HOME")
	}
	if _, err := stateDir("darwin", "/Users/test", "", "", errors.New("missing"), nil, "", "corresync"); err == nil {
		t.Fatal("stateDir() unexpectedly ignored config directory error")
	}
	if _, err := stateDir("windows", "", "", "", nil, errors.New("missing"), "", "corresync"); err == nil {
		t.Fatal("stateDir() unexpectedly ignored cache directory error")
	}
}

func TestMigrateLegacyStatePreservesRollbackAndRekeysProfiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG state migration fixture is Linux-specific")
	}
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("CORRESYNC_STATE_DIR", "")
	t.Setenv("OWA_STATE_DIR", "")
	legacyRoot := filepath.Join(root, "owa-bridge")
	legacyProfile := filepath.Join(legacyRoot, "profiles", profileKey("work"))
	if err := os.MkdirAll(legacyProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyCookie := filepath.Join(legacyProfile, "Cookies")
	if err := os.WriteFile(legacyCookie, []byte("synthetic browser state"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyAudit := filepath.Join(legacyRoot, "audit", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(legacyAudit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAudit, []byte("{\"synthetic\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyIPC := filepath.Join(legacyRoot, "ipc", "old.token")
	if err := os.MkdirAll(filepath.Dir(legacyIPC), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyIPC, []byte("must-not-migrate"), 0o600); err != nil {
		t.Fatal(err)
	}
	accountID := domain.AccountID("acc_00000000000000000000000000000001")
	migrated, err := MigrateLegacyState(map[string]domain.AccountID{"work": accountID})
	if err != nil {
		t.Fatalf("MigrateLegacyState() error = %v", err)
	}
	if !migrated {
		t.Fatal("MigrateLegacyState() did not report migration")
	}
	newRoot := filepath.Join(root, "corresync")
	newCookie := filepath.Join(newRoot, "profiles", profileKey(string(accountID)), "Cookies")
	if contents, readErr := os.ReadFile(newCookie); readErr != nil || // #nosec G304 -- path is confined to t.TempDir.
		string(contents) != "synthetic browser state" {
		t.Fatalf("new profile = %q, %v", contents, readErr)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "audit", "events.jsonl")); err != nil {
		t.Fatalf("migrated audit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "ipc", "old.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy IPC credential migrated: %v", err)
	}
	if _, err := os.Stat(legacyCookie); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy authenticated profile was retained: %v", err)
	}
	if contents, err := os.ReadFile(legacyAudit); err != nil || // #nosec G304 -- path is confined to t.TempDir.
		string(contents) != "{\"synthetic\":true}\n" {
		t.Fatalf("legacy rollback metadata changed: %q, %v", contents, err)
	}
	if again, err := MigrateLegacyState(map[string]domain.AccountID{"work": accountID}); err != nil || again {
		t.Fatalf("idempotent migration = %t, %v", again, err)
	}
}

func TestUpdateCachePathUsesPrivateStateTree(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path, err := UpdateCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "latest.json" || filepath.Base(filepath.Dir(path)) != "updates" {
		t.Fatalf("UpdateCachePath() = %q", path)
	}
}

func TestProfileDirDoesNotContainAccountAlias(t *testing.T) {
	t.Setenv("OWA_STATE_DIR", t.TempDir())
	alias := "work/team"
	path, err := ProfileDir(domain.AccountID(alias))
	if err != nil {
		t.Fatalf("ProfileDir() error = %v", err)
	}
	if strings.Contains(path, alias) || filepath.Base(path) == "team" {
		t.Fatalf("ProfileDir() exposed alias as path: %q", path)
	}
}
