package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

type getItemEnvelope struct {
	Type   string         `json:"__type"`
	Header requestHeader  `json:"Header"`
	Body   getItemRequest `json:"Body"`
}

type getItemRequest struct {
	Type      string            `json:"__type"`
	ItemShape itemResponseShape `json:"ItemShape"`
	ItemIDs   []itemID          `json:"ItemIds"`
}

type getItemResponseBody struct {
	ResponseMessages responseMessages[getItemResponseMessage] `json:"ResponseMessages"`
}

type getItemResponseMessage struct {
	ResponseClass string           `json:"ResponseClass"`
	ResponseCode  string           `json:"ResponseCode"`
	Items         []getItemMessage `json:"Items"`
}

type getItemMessage struct {
	ItemID        itemID               `json:"ItemId"`
	Subject       string               `json:"Subject"`
	Body          bodyContent          `json:"Body"`
	ToRecipients  []recipient          `json:"ToRecipients"`
	CCRecipients  []recipient          `json:"CcRecipients"`
	BCCRecipients []recipient          `json:"BccRecipients"`
	Attachments   []mailAttachmentItem `json:"Attachments"`
	IsDraft       bool                 `json:"IsDraft"`
}

type mailAttachmentItem struct {
	Type         string `json:"__type,omitempty"`
	AttachmentID itemID `json:"AttachmentId"`
	Name         string `json:"Name"`
	ContentType  string `json:"ContentType"`
	Size         int    `json:"Size"`
	IsInline     bool   `json:"IsInline"`
	ContentID    string `json:"ContentId"`
}

type bodyContent struct {
	Type     string `json:"__type,omitempty"`
	BodyType string `json:"BodyType"`
	Value    string `json:"Value"`
}

// GetMessageBody fetches plain text for one explicit item ID.
func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	if err := input.Validate(); err != nil {
		return application.MailBody{}, err
	}
	payload := buildGetItemBodyEnvelope(input)
	var response responseEnvelope[getItemResponseBody]
	if err := client.Call(ctx, GetItem, payload, &response); err != nil {
		return application.MailBody{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailBody{}, errors.New("OWA GetItem returned an unexpected response count")
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(GetItem.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailBody{}, err
	}
	if len(message.Items) != 1 {
		return application.MailBody{}, errors.New("OWA GetItem did not return exactly one message")
	}
	item := message.Items[0]
	if err := validateOpaqueID("message", item.ItemID.ID); err != nil {
		return application.MailBody{}, fmt.Errorf("invalid message in OWA response: %w", err)
	}
	if item.Body.BodyType != "Text" {
		return application.MailBody{}, errors.New("OWA GetItem did not return a plain text body")
	}
	if len(item.Body.Value) > application.MaxMailBodyBytes {
		return application.MailBody{}, fmt.Errorf("mail body exceeds %d bytes", application.MaxMailBodyBytes)
	}
	if len(item.Attachments) > application.MaxMailAttachmentMetadata {
		return application.MailBody{}, fmt.Errorf(
			"OWA GetItem returned more than %d attachments", application.MaxMailAttachmentMetadata,
		)
	}
	attachments := make([]application.MailAttachmentMetadata, 0, len(item.Attachments))
	for _, attachment := range item.Attachments {
		if err := validateOpaqueID("mail attachment", attachment.AttachmentID.ID); err != nil {
			return application.MailBody{}, fmt.Errorf("invalid attachment in OWA response: %w", err)
		}
		if attachment.Size < 0 || len(attachment.Name) > 4096 || len(attachment.ContentType) > 255 ||
			len(attachment.ContentID) > 4096 {
			return application.MailBody{}, errors.New("OWA GetItem returned malformed attachment metadata")
		}
		attachments = append(attachments, application.MailAttachmentMetadata{
			ID: attachment.AttachmentID.ID, Kind: mailAttachmentKind(attachment.Type), Name: attachment.Name,
			ContentType: attachment.ContentType, Size: attachment.Size,
			IsInline: attachment.IsInline, ContentID: attachment.ContentID,
		})
	}
	return application.MailBody{
		ID:          item.ItemID.ID,
		ChangeKey:   item.ItemID.ChangeKey,
		Text:        item.Body.Value,
		Attachments: attachments,
	}, nil
}

func mailAttachmentKind(typeName string) string {
	switch typeName {
	case "ItemAttachment:#Exchange":
		return "item"
	case "FileAttachment:#Exchange", "":
		return "file"
	default:
		return "unknown"
	}
}

func buildGetItemBodyEnvelope(input application.MailBodyInput) getItemEnvelope {
	return getItemEnvelope{
		Type:   "GetItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: getItemRequest{
			Type: "GetItemRequest:#Exchange",
			ItemShape: itemResponseShape{
				Type:      "ItemResponseShape:#Exchange",
				BaseShape: "IdOnly",
				BodyType:  "Text",
				AdditionalProperties: []propertyURI{
					{Type: "PropertyUri:#Exchange", FieldURI: "item:Body"},
					{Type: "PropertyUri:#Exchange", FieldURI: "item:Attachments"},
				},
			},
			ItemIDs: []itemID{{Type: "ItemId:#Exchange", ID: input.MessageID}},
		},
	}
}
