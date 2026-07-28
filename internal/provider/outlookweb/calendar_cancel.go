package outlookweb

import (
	"context"
	"errors"

	"github.com/nkiyohara/corresync/internal/application"
)

type calendarCancelEnvelope struct {
	Type   string                `json:"__type"`
	Header requestHeader         `json:"Header"`
	Body   calendarCancelRequest `json:"Body"`
}

type calendarCancelRequest struct {
	Type                     string   `json:"__type"`
	ItemIDs                  []itemID `json:"ItemIds"`
	DeleteType               string   `json:"DeleteType"`
	SendMeetingCancellations string   `json:"SendMeetingCancellations"`
	AffectedTaskOccurrences  string   `json:"AffectedTaskOccurrences"`
	SuppressReadReceipts     bool     `json:"SuppressReadReceipts"`
}

type calendarCancelResponseBody struct {
	ResponseMessages responseMessages[calendarCancelResponseMessage] `json:"ResponseMessages"`
}

type calendarCancelResponseMessage struct {
	ResponseClass string `json:"ResponseClass"`
	ResponseCode  string `json:"ResponseCode"`
}

// CancelCalendarEvent moves one exact event version to Deleted Items and asks
// Outlook to notify all attendees. DeleteItem is never retried.
func (client *Client) CancelCalendarEvent(
	ctx context.Context,
	input application.CalendarCancelInput,
) error {
	if err := input.Validate(); err != nil {
		return err
	}
	payload := buildCalendarCancelEnvelope(input)
	var response responseEnvelope[calendarCancelResponseBody]
	if err := client.Call(ctx, DeleteItem, payload, &response); err != nil {
		return err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return classifyPostRequestError(
			DeleteItem, errors.New("OWA DeleteItem returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	return checkWriteResponse(DeleteItem, message.ResponseClass, message.ResponseCode)
}

func buildCalendarCancelEnvelope(input application.CalendarCancelInput) calendarCancelEnvelope {
	return calendarCancelEnvelope{
		Type: "DeleteItemJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: calendarCancelRequest{
			Type: "DeleteItemRequest:#Exchange",
			ItemIDs: []itemID{{
				Type: "ItemId:#Exchange", ID: input.EventID, ChangeKey: input.ChangeKey,
			}},
			DeleteType:               "MoveToDeletedItems",
			SendMeetingCancellations: "SendToAllAndSaveCopy",
			AffectedTaskOccurrences:  "AllOccurrences",
			SuppressReadReceipts:     true,
		},
	}
}
