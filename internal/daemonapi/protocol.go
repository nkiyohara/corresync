// Package daemonapi defines the private, versioned API between owa-bridge
// adapters and the local session-owning daemon.
package daemonapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nkiyohara/owa-bridge/internal/application"
	"github.com/nkiyohara/owa-bridge/internal/domain"
)

const (
	ProtocolVersion   = 11
	maxRequestBytes   = 8 << 20
	maxResponseBytes  = 16 << 20
	contentType       = "application/json"
	requestPath       = "/v1/call"
	requestHost       = "owa.local"
	authorizationType = "Bearer "
)

// Method is a closed daemon operation name. There is no arbitrary OWA action.
type Method string

const (
	MethodStatus               Method = "status"
	MethodShutdown             Method = "shutdown"
	MethodLogin                Method = "login"
	MethodSessionStatus        Method = "session.status"
	MethodTerminalLogin        Method = "login.terminal"
	MethodMailFolders          Method = "mail.folders.list"
	MethodMailList             Method = "mail.list"
	MethodMailSearch           Method = "mail.search"
	MethodMailGetBody          Method = "mail.get_body"
	MethodMailCommitBody       Method = "mail.commit_body"
	MethodMailGetAttachment    Method = "mail.get_attachment"
	MethodMailCommitAttachment Method = "mail.commit_attachment"
	MethodMailCreateDraft      Method = "mail.create_draft"
	MethodMailCommitDraft      Method = "mail.commit_draft"
	MethodMailSend             Method = "mail.send"
	MethodMailCommitSend       Method = "mail.commit_send"
	MethodMailMove             Method = "mail.move"
	MethodMailCommitMove       Method = "mail.commit_move"
	MethodMailReadState        Method = "mail.set_read_state"
	MethodMailCommitState      Method = "mail.commit_read_state"
	MethodMailDelete           Method = "mail.delete"
	MethodMailCommitDelete     Method = "mail.commit_delete"
	MethodCalendarList         Method = "calendar.list"
	MethodCalendarCreate       Method = "calendar.create"
	MethodCalendarCommit       Method = "calendar.commit_create"
	MethodCalendarUpdate       Method = "calendar.update"
	MethodCalendarCommitUpdate Method = "calendar.commit_update"
	MethodCalendarCancel       Method = "calendar.cancel"
	MethodCalendarCommitCancel Method = "calendar.commit_cancel"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)
var terminalSessionIDPattern = regexp.MustCompile(`^tls1_[A-Za-z0-9_-]{32,64}$`)
var terminalControlIDPattern = regexp.MustCompile(`^control-[1-9][0-9]{0,2}$`)

type requestEnvelope struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Method  Method          `json:"method"`
	Caller  domain.Caller   `json:"caller"`
	Params  json.RawMessage `json:"params"`
}

type responseEnvelope struct {
	Version int             `json:"version"`
	ID      string          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a stable remote failure without response bodies or credentials.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return failure.Message
}

// ProtocolVersionError identifies an authenticated daemon response that used a
// different private protocol version. RequestRejected is true only when the
// daemon proved that it rejected the request before dispatch.
type ProtocolVersionError struct {
	ClientVersion int
	DaemonVersion int
	rejected      bool
}

func (failure *ProtocolVersionError) Error() string {
	if failure == nil {
		return ""
	}
	return fmt.Sprintf(
		"daemon protocol %d is incompatible with client protocol %d",
		failure.DaemonVersion,
		failure.ClientVersion,
	)
}

// RequestRejected reports whether retrying a read-only status call or the
// control-only shutdown call at the daemon's exact version is unambiguous.
func (failure *ProtocolVersionError) RequestRejected() bool {
	return failure != nil && failure.rejected
}

// Status describes a daemon without revealing its config or state paths.
type Status struct {
	ProtocolVersion int              `json:"protocolVersion"`
	Version         string           `json:"version"`
	ProcessID       int              `json:"processId"`
	StartedAt       time.Time        `json:"startedAt"`
	DefaultAccount  domain.AccountID `json:"defaultAccount"`
	ConfigDigest    string           `json:"configDigest"`
}

// OwnerSnapshot binds validated status metadata to one authenticated daemon
// generation. Its credential and protocol controls are deliberately private.
type OwnerSnapshot struct {
	status          Status
	protocolVersion int
	credential      string
}

// Status returns a copy of the content-free owner metadata.
func (snapshot OwnerSnapshot) Status() Status { return snapshot.status }

// String keeps the generation credential out of ordinary formatted output.
func (snapshot OwnerSnapshot) String() string {
	return fmt.Sprintf(
		"daemon owner version %s PID %d protocol %d",
		snapshot.status.Version,
		snapshot.status.ProcessID,
		snapshot.status.ProtocolVersion,
	)
}

