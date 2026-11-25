package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

// errInvalidSignInApproval is returned for a resolution that does not match
// one live reserved approval. It never starts anything.
var errInvalidSignInApproval = errors.New(
	"sign-in approval is invalid, already used, or bound to another caller; nothing was started",
)

// signInStateCaller attributes daemon-internal state events in the audit log.
var signInStateCaller = domain.Caller{Surface: "daemon", Instance: "sign-in-state"}

// signInApproval is one reserved approval prompt for one account and one MCP
// caller. At most one exists per account.
type signInApproval struct {
	ticket    string
	caller    domain.Caller
	expiresAt time.Time
}

// signInCooldown blocks further MCP prompts for one account after the person
// declined or cancelled, after a prompt went unanswered or failed, or after an
// MCP-started sign-in did not finish.
type signInCooldown struct {
	until    time.Time
	declined bool
}

// mcpSignInRun is one daemon-owned sign-in started after MCP approval.
type mcpSignInRun struct {
	alias  string
	endsAt time.Time
	cancel context.CancelFunc
	done   chan struct{}
}

// signInCoordinator holds the daemon-owned state for interactive sign-ins:
// running visible logins from either surface, pending MCP approval prompts,
// persisted per-account cool-downs, and the persisted record of connection
// settings verified by a CLI-started sign-in. It is guarded by
// sessionBackend.mu.
type signInCoordinator struct {
	running   map[domain.AccountID]int
	approvals map[domain.AccountID]signInApproval
	cooldowns map[domain.AccountID]signInCooldown
	verified  map[domain.AccountID]string
	mcpRuns   map[domain.AccountID]*mcpSignInRun
	// stateFailedUntil cools down every account when persisted state could
	// not be read or written.
	stateFailedUntil time.Time
	files            signInStateFiles
	now              func() time.Time
	display          func() error
	audit            application.AuditRecorder
	newTicket        func() (string, error)
}

