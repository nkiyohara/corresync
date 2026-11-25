package outlookweb

import (
	"context"
	"errors"
	"fmt"

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
	item, err := client.getExactItem(
		ctx, input.DraftID, input.DraftChangeKey, draftSnapshotShape(), "draft",
	)
	if err != nil {
		return application.MailDraftSnapshot{}, err
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

// SendMailDraft submits one reviewed Exchange draft version. The send is
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

// getExactItem reads one item at one exact ItemId/ChangeKey pair. Exchange
// rejects a stale pair, and a response for another identity is refused.
func (client *Client) getExactItem(
	ctx context.Context,
	id, changeKey string,
	shape itemResponseShape,
	kind string,
) (getItemMessage, error) {
	payload := buildExactGetItemEnvelope(id, changeKey, shape)
	var response responseEnvelope[getItemResponseBody]
	if err := client.Call(ctx, GetItem, payload, &response); err != nil {
		return getItemMessage{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return getItemMessage{}, errors.New(
			"OWA GetItem returned an unexpected response count",
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(GetItem.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return getItemMessage{}, err
	}
	if len(message.Items) != 1 {
		return getItemMessage{}, fmt.Errorf(
			"OWA GetItem did not return exactly one %s", kind,
		)
	}
	item := message.Items[0]
	if item.ItemID.ID != id || item.ItemID.ChangeKey != changeKey {
		return getItemMessage{}, fmt.Errorf(
			"OWA saved %s changed or returned a different identity", kind,
		)
	}
	return item, nil
}

func draftSnapshotShape() itemResponseShape {
	return exactItemShape(
		"Text",
		"item:Subject",
		"item:Body",
		"item:Attachments",
		"message:ToRecipients",
		"message:CcRecipients",
		"message:BccRecipients",
		"item:IsDraft",
	)
}

func exactItemShape(bodyType string, fields ...string) itemResponseShape {
	properties := make([]propertyURI, 0, len(fields))
	for _, field := range fields {
		properties = append(properties, propertyURI{Type: "PropertyUri:#Exchange", FieldURI: field})
	}
	return itemResponseShape{
		Type: "ItemResponseShape:#Exchange", BaseShape: "IdOnly", BodyType: bodyType,
		AdditionalProperties: properties,
	}
}

func buildExactGetItemEnvelope(id, changeKey string, shape itemResponseShape) getItemEnvelope {
	return getItemEnvelope{
		Type:   "GetItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: getItemRequest{
			Type:      "GetItemRequest:#Exchange",
			ItemShape: shape,
			ItemIDs:   []itemID{{Type: "ItemId:#Exchange", ID: id, ChangeKey: changeKey}},
		},
	}
}

func recipientAddresses(recipients []emailAddress) []string {
	addresses := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addresses = append(addresses, recipient.EmailAddress)
	}
	return addresses
}

var _ application.MailDraftSender = (*Client)(nil)
