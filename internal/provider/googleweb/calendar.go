package googleweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type calendarReference struct {
	EventID string `json:"eventId"`
}

func (client *Client) ListCalendarFolders(
	_ context.Context,
	input application.CalendarFolderListInput,
) (application.CalendarFolderPage, error) {
	if !client.calendar {
		return application.CalendarFolderPage{}, errors.New(
			"google Web calendar is not configured",
		)
	}
	id, err := encodeReference("ggwc1_", struct {
		ID string `json:"id"`
	}{ID: "primary"})
	if err != nil {
		return application.CalendarFolderPage{}, err
	}
	calendars := []application.CalendarFolderSummary{{
		ID: id, DisplayName: "Google Calendar",
		IsDefault: true, CanEdit: false, AccessRole: "reader",
	}}
	start := min(input.Offset, len(calendars))
	end := min(start+input.Limit, len(calendars))
	return application.CalendarFolderPage{
		Calendars: calendars[start:end], TotalCalendars: len(calendars),
		IncludesLastItem: end == len(calendars),
	}, nil
}

func (client *Client) ListCalendarEvents(
	ctx context.Context,
	input application.CalendarListInput,
) (application.CalendarPage, error) {
	if !client.calendar {
		return application.CalendarPage{}, errors.New(
			"google Web calendar is not configured",
		)
	}
	if input.Calendar.Kind == application.CalendarFolderOpaque {
		var reference struct {
			ID string `json:"id"`
		}
		if decodeReference(input.Calendar.ID, "ggwc1_", &reference) != nil ||
			reference.ID != "primary" {
			return application.CalendarPage{}, errors.New(
				"calendar ID is not a Google Web identifier",
			)
		}
	}
	start, _ := time.Parse(time.RFC3339, input.Start)
	end, _ := time.Parse(time.RFC3339, input.End)
	target := fmt.Sprintf(
		"%s/calendar/u/0/r/agenda/%d/%d/%d",
		client.calendarOrigin.String(),
		start.Year(),
		int(start.Month()),
		start.Day(),
	)
	rows, err := client.driver.GoogleCalendarRows(ctx, target)
	if err != nil {
		return application.CalendarPage{}, err
	}
	page := application.CalendarPage{
		Events: make([]application.CalendarEvent, 0, len(rows)),
		Start:  input.Start, End: input.End,
	}
	for _, row := range rows {
		eventStart, startErr := time.Parse(time.RFC3339Nano, row.Start)
		eventEnd, endErr := time.Parse(time.RFC3339Nano, row.End)
		if startErr != nil || endErr != nil ||
			!eventStart.Before(eventEnd) ||
			!eventStart.Before(end) || !eventEnd.After(start) {
			continue
		}
		if err := validateWebValue("calendar event ID", row.ID, 2048); err != nil {
			return application.CalendarPage{}, err
		}
		id, err := encodeReference(
			"ggwe1_",
			calendarReference{EventID: row.ID},
		)
		if err != nil {
			return application.CalendarPage{}, err
		}
		change := sha256.Sum256([]byte(
			row.ID + "\x00" + row.Text + "\x00" + row.Start + "\x00" + row.End,
		))
		page.Events = append(page.Events, application.CalendarEvent{
			ID: id, ChangeKey: hex.EncodeToString(change[:]),
			Subject:               boundedWebText(row.Text, 512),
			Start:                 eventStart.Format(time.RFC3339Nano),
			End:                   eventEnd.Format(time.RFC3339Nano),
			OriginalStart:         row.Start,
			OriginalEnd:           row.End,
			OriginalStartTimeZone: "UTC",
			OriginalEndTimeZone:   "UTC",
			Location:              boundedWebText(row.Location, 512),
		})
	}
	return page, nil
}

func (client *Client) CreateCalendarEvent(
	context.Context,
	application.CalendarCreateInput,
) (application.CalendarCreateResult, error) {
	return application.CalendarCreateResult{}, googleWebWriteUnavailable()
}

func (client *Client) UpdateCalendarEvent(
	context.Context,
	application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	return application.CalendarUpdateResult{}, googleWebWriteUnavailable()
}

func (client *Client) CancelCalendarEvent(
	context.Context,
	application.CalendarCancelInput,
) error {
	return googleWebWriteUnavailable()
}
