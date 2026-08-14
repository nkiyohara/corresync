package application

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/domain"
)

const AuthenticationContractVersion = "1"

// AuthenticationService is one independently routed account service.
type AuthenticationService string

const (
	AuthenticationServiceMail     AuthenticationService = "mail"
	AuthenticationServiceCalendar AuthenticationService = "calendar"
	AuthenticationServiceTasks    AuthenticationService = "tasks"
)

func (service AuthenticationService) Validate() error {
	switch service {
	case AuthenticationServiceMail,
		AuthenticationServiceCalendar,
		AuthenticationServiceTasks:
		return nil
	default:
		return fmt.Errorf("invalid authentication service %q", service)
	}
}

// AuthenticationState is the most recent content-free runtime observation for
// one account service. It does not imply that a remote grant can never expire.
type AuthenticationState string

const (
	AuthenticationStateSignedOut              AuthenticationState = "signed_out"
	AuthenticationStatePending                AuthenticationState = "authentication_pending"
	AuthenticationStateAuthenticated          AuthenticationState = "authenticated"
	AuthenticationStateReauthenticationNeeded AuthenticationState = "reauthentication_required"
)

func (state AuthenticationState) Validate() error {
	switch state {
	case AuthenticationStateSignedOut,
		AuthenticationStatePending,
		AuthenticationStateAuthenticated,
		AuthenticationStateReauthenticationNeeded:
		return nil
	default:
		return fmt.Errorf("invalid authentication state %q", state)
	}
}

// AuthenticationReason is a bounded classification. Raw provider responses
// and credential details must never be copied into it.
type AuthenticationReason string

const (
	AuthenticationReasonNeverAuthenticated  AuthenticationReason = "never_authenticated"
	AuthenticationReasonUserSignedOut       AuthenticationReason = "user_signed_out"
	AuthenticationReasonInteractionPending  AuthenticationReason = "interaction_pending"
	AuthenticationReasonSessionExpired      AuthenticationReason = "session_expired"
	AuthenticationReasonGrantRevoked        AuthenticationReason = "grant_revoked"
	AuthenticationReasonCredentialRejected  AuthenticationReason = "credential_rejected" //nolint:gosec // Stable error classification, never a credential value.
	AuthenticationReasonInteractionRequired AuthenticationReason = "interaction_required"
)

func (reason AuthenticationReason) Validate() error {
	switch reason {
	case AuthenticationReasonNeverAuthenticated,
		AuthenticationReasonUserSignedOut,
		AuthenticationReasonInteractionPending,
		AuthenticationReasonSessionExpired,
		AuthenticationReasonGrantRevoked,
		AuthenticationReasonCredentialRejected,
		AuthenticationReasonInteractionRequired:
		return nil
	default:
		return fmt.Errorf("invalid authentication reason %q", reason)
	}
}

// AuthenticationCode is the stable machine-readable class returned to a
// caller that must wait for a local human-owned authentication action.
type AuthenticationCode string

const (
	AuthenticationCodeRequired             AuthenticationCode = "authentication_required"
	AuthenticationCodePending              AuthenticationCode = "authentication_pending"
	AuthenticationCodeReauthenticationNeed AuthenticationCode = "reauthentication_required"
)

func (code AuthenticationCode) Validate() error {
	switch code {
	case AuthenticationCodeRequired,
		AuthenticationCodePending,
		AuthenticationCodeReauthenticationNeed:
		return nil
	default:
		return fmt.Errorf("invalid authentication code %q", code)
	}
}

// AuthenticationCommand is deliberately argv-shaped rather than shell text.
type AuthenticationCommand struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// AuthenticationNextAction describes the only automatic-free recovery action
// Corresync permits ordinary CLI and MCP callers to offer.
type AuthenticationNextAction struct {
	Kind                     string                `json:"kind"`
	Command                  AuthenticationCommand `json:"command"`
	RequiresUserConsent      bool                  `json:"requiresUserConsent"`
	RequiresHumanInteraction bool                  `json:"requiresHumanInteraction"`
	SecretsAllowedInMCP      bool                  `json:"secretsAllowedInMCP"`
}

// AuthenticationRetry makes route preservation and write non-replay explicit.
type AuthenticationRetry struct {
	Automatic                     bool   `json:"automatic"`
	AfterAction                   string `json:"afterAction"`
	AlternativeRequiresUserChoice bool   `json:"alternativeRequiresUserChoice"`
}

