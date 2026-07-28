package caldav

import (
	"context"
	"encoding/base64"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) ListCalendarFolders(
	_ context.Context,
	input application.CalendarFolderListInput,
) (application.CalendarFolderPage, error) {
	start := min(input.Offset, len(client.calendars))
	end := min(start+input.Limit, len(client.calendars))
	page := application.CalendarFolderPage{
		Calendars:        make([]application.CalendarFolderSummary, 0, end-start),
		TotalCalendars:   len(client.calendars),
		IncludesLastItem: end == len(client.calendars),
	}
	for _, calendar := range client.calendars[start:end] {
		name := calendar.Name
		if name == "" {
			name = calendar.Path
		}
		page.Calendars = append(page.Calendars, application.CalendarFolderSummary{
			ID:          "cdc1_" + base64.RawURLEncoding.EncodeToString([]byte(calendar.Path)),
			DisplayName: name,
			IsDefault:   calendar.Path == client.calendarPath,
			CanEdit:     false,
			AccessRole:  "unknown",
		})
	}
	return page, nil
}
