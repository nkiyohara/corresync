package main

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/settingsstore"
)

type settingsCommand struct{}

const (
	maximumSettingsInputBytes = 64 << 10
	settingsAccessibleCancel  = ":cancel"
)

type settingsAccessibleReader struct {
	reader    io.Reader
	exhausted bool
}

func (reader *settingsAccessibleReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	read, err := reader.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		reader.exhausted = true
	}
	return read, err
}

const (
	settingsActionAccounts = "accounts"
	settingsActionUpdates  = "updates"
	settingsActionSafety   = "safety"
	settingsActionFeedback = "feedback"
	settingsActionLogin    = "login"
	settingsActionAdvanced = "advanced"
	settingsActionSetup    = "setup"
	settingsActionDone     = "done"
	settingsAccountPrefix  = "account:"
)

func (command *settingsCommand) Run(app *runtime) error {
	if !app.interactiveInput() || !app.interactiveStdout() {
		return errors.New(
			"settings requires an interactive terminal; use `corr account rename`, " +
				"`corr config set`, or `corr config edit` in scripts",
		)
	}
	if settingsAccessible(app) {
		restoreInput := prepareAccessibleSettingsInput(app)
		defer restoreInput()
	}
	service, err := newLocalSettingsService(app)
	if err != nil {
		return err
	}
	for {
		settings, err := service.Show(app.context)
		if err != nil {
			return err
		}
		if err := writeSettingsOverview(app); err != nil {
			return err
		}
		action, selected, err := runSettingsSelect(
			app,
			"What would you like to change?",
			"↑/↓ move • enter select • esc finish",
			settingsMenuOptions(settings),
		)
		if err != nil {
			return err
		}
		if !selected || action == settingsActionDone {
			return writeSettingsDone(app)
		}
		switch action {
		case settingsActionAccounts:
			if err := runAccountsSettings(app, service); err != nil {
				return err
			}
		case settingsActionUpdates:
			if err := runUpdateSettings(app, service, settings); err != nil {
				return err
			}
		case settingsActionSafety:
			if err := runSafetySettings(app, service, settings); err != nil {
				return err
			}
		case settingsActionFeedback:
			if err := runFeedbackSettings(app, settings); err != nil {
				return err
			}
		case settingsActionLogin:
			if err := runLoginSettings(app, service, settings); err != nil {
				return err
			}
		case settingsActionAdvanced:
			if err := writeAdvancedSettingsHelp(app); err != nil {
				return err
			}
		}
	}
}

func newLocalSettingsService(app *runtime) (*application.SettingsService, error) {
	path, err := app.resolvedConfigPath()
	if err != nil {
		return nil, err
	}
	return application.NewSettingsService(settingsstore.Store{ConfigPath: path})
}

func settingsMenuOptions(settings application.SettingsView) []huh.Option[string] {
	options := make([]huh.Option[string], 0, 7)
	checks := "checks off"
	if settings.AutomaticChecks {
		checks = "daily checks"
	}
	install := "manual install"
	if settings.AutomaticInstall {
		install = "automatic install"
	}
	options = append(options,
		huh.NewOption(accountsSettingsSummary(settings), settingsActionAccounts),
		huh.NewOption(
			fmt.Sprintf("Updates  %s · %s · %s", settings.UpdateChannel, checks, install),
			settingsActionUpdates,
		),
		huh.NewOption(
			"Safety   "+settings.SafetyMode+" · "+safetySummary(settings.SafetyMode),
			settingsActionSafety,
		),
		huh.NewOption(
			"Browser  "+settings.LoginTimeout+" · sign-in timeout",
			settingsActionLogin,
		),
		huh.NewOption(
			"Feedback "+automaticFeedbackSummary(settings.FeedbackAutoSubmit),
			settingsActionFeedback,
		),
		huh.NewOption("Advanced  edit the complete validated config", settingsActionAdvanced),
		huh.NewOption("Done", settingsActionDone),
	)
	return options
}

func automaticFeedbackSummary(enabled bool) string {
	if enabled {
		return "on · allowlisted errors may become public GitHub Issues"
	}
	return "off · no automatic issue submission"
}

