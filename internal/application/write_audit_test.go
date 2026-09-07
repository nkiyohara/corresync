package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type failExecutedAudit struct{}

func (failExecutedAudit) Record(_ context.Context, event AuditEvent) error {
	if event.Phase == AuditPhaseExecuted {
		return errors.New("synthetic disk full")
	}
	return nil
}

func TestProviderWriteAuditFailurePreservesNoReplayClassification(t *testing.T) {
	callFailure := errors.New("provider rejected write")
	auditFailure := errors.New("audit failed")
	for _, tc := range []struct {
		name        string
		call, audit error
		unknown     bool
	}{
		{"success", nil, nil, false},
		{"success audit failed", nil, auditFailure, true},
		{"rejected audit failed", callFailure, auditFailure, false},
		{"ambiguous audit failed", ErrWriteOutcomeUnknown, auditFailure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := providerWriteErrors(tc.call, tc.audit)
			if errors.Is(err, ErrWriteOutcomeUnknown) != tc.unknown {
				t.Fatalf("error=%v", err)
			}
			if tc.call != nil && !errors.Is(err, tc.call) {
				t.Fatalf("lost provider error: %v", err)
			}
			if tc.audit != nil && !errors.Is(err, tc.audit) {
				t.Fatalf("lost audit error: %v", err)
			}
			if tc.call == nil && tc.audit == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMailSendAuditFailurePreservesNoReplayClassification(t *testing.T) {
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewGuard(policy.DefaultRules(), store, failExecutedAudit{})
	if err != nil {
		t.Fatal(err)
	}
	port := &fakeMailReader{}
	service, err := NewMailService(guard, port, testMailOptions())
	if err != nil {
		t.Fatal(err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "audit-failure"}
	preview, err := service.Send(t.Context(), validSendInput(), caller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CommitSend(t.Context(), preview.Preview.Token, caller)
	if port.calls != 1 || !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("calls=%d error=%v", port.calls, err)
	}
	if _, err := service.CommitSend(t.Context(), preview.Preview.Token, caller); err == nil {
		t.Fatal("consumed token replayed")
	}
	if port.calls != 1 {
		t.Fatal("provider write replayed")
	}
}
