package accountstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

func TestStoreLifecyclePreservesStableID(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	account := application.AccountRegistration{
		ID: "acc_00000000000000000000000000000002", Alias: "personal",
		Address: "reader@example.invalid",
		Mail: &application.AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &application.AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
		Calendar: &application.AccountCalendarRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &application.AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
	}
	if err := store.AddAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAccount(context.Background(), account.ID, "home"); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.ListAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, current := range catalog.Accounts {
		if current.ID == account.ID {
			found = current.Alias == "home"
		}
	}
	if !found {
		t.Fatalf("catalog = %#v", catalog)
	}
	if err := store.RemoveAccount(context.Background(), account.ID, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveFinalAccountClearsDefault(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	accountID := configuration.Accounts[configuration.DefaultAccount].ID
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	if err := store.RemoveAccount(t.Context(), accountID, accountID); err == nil {
		t.Fatal("RemoveAccount() accepted a replacement for the final account")
	}
	if err := store.RemoveAccount(t.Context(), accountID, ""); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Accounts) != 0 || updated.DefaultAccount != "" {
		t.Fatalf("final account removal = %+v", updated)
	}
}

func TestListAccountsExposesConfiguredTaskRouteAsUnavailable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = config.Account{
		ID:    "acc_00000000000000000000000000000009",
		Tasks: &config.TaskRoute{Provider: domain.ProviderThings},
	}
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	catalog, err := (Store{ConfigPath: path}).ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Accounts) != 1 || catalog.Accounts[0].Tasks == nil ||
		catalog.Accounts[0].Tasks.Provider != domain.ProviderThings ||
		catalog.Accounts[0].Tasks.Available {
		t.Fatalf("task account view = %+v", catalog)
	}
}

func TestStoreRoundTripsMessagingRouteWithoutCredentialHandleDisclosure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	registration := application.AccountRegistration{
		ID: "acc_00000000000000000000000000000145", Alias: "slack",
		Address: "reader@example.invalid", IsDefault: true,
		Messages: &application.AccountMessagingRouteInput{
			Provider: domain.MessagingProviderSlack,
			Slack: &application.AccountSlackMessagingInput{
				APIBase: "https://slack.com/api", WorkspaceID: "workspace-synthetic-1",
				Authorization: application.AccountCredentialInput{
					Backend: "os-keyring", Key: "private-slack-handle", Consent: true,
				},
				ReadOnly: true,
			},
		},
	}
	if err := store.AddAccount(t.Context(), registration); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Accounts) != 1 || catalog.Accounts[0].Messages == nil ||
		catalog.Accounts[0].Messages.Provider != domain.MessagingProviderSlack ||
		catalog.Accounts[0].Messages.Route != domain.MessagingRouteSlackAPI ||
		catalog.Accounts[0].Messages.WorkspaceID != "workspace-synthetic-1" ||
		catalog.Accounts[0].Messages.Credential == nil ||
		catalog.Accounts[0].Messages.Credential.Backend != "os-keyring" {
		t.Fatalf("messaging account view = %+v", catalog)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-slack-handle") {
		t.Fatalf("account catalog exposed a messaging credential handle: %s", encoded)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Accounts["slack"].Messages == nil ||
		reloaded.Accounts["slack"].Messages.Slack.Authorization.Key != "private-slack-handle" {
		t.Fatalf("persisted messaging route = %+v", reloaded.Accounts["slack"].Messages)
	}
}

