package application

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestAuthenticationActionRequiredIsContentFreeAndArgvShaped(t *testing.T) {
	t.Parallel()

	action, err := NewAuthenticationActionRequired(
		AuthenticationStateReauthenticationNeeded,
		AuthenticationReasonSessionExpired,
		domain.AccountID("acc_00000000000000000000000000000001"),
		"work",
		AuthenticationServiceMail,
		domain.ProviderMicrosoftOWA,
	)
	if err != nil {
		t.Fatalf("NewAuthenticationActionRequired() error = %v", err)
	}
	failure := NewAuthenticationActionError(action)
	decoded := AuthenticationActionRequired{}
	if err := json.Unmarshal([]byte(failure.Error()), &decoded); err != nil {
		t.Fatalf("error fallback is not JSON: %v", err)
	}
	if decoded.Code != AuthenticationCodeReauthenticationNeed ||
		decoded.NextAction.Command.Executable != "corr" ||
		strings.Join(decoded.NextAction.Command.Args, " ") !=
			"auth login --account work" ||
		decoded.Retry.Automatic || decoded.NextAction.SecretsAllowedInMCP {
		t.Fatalf("action = %+v", decoded)
	}
	if strings.Contains(failure.Error(), "example.com") ||
		strings.Contains(failure.Error(), "cookie") {
		t.Fatalf("action exposed private authentication material: %s", failure)
	}
	got, ok := AuthenticationActionFromError(
		fmtWrappedError{cause: failure},
	)
	if !ok || !reflect.DeepEqual(got, action) {
		t.Fatalf("AuthenticationActionFromError() = %+v, %t", got, ok)
	}
}

func TestProviderAuthenticationFailureHidesCause(t *testing.T) {
	t.Parallel()

	secret := errors.New("bearer secret-value")
	failure := NewProviderAuthenticationFailure(
		AuthenticationReasonCredentialRejected,
		secret,
	)
	if strings.Contains(failure.Error(), "secret-value") ||
		!errors.Is(failure, secret) {
		t.Fatalf("provider authentication failure = %v", failure)
	}
	reason, ok := ProviderAuthenticationReason(failure)
	if !ok || reason != AuthenticationReasonCredentialRejected {
		t.Fatalf("ProviderAuthenticationReason() = %q, %t", reason, ok)
	}
}

type fmtWrappedError struct{ cause error }

func (failure fmtWrappedError) Error() string { return "wrapped" }
func (failure fmtWrappedError) Unwrap() error { return failure.cause }
