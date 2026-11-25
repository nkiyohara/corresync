package application

import (
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode"

	"github.com/nkiyohara/corresync/internal/domain"
)

// AccountSignInTool is the only MCP tool that may ask the person to approve a
// sign-in. See docs/adr/0039-user-approved-sign-in-from-mcp.md.
const AccountSignInTool = "account_sign_in"

// AccountStatusTool is the content-free status tool an agent uses to observe
// an unfinished or completed sign-in without starting another one.
const AccountStatusTool = "account_status"

const (
	AccountSignInContractVersion = "1"
	// AccountSignInApprovalTTL bounds how long one reserved approval request
	// may wait for the person's answer in the MCP client.
	AccountSignInApprovalTTL = 5 * time.Minute
	// AccountSignInCooldown is the fixed period after a decline, cancellation,
	// or unfinished MCP-started sign-in during which the same account cannot be
	// prompted again from MCP. A person can always run `corr auth login`.
	AccountSignInCooldown = 10 * time.Minute
)

// AccountSignInStatus is the bounded outcome of one account_sign_in call.
type AccountSignInStatus string

const (
	AccountSignInStarted         AccountSignInStatus = "started"
	AccountSignInAlreadySignedIn AccountSignInStatus = "already_signed_in"
	AccountSignInDeclined        AccountSignInStatus = "declined"
	AccountSignInUnavailable     AccountSignInStatus = "unavailable"
	AccountSignInInProgress      AccountSignInStatus = "in_progress"
)

func (status AccountSignInStatus) Validate() error {
	switch status {
	case AccountSignInStarted, AccountSignInAlreadySignedIn,
		AccountSignInDeclined, AccountSignInUnavailable, AccountSignInInProgress:
		return nil
	default:
		return fmt.Errorf("invalid account sign-in status %q", status)
	}
}

// AccountSignInReason is a bounded classification. It never carries provider,
// browser, or model-supplied text.
type AccountSignInReason string

const (
	AccountSignInReasonUserDeclined            AccountSignInReason = "user_declined"
	AccountSignInReasonUserCancelled           AccountSignInReason = "user_cancelled"
	AccountSignInReasonRecentlyDeclined        AccountSignInReason = "recently_declined"
	AccountSignInReasonRecentlyUnfinished      AccountSignInReason = "recent_sign_in_unfinished"
	AccountSignInReasonElicitationUnsupported  AccountSignInReason = "elicitation_unsupported"
	AccountSignInReasonElicitationFailed       AccountSignInReason = "elicitation_failed"
	AccountSignInReasonDisplayUnavailable      AccountSignInReason = "display_unavailable"
	AccountSignInReasonApprovalExpired         AccountSignInReason = "approval_expired"
	AccountSignInReasonApprovalPending         AccountSignInReason = "approval_pending"
	AccountSignInReasonSignInRunning           AccountSignInReason = "sign_in_running"
	AccountSignInReasonRouteUnsupported        AccountSignInReason = "route_unsupported"
	AccountSignInReasonAliasNotDisplayable     AccountSignInReason = "alias_not_displayable"
	AccountSignInReasonSessionOwnerUnsupported AccountSignInReason = "session_owner_unsupported"
	// AccountSignInReasonCLISignInRequired means the account's current
	// connection settings have never completed a sign-in started from the
	// CLI, so MCP may not offer one (ADR 0039).
	AccountSignInReasonCLISignInRequired AccountSignInReason = "cli_sign_in_required"
	// AccountSignInReasonDestinationNotDisplayable means a sign-in host,
	// client ID, or address cannot be shown as plain ASCII in the prompt.
	AccountSignInReasonDestinationNotDisplayable AccountSignInReason = "destination_not_displayable"
	// AccountSignInReasonStateUnavailable means the persisted cool-down or
	// verification state could not be read; every account is cooled down.
	AccountSignInReasonStateUnavailable AccountSignInReason = "sign_in_state_unavailable"
)