func runAccountsSettings(
	app *runtime,
	service *application.SettingsService,
) error {
	for {
		settings, err := service.Show(app.context)
		if err != nil {
			return err
		}
		action, selected, err := runSettingsSelect(
			app,
			"Accounts",
			"Add an account or choose one to manage its sign-in and local settings.",
			accountsSettingsMenuOptions(settings),
		)
		if err != nil || !selected || action == "back" {
			return err
		}
		if action == settingsActionSetup {
			if err := runAddAccountSettings(app, settings); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(action, settingsAccountPrefix) {
			if err := runAccountSettings(
				app,
				service,
				settings,
				strings.TrimPrefix(action, settingsAccountPrefix),
			); err != nil {
				return err
			}
		}
	}
}

func accountsSettingsMenuOptions(
	settings application.SettingsView,
) []huh.Option[string] {
	options := []huh.Option[string]{huh.NewOption(
		"Add account · corr setup <email-address>",
		settingsActionSetup,
	)}
	for _, account := range settings.Accounts {
		label := account.Alias
		if account.IsDefault {
			label += " (default)"
		}
		if account.Address != "" {
			label += " · " + sanitizeCell(account.Address, 254)
		}
		options = append(options, huh.NewOption(
			label,
			settingsAccountPrefix+account.Alias,
		))
	}
	return append(options, huh.NewOption("Back", "back"))
}

func accountsSettingsSummary(settings application.SettingsView) string {
	count := len(settings.Accounts)
	if count == 0 {
		return "Accounts  none configured · add one"
	}
	noun := "accounts"
	if count == 1 {
		noun = "account"
	}
	return fmt.Sprintf(
		"Accounts  %d %s · %s default",
		count,
		noun,
		settings.DefaultAccount,
	)
}

func runAccountSettings(
	app *runtime,
	service *application.SettingsService,
	settings application.SettingsView,
	alias string,
) error {
	account, found := findSettingsAccount(settings, alias)
	if !found {
		return fmt.Errorf("account %q is no longer configured", alias)
	}
	options := []huh.Option[string]{
		huh.NewOption(
			"Sign in · corr auth login --account "+alias,
			"login",
		),
		huh.NewOption(
			"Sign in here · corr auth login --account "+alias+" --terminal",
			"login_terminal",
		),
		huh.NewOption(
			"Rename · corr account rename "+alias+" <new-name>",
			"rename",
		),
	}
	if !account.IsDefault {
		options = append(options, huh.NewOption(
			"Make default · corr config set default_account "+alias,
			"default",
		))
	}
	if len(settings.Accounts) > 1 {
		options = append(options, huh.NewOption(
			"Remove · deletes Corresync local state for "+alias,
			"remove",
		))
	} else {
		options = append(options, huh.NewOption(
			"Remove unavailable · add another account first",
			"remove_unavailable",
		))
	}
	options = append(options, huh.NewOption("Back", "back"))
	action, selected, err := runSettingsSelect(
		app,
		"Account · "+alias,
		accountDescription(account),
		options,
	)
	if err != nil || !selected || action == "back" {
		return err
	}
	switch action {
	case "login":
		return (&loginCommand{Account: alias}).Run(app)
	case "login_terminal":
		return runSettingsTerminalLogin(app, alias)
	case "default":
		return applyLocalSetting(app, service, application.SettingsUpdateInput{
			Key: application.SettingDefaultAccount, Value: alias,
		})
	case "remove":
		return runRemoveAccountSettings(app, settings, account)
	case "remove_unavailable":
		return writeLastAccountRemovalHelp(app)
	case "rename":
	default:
		return nil
	}
	newAlias := alias
	selected, err = runSettingsInput(
		app,
		"New name for "+alias,
		"This changes only the local name; the stable account ID and login stay unchanged.",
		&newAlias,
		64,
		func(value string) error {
			if value == alias {
				return errors.New("enter a different account name")
			}
			return domain.AccountAlias(value).Validate()
		},
	)
	if err != nil || !selected {
		return err
	}
	return runSettingsAccountMutation(app, func() error {
		return (&accountRenameCommand{Account: alias, Alias: newAlias}).Run(app)
	})
}

func runSettingsTerminalLogin(app *runtime, alias string) error {
	originalInput := app.stdin
	if accessible, ok := app.stdin.(*settingsAccessibleReader); ok {
		app.stdin = accessible.reader
		defer func() { app.stdin = originalInput }()
	}
	return (&loginCommand{Account: alias, Terminal: true}).Run(app)
}

func runAddAccountSettings(
	app *runtime,
	_ application.SettingsView,
) error {
	account, completed, err := runAccountRegistrationWizard(app)
	if err != nil || !completed {
		return err
	}
	return runOnboardingAccountHandoff(app, account)
}

func runRemoveAccountSettings(
	app *runtime,
	settings application.SettingsView,
	account application.SettingsAccount,
) error {
	replacement := ""
	if account.IsDefault {
		options := make([]huh.Option[string], 0, len(settings.Accounts)-1)
		for _, candidate := range settings.Accounts {
			if candidate.Alias == account.Alias {
				continue
			}
			label := candidate.Alias
			if candidate.Address != "" {
				label += " · " + sanitizeCell(candidate.Address, 254)
			}
			options = append(options, huh.NewOption(label, candidate.Alias))
		}
		var selected bool
		var err error
		replacement, selected, err = runSettingsSelect(
			app,
			"New default account",
			account.Alias+" is currently the default. Choose its replacement before removal.",
			options,
		)
		if err != nil || !selected {
			return err
		}
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	review, err := accounts.ReviewRemove(app.context, application.AccountRemoveInput{
		Account: account.Alias, ReplacementDefault: replacement,
	})
	if err != nil {
		return err
	}
	if err := writeAccountRemovalReview(app, review); err != nil {
		return err
	}
	confirmed, err := runSettingsConfirm(
		app,
		"Remove "+account.Alias+" and its local state?",
		"This removes only Corresync-owned state on this device. The provider account is not deleted.",
	)
	if err != nil || !confirmed {
		if err == nil {
			return writeSettingsNoChange(app)
		}
		return err
	}
	return runSettingsAccountMutation(app, func() error {
		return (&accountRemoveCommand{
			Account: account.Alias, NewDefault: replacement, Approve: true,
		}).Run(app)
	})
}

func runSettingsAccountMutation(app *runtime, change func() error) error {
	if err := app.requireDaemonStopped(); err == nil {
		return change()
	} else if !strings.Contains(err.Error(), "account changes require a stopped session owner") {
		return err
	}
	if err := (&daemonStopCommand{}).Run(app); err != nil {
		return err
	}
	if err := waitForSettingsDaemonStop(app); err != nil {
		return err
	}
	changeErr := change()
	restartErr := (&daemonStartCommand{}).Run(app)
	if changeErr != nil {
		return errors.Join(changeErr, restartErr)
	}
	if restartErr != nil {
		return fmt.Errorf(
			"account changed but the session owner did not restart: %w",
			restartErr,
		)
	}
	return nil
}

func waitForSettingsDaemonStop(app *runtime) error {
	deadline := time.NewTimer(daemonShutdownTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()
	for {
		if err := app.requireDaemonStopped(); err == nil {
			return nil
		}
		select {
		case <-app.context.Done():
			return app.context.Err()
		case <-deadline.C:
			return errors.New("session owner did not stop before the account change")
		case <-ticker.C:
		}
	}
}

func validateSettingsEmailAddress(value string) error {
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("enter one bare email address")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value ||
		!strings.Contains(value, "@") {
		return errors.New("enter one bare email address")
	}
	return nil
}

func runUpdateSettings(
	app *runtime,
	service *application.SettingsService,
	settings application.SettingsView,
) error {
	checks := "on"
	if !settings.AutomaticChecks {
		checks = "off"
	}
	install := "off"
	if settings.AutomaticInstall {
		install = "on"
	}
	action, selected, err := runSettingsSelect(
		app,
		"Updates",
		"Choose a row to see its values and exact command.",
		[]huh.Option[string]{
			huh.NewOption(
				"Channel  "+settings.UpdateChannel+" · stable or preview releases",
				"channel",
			),
			huh.NewOption(
				"Automatic checks  "+checks+" · quiet daily version check",
				"checks",
			),
			huh.NewOption(
				"Automatic install  "+install+" · verified direct updates",
				"install",
			),
			huh.NewOption("Back", "back"),
		},
	)
	if err != nil || !selected || action == "back" {
		return err
	}
	switch action {
	case "channel":
		return chooseAndApplySetting(
			app, service, application.SettingUpdateChannel,
			"Update channel",
			"Stable excludes prereleases. Preview includes alpha, beta, and RC builds.",
			[]huh.Option[string]{
				huh.NewOption("Stable · corr config set updates.channel stable", "stable").Selected(settings.UpdateChannel == "stable"),
				huh.NewOption("Preview · corr config set updates.channel preview", "preview").Selected(settings.UpdateChannel == "preview"),
			},
		)
	case "checks":
		return chooseAndApplySetting(
			app, service, application.SettingUpdateChecks,
			"Automatic update checks",
			"When on, Corresync checks quietly at most once per day.",
			[]huh.Option[string]{
				huh.NewOption("On · corr config set updates.automatic_checks true", "true").Selected(settings.AutomaticChecks),
				huh.NewOption("Off · corr config set updates.automatic_checks false", "false").Selected(!settings.AutomaticChecks),
			},
		)
	case "install":
		return chooseAndApplySetting(
			app, service, application.SettingUpdateInstall,
			"Automatic update installation",
			"Turning this on also enables the required daily update check.",
			[]huh.Option[string]{
				huh.NewOption("Off · install only with corr update", "false").Selected(!settings.AutomaticInstall),
				huh.NewOption("On · install verified direct updates automatically", "true").Selected(settings.AutomaticInstall),
			},
		)
	default:
		return nil
	}
}

func runSafetySettings(
	app *runtime,
	service *application.SettingsService,
	settings application.SettingsView,
) error {
	return chooseAndApplySetting(
		app, service, application.SettingSafetyMode,
		"Safety mode",
		"Guarded uses normal previews and approvals. Read-only blocks every write, including MCP writes.",
		[]huh.Option[string]{
			huh.NewOption("Guarded · corr config set policy.mode guarded", "guarded").Selected(settings.SafetyMode == "guarded"),
			huh.NewOption("Read-only · corr config set policy.mode read_only", "read_only").Selected(settings.SafetyMode == "read_only"),
		},
	)
}

func runFeedbackSettings(
	app *runtime,
	settings application.SettingsView,
) error {
	value, selected, err := runSettingsSelect(
		app,
		"Automatic error feedback",
		"Off is the default. On uses your signed-in GitHub CLI to create a public issue after an interactive corr command fails.",
		[]huh.Option[string]{
			huh.NewOption(
				"Off · corr config set feedback.auto_submit false",
				"false",
			).Selected(!settings.FeedbackAutoSubmit),
			huh.NewOption(
				"On · public allowlist-only issues via gh",
				"true",
			).Selected(settings.FeedbackAutoSubmit),
		},
	)
	if err != nil || !selected {
		return err
	}
	enabled := value == "true"
	if enabled == settings.FeedbackAutoSubmit {
		return writeSettingsNoChange(app)
	}
	if enabled {
		confirmed, err := confirmAutomaticFeedbackConsent(app)
		if err != nil || !confirmed {
			return err
		}
		if err := validateAutomaticFeedbackPrerequisite(app); err != nil {
			return err
		}
	}
	path, err := app.resolvedConfigPath()
	if err != nil {
		return err
	}
	if err := config.Update(app.context, path, func(configuration *config.Config) error {
		configuration.Feedback.AutoSubmit = enabled
		return nil
	}); err != nil {
		return err
	}
	view := newConsoleView(app, app.stdout, true)
	description := "Automatic public issue submission is off."
	if enabled {
		description = "Automatic public issue submission is on for allowlisted interactive command errors."
	}
	_, err = view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.success(),
		view.strong("Feedback setting updated"),
		view.muted(description),
		view.command("corr config set feedback.auto_submit "+value),
	)
	return err
}

func confirmAutomaticFeedbackConsent(app *runtime) (bool, error) {
	if _, err := fmt.Fprintln(app.stdout); err != nil {
		return false, err
	}
	if err := writeAutomaticFeedbackConsent(app.stdout); err != nil {
		return false, err
	}
	confirmed := false
	form := settingsForm(app, huh.NewConfirm().
		Title("Enable automatic public GitHub Issues?").
		Description(
			"Your GitHub username will be public. Corresync sends only version/OS, install method, command and flag names, and fixed error classes—never raw errors, values, paths, accounts, credentials, mail, or calendar data. Each build/error fingerprint is attempted once. Esc keeps this off.",
		).
		Affirmative("Enable public reports").
		Negative("Keep off").
		Value(&confirmed))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("confirm automatic feedback consent: %w", err)
	}
	return confirmed, nil
}

func runLoginSettings(
	app *runtime,
	service *application.SettingsService,
	settings application.SettingsView,
) error {
	value := settings.LoginTimeout
	selected, err := runSettingsInput(
		app,
		"Browser login timeout",
		"Use 1m through 30m. Command: corr config set browser.login_timeout <duration>",
		&value,
		16,
		func(candidate string) error {
			duration, parseErr := time.ParseDuration(candidate)
			if parseErr != nil {
				return errors.New("enter a duration such as 5m or 10m")
			}
			if duration < time.Minute || duration > 30*time.Minute {
				return errors.New("enter a duration from 1m through 30m")
			}
			return nil
		},
	)
	if err != nil || !selected {
		return err
	}
	return applyLocalSetting(app, service, application.SettingsUpdateInput{
		Key: application.SettingLoginTimeout, Value: value,
	})
}

func chooseAndApplySetting(
	app *runtime,
	service *application.SettingsService,
	key string,
	title string,
	description string,
	options []huh.Option[string],
) error {
	value, selected, err := runSettingsSelect(app, title, description, options)
	if err != nil || !selected {
		return err
	}
	return applyLocalSetting(app, service, application.SettingsUpdateInput{
		Key: key, Value: value,
	})
}

func applyLocalSetting(
	app *runtime,
	service *application.SettingsService,
	input application.SettingsUpdateInput,
) error {
	review, err := service.Review(app.context, input)
	if err != nil {
		if strings.Contains(err.Error(), " is already ") {
			return writeSettingsNoChange(app)
		}
		return err
	}
	if _, err := service.Apply(app.context, review); err != nil {
		return err
	}
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.success(), view.strong("Setting updated"),
		view.muted(review.Description), view.command(review.Command),
	); err != nil {
		return err
	}
	if len(review.RelatedChanges) > 0 {
		_, err = view.printf("   %s\n", view.muted("Automatic checks were also enabled because installation depends on them."))
	}
	if err == nil && review.RestartsSessions {
		_, err = view.printf(
			"   %s\n",
			view.muted("If a session owner is running, use `corr daemon stop`; it restarts on next use."),
		)
	}
	return err
}

