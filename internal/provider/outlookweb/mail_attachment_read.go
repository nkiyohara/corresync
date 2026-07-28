package outlookweb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

type getAttachmentEnvelope struct {
	Type   string               `json:"__type"`
	Header requestHeader        `json:"Header"`
	Body   getAttachmentRequest `json:"Body"`
}

type getAttachmentRequest struct {
	Type               string   `json:"__type"`
	AttachmentIDs      []itemID `json:"AttachmentIds"`
	IncludeMimeContent bool     `json:"IncludeMimeContent"`
}

type getAttachmentResponseBody struct {
	ResponseMessages responseMessages[getAttachmentResponseMessage] `json:"ResponseMessages"`
}

type getAttachmentResponseMessage struct {
	ResponseClass string               `json:"ResponseClass"`
	ResponseCode  string               `json:"ResponseCode"`
	Attachments   []mailAttachmentFile `json:"Attachments"`
}

type mailAttachmentFile struct {
	mailAttachmentItem
	Content []byte `json:"Content"`
}

// GetMailAttachment retrieves one explicit bounded file attachment. Item
// attachments and oversized content fail closed.
func (client *Client) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
) (application.MailAttachment, error) {
	if err := input.Validate(); err != nil {
		return application.MailAttachment{}, err
	}
	payload := buildGetAttachmentEnvelope(input)
	var response responseEnvelope[getAttachmentResponseBody]
	if err := client.Call(ctx, GetAttachment, payload, &response); err != nil {
		return application.MailAttachment{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailAttachment{}, errors.New("OWA GetAttachment returned an unexpected response count")
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(GetAttachment.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailAttachment{}, err
	}
	if len(message.Attachments) != 1 {
		return application.MailAttachment{}, errors.New("OWA GetAttachment did not return exactly one attachment")
	}
	attachment := message.Attachments[0]
	if attachment.Type != "" && attachment.Type != "FileAttachment:#Exchange" {
		return application.MailAttachment{}, errors.New("OWA GetAttachment returned an unsupported item attachment")
	}
	if attachment.AttachmentID.ID != "" && attachment.AttachmentID.ID != input.AttachmentID {
		return application.MailAttachment{}, errors.New("OWA GetAttachment returned a different attachment")
	}
	if len(attachment.Content) > application.MaxMailAttachmentBytes ||
		attachment.Size > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, fmt.Errorf(
			"mail attachment exceeds %d bytes", application.MaxMailAttachmentBytes,
		)
	}
	if attachment.Size != 0 && attachment.Size != len(attachment.Content) {
		return application.MailAttachment{}, errors.New("OWA GetAttachment returned inconsistent attachment size")
	}
	if len(attachment.Name) > 4096 || len(attachment.ContentType) > 255 || len(attachment.ContentID) > 4096 ||
		attachment.Size < 0 {
		return application.MailAttachment{}, errors.New("OWA GetAttachment returned malformed attachment metadata")
	}
	return application.MailAttachment{
		MailAttachmentMetadata: application.MailAttachmentMetadata{
			ID: input.AttachmentID, Kind: "file", Name: attachment.Name, ContentType: attachment.ContentType,
			Size: len(attachment.Content), IsInline: attachment.IsInline,
			ContentID: attachment.ContentID,
		},
		ContentBase64: base64.StdEncoding.EncodeToString(attachment.Content),
	}, nil
}

func buildGetAttachmentEnvelope(input application.MailAttachmentInput) getAttachmentEnvelope {
	return getAttachmentEnvelope{
		Type: "GetAttachmentJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: getAttachmentRequest{
			Type:               "GetAttachmentRequest:#Exchange",
			AttachmentIDs:      []itemID{{Type: "AttachmentIdType:#Exchange", ID: input.AttachmentID}},
			IncludeMimeContent: false,
		},
	}
}
