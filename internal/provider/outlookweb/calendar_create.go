package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const teamsForBusinessProvider = "TeamsForBusiness"

type calendarCreateEnvelope struct {
	Type   string                `json:"__type"`
	Header requestHeader         `json:"Header"`
	Body   calendarCreateRequest `json:"Body"`
}

type calendarCreateRequest struct {
	Type                   string               `json:"__type"`
	Items                  []calendarCreateItem `json:"Items"`
	SavedItemFolderID      targetFolderID       `json:"SavedItemFolderId"`
	SendMeetingInvitations string               `json:"SendMeetingInvitations"`
}

type calendarCreateItem struct {
	Type                       string                 `json:"__type"`
	Subject                    string                 `json:"Subject"`
	Body                       bodyContent            `json:"Body"`
	Sensitivity                string                 `json:"Sensitivity"`
	Importance                 string                 `json:"Importance"`
	ReminderIsSet              bool                   `json:"ReminderIsSet"`
	ReminderMinutesBeforeStart *int                   `json:"ReminderMinutesBeforeStart,omitempty"`
	IsAllDayEvent              bool                   `json:"IsAllDayEvent"`
	Start                      string                 `json:"Start"`
	End                        string                 `json:"End"`
	FreeBusyType               string                 `json:"FreeBusyType"`
	RequiredAttendees          []calendarAttendee     `json:"RequiredAttendees,omitempty"`
	OptionalAttendees          []calendarAttendee     `json:"OptionalAttendees,omitempty"`
	Location                   calendarCreateLocation `json:"Location"`
	StartTimeZone              timeZoneDefinition     `json:"StartTimeZone"`
	EndTimeZone                timeZoneDefinition     `json:"EndTimeZone"`
	IsOnlineMeeting            bool                   `json:"IsOnlineMeeting,omitempty"`
	OnlineMeetingProvider      string                 `json:"OnlineMeetingProvider,omitempty"`
	Recurrence                 *calendarRecurrence    `json:"Recurrence,omitempty"`
}

type calendarRecurrence struct {
	Type              string `json:"__type"`
	RecurrencePattern any    `json:"RecurrencePattern"`
	RecurrenceRange   any    `json:"RecurrenceRange"`
}

type dailyRecurrencePattern struct {
	Type     string `json:"__type"`
	Interval int    `json:"Interval"`
}

type weeklyRecurrencePattern struct {
	Type           string   `json:"__type"`
	Interval       int      `json:"Interval"`
	DaysOfWeek     []string `json:"DaysOfWeek"`
	FirstDayOfWeek string   `json:"FirstDayOfWeek"`
}

type absoluteMonthlyRecurrencePattern struct {
	Type       string `json:"__type"`
	Interval   int    `json:"Interval"`
	DayOfMonth int    `json:"DayOfMonth"`
}

type absoluteYearlyRecurrencePattern struct {
	Type       string `json:"__type"`
	DayOfMonth int    `json:"DayOfMonth"`
	Month      string `json:"Month"`
}

type endDateRecurrenceRange struct {
	Type      string `json:"__type"`
	StartDate string `json:"StartDate"`
	EndDate   string `json:"EndDate"`
}

type numberedRecurrenceRange struct {
	Type                string `json:"__type"`
	StartDate           string `json:"StartDate"`
	NumberOfOccurrences int    `json:"NumberOfOccurrences"`
}

type calendarAttendee struct {
	Type    string          `json:"__type"`
	Mailbox calendarMailbox `json:"Mailbox"`
}

type calendarMailbox struct {
	Name         string `json:"Name"`
	EmailAddress string `json:"EmailAddress"`
	RoutingType  string `json:"RoutingType"`
	MailboxType  string `json:"MailboxType"`
}

type calendarCreateLocation struct {
	Type          string                `json:"__type"`
	DisplayName   string                `json:"DisplayName"`
	PostalAddress calendarPostalAddress `json:"PostalAddress"`
}

type calendarPostalAddress struct {
	Type           string `json:"__type"`
	AddressType    string `json:"Type"`
	LocationSource string `json:"LocationSource"`
}

type calendarCreateResponseBody struct {
	ResponseMessages responseMessages[calendarCreateResponseMessage] `json:"ResponseMessages"`
}

type calendarCreateResponseMessage struct {
	ResponseClass string                     `json:"ResponseClass"`
	ResponseCode  string                     `json:"ResponseCode"`
	Items         []calendarCreateResultItem `json:"Items"`
}