var accountSignInReasonStatus = map[AccountSignInReason]AccountSignInStatus{
	AccountSignInReasonUserDeclined:              AccountSignInDeclined,
	AccountSignInReasonUserCancelled:             AccountSignInDeclined,
	AccountSignInReasonRecentlyDeclined:          AccountSignInDeclined,
	AccountSignInReasonRecentlyUnfinished:        AccountSignInUnavailable,
	AccountSignInReasonElicitationUnsupported:    AccountSignInUnavailable,
	AccountSignInReasonElicitationFailed:         AccountSignInUnavailable,
	AccountSignInReasonDisplayUnavailable:        AccountSignInUnavailable,
	AccountSignInReasonApprovalExpired:           AccountSignInUnavailable,
	AccountSignInReasonRouteUnsupported:          AccountSignInUnavailable,
	AccountSignInReasonAliasNotDisplayable:       AccountSignInUnavailable,
	AccountSignInReasonSessionOwnerUnsupported:   AccountSignInUnavailable,
	AccountSignInReasonCLISignInRequired:         AccountSignInUnavailable,
	AccountSignInReasonDestinationNotDisplayable: AccountSignInUnavailable,
	AccountSignInReasonStateUnavailable:          AccountSignInUnavailable,
	AccountSignInReasonApprovalPending:           AccountSignInInProgress,
	AccountSignInReasonSignInRunning:             AccountSignInInProgress,
}

// AccountSignInResult is the content-free structured result of
// account_sign_in. Guidance is fixed server text selected by status.
type AccountSignInResult struct {
	Version    string                    `json:"version"`
	Status     AccountSignInStatus       `json:"status"`
	Account    domain.AccountID          `json:"account"`
	Alias      string                    `json:"alias"`
	Reason     AccountSignInReason       `json:"reason,omitempty"`
	Guidance   string                    `json:"guidance"`
	StatusTool string                    `json:"statusTool"`
	ExpiresAt  *time.Time                `json:"expiresAt,omitempty"`
	NextAction *AuthenticationNextAction `json:"nextAction,omitempty"`
}

var accountSignInGuidance = map[AccountSignInStatus]string{
	AccountSignInStarted: "The person approved the request and Corresync opened the same browser sign-in as `corr auth login` on its host. " +
		"The person completes password, MFA, and consent in that browser; nothing is entered in chat. " +
		"Corresync finishes the session itself when the browser sign-in completes or expires at expiresAt. " +
		"Check account_status for this account; when the requested service is authenticated, retry the original read once. Do not call account_sign_in again for this account.",
	AccountSignInAlreadySignedIn: "This account already has an active session. Retry the original read once; do not call account_sign_in again.",
	AccountSignInDeclined: "The person declined or cancelled this sign-in request, or declined one recently. Nothing was started. " +
		"Do not ask again and do not call account_sign_in again; tell the person they can run the nextAction command themselves.",
	AccountSignInUnavailable: "Corresync could not ask for or start this sign-in from this MCP client. Nothing was started. " +
		"Tell the person to run the nextAction command on the Corresync host, then stop; do not retry account_sign_in.",
	AccountSignInInProgress: "A sign-in request or browser sign-in for this account is already pending. Do not request it again. " +
		"Wait for the person, then check account_status for this account.",
}

// accountSignInCLIRequiredGuidance replaces the generic unavailable guidance
// when the account's settings were never signed in to from the CLI. The agent
// must not vouch for the destination: the command shows it to the person.
const accountSignInCLIRequiredGuidance = "This account's current connection settings have not been signed in to from the CLI, so Corresync will not offer a sign-in here. Nothing was started. " +
	"Ask the person to run the nextAction command on the Corresync host themselves. It shows the sign-in page it will open and asks them to check it before anything opens; " +
	"tell them to compare that page with the one they normally use. Do not say the page is safe and do not retry account_sign_in."

