package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestWriteCalendarTableNeverEmitsEventControls(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
	page := application.CalendarPage{Events: []application.CalendarEvent{{
		ID:              "event-1",
		ChangeKey:       "change-1",
		Start:           "2026-07-17T09:00:00Z",
		End:             "2026-07-17T10:00:00Z",
		Subject:         "Review\n\x1b[31minjected",
		Location:        "Room\t1",
		IsOnlineMeeting: true,
	}}}
	if err := writeCalendarTable(app, page); err != nil {
		t.Fatalf("writeCalendarTable() error = %v", err)
	}
	if strings.ContainsAny(stdout.String(), "\x1b\r") {
		t.Fatalf("writeCalendarTable() emitted terminal control: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Review injected") || !strings.Contains(stdout.String(), "Room 1") ||
		!strings.Contains(stdout.String(), "event-1") || !strings.Contains(stdout.String(), "change-1") {
		t.Fatalf("writeCalendarTable() did not preserve sanitized content: %q", stdout.String())
	}
}

func TestWriteCalendarCreateReviewNeverEmitsControls(t *testing.T) {
	t.Parallel()

	input := application.CalendarCreateInput{
		Calendar:          application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Subject:           "Review\x1b[2Jsubject",
		Body:              "line one\n\x1b[31mline two",
		Start:             "2026-07-20T09:00:00Z",
		End:               "2026-07-20T10:00:00Z",
		Location:          "Room\tExample",
		RequiredAttendees: []string{"alice@example.invalid"},
	}
	var output bytes.Buffer
	if err := writeCalendarCreateReview(&output, input.Review(), false); err != nil {
		t.Fatalf("writeCalendarCreateReview() error = %v", err)
	}
	if strings.Contains(output.String(), "\x1b") ||
		!strings.Contains(output.String(), "Reviewsubject") ||
		!strings.Contains(output.String(), "Room Example") ||
		!strings.Contains(output.String(), "alice@example.invalid") {
		t.Fatalf("unsafe or incomplete review: %q", output.String())
	}
}

func TestReadCalendarBodyBoundsStdin(t *testing.T) {
	t.Parallel()

	app := newRuntime(t.Context(), "", &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader("calendar body")
	body, err := readPlainTextBody(app, "-", "calendar event")
	if err != nil || body != "calendar body" {
		t.Fatalf("readPlainTextBody() = %q, %v", body, err)
	}
	app.stdin = strings.NewReader(strings.Repeat("x", application.MaxCalendarBodyBytes+1))
	if _, err := readPlainTextBody(app, "-", "calendar event"); err == nil {
		t.Fatal("readPlainTextBody() unexpectedly accepted an oversized calendar body")
	}
}

func TestWriteCalendarUpdateReviewShowsClearsAndRemovesControls(t *testing.T) {
	t.Parallel()

	input := application.CalendarUpdateInput{
		EventID: "event-1", ChangeKey: "change-1",
		Subject:           stringValuePointer("Updated\x1b[2J subject"),
		Body:              stringValuePointer("line one\n\x1b[31mline two"),
		Location:          stringValuePointer(""),
		ReplaceRecurrence: true,
	}
	var output bytes.Buffer
	if err := writeCalendarUpdateReview(&output, input.Review(), false); err != nil {
		t.Fatalf("writeCalendarUpdateReview() error = %v", err)
	}
	if strings.Contains(output.String(), "\x1b") ||
		!strings.Contains(output.String(), "Updated subject") ||
		!strings.Contains(output.String(), "Location: (clear)") ||
		!strings.Contains(output.String(), "Recurrence: none") ||
		!strings.Contains(output.String(), "line two") {
		t.Fatalf("unsafe or incomplete update review: %q", output.String())
	}
}

func TestWriteCalendarCancelReviewIsExplicitAndSanitized(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	review := application.CalendarCancelReview{
		EventID: "event-1\x1b[2J", ChangeKey: "change-1",
		DeleteType:       "move_to_deleted_items",
		CancellationMode: application.CalendarCancellationModeAll,
	}
	if err := writeCalendarCancelReview(&output, review, false); err != nil {
		t.Fatalf("writeCalendarCancelReview() error = %v", err)
	}
	if strings.Contains(output.String(), "\x1b") ||
		!strings.Contains(output.String(), "nothing was cancelled") ||
		!strings.Contains(output.String(), "move_to_deleted_items") ||
		!strings.Contains(output.String(), "Attendee notifications may be sent: no") {
		t.Fatalf("unsafe or incomplete cancel review: %q", output.String())
	}
}
