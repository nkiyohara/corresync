package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func TestMailBodyExecutesExplicitSensitiveRead(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, recorder := testMailService(t, reader)
	access, err := service.GetBody(
		context.Background(),
		MailBodyInput{Account: "work", MessageID: "message-1"},
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatalf("GetBody() error = %v", err)
	}
	if access.Status != "completed" || access.Body == nil || access.Body.Text != "Synthetic body" {
		t.Fatalf("unexpected access: %+v", access)
	}
	if reader.calls != 1 || len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected execution: calls=%d events=%+v", reader.calls, recorder.events)
	}
}

func TestMailBodyPreviewAndCommitAreCallerBound(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.Rules{Mode: policy.ModeGuarded, PreviewSensitiveReads: true}, store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	reader := &fakeMailReader{}
	service, err := NewMailService(guard, reader, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.GetBody(
		context.Background(),
		MailBodyInput{Account: "work", MessageID: "message-1"},
		caller,
	)
	if err != nil {
		t.Fatalf("GetBody() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || reader.calls != 0 {
		t.Fatalf("unexpected preview: %+v calls=%d", access, reader.calls)
	}
	wrongCaller := domain.Caller{Surface: "mcp", Instance: "session-2"}
	if _, err := service.CommitBody(context.Background(), access.Preview.Token, wrongCaller); err == nil {
		t.Fatal("CommitBody() unexpectedly accepted a different caller")
	}
	committed, err := service.CommitBody(context.Background(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitBody() error = %v", err)
	}
	if committed.Status != "completed" || committed.Body == nil || reader.calls != 1 {
		t.Fatalf("unexpected commit: %+v calls=%d", committed, reader.calls)
	}
	if _, err := service.CommitBody(context.Background(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitBody() unexpectedly replayed an approval")
	}
}

func TestMailBodyAuditsTransportFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.New("synthetic transport failure")}
	service, recorder := testMailService(t, reader)
	_, err := service.GetBody(
		context.Background(),
		MailBodyInput{Account: "work", MessageID: "message-1"},
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil {
		t.Fatal("GetBody() unexpectedly succeeded")
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeFailure {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}
