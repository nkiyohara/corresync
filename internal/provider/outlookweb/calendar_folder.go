package outlookweb

import (
	"context"

	"github.com/nkiyohara/corresync/internal/application"
)

// ListCalendarFolders exposes the distinguished calendar supported by the OWA
// contract. Secondary OWA calendar hierarchy discovery is deliberately not
// inferred from undocumented response shapes.
func (*Client) ListCalendarFolders(
	_ context.Context,
	input application.CalendarFolderListInput,
) (application.CalendarFolderPage, error) {
	calendars := []application.CalendarFolderSummary{{
		ID: "calendar", DisplayName: "Calendar",
		IsDefault: true, CanEdit: true, AccessRole: "owner",
	}}
	start := min(input.Offset, len(calendars))
	end := min(start+input.Limit, len(calendars))
	return application.CalendarFolderPage{
		Calendars:        calendars[start:end],
		TotalCalendars:   len(calendars),
		IncludesLastItem: end == len(calendars),
	}, nil
}
