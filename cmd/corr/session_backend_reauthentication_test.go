package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type authenticationFailureMailPort struct {
	err   error
	calls atomic.Int32
}

func (port *authenticationFailureMailPort) ListMessages(
	context.Context,
	application.MailListInput,
) (application.MailPage, error) {
	port.calls.Add(1)
	return application.MailPage{}, port.err
}

func (port *authenticationFailureMailPort) SearchMessages(
	context.Context,
	application.MailSearchInput,
) (application.MailPage, error) {
	return application.MailPage{}, port.err
}

func (port *authenticationFailureMailPort) ListMailFolders(
	context.Context,
	application.MailFolderListInput,
) (application.MailFolderPage, error) {
	return application.MailFolderPage{}, port.err
}

func (port *authenticationFailureMailPort) GetMessageBody(
	context.Context,
	application.MailBodyInput,
) (application.MailBody, error) {
	return application.MailBody{}, port.err
}

func (port *authenticationFailureMailPort) GetMailAttachment(
	context.Context,
	application.MailAttachmentInput,
) (application.MailAttachment, error) {
	return application.MailAttachment{}, port.err
}

func (port *authenticationFailureMailPort) CreateMailDraft(
	context.Context,
	application.MailDraftInput,
) (application.MailDraft, error) {
	return application.MailDraft{}, port.err
}

func (port *authenticationFailureMailPort) SendMail(
	context.Context,
	application.MailSendInput,
) (application.MailSendResult, error) {
	return application.MailSendResult{}, port.err
}

func (port *authenticationFailureMailPort) MoveMail(
	context.Context,
	application.MailMoveInput,
) (application.MailMoveResult, error) {
	return application.MailMoveResult{}, port.err
}

func (port *authenticationFailureMailPort) SetMailReadState(
	context.Context,
	application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	return application.MailReadStateResult{}, port.err
}

func (port *authenticationFailureMailPort) DeleteMail(
	context.Context,
	application.MailDeleteInput,
) error {
	return port.err
}

