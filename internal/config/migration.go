package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/domain"
)

const legacyVersion = 1
const routeLegacyVersion = 2
const googleAPILegacyVersion = 3
const updateChannelLegacyVersion = 4
const taskRouteLegacyVersion = 5
const calDAVTaskLegacyVersion = 6
const googleTaskLegacyVersion = 7
const tickTickLegacyVersion = 8
const messagingLegacyVersion = 9
const googleCredentialLegacyVersion = 10

type legacyConfig struct {
	Version        int                      `toml:"version"`
	DefaultAccount string                   `toml:"default_account"`
	Accounts       map[string]legacyAccount `toml:"accounts"`
	Policy         Policy                   `toml:"policy"`
	Browser        Browser                  `toml:"browser"`
	Updates        legacyUpdates            `toml:"updates"`
}

// legacyUpdates deliberately excludes automatic installation. No schema that
// predates that consent may manufacture it during migration.
type legacyUpdates struct {
	DisableAutomaticChecks bool `toml:"disable_automatic_checks"`
}

type legacyAccount struct {
	Origin  string `toml:"origin"`
	Mailbox string `toml:"mailbox,omitempty"`
}

type routeLegacyConfig struct {
	Version        int                           `toml:"version"`
	DefaultAccount string                        `toml:"default_account"`
	Accounts       map[string]routeLegacyAccount `toml:"accounts"`
	Policy         Policy                        `toml:"policy"`
	Browser        Browser                       `toml:"browser"`
	Updates        legacyUpdates                 `toml:"updates"`
}

type routeLegacyAccount struct {
	ID       domain.AccountID  `toml:"id"`
	Provider domain.ProviderID `toml:"provider"`
	Address  string            `toml:"address,omitempty"`
	Origin   string            `toml:"origin"`
	Mailbox  string            `toml:"mailbox,omitempty"`
}

type googleAPILegacyConfig struct {
	Version        int                               `toml:"version"`
	DefaultAccount string                            `toml:"default_account"`
	Accounts       map[string]googleAPILegacyAccount `toml:"accounts"`
	Policy         Policy                            `toml:"policy"`
	Browser        Browser                           `toml:"browser"`
	Credentials    Credentials                       `toml:"credentials,omitempty"`
	Updates        legacyUpdates                     `toml:"updates"`
}

type updateChannelLegacyConfig struct {
	Version        int                               `toml:"version"`
	DefaultAccount string                            `toml:"default_account"`
	Accounts       map[string]taskRouteLegacyAccount `toml:"accounts"`
	Policy         Policy                            `toml:"policy"`
	Browser        Browser                           `toml:"browser"`
	Credentials    Credentials                       `toml:"credentials,omitempty"`
	Updates        updateChannelLegacyUpdates        `toml:"updates"`
}

// taskRouteLegacyAccount freezes the v5 account shape so a task route cannot
// be smuggled into an older schema version and silently accepted.
type taskRouteLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type taskRouteLegacyConfig struct {
	Version        int                               `toml:"version"`
	DefaultAccount string                            `toml:"default_account"`
	Accounts       map[string]taskRouteLegacyAccount `toml:"accounts"`
	Policy         Policy                            `toml:"policy"`
	Browser        Browser                           `toml:"browser"`
	Credentials    Credentials                       `toml:"credentials,omitempty"`
	Updates        Updates                           `toml:"updates"`
	Feedback       Feedback                          `toml:"feedback"`
}

// calDAVTaskLegacyRoute freezes the v6 task union so VTODO settings cannot be
// smuggled into an older schema and silently accepted during migration.
type calDAVTaskLegacyRoute struct {
	Provider       domain.ProviderID        `toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute `toml:"microsoft_graph,omitempty"`
	Todoist        *TodoistTaskRoute        `toml:"todoist,omitempty"`
}

type calDAVTaskLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Tasks    *calDAVTaskLegacyRoute               `toml:"tasks,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type calDAVTaskLegacyConfig struct {
	Version        int                                `toml:"version"`
	DefaultAccount string                             `toml:"default_account"`
	Accounts       map[string]calDAVTaskLegacyAccount `toml:"accounts"`
	Policy         Policy                             `toml:"policy"`
	Browser        Browser                            `toml:"browser"`
	Credentials    Credentials                        `toml:"credentials,omitempty"`
	Updates        Updates                            `toml:"updates"`
	Feedback       Feedback                           `toml:"feedback"`
}

