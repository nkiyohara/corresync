package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	if configuration.Accounts[configuration.DefaultAccount].Monitor != nil {
		t.Fatal("fresh configuration unexpectedly enabled monitoring")
	}
}

func TestMonitorConsentBoundariesValidateIndependently(t *testing.T) {
	t.Parallel()

	configuration := Default()
	account := configuration.Accounts["work"]

	notify := NewMonitor(domain.MonitorNotify)
	account.Monitor = &notify
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err != nil {
		t.Fatalf("notify config error = %v", err)
	}

	queue := NewMonitor(domain.MonitorQueue)
	account.Monitor = &queue
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err != nil {
		t.Fatalf("queue config error = %v", err)
	}

	agent := NewMonitor(domain.MonitorAgent)
	runner := NewRunner(
		"/synthetic/runner",
		[]string{"--json"},
		[]string{"account", "event_id", "subject", "trust"},
		"local",
		false,
	)
	agent.Runner = &runner
	account.Monitor = &agent
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err != nil {
		t.Fatalf("agent config error = %v", err)
	}

	agent.Runner.Egress = "remote"
	if err := configuration.Validate(); err == nil {
		t.Fatal("remote runner unexpectedly validated without separate approval")
	}
	agent.Runner.ApproveRemote = true
	if err := configuration.Validate(); err != nil {
		t.Fatalf("approved remote runner config error = %v", err)
	}
}

func TestMonitoringCannotBeEnabledForCalendarOnlyAccount(t *testing.T) {
	t.Parallel()

	configuration := Default()
	account := configuration.Accounts["work"]
	account.Mail = nil
	monitor := NewMonitor(domain.MonitorQueue)
	account.Monitor = &monitor
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("calendar-only monitoring unexpectedly validated")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Default()
	want.Policy.PreviewSensitiveReads = true
	want.Accounts["personal"] = testOutlookAccount(
		"acc_00000000000000000000000000000002",
		"https://outlook.office.com/",
		"shared@example.invalid",
	)
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != want.Version || got.Policy != want.Policy || got.Updates != want.Updates || len(got.Accounts) != len(want.Accounts) ||
		!reflect.DeepEqual(got.Accounts["personal"], want.Accounts["personal"]) {
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
	encoded := []byte("# retained\nversion = 3\n")
	defaultConfig, err := toml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, bytes.TrimPrefix(defaultConfig, []byte("version = 3\n"))...)
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
			configuration.Accounts["work"] = testOutlookAccount(
				defaultAccountID,
				"http://outlook.example",
				"",
			)
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = testOutlookAccount(
				defaultAccountID,
				"https://user@example.com",
				"",
			)
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = testOutlookAccount(
				defaultAccountID,
				"https://example.com/owa",
				"",
			)
		},
		func(configuration *Config) {
			configuration.Accounts["work"] = testOutlookAccount(
				defaultAccountID,
				"https://outlook.example",
				"Shared <shared@example.invalid>",
			)
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
	personalRoute, personalWeb := personal.OutlookWeb()
	if work.PrimaryProvider() != domain.ProviderMicrosoftOWA ||
		personal.PrimaryProvider() != domain.ProviderMicrosoftOWA ||
		work.ID == personal.ID ||
		work.ID.ValidateOpaque() != nil ||
		personal.ID.ValidateOpaque() != nil ||
		work.Monitor != nil ||
		personal.Monitor != nil ||
		!personalWeb ||
		personalRoute.Mailbox != "shared@example.invalid" {
		t.Fatalf("migrated accounts = %+v, %+v", work, personal)
	}
	if !bytes.Contains(legacy, []byte("version = 1")) {
		t.Fatal("legacy input was modified")
	}
}

func TestLoadMigratesV2ToPerServiceRoutes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	v2 := []byte(`
version = 2
default_account = "work"

[accounts.work]
id = "acc_00000000000000000000000000000001"
provider = "microsoft-owa"
address = "reader@example.invalid"
origin = "https://outlook.example.invalid"
mailbox = "shared@example.invalid"

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"
`)
	if err := os.WriteFile(path, v2, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["work"]
	web, ok := account.OutlookWeb()
	if configuration.Version != CurrentVersion ||
		account.ID != "acc_00000000000000000000000000000001" ||
		account.MailProvider() != domain.ProviderMicrosoftOWA ||
		account.CalendarProvider() != domain.ProviderMicrosoftOWA ||
		account.Monitor != nil ||
		!ok ||
		web.Mailbox != "shared@example.invalid" {
		t.Fatalf("migrated v2 config = %+v", configuration)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, v2) {
		t.Fatal("read-only Load modified the v2 source")
	}
}

