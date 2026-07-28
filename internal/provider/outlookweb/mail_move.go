package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

type moveItemEnvelope struct {
	Type   string          `json:"__type"`
	Header requestHeader   `json:"Header"`
	Body   moveItemRequest `json:"Body"`
}

type moveItemRequest struct {
	Type       string         `json:"__type"`
	ItemIDs    []itemID       `json:"ItemIds"`
	ToFolderID targetFolderID `json:"ToFolderId"`
}

type moveItemResponseBody struct {
	ResponseMessages responseMessages[moveItemResponseMessage] `json:"ResponseMessages"`
}

type moveItemResponseMessage struct {
	ResponseClass string           `json:"ResponseClass"`
	ResponseCode  string           `json:"ResponseCode"`
	Items         []moveItemResult `json:"Items"`
}

type moveItemResult struct {
	ItemID itemID `json:"ItemId"`
}

// MoveMail moves one versioned item within the selected account session.
func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	if err := input.Validate(); err != nil {
		return application.MailMoveResult{}, err
	}
	payload := buildMoveItemEnvelope(input)
	var response responseEnvelope[moveItemResponseBody]
	if err := client.Call(ctx, MoveItem, payload, &response); err != nil {
		return application.MailMoveResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailMoveResult{}, classifyPostRequestError(
			MoveItem, errors.New("OWA MoveItem returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(MoveItem, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailMoveResult{}, err
	}
	if len(message.Items) > 1 {
		return application.MailMoveResult{}, classifyPostRequestError(
			MoveItem, errors.New("OWA MoveItem returned too many moved items"),
		)
	}
	if len(message.Items) == 0 {
		return application.MailMoveResult{}, nil
	}
	result := message.Items[0].ItemID
	if err := validateOpaqueID("moved message", result.ID); err != nil {
		return application.MailMoveResult{}, classifyPostRequestError(
			MoveItem, fmt.Errorf("invalid moved message in OWA response: %w", err),
		)
	}
	if result.ChangeKey != "" {
		if err := validateOpaqueID("moved message change key", result.ChangeKey); err != nil {
			return application.MailMoveResult{}, classifyPostRequestError(
				MoveItem, fmt.Errorf("invalid moved message in OWA response: %w", err),
			)
		}
	}
	return application.MailMoveResult{ID: result.ID, ChangeKey: result.ChangeKey}, nil
}

func buildMoveItemEnvelope(input application.MailMoveInput) moveItemEnvelope {
	typeName := "FolderId:#Exchange"
	destinationID := input.Destination.ID
	if input.Destination.Kind == application.MailFolderDistinguished {
		typeName = "DistinguishedFolderId:#Exchange"
		destinationID = strings.ToLower(destinationID)
	}
	return moveItemEnvelope{
		Type:   "MoveItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: moveItemRequest{
			Type: "MoveItemRequest:#Exchange",
			ItemIDs: []itemID{{
				Type: "ItemId:#Exchange", ID: input.MessageID, ChangeKey: input.ChangeKey,
			}},
			ToFolderID: targetFolderID{
				Type: "TargetFolderId:#Exchange",
				BaseFolderID: folderID{
					Type: typeName, ID: destinationID,
				},
			},
		},
	}
}
