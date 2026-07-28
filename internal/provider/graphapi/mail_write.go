package graphapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	request, err := graphComposition(input)
	if err != nil {
		return application.MailDraft{}, err
	}
	var response graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		"me/messages",
		nil,
		request,
		&response,
		true,
		nil,
		http.StatusCreated,
	); err != nil {
		return application.MailDraft{}, err
	}
	if !validGraphID(response.ID) || !validETag(response.ODataETag) {
		return application.MailDraft{}, fmt.Errorf(
			"%w: graph draft response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeMessageID(response.ID)
	if err != nil {
		return application.MailDraft{}, err
	}
	return application.MailDraft{
		ID: id, ChangeKey: encodeETag(response.ODataETag),
	}, nil
}

func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	request, err := graphComposition(application.MailDraftInput(input))
	if err != nil {
		return application.MailSendResult{}, err
	}
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		"me/sendMail",
		nil,
		map[string]any{"message": request, "saveToSentItems": true},
		nil,
		true,
		nil,
		http.StatusAccepted,
	); err != nil {
		return application.MailSendResult{}, err
	}
	return application.MailSendResult{}, nil
}

func graphComposition(input application.MailDraftInput) (map[string]any, error) {
	if input.EffectiveComposeMode() != application.MailComposeNew {
		return nil, errors.New(
			"graph reply and forward actions do not expose an atomic source-message precondition",
		)
	}
	contentType := "text"
	if input.EffectiveBodyFormat() == application.MailBodyHTML {
		contentType = "html"
	}
	message := map[string]any{
		"subject": input.Subject,
		"body": map[string]string{
			"contentType": contentType,
			"content":     input.Body,
		},
		"toRecipients":  graphRecipients(input.To),
		"ccRecipients":  graphRecipients(input.CC),
		"bccRecipients": graphRecipients(input.BCC),
	}
	if len(input.Attachments) != 0 {
		attachments := make([]map[string]any, 0, len(input.Attachments))
		for _, attachment := range input.Attachments {
			contentType := attachment.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			attachments = append(attachments, map[string]any{
				"@odata.type":  "#microsoft.graph.fileAttachment",
				"name":         attachment.Name,
				"contentType":  contentType,
				"contentBytes": base64.StdEncoding.EncodeToString(attachment.Content),
			})
		}
		message["attachments"] = attachments
	}
	return message, nil
}

func graphRecipients(addresses []string) []map[string]any {
	result := make([]map[string]any, 0, len(addresses))
	for _, address := range addresses {
		result = append(result, map[string]any{
			"emailAddress": map[string]string{"address": address},
		})
	}
	return result
}

func (client *Client) exactMessage(
	ctx context.Context,
	messageID, changeKey string,
) (graphMessageReference, string, error) {
	reference, err := decodeMessageID(messageID)
	if err != nil {
		return graphMessageReference{}, "", err
	}
	etag, err := decodeETag(changeKey)
	if err != nil {
		return graphMessageReference{}, "", err
	}
	message, err := client.getMessage(ctx, reference.ID, false)
	if err != nil {
		return graphMessageReference{}, "", err
	}
	if message.ODataETag != etag {
		return graphMessageReference{}, "", errors.New(
			"graph message changed before write",
		)
	}
	return reference, etag, nil
}

func (client *Client) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	reference, etag, err := client.exactMessage(
		ctx,
		input.MessageID,
		input.ChangeKey,
	)
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	var updated graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPatch,
		"me/messages/"+escaped(reference.ID),
		nil,
		map[string]bool{
			"isRead": input.State == application.MailReadStateRead,
		},
		&updated,
		true,
		http.Header{"If-Match": {etag}},
		http.StatusOK,
	); err != nil {
		return application.MailReadStateResult{}, err
	}
	if updated.ID != reference.ID || !validETag(updated.ODataETag) {
		return application.MailReadStateResult{}, fmt.Errorf(
			"%w: graph message update response omitted identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.MailReadStateResult{
		ID: input.MessageID, ChangeKey: encodeETag(updated.ODataETag),
		State: input.State,
	}, nil
}

func (*Client) MoveMail(
	context.Context,
	application.MailMoveInput,
) (application.MailMoveResult, error) {
	return application.MailMoveResult{}, errors.New(
		"graph message move actions do not expose an atomic ETag precondition",
	)
}

func (client *Client) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
) error {
	reference, etag, err := client.exactMessage(
		ctx,
		input.MessageID,
		input.ChangeKey,
	)
	if err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx,
		http.MethodDelete,
		"me/messages/"+escaped(reference.ID),
		nil,
		nil,
		nil,
		true,
		http.Header{"If-Match": {etag}},
		http.StatusNoContent,
	)
	return err
}
