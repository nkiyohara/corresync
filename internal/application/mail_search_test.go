package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func validMailSearchInput() MailSearchInput {
	return MailSearchInput{
		Account:  "work",
		Folder:   MailFolder{Kind: MailFolderDistinguished, ID: "inbox"},
		Query:    `subject:"Quarterly plan" from:alice`,
		Limit:    25,
		TimeZone: "UTC",
	}
}

func TestMailServiceSearchesThroughPolicyAndContentFreeAudit(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{page: MailPage{Messages: []MailSummary{{ID: "message-1"}}}}
	service, recorder := testMailService(t, reader)
	page, err := service.Search(
		context.Background(), validMailSearchInput(),
		domain.Caller{Surface: "mcp", Instance: "session-1"},
	)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(page.Messages) != 1 || reader.calls != 1 {
		t.Fatalf("unexpected result: page=%+v calls=%d", page, reader.calls)
	}
	if len(recorder.events) != 2 || recorder.events[0].Operation.Name != "mail.search" ||
		recorder.events[1].Outcome != AuditOutcomeSuccess {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
	auditJSON, err := json.Marshal(recorder.events)
	if err != nil {
		t.Fatalf("Marshal(audit events) error = %v", err)
	}
	if bytes.Contains(auditJSON, []byte("Quarterly")) || bytes.Contains(auditJSON, []byte("alice")) {
		t.Fatalf("audit events exposed private search terms: %s", auditJSON)
	}
}

func TestMailSearchInputValidationPreventsReaderCall(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{}
	service, _ := testMailService(t, reader)
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	valid := validMailSearchInput()
	tests := []MailSearchInput{
		{},
		{Account: valid.Account, Folder: valid.Folder, Query: "", Limit: 25, TimeZone: "UTC"},
		{Account: valid.Account, Folder: valid.Folder, Query: " padded ", Limit: 25, TimeZone: "UTC"},
		{Account: valid.Account, Folder: valid.Folder, Query: "line\nbreak", Limit: 25, TimeZone: "UTC"},
		{Account: valid.Account, Folder: valid.Folder, Query: strings.Repeat("x", MaxMailSearchQueryBytes+1), Limit: 25, TimeZone: "UTC"},
		{Account: valid.Account, Folder: valid.Folder, Query: valid.Query, Limit: 51, TimeZone: "UTC"},
	}
	for _, input := range tests {
		if _, err := service.Search(context.Background(), input, caller); err == nil {
			t.Fatalf("Search(%+v) unexpectedly succeeded", input)
		}
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

func TestMailServiceAuditsSearchTransportFailure(t *testing.T) {
	t.Parallel()

	reader := &fakeMailReader{err: errors.New("synthetic transport failure")}
	service, recorder := testMailService(t, reader)
	_, err := service.Search(
		context.Background(), validMailSearchInput(),
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err == nil {
		t.Fatal("Search() unexpectedly succeeded")
	}
	if len(recorder.events) != 2 || recorder.events[1].Outcome != AuditOutcomeFailure ||
		recorder.events[1].Reason != "transport_error" {
		t.Fatalf("unexpected audit events: %+v", recorder.events)
	}
}