// GoString keeps the generation credential out of Go-syntax debug output.
func (snapshot OwnerSnapshot) GoString() string { return snapshot.String() }

// MarshalJSON rejects serialization of the generation-bound control handle.
func (OwnerSnapshot) MarshalJSON() ([]byte, error) {
	return nil, errors.New("daemon owner snapshot cannot be serialized")
}

// ApprovalInput commits an exact in-memory operation preview.
type ApprovalInput struct {
	Token string `json:"token"`
}

// LoginInput selects one configured account for interactive authentication.
type LoginInput struct {
	Account domain.AccountID `json:"account"`
}

// LoginResult contains freshness metadata but no authorization material.
type LoginResult struct {
	Account       domain.AccountID `json:"account"`
	Authenticated bool             `json:"authenticated"`
	CapturedAt    time.Time        `json:"capturedAt"`
}

// SessionStatus contains only local aliases and in-memory authentication
// freshness. It never exposes account identities or authorization material.
type SessionStatus struct {
	Account       domain.AccountID `json:"account"`
	State         string           `json:"state"`
	Authenticated bool             `json:"authenticated"`
	CapturedAt    *time.Time       `json:"capturedAt,omitempty"`
}

// SessionStatusResult reports every configured account in stable alias order.
type SessionStatusResult struct {
	Accounts []SessionStatus `json:"accounts"`
}

// TerminalLoginInput starts or advances one caller-bound text-only browser
// interaction. A start request has only Account; subsequent requests include a
// SessionID and one bounded action.
type TerminalLoginInput struct {
	Account   domain.AccountID     `json:"account"`
	SessionID string               `json:"sessionId,omitempty"`
	Action    *TerminalLoginAction `json:"action,omitempty"`
}

// TerminalLoginAction is one browser control activation, focus request, key,
// or page refresh. Key actions never carry more than one character.
type TerminalLoginAction struct {
	Type      string `json:"type"`
	ControlID string `json:"controlId,omitempty"`
	Key       string `json:"key,omitempty"`
}

// TerminalLoginControl is one visible browser control in the bounded terminal
// view. Form values and selectors are never returned.
type TerminalLoginControl struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

