package graphapi

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGraphCalendarNamedZonePreservesApprovedInstantAndRecurrenceDate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, zone, start, end, wantTime, wantDate string }{
		{"iana", "Europe/London", "2026-09-07T23:30:00Z", "2026-09-08T00:30:00Z", "2026-09-08T00:30:00", "2026-09-08"},
		{"windows", "GMT Standard Time", "2026-09-07T23:30:00Z", "2026-09-08T00:30:00Z", "2026-09-08T00:30:00", "2026-09-08"},
		{"default_utc", "", "2026-09-07T17:30:00-07:00", "2026-09-07T18:30:00-07:00", "2026-09-08T00:30:00", "2026-09-08"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := application.CalendarCreateInput{Account: graphTaskAccount, Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"}, Subject: "Synthetic", Start: test.start, End: test.end, TimeZone: test.zone, Recurrence: &application.CalendarRecurrence{Pattern: application.CalendarRecurrenceDaily, Interval: 1, NumberOfOccurrences: 2}}
			if err := input.Validate(10); err != nil {
				t.Fatal(err)
			}
			event, err := graphCreateEvent(input)
			if err != nil {
				t.Fatal(err)
			}
			start := event["start"].(map[string]string)
			recurrence := event["recurrence"].(map[string]any)["range"].(map[string]any)
			if start["dateTime"] != test.wantTime || recurrence["startDate"] != test.wantDate {
				t.Fatalf("start=%v recurrence=%v", start, recurrence)
			}
		})
	}
	if _, err := graphWriteTime("2026-09-07T09:00:00Z", "Unknown/Time_Zone"); err == nil {
		t.Fatal("accepted unresolved calendar time zone")
	}
}
