package settingsstore

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
)

func TestStoreProjectsAndAtomicallyUpdatesSettings(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	configuration := config.OutlookDefault()
	configuration.Updates.DisableAutomaticChecks = true
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	view, err := store.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.DefaultAccount != "work" || view.AutomaticChecks ||
		len(view.Accounts) != 1 || !view.Accounts[0].IsDefault {
		t.Fatalf("GetSettings() = %+v", view)
	}
	review := application.SettingsChangeReview{
		Key:      application.SettingUpdateInstall,
		Previous: "false", Value: "true",
	}
	if err := store.UpdateSettings(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load(path)
	if err != nil || !updated.Updates.AutoInstall ||
		updated.Updates.DisableAutomaticChecks {
		t.Fatalf("updated config = %+v, %v", updated.Updates, err)
	}
}

func TestStoreRejectsStaleSettingsReview(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	err := (Store{ConfigPath: path}).UpdateSettings(
		t.Context(),
		application.SettingsChangeReview{
			Key:      application.SettingUpdateChannel,
			Previous: "preview", Value: "stable",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
}
