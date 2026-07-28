package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestSanitizeCellRemovesTerminalControlsAndBoundsWidth(t *testing.T) {
	t.Parallel()

	input := "Subject\n\x1b[31mred\x1b[0m\twith controls"
	got := sanitizeCell(input, 18)
	if strings.ContainsAny(got, "\n\r\t\x1b") {
		t.Fatalf("sanitizeCell() retained terminal control: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitizeCell() did not mark truncation: %q", got)
	}
}

func TestReadDraftBodyBoundsStdin(t *testing.T) {
	t.Parallel()

	app := newRuntime(t.Context(), "", &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("plain text body")
	body, err := readDraftBody(app, "-")
	if err != nil || body != "plain text body" {
		t.Fatalf("readDraftBody() = %q, %v", body, err)
	}
	app.stdin = strings.NewReader(strings.Repeat("x", application.MaxMailDraftBodyBytes+1))
	if _, err := readDraftBody(app, "-"); err == nil {
		t.Fatal("readDraftBody() unexpectedly accepted an oversized body")
	}
}

func TestReadMailAttachmentsRejectsCountBeforeOpeningFiles(t *testing.T) {
	t.Parallel()

	paths := make([]string, application.MaxMailAttachments+1)
	if _, err := readMailAttachments(paths); err == nil {
		t.Fatal("readMailAttachments() accepted too many paths")
	}
}

func TestWriteMailTableNeverEmitsMailboxControls(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	page := application.MailPage{Messages: []application.MailSummary{{
		ID:         "message-1",
		ReceivedAt: "2026-07-17T10:30:00Z",
		From:       application.MailAddress{Name: "Alice\nInjected"},
		Subject:    "Synthetic\x1b[2Jsubject",
	}}}
	if err := writeMailTable(app, page); err != nil {
		t.Fatalf("writeMailTable() error = %v", err)
	}
	if strings.ContainsAny(stdout.String(), "\x1b\r") {
		t.Fatalf("writeMailTable() emitted terminal control: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Alice Injected") {
		t.Fatalf("writeMailTable() did not preserve sanitized content: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "message-1") {
		t.Fatalf("writeMailTable() did not expose the copyable message ID: %q", stdout.String())
	}
}

func TestMailFolderTableSanitizesNamesAndPreservesIDs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	page := application.MailFolderPage{Folders: []application.MailFolderSummary{{
		ID: "folder-1", DisplayName: "Synthetic\x1b]8;;https://example.invalid\x07 folder",
		Class: "IPF.Note", TotalItemCount: 9, UnreadItemCount: 2,
	}}}
	if err := writeMailFolderTable(app, page); err != nil {
		t.Fatalf("writeMailFolderTable() error = %v", err)
	}
	if strings.Contains(stdout.String(), "\x1b") || !strings.Contains(stdout.String(), "folder-1") {
		t.Fatalf("unsafe or incomplete folder table: %q", stdout.String())
	}
}

func TestCalendarFolderTableSanitizesNamesAndPreservesIDs(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	page := application.CalendarFolderPage{
		Calendars: []application.CalendarFolderSummary{{
			ID: "calendar-1", DisplayName: "Synthetic\x1b[2J calendar",
			IsDefault: true, CanEdit: true, AccessRole: "owner", TimeZone: "UTC",
		}},
		TotalCalendars: 1, IncludesLastItem: true,
	}
	if err := writeCalendarFolderTable(app, page); err != nil {
		t.Fatalf("writeCalendarFolderTable() error = %v", err)
	}
	if strings.Contains(stdout.String(), "\x1b") ||
		!strings.Contains(stdout.String(), "calendar-1") {
		t.Fatalf("unsafe or incomplete calendar table: %q", stdout.String())
	}
}

func TestSanitizeTerminalTextRemovesSequencesButKeepsLayout(t *testing.T) {
	t.Parallel()

	got := sanitizeTerminalText("line one\n\x1b[31mline two\x1b[0m\tvalue")
	if got != "line one\nline two\tvalue" {
		t.Fatalf("sanitizeTerminalText() = %q", got)
	}
}

func TestMailApprovalReviewsExposeExactSanitizedScope(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	review := application.MailReview{
		To: []string{"alice@example.invalid"}, Subject: "Synthetic\x1b[2J subject",
		BodyPreview: "fixture body", BodyBytes: 12, BodySHA256: strings.Repeat("a", 64),
	}
	if err := writeDraftReview(&output, review, false); err != nil {
		t.Fatalf("writeDraftReview() error = %v", err)
	}
	if !strings.Contains(output.String(), "no draft was saved") ||
		!strings.Contains(output.String(), "alice@example.invalid") ||
		strings.Contains(output.String(), "\x1b") {
		t.Fatalf("unsafe or incomplete draft review: %q", output.String())
	}

	output.Reset()
	if err := writeMailMoveReview(&output, application.MailMoveReview{
		MessageID: "message-1", ChangeKey: "change-1",
		Destination: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "archive"},
	}, false); err != nil {
		t.Fatalf("writeMailMoveReview() error = %v", err)
	}
	if !strings.Contains(output.String(), "message-1") ||
		!strings.Contains(output.String(), "change-1") ||
		!strings.Contains(output.String(), "archive") {
		t.Fatalf("incomplete move review: %q", output.String())
	}

	output.Reset()
	if err := writeMailReadStateReview(&output, application.MailReadStateReview{
		MessageID: "message-1", ChangeKey: "change-1", State: application.MailReadStateUnread,
	}, false); err != nil {
		t.Fatalf("writeMailReadStateReview() error = %v", err)
	}
	if !strings.Contains(output.String(), "unread") || !strings.Contains(output.String(), "change-1") {
		t.Fatalf("incomplete read-state review: %q", output.String())
	}
}
