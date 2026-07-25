// Package config loads strict, secret-free application configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/policy"
)

const CurrentVersion = 1

// Config is the complete persisted configuration. It intentionally has no
// credential, cookie, token, canary, or password field.
type Config struct {
	Version        int                `json:"version" toml:"version"`
	DefaultAccount string             `json:"defaultAccount" toml:"default_account"`
	Accounts       map[string]Account `json:"accounts" toml:"accounts"`
	Policy         Policy             `json:"policy" toml:"policy"`
	Browser        Browser            `json:"browser" toml:"browser"`
	Updates        Updates            `json:"updates" toml:"updates"`
}

// Account identifies one Outlook Web origin by a non-personal local alias.
// Mailbox optionally selects a shared or delegated SMTP mailbox while keeping
// authentication in the same browser-owned Outlook Web session.
type Account struct {
	Origin  string `json:"origin" toml:"origin"`
	Mailbox string `json:"mailbox,omitempty" toml:"mailbox,omitempty"`
}

// Policy maps persisted settings into the deterministic policy core.
type Policy struct {
	Mode                    policy.Mode `json:"mode" toml:"mode"`
	PreviewSensitiveReads   bool        `json:"previewSensitiveReads" toml:"preview_sensitive_reads"`
	PreviewReversibleWrites bool        `json:"previewReversibleWrites" toml:"preview_reversible_writes"`
	MaxRecipients           int         `json:"maxRecipients" toml:"max_recipients"`
	MaxAttendees            int         `json:"maxAttendees" toml:"max_attendees"`
}

// Browser controls the dedicated interactive browser process.
type Browser struct {
	Executable   string   `json:"executable,omitempty" toml:"executable,omitempty"`
	LoginTimeout Duration `json:"loginTimeout" toml:"login_timeout"`
}

// Updates controls only opportunistic public release checks. Explicit
// `owa update` and `owa update check` calls remain available when automatic
// checks are disabled.
type Updates struct {
	DisableAutomaticChecks bool `json:"disableAutomaticChecks" toml:"disable_automatic_checks"`
}

// Duration encodes a human-readable Go duration such as "5m" in TOML.
type Duration time.Duration

// MarshalText implements encoding.TextMarshaler.
func (duration Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(duration).String()), nil
}

// MarshalJSON keeps the JSON configuration view human-readable.
func (duration Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(duration).String())
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (duration *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}
	*duration = Duration(parsed)
	return nil
}

// Default returns a safe, immediately valid configuration.
func Default() Config {
	return Config{
		Version:        CurrentVersion,
		DefaultAccount: "work",
		Accounts: map[string]Account{
			"work": {Origin: "https://outlook.cloud.microsoft"},
		},
		Policy: Policy{
			Mode:          policy.ModeGuarded,
			MaxRecipients: 20,
			MaxAttendees:  50,
		},
		Browser: Browser{LoginTimeout: Duration(5 * time.Minute)},
	}
}

// Validate rejects ambiguous, unsafe, or unsupported configuration.
func (configuration Config) Validate() error {
	if configuration.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", configuration.Version)
	}
	if len(configuration.Accounts) == 0 {
		return errors.New("at least one account is required")
	}
	if len(configuration.Accounts) > 32 {
		return errors.New("at most 32 accounts are supported")
	}
	if _, exists := configuration.Accounts[configuration.DefaultAccount]; !exists {
		return fmt.Errorf("default account %q is not configured", configuration.DefaultAccount)
	}

	aliases := make([]string, 0, len(configuration.Accounts))
	for alias := range configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if err := domain.AccountID(alias).Validate(); err != nil {
			return fmt.Errorf("validate account alias %q: %w", alias, err)
		}
		if err := validateOrigin(configuration.Accounts[alias].Origin); err != nil {
			return fmt.Errorf("validate account %q: %w", alias, err)
		}
		if err := validateMailbox(configuration.Accounts[alias].Mailbox); err != nil {
			return fmt.Errorf("validate account %q: %w", alias, err)
		}
	}

	if err := configuration.Policy.Rules().Validate(); err != nil {
		return fmt.Errorf("validate policy: %w", err)
	}
	if configuration.Policy.MaxRecipients < 1 || configuration.Policy.MaxRecipients > 500 {
		return errors.New("max_recipients must be between 1 and 500")
	}
	if configuration.Policy.MaxAttendees < 1 || configuration.Policy.MaxAttendees > 1000 {
		return errors.New("max_attendees must be between 1 and 1000")
	}
	loginTimeout := time.Duration(configuration.Browser.LoginTimeout)
	if loginTimeout < time.Minute || loginTimeout > 30*time.Minute {
		return errors.New("login_timeout must be between 1 minute and 30 minutes")
	}
	if strings.ContainsAny(configuration.Browser.Executable, "\r\n\x00") {
		return errors.New("browser executable contains a forbidden character")
	}
	return nil
}

func validateMailbox(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 254 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("mailbox must be a bare SMTP address")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value || !strings.Contains(value, "@") {
		return errors.New("mailbox must be a bare SMTP address")
	}
	return nil
}

// Rules converts persisted policy without giving adapters a second policy path.
func (configuration Policy) Rules() policy.Rules {
	return policy.Rules{
		Mode:                    configuration.Mode,
		PreviewSensitiveReads:   configuration.PreviewSensitiveReads,
		PreviewReversibleWrites: configuration.PreviewReversibleWrites,
	}
}

func validateOrigin(raw string) error {
	origin, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse origin: %w", err)
	}
	if origin.Scheme != "https" {
		return errors.New("origin must use https")
	}
	if origin.Hostname() == "" {
		return errors.New("origin must include a hostname")
	}
	if origin.User != nil {
		return errors.New("origin must not include user information")
	}
	if origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("origin must not include a query or fragment")
	}
	if origin.Path != "" && origin.Path != "/" {
		return errors.New("origin must not include a path")
	}
	return nil
}
