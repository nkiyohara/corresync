package application

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestTaskProjectionPaginatesProviderOrderAndDueTies(t *testing.T) {
	for _, sameDue := range []bool{false, true} {
		t.Run(strconv.FormatBool(sameDue), func(t *testing.T) {
			tasks := []Task{
				projectionTask(projectionAlpha, domain.ProviderCalDAV, "task-c", "2026-09-30"),
				projectionTask(projectionAlpha, domain.ProviderCalDAV, "task-b", "2026-09-20"),
				projectionTask(projectionAlpha, domain.ProviderCalDAV, "task-a", "2026-09-10"),
			}
			if sameDue {
				for i := range tasks {
					tasks[i].Due.Value = "2026-09-10"
				}
			}
			reader := &projectionReaderStub{accounts: []ProjectionAccount{projectionTaskAccount(projectionAlpha, "alpha", domain.ProviderCalDAV, true)}, tasks: map[domain.AccountID][]Task{projectionAlpha: tasks}}
			service, err := NewProjectionService(reader)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, 3)
			for offset := 0; offset < 3; offset++ {
				page, err := service.ListAllTasks(t.Context(), TaskProjectionInput{Offset: offset, Limit: 1}, domain.Caller{Surface: "cli", Instance: "pagination"})
				if err != nil {
					t.Fatal(err)
				}
				if !page.Complete || len(page.Tasks) != 1 || page.HasMore != (offset < 2) {
					t.Fatalf("page=%+v", page)
				}
				got = append(got, page.Tasks[0].Task.ID)
			}
			if !slices.Equal(got, []string{"task-a", "task-b", "task-c"}) {
				t.Fatalf("pages=%v", got)
			}
		})
	}
}

func TestMailProjectionPaginatesProviderOrderAndTimestampTies(t *testing.T) {
	for _, sameTime := range []bool{false, true} {
		t.Run(strconv.FormatBool(sameTime), func(t *testing.T) {
			messages := []MailSummary{
				projectionMail(projectionAlpha, domain.ProviderIMAPSMTP, "uid-3", "2026-09-10T00:00:00Z"),
				projectionMail(projectionAlpha, domain.ProviderIMAPSMTP, "uid-2", "2026-09-20T00:00:00Z"),
				projectionMail(projectionAlpha, domain.ProviderIMAPSMTP, "uid-1", "2026-09-30T00:00:00Z"),
			}
			if sameTime {
				for i := range messages {
					messages[i].ReceivedAt = "2026-09-10T00:00:00Z"
				}
			}
			reader := &projectionReaderStub{accounts: []ProjectionAccount{projectionAccount(projectionAlpha, "alpha", domain.ProviderIMAPSMTP, "", true)}, mail: map[domain.AccountID][]MailSummary{projectionAlpha: messages}}
			service, err := NewProjectionService(reader)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, 3)
			for offset := 0; offset < 3; offset++ {
				page, err := service.SearchAllMail(t.Context(), MailProjectionInput{Folder: MailFolder{Kind: MailFolderDistinguished, ID: "inbox"}, Query: "synthetic", TimeZone: "UTC", Offset: offset, Limit: 1}, domain.Caller{Surface: "cli", Instance: "pagination"})
				if err != nil {
					t.Fatal(err)
				}
				if !page.Complete || len(page.Messages) != 1 || page.HasMore != (offset < 2) {
					t.Fatalf("page=%+v", page)
				}
				got = append(got, page.Messages[0].Message.ID)
			}
			if !slices.Equal(got, []string{"uid-1", "uid-2", "uid-3"}) {
				t.Fatalf("pages=%v", got)
			}
		})
	}
}

func TestMailProjectionRejectsOversizedSourceWithoutReturningItsPrefix(t *testing.T) {
	for _, largeBytes := range []bool{false, true} {
		t.Run(strconv.FormatBool(largeBytes), func(t *testing.T) {
			count := maxMailProjectionSourceItems + 1
			if largeBytes {
				count = 2
			}
			messages := make([]MailSummary, count)
			for i := range messages {
				messages[i] = projectionMail(projectionAlpha, domain.ProviderIMAPSMTP, fmt.Sprintf("uid-%d", i), "2026-09-10T00:00:00Z")
				if largeBytes {
					messages[i].Subject = strings.Repeat("x", maxMailProjectionSourceBytes)
				}
			}
			reader := &projectionReaderStub{accounts: []ProjectionAccount{projectionAccount(projectionAlpha, "alpha", domain.ProviderIMAPSMTP, "", true)}, mail: map[domain.AccountID][]MailSummary{projectionAlpha: messages}}
			service, err := NewProjectionService(reader)
			if err != nil {
				t.Fatal(err)
			}
			page, err := service.SearchAllMail(t.Context(), MailProjectionInput{Folder: MailFolder{Kind: MailFolderDistinguished, ID: "inbox"}, Query: "synthetic", TimeZone: "UTC", Limit: 1}, domain.Caller{Surface: "cli", Instance: "pagination"})
			if err != nil {
				t.Fatal(err)
			}
			if page.Complete || len(page.Messages) != 0 || len(page.Failures) != 1 || page.Accounts[0].Exhausted {
				t.Fatalf("page=%+v", page)
			}
		})
	}
}
