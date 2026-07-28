package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func validMailFolderListInput() MailFolderListInput {
	return MailFolderListInput{
		Account:   "work",
		Parent:    MailFolder{Kind: MailFolderDistinguished, ID: "msgfolderroot"},
		Traversal: MailFolderTraversalDeep,
		Limit:     100,
		TimeZone:  "UTC",
	}
}

func TestMailServiceListsFoldersThroughPolicyAndAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{folderPage: MailFolderPage{
		Folders:      []MailFolderSummary{{ID: "folder-1", DisplayName: "Synthetic"}},
		TotalFolders: 1, IncludesLastItem: true,
	}}
	service, recorder := testMailService(t, reader)
	page, err := service.ListFolders(
		context.Background(), validMailFolderListInput(),
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatalf("ListFolders() error = %v", err)
	}
	if len(page.Folders) != 1 || reader.calls != 1 {
		t.Fatalf("unexpected result: page=%+v calls=%d", page, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Name != "mail.folders.list" ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}

func TestMailFolderListInputValidationPreventsReaderCall(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, _ := testMailService(t, reader)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	for _, input := range []MailFolderListInput{
		{},
		{Account: "work", Parent: MailFolder{Kind: MailFolderDistinguished, ID: "root"}, Traversal: MailFolderTraversalDeep, Limit: 100},
		{Account: "work", Parent: MailFolder{Kind: MailFolderOpaque}, Traversal: MailFolderTraversalDeep, Limit: 100},
		{Account: "work", Parent: MailFolder{Kind: MailFolderDistinguished, ID: "msgfolderroot"}, Traversal: "sideways", Limit: 100},
		{Account: "work", Parent: MailFolder{Kind: MailFolderDistinguished, ID: "msgfolderroot"}, Traversal: MailFolderTraversalDeep},
		{Account: "work", Parent: MailFolder{Kind: MailFolderDistinguished, ID: "msgfolderroot"}, Traversal: MailFolderTraversalDeep, Limit: 101},
		{Account: "work", Parent: MailFolder{Kind: MailFolderDistinguished, ID: "msgfolderroot"}, Traversal: MailFolderTraversalDeep, Limit: 100, TimeZone: "UTC\nInjected"},
	} {
		if _, err := service.ListFolders(context.Background(), input, caller); err == nil {
			t.Fatalf("ListFolders(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

func TestMailServiceAuditsFolderTransportFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.New("synthetic transport failure")}
	service, recorder := testMailService(t, reader)
	_, err := service.ListFolders(
		context.Background(), validMailFolderListInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil {
		t.Fatal("ListFolders() unexpectedly succeeded")
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeFailure ||
		recorder.events[1].Reason != "transport_error" {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}
