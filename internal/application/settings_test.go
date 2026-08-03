package application

import (
	"context"
	"strings"
	"testing"
)

type fakeSettingsRepository struct {
	view   SettingsView
	review SettingsChangeReview
	err    error
}

func (repository *fakeSettingsRepository) GetSettings(context.Context) (SettingsView, error) {
	return repository.view, repository.err
}

func (repository *fakeSettingsRepository) UpdateSettings(
	_ context.Context,
	review SettingsChangeReview,
) error {
	repository.review = review
	if review.Key == SettingUpdateInstall {
		repository.view.AutomaticInstall = review.Value == "true"
		if repository.view.AutomaticInstall {
			repository.view.AutomaticChecks = true
		}
	}
	return repository.err
}

func TestSettingsServiceReviewsFriendlyValuesAndDependencies(t *testing.T) {
	repository := &fakeSettingsRepository{view: settingsFixture()}
	repository.view.AutomaticChecks = false
	service, err := NewSettingsService(repository)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.Review(t.Context(), SettingsUpdateInput{
		Key: SettingUpdateInstall, Value: "on",
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Previous != "false" || review.Value != "true" ||
		review.Command != "corr config set updates.auto_install true" ||
		len(review.RelatedChanges) != 1 ||
		review.RelatedChanges[0].Key != SettingUpdateChecks ||
		!review.RestartsSessions {
		t.Fatalf("review = %+v", review)
	}
	settings, err := service.Apply(t.Context(), review)
	if err != nil || !settings.AutomaticInstall || !settings.AutomaticChecks {
		t.Fatalf("Apply() = %+v, %v", settings, err)
	}
}

func TestSettingsServiceRejectsUnsafeOrUnclearChanges(t *testing.T) {
	repository := &fakeSettingsRepository{view: settingsFixture()}
	repository.view.AutomaticInstall = true
	service, err := NewSettingsService(repository)
	if err != nil {
		t.Fatal(err)
	}
	tests := []SettingsUpdateInput{
		{Key: SettingUpdateChecks, Value: "off"},
		{Key: SettingLoginTimeout, Value: "31m"},
		{Key: SettingSafetyMode, Value: "unsafe"},
		{Key: SettingDefaultAccount, Value: "missing"},
		{Key: "unknown", Value: "true"},
	}
	for _, input := range tests {
		if _, err := service.Review(t.Context(), input); err == nil {
			t.Fatalf("Review(%+v) succeeded", input)
		}
	}
}

func TestSettingsServiceRejectsNoop(t *testing.T) {
	service, err := NewSettingsService(&fakeSettingsRepository{view: settingsFixture()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Review(t.Context(), SettingsUpdateInput{
		Key: SettingUpdateChannel, Value: "stable",
	})
	if err == nil || !strings.Contains(err.Error(), "already stable") {
		t.Fatalf("Review() error = %v", err)
	}
}

func settingsFixture() SettingsView {
	return SettingsView{
		Accounts: []SettingsAccount{{
			Alias: "work", Address: "person@example.com", IsDefault: true,
		}},
		DefaultAccount: "work", UpdateChannel: "stable",
		AutomaticChecks: true, AutomaticInstall: false,
		SafetyMode: "guarded", LoginTimeout: "5m0s",
	}
}