// googleTaskLegacyRoute freezes the v7 task union so Google OAuth settings
// cannot be smuggled into a pre-Google-Tasks schema and silently accepted.
type googleTaskLegacyRoute struct {
	Provider       domain.ProviderID        `toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute `toml:"microsoft_graph,omitempty"`
	Todoist        *TodoistTaskRoute        `toml:"todoist,omitempty"`
	CalDAV         *CalDAVTaskRoute         `toml:"caldav,omitempty"`
}

type googleTaskLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Tasks    *googleTaskLegacyRoute               `toml:"tasks,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type googleTaskLegacyConfig struct {
	Version        int                                `toml:"version"`
	DefaultAccount string                             `toml:"default_account"`
	Accounts       map[string]googleTaskLegacyAccount `toml:"accounts"`
	Policy         Policy                             `toml:"policy"`
	Browser        Browser                            `toml:"browser"`
	Credentials    Credentials                        `toml:"credentials,omitempty"`
	Updates        Updates                            `toml:"updates"`
	Feedback       Feedback                           `toml:"feedback"`
}

// tickTickLegacyRoute freezes the v8 task union so a confidential-client
// reference cannot be smuggled into an older schema during migration.
type tickTickLegacyRoute struct {
	Provider       domain.ProviderID                      `toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute               `toml:"microsoft_graph,omitempty"`
	Todoist        *TodoistTaskRoute                      `toml:"todoist,omitempty"`
	CalDAV         *CalDAVTaskRoute                       `toml:"caldav,omitempty"`
	GoogleTasks    *googleCredentialLegacyGoogleTaskRoute `toml:"google_tasks,omitempty"`
}

type tickTickLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Tasks    *tickTickLegacyRoute                 `toml:"tasks,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type tickTickLegacyConfig struct {
	Version        int                              `toml:"version"`
	DefaultAccount string                           `toml:"default_account"`
	Accounts       map[string]tickTickLegacyAccount `toml:"accounts"`
	Policy         Policy                           `toml:"policy"`
	Browser        Browser                          `toml:"browser"`
	Credentials    Credentials                      `toml:"credentials,omitempty"`
	Updates        Updates                          `toml:"updates"`
	Feedback       Feedback                         `toml:"feedback"`
}

// messagingLegacyAccount freezes the v9 account shape so a messaging route
// cannot be smuggled into an older schema and silently accepted by migration.
type messagingLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Tasks    *googleCredentialLegacyTaskRoute     `toml:"tasks,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type messagingLegacyConfig struct {
	Version        int                               `toml:"version"`
	DefaultAccount string                            `toml:"default_account"`
	Accounts       map[string]messagingLegacyAccount `toml:"accounts"`
	Policy         Policy                            `toml:"policy"`
	Browser        Browser                           `toml:"browser"`
	Credentials    Credentials                       `toml:"credentials,omitempty"`
	Updates        Updates                           `toml:"updates"`
	Feedback       Feedback                          `toml:"feedback"`
}

// googleCredentialLegacy* freezes the v10 route shapes so a Google client
// credential reference cannot be smuggled into an older schema.
type googleCredentialLegacyGoogleMailRoute struct {
	Username      string        `toml:"username"`
	Mailbox       string        `toml:"mailbox,omitempty"`
	ClientID      string        `toml:"client_id"`
	RedirectURI   string        `toml:"redirect_uri"`
	Authorization CredentialRef `toml:"authorization"`
}

type googleCredentialLegacyMailRoute struct {
	Provider       domain.ProviderID                      `toml:"provider"`
	OutlookWeb     *OutlookWebRoute                       `toml:"outlook_web,omitempty"`
	JMAP           *JMAPRoute                             `toml:"jmap,omitempty"`
	IMAPSMTP       *IMAPSMTPRoute                         `toml:"imap_smtp,omitempty"`
	Google         *googleCredentialLegacyGoogleMailRoute `toml:"google,omitempty"`
	GoogleWeb      *WebRoute                              `toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute                            `toml:"microsoft_graph,omitempty"`
}

type googleCredentialLegacyCalendarRoute struct {
	Provider       domain.ProviderID `toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `toml:"outlook_web,omitempty"`
	CalDAV         *CalDAVRoute      `toml:"caldav,omitempty"`
	Google         *OAuthRoute       `toml:"google,omitempty"`
	GoogleWeb      *WebRoute         `toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `toml:"microsoft_graph,omitempty"`
}