func TestSessionBackendInvalidatesOnlyTheRejectedServiceLease(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	providerFailure := application.NewProviderAuthenticationFailure(
		application.AuthenticationReasonCredentialRejected,
		errors.New("synthetic provider response that must remain hidden"),
	)
	port := &authenticationFailureMailPort{err: providerFailure}
	mail, err := application.NewMailService(
		daemonMCPGuard(t, policy.DefaultRules(), &daemonMCPAudit{}),
		port,
		application.MailOptions{
			MaxRecipients: 20,
			Provenance: domain.Provenance{
				AccountID: accountID,
				Provider:  domain.ProviderJMAP,
				MailboxID: "synthetic-mailbox",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mailCloser := &logoutTestCloser{}
	calendarCloser := &logoutTestCloser{}
	mailLease := &sessionLease{
		services: sessionServiceMail,
		closers:  []sessionCloser{mailCloser},
		captured: time.Unix(1, 0).UTC(),
		capabilities: domain.Capabilities{
			Mail: true,
		},
		usage: newAccountUsage(),
	}
	calendarLease := &sessionLease{
		services: sessionServiceCalendar,
		closers:  []sessionCloser{calendarCloser},
		captured: time.Unix(1, 0).UTC(),
		capabilities: domain.Capabilities{
			Calendar: true,
		},
		usage: newAccountUsage(),
	}
	configuration := config.Config{Accounts: map[string]config.Account{
		"work": {
			ID: accountID,
			Mail: &config.MailRoute{
				Provider: domain.ProviderJMAP,
			},
			Calendar: &config.CalendarRoute{
				Provider: domain.ProviderCalDAV,
			},
		},
	}}
	backend := &sessionBackend{
		configuration: configuration,
		accounts: map[domain.AccountID]sessionAccount{
			accountID: {
				mail: mail, calendar: new(application.CalendarService),
				mailLease: mailLease, calendarLease: calendarLease,
				captured: time.Unix(1, 0).UTC(),
				capabilities: domain.Capabilities{
					Mail: true, Calendar: true,
				},
			},
		},
		previews: map[string]sessionPreview{
			"mail-preview": {
				account: accountID, service: application.AuthenticationServiceMail,
				expiresAt: time.Now().Add(time.Minute),
			},
			"calendar-preview": {
				account: accountID, service: application.AuthenticationServiceCalendar,
				expiresAt: time.Now().Add(time.Minute),
			},
		},
		monitorCancel:    make(map[domain.AccountID]context.CancelFunc),
		monitorDone:      make(map[domain.AccountID]chan struct{}),
		monitorStarted:   make(map[domain.AccountID]bool),
		reauthentication: make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
		signedOutReason:  make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
	}

	_, err = backend.ListMail(
		t.Context(),
		application.MailListInput{
			Account: accountID,
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "inbox",
			},
			Limit: 25,
		},
		domain.Caller{Surface: "mcp", Instance: "synthetic-client"},
	)
	action, ok := application.AuthenticationActionFromError(err)
	if !ok || action.Alias != "work" ||
		action.Service != application.AuthenticationServiceMail ||
		action.Provider != domain.ProviderJMAP ||
		action.Reason != application.AuthenticationReasonCredentialRejected {
		t.Fatalf("ListMail() error = %v", err)
	}
	if port.calls.Load() != 1 || mailCloser.calls.Load() != 1 ||
		calendarCloser.calls.Load() != 0 {
		t.Fatalf(
			"calls: port=%d mailClose=%d calendarClose=%d",
			port.calls.Load(),
			mailCloser.calls.Load(),
			calendarCloser.calls.Load(),
		)
	}
	backend.mu.Lock()
	active := backend.accounts[accountID]
	_, mailPreviewExists := backend.previews["mail-preview"]
	_, calendarPreviewExists := backend.previews["calendar-preview"]
	backend.mu.Unlock()
	if active.serviceActive(application.AuthenticationServiceMail) ||
		!active.serviceActive(application.AuthenticationServiceCalendar) ||
		active.capabilities.Mail || !active.capabilities.Calendar ||
		mailPreviewExists || !calendarPreviewExists {
		t.Fatalf("unaffected service state changed: %+v", active)
	}
	status, err := backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	work := status.Accounts[0]
	if !work.Authenticated || work.State != "authenticated" ||
		work.Services.Mail == nil ||
		work.Services.Mail.State != application.AuthenticationStateReauthenticationNeeded ||
		work.Services.Calendar == nil ||
		work.Services.Calendar.State != application.AuthenticationStateAuthenticated {
		t.Fatalf("session status = %+v", work)
	}

	_, err = backend.ListMail(
		t.Context(),
		application.MailListInput{
			Account: accountID,
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "inbox",
			},
			Limit: 25,
		},
		domain.Caller{Surface: "mcp", Instance: "synthetic-client"},
	)
	if retryAction, retryOK := application.AuthenticationActionFromError(err); !retryOK || retryAction.Reason != application.AuthenticationReasonCredentialRejected ||
		port.calls.Load() != 1 {
		t.Fatalf("second ListMail() = %v, calls=%d", err, port.calls.Load())
	}
}

func TestSessionBackendConcurrentInvalidationClosesSharedLeaseOnce(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	closer := &logoutTestCloser{}
	lease := &sessionLease{
		services: sessionServiceMail | sessionServiceCalendar,
		closers:  []sessionCloser{closer},
		usage:    newAccountUsage(),
	}
	account := sessionAccount{
		mail: new(application.MailService), calendar: new(application.CalendarService),
		mailLease: lease, calendarLease: lease,
		capabilities: domain.Capabilities{
			Mail: true, Calendar: true,
		},
	}
	backend := &sessionBackend{
		configuration: config.Config{Accounts: map[string]config.Account{
			"work": {
				ID:       accountID,
				Mail:     &config.MailRoute{Provider: domain.ProviderMicrosoftOWA},
				Calendar: &config.CalendarRoute{Provider: domain.ProviderMicrosoftOWA},
			},
		}},
		accounts:         map[domain.AccountID]sessionAccount{accountID: account},
		previews:         make(map[string]sessionPreview),
		monitorCancel:    make(map[domain.AccountID]context.CancelFunc),
		monitorDone:      make(map[domain.AccountID]chan struct{}),
		monitorStarted:   make(map[domain.AccountID]bool),
		reauthentication: make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
		signedOutReason:  make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
	}
	borrowed := make([]sessionAccount, 2)
	for index, service := range []application.AuthenticationService{
		application.AuthenticationServiceMail,
		application.AuthenticationServiceCalendar,
	} {
		if err := lease.usage.begin(); err != nil {
			t.Fatal(err)
		}
		borrowed[index] = account
		borrowed[index].borrowedLease = lease
		borrowed[index].borrowedService = service
		borrowed[index].usage = lease.usage
	}
	failure := application.NewProviderAuthenticationFailure(
		application.AuthenticationReasonSessionExpired,
		errors.New("synthetic expiry"),
	)
	results := make(chan error, len(borrowed))
	var start sync.WaitGroup
	start.Add(1)
	for _, current := range borrowed {
		go func() {
			start.Wait()
			results <- backend.finishServiceUse(accountID, current, failure)
		}()
	}
	start.Done()
	for range borrowed {
		if _, ok := application.AuthenticationActionFromError(<-results); !ok {
			t.Fatal("concurrent expiry did not return an authentication action")
		}
	}
	if closer.calls.Load() != 1 {
		t.Fatalf("shared closer calls = %d, want 1", closer.calls.Load())
	}
	backend.mu.Lock()
	_, active := backend.accounts[accountID]
	backend.mu.Unlock()
	if active {
		t.Fatal("shared rejected lease remained active")
	}
}

func TestSessionBackendKeepsUnknownWriteOutcomeDistinctFromAuthentication(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	closer := &logoutTestCloser{}
	lease := &sessionLease{
		services: sessionServiceMail,
		closers:  []sessionCloser{closer},
		usage:    newAccountUsage(),
	}
	if err := lease.usage.begin(); err != nil {
		t.Fatal(err)
	}
	account := sessionAccount{
		mail: new(application.MailService), mailLease: lease,
		borrowedLease: lease, borrowedService: application.AuthenticationServiceMail,
	}
	backend := &sessionBackend{accounts: map[domain.AccountID]sessionAccount{
		accountID: account,
	}}
	callErr := errors.Join(
		application.ErrWriteOutcomeUnknown,
		application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonCredentialRejected,
			errors.New("synthetic late response"),
		),
	)
	if err := backend.finishServiceUse(accountID, account, callErr); !errors.Is(
		err,
		application.ErrWriteOutcomeUnknown,
	) {
		t.Fatalf("finishServiceUse() error = %v", err)
	}
	if !backend.accounts[accountID].serviceActive(application.AuthenticationServiceMail) ||
		closer.calls.Load() != 0 {
		t.Fatal("unknown write outcome was mislabeled as authentication expiry")
	}
}

