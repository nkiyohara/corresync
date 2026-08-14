package application

import (
	"context"
	"errors"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

// ErrWriteOutcomeUnknown means a remote write may have committed before its
// response became unavailable. Callers must inspect remote state before retry.
var ErrWriteOutcomeUnknown = errors.New("remote write outcome is unknown; inspect provider state before retrying")

// AuditPhase describes where an operation crossed the application boundary.
type AuditPhase string

const (
	AuditPhasePrepared  AuditPhase = "prepared"
	AuditPhaseCommitted AuditPhase = "committed"
	AuditPhaseExecuted  AuditPhase = "executed"
)

// AuditOutcome is intentionally coarse to avoid leaking remote response data.
type AuditOutcome string

const (
	AuditOutcomeAllowed AuditOutcome = "allowed"
	AuditOutcomePreview AuditOutcome = "preview_required"
	AuditOutcomeDenied  AuditOutcome = "denied"
	AuditOutcomeSuccess AuditOutcome = "success"
	AuditOutcomeFailure AuditOutcome = "failure"
	AuditOutcomeUnknown AuditOutcome = "unknown"
)

// AuditEvent contains no free-form mailbox content and no approval token.
type AuditEvent struct {
	SchemaVersion int                  `json:"schemaVersion"`
	ID            string               `json:"id"`
	Timestamp     time.Time            `json:"timestamp"`
	Phase         AuditPhase           `json:"phase"`
	Outcome       AuditOutcome         `json:"outcome"`
	Reason        string               `json:"reason,omitempty"`
	Caller        domain.Caller        `json:"caller"`
	Operation     domain.OperationView `json:"operation"`
	Monitor       *MonitorAudit        `json:"monitor,omitempty"`
}

// MonitorAudit contains only bounded policy metadata. It records disclosure
// decisions without persisting sender, subject, body, address, or object IDs.
type MonitorAudit struct {
	Stage       string   `json:"stage"`
	Filter      string   `json:"filter,omitempty"`
	Fields      []string `json:"fields,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Result      string   `json:"result,omitempty"`
	Count       int      `json:"count,omitempty"`
}

// AuditRecorder is a required application port. Implementations must not add
// mailbox payloads or approval capabilities to an event.
type AuditRecorder interface {
	Record(context.Context, AuditEvent) error
}
