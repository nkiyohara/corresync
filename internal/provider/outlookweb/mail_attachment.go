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
	ResponseClass string                   `json:"ResponseClass"`
	ResponseCode  string                   `json:"ResponseCode"`
	Attachments   []createAttachmentResult `json:"Attachments"`
}

type createAttachmentResult struct {
	AttachmentID attachmentID `json:"AttachmentId"`
}

// attachmentID carries the parent item identity as EWS AttachmentIdType does:
// RootItemId and RootItemChangeKey describe the item after this attachment.
type attachmentID struct {
	ID                string `json:"Id"`
	RootItemID        string `json:"RootItemId"`
	RootItemChangeKey string `json:"RootItemChangeKey"`
}

// createFileAttachments attaches one bounded batch to a draft that was just
// created. Exchange answers with one response message per attachment, in
// request order, and the RootItemChangeKey in the last message identifies the
// draft version that carries the whole batch. Any failure is an unknown
// overall draft outcome because the parent draft already exists and may
// contain a partial attachment set.
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
	messages := response.Body.ResponseMessages.Items
	if len(messages) != len(attachments) {
		return application.MailDraft{}, attachmentOutcomeUnknown(fmt.Errorf(
			"OWA CreateAttachment returned %d response messages; expected %d",
			len(messages), len(attachments),
		))
	}
	var parentChangeKey string
	for _, message := range messages {
		if err := checkWriteResponse(CreateAttachment, message.ResponseClass, message.ResponseCode); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		if len(message.Attachments) != 1 {
			return application.MailDraft{}, attachmentOutcomeUnknown(fmt.Errorf(
				"OWA CreateAttachment returned %d attachments in one response message; expected 1",
				len(message.Attachments),
			))
		}
		created := message.Attachments[0].AttachmentID
		if err := validateOpaqueID("attachment", created.ID); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		if err := validateOpaqueID("attachment parent", created.RootItemID); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		if created.RootItemID != draft.ID {
			return application.MailDraft{}, attachmentOutcomeUnknown(
				errors.New("OWA CreateAttachment returned a different parent draft"),
			)
		}
		if err := validateOpaqueID("attachment parent change key", created.RootItemChangeKey); err != nil {
			return application.MailDraft{}, attachmentOutcomeUnknown(err)
		}
		parentChangeKey = created.RootItemChangeKey
	}
	if parentChangeKey != "" {
		draft.ChangeKey = parentChangeKey
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