func TestRemoveAccountPinsReplacementDefaultByStableID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	replacement := configuration.Accounts["work"]
	replacement.ID = "acc_00000000000000000000000000000002"
	other := replacement
	other.ID = "acc_00000000000000000000000000000003"
	configuration.Accounts["replacement"] = replacement
	configuration.Accounts["other"] = other
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	if err := store.RenameAccount(
		t.Context(),
		replacement.ID,
		"replacement-renamed",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAccount(t.Context(), other.ID, "replacement"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAccount(
		t.Context(),
		configuration.Accounts["work"].ID,
		replacement.ID,
	); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefaultAccount != "replacement-renamed" ||
		updated.Accounts[updated.DefaultAccount].ID != replacement.ID {
		t.Fatalf(
			"default = %q/%q, want replacement-renamed/%q",
			updated.DefaultAccount,
			updated.Accounts[updated.DefaultAccount].ID,
			replacement.ID,
		)
	}
}

func TestPurgeAccountStateRemovesOnlyDerivedRoots(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CORRESYNC_STATE_DIR", state)
	accountID := domain.AccountID("acc_00000000000000000000000000000001")
	profile, err := paths.ProfileDir(accountID)
	if err != nil {
		t.Fatal(err)
	}
	accountState, err := paths.AccountStateDir(accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{profile, accountState} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "synthetic"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(state, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Store{}).PurgeAccountState(context.Background(), accountID); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{profile, accountState} {
		if _, err := os.Lstat(directory); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", directory, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated state was removed: %v", err)
	}
}

func TestPurgeAccountStateRejectsSymlinkRoot(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CORRESYNC_STATE_DIR", state)
	accountID := domain.AccountID("acc_00000000000000000000000000000001")
	profile, err := paths.ProfileDir(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(profile), 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, profile); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := (Store{}).PurgeAccountState(context.Background(), accountID); err == nil {
		t.Fatal("PurgeAccountState() accepted a symlink root")
	}
}

func TestPurgeAccountStateDeletesOnlyUnsharedOAuthAuthorizations(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CORRESYNC_STATE_DIR", state)
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	route := func(key string) *config.OAuthRoute {
		return &config.OAuthRoute{
			APIBase:     "https://www.googleapis.com",
			ClientID:    "synthetic-public-client.apps.googleusercontent.com",
			RedirectURI: "http://127.0.0.1:43123/callback",
			Authorization: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     key,
				Consent: true,
			},
		}
	}
	graphRoute := func(key string) *config.OAuthRoute {
		result := route(key)
		result.APIBase = "https://graph.microsoft.com/v1.0"
		return result
	}
	googleTaskRoute := func(key string) *config.GoogleOAuthRoute {
		base := route(key)
		return &config.GoogleOAuthRoute{
			APIBase: "https://tasks.googleapis.com", ClientID: base.ClientID,
			RedirectURI: base.RedirectURI, Authorization: base.Authorization,
			ClientSecret: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     key + "-client", Consent: true,
			},
		}
	}
	const targetID domain.AccountID = "acc_00000000000000000000000000000002"
	configuration.Accounts["target"] = config.Account{
		ID: targetID, Address: "target@example.test",
		Mail: &config.MailRoute{
			Provider: domain.ProviderGoogle,
			Google: &config.GoogleMailRoute{
				Username:      "target@example.test",
				ClientID:      route("target-only").ClientID,
				RedirectURI:   route("target-only").RedirectURI,
				Authorization: route("target-only").Authorization,
				ClientSecret: config.CredentialRef{
					Backend: config.CredentialOSKeyring,
					Key:     "target-client", Consent: true,
				},
			},
		},
		Calendar: &config.CalendarRoute{
			Provider:       domain.ProviderMicrosoftGraph,
			MicrosoftGraph: graphRoute("shared"),
		},
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderGoogleTasks,
			GoogleTasks: &config.GoogleTaskRoute{
				OAuth: *googleTaskRoute("target-task"), ReadOnly: true,
			},
		},
	}
	configuration.Accounts["other"] = config.Account{
		ID:      "acc_00000000000000000000000000000003",
		Address: "other@example.test",
		Calendar: &config.CalendarRoute{
			Provider:       domain.ProviderMicrosoftGraph,
			MicrosoftGraph: graphRoute("shared"),
		},
	}
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	deleted := make([]string, 0, 1)
	store := Store{
		ConfigPath: path,
		DeleteOAuthAuthorization: func(key string) error {
			deleted = append(deleted, key)
			return nil
		},
	}
	if err := store.PurgeAccountState(t.Context(), targetID); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 || deleted[0] != "target-only" || deleted[1] != "target-task" {
		t.Fatalf("deleted OAuth keys = %#v", deleted)
	}
}

func TestPurgeAccountStateNeverDeletesExternalTickTickClientSecret(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("CORRESYNC_STATE_DIR", state)
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
	const accountID domain.AccountID = "acc_00000000000000000000000000000002"
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = config.Account{
		ID: accountID,
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderTickTick,
			TickTick: &config.TickTickTaskRoute{
				OAuth: config.TickTickOAuthRoute{
					APIBase:     "https://api.ticktick.com",
					ClientID:    "synthetic-confidential-client",
					RedirectURI: "http://127.0.0.1:43123/callback",
					Authorization: config.CredentialRef{
						Backend: config.CredentialOSKeyring,
						Key:     "ticktick-grant",
						Consent: true,
					},
					ClientSecret: config.CredentialRef{
						Backend: config.CredentialHelper,
						Key:     "externally-owned-client-secret",
						Consent: true,
					},
				},
			},
		},
	}
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	deleted := make([]string, 0, 1)
	store := Store{
		ConfigPath: path,
		DeleteOAuthAuthorization: func(key string) error {
			deleted = append(deleted, key)
			return nil
		},
	}
	if err := store.PurgeAccountState(t.Context(), accountID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []string{"ticktick-grant"}) {
		t.Fatalf("deleted credential keys = %#v", deleted)
	}
}

