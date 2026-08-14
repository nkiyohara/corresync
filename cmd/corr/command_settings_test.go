package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func TestSettingsRenamesAccountInteractively(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n2\n3\noffice\n3\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["work"]; exists {
		t.Fatal("old account alias remained configured")
	}
	if _, exists := configuration.Accounts["office"]; !exists || configuration.DefaultAccount != "office" {
		t.Fatalf("renamed settings = %+v", configuration)
	}
	if !strings.Contains(stdout.String(), "Account renamed to office") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsChangesEverydayConfiguration(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader(
		"2\n1\n2\n" +
			"2\n2\n2\n" +
			"3\n2\n" +
			"4\n10m\n" +
			"8\n",
	)
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Updates.Channel != config.UpdateChannelPreview ||
		configuration.Updates.AutoInstall ||
		!configuration.Updates.DisableAutomaticChecks ||
		configuration.Policy.Mode != policy.ModeReadOnly ||
		time.Duration(configuration.Browser.LoginTimeout) != 10*time.Minute {
		t.Fatalf("saved settings = %+v", configuration)
	}
}

func TestSettingsEnablingAutomaticInstallAlsoEnablesChecks(t *testing.T) {
	path := saveSettingsFixture(t)
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Updates.DisableAutomaticChecks = true
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("2\n3\n2\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err = config.Load(path)
	if err != nil || !configuration.Updates.AutoInstall ||
		configuration.Updates.DisableAutomaticChecks {
		t.Fatalf("saved automatic update settings = %+v, %v", configuration.Updates, err)
	}
	if !strings.Contains(stdout.String(), "Automatic checks were also enabled") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsEnablesAutomaticFeedbackOnlyAfterPublicConsent(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("5\n2\ny\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment
	app.runCommand = func(
		_ context.Context,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		if name != "gh" || strings.Join(arguments, " ") != "auth status --hostname github.com" {
			t.Fatalf("prerequisite command = %s %v", name, arguments)
		}
		return nil
	}
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil || !configuration.Feedback.AutoSubmit {
		t.Fatalf("saved automatic feedback = %+v, %v", configuration.Feedback, err)
	}
	if !strings.Contains(stdout.String(), "GitHub username and the generated issue will be public") ||
		!strings.Contains(stdout.String(), "Feedback setting updated") {
		t.Fatalf("settings consent output = %q", stdout.String())
	}
}

func TestSettingsRepromptsInvalidAliasAndLoginTimeout(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n2\n3\n \noffice\n3\n4\nsoon\n10m\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil || configuration.DefaultAccount != "office" ||
		time.Duration(configuration.Browser.LoginTimeout) != 10*time.Minute {
		t.Fatalf("saved settings = %+v, %v", configuration, err)
	}
	if !strings.Contains(stdout.String(), "account alias must not be empty") ||
		!strings.Contains(stdout.String(), "duration such as") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsRequiresInteractiveInputAndOutput(t *testing.T) {
	path := saveSettingsFixture(t)
	app := newRuntime(t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("q\n")
	app.interactiveInput = func() bool { return false }
	app.interactiveStdout = func() bool { return true }
	if err := (&settingsCommand{}).Run(app); err == nil ||
		!strings.Contains(err.Error(), "corr account rename") {
		t.Fatalf("non-interactive settings error = %v", err)
	}
}

func TestSettingsAlwaysOffersAccountSetup(t *testing.T) {
	for _, accounts := range [][]application.SettingsAccount{
		nil,
		{{Alias: "work", IsDefault: true}},
	} {
		options := accountsSettingsMenuOptions(application.SettingsView{Accounts: accounts})
		if !hasSettingsOption(options, settingsActionSetup) {
			t.Fatalf("account settings options without setup = %+v", options)
		}
	}
}

func TestSettingsTopLevelUsesAccountCategory(t *testing.T) {
	settings := application.SettingsView{
		Accounts:       []application.SettingsAccount{{Alias: "work", IsDefault: true}},
		DefaultAccount: "work",
	}
	options := settingsMenuOptions(settings)
	if !hasSettingsOption(options, settingsActionAccounts) ||
		!hasSettingsOption(options, settingsActionFeedback) ||
		!hasSettingsOption(options, settingsActionSetup) ||
		hasSettingsOption(options, settingsAccountPrefix+"work") {
		t.Fatalf("top-level settings hierarchy = %+v", options)
	}
}

func TestSettingsRemovesNonDefaultAccountAfterConfirmation(t *testing.T) {
	path := saveSettingsFixtureWithSecondAccount(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n3\n5\ny\n3\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["zeta"]; exists ||
		configuration.DefaultAccount != "work" {
		t.Fatalf("settings after removal = %+v", configuration)
	}
	if !strings.Contains(stdout.String(), "Review account removal") ||
		!strings.Contains(stdout.String(), "Removed account zeta") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsRequiresReplacementBeforeRemovingDefault(t *testing.T) {
	path := saveSettingsFixtureWithSecondAccount(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n2\n4\n1\ny\n3\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configuration.Accounts["work"]; exists ||
		configuration.DefaultAccount != "zeta" {
		t.Fatalf("settings after default removal = %+v", configuration)
	}
	if !strings.Contains(stdout.String(), "New default: zeta") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsCancelsAccountRemovalByDefault(t *testing.T) {
	path := saveSettingsFixtureWithSecondAccount(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n3\n5\nn\n4\n8\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil || len(configuration.Accounts) != 2 {
		t.Fatalf("settings after canceled removal = %+v, %v", configuration, err)
	}
}

func TestValidateSettingsEmailAddress(t *testing.T) {
	for _, value := range []string{"", "person", "Person <person@example.com>", "a@b\n"} {
		if err := validateSettingsEmailAddress(value); err == nil {
			t.Fatalf("validateSettingsEmailAddress(%q) accepted invalid input", value)
		}
	}
	if err := validateSettingsEmailAddress("person@example.com"); err != nil {
		t.Fatalf("valid address rejected: %v", err)
	}
}

func hasSettingsOption(options []huh.Option[string], value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func settingsTestEnvironment(key string) (string, bool) {
	if key == "CORRESYNC_ACCESSIBLE" {
		return "true", true
	}
	return "", false
}

func saveSettingsFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/config.toml"
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	return path
}

func saveSettingsFixtureWithSecondAccount(t *testing.T) string {
	t.Helper()
	path := saveSettingsFixture(t)
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := domain.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	second := configuration.Accounts["work"]
	second.ID = accountID
	configuration.Accounts["zeta"] = second
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}
	return path
}
