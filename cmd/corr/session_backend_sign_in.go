package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/domain"
)

// errInvalidSignInApproval is returned for a resolution that does not match
// one live reserved approval. It never starts anything.
var errInvalidSignInApproval = errors.New(
	"sign-in approval is invalid, already used, or bound to another caller; nothing was started",
)

// signInApproval is one reserved approval prompt for one account and one MCP
// caller. At most one exists per account.
type signInApproval struct {
	ticket    string
	caller    domain.Caller
	expiresAt time.Time
}

// signInCooldown blocks further MCP prompts for one account after the person
// declined or cancelled, or after an MCP-started sign-in did not finish.
type signInCooldown struct {
	until    time.Time
	declined bool
}

// signInCoordinator holds the daemon-owned state for interactive sign-ins:
// running visible logins from either surface, pending MCP approval prompts,
// and per-account cool-downs. It is guarded by sessionBackend.mu.
type signInCoordinator struct {
	running   map[domain.AccountID]int
	approvals map[domain.AccountID]signInApproval
	cooldowns map[domain.AccountID]signInCooldown
	now       func() time.Time
	display   func() error
	audit     application.AuditRecorder
	newTicket func() (string, error)
}

func newSignInCoordinator() *signInCoordinator {
	return &signInCoordinator{
		running:   make(map[domain.AccountID]int),
		approvals: make(map[domain.AccountID]signInApproval),
		cooldowns: make(map[domain.AccountID]signInCooldown),
		now:       time.Now,
		display:   browser.RequireGraphicalSession,
		newTicket: newSignInTicket,
	}
}

func newSignInTicket() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate sign-in ticket: %w", err)
	}
	return "sit1_" + base64.RawURLEncoding.EncodeToString(value), nil
}

// signInsLocked returns the coordinator, creating it on first use so test
// backends built as literals keep working.
func (backend *sessionBackend) signInsLocked() *signInCoordinator {
	if backend.signIn == nil {
		backend.signIn = newSignInCoordinator()
		if backend.recorder != nil {
			backend.signIn.audit = backend.recorder
		}
	}
	return backend.signIn
}

// beginInteractiveLoginLocked marks one visible sign-in as running so status
// reports it as pending and MCP requests answer in_progress.
func (backend *sessionBackend) beginInteractiveLoginLocked(account domain.AccountID) {
	backend.signInsLocked().running[account]++
}

func (backend *sessionBackend) endInteractiveLogin(account domain.AccountID) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.endInteractiveLoginLocked(account)
}

func (backend *sessionBackend) endInteractiveLoginLocked(account domain.AccountID) {
	coordinator := backend.signInsLocked()
	if coordinator.running[account] <= 1 {
		delete(coordinator.running, account)
		return
	}
	coordinator.running[account]--
}

// interactiveLoginPendingLocked reports any unfinished human sign-in for one
// account: a terminal or forwarded graphical interaction, or a visible login
// started by the CLI or by an approved MCP request.
func (backend *sessionBackend) interactiveLoginPendingLocked(account domain.AccountID) bool {
	if _, pending := backend.terminalAccounts[account]; pending {
		return true
	}
	return backend.signIn != nil && backend.signIn.running[account] > 0
}

type signInAuditEvent struct {
	phase   application.AuditPhase
	outcome application.AuditOutcome
	reason  string
}

