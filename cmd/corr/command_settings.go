package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumSettingsInputBytes = 4 << 10

type settingsCommand struct{}

type settingsReader struct {
	view    consoleView
	scanner *bufio.Scanner
}

type settingsOption struct {
	label string
	value string
}

func (command *settingsCommand) Run(app *runtime) error {
	if !app.interactiveInput() || !app.interactiveStdout() {
		return errors.New(
			"settings requires an interactive terminal; use `corr account rename`, " +
				"`corr config set`, or `corr config edit` in scripts",
		)
	}
	reader := newSettingsReader(app)
	for {
		configuration, path, err := app.loadConfig()
		if err != nil {
			return err
		}
		if err := writeSettingsMenu(reader.view, configuration); err != nil {
			return err
		}
		choice, err := reader.line("Choose 1-7, or q to finish: ")
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "1":
			if err := renameAccountSetting(app, reader, configuration); err != nil {
				return err
			}
		case "2":
			if err := changeDefaultAccount(app, reader, configuration, path); err != nil {
				return err
			}
		case "3":
			if err := chooseConfigSetting(app, reader, configuration, path, "updates.channel", []settingsOption{
				{label: "Stable releases", value: "stable"},
				{label: "Preview releases (alpha, beta, and RC)", value: "preview"},
			}); err != nil {
				return err
			}
		case "4":
			if err := changeAutomaticInstall(app, reader, configuration, path); err != nil {
				return err
			}
		case "5":
			if err := changeAutomaticChecks(app, reader, configuration, path); err != nil {
				return err
			}
		case "6":
			if err := chooseConfigSetting(app, reader, configuration, path, "policy.mode", []settingsOption{
				{label: "Guarded (writes require the normal safety policy)", value: "guarded"},
				{label: "Read-only (block every write)", value: "read_only"},
			}); err != nil {
				return err
			}
		case "7":
			if err := changeLoginTimeout(app, reader, configuration, path); err != nil {
				return err
			}
		case "q", "quit", "done", "exit":
			_, err := reader.view.printf(
				"\n%s  %s\n",
				reader.view.success(),
				reader.view.strong("Done"),
			)
			return err
		default:
			if _, err := reader.view.printf(
				"\n%s  Choose a number from 1 to 7, or q.\n",
				reader.view.warning(),
			); err != nil {
				return err
			}
		}
	}
}

func newSettingsReader(app *runtime) *settingsReader {
	scanner := bufio.NewScanner(io.LimitReader(app.stdin, maximumSettingsInputBytes+1))
	scanner.Buffer(make([]byte, 256), maximumSettingsInputBytes)
	return &settingsReader{
		view: newConsoleView(app, app.stdout, true), scanner: scanner,
	}
}

func (reader *settingsReader) line(prompt string) (string, error) {
	if _, err := reader.view.printf("%s", reader.view.command(prompt)); err != nil {
		return "", err
	}
	if !reader.scanner.Scan() {
		if err := reader.scanner.Err(); err != nil {
			return "", fmt.Errorf("read settings input: %w", err)
		}
		return "", io.EOF
	}
	return strings.TrimSpace(reader.scanner.Text()), nil
}

func (reader *settingsReader) choose(
	prompt string,
	options []settingsOption,
) (settingsOption, bool, error) {
	if len(options) == 0 {
		return settingsOption{}, false, errors.New("no choices are available")
	}
	for index, option := range options {
		if _, err := reader.view.printf("  %d. %s\n", index+1, option.label); err != nil {
			return settingsOption{}, false, err
		}
	}
	for {
		value, err := reader.line(prompt + " (b to go back): ")
		if err != nil {
			return settingsOption{}, false, err
		}
		if strings.EqualFold(value, "b") || strings.EqualFold(value, "back") {
			return settingsOption{}, false, nil
		}
		selected, parseErr := strconv.Atoi(value)
		if parseErr == nil && selected >= 1 && selected <= len(options) {
			return options[selected-1], true, nil
		}
		if _, err := reader.view.printf(
			"%s  Choose a number from 1 to %d, or b.\n",
			reader.view.warning(),
			len(options),
		); err != nil {
			return settingsOption{}, false, err
		}
	}
}

func writeSettingsMenu(view consoleView, configuration config.Config) error {
	defaultAccount := configuration.DefaultAccount
	if defaultAccount == "" {
		defaultAccount = "none"
	}
	checks := "on"
	if configuration.Updates.DisableAutomaticChecks {
		checks = "off"
	}
	automaticInstall := "off"
	if configuration.Updates.AutoInstall {
		automaticInstall = "on"
	}
	if _, err := view.printf(
		"\n%s  %s\n\n  %-18s %s\n  %-18s %s\n  %-18s %s\n  %-18s %s\n  %-18s %s\n  %-18s %s\n\n",
		view.info(),
		view.strong("Corresync settings"),
		"Default account", defaultAccount,
		"Update channel", configuration.Updates.Channel,
		"Update checks", checks,
		"Automatic install", automaticInstall,
		"Safety mode", configuration.Policy.Mode,
		"Login timeout", time.Duration(configuration.Browser.LoginTimeout),
	); err != nil {
		return err
	}
	_, err := view.printf(
		"  1. Rename an account\n"+
			"  2. Change the default account\n"+
			"  3. Change the update channel\n"+
			"  4. Change automatic update installation\n"+
			"  5. Change automatic update checks\n"+
			"  6. Change the safety mode\n"+
			"  7. Change the browser login timeout\n\n"+
			"  %s\n\n",
		view.muted("Advanced settings remain available through corr config edit."),
	)
	return err
}

