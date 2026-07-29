package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type calendarUpdateEnvelope struct {
	Type   string                `json:"__type"`
	Header requestHeader         `json:"Header"`
	Body   calendarUpdateRequest `json:"Body"`
}

type calendarUpdateRequest struct {
	Type       string             `json:"__type"`
	EventID    itemID             `json:"EventId"`
	ItemChange calendarItemChange `json:"ItemChange"`
	EventScope int                `json:"EventScope"`
}

type calendarItemChange struct {
	Type    string                 `json:"__type"`
	Updates []calendarSetItemField `json:"Updates"`
	ItemID  itemID                 `json:"ItemId"`
}

type calendarSetItemField struct {
	Type string              `json:"__type"`
	Item *calendarUpdateItem `json:"Item,omitempty"`
	Path propertyURI         `json:"Path"`
}

type calendarUpdateItem struct {
	Type                       string                    `json:"__type"`
	Subject                    *string                   `json:"Subject,omitempty"`
	Body                       *bodyContent              `json:"Body,omitempty"`
	Start                      *string                   `json:"Start,omitempty"`
	End                        *string                   `json:"End,omitempty"`
	StartTimeZoneID            *string                   `json:"StartTimeZoneId,omitempty"`
	EndTimeZoneID              *string                   `json:"EndTimeZoneId,omitempty"`
	Locations                  *[]calendarCreateLocation `json:"Locations,omitempty"`
	IsAllDayEvent              *bool                     `json:"IsAllDayEvent,omitempty"`
	ReminderIsSet              *bool                     `json:"ReminderIsSet,omitempty"`
	ReminderMinutesBeforeStart *int                      `json:"ReminderMinutesBeforeStart,omitempty"`
	RequiredAttendees          *[]calendarAttendee       `json:"RequiredAttendees,omitempty"`
	OptionalAttendees          *[]calendarAttendee       `json:"OptionalAttendees,omitempty"`
	Recurrence                 *calendarRecurrence       `json:"Recurrence,omitempty"`
}