func accountSignInGuidanceFor(status AccountSignInStatus, reason AccountSignInReason) string {
	if reason == AccountSignInReasonCLISignInRequired {
		return accountSignInCLIRequiredGuidance
	}
	return accountSignInGuidance[status]
}

// NewAccountSignInResult builds the canonical result for a status and reason.
// ExpiresAt is required exactly for started results.
func NewAccountSignInResult(
	status AccountSignInStatus,
	reason AccountSignInReason,
	account domain.AccountID,
	alias string,
	expiresAt *time.Time,
) (AccountSignInResult, error) {
	result := AccountSignInResult{
		Version:    AccountSignInContractVersion,
		Status:     status,
		Account:    account,
		Alias:      alias,
		Reason:     reason,
		Guidance:   accountSignInGuidanceFor(status, reason),
		StatusTool: AccountStatusTool,
	}
	if expiresAt != nil {
		value := expiresAt.UTC()
		result.ExpiresAt = &value
	}
	if status == AccountSignInDeclined || status == AccountSignInUnavailable {
		action := localInteractiveLoginAction(alias, "")
		result.NextAction = &action
	}
	if err := result.Validate(); err != nil {
		return AccountSignInResult{}, err
	}
	return result, nil
}

func (result AccountSignInResult) Validate() error {
	if result.Version != AccountSignInContractVersion {
		return errors.New("unsupported account sign-in result version")
	}
	if err := result.Status.Validate(); err != nil {
		return err
	}
	if err := result.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(result.Alias).Validate(); err != nil {
		return err
	}
	if result.Reason == "" {
		if result.Status != AccountSignInStarted && result.Status != AccountSignInAlreadySignedIn {
			return errors.New("account sign-in result omits its reason")
		}
	} else if status, known := accountSignInReasonStatus[result.Reason]; !known || status != result.Status {
		return fmt.Errorf("account sign-in reason %q does not match status %q", result.Reason, result.Status)
	}
	if result.Guidance != accountSignInGuidanceFor(result.Status, result.Reason) ||
		result.StatusTool != AccountStatusTool {
		return errors.New("account sign-in result has non-canonical guidance")
	}
	if (result.Status == AccountSignInStarted) != (result.ExpiresAt != nil) {
		return errors.New("account sign-in expiry must be present exactly for a started sign-in")
	}
	wantsAction := result.Status == AccountSignInDeclined || result.Status == AccountSignInUnavailable
	if wantsAction != (result.NextAction != nil) {
		return errors.New("account sign-in result has an unexpected next action")
	}
	if result.NextAction != nil {
		if err := result.NextAction.validate(result.Alias); err != nil {
			return err
		}
		if result.NextAction.MCPTool != "" {
			return errors.New("account sign-in result must not point back at the sign-in tool")
		}
	}
	return nil
}

// AccountSignInRouteKind is how approving a service signs it in.
type AccountSignInRouteKind string

const (
	// AccountSignInDedicatedBrowser opens Corresync's dedicated browser
	// profile at the configured origin (Outlook Web, Teams Web).
	AccountSignInDedicatedBrowser AccountSignInRouteKind = "dedicated_browser"
	// AccountSignInProviderAuthorization opens the provider's OAuth
	// authorization page in the system browser.
	AccountSignInProviderAuthorization AccountSignInRouteKind = "provider_authorization"
	// AccountSignInStoredCredential reads an already consented credential
	// from the OS keyring or credential helper; no page opens.
	AccountSignInStoredCredential AccountSignInRouteKind = "stored_credential" //nolint:gosec // Route kind name, not a credential.
)

func (kind AccountSignInRouteKind) Validate() error {
	switch kind {
	case AccountSignInDedicatedBrowser, AccountSignInProviderAuthorization, AccountSignInStoredCredential:
		return nil
	default:
		return fmt.Errorf("invalid account sign-in route kind %q", kind)
	}
}

