package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

type createAttachmentEnvelope struct {
	Type   string                  `json:"__type"`
	Header requestHeader           `json:"Header"`
	Body   createAttachmentRequest `json:"Body"`
}

type createAttachmentRequest struct {
	Type         string           `json:"__type"`
	ParentItemID itemID           `json:"ParentItemId"`
	Attachments  []fileAttachment `json:"Attachments"`
}

type fileAttachment struct {
	Type        string `json:"__type"`
	Name        string `json:"Name"`
	ContentType string `json:"ContentType,omitempty"`
	Content     []byte `json:"Content"`
	IsInline    bool   `json:"IsInline"`
}

type createAttachmentResponseBody struct {
	ResponseMessages responseMessages[createAttachmentResponseMessage] `json:"ResponseMessages"`
}

type createAttachmentResponseMessage struct {
	ResponseClass     string                   `json:"ResponseClass"`
	ResponseCode      string                   `json:"ResponseCode"`
	Attachments       []createAttachmentResult `json:"Attachments"`
	RootItemID        string                   `json:"RootItemId"`
	RootItemChangeKey string                   `json:"RootItemChangeKey"`
}

type createAttachmentResult struct {
	AttachmentID attachmentID `json:"AttachmentId"`
}

type attachmentID struct {
	ID string `json:"Id"`
}

// createFileAttachments attaches one bounded batch to a draft that was just
// created. Any failure is an unknown overall draft outcome because the parent
// draft already exists and may contain a partial attachment set.
func (client *Client) createFileAttachments(
	ctx context.Context,
	draft application.MailDraft,
	attachments []application.MailFileAttachment,
) (application.MailDraft, error) {
	payload := buildCreateAttachmentEnvelope(draft, attachments)
	var response responseEnvelope[createAttachmentResponseBody]
	if err := client.Call(ctx, CreateAttachment, payload, &response); err != nil {
		return application.MailDraft{}, attachmentOutcomeUnknown(err)
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailDraft{}, attachmentOutcomeUnknown(
			errors.New("OWA CreateAttachment returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(CreateAttachment, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailDraft{}, attachmentOutcomeUnknown(err)
	}
	if len(message.Attachments) != len(attachments) {
		return application.MailDraft{}, attachmentOutcomeUnknown(fmt.Errorf(
			"OWA CreateAttachment returned %d attachments; expected %d",
			len(message.Attachments), len(attachments),
		))
	}
	for _, attachment := range message.Attachments {
		if err := validateOpaqueID("attachment", attachment.AttachmentID.ID); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
	}
	if message.RootItemID != "" {
		if err := validateOpaqueID("attachment parent", message.RootItemID); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		draft.ID = message.RootItemID
	}
	if message.RootItemChangeKey != "" {
		if err := validateOpaqueID("attachment parent change key", message.RootItemChangeKey); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		draft.ChangeKey = message.RootItemChangeKey
	}
	return draft, nil
}

func buildCreateAttachmentEnvelope(
	draft application.MailDraft,
	attachments []application.MailFileAttachment,
) createAttachmentEnvelope {
	items := make([]fileAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		items = append(items, fileAttachment{
			Type: "FileAttachment:#Exchange", Name: attachment.Name,
			ContentType: attachment.ContentType, Content: attachment.Content,
		})
	}
	return createAttachmentEnvelope{
		Type: "CreateAttachmentJsonRequest:#Exchange", Header: newRequestHeader(defaultZone),
		Body: createAttachmentRequest{
			Type: "CreateAttachmentRequest:#Exchange",
			ParentItemID: itemID{
				Type: "ItemId:#Exchange", ID: draft.ID, ChangeKey: draft.ChangeKey,
			},
			Attachments: items,
		},
	}
}

func attachmentOutcomeUnknown(err error) error {
	if err == nil || errors.Is(err, application.ErrWriteOutcomeUnknown) {
		return err
	}
	return errors.Join(application.ErrWriteOutcomeUnknown, err)
}
