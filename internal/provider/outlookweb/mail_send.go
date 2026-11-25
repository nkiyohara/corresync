package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

// SendMail creates one message or response, sends it, and saves the copy in
// Sent Items. Messages with attachments are first saved as an exact draft,
// attached, and then submitted as a saved draft. No write is retried.
func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	if err := input.Validate(application.MaxMailRecipients); err != nil {
		return application.MailSendResult{}, err
	}
	if len(input.Attachments) != 0 {
		draft, err := client.createDraft(ctx, sendDraftInput(input))
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

// sendExistingDraft submits one saved draft version through UpdateItem with
// MessageDisposition SendAndSaveCopy, an EWS-documented way to send an updated
// message. On a live Exchange Online tenant the OWA SendItem action returned
// HTTP 500 for three request shapes (saved-folder target wrapped, flat, and
// omitted), while UpdateItem succeeded. ConflictResolution NeverOverwrite keeps
// the exact-version guarantee: a stale ItemId/ChangeKey pair fails closed
// instead of sending. The single update marks the draft read, which has no
// effect on the sent copy.
func (client *Client) sendExistingDraft(
	ctx context.Context,
	draft application.MailDraft,
) (application.MailSendResult, error) {
	payload := updateItemEnvelope{
		Type: "UpdateItemJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: updateItemRequest{
			Type:               "UpdateItemRequest:#Exchange",
			ConflictResolution: "NeverOverwrite",
			ItemChanges: []itemChange{{
				Type:   "ItemChange:#Exchange",
				ItemID: itemID{Type: "ItemId:#Exchange", ID: draft.ID, ChangeKey: draft.ChangeKey},
				Updates: []setItemField{{
					Type: "SetItemField:#Exchange",
					Item: updateMessage{Type: "Message:#Exchange", IsRead: true},
					Path: propertyURI{Type: "PropertyUri:#Exchange", FieldURI: "message:IsRead"},
				}},
			}},
			MessageDisposition:                     "SendAndSaveCopy",
			SendCalendarInvitationsOrCancellations: "SendToNone",
			SuppressReadReceipts:                   true,
			SavedItemFolderID: &targetFolderID{
				Type:         "TargetFolderId:#Exchange",
				BaseFolderID: folderID{Type: "DistinguishedFolderId:#Exchange", ID: "sentitems"},
			},
		},
	}
	var response responseEnvelope[updateItemResponseBody]
	if err := client.Call(ctx, UpdateItem, payload, &response); err != nil {
		return application.MailSendResult{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailSendResult{}, classifyPostRequestError(
			UpdateItem, errors.New("OWA UpdateItem send returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(UpdateItem, message.ResponseClass, message.ResponseCode); err != nil {
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
