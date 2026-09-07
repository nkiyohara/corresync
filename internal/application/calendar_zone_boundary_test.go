package application

import "testing"

func TestCalendarNamedZoneValidationUsesAbsoluteInstant(t *testing.T) {
	for _, zone := range []string{"Europe/London", "GMT Standard Time"} {
		t.Run(zone, func(t *testing.T) {
			input := validCalendarCreateInput()
			input.TimeZone = zone
			input.AllDay = true
			input.Start = "2026-07-19T23:00:00Z"
			input.End = "2026-07-20T23:00:00Z"
			if err := input.Validate(50); err != nil {
				t.Fatal(err)
			}
			update := CalendarUpdateInput{Account: input.Account, EventID: "event", ChangeKey: "version", Start: &input.Start, End: &input.End, TimeZone: &input.TimeZone, AllDay: &input.AllDay}
			if err := update.Validate(); err != nil {
				t.Fatal(err)
			}
			input.Start = "2026-07-20T00:00:00Z"
			input.End = "2026-07-21T00:00:00Z"
			if err := input.Validate(50); err == nil {
				t.Fatal("accepted local 01:00 as all-day midnight")
			}
			if err := update.Validate(); err == nil {
				t.Fatal("update accepted local 01:00 as all-day midnight")
			}
			input.AllDay = false
			input.Start = "2026-07-20T23:30:00Z"
			input.End = "2026-07-21T00:30:00Z"
			input.Recurrence = &CalendarRecurrence{Pattern: CalendarRecurrenceDaily, Interval: 1, EndDate: "2026-07-20"}
			if err := input.Validate(50); err == nil {
				t.Fatal("accepted recurrence ending before local start date")
			}
			update.AllDay = nil
			update.ReplaceRecurrence = true
			update.Recurrence = input.Recurrence
			if err := update.Validate(); err == nil {
				t.Fatal("update accepted reversed recurrence dates")
			}
		})
	}
	for _, zone := range []string{"Local", "Unknown/Time_Zone"} {
		input := validCalendarCreateInput()
		input.TimeZone = zone
		if err := input.Validate(50); err == nil {
			t.Fatalf("accepted unresolved zone %s", zone)
		}
	}
}