func runSettingsSelect[T comparable](
	app *runtime,
	title string,
	description string,
	options []huh.Option[T],
) (T, bool, error) {
	if !strings.Contains(strings.ToLower(description), "esc") {
		description += " Esc cancels."
	}
	var value T
	form := settingsForm(app, huh.NewSelect[T]().
		Title(title).
		Description(description).
		Options(options...).
		Value(&value).
		Height(min(12, len(options)+2)))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return value, false, nil
		}
		return value, false, fmt.Errorf("run settings selector: %w", err)
	}
	if settingsInputExhausted(app) {
		return value, false, nil
	}
	return value, true, nil
}

func runSettingsInput(
	app *runtime,
	title string,
	description string,
	value *string,
	limit int,
	validate func(string) error,
) (bool, error) {
	if !strings.Contains(strings.ToLower(description), "esc") {
		description += " Esc cancels."
	}
	accessible := settingsAccessible(app)
	if accessible {
		description += " Type :cancel to cancel."
		title += " (type :cancel to cancel)"
	}
	form := settingsForm(app, huh.NewInput().
		Title(title).
		Description(description).
		CharLimit(limit).
		Value(value).
		Validate(func(candidate string) error {
			if accessible && strings.TrimSpace(candidate) == settingsAccessibleCancel {
				return nil
			}
			return validate(strings.TrimSpace(candidate))
		}))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("run settings input: %w", err)
	}
	*value = strings.TrimSpace(*value)
	if settingsInputExhausted(app) || accessible && *value == settingsAccessibleCancel {
		return false, nil
	}
	return true, nil
}

