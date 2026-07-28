package outlookweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const maxCalendarEvents = 5000

type calendarViewEnvelope struct {
	Type   string              `json:"__type"`
	Header requestHeader       `json:"Header"`
	Body   calendarViewRequest `json:"Body"`
}

type calendarViewRequest struct {
	Type       string         `json:"__type"`
	CalendarID targetFolderID `json:"CalendarId"`
	RangeStart string         `json:"RangeStart"`
	RangeEnd   string         `json:"RangeEnd"`
}

type targetFolderID struct {
	Type         string   `json:"__type"`
	BaseFolderID folderID `json:"BaseFolderId"`
}

type calendarViewResponseBody struct {
	ResponseClass    string                                        `json:"ResponseClass"`
	ResponseCode     string                                        `json:"ResponseCode"`
	Items            []calendarViewItem                            `json:"Items"`
	ResponseMessages responseMessages[calendarViewResponseMessage] `json:"ResponseMessages"`
}

type calendarViewResponseMessage struct {
	ResponseClass string                `json:"ResponseClass"`
	ResponseCode  string                `json:"ResponseCode"`
	Items         []calendarViewItem    `json:"Items"`
	CalendarView  calendarViewItemGroup `json:"CalendarView"`
}

type calendarViewItemGroup struct {
	Items []calendarViewItem `json:"Items"`
}

type calendarViewItem struct {
	ItemID          itemID             `json:"ItemId"`
	Subject         string             `json:"Subject"`
	Start           string             `json:"Start"`
	End             string             `json:"End"`
	StartTimeZone   timeZoneDefinition `json:"StartTimeZone"`
	EndTimeZone     timeZoneDefinition `json:"EndTimeZone"`
	StartTimeZoneID string             `json:"StartTimeZoneId"`
	EndTimeZoneID   string             `json:"EndTimeZoneId"`
	Location        calendarPlace      `json:"Location"`
	Locations       []struct {
		DisplayName string `json:"DisplayName"`
	} `json:"Locations"`
	Organizer       recipient `json:"Organizer"`
	IsAllDayEvent   bool      `json:"IsAllDayEvent"`
	IsOnlineMeeting bool      `json:"IsOnlineMeeting"`
	IsOrganizer     bool      `json:"IsOrganizer"`
	IsCancelled     bool      `json:"IsCancelled"`
	IsCanceled      bool      `json:"IsCanceled"`
	MyResponseType  string    `json:"MyResponseType"`
	FreeBusyType    string    `json:"FreeBusyType"`
}

type calendarPlace struct {
	DisplayName string
}

