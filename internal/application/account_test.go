package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
)

type accountRepositoryStub struct {
	catalog     AccountCatalog
	credentials []AccountCredentialBinding
	added       AccountRegistration
	renamedID   domain.AccountID
	renamed     string
	removedID   domain.AccountID
	replacement domain.AccountID
	err         error
}

func (stub *accountRepositoryStub) ListAccounts(context.Context) (AccountCatalog, error) {
	return stub.catalog, stub.err
}

func (stub *accountRepositoryStub) ListCredentialBindings(
	context.Context,
) ([]AccountCredentialBinding, error) {
	return append([]AccountCredentialBinding(nil), stub.credentials...), stub.err
}

func (stub *accountRepositoryStub) AddAccount(
	_ context.Context,
	account AccountRegistration,
) error {
	stub.added = account
	return stub.err
}

func (stub *accountRepositoryStub) RenameAccount(
	_ context.Context,
	account domain.AccountID,
	alias string,
) error {
	stub.renamedID, stub.renamed = account, alias
	return stub.err
}

func (stub *accountRepositoryStub) RemoveAccount(
	_ context.Context,
	account domain.AccountID,
	replacement domain.AccountID,
) error {
	stub.removedID, stub.replacement = account, replacement
	return stub.err
}

type accountPurgerStub struct {
	account domain.AccountID
	err     error
}

func TestAccountServiceAcceptsEmptyOnboardingCatalog(t *testing.T) {
	t.Parallel()

	repository := &accountRepositoryStub{}
	service, err := NewAccountService(
		repository,
		&accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderGoogleWeb},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := service.List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(catalog.Accounts) != 0 {
		t.Fatalf("List() = %+v, want empty onboarding catalog", catalog)
	}
}