// AuthenticationActionRequired is content-free account routing metadata. It
// contains neither the configured address nor a provider response.
type AuthenticationActionRequired struct {
	Version    string                   `json:"version"`
	Code       AuthenticationCode       `json:"code"`
	Account    domain.AccountID         `json:"account"`
	Alias      string                   `json:"alias"`
	Service    AuthenticationService    `json:"service"`
	Provider   domain.ProviderID        `json:"provider"`
	Reason     AuthenticationReason     `json:"reason"`
	NextAction AuthenticationNextAction `json:"nextAction"`
	Retry      AuthenticationRetry      `json:"retry"`
}

func NewAuthenticationActionRequired(
	state AuthenticationState,
	reason AuthenticationReason,
	account domain.AccountID,
	alias string,
	service AuthenticationService,
	provider domain.ProviderID,
) (AuthenticationActionRequired, error) {
	code := AuthenticationCodeRequired
	switch state {
	case AuthenticationStatePending:
		code = AuthenticationCodePending
	case AuthenticationStateReauthenticationNeeded:
		code = AuthenticationCodeReauthenticationNeed
	case AuthenticationStateSignedOut:
	case AuthenticationStateAuthenticated:
		return AuthenticationActionRequired{}, errors.New(
			"an authenticated service does not require an authentication action",
		)
	default:
		return AuthenticationActionRequired{}, fmt.Errorf(
			"cannot create an action for authentication state %q",
			state,
		)
	}
	action := AuthenticationActionRequired{
		Version:  AuthenticationContractVersion,
		Code:     code,
		Account:  account,
		Alias:    alias,
		Service:  service,
		Provider: provider,
		Reason:   reason,
		NextAction: AuthenticationNextAction{
			Kind: "local_interactive_login",
			Command: AuthenticationCommand{
				Executable: "corr",
				Args:       []string{"auth", "login", "--account", alias},
			},
			RequiresUserConsent:      true,
			RequiresHumanInteraction: true,
			SecretsAllowedInMCP:      false,
		},
		Retry: AuthenticationRetry{
			Automatic:                     false,
			AfterAction:                   "retry_same_read_once",
			AlternativeRequiresUserChoice: true,
		},
	}
	if err := action.Validate(); err != nil {
		return AuthenticationActionRequired{}, err
	}
	return action, nil
}

func (action AuthenticationActionRequired) Validate() error {
	if action.Version != AuthenticationContractVersion {
		return errors.New("unsupported authentication action version")
	}
	if err := action.Code.Validate(); err != nil {
		return err
	}
	if err := action.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(action.Alias).Validate(); err != nil {
		return err
	}
	if err := action.Service.Validate(); err != nil {
		return err
	}
	if err := action.Provider.Validate(); err != nil {
		return err
	}
	if err := action.Reason.Validate(); err != nil {
		return err
	}
	if action.NextAction.Kind != "local_interactive_login" ||
		action.NextAction.Command.Executable != "corr" ||
		len(action.NextAction.Command.Args) != 4 ||
		action.NextAction.Command.Args[0] != "auth" ||
		action.NextAction.Command.Args[1] != "login" ||
		action.NextAction.Command.Args[2] != "--account" ||
		action.NextAction.Command.Args[3] != action.Alias ||
		!action.NextAction.RequiresUserConsent ||
		!action.NextAction.RequiresHumanInteraction ||
		action.NextAction.SecretsAllowedInMCP {
		return errors.New("authentication action has an unsafe next action")
	}
	if action.Retry.Automatic ||
		action.Retry.AfterAction != "retry_same_read_once" ||
		!action.Retry.AlternativeRequiresUserChoice {
		return errors.New("authentication action has an unsafe retry policy")
	}
	return nil
}

// AuthenticationActionError preserves a typed action for transports while its
// Error string remains the complete JSON fallback required by older clients.
type AuthenticationActionError struct {
	Action AuthenticationActionRequired
}

func (failure *AuthenticationActionError) Error() string {
	encoded, err := json.Marshal(failure.Action)
	if err != nil {
		return `{"version":"1","code":"authentication_required"}`
	}
	return string(encoded)
}