func (place *calendarPlace) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	var displayName string
	if err := json.Unmarshal(data, &displayName); err == nil {
		place.DisplayName = displayName
		return nil
	}
	var object struct {
		DisplayName string `json:"DisplayName"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return errors.New("calendar location must be a string or object")
	}
	place.DisplayName = object.DisplayName
	return nil
}

// ListCalendarEvents implements the application calendar port using the OWA
// GetCalendarView contract.
func (client *Client) ListCalendarEvents(
	ctx context.Context,
	input application.CalendarListInput,
) (application.CalendarPage, error) {
	if err := input.Validate(); err != nil {
		return application.CalendarPage{}, err
	}
	payload, err := buildCalendarViewEnvelope(input)
	if err != nil {
		return application.CalendarPage{}, err
	}
	var response responseEnvelope[calendarViewResponseBody]
	if err := client.Call(ctx, GetCalendarView, payload, &response); err != nil {
		return application.CalendarPage{}, err
	}
	items, err := calendarItems(response.Body)
	if err != nil {
		return application.CalendarPage{}, err
	}
	if len(items) > maxCalendarEvents {
		return application.CalendarPage{}, fmt.Errorf("OWA GetCalendarView returned more than %d events", maxCalendarEvents)
	}
	page := application.CalendarPage{
		Events: make([]application.CalendarEvent, 0, len(items)),
		Start:  input.Start,
		End:    input.End,
	}
	for _, item := range items {
		if err := validateOpaqueID("calendar event", item.ItemID.ID); err != nil {
			return application.CalendarPage{}, fmt.Errorf("invalid event in OWA response: %w", err)
		}
		start, err := normalizeCalendarTime(item.Start)
		if err != nil {
			return application.CalendarPage{}, fmt.Errorf("invalid event start in OWA response: %w", err)
		}
		end, err := normalizeCalendarTime(item.End)
		if err != nil {
			return application.CalendarPage{}, fmt.Errorf("invalid event end in OWA response: %w", err)
		}
		location := item.Location.DisplayName
		if location == "" && len(item.Locations) > 0 {
			location = item.Locations[0].DisplayName
		}
		startZone := item.StartTimeZone.ID
		if startZone == "" {
			startZone = item.StartTimeZoneID
		}
		if startZone == "" {
			startZone = defaultZone
		}
		endZone := item.EndTimeZone.ID
		if endZone == "" {
			endZone = item.EndTimeZoneID
		}
		if endZone == "" {
			endZone = defaultZone
		}
		page.Events = append(page.Events, application.CalendarEvent{
			ID:                    item.ItemID.ID,
			ChangeKey:             item.ItemID.ChangeKey,
			Subject:               item.Subject,
			Start:                 start,
			End:                   end,
			OriginalStart:         item.Start,
			OriginalEnd:           item.End,
			OriginalStartTimeZone: startZone,
			OriginalEndTimeZone:   endZone,
			Location:              location,
			Organizer:             application.MailAddress{Name: item.Organizer.Mailbox.Name, Address: item.Organizer.Mailbox.EmailAddress},
			IsAllDay:              item.IsAllDayEvent,
			IsOnlineMeeting:       item.IsOnlineMeeting,
			IsOrganizer:           item.IsOrganizer,
			IsCancelled:           item.IsCancelled || item.IsCanceled,
			MyResponse:            item.MyResponseType,
			FreeBusy:              item.FreeBusyType,
		})
	}
	return page, nil
}

func buildCalendarViewEnvelope(input application.CalendarListInput) (calendarViewEnvelope, error) {
	if err := input.Validate(); err != nil {
		return calendarViewEnvelope{}, err
	}
	start, _ := time.Parse(time.RFC3339, input.Start)
	end, _ := time.Parse(time.RFC3339, input.End)
	folderType := "FolderId:#Exchange"
	if input.Calendar.Kind == application.CalendarFolderDistinguished {
		folderType = "DistinguishedFolderId:#Exchange"
	}
	return calendarViewEnvelope{
		Type:   "GetCalendarViewJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: calendarViewRequest{
			Type: "GetCalendarViewRequest:#Exchange",
			CalendarID: targetFolderID{
				Type: "TargetFolderId:#Exchange",
				BaseFolderID: folderID{
					Type: folderType,
					ID:   input.Calendar.ID,
				},
			},
			RangeStart: formatCalendarBoundary(start),
			RangeEnd:   formatCalendarBoundary(end),
		},
	}, nil
}

func calendarItems(body calendarViewResponseBody) ([]calendarViewItem, error) {
	if body.ResponseCode != "" || body.ResponseClass != "" {
		if err := checkResponse(GetCalendarView.Name(), body.ResponseClass, body.ResponseCode); err != nil {
			return nil, err
		}
	}
	if body.Items != nil {
		return body.Items, nil
	}
	if len(body.ResponseMessages.Items) != 1 {
		return nil, errors.New("OWA GetCalendarView returned an unexpected response shape")
	}
	message := body.ResponseMessages.Items[0]
	if err := checkResponse(GetCalendarView.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return nil, err
	}
	if message.CalendarView.Items != nil {
		return message.CalendarView.Items, nil
	}
	if message.Items != nil {
		return message.Items, nil
	}
	return nil, errors.New("OWA GetCalendarView response did not contain events")
}

func formatCalendarBoundary(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000")
}

func formatCalendarBoundaryForZone(value time.Time, zone string) string {
	if zone == "" || zone == defaultZone {
		return formatCalendarBoundary(value)
	}
	return value.Format("2006-01-02T15:04:05.000")
}

func normalizeCalendarTime(value string) (string, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339Nano), nil
	}
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", value, time.UTC)
	if err != nil {
		return "", errors.New("calendar time is not supported")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}
