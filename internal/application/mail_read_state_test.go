package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validMailReadStateInput() MailReadStateInput {
	return MailReadStateInput{
		Account: "work", MessageID: "message-1", ChangeKey: "change-1", State: MailReadStateRead,
	}
}

func TestMailServiceSetsReadStateThroughPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, recorder := testMailService(t, reader)
	access, err := service.SetReadState(
		context.Background(), validMailReadStateInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatalf("SetReadState() error = %v", err)
	}
	if access.Status != "completed" || access.Updated == nil || access.Updated.ChangeKey != "change-2" || reader.calls != 1 {
		t.Fatalf("unexpected access=%+v calls=%d", access, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Name != "mail.set_read_state" ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailReadStateValidationPreventsTransportCall(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, _ := testMailService(t, reader)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	valid := validMailReadStateInput()
	for _, input := range []MailReadStateInput{
		{},
		{Account: valid.Account, MessageID: "", ChangeKey: valid.ChangeKey, State: valid.State},
		{Account: valid.Account, MessageID: valid.MessageID, ChangeKey: "", State: valid.State},
		{Account: valid.Account, MessageID: valid.MessageID, ChangeKey: valid.ChangeKey, State: "seen"},
	} {
		if _, err := service.SetReadState(context.Background(), input, caller); err == nil {
			t.Fatalf("SetReadState(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("transport calls = %d, want 0", reader.calls)
	}
}

func TestMailReadStateUnknownOutcomeIsAudited(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.Join(ErrWriteOutcomeUnknown, errors.New("synthetic disconnect"))}
	service, recorder := testMailService(t, reader)
	_, err := service.SetReadState(
		context.Background(), validMailReadStateInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("SetReadState() error = %v, want unknown outcome", err)
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeUnknown {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailReadStatePreviewCommitsExactInputOnce(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("approval.NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.Rules{
		Mode: policy.ModeGuarded, PreviewReversibleWrites: true,
	}, store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	service, err := NewMailService(guard, reader, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	preview, err := service.SetReadState(t.Context(), validMailReadStateInput(), caller)
	if err != nil {
		t.Fatalf("SetReadState() error = %v", err)
	}
	if preview.Status != "approval_required" || preview.Preview == nil || reader.calls != 0 {
		t.Fatalf("preview=%+v calls=%d", preview, reader.calls)
	}
	completed, err := service.CommitReadState(t.Context(), preview.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitReadState() error = %v", err)
	}
	if completed.Status != "completed" || completed.Updated == nil ||
		completed.Review != validMailReadStateInput().Review() || reader.calls != 1 {
		t.Fatalf("completed=%+v calls=%d", completed, reader.calls)
	}
	if _, err := service.CommitReadState(t.Context(), preview.Preview.Token, caller); err == nil {
		t.Fatal("CommitReadState() replay unexpectedly succeeded")
	}
}
