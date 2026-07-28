package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

type accountRepositoryStub struct {
	catalog     AccountCatalog
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
