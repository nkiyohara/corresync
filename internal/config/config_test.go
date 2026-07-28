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

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
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
		ID:       "acc_00000000000000000000000000000002",
		Provider: domain.ProviderMicrosoftOWA,
		Origin:   "https://outlook.office.com/",
		Mailbox:  "shared@example.invalid",
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
	encoded := []byte("# retained\nversion = 2\n")
	defaultConfig, err := toml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, bytes.TrimPrefix(defaultConfig, []byte("version = 2\n"))...)
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
		func(configuration *Config) { configuration.Version = 1 },
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

func TestNewDefaultUsesFreshOpaqueAccountID(t *testing.T) {
	t.Parallel()

	first, err := NewDefault()
	if err != nil {
		t.Fatalf("NewDefault() error = %v", err)
	}
	second, err := NewDefault()
	if err != nil {
		t.Fatalf("NewDefault() second error = %v", err)
	}
	firstID := first.Accounts[first.DefaultAccount].ID
	secondID := second.Accounts[second.DefaultAccount].ID
	if err := firstID.ValidateOpaque(); err != nil {
		t.Fatalf("first ID = %q: %v", firstID, err)
	}
	if firstID == secondID {
		t.Fatalf("NewDefault() repeated account ID %q", firstID)
	}
}

func TestResolveAccountSupportsAliasAndStableID(t *testing.T) {
	t.Parallel()

	configuration := Default()
	want := configuration.Accounts["work"]
	for _, reference := range []string{"", "work", string(want.ID)} {
		alias, got, err := configuration.ResolveAccount(reference)
		if err != nil {
			t.Fatalf("ResolveAccount(%q) error = %v", reference, err)
		}
		if alias != "work" || got != want {
			t.Fatalf("ResolveAccount(%q) = %q, %+v", reference, alias, got)
		}
	}
	if _, _, err := configuration.ResolveAccount("missing"); err == nil {
		t.Fatal("ResolveAccount(missing) unexpectedly succeeded")
	}
}

func TestMigrateV1AssignsStableOpaqueIDsAndPreservesLegacy(t *testing.T) {
	t.Parallel()

	legacy := []byte(`
version = 1
default_account = "work"

[accounts.work]
origin = "https://outlook.cloud.microsoft"

[accounts.personal]
origin = "https://outlook.office.com/"
mailbox = "shared@example.invalid"

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
disable_automatic_checks = true
`)
	configuration, err := MigrateV1(legacy)
	if err != nil {
		t.Fatalf("MigrateV1() error = %v", err)
	}
	if configuration.Version != CurrentVersion ||
		configuration.DefaultAccount != "work" ||
		!configuration.Updates.DisableAutomaticChecks {
		t.Fatalf("MigrateV1() = %+v", configuration)
	}
	work := configuration.Accounts["work"]
	personal := configuration.Accounts["personal"]
	if work.Provider != domain.ProviderMicrosoftOWA ||
		personal.Provider != domain.ProviderMicrosoftOWA ||
		work.ID == personal.ID ||
		work.ID.ValidateOpaque() != nil ||
		personal.ID.ValidateOpaque() != nil ||
		personal.Mailbox != "shared@example.invalid" {
		t.Fatalf("migrated accounts = %+v, %+v", work, personal)
	}
	if !bytes.Contains(legacy, []byte("version = 1")) {
		t.Fatal("legacy input was modified")
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
