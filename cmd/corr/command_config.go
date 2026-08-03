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
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
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
	configuration := config.Default()
	if command.Force {
		if err := config.Save(path, configuration); err != nil {
			return err
		}
	} else {
		created, err := config.Create(app.context, path, configuration)
		if err != nil {
			return err
		}
		if !created {
			return fmt.Errorf("config already exists at %s; use --force to replace it", path)
		}
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{
			"created":          true,
			"path":             path,
			"providerSelected": false,
		})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n\n   %s\n   %s\n",
		view.success(),
		view.strong("Provider-neutral configuration created"),
		view.muted(path),
		"No account or provider was selected.",
		view.command("Next: corr setup <email-address>"),
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
		view.strong("Corresync configuration"),
		view.muted(path),
	); err != nil {
		return err
	}
	if _, err := view.printf(
		"  %-18s %s\n  %-18s %t\n  %-18s %t\n",
		"Update channel",
		configuration.Updates.Channel,
		"Automatic install",
		configuration.Updates.AutoInstall,
		"Automatic feedback",
		configuration.Feedback.AutoSubmit,
	); err != nil {
		return err
	}
	if len(configuration.Accounts) == 0 {
		_, err := view.printf(
			"\n  No accounts configured.\n\n  %s\n",
			view.command("Next: corr setup <email-address>"),
		)
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
		web, _ := account.OutlookWeb()
		mailbox := ""
		if web.Mailbox != "" {
			mailbox = " · " + web.Mailbox
		}
		route := string(account.MailProvider())
		if account.CalendarProvider() != "" && account.CalendarProvider() != account.MailProvider() {
			route += " + " + string(account.CalendarProvider())
		}
		if _, err := view.printf(
			"  %s  %s %s · %s%s\n",
			view.success(),
			view.strong(fmt.Sprintf("%-16s", alias)),
			route,
			web.Origin,
			view.muted(mailbox),
		); err != nil {
			return err
		}
	}
	_, err = view.printf(
		"\n  %-18s %t\n  %-18s %t\n  %-18s %d\n  %-18s %d\n  %-18s %s\n\n  %s\n",
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
		view.command("Change everyday settings: corr settings"),
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
	if command.Key == "feedback.auto_submit" && configuration.Feedback.AutoSubmit {
		if err := writeAutomaticFeedbackConsent(app.stderr); err != nil {
			return err
		}
		if err := validateAutomaticFeedbackPrerequisite(app); err != nil {
			return err
		}
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
	configuration, path, err := app.loadConfig()
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
	editedConfiguration, err := config.Parse(edited)
	if err != nil {
		return fmt.Errorf("edited config is invalid and was not applied: %w", err)
	}
	if !configuration.Feedback.AutoSubmit && editedConfiguration.Feedback.AutoSubmit {
		if err := writeAutomaticFeedbackConsent(app.stderr); err != nil {
			return err
		}
		if err := validateAutomaticFeedbackPrerequisite(app); err != nil {
			return err
		}
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
	case "updates.automatic_checks":
		return !configuration.Updates.DisableAutomaticChecks, nil
	case "updates.auto_install":
		return configuration.Updates.AutoInstall, nil
	case "updates.channel":
		return configuration.Updates.Channel, nil
	case "feedback.auto_submit":
		return configuration.Feedback.AutoSubmit, nil
	}
	if alias, field, ok := accountConfigKey(key); ok {
		account, exists := configuration.Accounts[alias]
		if !exists {
			return nil, fmt.Errorf("account %q is not configured", alias)
		}
		switch field {
		case "id":
			return account.ID, nil
		case "provider":
			return account.PrimaryProvider(), nil
		case "address":
			return account.Address, nil
		case "origin":
			web, ok := account.OutlookWeb()
			if !ok {
				return nil, fmt.Errorf("account %q does not use one Outlook Web origin", alias)
			}
			return web.Origin, nil
		case "mailbox":
			web, ok := account.OutlookWeb()
			if !ok {
				return nil, fmt.Errorf("account %q does not use one Outlook Web mailbox", alias)
			}
			return web.Mailbox, nil
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
	case "updates.automatic_checks":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Updates.DisableAutomaticChecks = !parsed
	case "updates.auto_install":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Updates.AutoInstall = parsed
		if parsed {
			configuration.Updates.DisableAutomaticChecks = false
		}
	case "updates.channel":
		configuration.Updates.Channel = config.UpdateChannel(value)
	case "feedback.auto_submit":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse %s as boolean: %w", key, err)
		}
		configuration.Feedback.AutoSubmit = parsed
	default:
		alias, field, ok := accountConfigKey(key)
		if !ok {
			return fmt.Errorf("unsupported configuration key %q", key)
		}
		if err := domain.AccountAlias(alias).Validate(); err != nil {
			return fmt.Errorf("invalid account alias %q: %w", alias, err)
		}
		account, exists := configuration.Accounts[alias]
		if !exists {
			accountID, err := domain.NewAccountID()
			if err != nil {
				return err
			}
			account.ID = accountID
		}
		switch field {
		case "id":
			return errors.New("account ID is read-only")
		case "provider":
			if domain.ProviderID(value) != domain.ProviderMicrosoftOWA {
				return errors.New(
					"non-Outlook providers require explicit nested mail/calendar route configuration",
				)
			}
			if account.Mail == nil {
				account.Mail = &config.MailRoute{
					Provider:   domain.ProviderMicrosoftOWA,
					OutlookWeb: &config.OutlookWebRoute{},
				}
			}
			if account.Calendar == nil {
				account.Calendar = &config.CalendarRoute{
					Provider:   domain.ProviderMicrosoftOWA,
					OutlookWeb: &config.OutlookWebRoute{},
				}
			}
		case "address":
			account.Address = value
		case "origin":
			ensureOutlookRoutes(&account)
			account.Mail.OutlookWeb.Origin = value
			account.Calendar.OutlookWeb.Origin = value
		case "mailbox":
			if !exists {
				return fmt.Errorf("set accounts.%s.origin before its mailbox", alias)
			}
			web, ok := account.OutlookWeb()
			if !ok {
				return fmt.Errorf("account %q does not use one Outlook Web route", alias)
			}
			web.Mailbox = value
			account.Mail.OutlookWeb.Mailbox = value
			account.Calendar.OutlookWeb.Mailbox = value
		default:
			return fmt.Errorf("unsupported configuration key %q", key)
		}
		configuration.Accounts[alias] = account
	}
	return configuration.Validate()
}

func ensureOutlookRoutes(account *config.Account) {
	if account.Mail == nil {
		account.Mail = &config.MailRoute{
			Provider:   domain.ProviderMicrosoftOWA,
			OutlookWeb: &config.OutlookWebRoute{},
		}
	}
	if account.Calendar == nil {
		account.Calendar = &config.CalendarRoute{
			Provider:   domain.ProviderMicrosoftOWA,
			OutlookWeb: &config.OutlookWebRoute{},
		}
	}
}

func accountConfigKey(key string) (alias, field string, ok bool) {
	if !strings.HasPrefix(key, "accounts.") {
		return "", "", false
	}
	remainder := strings.TrimPrefix(key, "accounts.")
	for _, candidate := range []string{"id", "provider", "address", "origin", "mailbox"} {
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
