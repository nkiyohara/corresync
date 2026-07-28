package application

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestMailDeleteAlwaysPreviewsThenCommitsHardDelete(t *testing.T) {
	t.Parallel()

	port := &fakeMailReader{}
	service, recorder := testMailService(t, port)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	input := MailDeleteInput{Account: "work", MessageID: "message-1", ChangeKey: "change-1"}
	access, err := service.Delete(t.Context(), input, caller)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil || port.calls != 0 ||
		access.Review.DeleteType != "hard_delete" ||
		access.Preview.Operation.Effect != domain.EffectDestructiveWrite {
		t.Fatalf("unsafe preview: %+v calls=%d", access, port.calls)
	}
	committed, err := service.CommitDelete(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitDelete() error = %v", err)
	}
	if committed.Status != "deleted" || committed.Deleted == nil ||
		committed.Deleted.ID != "message-1" || port.calls != 1 {
		t.Fatalf("unexpected commit: %+v calls=%d", committed, port.calls)
	}
	if len(recorder.events) != 3 || recorder.events[2].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailDeleteRequiresExactVersion(t *testing.T) {
	t.Parallel()

	for _, input := range []MailDeleteInput{
		{},
		{Account: "work", MessageID: "message-1"},
		{Account: "work", MessageID: "bad\nmessage", ChangeKey: "change-1"},
	} {
		if err := input.Validate(); err == nil {
			t.Fatalf("Validate(%+v) unexpectedly succeeded", input)
		}
	}
}
