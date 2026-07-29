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
	if input.EffectiveComposeMode() != application.MailComposeNew {
		response, err := client.createResponseDraft(ctx, input)
		if err != nil {
			return application.MailDraft{}, err
		}
		id, err := encodeMessageID(response.ID)
		if err != nil {
			return application.MailDraft{}, err
		}
		return application.MailDraft{
			ID: id, ChangeKey: encodeETag(response.ODataETag),
		}, nil
	}
	request := graphComposition(input)
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
	draftInput := application.MailDraftInput(input)
	if input.ComposeMode != "" &&
		input.ComposeMode != application.MailComposeNew {
		draft, err := client.createResponseDraft(ctx, draftInput)
		if err != nil {
			return application.MailSendResult{}, err
		}
		if _, err := client.api.DoJSON(
			ctx,
			http.MethodPost,
			"me/messages/"+escaped(draft.ID)+"/send",
			nil,
			nil,
			nil,
			true,
			nil,
			http.StatusAccepted,
		); err != nil {
			return application.MailSendResult{}, err
		}
		return application.MailSendResult{}, nil
	}
	request := graphComposition(draftInput)
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

func graphComposition(input application.MailDraftInput) map[string]any {
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
	return message
}

func (client *Client) createResponseDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (graphMessage, error) {
	reference, _, err := client.exactMessage(
		ctx, input.ReferenceMessageID, input.ReferenceChangeKey,
	)
	if err != nil {
		return graphMessage{}, err
	}
	action := ""
	switch input.EffectiveComposeMode() {
	case application.MailComposeNew:
		return graphMessage{}, errors.New(
			"new graph composition does not use a response action",
		)
	case application.MailComposeReply:
		action = "createReply"
	case application.MailComposeReplyAll:
		action = "createReplyAll"
	case application.MailComposeForward:
		action = "createForward"
	default:
		return graphMessage{}, errors.New("unsupported graph response mode")
	}
	message := graphComposition(input)
	if input.EffectiveComposeMode() != application.MailComposeForward {
		delete(message, "toRecipients")
		delete(message, "ccRecipients")
		delete(message, "bccRecipients")
	}
	if input.Subject == "" {
		delete(message, "subject")
	}
	var response graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		"me/messages/"+escaped(reference.ID)+"/"+action,
		nil,
		map[string]any{"message": message},
		&response,
		true,
		nil,
		http.StatusCreated,
	); err != nil {
		return graphMessage{}, err
	}
	if !validGraphID(response.ID) || !validETag(response.ODataETag) {
		return graphMessage{}, fmt.Errorf(
			"%w: graph response draft omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return response, nil
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

func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	reference, _, err := client.exactMessage(
		ctx, input.MessageID, input.ChangeKey,
	)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	destination, err := client.graphFolder(input.Destination)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	var moved graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		"me/messages/"+escaped(reference.ID)+"/move",
		nil,
		map[string]string{"destinationId": destination},
		&moved,
		true,
		nil,
		http.StatusCreated,
	); err != nil {
		return application.MailMoveResult{}, err
	}
	if !validGraphID(moved.ID) || !validETag(moved.ODataETag) {
		return application.MailMoveResult{}, fmt.Errorf(
			"%w: graph move response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeMessageID(moved.ID)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	return application.MailMoveResult{
		ID: id, ChangeKey: encodeETag(moved.ODataETag),
	}, nil
}

func (client *Client) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
) error {
	reference, _, err := client.exactMessage(
		ctx,
		input.MessageID,
		input.ChangeKey,
	)
	if err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx,
		http.MethodPost,
		"users/"+escaped(client.userID)+
			"/messages/"+escaped(reference.ID)+"/permanentDelete",
		nil,
		nil,
		nil,
		true,
		nil,
		http.StatusNoContent,
	)
	return err
}