type googleCredentialLegacyGoogleTaskRoute struct {
	OAuth    OAuthRoute `toml:"oauth"`
	ReadOnly bool       `toml:"read_only,omitempty"`
}

type googleCredentialLegacyTaskRoute struct {
	Provider       domain.ProviderID                      `toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute               `toml:"microsoft_graph,omitempty"`
	Todoist        *TodoistTaskRoute                      `toml:"todoist,omitempty"`
	CalDAV         *CalDAVTaskRoute                       `toml:"caldav,omitempty"`
	GoogleTasks    *googleCredentialLegacyGoogleTaskRoute `toml:"google_tasks,omitempty"`
	TickTick       *TickTickTaskRoute                     `toml:"ticktick,omitempty"`
}

type googleCredentialLegacyAccount struct {
	ID       domain.AccountID                     `toml:"id"`
	Address  string                               `toml:"address,omitempty"`
	Mail     *googleCredentialLegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleCredentialLegacyCalendarRoute `toml:"calendar,omitempty"`
	Tasks    *googleCredentialLegacyTaskRoute     `toml:"tasks,omitempty"`
	Messages *MessagingRoute                      `toml:"messages,omitempty"`
	Monitor  *Monitor                             `toml:"monitor,omitempty"`
}

type googleCredentialLegacyConfig struct {
	Version        int                                      `toml:"version"`
	DefaultAccount string                                   `toml:"default_account"`
	Accounts       map[string]googleCredentialLegacyAccount `toml:"accounts"`
	Policy         Policy                                   `toml:"policy"`
	Browser        Browser                                  `toml:"browser"`
	Credentials    Credentials                              `toml:"credentials,omitempty"`
	Updates        Updates                                  `toml:"updates"`
	Feedback       Feedback                                 `toml:"feedback"`
}

func migrateGoogleCredentialLegacyMail(
	alias string,
	source *googleCredentialLegacyMailRoute,
) (*MailRoute, error) {
	if source == nil {
		return nil, nil
	}
	if source.Provider == domain.ProviderGoogle || source.Google != nil {
		return nil, legacyGoogleCredentialError(alias)
	}
	return &MailRoute{
		Provider: source.Provider, OutlookWeb: source.OutlookWeb,
		JMAP: source.JMAP, IMAPSMTP: source.IMAPSMTP,
		GoogleWeb: source.GoogleWeb, MicrosoftGraph: source.MicrosoftGraph,
	}, nil
}

func migrateGoogleCredentialLegacyCalendar(
	alias string,
	source *googleCredentialLegacyCalendarRoute,
) (*CalendarRoute, error) {
	if source == nil {
		return nil, nil
	}
	if source.Provider == domain.ProviderGoogle || source.Google != nil {
		return nil, legacyGoogleCredentialError(alias)
	}
	return &CalendarRoute{
		Provider: source.Provider, OutlookWeb: source.OutlookWeb,
		CalDAV: source.CalDAV, GoogleWeb: source.GoogleWeb,
		MicrosoftGraph: source.MicrosoftGraph,
	}, nil
}

func migrateGoogleCredentialLegacyTasks(
	alias string,
	source *googleCredentialLegacyTaskRoute,
) (*TaskRoute, error) {
	if source == nil {
		return nil, nil
	}
	if source.Provider == domain.ProviderGoogleTasks || source.GoogleTasks != nil {
		return nil, legacyGoogleCredentialError(alias)
	}
	return &TaskRoute{
		Provider: source.Provider, MicrosoftGraph: source.MicrosoftGraph,
		Todoist: source.Todoist, CalDAV: source.CalDAV,
		TickTick: source.TickTick,
	}, nil
}

func legacyGoogleCredentialError(alias string) error {
	return fmt.Errorf(
		"account %q has a legacy Google route without a consented Desktop client-credential reference; the file was left unchanged: follow docs/google-oauth-setup.md#recover-a-legacy-google-route before running `corr setup`",
		alias,
	)
}

// MigrateV10 adds no Google authority. A dormant v10 Google route cannot be
// activated because v10 had no consented external client-credential reference;
// the user must follow the documented manual recovery before re-adding it.
func MigrateV10(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy googleCredentialLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v10 config: %w", err)
	}
	if legacy.Version != googleCredentialLegacyVersion {
		return Config{}, fmt.Errorf(
			"google-credential legacy config version must be %d",
			googleCredentialLegacyVersion,
		)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	for alias, source := range legacy.Accounts {
		mail, err := migrateGoogleCredentialLegacyMail(alias, source.Mail)
		if err != nil {
			return Config{}, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, source.Calendar)
		if err != nil {
			return Config{}, err
		}
		tasks, err := migrateGoogleCredentialLegacyTasks(alias, source.Tasks)
		if err != nil {
			return Config{}, err
		}
		account := Account{
			ID: source.ID, Address: source.Address,
			Mail: mail, Calendar: calendar, Tasks: tasks,
			Messages: source.Messages, Monitor: source.Monitor,
		}
		configuration.Accounts[alias] = account
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v10 config: %w", err)
	}
	return configuration, nil
}

type updateChannelLegacyUpdates struct {
	DisableAutomaticChecks bool `toml:"disable_automatic_checks"`
	AutoInstall            bool `toml:"auto_install"`
}

// MigrateV9 adds an optional, explicit messaging route. Existing accounts
// receive no route, authorization, workspace, monitoring consent, or runtime
// capability.
func MigrateV9(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy messagingLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v9 config: %w", err)
	}
	if legacy.Version != messagingLegacyVersion {
		return Config{}, fmt.Errorf("messaging legacy config version must be %d", messagingLegacyVersion)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	for alias, source := range legacy.Accounts {
		mail, err := migrateGoogleCredentialLegacyMail(alias, source.Mail)
		if err != nil {
			return Config{}, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, source.Calendar)
		if err != nil {
			return Config{}, err
		}
		tasks, err := migrateGoogleCredentialLegacyTasks(alias, source.Tasks)
		if err != nil {
			return Config{}, err
		}
		configuration.Accounts[alias] = Account{
			ID: source.ID, Address: source.Address, Mail: mail,
			Calendar: calendar, Tasks: tasks, Monitor: source.Monitor,
		}
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v9 config: %w", err)
	}
	return configuration, nil
}

// MigrateV4 adds the stable update channel without broadening either automatic
// checking or automatic-install consent.
func MigrateV4(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy updateChannelLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v4 config: %w", err)
	}
	if legacy.Version != updateChannelLegacyVersion {
		return Config{}, fmt.Errorf("update-channel legacy config version must be %d", updateChannelLegacyVersion)
	}
	accounts, err := migratePreTaskAccounts(legacy.Accounts)
	if err != nil {
		return Config{}, err
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: accounts,
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials,
		Updates: Updates{
			Channel:                UpdateChannelStable,
			DisableAutomaticChecks: legacy.Updates.DisableAutomaticChecks,
			AutoInstall:            legacy.Updates.AutoInstall,
		},
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v4 config: %w", err)
	}
	return configuration, nil
}

// MigrateV5 adds an optional, explicit task route. Existing accounts receive
// no route and therefore no task authorization, provider access, or capability.
func MigrateV5(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy taskRouteLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v5 config: %w", err)
	}
	if legacy.Version != taskRouteLegacyVersion {
		return Config{}, fmt.Errorf("task-route legacy config version must be %d", taskRouteLegacyVersion)
	}
	accounts, err := migratePreTaskAccounts(legacy.Accounts)
	if err != nil {
		return Config{}, err
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: accounts,
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v5 config: %w", err)
	}
	return configuration, nil
}

// MigrateV6 adds typed CalDAV VTODO settings without changing any existing
// service route or manufacturing credential consent.
func MigrateV6(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy calDAVTaskLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v6 config: %w", err)
	}
	if legacy.Version != calDAVTaskLegacyVersion {
		return Config{}, fmt.Errorf("CalDAV-task legacy config version must be %d", calDAVTaskLegacyVersion)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	for alias, source := range legacy.Accounts {
		mail, err := migrateGoogleCredentialLegacyMail(alias, source.Mail)
		if err != nil {
			return Config{}, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, source.Calendar)
		if err != nil {
			return Config{}, err
		}
		account := Account{
			ID: source.ID, Address: source.Address, Mail: mail,
			Calendar: calendar, Monitor: source.Monitor,
		}
		if source.Tasks != nil {
			account.Tasks = &TaskRoute{
				Provider:       source.Tasks.Provider,
				MicrosoftGraph: source.Tasks.MicrosoftGraph,
				Todoist:        source.Tasks.Todoist,
			}
		}
		configuration.Accounts[alias] = account
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v6 config: %w", err)
	}
	return configuration, nil
}

// MigrateV7 adds typed Google Tasks OAuth settings without selecting a route,
// broadening a grant, or manufacturing authorization consent.
func MigrateV7(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy googleTaskLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v7 config: %w", err)
	}
	if legacy.Version != googleTaskLegacyVersion {
		return Config{}, fmt.Errorf("google-task legacy config version must be %d", googleTaskLegacyVersion)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	for alias, source := range legacy.Accounts {
		mail, err := migrateGoogleCredentialLegacyMail(alias, source.Mail)
		if err != nil {
			return Config{}, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, source.Calendar)
		if err != nil {
			return Config{}, err
		}
		account := Account{
			ID: source.ID, Address: source.Address, Mail: mail,
			Calendar: calendar, Monitor: source.Monitor,
		}
		if source.Tasks != nil {
			account.Tasks = &TaskRoute{
				Provider:       source.Tasks.Provider,
				MicrosoftGraph: source.Tasks.MicrosoftGraph,
				Todoist:        source.Tasks.Todoist,
				CalDAV:         source.Tasks.CalDAV,
			}
		}
		configuration.Accounts[alias] = account
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v7 config: %w", err)
	}
	return configuration, nil
}

// MigrateV8 adds typed TickTick confidential-client references without
// selecting a route or manufacturing consent for either external credential.
func MigrateV8(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy tickTickLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v8 config: %w", err)
	}
	if legacy.Version != tickTickLegacyVersion {
		return Config{}, fmt.Errorf("TickTick legacy config version must be %d", tickTickLegacyVersion)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials, Updates: legacy.Updates,
		Feedback: legacy.Feedback,
	}
	for alias, source := range legacy.Accounts {
		mail, err := migrateGoogleCredentialLegacyMail(alias, source.Mail)
		if err != nil {
			return Config{}, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, source.Calendar)
		if err != nil {
			return Config{}, err
		}
		account := Account{
			ID: source.ID, Address: source.Address, Mail: mail,
			Calendar: calendar, Monitor: source.Monitor,
		}
		if source.Tasks != nil {
			tasks, taskErr := migrateGoogleCredentialLegacyTasks(alias, &googleCredentialLegacyTaskRoute{
				Provider: source.Tasks.Provider, MicrosoftGraph: source.Tasks.MicrosoftGraph,
				Todoist: source.Tasks.Todoist, CalDAV: source.Tasks.CalDAV,
				GoogleTasks: source.Tasks.GoogleTasks,
			})
			if taskErr != nil {
				return Config{}, taskErr
			}
			account.Tasks = tasks
		}
		configuration.Accounts[alias] = account
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v8 config: %w", err)
	}
	return configuration, nil
}

func migratePreTaskAccounts(
	legacy map[string]taskRouteLegacyAccount,
) (map[string]Account, error) {
	accounts := make(map[string]Account, len(legacy))
	for alias, account := range legacy {
		mail, err := migrateGoogleCredentialLegacyMail(alias, account.Mail)
		if err != nil {
			return nil, err
		}
		calendar, err := migrateGoogleCredentialLegacyCalendar(alias, account.Calendar)
		if err != nil {
			return nil, err
		}
		accounts[alias] = Account{
			ID: account.ID, Address: account.Address,
			Mail: mail, Calendar: calendar, Monitor: account.Monitor,
		}
	}
	return accounts, nil
}

type googleAPILegacyAccount struct {
	ID       domain.AccountID              `toml:"id"`
	Address  string                        `toml:"address,omitempty"`
	Mail     *googleAPILegacyMailRoute     `toml:"mail,omitempty"`
	Calendar *googleAPILegacyCalendarRoute `toml:"calendar,omitempty"`
	Monitor  *Monitor                      `toml:"monitor,omitempty"`
}

type googleAPILegacyMailRoute struct {
	Provider       domain.ProviderID `toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `toml:"outlook_web,omitempty"`
	JMAP           *JMAPRoute        `toml:"jmap,omitempty"`
	IMAPSMTP       *IMAPSMTPRoute    `toml:"imap_smtp,omitempty"`
	GoogleAPI      *OAuthRoute       `toml:"google_api,omitempty"`
	GoogleWeb      *WebRoute         `toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `toml:"microsoft_graph,omitempty"`
}

type googleAPILegacyCalendarRoute struct {
	Provider       domain.ProviderID `toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `toml:"outlook_web,omitempty"`
	CalDAV         *CalDAVRoute      `toml:"caldav,omitempty"`
	GoogleAPI      *OAuthRoute       `toml:"google_api,omitempty"`
	GoogleWeb      *WebRoute         `toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `toml:"microsoft_graph,omitempty"`
}

// MigrateV3 replaces the legacy Google route with the unified API route while
// preserving account IDs, public-client
// settings, authorization handles, calendar API routing, and local policy.
func MigrateV3(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy googleAPILegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v3 config: %w", err)
	}
	if legacy.Version != googleAPILegacyVersion {
		return Config{}, fmt.Errorf(
			"google API legacy config version must be %d",
			googleAPILegacyVersion,
		)
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Credentials: legacy.Credentials,
		Updates: Updates{
			Channel:                UpdateChannelStable,
			DisableAutomaticChecks: legacy.Updates.DisableAutomaticChecks,
		},
	}
	for alias, source := range legacy.Accounts {
		account := Account{
			ID: source.ID, Address: source.Address, Monitor: source.Monitor,
		}
		if source.Mail != nil {
			mail, err := migrateV3Mail(source.Address, *source.Mail)
			if err != nil {
				return Config{}, fmt.Errorf("migrate account %q mail: %w", alias, err)
			}
			account.Mail = mail
		}
		if source.Calendar != nil {
			calendar, err := migrateV3Calendar(*source.Calendar)
			if err != nil {
				return Config{}, fmt.Errorf("migrate account %q calendar: %w", alias, err)
			}
			account.Calendar = calendar
		}
		configuration.Accounts[alias] = account
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v3 config: %w", err)
	}
	return configuration, nil
}

func migrateV3Mail(
	_ string,
	route googleAPILegacyMailRoute,
) (*MailRoute, error) {
	if route.Provider != domain.ProviderID("google-api") {
		return &MailRoute{
			Provider: route.Provider, OutlookWeb: route.OutlookWeb,
			JMAP: route.JMAP, IMAPSMTP: route.IMAPSMTP,
			GoogleWeb: route.GoogleWeb, MicrosoftGraph: route.MicrosoftGraph,
		}, nil
	}
	return nil, errors.New(
		"legacy Google route has no consented Desktop client-credential reference; the file was left unchanged: follow docs/google-oauth-setup.md#recover-a-legacy-google-route before running `corr setup`",
	)
}

func migrateV3Calendar(
	route googleAPILegacyCalendarRoute,
) (*CalendarRoute, error) {
	if route.Provider != domain.ProviderID("google-api") {
		return &CalendarRoute{
			Provider: route.Provider, OutlookWeb: route.OutlookWeb,
			CalDAV: route.CalDAV, GoogleWeb: route.GoogleWeb,
			MicrosoftGraph: route.MicrosoftGraph,
		}, nil
	}
	return nil, errors.New(
		"legacy Google route has no consented Desktop client-credential reference; the file was left unchanged: follow docs/google-oauth-setup.md#recover-a-legacy-google-route before running `corr setup`",
	)
}

// MigrateV1 converts an exact legacy config snapshot to the current schema. Account IDs are
// generated once and must be persisted by the caller before use.
func MigrateV1(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy legacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode legacy config: %w", err)
	}
	if legacy.Version != legacyVersion {
		return Config{}, fmt.Errorf("legacy config version must be %d", legacyVersion)
	}
	if len(legacy.Accounts) == 0 {
		return Config{}, errors.New("at least one account is required")
	}

	aliases := make([]string, 0, len(legacy.Accounts))
	for alias := range legacy.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	configuration := Config{
		Version:        CurrentVersion,
		DefaultAccount: legacy.DefaultAccount,
		Accounts:       make(map[string]Account, len(legacy.Accounts)),
		Policy:         legacy.Policy,
		Browser:        legacy.Browser,
		Updates: Updates{
			Channel:                UpdateChannelStable,
			DisableAutomaticChecks: legacy.Updates.DisableAutomaticChecks,
		},
	}
	for _, alias := range aliases {
		accountID, err := domain.NewAccountID()
		if err != nil {
			return Config{}, err
		}
		account := legacy.Accounts[alias]
		configuration.Accounts[alias] = migratedOutlookAccount(
			accountID,
			"",
			account.Origin,
			account.Mailbox,
		)
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated config: %w", err)
	}
	return configuration, nil
}

// MigrateV2 converts the single-provider account schema to separate mail and
// calendar routes without changing stable account IDs or browser-profile keys.
func MigrateV2(data []byte) (Config, error) {
	if len(data) > maximumConfigBytes {
		return Config{}, fmt.Errorf("config exceeds %d bytes", maximumConfigBytes)
	}
	var legacy routeLegacyConfig
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&legacy); err != nil {
		return Config{}, fmt.Errorf("decode v2 config: %w", err)
	}
	if legacy.Version != routeLegacyVersion {
		return Config{}, fmt.Errorf("route legacy config version must be %d", routeLegacyVersion)
	}
	if len(legacy.Accounts) == 0 {
		return Config{}, errors.New("at least one account is required")
	}
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: make(map[string]Account, len(legacy.Accounts)),
		Policy:   legacy.Policy, Browser: legacy.Browser,
		Updates: Updates{
			Channel:                UpdateChannelStable,
			DisableAutomaticChecks: legacy.Updates.DisableAutomaticChecks,
		},
	}
	for alias, account := range legacy.Accounts {
		if account.Provider != domain.ProviderMicrosoftOWA {
			return Config{}, fmt.Errorf(
				"v2 account %q uses unsupported provider %q",
				alias,
				account.Provider,
			)
		}
		configuration.Accounts[alias] = migratedOutlookAccount(
			account.ID,
			account.Address,
			account.Origin,
			account.Mailbox,
		)
	}
	if err := configuration.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate migrated v2 config: %w", err)
	}
	return configuration, nil
}

func migratedOutlookAccount(
	accountID domain.AccountID,
	address string,
	origin string,
	mailbox string,
) Account {
	return Account{
		ID: accountID, Address: address,
		Mail: &MailRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &OutlookWebRoute{
				Origin: origin, Mailbox: mailbox,
			},
		},
		Calendar: &CalendarRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &OutlookWebRoute{
				Origin: origin, Mailbox: mailbox,
			},
		},
	}
}

// EnsureDefaultPath performs the one-way, rollback-safe default migration.
// The legacy file remains byte-for-byte unchanged; the returned file uses the
// current schema.
func EnsureDefaultPath() (path string, migrated bool, err error) {
	path, err = DefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Lstat(path); statErr == nil {
		if _, loadErr := Load(path); loadErr != nil {
			return "", false, loadErr
		}
		return path, false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect config path: %w", statErr)
	}

	legacyPath, err := LegacyDefaultPath()
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Lstat(legacyPath); errors.Is(statErr, os.ErrNotExist) {
		return path, false, nil
	} else if statErr != nil {
		return "", false, fmt.Errorf("inspect legacy config path: %w", statErr)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false, fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return "", false, fmt.Errorf("protect config directory: %w", err)
	}
	unlock, acquired, err := acquireMigrationLock(directory, path)
	if err != nil {
		return "", false, err
	}
	if !acquired {
		return path, false, nil
	}
	defer unlock()

	if _, statErr := os.Lstat(path); statErr == nil {
		if _, loadErr := Load(path); loadErr != nil {
			return "", false, loadErr
		}
		return path, false, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect config path: %w", statErr)
	}

	data, err := readConfigFile(legacyPath)
	if err != nil {
		return "", false, err
	}
	configuration, err := MigrateV1(data)
	if err != nil {
		return "", false, err
	}
	if err := Save(path, configuration); err != nil {
		return "", false, fmt.Errorf("save migrated config: %w", err)
	}
	return path, true, nil
}

func acquireMigrationLock(
	directory string,
	target string,
) (unlock func(), acquired bool, err error) {
	lockPath := filepath.Join(directory, ".migration.lock")
	for attempt := 0; attempt < 100; attempt++ {
		// #nosec G304 -- lockPath is derived from the platform config directory.
		file, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if openErr == nil {
			if closeErr := file.Close(); closeErr != nil {
				_ = os.Remove(lockPath)
				return nil, false, fmt.Errorf("close migration lock: %w", closeErr)
			}
			return func() { _ = os.Remove(lockPath) }, true, nil
		}
		if !errors.Is(openErr, os.ErrExist) {
			return nil, false, fmt.Errorf("create migration lock: %w", openErr)
		}
		if _, statErr := os.Lstat(target); statErr == nil {
			if _, loadErr := Load(target); loadErr != nil {
				return nil, false, loadErr
			}
			return func() {}, false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, false, errors.New("timed out waiting for config migration lock")
}