func runSettingsConfirm(
	app *runtime,
	title string,
	description string,
) (bool, error) {
	confirmed := false
	form := settingsForm(app, huh.NewConfirm().
		Title(title).
		Description(description+" Esc goes back.").
		Affirmative("Remove").
		Negative("Keep account").
		Value(&confirmed))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("run settings confirmation: %w", err)
	}
	if settingsInputExhausted(app) {
		return false, nil
	}
	return confirmed, nil
}

func settingsForm(app *runtime, fields ...huh.Field) *huh.Form {
	keymap := huh.NewDefaultKeyMap()
	keymap.Quit.SetKeys("ctrl+c", "esc")
	keymap.Quit.SetHelp("esc", "cancel")
	return huh.NewForm(huh.NewGroup(fields...)).
		WithInput(app.stdin).
		WithOutput(app.stdout).
		WithAccessible(settingsAccessible(app)).
		WithKeyMap(keymap).
		WithShowHelp(true).
		WithShowErrors(true).
		WithWidth(88)
}

func settingsAccessible(app *runtime) bool {
	value, exists := app.lookupEnv("CORRESYNC_ACCESSIBLE")
	if !exists {
		return false
	}
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

func settingsInputExhausted(app *runtime) bool {
	reader, ok := app.stdin.(*settingsAccessibleReader)
	return ok && reader.exhausted
}

func writeSettingsOverview(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n\n",
		view.info(), view.strong("Corresync settings"),
		view.muted("Accounts, updates, safety, feedback, and sign-in — with the exact CLI command for every change."),
	)
	return err
}

