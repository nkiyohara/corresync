package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validMailMoveInput() MailMoveInput {
	return MailMoveInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1",
		Destination: MailFolder{Kind: MailFolderOpaque, ID: "folder-1"},
	}
}

func TestMailMovePreviewCommitsExactImmutableInputOnce(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.Rules{
		Mode: policy.ModeGuarded, PreviewReversibleWrites: true,
	}, store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	service, err := NewMailService(guard, reader, testMailOptions())
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.Move(context.Background(), validMailMoveInput(), caller)
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || reader.calls != 0 {
		t.Fatalf("unexpected preview=%+v calls=%d", access, reader.calls)
	}
	committed, err := service.CommitMove(context.Background(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitMove() error = %v", err)
	}
	if committed.Status != "completed" || committed.Moved == nil || reader.calls != 1 {
		t.Fatalf("unexpected commit=%+v calls=%d", committed, reader.calls)
	}
	if _, err := service.CommitMove(context.Background(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitMove() unexpectedly reused a consumed preview")
	}
}

func TestMailServiceMovesThroughPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, recorder := testMailService(t, reader)
	access, err := service.Move(
		context.Background(), validMailMoveInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if access.Status != "completed" || access.Moved == nil || access.Moved.ID != "moved-1" || reader.calls != 1 {
		t.Fatalf("unexpected access=%+v calls=%d", access, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Name != "mail.move" ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailMoveValidationPreventsTransportCall(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, _ := testMailService(t, reader)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	valid := validMailMoveInput()
	for _, input := range []MailMoveInput{
		{},
		{Account: valid.Account, MessageID: "", ChangeKey: valid.ChangeKey, Destination: valid.Destination},
		{Account: valid.Account, MessageID: valid.MessageID, ChangeKey: "", Destination: valid.Destination},
		{Account: valid.Account, MessageID: valid.MessageID, ChangeKey: valid.ChangeKey, Destination: MailFolder{Kind: MailFolderDistinguished, ID: "random"}},
	} {
		if _, err := service.Move(context.Background(), input, caller); err == nil {
			t.Fatalf("Move(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", reader.calls)
	}
}

func TestMailMoveUnknownOutcomeIsAudited(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.Join(ErrWriteOutcomeUnknown, errors.New("synthetic disconnect"))}
	service, recorder := testMailService(t, reader)
	_, err := service.Move(
		context.Background(), validMailMoveInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("Move() error = %v, want unknown outcome", err)
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeUnknown ||
		recorder.events[1].Reason != "outcome_unknown" {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}
