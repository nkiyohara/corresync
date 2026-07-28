package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

type fakeMailReader struct {
	page       MailPage
	folderPage MailFolderPage
	err        error
	calls      int
}

func (reader *fakeMailReader) ListMessages(context.Context, MailListInput) (MailPage, error) {
	reader.calls++
	return reader.page, reader.err
}

func (reader *fakeMailReader) SearchMessages(context.Context, MailSearchInput) (MailPage, error) {
	reader.calls++
	return reader.page, reader.err
}

func (reader *fakeMailReader) ListMailFolders(context.Context, MailFolderListInput) (MailFolderPage, error) {
	reader.calls++
	return reader.folderPage, reader.err
}

func (reader *fakeMailReader) GetMessageBody(context.Context, MailBodyInput) (MailBody, error) {
	reader.calls++
	return MailBody{ID: "message-1", Text: "Synthetic body"}, reader.err
}

func (reader *fakeMailReader) GetMailAttachment(context.Context, MailAttachmentInput) (MailAttachment, error) {
	reader.calls++
	return MailAttachment{
		MailAttachmentMetadata: MailAttachmentMetadata{ID: "attachment-1", Name: "fixture.txt"},
		ContentBase64:          "c3ludGhldGljIGF0dGFjaG1lbnQ=",
	}, reader.err
}

func (reader *fakeMailReader) CreateMailDraft(context.Context, MailDraftInput) (MailDraft, error) {
	reader.calls++
	return MailDraft{ID: "draft-1", ChangeKey: "change-1"}, reader.err
}

func (reader *fakeMailReader) SendMail(context.Context, MailSendInput) (MailSendResult, error) {
	reader.calls++
	return MailSendResult{ID: "sent-1", ChangeKey: "change-1"}, reader.err
}

func (reader *fakeMailReader) MoveMail(context.Context, MailMoveInput) (MailMoveResult, error) {
	reader.calls++
	return MailMoveResult{ID: "moved-1", ChangeKey: "change-2"}, reader.err
}

func (reader *fakeMailReader) SetMailReadState(context.Context, MailReadStateInput) (MailReadStateResult, error) {
	reader.calls++
	return MailReadStateResult{ID: "message-1", ChangeKey: "change-2"}, reader.err
}

func (reader *fakeMailReader) DeleteMail(context.Context, MailDeleteInput) error {
	reader.calls++
	return reader.err
}

func testMailService(t *testing.T, reader MailPort) (*MailService, *memoryAudit) {
	t.Helper()
	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	recorder := &memoryAudit{}
	guard, err := NewGuard(policy.DefaultRules(), store, recorder)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	service, err := NewMailService(guard, reader, MailOptions{MaxRecipients: 20})
	if err != nil {
		t.Fatalf("NewMailService() error = %v", err)
	}
	return service, recorder
}

func validMailListInput() MailListInput {
	return MailListInput{
		Account: "work",
		Folder:  MailFolder{Kind: MailFolderDistinguished, ID: "inbox"},
		Limit:   25,
	}
}

func TestMailServiceListsThroughPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{page: MailPage{Messages: []MailSummary{{ID: "message-1"}}}}
	service, recorder := testMailService(t, reader)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	page, err := service.List(context.Background(), validMailListInput(), caller)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Messages) != 1 || reader.calls != 1 {
		t.Fatalf("unexpected result: page=%+v calls=%d", page, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Phase != AuditPhasePrepared ||
		recorder.events[1].Phase != AuditPhaseExecuted ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailServiceAuditsTransportFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.New("synthetic transport failure")}
	service, recorder := testMailService(t, reader)
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	if _, err := service.List(context.Background(), validMailListInput(), caller); err == nil {
		t.Fatal("List() unexpectedly succeeded")
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeFailure ||
		recorder.events[1].Reason != "transport_error" {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailServiceFailsClosedOnAuditFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{page: MailPage{Messages: []MailSummary{{ID: "message-1"}}}}
	service, recorder := testMailService(t, reader)
	recorder.err = errors.New("disk full")
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	if _, err := service.List(context.Background(), validMailListInput(), caller); err == nil {
		t.Fatal("List() unexpectedly returned mailbox data after audit failure")
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0 after prepare audit failure", reader.calls)
	}
}

func TestMailListInputValidationPreventsReaderCall(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, _ := testMailService(t, reader)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	tests := []MailListInput{
		{},
		{Account: "work", Folder: MailFolder{Kind: MailFolderDistinguished, ID: "random"}, Limit: 25},
		{Account: "work", Folder: MailFolder{Kind: MailFolderOpaque}, Limit: 25},
		{Account: "work", Folder: MailFolder{Kind: MailFolderDistinguished, ID: "inbox"}},
		{Account: "work", Folder: MailFolder{Kind: MailFolderDistinguished, ID: "inbox"}, Limit: 101},
	}
	for _, input := range tests {
		if _, err := service.List(context.Background(), input, caller); err == nil {
			t.Fatalf("List(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

func TestNewMailServiceRequiresDependencies(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	if _, err := NewMailService(nil, reader, MailOptions{MaxRecipients: 20}); err == nil {
		t.Fatal("NewMailService() unexpectedly accepted nil guard")
	}
	guard, _ := newTestGuard(t, policy.DefaultRules())
	if _, err := NewMailService(guard, nil, MailOptions{MaxRecipients: 20}); err == nil {
		t.Fatal("NewMailService() unexpectedly accepted nil reader")
	}
	if _, err := NewMailService(guard, reader, MailOptions{}); err == nil {
		t.Fatal("NewMailService() unexpectedly accepted an invalid recipient limit")
	}
}