func TestSessionBackendMonitorAuthenticationFailureInvalidatesMail(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	closer := &logoutTestCloser{}
	lease := &sessionLease{
		services: sessionServiceMail,
		closers:  []sessionCloser{closer},
		usage:    newAccountUsage(),
	}
	monitorContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	backend := &sessionBackend{
		configuration: config.Config{Accounts: map[string]config.Account{
			"work": {
				ID:   accountID,
				Mail: &config.MailRoute{Provider: domain.ProviderJMAP},
			},
		}},
		accounts: map[domain.AccountID]sessionAccount{
			accountID: {
				mail: new(application.MailService), mailLease: lease,
				capabilities: domain.Capabilities{Mail: true},
			},
		},
		previews: map[string]sessionPreview{
			"mail-preview": {
				account: accountID, service: application.AuthenticationServiceMail,
				expiresAt: time.Now().Add(time.Minute),
			},
		},
		monitorCancel: map[domain.AccountID]context.CancelFunc{accountID: cancel},
		monitorDone: map[domain.AccountID]chan struct{}{
			accountID: make(chan struct{}),
		},
		monitorStarted:   map[domain.AccountID]bool{accountID: true},
		reauthentication: make(map[domain.AccountID]map[application.AuthenticationService]application.AuthenticationReason),
	}
	backend.invalidateMonitorAuthentication(
		accountID,
		lease,
		application.AuthenticationReasonSessionExpired,
	)
	if monitorContext.Err() == nil || closer.calls.Load() != 1 {
		t.Fatalf(
			"monitor cancellation = %v, closer calls = %d",
			monitorContext.Err(),
			closer.calls.Load(),
		)
	}
	if _, active := backend.accounts[accountID]; active {
		t.Fatal("monitor rejection left the mail lease active")
	}
	if _, exists := backend.previews["mail-preview"]; exists {
		t.Fatal("monitor rejection left a mail preview active")
	}
	if backend.reauthentication[accountID][application.AuthenticationServiceMail] !=
		application.AuthenticationReasonSessionExpired {
		t.Fatalf("reauthentication reason = %+v", backend.reauthentication)
	}
}

var _ application.MailPort = (*authenticationFailureMailPort)(nil)
