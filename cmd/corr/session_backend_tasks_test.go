package main

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestTaskRouteActivationFailsBeforeAuthenticationWithoutAdapter(t *testing.T) {
	t.Parallel()
	accountID := domain.AccountID("acc_00000000000000000000000000000009")
	configuration := config.Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = config.Account{
		ID: accountID, Tasks: &config.TaskRoute{Provider: domain.ProviderTodoist},
	}
	backend := &sessionBackend{
		configuration:    configuration,
		accounts:         make(map[domain.AccountID]sessionAccount),
		terminalAccounts: make(map[domain.AccountID]string),
	}
	_, err := backend.activateAccount(t.Context(), accountID)
	if err == nil || !strings.Contains(err.Error(), "task provider \"todoist\" is not available") {
		t.Fatalf("activateAccount() error = %v", err)
	}
	if len(backend.accounts) != 0 {
		t.Fatal("unavailable task route was retained as an authenticated account")
	}
}

func TestInactiveTaskRouteDoesNotDisableAnotherService(t *testing.T) {
	t.Parallel()
	configured := config.Account{
		ID: "acc_00000000000000000000000000000009",
		Mail: &config.MailRoute{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &config.OutlookWebRoute{
				Origin: "https://outlook.example.invalid",
			},
		},
		Tasks: &config.TaskRoute{Provider: domain.ProviderTodoist},
	}
	degradation, err := inactiveTaskRoute(configured)
	if err != nil || degradation == nil || degradation.Feature != "tasks.route" ||
		degradation.Lossy || !strings.Contains(degradation.Reason, "todoist") {
		t.Fatalf("inactiveTaskRoute() = %+v, %v", degradation, err)
	}
	if degradation, err := inactiveTaskRoute(config.Account{}); err != nil || degradation != nil {
		t.Fatalf("account without tasks = %+v, %v", degradation, err)
	}
}
