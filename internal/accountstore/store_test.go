package accountstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	configuration := config.Default()
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

func TestRemoveAccountPinsReplacementDefaultByStableID(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
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
	configuration := config.Default()
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
	const targetID domain.AccountID = "acc_00000000000000000000000000000002"
	configuration.Accounts["target"] = config.Account{
		ID: targetID, Address: "target@example.test",
		Mail: &config.MailRoute{
			Provider:  domain.ProviderGoogleAPI,
			GoogleAPI: route("target-only"),
		},
		Calendar: &config.CalendarRoute{
			Provider:  domain.ProviderGoogleAPI,
			GoogleAPI: route("shared"),
		},
	}
	configuration.Accounts["other"] = config.Account{
		ID:      "acc_00000000000000000000000000000003",
		Address: "other@example.test",
		Calendar: &config.CalendarRoute{
			Provider:  domain.ProviderGoogleAPI,
			GoogleAPI: route("shared"),
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
	if len(deleted) != 1 || deleted[0] != "target-only" {
		t.Fatalf("deleted OAuth keys = %#v", deleted)
	}
}

func TestStoreRedactsCredentialLookupDetailsFromAccountViews(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
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

func TestAddAccountAtomicallyRejectsCrossAccountCredentialReuse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	configuration := config.Default()
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
