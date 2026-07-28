package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

// SendMail creates one message or response, sends it, and saves the copy in
// Sent Items. Messages with attachments are first saved as an exact draft,
// attached, and then submitted with SendItem. No write is retried.
func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	if err := input.Validate(application.MaxMailRecipients); err != nil {
		return application.MailSendResult{}, err
	}
	if len(input.Attachments) != 0 {
		draft, err := client.CreateMailDraft(ctx, sendDraftInput(input))
		if err != nil {
			return application.MailSendResult{}, errors.Join(application.ErrWriteOutcomeUnknown, err)
		}
		return client.sendExistingDraft(ctx, draft)
	}
	var response responseEnvelope[createItemResponseBody]
	if err := client.Call(ctx, CreateItem, buildSendEnvelope(input), &response); err != nil {
		return application.MailSendResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailSendResult{}, classifyPostRequestError(
			CreateItem, errors.New("OWA CreateItem send returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(CreateItem, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailSendResult{}, err
	}
	if len(message.Items) > 1 {
		return application.MailSendResult{}, classifyPostRequestError(
			CreateItem, errors.New("OWA CreateItem send returned multiple sent copies"),
		)
	}
	if len(message.Items) == 0 {
		return application.MailSendResult{}, nil
	}
	item := message.Items[0]
	if item.ItemID.ID == "" {
		if item.ItemID.ChangeKey != "" {
			return application.MailSendResult{}, classifyPostRequestError(
				CreateItem, errors.New("OWA CreateItem send returned a change key without a sent-copy ID"),
			)
		}
		return application.MailSendResult{}, nil
	}
	if err := validateOpaqueID("sent copy", item.ItemID.ID); err != nil {
		return application.MailSendResult{}, classifyPostRequestError(
			CreateItem, fmt.Errorf("invalid sent copy in OWA response: %w", err),
		)
	}
	if item.ItemID.ChangeKey != "" {
		if err := validateOpaqueID("sent copy change key", item.ItemID.ChangeKey); err != nil {
			return application.MailSendResult{}, classifyPostRequestError(
				CreateItem, fmt.Errorf("invalid sent copy in OWA response: %w", err),
			)
		}
	}
	return application.MailSendResult{
		ID: item.ItemID.ID, ChangeKey: item.ItemID.ChangeKey,
	}, nil
}

type sendItemEnvelope struct {
	Type   string          `json:"__type"`
	Header requestHeader   `json:"Header"`
	Body   sendItemRequest `json:"Body"`
}

type sendItemRequest struct {
	Type              string         `json:"__type"`
	ItemIDs           []itemID       `json:"ItemIds"`
	SaveItemToFolder  bool           `json:"SaveItemToFolder"`
	SavedItemFolderID targetFolderID `json:"SavedItemFolderId"`
}

func (client *Client) sendExistingDraft(
	ctx context.Context,
	draft application.MailDraft,
) (application.MailSendResult, error) {
	payload := sendItemEnvelope{
		Type: "SendItemJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: sendItemRequest{
			Type: "SendItemRequest:#Exchange",
			ItemIDs: []itemID{{
				Type: "ItemId:#Exchange", ID: draft.ID, ChangeKey: draft.ChangeKey,
			}},
			SaveItemToFolder: true,
			SavedItemFolderID: targetFolderID{
				Type:         "TargetFolderId:#Exchange",
				BaseFolderID: folderID{Type: "DistinguishedFolderId:#Exchange", ID: "sentitems"},
			},
		},
	}
	var response responseEnvelope[createItemResponseBody]
	if err := client.Call(ctx, SendItem, payload, &response); err != nil {
		return application.MailSendResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailSendResult{}, classifyPostRequestError(
			SendItem, errors.New("OWA SendItem returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(SendItem, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailSendResult{}, err
	}
	return application.MailSendResult{}, nil
}

func sendDraftInput(input application.MailSendInput) application.MailDraftInput {
	return application.MailDraftInput(input)
}

func buildSendEnvelope(input application.MailSendInput) createItemEnvelope {
	envelope := buildCreateDraftEnvelope(application.MailDraftInput{
		Account: input.Account, To: input.To, CC: input.CC, BCC: input.BCC,
		Subject: input.Subject, Body: input.Body, BodyFormat: input.BodyFormat,
		ComposeMode: input.ComposeMode, ReferenceMessageID: input.ReferenceMessageID,
		ReferenceChangeKey: input.ReferenceChangeKey,
	})
	envelope.Body.MessageDisposition = "SendAndSaveCopy"
	envelope.Body.MessageDispositionString = "SendAndSaveCopy"
	envelope.Body.SavedItemFolderID.BaseFolderID.ID = "sentitems"
	return envelope
}