func accountOptions(configuration config.Config) []settingsOption {
	aliases := sortedAccountAliases(configuration)
	options := make([]settingsOption, 0, len(aliases))
	for _, alias := range aliases {
		label := alias
		if address := configuration.Accounts[alias].Address; address != "" {
			label += " (" + sanitizeCell(address, 254) + ")"
		}
		options = append(options, settingsOption{label: label, value: alias})
	}
	return options
}

func renameAccountSetting(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
) error {
	options := accountOptions(configuration)
	if len(options) == 0 {
		_, err := reader.view.printf(
			"%s  No accounts are configured.\n   %s\n",
			reader.view.warning(),
			reader.view.command("Next: corr setup <email-address>"),
		)
		return err
	}
	option, selected, err := reader.choose("Account to rename", options)
	if err != nil || !selected {
		return err
	}
	for {
		alias, err := reader.line("New account name (up to 64 characters; b to go back): ")
		if err != nil {
			return err
		}
		if strings.EqualFold(alias, "b") || strings.EqualFold(alias, "back") {
			return nil
		}
		if err := domain.AccountAlias(alias).Validate(); err != nil {
			if _, writeErr := reader.view.printf(
				"%s  %s\n",
				reader.view.warning(),
				sanitizeCell(err.Error(), 256),
			); writeErr != nil {
				return writeErr
			}
			continue
		}
		return (&accountRenameCommand{Account: option.value, Alias: alias}).Run(app)
	}
}

func changeDefaultAccount(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
	path string,
) error {
	options := accountOptions(configuration)
	if len(options) == 0 {
		_, err := reader.view.printf(
			"%s  Add an account before choosing a default.\n   %s\n",
			reader.view.warning(),
			reader.view.command("Next: corr setup <email-address>"),
		)
		return err
	}
	return chooseConfigSetting(
		app,
		reader,
		configuration,
		path,
		"default_account",
		options,
	)
}

func chooseConfigSetting(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
	path string,
	key string,
	options []settingsOption,
) error {
	option, selected, err := reader.choose("New value", options)
	if err != nil || !selected {
		return err
	}
	return saveSettingsValue(app, configuration, path, key, option.value)
}

func changeLoginTimeout(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
	path string,
) error {
	for {
		value, err := reader.line("Login timeout (for example 5m or 10m; b to go back): ")
		if err != nil {
			return err
		}
		if strings.EqualFold(value, "b") || strings.EqualFold(value, "back") {
			return nil
		}
		candidate := configuration
		if err := setConfigValue(&candidate, "browser.login_timeout", value); err != nil {
			if _, writeErr := reader.view.printf(
				"%s  %s\n",
				reader.view.warning(),
				sanitizeCell(err.Error(), 256),
			); writeErr != nil {
				return writeErr
			}
			continue
		}
		if err := config.Save(path, candidate); err != nil {
			return err
		}
		return writeConfigUpdated(app, "browser.login_timeout = "+value)
	}
}

func changeAutomaticInstall(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
	path string,
) error {
	option, selected, err := reader.choose("Automatic installation", []settingsOption{
		{label: "Ask me to run corr update", value: "false"},
		{label: "Install verified direct updates automatically", value: "true"},
	})
	if err != nil || !selected {
		return err
	}
	if option.value == "true" {
		configuration.Updates.DisableAutomaticChecks = false
	}
	if err := setConfigValue(&configuration, "updates.auto_install", option.value); err != nil {
		return err
	}
	if err := config.Save(path, configuration); err != nil {
		return err
	}
	detail := "updates.auto_install = " + option.value
	if option.value == "true" {
		detail += "; automatic checks = on (required)"
	}
	return writeConfigUpdated(app, detail)
}

func changeAutomaticChecks(
	app *runtime,
	reader *settingsReader,
	configuration config.Config,
	path string,
) error {
	option, selected, err := reader.choose("Automatic update checks", []settingsOption{
		{label: "Check quietly once per day", value: "false"},
		{label: "Do not check automatically", value: "true"},
	})
	if err != nil || !selected {
		return err
	}
	if option.value == "true" && configuration.Updates.AutoInstall {
		_, err = reader.view.printf(
			"%s  Automatic checks are required while automatic installation is on.\n"+
				"   Turn off automatic installation first.\n",
			reader.view.warning(),
		)
		return err
	}
	return saveSettingsValue(
		app,
		configuration,
		path,
		"updates.disable_automatic_checks",
		option.value,
	)
}

func saveSettingsValue(
	app *runtime,
	configuration config.Config,
	path string,
	key string,
	value string,
) error {
	if err := setConfigValue(&configuration, key, value); err != nil {
		return err
	}
	if err := config.Save(path, configuration); err != nil {
		return err
	}
	return writeConfigUpdated(app, key+" = "+value)
}
