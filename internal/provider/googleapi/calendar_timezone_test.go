package googleapi

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestGoogleCalendarAllDayDatesUseReviewedZoneBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, zone, start, end, want string }{
		{"default_utc", "", "2026-09-06T17:00:00-07:00", "2026-09-07T17:00:00-07:00", "2026-09-07"},
		{"explicit_utc", "UTC", "2026-09-06T17:00:00-07:00", "2026-09-07T17:00:00-07:00", "2026-09-07"},
		{"named_equivalent_instant", "America/Los_Angeles", "2026-09-06T07:00:00Z", "2026-09-07T07:00:00Z", "2026-09-06"},
		{"named_local", "America/Los_Angeles", "2026-09-06T00:00:00-07:00", "2026-09-07T00:00:00-07:00", "2026-09-06"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := application.CalendarCreateInput{Account: "acc_00000000000000000000000000000001", Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"}, Subject: "Synthetic", Start: test.start, End: test.end, TimeZone: test.zone, AllDay: true}
			if err := input.Validate(10); err != nil {
				t.Fatal(err)
			}
			event, err := googleCreateEvent(input)
			if err != nil {
				t.Fatal(err)
			}
			if event["start"].(map[string]any)["date"] != test.want {
				t.Fatalf("start=%v, want %s", event["start"], test.want)
			}
		})
	}
}