func TestStoreRedactsCredentialLookupDetailsFromAccountViews(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	configuration.Credentials.Helper = []string{
		filepath.Join(t.TempDir(), "private-helper-command"),
		"--private-profile",
	}
	configuration.Accounts["work"] = config.Account{
		ID:      configuration.Accounts["work"].ID,
		Address: "reader@example.invalid",
		Mail: &config.MailRoute{
			Provider: domain.ProviderJMAP,
			JMAP: &config.JMAPRoute{
				SessionURL: "https://jmap.example.invalid/session",
				Username:   "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialHelper,
					Key:     "private-credential-key",
					Consent: true,
				},
			},
		},
		Calendar: &config.CalendarRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &config.CalDAVRoute{
				Endpoint: "https://dav.example.invalid/",
				Username: "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialHelper,
					Key:     "private-calendar-key",
					Consent: true,
				},
			},
		},
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderCalDAV,
			CalDAV: &config.CalDAVTaskRoute{
				Endpoint: "https://dav.example.invalid/", TaskListPath: "/tasks/",
				Username: "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialHelper,
					Key:     "private-task-key",
					Consent: true,
				},
			},
		},
	}
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	catalog, err := (Store{ConfigPath: path}).ListAccounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	for _, private := range []string{
		"private-credential-key",
		"private-calendar-key",
		"private-task-key",
		"private-helper-command",
		"--private-profile",
	} {
		if strings.Contains(output, private) {
			t.Fatalf("account catalog exposed %q: %s", private, output)
		}
	}
	if !strings.Contains(output, `"backend":"helper"`) ||
		!strings.Contains(output, `"consented":true`) {
		t.Fatalf("account catalog omitted safe credential summary: %s", output)
	}
}

func TestAddAccountRejectsCrossAccountCalDAVTaskCredentialReuse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	work := configuration.Accounts["work"]
	work.Tasks = &config.TaskRoute{
		Provider: domain.ProviderCalDAV,
		CalDAV: &config.CalDAVTaskRoute{
			Endpoint: "https://work.example.invalid/dav", TaskListPath: "/tasks/",
			Username: "work@example.invalid",
			Credential: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     "work-task-handle",
				Consent: true,
			},
		},
	}
	configuration.Accounts["work"] = work
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	err := store.AddAccount(t.Context(), application.AccountRegistration{
		ID: "acc_00000000000000000000000000000002", Alias: "team",
		Tasks: &application.AccountTaskRouteInput{
			Provider: domain.ProviderCalDAV,
			CalDAV: &application.AccountCalDAVTaskInput{
				Endpoint: "https://team.example.invalid/dav", TaskListPath: "/tasks/",
				Username: "team@example.invalid",
				Credential: application.AccountCredentialInput{
					Backend: "os-keyring", Key: "work-task-handle", Consent: true,
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already belongs") {
		t.Fatalf("AddAccount() error = %v", err)
	}
	reloaded, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, exists := reloaded.Accounts["team"]; exists {
		t.Fatal("rejected CalDAV task account was persisted")
	}
}

func TestAddAccountAtomicallyRejectsCrossAccountCredentialReuse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.OutlookDefault()
	work := configuration.Accounts["work"]
	work.Address = "work@example.invalid"
	work.Mail = &config.MailRoute{
		Provider: domain.ProviderJMAP,
		JMAP: &config.JMAPRoute{
			SessionURL: "https://work.example.invalid/session",
			Username:   "work@example.invalid",
			Credential: config.CredentialRef{
				Backend: config.CredentialOSKeyring,
				Key:     "work-handle",
				Consent: true,
			},
		},
	}
	work.Calendar = nil
	configuration.Accounts["work"] = work
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	err := store.AddAccount(t.Context(), application.AccountRegistration{
		ID:      "acc_00000000000000000000000000000002",
		Alias:   "team",
		Address: "team@example.invalid",
		Mail: &application.AccountMailRouteInput{
			Provider: domain.ProviderJMAP,
			JMAP: &application.AccountJMAPInput{
				SessionURL: "https://attacker.example.invalid/session",
				Username:   "team@example.invalid",
				Credential: application.AccountCredentialInput{
					Backend: "os-keyring",
					Key:     "work-handle",
					Consent: true,
				},
			},
		},
	})
	if err == nil {
		t.Fatal("AddAccount() reused another account's credential handle")
	}
	reloaded, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, exists := reloaded.Accounts["team"]; exists {
		t.Fatal("rejected account was persisted")
	}
}