type calendarCreateResultItem struct {
	ItemID                itemID `json:"ItemId"`
	IsOnlineMeeting       bool   `json:"IsOnlineMeeting"`
	OnlineMeetingProvider string `json:"OnlineMeetingProvider"`
	OnlineMeetingJoinURL  string `json:"OnlineMeetingJoinUrl"`
	JoinOnlineMeetingURL  string `json:"JoinOnlineMeetingUrl"`
}

// CreateCalendarEvent creates exactly one bounded calendar item through OWA's
// specialized CreateCalendarEvent action. Client.Call never retries writes.
func (client *Client) CreateCalendarEvent(
	ctx context.Context,
	input application.CalendarCreateInput,
) (application.CalendarCreateResult, error) {
	if err := input.Validate(application.MaxCalendarAttendees); err != nil {
		return application.CalendarCreateResult{}, err
	}
	payload, err := buildCalendarCreateEnvelope(input)
	if err != nil {
		return application.CalendarCreateResult{}, err
	}
	var response responseEnvelope[calendarCreateResponseBody]
	if err := client.Call(ctx, CreateCalendarEvent, payload, &response); err != nil {
		return application.CalendarCreateResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.CalendarCreateResult{}, classifyPostRequestError(
			CreateCalendarEvent,
			errors.New("OWA CreateCalendarEvent returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(
		CreateCalendarEvent, message.ResponseClass, message.ResponseCode,
	); err != nil {
		return application.CalendarCreateResult{}, err
	}
	if len(message.Items) != 1 {
		return application.CalendarCreateResult{}, classifyPostRequestError(
			CreateCalendarEvent,
			errors.New("OWA CreateCalendarEvent did not return exactly one calendar event"),
		)
	}
	created := message.Items[0].ItemID
	if err := validateOpaqueID("calendar event", created.ID); err != nil {
		return application.CalendarCreateResult{}, classifyPostRequestError(
			CreateCalendarEvent, fmt.Errorf("invalid event in OWA response: %w", err),
		)
	}
	if created.ChangeKey != "" {
		if err := validateOpaqueID("calendar event change key", created.ChangeKey); err != nil {
			return application.CalendarCreateResult{}, classifyPostRequestError(
				CreateCalendarEvent, fmt.Errorf("invalid event in OWA response: %w", err),
			)
		}
	}
	joinURL := message.Items[0].OnlineMeetingJoinURL
	if joinURL == "" {
		joinURL = message.Items[0].JoinOnlineMeetingURL
	}
	provider := ""
	if message.Items[0].IsOnlineMeeting {
		provider = "teams"
	}
	return application.CalendarCreateResult{
		ID: created.ID, ChangeKey: created.ChangeKey,
		IsOnlineMeeting:       message.Items[0].IsOnlineMeeting,
		OnlineMeetingProvider: provider,
		OnlineMeetingJoinURL:  joinURL,
	}, nil
}

func buildCalendarCreateEnvelope(
	input application.CalendarCreateInput,
) (calendarCreateEnvelope, error) {
	if err := input.Validate(application.MaxCalendarAttendees); err != nil {
		return calendarCreateEnvelope{}, err
	}
	start, _ := time.Parse(time.RFC3339, input.Start)
	end, _ := time.Parse(time.RFC3339, input.End)
	folderType := "FolderId:#Exchange"
	if input.Calendar.Kind == application.CalendarFolderDistinguished {
		folderType = "DistinguishedFolderId:#Exchange"
	}
	sendInvitations := "SendToNone"
	if len(input.RequiredAttendees)+len(input.OptionalAttendees) > 0 {
		sendInvitations = "SendToAllAndSaveCopy"
	}
	onlineMeetingProvider := ""
	if input.OnlineMeeting || input.TeamsMeeting {
		onlineMeetingProvider = teamsForBusinessProvider
	}
	zoneID := input.TimeZone
	if zoneID == "" {
		zoneID = defaultZone
	}
	zone := timeZoneDefinition{Type: "TimeZoneDefinitionType:#Exchange", ID: zoneID}
	reminderIsSet := input.Reminder != nil && input.Reminder.Enabled
	var reminderMinutes *int
	if reminderIsSet {
		value := input.Reminder.MinutesBeforeStart
		reminderMinutes = &value
	}
	recurrence := calendarCreateRecurrence(input.Recurrence, start, zoneID)
	request := calendarCreateRequest{
		Type: "CreateItemRequest:#Exchange",
		Items: []calendarCreateItem{{
			Type:                       "CalendarItem:#Exchange",
			Subject:                    input.Subject,
			Body:                       bodyContent{Type: "BodyContentType:#Exchange", BodyType: "Text", Value: input.Body},
			Sensitivity:                "Normal",
			Importance:                 "Normal",
			ReminderIsSet:              reminderIsSet,
			ReminderMinutesBeforeStart: reminderMinutes,
			IsAllDayEvent:              input.AllDay,
			Start:                      formatCalendarBoundaryForZone(start, input.TimeZone),
			End:                        formatCalendarBoundaryForZone(end, input.TimeZone),
			FreeBusyType:               "Busy",
			RequiredAttendees:          calendarAttendees(input.RequiredAttendees),
			OptionalAttendees:          calendarAttendees(input.OptionalAttendees),
			Location: calendarCreateLocation{
				Type: "EnhancedLocation:#Exchange", DisplayName: input.Location,
				PostalAddress: calendarPostalAddress{
					Type: "PersonaPostalAddress:#Exchange", AddressType: "Business", LocationSource: "None",
				},
			},
			StartTimeZone:         zone,
			EndTimeZone:           zone,
			IsOnlineMeeting:       input.OnlineMeeting || input.TeamsMeeting,
			OnlineMeetingProvider: onlineMeetingProvider,
			Recurrence:            recurrence,
		}},
		SavedItemFolderID: targetFolderID{
			Type:         "TargetFolderId:#Exchange",
			BaseFolderID: folderID{Type: folderType, ID: input.Calendar.ID},
		},
		SendMeetingInvitations: sendInvitations,
	}
	return calendarCreateEnvelope{
		Type:   "CreateItemJsonRequest:#Exchange",
		Header: newRequestHeader(zoneID),
		Body:   request,
	}, nil
}

func calendarCreateRecurrence(
	value *application.CalendarRecurrence,
	start time.Time,
	zone string,
) *calendarRecurrence {
	if value == nil {
		return nil
	}
	result := &calendarRecurrence{Type: "RecurrenceType:#Exchange"}
	switch value.Pattern {
	case application.CalendarRecurrenceDaily:
		result.RecurrencePattern = dailyRecurrencePattern{
			Type: "DailyRecurrencePattern:#Exchange", Interval: value.Interval,
		}
	case application.CalendarRecurrenceWeekly:
		result.RecurrencePattern = weeklyRecurrencePattern{
			Type: "WeeklyRecurrencePattern:#Exchange", Interval: value.Interval,
			DaysOfWeek: append([]string(nil), value.DaysOfWeek...), FirstDayOfWeek: "Monday",
		}
	case application.CalendarRecurrenceAbsoluteMonthly:
		result.RecurrencePattern = absoluteMonthlyRecurrencePattern{
			Type:     "AbsoluteMonthlyRecurrencePattern:#Exchange",
			Interval: value.Interval, DayOfMonth: value.DayOfMonth,
		}
	case application.CalendarRecurrenceAbsoluteYearly:
		result.RecurrencePattern = absoluteYearlyRecurrencePattern{
			Type:       "AbsoluteYearlyRecurrencePattern:#Exchange",
			DayOfMonth: value.DayOfMonth, Month: value.Month,
		}
	}
	startDate := formatCalendarRecurrenceDate(start, zone)
	if value.EndDate != "" {
		result.RecurrenceRange = endDateRecurrenceRange{
			Type: "EndDateRecurrenceRange:#Exchange", StartDate: startDate, EndDate: value.EndDate,
		}
	} else {
		result.RecurrenceRange = numberedRecurrenceRange{
			Type: "NumberedRecurrenceRange:#Exchange", StartDate: startDate,
			NumberOfOccurrences: value.NumberOfOccurrences,
		}
	}
	return result
}

func formatCalendarRecurrenceDate(value time.Time, zone string) string {
	if zone == "" || zone == defaultZone {
		return value.UTC().Format("2006-01-02")
	}
	return value.Format("2006-01-02")
}

func calendarAttendees(addresses []string) []calendarAttendee {
	if len(addresses) == 0 {
		return nil
	}
	attendees := make([]calendarAttendee, 0, len(addresses))
	for _, address := range addresses {
		attendees = append(attendees, calendarAttendee{
			Type: "AttendeeType:#Exchange",
			Mailbox: calendarMailbox{
				Name: address, EmailAddress: address, RoutingType: "SMTP", MailboxType: "Mailbox",
			},
		})
	}
	return attendees
}
