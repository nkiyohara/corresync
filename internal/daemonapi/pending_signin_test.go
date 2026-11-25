package daemonapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

func pendingSignInTestAccount() SessionStatus {
	return SessionStatus{
		Account: testAccountID, Alias: "work",
		Provider:     domain.ProviderMicrosoftOWA,
		MailProvider: domain.ProviderMicrosoftOWA, State: "signed_out",
		Services: testSessionAuthenticationStatuses(
			testAccountID,
			"work",
			domain.ProviderMicrosoftOWA,
		),
	}
}

func TestSessionStatusResultFromBeforeProtocol31StillValidates(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(map[string]any{
		"accounts": []SessionStatus{pendingSignInTestAccount()},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result SessionStatusResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode old session status: %v", err)
	}
	if err := validateSessionStatusResult(result); err != nil {
		t.Fatalf("old session status error = %v", err)
	}
	if _, exists := result.PendingSignIn(testAccountID); exists || result.PendingSignIns != nil {
		t.Fatalf("old session status has a pending sign-in: %+v", result.PendingSignIns)
	}
}

func TestSessionStatusResultCarriesOnlyKindAndExpiryOfPendingSignIn(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, 11, 25, 10, 3, 0, 0, time.UTC)
	result := SessionStatusResult{
		Accounts: []SessionStatus{pendingSignInTestAccount()},
		PendingSignIns: []PendingSignIn{{
			Account: testAccountID, Kind: PendingSignInLoginView, ExpiresAt: expiresAt,
		}},
	}
	if err := validateSessionStatusResult(result); err != nil {
		t.Fatalf("validateSessionStatusResult() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	want := `"pendingSignIns":[{"account":"` + testAccountID +
		`","kind":"login_view","expiresAt":"2026-11-25T10:03:00Z"}]`
	if !strings.Contains(string(encoded), want) {
		t.Fatalf("encoded status = %s, want %s", encoded, want)
	}
	if application := result.Application(); len(application.Accounts) != 1 {
		t.Fatalf("Application() = %+v", application)
	}
	if pending, exists := result.PendingSignIn(testAccountID); !exists || pending.Kind != PendingSignInLoginView {
		t.Fatalf("PendingSignIn() = %+v, %t", pending, exists)
	}
}

func TestValidateSessionStatusResultRejectsInvalidPendingSignIns(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Minute)
	for name, pending := range map[string][]PendingSignIn{
		"unknown account": {{Account: testAccountID2, Kind: PendingSignInTextRelay, ExpiresAt: expiresAt}},
		"unknown kind":    {{Account: testAccountID, Kind: "browser_tab", ExpiresAt: expiresAt}},
		"no expiry":       {{Account: testAccountID, Kind: PendingSignInTextRelay}},
		"duplicate": {
			{Account: testAccountID, Kind: PendingSignInTextRelay, ExpiresAt: expiresAt},
			{Account: testAccountID, Kind: PendingSignInLoginView, ExpiresAt: expiresAt},
		},
	} {
		result := SessionStatusResult{
			Accounts:       []SessionStatus{pendingSignInTestAccount()},
			PendingSignIns: pending,
		}
		if err := validateSessionStatusResult(result); err == nil {
			t.Errorf("%s: validateSessionStatusResult() accepted %+v", name, pending)
		}
	}
}