func writeAdvancedSettingsHelp(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n\n",
		view.info(), view.strong("Advanced configuration"),
		view.command("corr config edit"),
		view.muted("The editor validates the complete file before replacing your current config."),
	)
	return err
}

func writeLastAccountRemovalHelp(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n\n",
		view.info(), view.strong("Keep one configured account"),
		view.muted("Add a replacement account first, then remove this one."),
	)
	return err
}

func writeAccountRemovalReview(
	app *runtime,
	review application.AccountChangeReview,
) error {
	view := newConsoleView(app, app.stdout, true)
	replacement := ""
	if review.ReplacementDefault != "" {
		replacement = "\n   " + view.muted("New default: "+review.ReplacementDefault)
	}
	oauth := ""
	if review.MayDeleteOwnedOAuth {
		oauth = "\n   " + view.muted("Its Corresync-owned OAuth authorization may also be removed.")
	}
	_, err := view.printf(
		"\n%s  %s\n   %s%s%s\n\n",
		view.warning(), view.strong("Review account removal"),
		view.muted("Account: "+review.Alias+" · local sessions and state will be deleted."),
		replacement, oauth,
	)
	return err
}

func writeSettingsNoChange(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf("\n%s  %s\n", view.info(), view.strong("No change"))
	return err
}

func writeSettingsDone(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf("\n%s  %s\n", view.success(), view.strong("Done"))
	return err
}

func findSettingsAccount(
	settings application.SettingsView,
	alias string,
) (application.SettingsAccount, bool) {
	for _, account := range settings.Accounts {
		if account.Alias == alias {
			return account, true
		}
	}
	return application.SettingsAccount{}, false
}

func accountDescription(account application.SettingsAccount) string {
	parts := []string{"Stable account identity and login are preserved when renaming."}
	if account.Address != "" {
		parts = append(parts, "Address: "+sanitizeCell(account.Address, 254))
	}
	return strings.Join(parts, " ")
}

func safetySummary(mode string) string {
	if mode == "read_only" {
		return "all writes blocked"
	}
	return "writes use normal approval policy"
}
