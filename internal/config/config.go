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

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const CurrentVersion = 3

const defaultAccountID domain.AccountID = "acc_00000000000000000000000000000001"

// Config is the complete persisted configuration. It intentionally has no
// credential, cookie, token, canary, or password field.
type Config struct {
	Version        int                `json:"version" toml:"version"`
	DefaultAccount string             `json:"defaultAccount" toml:"default_account"`
	Accounts       map[string]Account `json:"accounts" toml:"accounts"`
	Policy         Policy             `json:"policy" toml:"policy"`
	Browser        Browser            `json:"browser" toml:"browser"`
	Credentials    Credentials        `json:"credentials,omitempty" toml:"credentials,omitempty"`
	Updates        Updates            `json:"updates" toml:"updates"`
}

// Account is one provider routing and isolation boundary. The map key in
// Config.Accounts is its mutable local alias; ID is an opaque, stable storage
// and policy key that does not change when the alias or address changes.
type Account struct {
	ID       domain.AccountID `json:"id" toml:"id"`
	Address  string           `json:"address,omitempty" toml:"address,omitempty"`
	Mail     *MailRoute       `json:"mail,omitempty" toml:"mail,omitempty"`
	Calendar *CalendarRoute   `json:"calendar,omitempty" toml:"calendar,omitempty"`
	Monitor  *Monitor         `json:"monitor,omitempty" toml:"monitor,omitempty"`
}

// Monitor is an explicit, account-local consent record. A nil value is always
// equivalent to off, including for every older configuration and import.
type Monitor struct {
	Mode          domain.MonitorMode `json:"mode" toml:"mode"`
	PollInterval  Duration           `json:"pollInterval" toml:"poll_interval"`
	Debounce      Duration           `json:"debounce" toml:"debounce"`
	Retention     Duration           `json:"retention" toml:"retention"`
	RateLimitHour int                `json:"rateLimitHour" toml:"rate_limit_hour"`
	QuietHours    *QuietHours        `json:"quietHours,omitempty" toml:"quiet_hours,omitempty"`
	Filter        MonitorFilter      `json:"filter" toml:"filter"`
	Notification  *Notification      `json:"notification,omitempty" toml:"notification,omitempty"`
	Runner        *Runner            `json:"runner,omitempty" toml:"runner,omitempty"`
}

// MonitorFilter is deliberately metadata-only. It can never inspect a body or
// attachment and matching cannot grant broader execution policy.
type MonitorFilter struct {
	SenderDomains   []string `json:"senderDomains,omitempty" toml:"sender_domains,omitempty"`
	SubjectContains []string `json:"subjectContains,omitempty" toml:"subject_contains,omitempty"`
	ImportantOnly   bool     `json:"importantOnly,omitempty" toml:"important_only,omitempty"`
}

// QuietHours suppresses notification and dispatch in one IANA time zone. Queue
// collection may continue so restart recovery does not lose an event.
type QuietHours struct {
	Start    string `json:"start" toml:"start"`
	End      string `json:"end" toml:"end"`
	TimeZone string `json:"timeZone" toml:"time_zone"`
}

// Notification identifies a local-only adapter and the bounded metadata fields
// it may display.
type Notification struct {
	Adapter string   `json:"adapter" toml:"adapter"`
	Fields  []string `json:"fields" toml:"fields"`
}

// Runner is an explicitly selected local process. Egress is a human
// declaration shown by status and audit; remote declarations require a second
// affirmative field so a mode change cannot silently authorize disclosure.
type Runner struct {
	Command       string   `json:"command" toml:"command"`
	Arguments     []string `json:"arguments,omitempty" toml:"arguments,omitempty"`
	Egress        string   `json:"egress" toml:"egress"`
	ApproveRemote bool     `json:"approveRemote,omitempty" toml:"approve_remote,omitempty"`
	Fields        []string `json:"fields" toml:"fields"`
	Timeout       Duration `json:"timeout" toml:"timeout"`
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
// `corr update` and `corr update check` remain available when automatic
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

// Default returns a provider-neutral, secret-free onboarding state.
// It is valid for account discovery and configuration, but session-backed
// commands require the human to add at least one explicit provider route.
func Default() Config {
	return Config{
		Version:        CurrentVersion,
		DefaultAccount: "",
		Accounts:       make(map[string]Account),
		Policy: Policy{
			Mode:          policy.ModeGuarded,
			MaxRecipients: 20,
			MaxAttendees:  50,
		},
		Browser: Browser{LoginTimeout: Duration(5 * time.Minute)},
	}
}

// OutlookDefault returns the deterministic Outlook route used by provider
// fixtures and legacy migration tests. Product onboarding must use Default.
func OutlookDefault() Config {
	configuration := Default()
	configuration.DefaultAccount = "work"
	configuration.Accounts["work"] = Account{
		ID: defaultAccountID,
		Mail: &MailRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &OutlookWebRoute{
				Origin: "https://outlook.cloud.microsoft",
			},
		},
		Calendar: &CalendarRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &OutlookWebRoute{
				Origin: "https://outlook.cloud.microsoft",
			},
		},
	}
	return configuration
}