// TerminalLoginView is an accessibility-oriented page projection. Origin is
// intentionally path-free so identity-provider query values cannot escape.
type TerminalLoginView struct {
	Origin   string                 `json:"origin,omitempty"`
	Title    string                 `json:"title,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Controls []TerminalLoginControl `json:"controls"`
}

// TerminalLoginResult reports either the next text interaction or completed
// authentication without exposing authorization material.
type TerminalLoginResult struct {
	Account    domain.AccountID   `json:"account"`
	SessionID  string             `json:"sessionId,omitempty"`
	Status     string             `json:"status"`
	CapturedAt time.Time          `json:"capturedAt,omitempty"`
	View       *TerminalLoginView `json:"view,omitempty"`
}

func (input TerminalLoginInput) validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if input.SessionID == "" {
		if input.Action != nil {
			return errors.New("a terminal login start cannot include an action")
		}
		return nil
	}
	if !terminalSessionIDPattern.MatchString(input.SessionID) {
		return errors.New("invalid terminal login session ID")
	}
	if input.Action == nil {
		return errors.New("a terminal login continuation requires an action")
	}
	return input.Action.validate()
}

func (action TerminalLoginAction) validate() error {
	switch action.Type {
	case "refresh", "cancel":
		if action.ControlID != "" || action.Key != "" {
			return errors.New("terminal refresh or cancellation cannot include a control or key")
		}
		return nil
	case "activate", "focus":
		if !terminalControlIDPattern.MatchString(action.ControlID) || action.Key != "" {
			return errors.New("invalid terminal control action")
		}
		return nil
	case "key":
		if !terminalControlIDPattern.MatchString(action.ControlID) {
			return errors.New("invalid terminal key control")
		}
		if action.Key == "enter" || action.Key == "backspace" || action.Key == "tab" {
			return nil
		}
		if utf8.RuneCountInString(action.Key) != 1 {
			return errors.New("terminal key must contain exactly one rune")
		}
		key, _ := utf8.DecodeRuneInString(action.Key)
		if unicode.IsControl(key) || unicode.Is(unicode.Cf, key) {
			return errors.New("terminal key contains an unsupported control character")
		}
		return nil
	default:
		return errors.New("unsupported terminal login action")
	}
}

// Backend is the complete typed surface hosted by the session owner.
type Backend interface {
	DefaultAccount() domain.AccountID
	Login(context.Context, domain.AccountID, domain.Caller) (LoginResult, error)
	SessionStatus(context.Context, domain.Caller) (SessionStatusResult, error)
	ListMailFolders(context.Context, application.MailFolderListInput, domain.Caller) (application.MailFolderPage, error)
	ListMail(context.Context, application.MailListInput, domain.Caller) (application.MailPage, error)
	SearchMail(context.Context, application.MailSearchInput, domain.Caller) (application.MailPage, error)
	GetMailBody(context.Context, application.MailBodyInput, domain.Caller) (application.MailBodyAccess, error)
	CommitMailBody(context.Context, string, domain.Caller) (application.MailBodyAccess, error)
	GetMailAttachment(context.Context, application.MailAttachmentInput, domain.Caller) (application.MailAttachmentAccess, error)
	CommitMailAttachment(context.Context, string, domain.Caller) (application.MailAttachmentAccess, error)
	CreateMailDraft(context.Context, application.MailDraftInput, domain.Caller) (application.MailDraftAccess, error)
	CommitMailDraft(context.Context, string, domain.Caller) (application.MailDraftAccess, error)
	SendMail(context.Context, application.MailSendInput, domain.Caller) (application.MailSendAccess, error)
	CommitMailSend(context.Context, string, domain.Caller) (application.MailSendAccess, error)
	MoveMail(context.Context, application.MailMoveInput, domain.Caller) (application.MailMoveAccess, error)
	CommitMailMove(context.Context, string, domain.Caller) (application.MailMoveAccess, error)
	SetMailReadState(context.Context, application.MailReadStateInput, domain.Caller) (application.MailReadStateAccess, error)
	CommitMailReadState(context.Context, string, domain.Caller) (application.MailReadStateAccess, error)
	DeleteMail(context.Context, application.MailDeleteInput, domain.Caller) (application.MailDeleteAccess, error)
	CommitMailDelete(context.Context, string, domain.Caller) (application.MailDeleteAccess, error)
	ListCalendar(context.Context, application.CalendarListInput, domain.Caller) (application.CalendarPage, error)
	CreateCalendar(context.Context, application.CalendarCreateInput, domain.Caller) (application.CalendarCreateAccess, error)
	CommitCalendarCreate(context.Context, string, domain.Caller) (application.CalendarCreateAccess, error)
	UpdateCalendar(context.Context, application.CalendarUpdateInput, domain.Caller) (application.CalendarUpdateAccess, error)
	CommitCalendarUpdate(context.Context, string, domain.Caller) (application.CalendarUpdateAccess, error)
	CancelCalendar(context.Context, application.CalendarCancelInput, domain.Caller) (application.CalendarCancelAccess, error)
	CommitCalendarCancel(context.Context, string, domain.Caller) (application.CalendarCancelAccess, error)
}

// TerminalLoginBackend is implemented by session owners that support the
// optional text-only interactive authentication extension.
type TerminalLoginBackend interface {
	TerminalLogin(context.Context, TerminalLoginInput, domain.Caller) (TerminalLoginResult, error)
}

func (method Method) valid() bool {
	switch method {
	case MethodStatus, MethodShutdown, MethodLogin, MethodSessionStatus, MethodTerminalLogin, MethodMailFolders, MethodMailList, MethodMailSearch, MethodMailGetBody, MethodMailCommitBody,
		MethodMailGetAttachment, MethodMailCommitAttachment,
		MethodMailCreateDraft, MethodMailCommitDraft, MethodMailSend, MethodMailCommitSend,
		MethodMailMove, MethodMailCommitMove,
		MethodMailReadState, MethodMailCommitState,
		MethodMailDelete, MethodMailCommitDelete,
		MethodCalendarList, MethodCalendarCreate, MethodCalendarCommit,
		MethodCalendarUpdate, MethodCalendarCommitUpdate,
		MethodCalendarCancel, MethodCalendarCommitCancel:
		return true
	default:
		return false
	}
}

func validateConfigDigest(value string) error {
	if len(value) != 64 {
		return errors.New("config digest must be a SHA-256 hex string")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return errors.New("config digest must be a SHA-256 hex string")
	}
	return nil
}

func (request requestEnvelope) validate() error {
	if request.Version != ProtocolVersion {
		return fmt.Errorf("unsupported daemon protocol version %d", request.Version)
	}
	if !requestIDPattern.MatchString(request.ID) {
		return errors.New("invalid daemon request ID")
	}
	if !request.Method.valid() {
		return errors.New("unknown daemon method")
	}
	if err := request.Caller.Validate(); err != nil {
		return fmt.Errorf("invalid daemon caller: %w", err)
	}
	if len(request.Params) == 0 {
		return errors.New("daemon request params are required")
	}
	return nil
}
