package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-shellwords"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/policy"
)

type configCommand struct {
	Init     configInitCommand     `cmd:"" help:"Create a safe default configuration."`
	Path     configPathCommand     `cmd:"" help:"Print the effective configuration path."`
	Show     configShowCommand     `cmd:"" help:"Show the validated secret-free configuration."`
	Get      configGetCommand      `cmd:"" help:"Read one typed configuration value."`
	Set      configSetCommand      `cmd:"" help:"Set one typed configuration value."`
	Edit     configEditCommand     `cmd:"" help:"Safely edit and validate configuration."`
	Validate configValidateCommand `cmd:"" help:"Strictly validate configuration."`
}

type configInitCommand struct {
	Force bool `help:"Replace an existing regular config file."`
	JSON  bool `help:"Write machine-readable JSON."`
}

func (command *configInitCommand) Run(app *runtime) error {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil && !command.Force {
		return fmt.Errorf("config already exists at %s; use --force to replace it", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config path: %w", err)
	}
	if err := config.Save(path, config.Default()); err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"created": true, "path": path})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Configuration created"),
		view.muted(path),
	)
	return err
}

type configPathCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

func (command *configPathCommand) Run(app *runtime) error {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]string{"path": path})
	}
	_, err = fmt.Fprintln(app.stdout, path)
	return err
}

type configShowCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

func (command *configShowCommand) Run(app *runtime) error {
	configuration, path, err := app.loadConfig()
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, configuration)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n   %s\n\n",
		view.info(),
		view.strong("OWA Bridge configuration"),
		view.muted(path),
	); err != nil {
		return err
	}
	if _, err := view.printf(
		"  %-18s %s\n  %-18s %s\n",
		"Default account",
		configuration.DefaultAccount,
		"Policy",
		configuration.Policy.Mode,
	); err != nil {
		return err
	}
	if _, err := view.printf("\n  %s\n", view.strong("Accounts")); err != nil {
		return err
	}
	aliases := sortedAccountAliases(configuration)
	for _, alias := range aliases {
		account := configuration.Accounts[alias]
		mailbox := ""
		if account.Mailbox != "" {
			mailbox = " · " + account.Mailbox
		}
		if _, err := view.printf(
			"  %s  %s %s%s\n",
			view.success(),
			view.strong(fmt.Sprintf("%-16s", alias)),
			account.Origin,
			view.muted(mailbox),
		); err != nil {
			return err
		}
	}
	_, err = view.printf(
		"\n  %-18s %t\n  %-18s %t\n  %-18s %d\n  %-18s %d\n  %-18s %s\n",
		"Preview reads",
		configuration.Policy.PreviewSensitiveReads,
		"Preview writes",
		configuration.Policy.PreviewReversibleWrites,
		"Max recipients",
		configuration.Policy.MaxRecipients,
		"Max attendees",
		configuration.Policy.MaxAttendees,
		"Login timeout",
		time.Duration(configuration.Browser.LoginTimeout),
	)
	return err
}

type configGetCommand struct {
	Key  string `arg:"" help:"Typed key such as policy.max_recipients."`
	JSON bool   `help:"Write machine-readable JSON."`
}

func (command *configGetCommand) Run(app *runtime) error {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	value, err := getConfigValue(configuration, command.Key)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"key": command.Key, "value": value})
	}
	if !app.interactiveStdout() {
		_, err = fmt.Fprintln(app.stdout, value)
		return err
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %-34s %v\n",
		view.info(),
		view.strong(command.Key),
		value,
	)
	return err
}

type configSetCommand struct {
	Key   string `arg:"" help:"Typed key such as policy.max_recipients."`
	Value string `arg:"" help:"New value."`
	JSON  bool   `help:"Write machine-readable JSON."`
}

func (command *configSetCommand) Run(app *runtime) error {
	configuration, path, err := app.loadConfig()
	if err != nil {
		return err
	}
	if err := setConfigValue(&configuration, command.Key, command.Value); err != nil {
		return err
	}
	if err := config.Save(path, configuration); err != nil {
		return err
	}
	value, err := getConfigValue(configuration, command.Key)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(
			app.stdout,
			map[string]any{"key": command.Key, "value": value, "updated": true},
		)
	}
	return writeConfigUpdated(app, command.Key+" = "+fmt.Sprint(value))
}

type configEditCommand struct {
	Editor string `help:"Editor command; defaults to VISUAL, EDITOR, then the platform default."`
	JSON   bool   `help:"Write machine-readable JSON."`
}