// NewOutlookDefault returns the explicit Outlook fixture route with a freshly
// generated local account ID.
func NewOutlookDefault() (Config, error) {
	configuration := OutlookDefault()
	accountID, err := domain.NewAccountID()
	if err != nil {
		return Config{}, err
	}
	account := configuration.Accounts[configuration.DefaultAccount]
	account.ID = accountID
	configuration.Accounts[configuration.DefaultAccount] = account
	return configuration, nil
}

// Validate rejects ambiguous, unsafe, or unsupported configuration.
func (configuration Config) Validate() error {
	if configuration.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", configuration.Version)
	}
	if len(configuration.Accounts) > 32 {
		return errors.New("at most 32 accounts are supported")
	}
	if len(configuration.Accounts) == 0 && configuration.DefaultAccount != "" {
		return errors.New("default_account must be empty until an account is configured")
	}
	if len(configuration.Accounts) > 0 {
		if _, exists := configuration.Accounts[configuration.DefaultAccount]; !exists {
			return fmt.Errorf("default account %q is not configured", configuration.DefaultAccount)
		}
	}

	accountIDs := make(map[domain.AccountID]string, len(configuration.Accounts))
	aliases := make([]string, 0, len(configuration.Accounts))
	for alias := range configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		if err := domain.AccountAlias(alias).Validate(); err != nil {
			return fmt.Errorf("validate account alias %q: %w", alias, err)
		}
		account := configuration.Accounts[alias]
		if err := account.ID.ValidateOpaque(); err != nil {
			return fmt.Errorf("validate account %q ID: %w", alias, err)
		}
		if previous, exists := accountIDs[account.ID]; exists {
			return fmt.Errorf(
				"accounts %q and %q use duplicate ID %q",
				previous,
				alias,
				account.ID,
			)
		}
		accountIDs[account.ID] = alias
		if err := validateAddress(account.Address); err != nil {
			return fmt.Errorf("validate account %q: %w", alias, err)
		}
		if err := account.validate(); err != nil {
			return fmt.Errorf("validate account %q routes: %w", alias, err)
		}
	}
	for _, alias := range aliases {
		if _, conflicts := accountIDs[domain.AccountID(alias)]; conflicts {
			return fmt.Errorf("account alias %q conflicts with an opaque account ID", alias)
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
	if err := configuration.Credentials.validate(); err != nil {
		return err
	}
	return nil
}

// ResolveAccount accepts either a human-facing alias or an opaque account ID.
// It always returns the canonical alias and persisted account definition.
func (configuration Config) ResolveAccount(reference string) (string, Account, error) {
	if len(configuration.Accounts) == 0 {
		return "", Account{}, errors.New(
			"no account is configured; run `corr setup <email-address>` first",
		)
	}
	if reference == "" {
		reference = configuration.DefaultAccount
	}
	if account, exists := configuration.Accounts[reference]; exists {
		return reference, account, nil
	}
	for alias, account := range configuration.Accounts {
		if string(account.ID) == reference {
			return alias, account, nil
		}
	}
	return "", Account{}, fmt.Errorf("account %q is not configured", reference)
}

// AccountByID returns the definition for an already resolved stable account ID.
func (configuration Config) AccountByID(accountID domain.AccountID) (string, Account, bool) {
	for alias, account := range configuration.Accounts {
		if account.ID == accountID {
			return alias, account, true
		}
	}
	return "", Account{}, false
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

func validateAddress(value string) error {
	if value == "" {
		return nil
	}
	if err := validateMailbox(value); err != nil {
		return errors.New("address must be a bare email address")
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
