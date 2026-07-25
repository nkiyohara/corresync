package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/owa-bridge/internal/policy"
)

func TestDefaultIsValidAndSecretFree(t *testing.T) {
	t.Parallel()

	configuration := Default()
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, forbidden := range []string{"password", "token", "cookie", "canary", "secret"} {
		if strings.Contains(strings.ToLower(string(contents)), forbidden) {
			t.Fatalf("saved config contains forbidden secret field %q: %s", forbidden, contents)
		}
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Default()
	want.Policy.PreviewSensitiveReads = true
	want.Accounts["personal"] = Account{
		Origin: "https://outlook.office.com/", Mailbox: "shared@example.invalid",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != want.Version || got.Policy != want.Policy || got.Updates != want.Updates || len(got.Accounts) != len(want.Accounts) ||
		got.Accounts["personal"] != want.Accounts["personal"] {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat() error = %v", statErr)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("config permissions = %o, want 600", gotPerm)
		}
	}
}

func TestSaveTOMLPreservesValidatedComments(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	encoded := []byte("# retained\nversion = 1\n")
	defaultConfig, err := toml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, bytes.TrimPrefix(defaultConfig, []byte("version = 1\n"))...)
	if err := SaveTOML(path, encoded); err != nil {
		t.Fatalf("SaveTOML() error = %v", err)
	}
	saved, err := os.ReadFile(path) // #nosec G304 -- private test path.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, encoded) {
		t.Fatalf("saved TOML changed:\n%s", saved)
	}
}

func TestFingerprintChangesWithExactConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := Default()
	if err := Save(path, configuration); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	first, err := Fingerprint(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("Fingerprint() = %q, %v", first, err)
	}
	configuration.Policy.PreviewSensitiveReads = true
	if err := Save(path, configuration); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	second, err := Fingerprint(path)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if first == second {
		t.Fatal("Fingerprint() did not change after a policy edit")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	contents := []byte(`
version = 1
default_account = "work"
unexpected_token = "must-not-be-accepted"

[accounts.work]
origin = "https://outlook.cloud.microsoft"

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() unexpectedly accepted unknown field")
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []func(*Config){
		func(configuration *Config) { configuration.Version = 2 },
		func(configuration *Config) { configuration.Accounts = nil },
		func(configuration *Config) { configuration.DefaultAccount = "missing" },
		func(configuration *Config) {
			configuration.Accounts["work"] = Account{Origin: "http://outlook.example"}
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = Account{Origin: "https://user@example.com"}
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = Account{Origin: "https://example.com/owa"}
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = Account{
				Origin: "https://outlook.example", Mailbox: "Shared <shared@example.invalid>",
			}
		},
		func(configuration *Config) { configuration.Policy.Mode = policy.Mode("unguarded") },
		func(configuration *Config) { configuration.Policy.MaxRecipients = 0 },
		func(configuration *Config) { configuration.Policy.MaxAttendees = 1001 },
		func(configuration *Config) { configuration.Browser.LoginTimeout = 0 },
		func(configuration *Config) { configuration.Browser.Executable = "chrome\n--dangerous" },
	}
	for index, mutate := range tests {
		configuration := Default()
		mutate(&configuration)
		if err := configuration.Validate(); err == nil {
			t.Fatalf("case %d unexpectedly passed validation: %+v", index, configuration)
		}
	}
}

func TestSaveRejectsNonRegularTarget(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := Save(path, Default()); err == nil {
		t.Fatal("Save() unexpectedly accepted directory target")
	}
}

func TestLoadMissingFilePreservesCause(t *testing.T) {
	t.Parallel()

	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want os.ErrNotExist", err)
	}
}
