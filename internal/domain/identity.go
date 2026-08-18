package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var opaqueAccountIDPattern = regexp.MustCompile(`^acc_[a-f0-9]{32}$`)

// AccountID is an opaque identifier for one configured mailbox account.
type AccountID string

// Validate ensures an account identifier is safe to use in policy boundaries.
func (account AccountID) Validate() error {
	return validateIdentifier("account", string(account), 128)
}

// ValidateOpaque ensures a persisted account identifier is generated,
// non-personal, and independent of an address or mutable alias.
func (account AccountID) ValidateOpaque() error {
	if !opaqueAccountIDPattern.MatchString(string(account)) {
		return errorsAccountID()
	}
	return nil
}

// NewAccountID returns a random local account identifier. It contains no
// provider, address, alias, tenant, or mailbox information.
func NewAccountID() (AccountID, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate account ID: %w", err)
	}
	return AccountID("acc_" + hex.EncodeToString(value)), nil
}

func errorsAccountID() error {
	return errors.New("account ID must use the opaque acc_<32 lowercase hex> form")
}

// AccountAlias is a mutable, human-facing local account name.
type AccountAlias string

// Validate rejects aliases that cannot safely cross CLI, config, or IPC
// boundaries. Aliases are never used as persistent storage keys.
func (alias AccountAlias) Validate() error {
	if opaqueAccountIDPattern.MatchString(string(alias)) {
		return errors.New("account alias must not use the opaque account ID form")
	}
	return validateIdentifier("account alias", string(alias), 64)
}

// ProviderID identifies one explicit provider adapter.
type ProviderID string

const (
	ProviderMicrosoftOWA   ProviderID = "microsoft-owa"
	ProviderMicrosoftGraph ProviderID = "microsoft-graph"
	ProviderGoogle         ProviderID = "google"
	ProviderGoogleWeb      ProviderID = "google-web"
	ProviderJMAP           ProviderID = "jmap"
	ProviderIMAPSMTP       ProviderID = "imap-smtp"
	ProviderCalDAV         ProviderID = "caldav"
	ProviderPOP3           ProviderID = "pop3"
	ProviderMicrosoftTasks ProviderID = "microsoft-web-tasks"
	ProviderTodoist        ProviderID = "todoist"
	ProviderGoogleTasks    ProviderID = "google-tasks"
	ProviderAppleReminders ProviderID = "apple-reminders"
	ProviderTickTick       ProviderID = "ticktick"
	ProviderAnyDoMCP       ProviderID = "anydo-mcp"
	ProviderThings         ProviderID = "things"
	ProviderOmniFocus      ProviderID = "omnifocus"
)

var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// Validate ensures a provider ID is stable and safe for schemas and routing.
func (provider ProviderID) Validate() error {
	if len(provider) > 64 || !providerIDPattern.MatchString(string(provider)) {
		return fmt.Errorf("invalid provider ID %q", provider)
	}
	return nil
}

// MessagingProviderID is separate from the mail/calendar/task provider union
// so adding a communications route cannot accidentally make it selectable by
// an unrelated service switch.
type MessagingProviderID string

const (
	MessagingProviderMicrosoftTeams MessagingProviderID = "microsoft-teams"
	MessagingProviderSlack          MessagingProviderID = "slack"
	MessagingProviderMattermost     MessagingProviderID = "mattermost"
)

func (provider MessagingProviderID) Validate() error {
	switch provider {
	case MessagingProviderMicrosoftTeams, MessagingProviderSlack, MessagingProviderMattermost:
		return nil
	default:
		return fmt.Errorf("invalid messaging provider ID %q", provider)
	}
}

// MessagingRouteKind identifies one explicitly selected transport. Keeping it
// in the domain lets configuration and application code share the same closed
// identity without either layer depending on a provider adapter.
type MessagingRouteKind string

const (
	MessagingRouteTeamsGraph MessagingRouteKind = "teams_graph"
	MessagingRouteTeamsWeb   MessagingRouteKind = "teams_web"
	MessagingRouteSlackAPI   MessagingRouteKind = "slack_api"
	MessagingRouteMattermost MessagingRouteKind = "mattermost_api"
)

func (kind MessagingRouteKind) Validate() error {
	switch kind {
	case MessagingRouteTeamsGraph, MessagingRouteTeamsWeb,
		MessagingRouteSlackAPI, MessagingRouteMattermost:
		return nil
	default:
		return fmt.Errorf("unsupported messaging route %q", kind)
	}
}

// Caller identifies the local adapter instance requesting an operation.
type Caller struct {
	Surface  string `json:"surface"`
	Instance string `json:"instance"`
}

// Validate ensures a caller can be safely bound to an approval token.
func (caller Caller) Validate() error {
	if err := validateIdentifier("caller surface", caller.Surface, 32); err != nil {
		return err
	}
	return validateIdentifier("caller instance", caller.Instance, 128)
}

func validateIdentifier(field, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", field, maximum)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not start or end with whitespace", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}
