package main

import (
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/rollout"
)

const dormantMessagingAccountID = domain.AccountID("acc_00000000000000000000000000000901")

func TestMessagingOnlyAccountFailsBeforeAuthentication(t *testing.T) {
	t.Parallel()
	configured := dormantSlackAccount()
	backend := &sessionBackend{
		configuration: config.Config{Accounts: map[string]config.Account{"messages": configured}},
		accounts:      make(map[domain.AccountID]sessionAccount),
	}

	if _, err := backend.activateAccount(t.Context(), dormantMessagingAccountID); !errors.Is(err, rollout.ErrMessagingPending) {
		t.Fatalf("activateAccount() error = %v, want messaging release gate", err)
	}
	if len(backend.accounts) != 0 {
		t.Fatalf("activateAccount() stored a dormant session: %#v", backend.accounts)
	}
}

func TestMessagingDaemonSurfaceFailsAtReleaseGate(t *testing.T) {
	t.Parallel()
	configured := dormantSlackAccount()
	backend := &sessionBackend{
		configuration: config.Config{Accounts: map[string]config.Account{"messages": configured}},
		accounts:      make(map[domain.AccountID]sessionAccount),
	}
	caller := domain.Caller{Surface: "cli", Instance: "messages-gate-test"}

	_, err := backend.ListConversations(t.Context(), application.ConversationListInput{
		Account: dormantMessagingAccountID, WorkspaceID: "T-SYNTHETIC", Limit: 25,
	}, caller)
	if !errors.Is(err, rollout.ErrMessagingPending) {
		t.Fatalf("ListConversations() error = %v, want messaging release gate", err)
	}
	_, err = backend.CommitSendMessage(t.Context(), "opv1_unreachable", caller)
	if !errors.Is(err, rollout.ErrMessagingPending) {
		t.Fatalf("CommitSendMessage() error = %v, want messaging release gate", err)
	}
}

func TestDormantMessagingRouteHasTypedSignedOutStatus(t *testing.T) {
	t.Parallel()
	configured := dormantSlackAccount()
	backend := &sessionBackend{
		configuration: config.Config{Accounts: map[string]config.Account{"messages": configured}},
		accounts:      make(map[domain.AccountID]sessionAccount),
	}

	result, err := backend.SessionStatus(t.Context(), domain.Caller{
		Surface: "cli", Instance: "messages-status-test",
	})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if len(result.Accounts) != 1 {
		t.Fatalf("SessionStatus() = %#v", result)
	}
	status := result.Accounts[0]
	if status.MessagingProvider != domain.MessagingProviderSlack ||
		status.Provider != "" || status.Services.Messages == nil ||
		status.Services.Messages.Service != application.AuthenticationServiceMessages ||
		status.Services.Messages.Provider != domain.ProviderID(domain.MessagingProviderSlack) ||
		status.Services.Messages.State != application.AuthenticationStateSignedOut {
		t.Fatalf("messaging session status = %#v", status)
	}
}

func TestInactiveMessagingRoutePreservesReleasedServices(t *testing.T) {
	t.Parallel()
	configured := dormantSlackAccount()
	configured.Mail = &config.MailRoute{}

	degradation, err := inactiveMessagingRoute(configured)
	if err != nil {
		t.Fatalf("inactiveMessagingRoute() error = %v", err)
	}
	if degradation == nil || degradation.Feature != "messages.route" {
		t.Fatalf("inactiveMessagingRoute() = %#v", degradation)
	}
	if degradation, err := inactiveMessagingRoute(config.Account{}); err != nil || degradation != nil {
		t.Fatalf("inactiveMessagingRoute(empty) = %#v, %v", degradation, err)
	}
}

func dormantSlackAccount() config.Account {
	return config.Account{
		ID: dormantMessagingAccountID,
		Messages: &config.MessagingRoute{
			Provider: domain.MessagingProviderSlack,
			Slack: &config.SlackMessagingRoute{
				APIBase: "https://slack.com/api", WorkspaceID: "T-SYNTHETIC",
				Authorization: config.CredentialRef{
					Backend: config.CredentialOSKeyring, Key: "unreachable-synthetic", Consent: true,
				},
			},
		},
	}
}
