package application

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type memoryAudit struct {
	mu     sync.Mutex
	events []AuditEvent
	err    error
}

func (audit *memoryAudit) Record(_ context.Context, event AuditEvent) error {
	audit.mu.Lock()
	defer audit.mu.Unlock()
	audit.events = append(audit.events, event)
	return audit.err
}

func newTestGuard(t *testing.T, rules policy.Rules) (*Guard, *memoryAudit) {
	t.Helper()
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("approval.NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(rules, store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	return guard, recorder
}

func operationWithEffect(t *testing.T, effect domain.Effect) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation("test.operation", effect, "work", nil)
	if err != nil {
		t.Fatalf("domain.NewOperation() error = %v", err)
	}
	return operation
}

func TestGuardAllowsReadWithoutPreview(t *testing.T) {
	t.Parallel()

	guard, recorder := newTestGuard(t, policy.DefaultRules())
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	prepared, err := guard.Prepare(context.Background(), operationWithEffect(t, domain.EffectRead), caller)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Decision.Verdict != policy.VerdictAllow || prepared.Preview != nil {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	if len(recorder.events) != 1 || recorder.events[0].Outcome != AuditOutcomeAllowed {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestGuardPreviewsAndCommitsExternalWrite(t *testing.T) {
	t.Parallel()

	guard, recorder := newTestGuard(t, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	operation := operationWithEffect(t, domain.EffectExternalWrite)
	prepared, err := guard.Prepare(context.Background(), operation, caller)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if prepared.Decision.Verdict != policy.VerdictPreview || prepared.Preview == nil {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	committed, err := guard.Commit(context.Background(), prepared.Preview.Token, caller)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if committed.View() != operation.View() {
		t.Fatalf("Commit() operation = %+v, want %+v", committed.View(), operation.View())
	}
	if len(recorder.events) != 2 || recorder.events[1].Phase != AuditPhaseCommitted {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestGuardDeniesWriteInReadOnlyMode(t *testing.T) {
	t.Parallel()

	guard, recorder := newTestGuard(t, policy.Rules{Mode: policy.ModeReadOnly})
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	prepared, err := guard.Prepare(
		context.Background(),
		operationWithEffect(t, domain.EffectDestructiveWrite),
		caller,
	)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Prepare() error = %v, want ErrDenied", err)
	}
	if prepared.Decision.Verdict != policy.VerdictDeny || prepared.Preview != nil {
		t.Fatalf("unexpected preparation: %+v", prepared)
	}
	if len(recorder.events) != 1 || recorder.events[0].Outcome != AuditOutcomeDenied {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestGuardRecordsExecutionOutcome(t *testing.T) {
	t.Parallel()
	guard, recorder := newTestGuard(t, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "execution-test"}
	operation := operationWithEffect(t, domain.EffectReversibleWrite)
	if err := guard.RecordExecution(
		t.Context(),
		operation,
		caller,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := guard.RecordExecution(
		t.Context(),
		operation,
		caller,
		errors.New("synthetic failure"),
	); err != nil {
		t.Fatal(err)
	}
	if len(recorder.events) != 2 ||
		recorder.events[0].Phase != AuditPhaseExecuted ||
		recorder.events[0].Outcome != AuditOutcomeSuccess ||
		recorder.events[1].Outcome != AuditOutcomeFailure {
		t.Fatalf("execution audit = %+v", recorder.events)
	}
}

func TestNewGuardRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	recorder := &memoryAudit{}
	if _, err := NewGuard(policy.Rules{Mode: "invalid"}, nil, recorder); err == nil {
		t.Fatal("NewGuard() unexpectedly accepted invalid policy")
	}
	if _, err := NewGuard(policy.DefaultRules(), nil, recorder); err == nil {
		t.Fatal("NewGuard() unexpectedly accepted nil approval store")
	}
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("approval.NewStore() error = %v", err)
	}
	if _, err := NewGuard(policy.DefaultRules(), store, nil); err == nil {
		t.Fatal("NewGuard() unexpectedly accepted nil audit recorder")
	}
}

func TestGuardFailsClosedWhenAuditFails(t *testing.T) {
	t.Parallel()

	guard, recorder := newTestGuard(t, policy.DefaultRules())
	recorder.err = errors.New("disk full")
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	if _, err := guard.Prepare(
		context.Background(),
		operationWithEffect(t, domain.EffectRead),
		caller,
	); err == nil {
		t.Fatal("Prepare() unexpectedly succeeded after audit failure")
	}
}

func TestGuardConsumesApprovalWhenCommitAuditFails(t *testing.T) {
	t.Parallel()

	guard, recorder := newTestGuard(t, policy.DefaultRules())
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	prepared, err := guard.Prepare(
		context.Background(),
		operationWithEffect(t, domain.EffectExternalWrite),
		caller,
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	recorder.err = errors.New("disk full")
	if _, err := guard.Commit(context.Background(), prepared.Preview.Token, caller); err == nil {
		t.Fatal("Commit() unexpectedly returned an operation after audit failure")
	}
	recorder.err = nil
	if _, err := guard.Commit(context.Background(), prepared.Preview.Token, caller); err == nil {
		t.Fatal("Commit() unexpectedly replayed a token after audit failure")
	}
}