// AccountSignInRoute names one released, enabled service, its provider, and
// where approving it signs in. Host and ClientID come only from configuration
// and fixed provider endpoints and are the route details shown in the prompt.
type AccountSignInRoute struct {
	Service  AuthenticationService  `json:"service"`
	Provider domain.ProviderID      `json:"provider"`
	Kind     AccountSignInRouteKind `json:"kind"`
	Host     string                 `json:"host,omitempty"`
	ClientID string                 `json:"clientId,omitempty"`
	// AdditionalHosts names further servers a stored credential is sent to,
	// such as the SMTP submission server of an IMAP/SMTP route.
	AdditionalHosts []string `json:"additionalHosts,omitempty"`
}

func (route AccountSignInRoute) Validate() error {
	if err := route.Service.Validate(); err != nil {
		return err
	}
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	if err := route.Kind.Validate(); err != nil {
		return err
	}
	if route.Kind != AccountSignInStoredCredential && route.Host == "" {
		return errors.New("a browser sign-in route must name its host")
	}
	if route.Host != "" && !AccountSignInHostDisplayable(route.Host) {
		return errors.New("account sign-in host cannot be displayed safely")
	}
	if len(route.AdditionalHosts) > 2 ||
		len(route.AdditionalHosts) > 0 && route.Kind != AccountSignInStoredCredential {
		return errors.New("account sign-in route has unexpected additional hosts")
	}
	for _, host := range route.AdditionalHosts {
		if !AccountSignInHostDisplayable(host) {
			return errors.New("account sign-in host cannot be displayed safely")
		}
	}
	if route.ClientID != "" && (route.Kind != AccountSignInProviderAuthorization ||
		!accountSignInClientIDPattern.MatchString(route.ClientID)) {
		return errors.New("account sign-in client ID cannot be displayed safely")
	}
	return nil
}

var (
	accountSignInHostPattern = regexp.MustCompile(
		`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+(?::[0-9]{1,5})?$`,
	)
	accountSignInClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
	accountSignInAddressPattern  = regexp.MustCompile(
		`^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`,
	)
)

// AccountSignInHostDisplayable accepts only lowercase ASCII DNS names (IDNA
// names in their xn-- form) with an optional port, so a look-alike host can
// never be rendered from Unicode.
func AccountSignInHostDisplayable(host string) bool {
	return len(host) <= 253 && accountSignInHostPattern.MatchString(host)
}

// AccountSignInAddressDisplayable accepts only a plain ASCII mailbox address.
func AccountSignInAddressDisplayable(address string) bool {
	return len(address) <= 254 && accountSignInAddressPattern.MatchString(address)
}

var accountSignInTicketPattern = regexp.MustCompile(`^sit1_[A-Za-z0-9_-]{43}$`)

// AccountSignInApproval reserves one pending approval request for one account
// and one MCP caller. The ticket is an unguessable one-use reference.
type AccountSignInApproval struct {
	Ticket    string               `json:"ticket"`
	Account   domain.AccountID     `json:"account"`
	Alias     string               `json:"alias"`
	Address   string               `json:"address,omitempty"`
	Routes    []AccountSignInRoute `json:"routes"`
	ExpiresAt time.Time            `json:"expiresAt"`
}

func (approval AccountSignInApproval) Validate() error {
	if err := ValidateAccountSignInTicket(approval.Ticket); err != nil {
		return err
	}
	if err := approval.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(approval.Alias).Validate(); err != nil {
		return err
	}
	if approval.Address != "" && !AccountSignInAddressDisplayable(approval.Address) {
		return errors.New("account sign-in address cannot be displayed safely")
	}
	if len(approval.Routes) == 0 || len(approval.Routes) > 4 {
		return errors.New("account sign-in approval must name one to four services")
	}
	for _, route := range approval.Routes {
		if err := route.Validate(); err != nil {
			return err
		}
	}
	if approval.ExpiresAt.IsZero() {
		return errors.New("account sign-in approval has no expiry")
	}
	return nil
}

