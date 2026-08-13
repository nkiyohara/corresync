package application

import (
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func validDraftSendInput() MailDraftSendInput {
	return MailDraftSendInput{
		Account: "work", DraftID: "draft-1", DraftChangeKey: "change-1",
	}
}

func validDraftSnapshot() MailDraftSnapshot {
	return MailDraftSnapshot{
		ID: "draft-1", ChangeKey: "change-1",
		To: []string{"alice@example.invalid"}, Subject: "Reviewed draft",
		Body: "Exact saved body", BodyFormat: MailBodyText,
		Attachments: []MailDraftAttachmentSnapshot{{
			Name: "fixture.txt", ContentType: "text/plain", Bytes: 7,
		}},
	}
}

func TestMailSendDraftBindsSnapshotAndCommitsExactVersion(t *testing.T) {
	t.Parallel()
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.DefaultRules(), store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, err := NewMailService(guard, port, testMailOptions())
	if err != nil {
		t.Fatal(err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.SendDraft(t.Context(), validDraftSendInput(), caller)
	if err != nil {
		t.Fatalf("SendDraft() error = %v", err)
	}
	if access.Status != "approval_required" || access.Preview == nil ||
		access.Preview.Operation.Name != "mail.send_draft" ||
		access.Preview.Operation.Effect != domain.EffectExternalWrite ||
		access.Preview.Operation.Target == nil ||
		access.Preview.Operation.Target.Kind != domain.TargetMailbox ||
		access.Review.BodySHA256 == "" || len(access.Review.Attachments) != 1 ||
		port.draftReads != 1 || port.draftSends != 0 {
		t.Fatalf("preview = %+v, reads=%d sends=%d", access, port.draftReads, port.draftSends)
	}
	committed, err := service.CommitSendDraft(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitSendDraft() error = %v", err)
	}
	if committed.Status != "sent" || committed.Sent == nil ||
		committed.Sent.ID != "sent-draft-1" || port.draftSends != 1 {
		t.Fatalf("commit = %+v, sends=%d", committed, port.draftSends)
	}
	if _, err := service.CommitSendDraft(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitSendDraft() replay unexpectedly succeeded")
	}
	if len(recorder.events) != 4 ||
		recorder.events[0].Phase != AuditPhasePrepared ||
		recorder.events[0].Outcome != AuditOutcomePreview ||
		recorder.events[1].Phase != AuditPhasePrepared ||
		recorder.events[2].Phase != AuditPhaseCommitted ||
		recorder.events[3].Phase != AuditPhaseExecuted {
		t.Fatalf("audit events = %+v", recorder.events)
	}
}

func TestMailSendDraftFailsClosedOnStaleSnapshot(t *testing.T) {
	t.Parallel()
	port := &fakeMailReader{draft: validDraftSnapshot()}
	port.draft.ChangeKey = "change-2"
	service, recorder := testMailService(t, port)
	_, err := service.SendDraft(
		t.Context(),
		validDraftSendInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil || port.draftSends != 0 || len(recorder.events) != 1 ||
		recorder.events[0].Outcome != AuditOutcomePreview {
		t.Fatalf(
			"SendDraft() error=%v sends=%d audit=%+v",
			err, port.draftSends, recorder.events,
		)
	}
}

func TestMailSendDraftTokenCannotCrossSendTools(t *testing.T) {
	t.Parallel()
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, _ := testMailService(t, port)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	access, err := service.SendDraft(t.Context(), validDraftSendInput(), caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitSend(t.Context(), access.Preview.Token, caller); err == nil {
		t.Fatal("CommitSend() accepted a saved-draft approval")
	}
	if _, err := service.CommitSendDraft(t.Context(), access.Preview.Token, caller); err != nil {
		t.Fatalf("CommitSendDraft() after mismatched tool = %v", err)
	}
}

func TestMailSendDraftAuditsUnknownSubmissionOutcome(t *testing.T) {
	t.Parallel()
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, recorder := testMailService(t, port)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	access, err := service.SendDraft(t.Context(), validDraftSendInput(), caller)
	if err != nil {
		t.Fatal(err)
	}
	port.err = ErrWriteOutcomeUnknown
	_, err = service.CommitSendDraft(t.Context(), access.Preview.Token, caller)
	if !errors.Is(err, ErrWriteOutcomeUnknown) {
		t.Fatalf("CommitSendDraft() error = %v", err)
	}
	last := recorder.events[len(recorder.events)-1]
	if last.Outcome != AuditOutcomeUnknown || last.Reason != "outcome_unknown" {
		t.Fatalf("audit event = %+v", last)
	}
}

func TestMailSendDraftReadOnlyPolicyRejectsBeforeSnapshotRead(t *testing.T) {
	t.Parallel()
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.Rules{Mode: policy.ModeReadOnly}, store, recorder)
	if err != nil {
		t.Fatal(err)
	}
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, err := NewMailService(guard, port, testMailOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SendDraft(
		t.Context(), validDraftSendInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if !errors.Is(err, ErrDenied) || port.draftReads != 0 || port.draftSends != 0 {
		t.Fatalf("SendDraft() error=%v reads=%d sends=%d", err, port.draftReads, port.draftSends)
	}
	if len(recorder.events) != 1 || recorder.events[0].Outcome != AuditOutcomeDenied {
		t.Fatalf("audit events = %+v", recorder.events)
	}
}

func TestMailSendDraftAuditFailureRejectsBeforeSnapshotRead(t *testing.T) {
	t.Parallel()
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, recorder := testMailService(t, port)
	recorder.err = errors.New("synthetic audit failure")
	_, err := service.SendDraft(
		t.Context(), validDraftSendInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil || port.draftReads != 0 || port.draftSends != 0 {
		t.Fatalf(
			"SendDraft() error=%v reads=%d sends=%d",
			err, port.draftReads, port.draftSends,
		)
	}
}

type mailPortWithoutDraftSend struct {
	MailPort
}

func TestMailSendDraftRejectsRoutesWithoutAnExactVersionContract(t *testing.T) {
	t.Parallel()
	port := &fakeMailReader{draft: validDraftSnapshot()}
	service, _ := testMailService(t, mailPortWithoutDraftSend{MailPort: port})
	_, err := service.SendDraft(
		t.Context(), validDraftSendInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if !errors.Is(err, ErrExactDraftSendUnavailable) ||
		port.draftReads != 0 || port.draftSends != 0 {
		t.Fatalf(
			"SendDraft() error=%v reads=%d sends=%d",
			err, port.draftReads, port.draftSends,
		)
	}
}
