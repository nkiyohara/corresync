package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/policy"
)

func TestSettingsRenamesAccountInteractively(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n1\noffice\nq\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = func(string) (string, bool) { return "", false }

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
		"3\n2\n" +
			"5\n2\n" +
			"6\n2\n" +
			"7\n10m\n" +
			"q\n",
	)
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = func(string) (string, bool) { return "", false }

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
	app.stdin = strings.NewReader("4\n2\nq\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = func(string) (string, bool) { return "", false }
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err = config.Load(path)
	if err != nil || !configuration.Updates.AutoInstall ||
		configuration.Updates.DisableAutomaticChecks {
		t.Fatalf("saved automatic update settings = %+v, %v", configuration.Updates, err)
	}
	if !strings.Contains(stdout.String(), "automatic checks = on (required)") {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func TestSettingsRepromptsInvalidAliasAndLoginTimeout(t *testing.T) {
	path := saveSettingsFixture(t)
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n1\n \noffice\n7\nsoon\n10m\nq\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = func(string) (string, bool) { return "", false }
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil || configuration.DefaultAccount != "office" ||
		time.Duration(configuration.Browser.LoginTimeout) != 10*time.Minute {
		t.Fatalf("saved settings = %+v, %v", configuration, err)
	}
	if !strings.Contains(stdout.String(), "account alias must not be empty") ||
		!strings.Contains(stdout.String(), "as duration") {
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

func TestSettingsExplainsAccountActionsBeforeSetup(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("1\n2\nq\n")
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = func(string) (string, bool) { return "", false }
	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout.String(), "corr setup <email-address>") != 2 {
		t.Fatalf("settings output = %q", stdout.String())
	}
}

func saveSettingsFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/config.toml"
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	return path
}