func TestAccountServiceAddsTaskOnlyRouteWithoutAddress(t *testing.T) {
	t.Parallel()
	repository := &accountRepositoryStub{}
	service, err := NewAccountService(
		repository,
		&accountPurgerStub{},
		nil,
		[]domain.ProviderID{domain.ProviderThings},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.newID = func() (domain.AccountID, error) {
		return "acc_00000000000000000000000000000009", nil
	}
	account, err := service.Add(t.Context(), AccountAddInput{
		Alias: "tasks", Tasks: &AccountTaskRouteInput{Provider: domain.ProviderThings},
	})
	if err != nil {
		t.Fatal(err)
	}
	if account.Address != "" || account.Mail != nil || account.Calendar != nil ||
		account.Tasks == nil || account.Tasks.Provider != domain.ProviderThings ||
		!account.Tasks.Available || repository.added.Tasks == nil {
		t.Fatalf("task-only account = %+v registration=%+v", account, repository.added)
	}
}

func TestAccountServiceKeepsPendingMessagingRoutesOutOfConfiguration(t *testing.T) {
	t.Parallel()

	repository := &accountRepositoryStub{}
	service, err := NewAccountService(repository, &accountPurgerStub{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Add(t.Context(), AccountAddInput{
		Alias: "teams", Address: "reader@example.invalid",
		Messages: &AccountMessagingRouteInput{
			Provider: domain.MessagingProviderMicrosoftTeams,
			TeamsWeb: &AccountTeamsWebMessagingInput{
				Web:         AccountWebInput{Origin: "https://teams.microsoft.com"},
				WorkspaceID: "workspace-1",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not available in this build") {
		t.Fatalf("pending messaging route error = %v", err)
	}
	if repository.added.Messages != nil {
		t.Fatalf("pending messaging route reached persistence: %+v", repository.added)
	}
}

func TestAccountServiceReviewsAndAddsIndependentMessagingRoute(t *testing.T) {
	t.Parallel()

	repository := &accountRepositoryStub{}
	service, err := NewAccountService(
		repository, &accountPurgerStub{}, nil, nil,
		domain.MessagingRouteTeamsGraph,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.newID = func() (domain.AccountID, error) {
		return "acc_00000000000000000000000000000145", nil
	}
	input := AccountAddInput{
		Alias: "teams",
		Messages: &AccountMessagingRouteInput{
			Provider: domain.MessagingProviderMicrosoftTeams,
			TeamsGraph: &AccountTeamsGraphMessagingInput{
				OAuth: AccountOAuthInput{
					APIBase: "https://graph.microsoft.com/v1.0", MicrosoftCloud: microsoftcloud.Global,
					ClientID: "synthetic-public-client", RedirectURI: "http://127.0.0.1:0/callback",
					Authorization: AccountCredentialInput{
						Backend: "os-keyring", Key: "teams-graph-grant", Consent: true,
					},
				},
				WorkspaceID: "tenant-synthetic-1", ReadOnly: true,
			},
		},
	}
	review, err := service.ReviewAdd(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if review.MessagingProvider != domain.MessagingProviderMicrosoftTeams || review.Messages == nil ||
		review.Messages.Route != domain.MessagingRouteTeamsGraph || len(review.Credentials) != 1 ||
		review.Credentials[0].MessagingProvider != domain.MessagingProviderMicrosoftTeams {
		t.Fatalf("messaging review = %+v", review)
	}
	account, err := service.Add(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if account.Messages == nil || !account.Messages.Available || account.Messages.WorkspaceID != "tenant-synthetic-1" ||
		repository.added.Messages == nil || repository.added.Mail != nil || repository.added.Calendar != nil ||
		repository.added.Tasks != nil {
		t.Fatalf("messaging account = %+v registration = %+v", account, repository.added)
	}
}

func TestAccountServiceReviewsTickTickGrantAndExternalClientSecretSeparately(t *testing.T) {
	t.Parallel()
	repository := &accountRepositoryStub{}
	service, err := NewAccountService(
		repository,
		&accountPurgerStub{},
		nil,
		[]domain.ProviderID{domain.ProviderTickTick},
	)
	if err != nil {
		t.Fatal(err)
	}
	service.newID = func() (domain.AccountID, error) {
		return "acc_00000000000000000000000000000114", nil
	}
	input := AccountAddInput{
		Alias: "ticktick",
		Tasks: &AccountTaskRouteInput{
			Provider: domain.ProviderTickTick,
			TickTick: &AccountTickTickTaskInput{
				ReadOnly: true,
				OAuth: AccountTickTickOAuthInput{
					APIBase:     "https://api.ticktick.com",
					ClientID:    "synthetic-confidential-client",
					RedirectURI: "http://127.0.0.1:43123/callback",
					Authorization: AccountCredentialInput{
						Backend: "os-keyring", Key: "ticktick-grant", Consent: true,
					},
					ClientSecret: AccountCredentialInput{
						Backend: "helper", Key: "ticktick-client-secret", Consent: true,
					},
				},
			},
		},
	}
	review, err := service.ReviewAdd(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if review.TaskProvider != domain.ProviderTickTick || len(review.Credentials) != 2 ||
		review.Credentials[0].Key != "ticktick-grant" ||
		review.Credentials[1].Key != "ticktick-client-secret" {
		t.Fatalf("TickTick review = %+v", review)
	}
	account, err := service.Add(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if account.Address != "" || account.Tasks == nil ||
		account.Tasks.Provider != domain.ProviderTickTick || !account.Tasks.Available ||
		repository.added.Tasks == nil || repository.added.Tasks.TickTick == nil {
		t.Fatalf("TickTick account = %+v registration=%+v", account, repository.added)
	}
	input.Tasks.TickTick.OAuth.RedirectURI = "http://127.0.0.1:00/callback"
	if _, err := service.ReviewAdd(t.Context(), input); err == nil ||
		!strings.Contains(err.Error(), "fixed loopback port") {
		t.Fatalf("leading-zero ephemeral TickTick port error = %v", err)
	}
}

func TestAccountServiceRejectsTickTickCredentialRoleReuse(t *testing.T) {
	t.Parallel()
	err := validateAccountTickTickCredentialIsolation([]AccountCredentialReview{
		{Provider: domain.ProviderTickTick, Backend: "helper", Key: "shared"},
		{Provider: domain.ProviderJMAP, Backend: "helper", Key: "shared"},
	})
	if err == nil || !strings.Contains(err.Error(), "must not be reused") {
		t.Fatalf("TickTick credential role reuse error = %v", err)
	}
}

func TestAccountServiceDoesNotInferTaskAvailabilityFromSharedProviderID(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{},
		&accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderMicrosoftGraph, domain.ProviderCalDAV},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []domain.ProviderID{domain.ProviderMicrosoftGraph, domain.ProviderCalDAV} {
		_, err := service.ReviewAdd(t.Context(), AccountAddInput{
			Alias: "tasks", Tasks: &AccountTaskRouteInput{Provider: provider},
		})
		if err == nil || !strings.Contains(err.Error(), "task provider") {
			t.Fatalf("ReviewAdd(%q) error = %v", provider, err)
		}
	}
}

func TestAccountServiceReviewsIndependentMicrosoftTodoGrant(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{}, &accountPurgerStub{}, nil,
		[]domain.ProviderID{domain.ProviderMicrosoftGraph},
	)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewAdd(t.Context(), AccountAddInput{
		Alias: "tasks", Address: "reader@example.test",
		Tasks: &AccountTaskRouteInput{
			Provider: domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &AccountMicrosoftTaskInput{
				ReadOnly: true,
				OAuth: AccountOAuthInput{
					APIBase:        "https://graph.microsoft.us/v1.0",
					MicrosoftCloud: microsoftcloud.GCCHigh,
					ClientID:       "synthetic-public-client",
					RedirectURI:    "http://127.0.0.1:43123/callback",
					Authorization: AccountCredentialInput{
						Backend: "os-keyring", Key: "tasks-graph", Consent: true,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Tasks == nil || review.Tasks.Provider != domain.ProviderMicrosoftGraph ||
		len(review.Tasks.Endpoints) != 1 || review.Tasks.Endpoints[0].Value != "https://graph.microsoft.us/v1.0" ||
		len(review.Credentials) != 1 || review.Credentials[0].Service != "tasks" ||
		review.Credentials[0].Key != "tasks-graph" {
		t.Fatalf("Microsoft To Do review = %+v", review)
	}
}

func TestAccountServiceReviewsExplicitCalDAVTaskCredential(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{}, &accountPurgerStub{}, nil,
		[]domain.ProviderID{domain.ProviderCalDAV},
	)
	if err != nil {
		t.Fatal(err)
	}
	review, err := service.ReviewAdd(t.Context(), AccountAddInput{
		Alias: "tasks",
		Tasks: &AccountTaskRouteInput{
			Provider: domain.ProviderCalDAV,
			CalDAV: &AccountCalDAVTaskInput{
				Endpoint: "https://dav.example.invalid/", TaskListPath: "/tasks/work/",
				Username: "reader",
				Credential: AccountCredentialInput{
					Backend: "helper", Key: "caldav-tasks", Consent: true,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Address != "" || review.Tasks == nil ||
		len(review.Tasks.Endpoints) != 2 || review.Tasks.Identity != "reader" ||
		review.Tasks.Credential == nil || review.Tasks.Credential.Backend != "helper" ||
		len(review.Credentials) != 1 || review.Credentials[0].Service != "tasks" ||
		review.Credentials[0].Key != "caldav-tasks" {
		t.Fatalf("CalDAV task review = %+v", review)
	}
}

func TestAccountServiceRejectsOneOAuthHandleForDifferentGraphGrants(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{}, &accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderMicrosoftGraph},
		[]domain.ProviderID{domain.ProviderMicrosoftGraph},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorization := AccountCredentialInput{
		Backend: "os-keyring", Key: "shared-graph", Consent: true,
	}
	global := AccountOAuthInput{
		APIBase:  "https://graph.microsoft.com/v1.0",
		ClientID: "synthetic-public-client", RedirectURI: "http://127.0.0.1:43123/callback",
		Authorization: authorization,
	}
	explicitGlobal := global
	explicitGlobal.MicrosoftCloud = microsoftcloud.Global
	input := AccountAddInput{
		Alias: "shared", Address: "reader@example.test",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftGraph, MicrosoftGraph: &global,
		},
		Tasks: &AccountTaskRouteInput{
			Provider: domain.ProviderMicrosoftGraph,
			MicrosoftGraph: &AccountMicrosoftTaskInput{
				OAuth: explicitGlobal, ReadOnly: true,
			},
		},
	}
	if _, err := service.ReviewAdd(t.Context(), input); err != nil {
		t.Fatalf("legacy and explicit global grant: %v", err)
	}
	missingAddress := input
	missingAddress.Address = ""
	if _, err := service.ReviewAdd(t.Context(), missingAddress); err == nil ||
		!strings.Contains(err.Error(), "account address") {
		t.Fatalf("addressless Microsoft To Do review error = %v", err)
	}

	input.Tasks.MicrosoftGraph.OAuth.APIBase = "https://graph.microsoft.us/v1.0"
	input.Tasks.MicrosoftGraph.OAuth.MicrosoftCloud = microsoftcloud.GCCHigh
	if _, err := service.ReviewAdd(t.Context(), input); err == nil ||
		!strings.Contains(err.Error(), "authorization handle") {
		t.Fatalf("conflicting shared OAuth handle error = %v", err)
	}
}

func (stub *accountPurgerStub) PurgeAccountState(
	_ context.Context,
	account domain.AccountID,
) error {
	stub.account = account
	return stub.err
}

func accountFixture(alias, id string, isDefault bool) AccountView {
	return AccountView{
		ID: domain.AccountID(id), Alias: alias, Address: alias + "@example.invalid",
		Mail: &AccountRouteView{
			Provider: domain.ProviderMicrosoftOWA,
			Endpoints: []DiscoveredEndpoint{
				{Kind: "origin", Value: "https://outlook.example.invalid"},
			},
		},
		Calendar: &AccountRouteView{
			Provider: domain.ProviderMicrosoftOWA,
			Endpoints: []DiscoveredEndpoint{
				{Kind: "origin", Value: "https://outlook.example.invalid"},
			},
		},
		IsDefault: isDefault,
	}
}

func TestAccountUsesOAuthIncludesTodoistTaskRoute(t *testing.T) {
	t.Parallel()
	account := AccountView{Tasks: &AccountRouteView{Provider: domain.ProviderTodoist}}
	if !accountUsesOAuth(account) {
		t.Fatal("Todoist task route did not disclose owned OAuth removal")
	}
}

func TestAccountServiceLifecyclePreservesStableIdentity(t *testing.T) {
	t.Parallel()
	work := accountFixture("work", "acc_00000000000000000000000000000001", true)
	personal := accountFixture("personal", "acc_00000000000000000000000000000002", false)
	repository := &accountRepositoryStub{
		catalog: AccountCatalog{Accounts: []AccountView{work, personal}},
	}
	purger := &accountPurgerStub{}
	service, err := NewAccountService(
		repository,
		purger,
		[]domain.ProviderID{domain.ProviderMicrosoftOWA},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.newID = func() (domain.AccountID, error) {
		return "acc_00000000000000000000000000000003", nil
	}

	added, err := service.Add(context.Background(), AccountAddInput{
		Alias: "team", Address: "reader@EXAMPLE.invalid",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
		Calendar: &AccountCalendarRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if added.ID != "acc_00000000000000000000000000000003" ||
		added.Address != "reader@example.invalid" ||
		repository.added.ID != added.ID ||
		repository.added.Alias != added.Alias {
		t.Fatalf("added = %#v, repository = %#v", added, repository.added)
	}

	renamed, err := service.Rename(context.Background(), AccountRenameInput{
		Account: work.Alias, NewAlias: "office",
	})
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if renamed.ID != work.ID || renamed.Alias != "office" ||
		repository.renamedID != work.ID || repository.renamed != "office" {
		t.Fatalf("renamed = %#v, repository = %#v", renamed, repository)
	}

	removed, err := service.Remove(context.Background(), AccountRemoveInput{
		Account: work.Alias, ReplacementDefault: personal.Alias,
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if removed.ID != work.ID || purger.account != work.ID ||
		repository.removedID != work.ID || repository.replacement != personal.ID {
		t.Fatalf("removed = %#v, repository = %#v, purge = %#v", removed, repository, purger)
	}
}

func TestAccountServiceFailsClosed(t *testing.T) {
	t.Parallel()
	work := accountFixture("work", "acc_00000000000000000000000000000001", true)
	repository := &accountRepositoryStub{
		catalog: AccountCatalog{Accounts: []AccountView{work}},
	}
	purger := &accountPurgerStub{}
	service, err := NewAccountService(
		repository,
		purger,
		[]domain.ProviderID{domain.ProviderMicrosoftOWA},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Add(context.Background(), AccountAddInput{
		Alias: "jmap", Address: "reader@example.invalid",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderJMAP,
			JMAP: &AccountJMAPInput{
				SessionURL: "https://jmap.example.invalid/session",
				Username:   "reader@example.invalid",
				Credential: AccountCredentialInput{
					Backend: "os-keyring", Key: "jmap-test", Consent: true,
				},
			},
		},
	}); err == nil {
		t.Fatal("Add() accepted an unavailable provider")
	}
	if _, err := service.Remove(
		context.Background(),
		AccountRemoveInput{Account: "work"},
	); err == nil {
		t.Fatal("Remove() accepted the only account")
	}

	repository.catalog.Accounts = append(
		repository.catalog.Accounts,
		accountFixture("other", "acc_00000000000000000000000000000002", false),
	)
	purger.err = errors.New("synthetic purge failure")
	if _, err := service.Remove(context.Background(), AccountRemoveInput{
		Account: "work", ReplacementDefault: "other",
	}); err == nil || repository.removedID != "" {
		t.Fatalf("Remove() did not fail before config removal: err=%v repo=%#v", err, repository)
	}
}

func TestAccountServiceBindsGoogleMailUsernameToAccountAddress(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{},
		&accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderGoogle},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ReviewAdd(t.Context(), AccountAddInput{
		Alias: "personal", Address: "reader@gmail.com",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderGoogle,
			Google: &AccountGoogleMailInput{
				Username:    "other@gmail.com",
				ClientID:    "synthetic.apps.googleusercontent.com",
				RedirectURI: "http://127.0.0.1:0",
				Authorization: AccountCredentialInput{
					Backend: "os-keyring", Key: "google-personal", Consent: true,
				},
				ClientSecret: AccountCredentialInput{
					Backend: "os-keyring", Key: "google-client", Consent: true,
				},
			},
		},
	})
	if err == nil {
		t.Fatal("ReviewAdd() accepted a Google username for another account")
	}
}

func TestAccountServiceAcceptsOnlyTypedGoogleTaskRouteWhenAvailable(t *testing.T) {
	t.Parallel()
	service, err := NewAccountService(
		&accountRepositoryStub{},
		&accountPurgerStub{},
		nil,
		[]domain.ProviderID{domain.ProviderGoogleTasks},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := AccountAddInput{
		Alias: "tasks", Address: "reader@example.test",
		Tasks: &AccountTaskRouteInput{
			Provider: domain.ProviderGoogleTasks,
			GoogleTasks: &AccountGoogleTaskInput{
				ReadOnly: true,
				OAuth: AccountGoogleOAuthInput{
					APIBase:     "https://tasks.googleapis.com",
					ClientID:    "synthetic.apps.googleusercontent.com",
					RedirectURI: "http://127.0.0.1:43123/callback",
					Authorization: AccountCredentialInput{
						Backend: "os-keyring", Key: "google-tasks", Consent: true,
					},
					ClientSecret: AccountCredentialInput{
						Backend: "os-keyring", Key: "google-tasks-client", Consent: true,
					},
				},
			},
		},
	}
	review, err := service.ReviewAdd(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if review.TaskProvider != domain.ProviderGoogleTasks || len(review.Credentials) != 2 ||
		review.Credentials[0].Key != "google-tasks" ||
		review.Credentials[1].Key != "google-tasks-client" {
		t.Fatalf("Google Tasks review = %+v", review)
	}
	input.Tasks.GoogleTasks.OAuth.APIBase = "https://www.googleapis.com"
	if _, err := service.ReviewAdd(t.Context(), input); err == nil ||
		!strings.Contains(err.Error(), "tasks.googleapis.com") {
		t.Fatalf("unpinned Google Tasks review error = %v", err)
	}
}

func TestAccountLifecycleReviewsNeverMutateRepositoryOrState(t *testing.T) {
	t.Parallel()
	work := accountFixture("work", "acc_00000000000000000000000000000001", true)
	personal := accountFixture(
		"personal",
		"acc_00000000000000000000000000000002",
		false,
	)
	personal.Mail.Provider = domain.ProviderMicrosoftGraph
	repository := &accountRepositoryStub{
		catalog: AccountCatalog{Accounts: []AccountView{work, personal}},
	}
	purger := &accountPurgerStub{}
	service, err := NewAccountService(
		repository,
		purger,
		[]domain.ProviderID{
			domain.ProviderMicrosoftOWA,
			domain.ProviderMicrosoftGraph,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	addReview, err := service.ReviewAdd(t.Context(), AccountAddInput{
		Alias: "team", Address: "team@example.invalid",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if addReview.Authentication != "explicit_cli_required" {
		t.Fatalf("account add authentication boundary = %+v", addReview)
	}
	if _, err := service.ReviewRename(t.Context(), AccountRenameInput{
		Account: "personal", NewAlias: "home",
	}); err != nil {
		t.Fatal(err)
	}
	removeReview, err := service.ReviewRemove(t.Context(), AccountRemoveInput{
		Account: "personal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !removeReview.MayDeleteOwnedOAuth {
		t.Fatalf("OAuth removal effect was omitted: %+v", removeReview)
	}
	if repository.added.ID != "" || repository.renamedID != "" ||
		repository.removedID != "" || purger.account != "" {
		t.Fatalf(
			"review mutated lifecycle state: repository=%+v purger=%+v",
			repository,
			purger,
		)
	}
}

func TestAccountServiceAddsMixedStandardsRoutesWithoutExposingLookupKeys(t *testing.T) {
	t.Parallel()
	repository := &accountRepositoryStub{
		catalog: AccountCatalog{Accounts: []AccountView{
			accountFixture("work", "acc_00000000000000000000000000000001", true),
		}},
	}
	service, err := NewAccountService(
		repository,
		&accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderIMAPSMTP, domain.ProviderCalDAV},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.newID = func() (domain.AccountID, error) {
		return "acc_00000000000000000000000000000002", nil
	}
	added, err := service.Add(t.Context(), AccountAddInput{
		Alias: "standards", Address: "reader@example.invalid",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderIMAPSMTP,
			IMAPSMTP: &AccountIMAPSMTPInput{
				IMAP: AccountTLSEndpointInput{
					Host: "imap.example.invalid", Port: 993, Mode: "implicit",
				},
				SMTP: AccountTLSEndpointInput{
					Host: "smtp.example.invalid", Port: 587, Mode: "starttls",
				},
				Username: "reader@example.invalid",
				Credential: AccountCredentialInput{
					Backend: "os-keyring", Key: "private-mail-key", Consent: true,
				},
			},
		},
		Calendar: &AccountCalendarRouteInput{
			Provider: domain.ProviderCalDAV,
			CalDAV: &AccountCalDAVInput{
				Endpoint:     "https://dav.example.invalid/",
				CalendarPath: "/calendars/reader/main/",
				Username:     "reader@example.invalid",
				Credential: AccountCredentialInput{
					Backend: "helper", Key: "private-calendar-key", Consent: true,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Mail == nil || added.Mail.Provider != domain.ProviderIMAPSMTP ||
		added.Calendar == nil || added.Calendar.Provider != domain.ProviderCalDAV ||
		added.Mail.Credential.Backend != "os-keyring" ||
		added.Calendar.Credential.Backend != "helper" {
		t.Fatalf("mixed account view = %#v", added)
	}
	if repository.added.Mail.IMAPSMTP.Credential.Key != "private-mail-key" ||
		repository.added.Calendar.CalDAV.Credential.Key != "private-calendar-key" {
		t.Fatalf("persisted registration lost credential references: %#v", repository.added)
	}
}

func TestAccountAddReviewDisclosesAndExclusivelyBindsCredentialHandle(t *testing.T) {
	t.Parallel()
	repository := &accountRepositoryStub{
		catalog: AccountCatalog{Accounts: []AccountView{
			accountFixture("work", "acc_00000000000000000000000000000001", true),
		}},
		credentials: []AccountCredentialBinding{{
			Account: "acc_00000000000000000000000000000001",
			Backend: "os-keyring",
			Key:     "owned-handle",
		}},
	}
	service, err := NewAccountService(
		repository,
		&accountPurgerStub{},
		[]domain.ProviderID{domain.ProviderJMAP},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	input := AccountAddInput{
		Alias: "team", Address: "reader@example.invalid",
		Mail: &AccountMailRouteInput{
			Provider: domain.ProviderJMAP,
			JMAP: &AccountJMAPInput{
				SessionURL: "https://jmap.example.invalid/session",
				Username:   "reader@example.invalid",
				Credential: AccountCredentialInput{
					Backend: "os-keyring",
					Key:     "owned-handle",
					Consent: true,
				},
			},
		},
	}
	if _, err := service.ReviewAdd(t.Context(), input); err == nil {
		t.Fatal("ReviewAdd() accepted another account's credential handle")
	}
	input.Mail.JMAP.Credential.Key = "team-handle"
	review, err := service.ReviewAdd(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Credentials) != 1 ||
		review.Credentials[0].Service != "mail" ||
		review.Credentials[0].Provider != domain.ProviderJMAP ||
		review.Credentials[0].Backend != "os-keyring" ||
		review.Credentials[0].Key != "team-handle" {
		t.Fatalf("credential approval review = %+v", review.Credentials)
	}
}