func (command *configEditCommand) Run(app *runtime) error {
	_, path, err := app.loadConfig()
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(path) // #nosec G304 -- explicit validated config path.
	if err != nil {
		return fmt.Errorf("read config for editing: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-edit-*.toml")
	if err != nil {
		return fmt.Errorf("create config edit file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect config edit file: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("prepare config edit file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config edit file: %w", err)
	}
	editor := command.Editor
	if editor == "" {
		if value, exists := app.lookupEnv("VISUAL"); exists {
			editor = value
		} else if value, exists := app.lookupEnv("EDITOR"); exists {
			editor = value
		} else if goruntime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	arguments, err := shellwords.Parse(editor)
	if err != nil {
		return fmt.Errorf("parse editor command %q: %w", editor, err)
	}
	if len(arguments) == 0 {
		return errors.New("editor command is empty")
	}
	if err := app.runCommand(
		app.context,
		app.stdout,
		app.stderr,
		arguments[0],
		append(arguments[1:], temporaryPath)...,
	); err != nil {
		return fmt.Errorf("run editor: %w", err)
	}
	edited, err := os.ReadFile(temporaryPath) // #nosec G304 -- private edit file created above.
	if err != nil {
		return fmt.Errorf("read edited config: %w", err)
	}
	if err := config.SaveTOML(path, edited); err != nil {
		return fmt.Errorf("edited config is invalid and was not applied: %w", err)
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"edited": true, "path": path})
	}
	return writeConfigUpdated(app, "Validated and saved "+path)
}

func writeConfigUpdated(app *runtime, detail string) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := view.printf(
		"%s  %s\n   %s\n   %s\n",
		view.success(),
		view.strong("Configuration updated"),
		view.muted(detail),
		view.muted("A running session owner must be stopped before the new configuration takes effect."),
	)
	return err
}

func sortedAccountAliases(configuration config.Config) []string {
	aliases := make([]string, 0, len(configuration.Accounts))
	for alias := range configuration.Accounts {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	return aliases
}

func getConfigValue(configuration config.Config, key string) (any, error) {
	switch key {
	case "version":
		return configuration.Version, nil
	case "default_account":
		return configuration.DefaultAccount, nil
	case "policy.mode":
		return configuration.Policy.Mode, nil
	case "policy.preview_sensitive_reads":
		return configuration.Policy.PreviewSensitiveReads, nil
	case "policy.preview_reversible_writes":
		return configuration.Policy.PreviewReversibleWrites, nil
	case "policy.max_recipients":
		return configuration.Policy.MaxRecipients, nil
	case "policy.max_attendees":
		return configuration.Policy.MaxAttendees, nil
	case "browser.executable":
		return configuration.Browser.Executable, nil
	case "browser.login_timeout":
		return time.Duration(configuration.Browser.LoginTimeout).String(), nil
	case "updates.disable_automatic_checks":
		return configuration.Updates.DisableAutomaticChecks, nil
	}
	if alias, field, ok := accountConfigKey(key); ok {
		account, exists := configuration.Accounts[alias]
		if !exists {
			return nil, fmt.Errorf("account %q is not configured", alias)
		}
		switch field {
		case "origin":
			return account.Origin, nil
		case "mailbox":
			return account.Mailbox, nil
		}
	}
	return nil, fmt.Errorf("unsupported configuration key %q", key)
}

func setConfigValue(configuration *config.Config, key, value string) error {
	switch key {
	case "version":
		return errors.New("config version is read-only")
	case "default_account":
		configuration.DefaultAccount = value
	case "policy.mode":
		configuration.Policy.Mode = policy.Mode(value)
	case "policy.preview_sensitive_reads":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Policy.PreviewSensitiveReads = parsed
	case "policy.preview_reversible_writes":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Policy.PreviewReversibleWrites = parsed
	case "policy.max_recipients":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse %s as integer: %w", key, err)
		}
		configuration.Policy.MaxRecipients = parsed
	case "policy.max_attendees":
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse %s as integer: %w", key, err)
		}
		configuration.Policy.MaxAttendees = parsed
	case "browser.executable":
		configuration.Browser.Executable = value
	case "browser.login_timeout":
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("parse %s as duration: %w", key, err)
		}
		configuration.Browser.LoginTimeout = config.Duration(parsed)
	case "updates.disable_automatic_checks":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Updates.DisableAutomaticChecks = parsed
	default:
		alias, field, ok := accountConfigKey(key)
		if !ok {
			return fmt.Errorf("unsupported configuration key %q", key)
		}
		if err := domain.AccountID(alias).Validate(); err != nil {
			return fmt.Errorf("invalid account alias %q: %w", alias, err)
		}
		account, exists := configuration.Accounts[alias]
		switch field {
		case "origin":
			account.Origin = value
		case "mailbox":
			if !exists {
				return fmt.Errorf("set accounts.%s.origin before its mailbox", alias)
			}
			account.Mailbox = value
		default:
			return fmt.Errorf("unsupported configuration key %q", key)
		}
		configuration.Accounts[alias] = account
	}
	return configuration.Validate()
}

func accountConfigKey(key string) (alias, field string, ok bool) {
	if !strings.HasPrefix(key, "accounts.") {
		return "", "", false
	}
	remainder := strings.TrimPrefix(key, "accounts.")
	for _, candidate := range []string{"origin", "mailbox"} {
		suffix := "." + candidate
		if strings.HasSuffix(remainder, suffix) {
			alias := strings.TrimSuffix(remainder, suffix)
			return alias, candidate, alias != ""
		}
	}
	return "", "", false
}

type configValidateCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

func (command *configValidateCommand) Run(app *runtime) error {
	_, path, err := app.loadConfig()
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"path": path, "valid": true})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Configuration is valid"),
		view.muted(path),
	)
	return err
}
