package outlookweb

import (
	"context"
	"errors"

	"github.com/nkiyohara/corresync/internal/application"
)

// GetMailDraftSnapshot reads the exact version that will be bound to the
// application approval. Exchange rejects a stale ItemId ChangeKey pair.
func (client *Client) GetMailDraftSnapshot(
	ctx context.Context,
	input application.MailDraftSendInput,
) (application.MailDraftSnapshot, error) {
	if err := input.Validate(); err != nil {
		return application.MailDraftSnapshot{}, err
	}
	payload := buildGetDraftSnapshotEnvelope(input)
	var response responseEnvelope[getItemResponseBody]
	if err := client.Call(ctx, GetItem, payload, &response); err != nil {
		return application.MailDraftSnapshot{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailDraftSnapshot{}, errors.New(
			"OWA GetItem returned an unexpected response count",
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(GetItem.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailDraftSnapshot{}, err
	}
	if len(message.Items) != 1 {
		return application.MailDraftSnapshot{}, errors.New(
			"OWA GetItem did not return exactly one draft",
		)
	}
	item := message.Items[0]
	if item.ItemID.ID != input.DraftID || item.ItemID.ChangeKey != input.DraftChangeKey {
		return application.MailDraftSnapshot{}, errors.New(
			"OWA saved draft changed or returned a different identity",
		)
	}
	if !item.IsDraft {
		return application.MailDraftSnapshot{}, errors.New("OWA item is not a saved draft")
	}
	if item.Body.BodyType != "Text" {
		return application.MailDraftSnapshot{}, errors.New(
			"OWA GetItem did not return the requested text draft body",
		)
	}
	attachments := make([]application.MailDraftAttachmentSnapshot, 0, len(item.Attachments))
	for _, attachment := range item.Attachments {
		attachments = append(attachments, application.MailDraftAttachmentSnapshot{
			Name: attachment.Name, ContentType: attachment.ContentType,
			Bytes: attachment.Size, Inline: attachment.IsInline,
		})
	}
	return application.MailDraftSnapshot{
		ID: input.DraftID, ChangeKey: input.DraftChangeKey,
		To:      recipientAddresses(item.ToRecipients),
		CC:      recipientAddresses(item.CCRecipients),
		BCC:     recipientAddresses(item.BCCRecipients),
		Subject: item.Subject, Body: item.Body.Value,
		BodyFormat: application.MailBodyText, Attachments: attachments,
	}, nil
}

// SendMailDraft submits one reviewed Exchange draft version. SendItem is
// attempted once and the provider performs the native Drafts-to-Sent move.
func (client *Client) SendMailDraft(
	ctx context.Context,
	input application.MailDraftSendInput,
) (application.MailSendResult, error) {
	if err := input.Validate(); err != nil {
		return application.MailSendResult{}, err
	}
	return client.sendExistingDraft(ctx, application.MailDraft{
		ID: input.DraftID, ChangeKey: input.DraftChangeKey,
	})
}

func buildGetDraftSnapshotEnvelope(input application.MailDraftSendInput) getItemEnvelope {
	return getItemEnvelope{
		Type:   "GetItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: getItemRequest{
			Type: "GetItemRequest:#Exchange",
			ItemShape: itemResponseShape{
				Type: "ItemResponseShape:#Exchange", BaseShape: "IdOnly", BodyType: "Text",
				AdditionalProperties: []propertyURI{
					{Type: "PropertyUri:#Exchange", FieldURI: "item:Subject"},
					{Type: "PropertyUri:#Exchange", FieldURI: "item:Body"},
					{Type: "PropertyUri:#Exchange", FieldURI: "item:Attachments"},
					{Type: "PropertyUri:#Exchange", FieldURI: "message:ToRecipients"},
					{Type: "PropertyUri:#Exchange", FieldURI: "message:CcRecipients"},
					{Type: "PropertyUri:#Exchange", FieldURI: "message:BccRecipients"},
					{Type: "PropertyUri:#Exchange", FieldURI: "message:IsDraft"},
				},
			},
			ItemIDs: []itemID{{
				Type: "ItemId:#Exchange", ID: input.DraftID,
				ChangeKey: input.DraftChangeKey,
			}},
		},
	}
}

func recipientAddresses(recipients []recipient) []string {
	addresses := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addresses = append(addresses, recipient.Mailbox.EmailAddress)
	}
	return addresses
}

var _ application.MailDraftSender = (*Client)(nil)
