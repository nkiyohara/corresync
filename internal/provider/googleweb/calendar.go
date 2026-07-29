package googleweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
)

type calendarReference struct {
	EventID string `json:"eventId"`
	Start   string `json:"start"`
	End     string `json:"end"`
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
	start, startErr := time.Parse(time.RFC3339, input.Start)
	end, endErr := time.Parse(time.RFC3339, input.End)
	if startErr != nil || endErr != nil || !start.Before(end) ||
		end.Sub(start) > application.MaxCalendarWindow {
		return application.CalendarPage{}, errors.New(
			"google Web calendar window is invalid",
		)
	}
	firstDay := dateUTC(start)
	lastDay := dateUTC(end.Add(-time.Nanosecond))
	uniqueRows := make(map[string]browser.GoogleCalendarRow)
	for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		target := fmt.Sprintf(
			"%s/calendar/u/0/r/agenda/%d/%d/%d",
			client.calendarOrigin.String(),
			day.Year(),
			int(day.Month()),
			day.Day(),
		)
		snapshot, err := client.driver.GoogleCalendarRows(ctx, target)
		if err != nil {
			return application.CalendarPage{}, err
		}
		for _, row := range snapshot.Rows {
			key := row.ID + "\x00" + row.Start + "\x00" + row.End
			uniqueRows[key] = row
			if len(uniqueRows) > 2500 {
				return application.CalendarPage{}, errors.New(
					"google Web calendar window exceeds the configured limit",
				)
			}
		}
	}
	rows := make([]browser.GoogleCalendarRow, 0, len(uniqueRows))
	for _, row := range uniqueRows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].Start != rows[right].Start {
			return rows[left].Start < rows[right].Start
		}
		if rows[left].End != rows[right].End {
			return rows[left].End < rows[right].End
		}
		return rows[left].ID < rows[right].ID
	})
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
			calendarReference{
				EventID: row.ID,
				Start:   row.Start,
				End:     row.End,
			},
		)
		if err != nil {
			return application.CalendarPage{}, err
		}
		change := sha256.Sum256([]byte(
			row.ID + "\x00" + row.Text + "\x00" + row.Start + "\x00" +
				row.End + "\x00" + row.Location,
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

func dateUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(
		value.Year(),
		value.Month(),
		value.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
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