// ValidateAccountSignInTicket checks the opaque one-use ticket shape.
func ValidateAccountSignInTicket(ticket string) error {
	if !accountSignInTicketPattern.MatchString(ticket) {
		return errors.New("invalid account sign-in ticket")
	}
	return nil
}

// AccountSignInRequest is the session owner's answer to a sign-in request:
// either a final result, or one reserved approval request to show the person.
type AccountSignInRequest struct {
	Result   *AccountSignInResult   `json:"result,omitempty"`
	Approval *AccountSignInApproval `json:"approval,omitempty"`
}

func (request AccountSignInRequest) Validate(account domain.AccountID) error {
	switch {
	case request.Result != nil && request.Approval == nil:
		if err := request.Result.Validate(); err != nil {
			return err
		}
		if request.Result.Status == AccountSignInStarted {
			return errors.New("a sign-in request cannot start before approval")
		}
		if request.Result.Account != account {
			return errors.New("sign-in request result names a different account")
		}
	case request.Approval != nil && request.Result == nil:
		if err := request.Approval.Validate(); err != nil {
			return err
		}
		if request.Approval.Account != account {
			return errors.New("sign-in approval names a different account")
		}
	default:
		return errors.New("sign-in request must contain exactly one result or approval")
	}
	return nil
}

// AccountSignInDecision is what the MCP server observed from the person's
// elicitation answer. Only AccountSignInDecisionApprovedByUser can start a
// sign-in, and only the elicitation path of the MCP server sends it.
type AccountSignInDecision string

const (
	AccountSignInDecisionApprovedByUser         AccountSignInDecision = "approved_by_user_elicitation"
	AccountSignInDecisionDeclinedByUser         AccountSignInDecision = "declined_by_user"
	AccountSignInDecisionCancelledByUser        AccountSignInDecision = "cancelled_by_user"
	AccountSignInDecisionElicitationFailed      AccountSignInDecision = "elicitation_failed"
	AccountSignInDecisionElicitationUnsupported AccountSignInDecision = "elicitation_unsupported"
)

func (decision AccountSignInDecision) Validate() error {
	switch decision {
	case AccountSignInDecisionApprovedByUser, AccountSignInDecisionDeclinedByUser,
		AccountSignInDecisionCancelledByUser, AccountSignInDecisionElicitationFailed,
		AccountSignInDecisionElicitationUnsupported:
		return nil
	default:
		return fmt.Errorf("invalid account sign-in decision %q", decision)
	}
}

// AccountSignInResolution consumes one reserved approval request.
type AccountSignInResolution struct {
	Account  domain.AccountID      `json:"account"`
	Ticket   string                `json:"ticket"`
	Decision AccountSignInDecision `json:"decision"`
}

func (resolution AccountSignInResolution) Validate() error {
	if err := resolution.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := ValidateAccountSignInTicket(resolution.Ticket); err != nil {
		return err
	}
	return resolution.Decision.Validate()
}

// AccountSignInAliasDisplayable reports whether an alias is safe to quote in
// a server-authored approval prompt. Aliases with punctuation that could frame
// prompt text, or that mix Latin, Cyrillic, and Greek letters into a
// look-alike, are refused instead of being shown.
func AccountSignInAliasDisplayable(alias string) bool {
	if domain.AccountAlias(alias).Validate() != nil {
		return false
	}
	confusableScripts := map[string]bool{}
	for _, character := range alias {
		switch {
		case unicode.IsLetter(character):
			for name, table := range map[string]*unicode.RangeTable{
				"latin": unicode.Latin, "cyrillic": unicode.Cyrillic, "greek": unicode.Greek,
			} {
				if unicode.Is(table, character) {
					confusableScripts[name] = true
				}
			}
		case unicode.IsDigit(character):
			if character > unicode.MaxASCII {
				return false
			}
		case character == '.', character == '_', character == '-',
			character == '@', character == '+':
		default:
			return false
		}
	}
	return len(confusableScripts) <= 1
}
