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
	Version        int                        `toml:"version"`
	DefaultAccount string                     `toml:"default_account"`
	Accounts       map[string]Account         `toml:"accounts"`
	Policy         Policy                     `toml:"policy"`
	Browser        Browser                    `toml:"browser"`
	Credentials    Credentials                `toml:"credentials,omitempty"`
	Updates        updateChannelLegacyUpdates `toml:"updates"`
}

type updateChannelLegacyUpdates struct {
	DisableAutomaticChecks bool `toml:"disable_automatic_checks"`
	AutoInstall            bool `toml:"auto_install"`
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
	configuration := Config{
		Version: CurrentVersion, DefaultAccount: legacy.DefaultAccount,
		Accounts: legacy.Accounts, Policy: legacy.Policy, Browser: legacy.Browser,
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
	address string,
	route googleAPILegacyMailRoute,
) (*MailRoute, error) {
	if route.Provider != domain.ProviderID("google-api") {
		return &MailRoute{
			Provider: route.Provider, OutlookWeb: route.OutlookWeb,
			JMAP: route.JMAP, IMAPSMTP: route.IMAPSMTP,
			GoogleWeb: route.GoogleWeb, MicrosoftGraph: route.MicrosoftGraph,
		}, nil
	}
	if route.GoogleAPI == nil {
		return nil, errors.New("google-api settings are missing")
	}
	if address == "" {
		return nil, errors.New("google mail migration requires the account address")
	}
	return &MailRoute{
		Provider: domain.ProviderGoogle,
		Google: &GoogleMailRoute{
			Username:      address,
			ClientID:      route.GoogleAPI.ClientID,
			RedirectURI:   route.GoogleAPI.RedirectURI,
			Authorization: route.GoogleAPI.Authorization,
		},
	}, nil
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
	if route.GoogleAPI == nil {
		return nil, errors.New("google-api settings are missing")
	}
	return &CalendarRoute{
		Provider: domain.ProviderGoogle,
		Google:   route.GoogleAPI,
	}, nil
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
