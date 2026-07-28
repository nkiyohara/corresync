package outlookweb

import (
	"context"
	"errors"

	"github.com/nkiyohara/corresync/internal/application"
)

// DeleteMail permanently deletes one exact message version. DeleteItem is
// attempted once and any ambiguous post-submit result remains unknown.
func (client *Client) DeleteMail(ctx context.Context, input application.MailDeleteInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	payload := buildMailDeleteEnvelope(input)
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

func buildMailDeleteEnvelope(input application.MailDeleteInput) calendarCancelEnvelope {
	return calendarCancelEnvelope{
		Type: "DeleteItemJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: calendarCancelRequest{
			Type: "DeleteItemRequest:#Exchange",
			ItemIDs: []itemID{{
				Type: "ItemId:#Exchange", ID: input.MessageID, ChangeKey: input.ChangeKey,
			}},
			DeleteType:               "HardDelete",
			SendMeetingCancellations: "SendToNone",
			AffectedTaskOccurrences:  "AllOccurrences",
			SuppressReadReceipts:     true,
		},
	}
}
