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
	configuration.Feedback.AutoSubmit = true
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	store := Store{ConfigPath: path}
	view, err := store.GetSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.DefaultAccount != "work" || view.AutomaticChecks || !view.FeedbackAutoSubmit ||
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

func TestServiceSwitchRetainsConfigurationAndRejectsStaleReview(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	configuration := config.OutlookDefault()
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	service, err := application.NewSettingsService(Store{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	key := application.AccountServicesSettingKey(configuration.Accounts["work"].ID)
	review, err := service.Review(t.Context(), application.SettingsUpdateInput{Key: key, Value: "calendar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := saved.Accounts["work"]
	if account.Mail == nil || account.Calendar == nil || account.ServiceEnabled("mail") || !account.ServiceEnabled("calendar") {
		t.Fatalf("switched account = %+v", account)
	}
	if _, err := service.Apply(t.Context(), review); err == nil {
		t.Fatal("stale selection applied")
	}
	if _, err := service.Review(t.Context(), application.SettingsUpdateInput{Key: key, Value: "messages"}); err == nil {
		t.Fatal("unconfigured route enabled")
	}
	review, err = service.Review(t.Context(), application.SettingsUpdateInput{Key: key, Value: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), review); err != nil {
		t.Fatal(err)
	}
	review, err = service.Review(t.Context(), application.SettingsUpdateInput{Key: key, Value: "mail,calendar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(t.Context(), review); err != nil {
		t.Fatal(err)
	}
}
