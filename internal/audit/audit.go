// Package audit records deliberately content-free security events as JSONL.
package audit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const eventIDBytes = 16

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Event = application.AuditEvent
type Phase = application.AuditPhase
type Outcome = application.AuditOutcome

const (
	PhasePrepared  = application.AuditPhasePrepared
	PhaseCommitted = application.AuditPhaseCommitted
	PhaseExecuted  = application.AuditPhaseExecuted
	OutcomeAllowed = application.AuditOutcomeAllowed
	OutcomePreview = application.AuditOutcomePreview
	OutcomeDenied  = application.AuditOutcomeDenied
	OutcomeSuccess = application.AuditOutcomeSuccess
	OutcomeFailure = application.AuditOutcomeFailure
	OutcomeUnknown = application.AuditOutcomeUnknown
)

// Clock makes audit timestamps deterministic in tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Options provides deterministic dependencies without expanding event data.
type Options struct {
	Clock  Clock
	Random io.Reader
}

type Recorder = application.AuditRecorder

// FileRecorder appends synchronized JSON lines to a user-only file.
type FileRecorder struct {
	mu     sync.Mutex
	file   *os.File
	clock  Clock
	random io.Reader
}

// NewFileRecorder opens a protected append-only audit file.
func NewFileRecorder(path string, options Options) (*FileRecorder, error) {
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return nil, fmt.Errorf("protect audit directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("audit path exists and is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect audit path: %w", err)
	}
	file, err := os.OpenFile( // #nosec G304 -- path is the explicit local audit API input.
		path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect audit file: %w", err)
	}
	return &FileRecorder{file: file, clock: options.Clock, random: options.Random}, nil
}

// Record validates, timestamps, writes, and synchronizes one event.
func (recorder *FileRecorder) Record(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEvent(event); err != nil {
		return err
	}
	idBytes := make([]byte, eventIDBytes)
	if _, err := io.ReadFull(recorder.random, idBytes); err != nil {
		return fmt.Errorf("generate audit event ID: %w", err)
	}
	event.SchemaVersion = 1
	event.ID = base64.RawURLEncoding.EncodeToString(idBytes)
	event.Timestamp = recorder.clock.Now().UTC()
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	encoded = append(encoded, '\n')

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if _, err := recorder.file.Write(encoded); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	if err := recorder.file.Sync(); err != nil {
		return fmt.Errorf("sync audit event: %w", err)
	}
	return nil
}

// Close flushes and closes the audit sink.
func (recorder *FileRecorder) Close() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.file.Close()
}

func validateEvent(event Event) error {
	if err := event.Caller.Validate(); err != nil {
		return fmt.Errorf("validate audit caller: %w", err)
	}
	if err := event.Operation.Validate(); err != nil {
		return fmt.Errorf("validate audit operation: %w", err)
	}
	switch event.Phase {
	case PhasePrepared, PhaseCommitted, PhaseExecuted:
	default:
		return fmt.Errorf("unknown audit phase %q", event.Phase)
	}
	switch event.Outcome {
	case OutcomeAllowed, OutcomePreview, OutcomeDenied, OutcomeSuccess, OutcomeFailure, OutcomeUnknown:
	default:
		return fmt.Errorf("unknown audit outcome %q", event.Outcome)
	}
	if event.Reason != "" && !reasonPattern.MatchString(event.Reason) {
		return errors.New("audit reason must be a short machine-readable code")
	}
	return nil
}