func NewAuthenticationActionError(action AuthenticationActionRequired) error {
	if err := action.Validate(); err != nil {
		return err
	}
	return &AuthenticationActionError{Action: action}
}

func AuthenticationActionFromError(
	err error,
) (AuthenticationActionRequired, bool) {
	var failure *AuthenticationActionError
	if !errors.As(err, &failure) || failure == nil ||
		failure.Action.Validate() != nil {
		return AuthenticationActionRequired{}, false
	}
	return failure.Action, true
}

// ProviderAuthenticationFailure is adapter evidence without routing metadata.
// The session owner consumes it, invalidates the exact shared resource, and
// replaces it with AuthenticationActionError before returning to a caller.
type ProviderAuthenticationFailure struct {
	Reason AuthenticationReason
	cause  error
}

func (failure *ProviderAuthenticationFailure) Error() string {
	return "provider authentication is no longer usable"
}

func (failure *ProviderAuthenticationFailure) Unwrap() error {
	return failure.cause
}

func NewProviderAuthenticationFailure(
	reason AuthenticationReason,
	cause error,
) error {
	if err := reason.Validate(); err != nil {
		return err
	}
	if cause == nil {
		cause = errors.New("provider rejected authentication")
	}
	return &ProviderAuthenticationFailure{Reason: reason, cause: cause}
}

func ProviderAuthenticationReason(err error) (AuthenticationReason, bool) {
	var failure *ProviderAuthenticationFailure
	if !errors.As(err, &failure) || failure == nil ||
		failure.Reason.Validate() != nil {
		return "", false
	}
	return failure.Reason, true
}

// ServiceAuthenticationStatus is one content-free per-service state snapshot.
type ServiceAuthenticationStatus struct {
	Service  AuthenticationService         `json:"service"`
	Provider domain.ProviderID             `json:"provider"`
	State    AuthenticationState           `json:"state"`
	Reason   AuthenticationReason          `json:"reason,omitempty"`
	Action   *AuthenticationActionRequired `json:"action,omitempty"`
}

func (status ServiceAuthenticationStatus) Validate(
	account domain.AccountID,
	alias string,
) error {
	if err := status.Service.Validate(); err != nil {
		return err
	}
	if err := status.Provider.Validate(); err != nil {
		return err
	}
	if err := status.State.Validate(); err != nil {
		return err
	}
	if status.State == AuthenticationStateAuthenticated {
		if status.Reason != "" || status.Action != nil {
			return errors.New("authenticated service includes a recovery action")
		}
		return nil
	}
	if err := status.Reason.Validate(); err != nil {
		return err
	}
	if status.Action == nil {
		return errors.New("inactive service omits its recovery action")
	}
	if err := status.Action.Validate(); err != nil {
		return err
	}
	if status.Action.Account != account || status.Action.Alias != alias ||
		status.Action.Service != status.Service ||
		status.Action.Provider != status.Provider ||
		status.Action.Reason != status.Reason {
		return errors.New("service recovery action does not match its status")
	}
	return nil
}

// ServiceAuthenticationStatuses uses fixed fields so additions are explicit
// schema changes and serialized output remains deterministic.
type ServiceAuthenticationStatuses struct {
	Mail     *ServiceAuthenticationStatus `json:"mail,omitempty"`
	Calendar *ServiceAuthenticationStatus `json:"calendar,omitempty"`
	Tasks    *ServiceAuthenticationStatus `json:"tasks,omitempty"`
}

func (statuses ServiceAuthenticationStatuses) Values() []ServiceAuthenticationStatus {
	values := make([]ServiceAuthenticationStatus, 0, 3)
	for _, status := range []*ServiceAuthenticationStatus{
		statuses.Mail,
		statuses.Calendar,
		statuses.Tasks,
	} {
		if status != nil {
			values = append(values, *status)
		}
	}
	return values
}

func (statuses *ServiceAuthenticationStatuses) Set(
	status ServiceAuthenticationStatus,
) error {
	if statuses == nil {
		return errors.New("authentication status destination is nil")
	}
	copy := status
	switch status.Service {
	case AuthenticationServiceMail:
		statuses.Mail = &copy
	case AuthenticationServiceCalendar:
		statuses.Calendar = &copy
	case AuthenticationServiceTasks:
		statuses.Tasks = &copy
	default:
		return status.Service.Validate()
	}
	return nil
}
