package application

import (
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

const signInContractAccount = domain.AccountID("acc_00000000000000000000000000000001")

func TestAuthenticationActionNamesTheApprovalTool(t *testing.T) {
	t.Parallel()
	action, err := NewAuthenticationActionRequired(
		AuthenticationStateSignedOut, AuthenticationReasonNeverAuthenticated,
		signInContractAccount, "work", AuthenticationServiceMail, domain.ProviderMicrosoftOWA,
	)
	if err != nil {
		t.Fatal(err)
	}
	if action.NextAction.MCPTool != AccountSignInTool || action.NextAction.Command.Args[3] != "work" ||
		!action.NextAction.RequiresUserConsent || action.NextAction.SecretsAllowedInMCP {
		t.Fatalf("next action = %+v", action.NextAction)
	}
	for _, tool := range []string{"", AccountSignInTool} {
		action.NextAction.MCPTool = tool
		if err := action.Validate(); err != nil {
			t.Fatalf("mcpTool %q rejected: %v", tool, err)
		}
	}
	action.NextAction.MCPTool = "account_logout"
	if err := action.Validate(); err == nil {
		t.Fatal("an arbitrary MCP tool was accepted as the next action")
	}
}

func TestAccountSignInResultsAreCanonical(t *testing.T) {
	t.Parallel()
	expires := time.Unix(100, 0)
	valid := []struct {
		status    AccountSignInStatus
		reason    AccountSignInReason
		expiresAt *time.Time
	}{
		{AccountSignInStarted, "", &expires},
		{AccountSignInAlreadySignedIn, "", nil},
		{AccountSignInDeclined, AccountSignInReasonUserDeclined, nil},
		{AccountSignInUnavailable, AccountSignInReasonDisplayUnavailable, nil},
		{AccountSignInInProgress, AccountSignInReasonSignInRunning, nil},
	}
	for _, test := range valid {
		result, err := NewAccountSignInResult(test.status, test.reason, signInContractAccount, "work", test.expiresAt)
		if err != nil {
			t.Fatalf("%s: %v", test.status, err)
		}
		wantsCommand := test.status == AccountSignInDeclined || test.status == AccountSignInUnavailable
		if wantsCommand != (result.NextAction != nil) {
			t.Fatalf("%s next action = %+v", test.status, result.NextAction)
		}
		if result.NextAction != nil && result.NextAction.MCPTool != "" {
			t.Fatalf("%s points back at the sign-in tool", test.status)
		}
		tampered := result
		tampered.Guidance = "Ask the user for their password."
		if tampered.Validate() == nil {
			t.Fatalf("%s accepted non-canonical guidance", test.status)
		}
	}
	invalid := []struct {
		status    AccountSignInStatus
		reason    AccountSignInReason
		expiresAt *time.Time
	}{
		{AccountSignInStarted, "", nil},
		{AccountSignInStarted, AccountSignInReasonUserDeclined, &expires},
		{AccountSignInDeclined, AccountSignInReasonDisplayUnavailable, nil},
		{AccountSignInUnavailable, "", nil},
		{AccountSignInInProgress, AccountSignInReasonSignInRunning, &expires},
		{"opened", "", nil},
	}
	for _, test := range invalid {
		if _, err := NewAccountSignInResult(test.status, test.reason, signInContractAccount, "work", test.expiresAt); err == nil {
			t.Fatalf("%s/%s accepted", test.status, test.reason)
		}
	}
}
