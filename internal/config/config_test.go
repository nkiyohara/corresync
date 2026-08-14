package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
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
	if configuration.DefaultAccount != "" || len(configuration.Accounts) != 0 {
		t.Fatalf("Default() selected a provider route: %+v", configuration)
	}
}

func TestDefaultRoundTripIsProviderNeutral(t *testing.T) {
	t.Parallel()

	configuration := Default()
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	if configuration.DefaultAccount != "" || len(configuration.Accounts) != 0 {
		t.Fatalf("Default() selected a provider route: %+v", configuration)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.DefaultAccount != "" || len(loaded.Accounts) != 0 {
		t.Fatalf("round trip selected a provider route: %+v", loaded)
	}
	if loaded.Updates.AutoInstall {
		t.Fatal("fresh configuration enabled automatic installation")
	}
	if loaded.Feedback.AutoSubmit {
		t.Fatal("fresh configuration enabled automatic public feedback")
	}
	if loaded.Updates.Channel != UpdateChannelStable {
		t.Fatalf("fresh update channel = %q, want stable", loaded.Updates.Channel)
	}
}

func TestTaskOnlyAccountRoundTripsWithExplicitProviderSelection(t *testing.T) {
	t.Parallel()

	configuration := Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = Account{
		ID:    "acc_00000000000000000000000000000009",
		Tasks: &TaskRoute{Provider: domain.ProviderTickTick},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	account := loaded.Accounts["tasks"]
	if account.Mail != nil || account.Calendar != nil ||
		account.TaskProvider() != domain.ProviderTickTick ||
		account.PrimaryProvider() != domain.ProviderTickTick {
		t.Fatalf("task-only account = %+v", account)
	}
}

func TestTaskRouteRejectsNonTaskAndUnknownProviders(t *testing.T) {
	t.Parallel()

	for _, provider := range []domain.ProviderID{domain.ProviderJMAP, "synthetic-unknown"} {
		configuration := Default()
		configuration.DefaultAccount = "tasks"
		configuration.Accounts["tasks"] = Account{
			ID: "acc_00000000000000000000000000000009", Tasks: &TaskRoute{Provider: provider},
		}
		if err := configuration.Validate(); err == nil {
			t.Fatalf("task provider %q unexpectedly validated", provider)
		}
	}
}

func TestMicrosoftTodoRouteRoundTripsAndRejectsCrossCloudEndpoints(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.DefaultAccount = "tasks"
	route := OAuthRoute{
		APIBase: "https://graph.microsoft.us/v1.0", MicrosoftCloud: microsoftcloud.GCCHigh,
		ClientID: "synthetic-public-client", RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: CredentialRef{
			Backend: CredentialOSKeyring, Key: "tasks-graph", Consent: true,
		},
	}
	configuration.Accounts["tasks"] = Account{
		ID:      "acc_00000000000000000000000000000009",
		Address: "reader@example.test",
		Tasks: &TaskRoute{
			Provider:       domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &MicrosoftGraphTaskRoute{OAuth: route, ReadOnly: true},
		},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Accounts["tasks"].Tasks.MicrosoftGraph
	if got == nil || !got.ReadOnly || got.OAuth.MicrosoftCloud != microsoftcloud.GCCHigh ||
		got.OAuth.APIBase != route.APIBase {
		t.Fatalf("Microsoft To Do route = %+v", got)
	}
	missingAddress := configuration.Accounts["tasks"]
	missingAddress.Address = ""
	if err := missingAddress.validate(); err == nil || !strings.Contains(err.Error(), "email address") {
		t.Fatalf("addressless Microsoft To Do route error = %v", err)
	}
	bad := configuration
	account := bad.Accounts["tasks"]
	account.Tasks.MicrosoftGraph.OAuth.APIBase = "https://graph.microsoft.com/v1.0"
	bad.Accounts["tasks"] = account
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("cross-cloud route error = %v", err)
	}
	account.Tasks.MicrosoftGraph.OAuth.APIBase = "https://microsoftgraph.chinacloudapi.cn/v1.0"
	account.Tasks.MicrosoftGraph.OAuth.MicrosoftCloud = microsoftcloud.China
	bad.Accounts["tasks"] = account
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("China To Do route error = %v", err)
	}
}

func TestTodoistRouteRoundTripsAsSecretFreePublicClient(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = Account{
		ID:      "acc_00000000000000000000000000000009",
		Address: "reader@example.test",
		Tasks: &TaskRoute{
			Provider: domain.ProviderTodoist,
			Todoist: &TodoistTaskRoute{
				ReadOnly: true,
				OAuth: OAuthRoute{
					APIBase:     "https://api.todoist.com/api/v1",
					ClientID:    "synthetic-public-client",
					RedirectURI: "http://127.0.0.1:43123/callback",
					Authorization: CredentialRef{
						Backend: CredentialOSKeyring, Key: "tasks-todoist", Consent: true,
					},
				},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Accounts["tasks"].Tasks.Todoist
	if got == nil || !got.ReadOnly ||
		got.OAuth.APIBase != "https://api.todoist.com/api/v1" ||
		got.OAuth.Authorization.Key != "tasks-todoist" {
		t.Fatalf("Todoist route = %+v", got)
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"client_secret", "personal_token", "access_token", "refresh_token"} {
		if strings.Contains(strings.ToLower(string(contents)), forbidden) {
			t.Fatalf("Todoist route persisted forbidden field %q: %s", forbidden, contents)
		}
	}

	bad := configuration
	account := bad.Accounts["tasks"]
	account.Tasks.Todoist.OAuth.APIBase = "https://api.example.invalid/api/v1"
	bad.Accounts["tasks"] = account
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "todoist API base") {
		t.Fatalf("unpinned Todoist route error = %v", err)
	}
	account.Tasks.Todoist.OAuth.APIBase = "https://api.todoist.com/api/v1"
	missingAddress := configuration.Accounts["tasks"]
	missingAddress.Address = ""
	if err := missingAddress.validate(); err == nil || !strings.Contains(err.Error(), "email address") {
		t.Fatalf("addressless Todoist route error = %v", err)
	}
}

func TestCalDAVTaskRouteRoundTripsWithoutAnAccountAddress(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = Account{
		ID: "acc_00000000000000000000000000000009",
		Tasks: &TaskRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &CalDAVTaskRoute{
				Endpoint: "https://dav.example.invalid/", TaskListPath: "/tasks/work/",
				Username: "task-user",
				Credential: CredentialRef{
					Backend: CredentialHelper, Key: "caldav-tasks", Consent: true,
				},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	route := loaded.Accounts["tasks"].Tasks.CalDAV
	if route == nil || route.Endpoint != "https://dav.example.invalid/" ||
		route.TaskListPath != "/tasks/work/" || route.Username != "task-user" ||
		route.Credential.Key != "caldav-tasks" {
		t.Fatalf("CalDAV task route = %+v", route)
	}
	wrongPayload := configuration
	account := wrongPayload.Accounts["tasks"]
	account.Tasks.Todoist = &TodoistTaskRoute{}
	wrongPayload.Accounts["tasks"] = account
	if err := wrongPayload.Validate(); err == nil || !strings.Contains(err.Error(), "VTODO") {
		t.Fatalf("mixed CalDAV task payload error = %v", err)
	}
}

func TestTodoistRouteRejectsAnEphemeralOAuthPort(t *testing.T) {
	t.Parallel()
	configuration := Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = Account{
		ID:      "acc_00000000000000000000000000000009",
		Address: "reader@example.test",
		Tasks: &TaskRoute{
			Provider: domain.ProviderTodoist,
			Todoist: &TodoistTaskRoute{OAuth: OAuthRoute{
				APIBase: "https://api.todoist.com/api/v1", ClientID: "synthetic-public-client",
				RedirectURI: "http://127.0.0.1:0/callback",
				Authorization: CredentialRef{
					Backend: CredentialOSKeyring, Key: "tasks-todoist", Consent: true,
				},
			}},
		},
	}
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "fixed loopback port") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestOAuthHandleSharingRequiresTheSameCanonicalGrant(t *testing.T) {
	t.Parallel()
	global := OAuthRoute{
		APIBase:  "https://graph.microsoft.com/v1.0",
		ClientID: "synthetic-public-client", RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: CredentialRef{
			Backend: CredentialOSKeyring, Key: "shared-graph", Consent: true,
		},
	}
	explicitGlobal := global
	explicitGlobal.MicrosoftCloud = microsoftcloud.Global
	configuration := Default()
	configuration.DefaultAccount = "shared"
	configuration.Accounts["shared"] = Account{
		ID:      "acc_00000000000000000000000000000009",
		Address: "reader@example.test",
		Mail: &MailRoute{
			Provider: domain.ProviderMicrosoftGraph, MicrosoftGraph: &global,
		},
		Tasks: &TaskRoute{
			Provider: domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &MicrosoftGraphTaskRoute{
				OAuth: explicitGlobal, ReadOnly: true,
			},
		},
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("legacy and explicit global grant: %v", err)
	}

	conflicting := explicitGlobal
	conflicting.APIBase = "https://graph.microsoft.us/v1.0"
	conflicting.MicrosoftCloud = microsoftcloud.GCCHigh
	account := configuration.Accounts["shared"]
	account.Tasks.MicrosoftGraph.OAuth = conflicting
	configuration.Accounts["shared"] = account
	if err := configuration.Validate(); err == nil || !strings.Contains(err.Error(), "authorization handle") {
		t.Fatalf("conflicting shared OAuth handle error = %v", err)
	}
}

func TestAutomaticInstallRequiresAutomaticChecks(t *testing.T) {
	t.Parallel()

	configuration := OutlookDefault()
	configuration.Updates.DisableAutomaticChecks = true
	configuration.Updates.AutoInstall = true
	if err := configuration.Validate(); err == nil ||
		!strings.Contains(err.Error(), "updates.auto_install") {
		t.Fatalf("contradictory update settings error = %v", err)
	}
}

func TestDefaultRejectsDefaultWithoutAccount(t *testing.T) {
	t.Parallel()

	configuration := Default()
	configuration.DefaultAccount = "work"
	if err := configuration.Validate(); err == nil {
		t.Fatal("empty configuration accepted a dangling default account")
	}
}

func TestValidateRejectsAliasThatLooksLikeOpaqueAccountID(t *testing.T) {
	t.Parallel()

	configuration := OutlookDefault()
	account := configuration.Accounts["work"]
	delete(configuration.Accounts, "work")
	alias := "acc_11111111111111111111111111111111"
	configuration.Accounts[alias] = account
	configuration.DefaultAccount = alias
	if err := configuration.Validate(); err == nil {
		t.Fatal("opaque account ID form was accepted as an alias")
	}
}

func TestMonitorConsentBoundariesValidateIndependently(t *testing.T) {
	t.Parallel()

	configuration := OutlookDefault()
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

	configuration := OutlookDefault()
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
	want := OutlookDefault()
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
	versionLine := fmt.Sprintf("version = %d\n", CurrentVersion)
	encoded := []byte("# retained\n" + versionLine)
	defaultConfig, err := toml.Marshal(OutlookDefault())
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, bytes.TrimPrefix(defaultConfig, []byte(versionLine))...)
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
	configuration := OutlookDefault()
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
		configuration := OutlookDefault()
		mutate(&configuration)
		if err := configuration.Validate(); err == nil {
			t.Fatalf("case %d unexpectedly passed validation: %+v", index, configuration)
		}
	}
}

func TestNewOutlookDefaultUsesFreshOpaqueAccountID(t *testing.T) {
	t.Parallel()

	first, err := NewOutlookDefault()
	if err != nil {
		t.Fatalf("NewOutlookDefault() error = %v", err)
	}
	second, err := NewOutlookDefault()
	if err != nil {
		t.Fatalf("NewOutlookDefault() second error = %v", err)
	}
	firstID := first.Accounts[first.DefaultAccount].ID
	secondID := second.Accounts[second.DefaultAccount].ID
	if err := firstID.ValidateOpaque(); err != nil {
		t.Fatalf("first ID = %q: %v", firstID, err)
	}
	if firstID == secondID {
		t.Fatalf("NewOutlookDefault() repeated account ID %q", firstID)
	}
}

func TestResolveAccountSupportsAliasAndStableID(t *testing.T) {
	t.Parallel()

	configuration := OutlookDefault()
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

func TestLoadMigratesV3GoogleAPIToGmailXOAUTH2(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	v3 := []byte(`
version = 3
default_account = "personal"

[accounts.personal]
id = "acc_00000000000000000000000000000003"
address = "reader@gmail.com"

[accounts.personal.mail]
provider = "google-api"

[accounts.personal.mail.google_api]
api_base = "https://www.googleapis.com"
client_id = "synthetic-google-public-client"
redirect_uri = "http://127.0.0.1:43123/oauth/callback"

[accounts.personal.mail.google_api.authorization]
backend = "os-keyring"
key = "google-personal-oauth"
consent = true

[accounts.personal.calendar]
provider = "google-api"

[accounts.personal.calendar.google_api]
api_base = "https://www.googleapis.com"
client_id = "synthetic-google-public-client"
redirect_uri = "http://127.0.0.1:43123/oauth/callback"

[accounts.personal.calendar.google_api.authorization]
backend = "os-keyring"
key = "google-personal-oauth"
consent = true

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
disable_automatic_checks = true
`)
	if err := os.WriteFile(path, v3, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["personal"]
	if configuration.Version != CurrentVersion ||
		configuration.DefaultAccount != "personal" ||
		account.ID != "acc_00000000000000000000000000000003" ||
		account.MailProvider() != domain.ProviderGoogle ||
		account.CalendarProvider() != domain.ProviderGoogle ||
		account.Mail.Google == nil ||
		account.Mail.Google.Username != "reader@gmail.com" ||
		account.Mail.Google.Mailbox != "" ||
		account.Mail.Google.ClientID != "synthetic-google-public-client" ||
		account.Mail.Google.Authorization.Key != "google-personal-oauth" ||
		account.Calendar.Google == nil ||
		account.Calendar.Google.APIBase != "https://www.googleapis.com" ||
		account.Mail.Google.Client() != account.Calendar.Google.Client() ||
		!configuration.Updates.DisableAutomaticChecks {
		t.Fatalf("migrated v3 Google config = %+v", configuration)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, v3) {
		t.Fatal("read-only Load modified the v3 source")
	}
}

func TestLoadMigratesV4ToStableUpdateChannel(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	v4 := []byte(`
version = 4
default_account = ""

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
auto_install = true
`)
	if err := os.WriteFile(path, v4, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Version != CurrentVersion ||
		configuration.Updates.Channel != UpdateChannelStable ||
		!configuration.Updates.AutoInstall {
		t.Fatalf("migrated v4 config = %+v", configuration)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, v4) {
		t.Fatal("read-only Load modified the v4 source")
	}
}

func TestLoadMigratesV5WithoutSelectingTaskRoute(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	v5 := []byte(`
version = 5
default_account = "work"

[accounts.work]
id = "acc_00000000000000000000000000000001"
address = "reader@example.invalid"

[accounts.work.mail]
provider = "microsoft-owa"

[accounts.work.mail.outlook_web]
origin = "https://outlook.example.invalid"

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
channel = "preview"

[feedback]
auto_submit = true
`)
	if err := os.WriteFile(path, v5, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configuration.Accounts["work"]
	if configuration.Version != CurrentVersion || account.Tasks != nil ||
		configuration.Updates.Channel != UpdateChannelPreview ||
		!configuration.Feedback.AutoSubmit {
		t.Fatalf("migrated v5 config = %+v", configuration)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, v5) {
		t.Fatal("read-only Load modified the v5 source")
	}
}

func TestOlderConfigCannotDeclareTaskRoute(t *testing.T) {
	t.Parallel()
	for _, version := range []int{4, 5} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("v%d.toml", version))
		data := []byte(fmt.Sprintf(`
version = %d
default_account = "tasks"

[accounts.tasks]
id = "acc_00000000000000000000000000000009"

[accounts.tasks.tasks]
provider = "todoist"
`, version))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "strict mode") {
			t.Fatalf("Load(v%d with task route) error = %v", version, err)
		}
	}
}

func TestV6MigrationPreservesTaskRouteAndRejectsCalDAVPayload(t *testing.T) {
	t.Parallel()
	const base = `
version = 6
default_account = "tasks"

[accounts.tasks]
id = "acc_00000000000000000000000000000009"
address = "reader@example.invalid"

[accounts.tasks.tasks]
provider = "todoist"

[accounts.tasks.tasks.todoist]
read_only = true

[accounts.tasks.tasks.todoist.oauth]
api_base = "https://api.todoist.com/api/v1"
client_id = "synthetic-public-client"
redirect_uri = "http://127.0.0.1:43123/callback"

[accounts.tasks.tasks.todoist.oauth.authorization]
backend = "os-keyring"
key = "tasks-todoist"
consent = true

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
channel = "stable"
`
	configuration, err := MigrateV6([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Version != CurrentVersion ||
		configuration.Accounts["tasks"].Tasks.Todoist == nil ||
		!configuration.Accounts["tasks"].Tasks.Todoist.ReadOnly {
		t.Fatalf("migrated v6 config = %+v", configuration)
	}
	withCalDAV := strings.Replace(
		base,
		"[policy]",
		"[accounts.tasks.tasks.caldav]\nendpoint = \"https://dav.example.invalid/\"\nusername = \"reader\"\n\n[policy]",
		1,
	)
	if _, err := MigrateV6([]byte(withCalDAV)); err == nil ||
		!strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("v6 CalDAV task payload error = %v", err)
	}
}

func TestV7MigrationPreservesCalDAVTaskRouteAndRejectsGooglePayload(t *testing.T) {
	t.Parallel()
	const base = `
version = 7
default_account = "tasks"

[accounts.tasks]
id = "acc_00000000000000000000000000000009"
address = "reader@example.invalid"

[accounts.tasks.tasks]
provider = "caldav"

[accounts.tasks.tasks.caldav]
endpoint = "https://dav.example.invalid/"
task_list_path = "/tasks/work/"
username = "reader@example.invalid"

[accounts.tasks.tasks.caldav.credential]
backend = "os-keyring"
key = "tasks-caldav"
consent = true

[policy]
mode = "guarded"
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m"

[updates]
channel = "stable"
`
	configuration, err := MigrateV7([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	route := configuration.Accounts["tasks"].Tasks
	if configuration.Version != CurrentVersion || route == nil ||
		route.CalDAV == nil || route.CalDAV.TaskListPath != "/tasks/work/" {
		t.Fatalf("migrated v7 config = %+v", configuration)
	}
	withGoogleTasks := strings.Replace(
		base,
		"[policy]",
		"[accounts.tasks.tasks.google_tasks]\nread_only = true\n\n[policy]",
		1,
	)
	if _, err := MigrateV7([]byte(withGoogleTasks)); err == nil ||
		!strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("v7 Google Tasks payload error = %v", err)
	}
}

func TestMigrateV4RejectsInventedChannelField(t *testing.T) {
	t.Parallel()
	_, err := MigrateV4([]byte(`
version = 4
[updates]
channel = "preview"
`))
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("MigrateV4() error = %v", err)
	}
}

func TestMigrateV3RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	_, err := MigrateV3([]byte(`
version = 3
unexpected = "must-not-be-accepted"
`))
	if err == nil {
		t.Fatalf("MigrateV3() error = %v", err)
	}
}

func TestLegacyMigrationsCannotPreauthorizeAutomaticInstallation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version int
		migrate func([]byte) (Config, error)
	}{
		{"v1", 1, MigrateV1},
		{"v2", 2, MigrateV2},
		{"v3", 3, MigrateV3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.migrate([]byte(fmt.Sprintf(`
version = %d

[updates]
auto_install = true
`, test.version)))
			if err == nil || !strings.Contains(err.Error(), "strict mode") {
				t.Fatalf("legacy auto-install consent error = %v", err)
			}
		})
	}
}

func TestMixedStandardsRoutesAreStrictAndSecretFree(t *testing.T) {
	t.Parallel()
	configuration := OutlookDefault()
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
	relativeHelper := configuration
	relativeHelper.Credentials.Helper = []string{"example-helper", "get"}
	if err := relativeHelper.Validate(); err == nil {
		t.Fatal("relative credential helper executable was accepted")
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

func TestGoogleMailOAuthRouteRequiresLoopbackAndOSKeyring(t *testing.T) {
	t.Parallel()
	configuration := OutlookDefault()
	configuration.Accounts["work"] = Account{
		ID:      defaultAccountID,
		Address: "reader@example.test",
		Mail: &MailRoute{
			Provider: domain.ProviderGoogle,
			Google: &GoogleMailRoute{
				Username:    "reader@example.test",
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
	account.Mail.Google.Username = "other@example.test"
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("Google username different from the account address was accepted")
	}
	account.Mail.Google.Username = account.Address
	configuration.Accounts["work"] = account

	oauth := *account.Mail.Google
	oauth.RedirectURI = "http://127.0.0.1:43123/callback?leak=true"
	account.Mail.Google = &oauth
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("OAuth redirect query was accepted")
	}

	oauth.RedirectURI = "http://127.0.0.1:43123/callback"
	account.Mail.Google = &oauth
	account.Mail.Google.Authorization.Backend = CredentialHelper
	configuration.Accounts["work"] = account
	if err := configuration.Validate(); err == nil {
		t.Fatal("OAuth helper-backed grant was accepted")
	}
}

func TestGoogleTaskRouteRoundTripsWithPinnedIndependentGrant(t *testing.T) {
	t.Parallel()
	configuration := OutlookDefault()
	configuration.Accounts["tasks"] = Account{
		ID:      "acc_00000000000000000000000000000112",
		Address: "reader@example.test",
		Tasks: &TaskRoute{
			Provider: domain.ProviderGoogleTasks,
			GoogleTasks: &GoogleTaskRoute{
				ReadOnly: true,
				OAuth: OAuthRoute{
					APIBase:     "https://tasks.googleapis.com",
					ClientID:    "synthetic.apps.googleusercontent.com",
					RedirectURI: "http://127.0.0.1:43123/callback",
					Authorization: CredentialRef{
						Backend: CredentialOSKeyring, Key: "google-tasks", Consent: true,
					},
				},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	route := loaded.Accounts["tasks"].Tasks
	if route == nil || route.Provider != domain.ProviderGoogleTasks ||
		route.GoogleTasks == nil || !route.GoogleTasks.ReadOnly ||
		route.GoogleTasks.OAuth.Authorization.Key != "google-tasks" {
		t.Fatalf("Google Tasks round trip = %+v", loaded.Accounts["tasks"])
	}
	account := configuration.Accounts["tasks"]
	account.Tasks.GoogleTasks.OAuth.APIBase = "https://example.test"
	configuration.Accounts["tasks"] = account
	if err := configuration.Validate(); err == nil ||
		!strings.Contains(err.Error(), "tasks.googleapis.com") {
		t.Fatalf("unpinned Google Tasks API base error = %v", err)
	}
}

func TestGoogleWebRoutesRequireExactProviderOwnedOrigins(t *testing.T) {
	t.Parallel()
	configuration := OutlookDefault()
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
	if err := Save(path, OutlookDefault()); err == nil {
		t.Fatal("Save() unexpectedly accepted directory target")
	}
}

func TestCreateNeverReplacesExistingConfiguration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	created, err := Create(t.Context(), path, Default())
	if err != nil || !created {
		t.Fatalf("Create(first) = %t, %v", created, err)
	}
	before, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	created, err = Create(t.Context(), path, OutlookDefault())
	if err != nil || created {
		t.Fatalf("Create(existing) = %t, %v", created, err)
	}
	after, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Create replaced an existing configuration")
	}
}

func TestLoadMissingFilePreservesCause(t *testing.T) {
	t.Parallel()

	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want os.ErrNotExist", err)
	}
}