func (backend *sessionBackend) recordSignInAudit(
	ctx context.Context,
	recorder application.AuditRecorder,
	account domain.AccountID,
	caller domain.Caller,
	events ...signInAuditEvent,
) error {
	if recorder == nil {
		return errors.New("sign-in audit recorder is unavailable")
	}
	operation, err := domain.NewOperation(
		"account.sign_in", domain.EffectReversibleWrite, account, struct{}{},
	)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := recorder.Record(context.WithoutCancel(ctx), application.AuditEvent{
			Phase: event.phase, Outcome: event.outcome, Reason: event.reason,
			Caller: caller, Operation: operation.View(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func requireMCPSignInCaller(caller domain.Caller) error {
	if err := caller.Validate(); err != nil {
		return err
	}
	if caller.Surface != "mcp" {
		return errors.New(
			"user-approved sign-in requests are accepted only from the MCP surface; use corr auth login",
		)
	}
	return nil
}

func signInDeniedEvent(reason application.AccountSignInReason) signInAuditEvent {
	return signInAuditEvent{
		phase: application.AuditPhasePrepared, outcome: application.AuditOutcomeDenied,
		reason: string(reason),
	}
}

// RequestAccountSignIn reserves one approval prompt for an MCP caller, or
// returns the final answer when no prompt may be shown. It never launches a
// browser or resolves a credential.
func (backend *sessionBackend) RequestAccountSignIn(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
) (application.AccountSignInRequest, error) {
	if err := requireMCPSignInCaller(caller); err != nil {
		return application.AccountSignInRequest{}, err
	}
	if err := accountID.ValidateOpaque(); err != nil {
		return application.AccountSignInRequest{}, err
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, errors.New("session backend is closed")
	}
	coordinator := backend.signInsLocked()
	recorder := coordinator.audit
	alias, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, fmt.Errorf("account %q is not configured", accountID)
	}
	expired := backend.expireSignInApprovalsLocked()
	now := coordinator.now()
	configured = configured.ActiveServices()

	var status application.AccountSignInStatus
	var reason application.AccountSignInReason
	account, active := backend.accounts[accountID]
	_, approvalPending := coordinator.approvals[accountID]
	cooldown, cooling := coordinator.cooldowns[accountID]
	switch {
	case active && sessionAccountComplete(configured, account):
		status = application.AccountSignInAlreadySignedIn
	case backend.interactiveLoginPendingLocked(accountID):
		status, reason = application.AccountSignInInProgress, application.AccountSignInReasonSignInRunning
	case approvalPending:
		status, reason = application.AccountSignInInProgress, application.AccountSignInReasonApprovalPending
	case cooling && now.Before(cooldown.until) && cooldown.declined:
		status, reason = application.AccountSignInDeclined, application.AccountSignInReasonRecentlyDeclined
	case cooling && now.Before(cooldown.until):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonRecentlyUnfinished
	case len(configuredAuthenticationServices(configured)) == 0 || hasGoogleWebRoute(configured):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonRouteUnsupported
	case !application.AccountSignInAliasDisplayable(alias):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonAliasNotDisplayable
	case coordinator.display() != nil:
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonDisplayUnavailable
	}
	if status != "" {
		backend.mu.Unlock()
		if err := backend.recordExpiredSignInApprovals(ctx, recorder, expired); err != nil {
			return application.AccountSignInRequest{}, err
		}
		events := append([]signInAuditEvent(nil), signInAuditEvent{
			phase: application.AuditPhasePrepared, outcome: application.AuditOutcomeAllowed,
			reason: "sign_in_requested",
		})
		if status == application.AccountSignInAlreadySignedIn {
			events = append(events, signInDeniedEvent("already_signed_in"))
		} else {
			events = append(events, signInDeniedEvent(reason))
		}
		if err := backend.recordSignInAudit(ctx, recorder, accountID, caller, events...); err != nil {
			return application.AccountSignInRequest{}, err
		}
		result, err := application.NewAccountSignInResult(status, reason, accountID, alias, nil)
		if err != nil {
			return application.AccountSignInRequest{}, err
		}
		return application.AccountSignInRequest{Result: &result}, nil
	}

	ticket, err := coordinator.newTicket()
	if err != nil {
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, err
	}
	approval := application.AccountSignInApproval{
		Ticket: ticket, Account: accountID, Alias: alias,
		ExpiresAt: now.Add(application.AccountSignInApprovalTTL).UTC(),
	}
	for _, service := range configuredAuthenticationServices(configured) {
		approval.Routes = append(approval.Routes, application.AccountSignInRoute{
			Service: service, Provider: configuredServiceProvider(configured, service),
		})
	}
	if err := approval.Validate(); err != nil {
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, err
	}
	coordinator.approvals[accountID] = signInApproval{
		ticket: ticket, caller: caller, expiresAt: approval.ExpiresAt,
	}
	backend.mu.Unlock()

	events := append(
		[]signInAuditEvent(nil),
		signInAuditEvent{
			phase: application.AuditPhasePrepared, outcome: application.AuditOutcomeAllowed,
			reason: "sign_in_requested",
		},
		signInAuditEvent{
			phase: application.AuditPhasePrepared, outcome: application.AuditOutcomePreview,
			reason: "approval_requested",
		},
	)
	err = errors.Join(
		backend.recordExpiredSignInApprovals(ctx, recorder, expired),
		backend.recordSignInAudit(ctx, recorder, accountID, caller, events...),
	)
	if err != nil {
		backend.mu.Lock()
		if current, exists := coordinator.approvals[accountID]; exists && current.ticket == ticket {
			delete(coordinator.approvals, accountID)
		}
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, err
	}
	return application.AccountSignInRequest{Approval: &approval}, nil
}

type expiredSignInApproval struct {
	account domain.AccountID
	caller  domain.Caller
}

// expireSignInApprovalsLocked drops unanswered approval prompts whose time
// has passed. The caller records them after releasing the lock.
func (backend *sessionBackend) expireSignInApprovalsLocked() []expiredSignInApproval {
	coordinator := backend.signInsLocked()
	now := coordinator.now()
	var expired []expiredSignInApproval
	for account, approval := range coordinator.approvals {
		if !now.Before(approval.expiresAt) {
			delete(coordinator.approvals, account)
			expired = append(expired, expiredSignInApproval{account: account, caller: approval.caller})
		}
	}
	return expired
}

func (backend *sessionBackend) recordExpiredSignInApprovals(
	ctx context.Context,
	recorder application.AuditRecorder,
	expired []expiredSignInApproval,
) error {
	for _, approval := range expired {
		if err := backend.recordSignInAudit(ctx, recorder, approval.account, approval.caller, signInAuditEvent{
			phase: application.AuditPhaseCommitted, outcome: application.AuditOutcomeDenied,
			reason: string(application.AccountSignInReasonApprovalExpired),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ResolveAccountSignIn consumes one reserved approval prompt. Only the
// approved-by-elicitation decision starts the CLI's visible sign-in, and it
// returns as soon as that sign-in has been started.
func (backend *sessionBackend) ResolveAccountSignIn(
	ctx context.Context,
	resolution application.AccountSignInResolution,
	caller domain.Caller,
) (application.AccountSignInResult, error) {
	if err := requireMCPSignInCaller(caller); err != nil {
		return application.AccountSignInResult{}, err
	}
	if err := resolution.Validate(); err != nil {
		return application.AccountSignInResult{}, err
	}
	accountID := resolution.Account
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return application.AccountSignInResult{}, errors.New("session backend is closed")
	}
	coordinator := backend.signInsLocked()
	recorder := coordinator.audit
	approval, exists := coordinator.approvals[accountID]
	if !exists || approval.caller != caller ||
		subtle.ConstantTimeCompare([]byte(approval.ticket), []byte(resolution.Ticket)) != 1 {
		backend.mu.Unlock()
		return application.AccountSignInResult{}, errInvalidSignInApproval
	}
	delete(coordinator.approvals, accountID)
	alias, configured, configuredExists := backend.configuration.AccountByID(accountID)
	if !configuredExists {
		backend.mu.Unlock()
		return application.AccountSignInResult{}, fmt.Errorf("account %q is not configured", accountID)
	}
	configured = configured.ActiveServices()
	now := coordinator.now()

	finish := func(
		status application.AccountSignInStatus,
		reason application.AccountSignInReason,
		expiresAt *time.Time,
		events ...signInAuditEvent,
	) (application.AccountSignInResult, error) {
		if err := backend.recordSignInAudit(ctx, recorder, accountID, caller, events...); err != nil {
			return application.AccountSignInResult{}, err
		}
		return application.NewAccountSignInResult(status, reason, accountID, alias, expiresAt)
	}
	committed := func(outcome application.AuditOutcome, reason string) signInAuditEvent {
		return signInAuditEvent{phase: application.AuditPhaseCommitted, outcome: outcome, reason: reason}
	}

	if !now.Before(approval.expiresAt) {
		backend.mu.Unlock()
		return finish(
			application.AccountSignInUnavailable, application.AccountSignInReasonApprovalExpired, nil,
			committed(application.AuditOutcomeDenied, string(application.AccountSignInReasonApprovalExpired)),
		)
	}
	switch resolution.Decision {
	case application.AccountSignInDecisionDeclinedByUser, application.AccountSignInDecisionCancelledByUser:
		coordinator.cooldowns[accountID] = signInCooldown{
			until: now.Add(application.AccountSignInCooldown), declined: true,
		}
		backend.mu.Unlock()
		reason := application.AccountSignInReasonUserDeclined
		if resolution.Decision == application.AccountSignInDecisionCancelledByUser {
			reason = application.AccountSignInReasonUserCancelled
		}
		return finish(
			application.AccountSignInDeclined, reason, nil,
			committed(application.AuditOutcomeDenied, string(reason)),
		)
	case application.AccountSignInDecisionElicitationFailed, application.AccountSignInDecisionElicitationUnsupported:
		backend.mu.Unlock()
		reason := application.AccountSignInReasonElicitationFailed
		if resolution.Decision == application.AccountSignInDecisionElicitationUnsupported {
			reason = application.AccountSignInReasonElicitationUnsupported
		}
		return finish(
			application.AccountSignInUnavailable, reason, nil,
			committed(application.AuditOutcomeDenied, string(reason)),
		)
	case application.AccountSignInDecisionApprovedByUser:
	default:
		backend.mu.Unlock()
		return application.AccountSignInResult{}, resolution.Decision.Validate()
	}

	approved := committed(application.AuditOutcomeAllowed, "user_approved")
	if account, active := backend.accounts[accountID]; active && sessionAccountComplete(configured, account) {
		backend.mu.Unlock()
		return finish(application.AccountSignInAlreadySignedIn, "", nil, approved, signInDeniedEvent("already_signed_in"))
	}
	if backend.interactiveLoginPendingLocked(accountID) {
		backend.mu.Unlock()
		reason := application.AccountSignInReasonSignInRunning
		return finish(application.AccountSignInInProgress, reason, nil, approved, signInDeniedEvent(reason))
	}
	if coordinator.display() != nil {
		backend.mu.Unlock()
		reason := application.AccountSignInReasonDisplayUnavailable
		return finish(application.AccountSignInUnavailable, reason, nil, approved, signInDeniedEvent(reason))
	}
	timeout := time.Duration(backend.configuration.Browser.LoginTimeout)
	if timeout <= 0 {
		backend.mu.Unlock()
		return application.AccountSignInResult{}, errors.New("browser login timeout is not configured")
	}
	expiresAt := now.Add(timeout).UTC()
	backend.beginInteractiveLoginLocked(accountID)
	backend.active.Add(1)
	backend.mu.Unlock()

	result, err := finish(
		application.AccountSignInStarted, "", &expiresAt, approved,
		signInAuditEvent{
			phase: application.AuditPhaseExecuted, outcome: application.AuditOutcomeAllowed,
			reason: "sign_in_started",
		},
	)
	if err != nil {
		backend.endInteractiveLogin(accountID)
		backend.active.Done()
		return application.AccountSignInResult{}, err
	}
	// The sign-in belongs to the daemon lifecycle, not this request: the MCP
	// tool returns now and the daemon finishes or expires the session itself.
	go backend.runApprovedSignIn(accountID, caller, timeout, recorder) //nolint:contextcheck
	return result, nil
}

// runApprovedSignIn runs the CLI's visible sign-in for one account under the
// configured login timeout and records its content-free outcome.
func (backend *sessionBackend) runApprovedSignIn(
	accountID domain.AccountID,
	caller domain.Caller,
	timeout time.Duration,
	recorder application.AuditRecorder,
) {
	defer backend.active.Done()
	ctx, cancel := context.WithTimeout(backend.lifecycle, timeout)
	defer cancel()
	_, err := backend.activateAccount(ctx, accountID)

	backend.mu.Lock()
	backend.endInteractiveLoginLocked(accountID)
	coordinator := backend.signInsLocked()
	if err != nil {
		coordinator.cooldowns[accountID] = signInCooldown{
			until: coordinator.now().Add(application.AccountSignInCooldown),
		}
	} else {
		delete(coordinator.cooldowns, accountID)
	}
	backend.mu.Unlock()

	event := signInAuditEvent{
		phase: application.AuditPhaseExecuted, outcome: application.AuditOutcomeSuccess,
		reason: "sign_in_completed",
	}
	switch {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		event.outcome, event.reason = application.AuditOutcomeFailure, "sign_in_expired"
	default:
		event.outcome, event.reason = application.AuditOutcomeFailure, "sign_in_failed"
	}
	_ = backend.recordSignInAudit(backend.lifecycle, recorder, accountID, caller, event)
}