// UpdateCalendarEvent applies only the closed application patch to one exact
// item version through OWA's specialized calendar action. The request is never
// retried.
func (client *Client) UpdateCalendarEvent(
	ctx context.Context,
	input application.CalendarUpdateInput,
) (application.CalendarUpdateResult, error) {
	if err := input.Validate(); err != nil {
		return application.CalendarUpdateResult{}, err
	}
	payload, err := buildCalendarUpdateEnvelope(input)
	if err != nil {
		return application.CalendarUpdateResult{}, err
	}
	var response responseEnvelope[updateItemResponseBody]
	if err := client.Call(ctx, UpdateCalendarEvent, payload, &response); err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.CalendarUpdateResult{}, classifyPostRequestError(
			UpdateCalendarEvent,
			errors.New("OWA UpdateCalendarEvent returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(
		UpdateCalendarEvent, message.ResponseClass, message.ResponseCode,
	); err != nil {
		return application.CalendarUpdateResult{}, err
	}
	if len(message.Items) != 1 {
		return application.CalendarUpdateResult{}, classifyPostRequestError(
			UpdateCalendarEvent,
			errors.New("OWA UpdateCalendarEvent did not return exactly one updated calendar event"),
		)
	}
	updated := message.Items[0].ItemID
	if err := validateOpaqueID("updated calendar event", updated.ID); err != nil {
		return application.CalendarUpdateResult{}, classifyPostRequestError(
			UpdateCalendarEvent, fmt.Errorf("invalid updated event in OWA response: %w", err),
		)
	}
	if updated.ChangeKey != "" {
		if err := validateOpaqueID("updated calendar event change key", updated.ChangeKey); err != nil {
			return application.CalendarUpdateResult{}, classifyPostRequestError(
				UpdateCalendarEvent, fmt.Errorf("invalid updated event in OWA response: %w", err),
			)
		}
	}
	return application.CalendarUpdateResult{ID: updated.ID, ChangeKey: updated.ChangeKey}, nil
}

func buildCalendarUpdateEnvelope(
	input application.CalendarUpdateInput,
) (calendarUpdateEnvelope, error) {
	if err := input.Validate(); err != nil {
		return calendarUpdateEnvelope{}, err
	}
	updates := make([]calendarSetItemField, 0, 13)
	if input.Subject != nil {
		updates = append(updates, calendarUpdateField("Subject", calendarUpdateItem{
			Type: "CalendarItem:#Exchange", Subject: cloneOWAString(input.Subject),
		}))
	}
	if input.Body != nil {
		body := bodyContent{Type: "BodyContentType:#Exchange", BodyType: "Text", Value: *input.Body}
		updates = append(updates, calendarUpdateField("Body", calendarUpdateItem{
			Type: "CalendarItem:#Exchange", Body: &body,
		}))
	}
	if input.Start != nil {
		start, _ := time.Parse(time.RFC3339, *input.Start)
		end, _ := time.Parse(time.RFC3339, *input.End)
		zone := defaultZone
		if input.TimeZone != nil {
			zone = *input.TimeZone
		}
		startValue := formatCalendarBoundaryForZone(start, zone)
		endValue := formatCalendarBoundaryForZone(end, zone)
		updates = append(updates,
			calendarUpdateField("Start", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", Start: &startValue,
			}),
			calendarUpdateField("End", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", End: &endValue,
			}),
			calendarUpdateField("StartTimeZoneId", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", StartTimeZoneID: &zone,
			}),
			calendarUpdateField("EndTimeZoneId", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", EndTimeZoneID: &zone,
			}),
		)
	}
	if input.AllDay != nil {
		updates = append(updates, calendarUpdateField("IsAllDayEvent", calendarUpdateItem{
			Type: "CalendarItem:#Exchange", IsAllDayEvent: cloneOWABool(input.AllDay),
		}))
	}
	if input.Reminder != nil {
		enabled := input.Reminder.Enabled
		updates = append(updates, calendarUpdateField("ReminderIsSet", calendarUpdateItem{
			Type: "CalendarItem:#Exchange", ReminderIsSet: &enabled,
		}))
		if enabled {
			minutes := input.Reminder.MinutesBeforeStart
			updates = append(updates, calendarUpdateField("ReminderMinutesBeforeStart", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", ReminderMinutesBeforeStart: &minutes,
			}))
		}
	}
	if input.ReplaceAttendees {
		required := calendarAttendees(input.RequiredAttendees)
		optional := calendarAttendees(input.OptionalAttendees)
		if required == nil {
			required = []calendarAttendee{}
		}
		if optional == nil {
			optional = []calendarAttendee{}
		}
		updates = append(updates,
			calendarUpdateField("RequiredAttendees", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", RequiredAttendees: &required,
			}),
			calendarUpdateField("OptionalAttendees", calendarUpdateItem{
				Type: "CalendarItem:#Exchange", OptionalAttendees: &optional,
			}),
		)
	}
	if input.Location != nil {
		locations := make([]calendarCreateLocation, 0, 1)
		if *input.Location != "" {
			locations = append(locations, calendarCreateLocation{
				Type: "EnhancedLocation:#Exchange", DisplayName: *input.Location,
				PostalAddress: calendarPostalAddress{
					Type: "PersonaPostalAddress:#Exchange", AddressType: "Business", LocationSource: "None",
				},
			})
		}
		updates = append(updates, calendarUpdateField("Locations", calendarUpdateItem{
			Type: "CalendarItem:#Exchange", Locations: &locations,
		}))
	}
	if input.ReplaceRecurrence {
		if input.Recurrence == nil {
			updates = append(updates, calendarDeleteUpdateField("Recurrence"))
		} else {
			start, _ := time.Parse(time.RFC3339, *input.Start)
			zone := defaultZone
			if input.TimeZone != nil {
				zone = *input.TimeZone
			}
			recurrence := calendarCreateRecurrence(
				input.Recurrence,
				start,
				zone,
			)
			updates = append(
				updates,
				calendarUpdateField("Recurrence", calendarUpdateItem{
					Type:       "CalendarItem:#Exchange",
					Recurrence: recurrence,
				}),
			)
		}
	}
	identifier := itemID{
		Type: "ItemId:#Exchange", ID: input.EventID, ChangeKey: input.ChangeKey,
	}
	zone := defaultZone
	if input.TimeZone != nil {
		zone = *input.TimeZone
	}
	return calendarUpdateEnvelope{
		Type: "UpdateCalendarEventJsonRequest:#Exchange", Header: newRequestHeader(zone),
		Body: calendarUpdateRequest{
			Type:    "UpdateCalendarEventRequest:#Exchange",
			EventID: identifier,
			ItemChange: calendarItemChange{
				Type: "ItemChange:#Exchange", Updates: updates,
				ItemID: identifier,
			},
			EventScope: 0,
		},
	}, nil
}

func calendarUpdateField(fieldURI string, item calendarUpdateItem) calendarSetItemField {
	return calendarSetItemField{
		Type: "SetItemField:#Exchange", Item: &item,
		Path: propertyURI{Type: "PropertyUri:#Exchange", FieldURI: fieldURI},
	}
}

func calendarDeleteUpdateField(fieldURI string) calendarSetItemField {
	return calendarSetItemField{
		Type: "DeleteItemField:#Exchange",
		Path: propertyURI{
			Type: "PropertyUri:#Exchange", FieldURI: fieldURI,
		},
	}
}

func cloneOWAString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneOWABool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
