package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
	"github.com/nkiyohara/corresync/internal/provider/graphapi"
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

func TestMicrosoftTodoTaskOnlyRouteActivatesWithIndependentScope(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/me":
			_, _ = writer.Write([]byte(`{"id":"user1","mail":"reader@example.test"}`))
		case "/me/todo/lists":
			if request.URL.Query().Get("$top") == "1" {
				_, _ = writer.Write([]byte(`{"value":[{"id":"list1"}]}`))
				return
			}
			_, _ = writer.Write([]byte(`{"value":[{"id":"list1","displayName":"Tasks","isOwner":true,"wellknownListName":"defaultList"}]}`))
		default:
			http.Error(writer, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()
	const accountID domain.AccountID = "acc_00000000000000000000000000000019"
	route := config.OAuthRoute{
		APIBase: server.URL, ClientID: "synthetic-task-client",
		RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "task-grant", Consent: true,
		},
	}
	configuration := config.Default()
	configuration.DefaultAccount = "tasks"
	configuration.Accounts["tasks"] = config.Account{
		ID: accountID, Address: "reader@example.test",
		Tasks: &config.TaskRoute{
			Provider:       domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &config.MicrosoftGraphTaskRoute{OAuth: route},
		},
	}
	manager := &oauthManagerStub{client: server.Client()}
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &sessionBackend{
		configuration: configuration,
		guard:         daemonMCPGuard(t, policy.DefaultRules(), &daemonMCPAudit{}),
		oauth:         manager, newGraph: graphapi.New,
		accounts: make(map[domain.AccountID]sessionAccount),
		previews: make(map[string]sessionPreview), lifecycle: lifecycle, cancel: cancel,
		monitorStarted: make(map[domain.AccountID]bool),
		monitorCancel:  make(map[domain.AccountID]context.CancelFunc),
		monitorDone:    make(map[domain.AccountID]chan struct{}),
	}
	caller := domain.Caller{Surface: "cli", Instance: "task-only-test"}
	if _, err := backend.Login(t.Context(), accountID, caller); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 || !slices.Contains(manager.provider.Scopes, "Tasks.ReadWrite") ||
		slices.Contains(manager.provider.Scopes, "Mail.ReadWrite") ||
		slices.Contains(manager.provider.Scopes, "Calendars.ReadWrite") {
		t.Fatalf("task OAuth profile = %+v calls=%d", manager.provider, manager.calls)
	}
	page, err := backend.ListTaskLists(t.Context(), application.TaskListInput{
		Account: accountID, Limit: 10,
	}, caller)
	if err != nil || len(page.Lists) != 1 || !page.Lists[0].Default {
		t.Fatalf("ListTaskLists() = %+v, %v", page, err)
	}
	account := backend.accounts[accountID]
	if account.tasks == nil || !account.capabilities.Tasks || !account.capabilities.IncrementalSync {
		t.Fatalf("task-only session = %+v", account.capabilities)
	}
}

func TestConfiguredGraphServicesMergeOnlyTheSameCanonicalGrant(t *testing.T) {
	t.Parallel()
	legacyGlobal := config.OAuthRoute{
		APIBase:  "https://graph.microsoft.com/v1.0",
		ClientID: "synthetic-client", RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: "shared", Consent: true,
		},
	}
	explicitGlobal := legacyGlobal
	explicitGlobal.MicrosoftCloud = "global"
	account := config.Account{
		Mail: &config.MailRoute{
			Provider: domain.ProviderMicrosoftGraph, MicrosoftGraph: &legacyGlobal,
		},
		Tasks: &config.TaskRoute{
			Provider: domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &config.MicrosoftGraphTaskRoute{
				OAuth: explicitGlobal, ReadOnly: true,
			},
		},
	}
	services, err := configuredGraphServices(account)
	if err != nil || len(services) != 1 || !services[0].mail || !services[0].tasks ||
		services[0].taskWrite {
		t.Fatalf("canonical Global Graph services = %+v, %v", services, err)
	}

	distinct := explicitGlobal
	distinct.Authorization.Key = "tasks"
	account.Tasks.MicrosoftGraph.OAuth = distinct
	services, err = configuredGraphServices(account)
	if err != nil || len(services) != 2 {
		t.Fatalf("independent Graph services = %+v, %v", services, err)
	}
}