func newSignInCoordinator() *signInCoordinator {
	return &signInCoordinator{
		running:   make(map[domain.AccountID]int),
		approvals: make(map[domain.AccountID]signInApproval),
		cooldowns: make(map[domain.AccountID]signInCooldown),
		verified:  make(map[domain.AccountID]string),
		mcpRuns:   make(map[domain.AccountID]*mcpSignInRun),
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

// signInsLocked returns the coordinator. A backend built without
// initSignInState gets one without persisted state, which fails closed.
func (backend *sessionBackend) signInsLocked() *signInCoordinator {
	if backend.signIn == nil {
		backend.signIn = newSignInCoordinator()
		backend.loadSignInStateLocked()
	}
	return backend.signIn
}

// initSignInState loads persisted sign-in state from <state>/sign-in.
func (backend *sessionBackend) initSignInState() {
	coordinator := newSignInCoordinator()
	if backend.recorder != nil {
		coordinator.audit = backend.recorder
	}
	if state, err := paths.StateDir(); err == nil {
		coordinator.files = signInStateFiles{dir: filepath.Join(state, "sign-in")}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.signIn = coordinator
	backend.loadSignInStateLocked()
}

// loadSignInStateLocked reads persisted cool-downs and CLI verification
// records. Unreadable cool-down state cools down every account for the full
// period; unreadable verification state verifies nothing. Records for
// accounts no longer configured are removed.
func (backend *sessionBackend) loadSignInStateLocked() {
	coordinator := backend.signIn
	now := coordinator.now()
	loaded, err := coordinator.files.loadCooldowns(now)
	coordinator.cooldowns = loaded.cooldowns
	coordinator.stateFailedUntil = loaded.allUntil
	if err != nil {
		coordinator.stateFailedUntil = now.Add(application.AccountSignInCooldown)
		backend.recordSignInStateEvent("cool_down_state_unreadable")
	}
	// A prompt that was still open when the previous daemon stopped counts as
	// not accepted: its answer can no longer be matched to a ticket.
	for _, account := range loaded.pending {
		coordinator.cooldowns[account] = signInCooldown{until: now.Add(application.AccountSignInCooldown)}
	}
	if err != nil || len(loaded.pending) > 0 {
		backend.persistSignInCooldownsLocked()
	}
	verified, err := coordinator.files.loadVerified()
	if err != nil {
		backend.recordSignInStateEvent("verification_state_unreadable")
		verified = make(map[domain.AccountID]string)
	}
	pruned := false
	for account := range verified {
		if _, _, exists := backend.configuration.AccountByID(account); !exists {
			delete(verified, account)
			pruned = true
		}
	}
	coordinator.verified = verified
	if pruned {
		if err := coordinator.files.saveVerified(verified); err != nil {
			backend.recordSignInStateEvent("verification_state_unwritable")
		}
	}
}

// recordSignInStateEvent audits daemon-internal state failures. They belong
// to no request, so they use the daemon lifecycle rather than a caller context.
//
//nolint:contextcheck,nolintlint // contextcheck reports this on every caller.
func (backend *sessionBackend) recordSignInStateEvent(reason string) {
	recorder := backend.signIn.audit
	if recorder == nil {
		return
	}
	operation, err := domain.NewGlobalOperation("account.sign_in.state", domain.EffectReversibleWrite, struct{}{})
	if err != nil {
		return
	}
	_ = recorder.Record(context.WithoutCancel(backend.lifecycle), application.AuditEvent{
		Phase: application.AuditPhaseExecuted, Outcome: application.AuditOutcomeFailure,
		Reason: reason, Caller: signInStateCaller, Operation: operation.View(),
	})
}

// setSignInCooldownLocked starts the fixed cool-down for one account and
// persists it so a daemon restart cannot clear it.
func (backend *sessionBackend) setSignInCooldownLocked(account domain.AccountID, declined bool) {
	coordinator := backend.signInsLocked()
	coordinator.cooldowns[account] = signInCooldown{
		until: coordinator.now().Add(application.AccountSignInCooldown), declined: declined,
	}
	backend.persistSignInCooldownsLocked()
}

// persistSignInCooldownsLocked writes cool-downs, open prompts, and any
// all-account cool-down. A failed write cools down every account now and,
// through the failure marker, after the next start too.
func (backend *sessionBackend) persistSignInCooldownsLocked() {
	coordinator := backend.signInsLocked()
	now := coordinator.now()
	pending := make(map[domain.AccountID]time.Time, len(coordinator.approvals))
	for account, approval := range coordinator.approvals {
		pending[account] = approval.expiresAt
	}
	if err := coordinator.files.saveCooldowns(coordinator.cooldowns, pending, coordinator.stateFailedUntil, now); err != nil {
		coordinator.stateFailedUntil = now.Add(application.AccountSignInCooldown)
		backend.recordSignInStateEvent("cool_down_state_unwritable")
	}
}

// recordCLISignInLocked marks the account's current connection settings as
// verified by a sign-in that a person started from the CLI.
func (backend *sessionBackend) recordCLISignInLocked(accountID domain.AccountID) {
	_, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return
	}
	digest, err := signInRouteDigest(configured)
	if err != nil {
		return
	}
	coordinator := backend.signInsLocked()
	if coordinator.verified[accountID] == digest {
		return
	}
	coordinator.verified[accountID] = digest
	if err := coordinator.files.saveVerified(coordinator.verified); err != nil {
		backend.recordSignInStateEvent("verification_state_unwritable")
	}
}

// cliVerifiedLocked reports whether the account's current settings match the
// record of its last CLI-completed sign-in.
func (backend *sessionBackend) cliVerifiedLocked(accountID domain.AccountID) bool {
	_, configured, exists := backend.configuration.AccountByID(accountID)
	if !exists {
		return false
	}
	digest, err := signInRouteDigest(configured)
	recorded := backend.signInsLocked().verified[accountID]
	return err == nil && recorded != "" &&
		subtle.ConstantTimeCompare([]byte(recorded), []byte(digest)) == 1
}

// errBehindMCPSignIn explains that a CLI sign-in operation would wait behind
// a running MCP-started sign-in, which holds the daemon's activation lock.
func (backend *sessionBackend) errBehindMCPSignIn(action string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.signIn == nil {
		return nil
	}
	for _, run := range backend.signIn.mcpRuns {
		return fmt.Errorf(
			"%s would wait for the sign-in an MCP client started for account %q, which stays open in its browser until %s; "+
				"finish or close it there, or cancel it with `corr auth logout --account %s`",
			action, run.alias, run.endsAt.Local().Format("15:04:05 MST"), run.alias,
		)
	}
	return nil
}

// SignInVerified answers the CLI's question whether the account's current
// connection settings completed a CLI-started sign-in before. Only the CLI
// surface may ask; MCP learns the same fact only as cli_sign_in_required.
func (backend *sessionBackend) SignInVerified(
	_ context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
) (daemonapi.SignInVerifiedResult, error) {
	if err := caller.Validate(); err != nil {
		return daemonapi.SignInVerifiedResult{}, err
	}
	if caller.Surface != "cli" {
		return daemonapi.SignInVerifiedResult{}, errors.New("sign-in verification is available only to the local CLI")
	}
	if err := accountID.ValidateOpaque(); err != nil {
		return daemonapi.SignInVerifiedResult{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return daemonapi.SignInVerifiedResult{}, errors.New("session backend is closed")
	}
	if _, _, exists := backend.configuration.AccountByID(accountID); !exists {
		return daemonapi.SignInVerifiedResult{}, fmt.Errorf("account %q is not configured", accountID)
	}
	return daemonapi.SignInVerifiedResult{Account: accountID, Verified: backend.cliVerifiedLocked(accountID)}, nil
}

// mcpSignInRunning reports whether any MCP-started sign-in is running.
func (backend *sessionBackend) mcpSignInRunning() bool {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.signIn != nil && len(backend.signIn.mcpRuns) > 0
}

// lockActivationForCLI acquires activationMu for a CLI sign-in or logout. It
// never waits behind an MCP-started sign-in: while one holds or is about to
// take the lock, it returns errBehindMCPSignIn instead. Waiting behind another
// CLI operation keeps its old behaviour.
func (backend *sessionBackend) lockActivationForCLI(ctx context.Context, action string) error {
	for {
		if backend.activationMu.TryLock() {
			return nil
		}
		if err := backend.errBehindMCPSignIn(action); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// cancelMCPSignIn stops a running MCP-started sign-in for one account and
// waits until its browser is closed and its state cleared.
func (backend *sessionBackend) cancelMCPSignIn(ctx context.Context, accountID domain.AccountID) (bool, error) {
	backend.mu.Lock()
	var run *mcpSignInRun
	if backend.signIn != nil {
		run = backend.signIn.mcpRuns[accountID]
	}
	backend.mu.Unlock()
	if run == nil {
		return false, nil
	}
	run.cancel()
	select {
	case <-run.done:
		return true, nil
	case <-ctx.Done():
		return true, ctx.Err()
	}
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
	case now.Before(coordinator.stateFailedUntil):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonStateUnavailable
	case !backend.cliVerifiedLocked(accountID):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonCLISignInRequired
	case !application.AccountSignInAliasDisplayable(alias):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonAliasNotDisplayable
	case configured.Address != "" && !application.AccountSignInAddressDisplayable(configured.Address):
		status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonDestinationNotDisplayable
	}
	var routes []application.AccountSignInRoute
	if status == "" {
		var routeErr error
		routes, routeErr = signInDestinations(configured)
		switch {
		case errors.Is(routeErr, errSignInDestinationUndisplayed):
			status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonDestinationNotDisplayable
		case routeErr != nil:
			status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonRouteUnsupported
		case coordinator.display() != nil:
			status, reason = application.AccountSignInUnavailable, application.AccountSignInReasonDisplayUnavailable
		}
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
		Ticket: ticket, Account: accountID, Alias: alias, Address: configured.Address,
		Routes: routes, ExpiresAt: now.Add(application.AccountSignInApprovalTTL).UTC(),
	}
	if err := approval.Validate(); err != nil {
		backend.mu.Unlock()
		return application.AccountSignInRequest{}, err
	}
	coordinator.approvals[accountID] = signInApproval{
		ticket: ticket, caller: caller, expiresAt: approval.ExpiresAt,
	}
	backend.persistSignInCooldownsLocked()
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
			backend.persistSignInCooldownsLocked()
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
			backend.setSignInCooldownLocked(account, false)
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
		// The prompt may have been shown by a previous daemon (a restart
		// forgets tickets). Its refusal or failure must still cool down.
		var events []signInAuditEvent
		if _, _, configured := backend.configuration.AccountByID(accountID); configured {
			switch resolution.Decision {
			case application.AccountSignInDecisionDeclinedByUser, application.AccountSignInDecisionCancelledByUser:
				backend.setSignInCooldownLocked(accountID, true)
				events = append(events, signInDeniedEvent("stale_prompt_refused"))
			case application.AccountSignInDecisionElicitationFailed:
				backend.setSignInCooldownLocked(accountID, false)
				events = append(events, signInDeniedEvent("stale_prompt_failed"))
			case application.AccountSignInDecisionApprovedByUser, application.AccountSignInDecisionElicitationUnsupported:
			}
		}
		backend.mu.Unlock()
		if len(events) > 0 {
			_ = backend.recordSignInAudit(ctx, recorder, accountID, caller, events...)
		}
		return application.AccountSignInResult{}, errInvalidSignInApproval
	}
	delete(coordinator.approvals, accountID)
	backend.persistSignInCooldownsLocked()
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
		backend.setSignInCooldownLocked(accountID, false)
		backend.mu.Unlock()
		return finish(
			application.AccountSignInUnavailable, application.AccountSignInReasonApprovalExpired, nil,
			committed(application.AuditOutcomeDenied, string(application.AccountSignInReasonApprovalExpired)),
		)
	}
	switch resolution.Decision {
	case application.AccountSignInDecisionDeclinedByUser, application.AccountSignInDecisionCancelledByUser:
		backend.setSignInCooldownLocked(accountID, true)
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
		// A prompt that was shown but not accepted cools the account down;
		// a client that could not show one does not.
		reason := application.AccountSignInReasonElicitationUnsupported
		if resolution.Decision == application.AccountSignInDecisionElicitationFailed {
			reason = application.AccountSignInReasonElicitationFailed
			backend.setSignInCooldownLocked(accountID, false)
		}
		backend.mu.Unlock()
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
	if !backend.cliVerifiedLocked(accountID) {
		backend.mu.Unlock()
		reason := application.AccountSignInReasonCLISignInRequired
		return finish(application.AccountSignInUnavailable, reason, nil, approved, signInDeniedEvent(reason))
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
	runContext, cancel := context.WithTimeout(backend.lifecycle, timeout)
	run := &mcpSignInRun{alias: alias, endsAt: expiresAt, cancel: cancel, done: make(chan struct{})}
	coordinator.mcpRuns[accountID] = run
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
		backend.mu.Lock()
		delete(coordinator.mcpRuns, accountID)
		backend.endInteractiveLoginLocked(accountID)
		backend.mu.Unlock()
		cancel()
		close(run.done)
		backend.active.Done()
		return application.AccountSignInResult{}, err
	}
	// The sign-in belongs to the daemon lifecycle, not this request: the MCP
	// tool returns now and the daemon finishes, expires, or cancels it itself.
	go backend.runApprovedSignIn(runContext, accountID, caller, run, recorder) //nolint:contextcheck
	return result, nil
}

// runApprovedSignIn runs the CLI's visible sign-in for one account under the
// configured login timeout and records its content-free outcome.
func (backend *sessionBackend) runApprovedSignIn(
	ctx context.Context,
	accountID domain.AccountID,
	caller domain.Caller,
	run *mcpSignInRun,
	recorder application.AuditRecorder,
) {
	defer backend.active.Done()
	defer close(run.done)
	defer run.cancel()
	backend.activationMu.Lock()
	_, err := backend.activateAccountHeld(ctx, accountID)
	backend.activationMu.Unlock()

	backend.mu.Lock()
	backend.endInteractiveLoginLocked(accountID)
	coordinator := backend.signInsLocked()
	if coordinator.mcpRuns[accountID] == run {
		delete(coordinator.mcpRuns, accountID)
	}
	if err != nil {
		backend.setSignInCooldownLocked(accountID, false)
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
	case errors.Is(err, context.Canceled):
		event.outcome, event.reason = application.AuditOutcomeFailure, "sign_in_cancelled"
	default:
		event.outcome, event.reason = application.AuditOutcomeFailure, "sign_in_failed"
	}
	_ = backend.recordSignInAudit(ctx, recorder, accountID, caller, event)
}