func TestMixedStandardsRoutesAreStrictAndSecretFree(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.Accounts["work"] = Account{
		ID:      defaultAccountID,
		Address: "reader@example.invalid",
		Mail: &MailRoute{
			Provider: domain.ProviderIMAPSMTP,
			IMAPSMTP: &IMAPSMTPRoute{
				IMAP: TLSEndpoint{
					Host: "imap.example.invalid", Port: 993, Mode: TLSImplicit,
				},
				SMTP: TLSEndpoint{
					Host: "smtp.example.invalid", Port: 587, Mode: TLSStartTLS,
				},
				Username: "reader@example.invalid",
				Credential: CredentialRef{
					Backend: CredentialHelper, Key: "work-mail", Consent: true,
				},
			},
		},
		Calendar: &CalendarRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &CalDAVRoute{
				Endpoint: "https://calendar.example.invalid/dav",
				Username: "reader@example.invalid",
				Credential: CredentialRef{
					Backend: CredentialOSKeyring, Key: "work-calendar", Consent: true,
				},
			},
		},
	}
	configuration.Credentials.Helper = []string{"/usr/local/bin/example-helper", "get"}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("mixed standards routes are invalid: %v", err)
	}
	encoded, err := toml.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password =", "secret =", "token =", "cookie ="} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Fatalf("encoded config contains %q:\n%s", forbidden, encoded)
		}
	}

	invalid := configuration
	account := invalid.Accounts["work"]
	account.Mail.Provider = domain.ProviderJMAP
	invalid.Accounts["work"] = account
	if err := invalid.Validate(); err == nil {
		t.Fatal("mismatched provider payload was accepted")
	}
}

func TestOAuthRouteRequiresLoopbackPKCEAndOSKeyring(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.Accounts["work"] = Account{
		ID: defaultAccountID,
		Mail: &MailRoute{
			Provider: domain.ProviderGoogleAPI,
			GoogleAPI: &OAuthRoute{
				APIBase:     "https://www.googleapis.com",
				ClientID:    "synthetic-public-client",
				RedirectURI: "http://127.0.0.1:43123/callback",
				Authorization: CredentialRef{
					Backend: CredentialOSKeyring, Key: "google-work", Consent: true,
				},
			},
		},
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("valid public-client route rejected: %v", err)
	}
	account := configuration.Accounts["work"]
	oauth := *account.Mail.GoogleAPI
	oauth.APIBase = "https://api.attacker.invalid"
	account.Mail.GoogleAPI = &oauth
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("credential-exfiltrating OAuth API base was accepted")
	}

	oauth.APIBase = "https://www.googleapis.com"
	oauth.RedirectURI = "http://127.0.0.1:43123/callback?leak=true"
	account.Mail.GoogleAPI = &oauth
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("OAuth redirect query was accepted")
	}

	oauth.RedirectURI = "http://127.0.0.1:43123/callback"
	account.Mail.GoogleAPI = &oauth
	account.Mail.GoogleAPI.Authorization.Backend = CredentialHelper
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("OAuth helper-backed grant was accepted")
	}
}

func TestGoogleWebRoutesRequireExactProviderOwnedOrigins(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.Accounts["work"] = Account{
		ID: defaultAccountID,
		Mail: &MailRoute{
			Provider: domain.ProviderGoogleWeb,
			GoogleWeb: &WebRoute{
				Origin: "https://mail.google.com",
			},
		},
		Calendar: &CalendarRoute{
			Provider: domain.ProviderGoogleWeb,
			GoogleWeb: &WebRoute{
				Origin: "https://calendar.google.com",
			},
		},
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("valid Google Web routes rejected: %v", err)
	}
	for name, origin := range map[string]string{
		"lookalike": "https://mail.google.com.attacker.invalid",
		"path":      "https://mail.google.com/mail",
		"query":     "https://mail.google.com/?continue=attacker",
		"userinfo":  "https://user@mail.google.com",
	} {
		t.Run(name, func(t *testing.T) {
			invalid := configuration
			account := invalid.Accounts["work"]
			route := *account.Mail.GoogleWeb
			route.Origin = origin
			mail := *account.Mail
			mail.GoogleWeb = &route
			account.Mail = &mail
			invalid.Accounts["work"] = account
			if err := invalid.Validate(); err == nil {
				t.Fatalf("Google Web accepted origin %q", origin)
			}
		})
	}
}

func testOutlookAccount(
	id domain.AccountID,
	origin string,
	mailbox string,
) Account {
	return migratedOutlookAccount(id, "", origin, mailbox)
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
