package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

type updateItemEnvelope struct {
	Type   string            `json:"__type"`
	Header requestHeader     `json:"Header"`
	Body   updateItemRequest `json:"Body"`
}

type updateItemRequest struct {
	Type                                   string       `json:"__type"`
	ConflictResolution                     string       `json:"ConflictResolution"`
	ItemChanges                            []itemChange `json:"ItemChanges"`
	MessageDisposition                     string       `json:"MessageDisposition"`
	SendCalendarInvitationsOrCancellations string       `json:"SendCalendarInvitationsOrCancellations"`
	SuppressReadReceipts                   bool         `json:"SuppressReadReceipts"`
}

type itemChange struct {
	Type    string         `json:"__type"`
	ItemID  itemID         `json:"ItemId"`
	Updates []setItemField `json:"Updates"`
}

type setItemField struct {
	Type string        `json:"__type"`
	Item updateMessage `json:"Item"`
	Path propertyURI   `json:"Path"`
}

type updateMessage struct {
	Type   string `json:"__type"`
	IsRead bool   `json:"IsRead"`
}

type updateItemResponseBody struct {
	ResponseMessages responseMessages[updateItemResponseMessage] `json:"ResponseMessages"`
}

type updateItemResponseMessage struct {
	ResponseClass string             `json:"ResponseClass"`
	ResponseCode  string             `json:"ResponseCode"`
	Items         []updateItemResult `json:"Items"`
}

type updateItemResult struct {
	ItemID itemID `json:"ItemId"`
}

// SetMailReadState updates only IsRead on one exact message version.
func (client *Client) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	if err := input.Validate(); err != nil {
		return application.MailReadStateResult{}, err
	}
	payload := buildUpdateReadStateEnvelope(input)
	var response responseEnvelope[updateItemResponseBody]
	if err := client.Call(ctx, UpdateItem, payload, &response); err != nil {
		return application.MailReadStateResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailReadStateResult{}, classifyPostRequestError(
			UpdateItem, errors.New("OWA UpdateItem returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(UpdateItem, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailReadStateResult{}, err
	}
	if len(message.Items) > 1 {
		return application.MailReadStateResult{}, classifyPostRequestError(
			UpdateItem, errors.New("OWA UpdateItem returned too many updated items"),
		)
	}
	result := application.MailReadStateResult{State: input.State}
	if len(message.Items) == 0 {
		return result, nil
	}
	updated := message.Items[0].ItemID
	if err := validateOpaqueID("updated message", updated.ID); err != nil {
		return application.MailReadStateResult{}, classifyPostRequestError(
			UpdateItem, fmt.Errorf("invalid updated message in OWA response: %w", err),
		)
	}
	if updated.ChangeKey != "" {
		if err := validateOpaqueID("updated message change key", updated.ChangeKey); err != nil {
			return application.MailReadStateResult{}, classifyPostRequestError(
				UpdateItem, fmt.Errorf("invalid updated message in OWA response: %w", err),
			)
		}
	}
	result.ID, result.ChangeKey = updated.ID, updated.ChangeKey
	return result, nil
}

func buildUpdateReadStateEnvelope(input application.MailReadStateInput) updateItemEnvelope {
	return updateItemEnvelope{
		Type:   "UpdateItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: updateItemRequest{
			Type:               "UpdateItemRequest:#Exchange",
			ConflictResolution: "NeverOverwrite",
			ItemChanges: []itemChange{{
				Type:   "ItemChange:#Exchange",
				ItemID: itemID{Type: "ItemId:#Exchange", ID: input.MessageID, ChangeKey: input.ChangeKey},
				Updates: []setItemField{{
					Type: "SetItemField:#Exchange",
					Item: updateMessage{
						Type: "Message:#Exchange", IsRead: input.State == application.MailReadStateRead,
					},
					Path: propertyURI{Type: "PropertyUri:#Exchange", FieldURI: "message:IsRead"},
				}},
			}},
			MessageDisposition:                     "SaveOnly",
			SendCalendarInvitationsOrCancellations: "SendToNone",
			SuppressReadReceipts:                   true,
		},
	}
}
