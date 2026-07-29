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
	var response graphMessage
	var err error
	if input.EffectiveComposeMode() != application.MailComposeNew {
		response, err = client.createResponseDraft(ctx, input)
	} else {
		response, err = client.createNewDraft(ctx, input)
	}
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

func (client *Client) createNewDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (graphMessage, error) {
	var response graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodPost,
		"me/messages",
		nil,
		graphComposition(input),
		&response,
		true,
		nil,
		http.StatusCreated,
	); err != nil {
		return graphMessage{}, err
	}
	if !validGraphID(response.ID) || !validETag(response.ODataETag) {
		return graphMessage{}, fmt.Errorf(
			"%w: graph draft response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return client.assembleDraft(ctx, response, input.Attachments)
}

func (client *Client) assembleDraft(
	ctx context.Context,
	draft graphMessage,
	attachments []application.MailFileAttachment,
) (graphMessage, error) {
	if len(attachments) == 0 {
		return draft, nil
	}
	for _, attachment := range attachments {
		contentType := attachment.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		var created graphAttachment
		if _, err := client.api.DoJSON(
			ctx,
			http.MethodPost,
			"me/messages/"+escaped(draft.ID)+"/attachments",
			nil,
			map[string]any{
				"@odata.type": "#microsoft.graph.fileAttachment",
				"name":        attachment.Name,
				"contentType": contentType,
				"contentBytes": base64.StdEncoding.EncodeToString(
					attachment.Content,
				),
			},
			&created,
			true,
			nil,
			http.StatusCreated,
		); err != nil {
			return graphMessage{}, graphDraftAssemblyError(err)
		}
		if created.ODataType != "#microsoft.graph.fileAttachment" ||
			!validGraphID(created.ID) ||
			created.Name != attachment.Name ||
			created.Size != len(attachment.Content) {
			return graphMessage{}, graphDraftAssemblyError(errors.New(
				"graph attachment response omitted reviewed identity",
			))
		}
	}
	updated, err := client.getMessage(ctx, draft.ID, false)
	if err != nil {
		return graphMessage{}, graphDraftAssemblyError(err)
	}
	if updated.ID != draft.ID || !validETag(updated.ODataETag) {
		return graphMessage{}, graphDraftAssemblyError(errors.New(
			"graph assembled draft omitted current identity",
		))
	}
	return updated, nil
}

func graphDraftAssemblyError(err error) error {
	if errors.Is(err, application.ErrWriteOutcomeUnknown) {
		return err
	}
	return fmt.Errorf(
		"%w: graph draft exists but attachment assembly failed: %w",
		application.ErrWriteOutcomeUnknown,
		err,
	)
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
	if len(input.Attachments) != 0 {
		draft, err := client.createNewDraft(ctx, draftInput)
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
	return client.assembleDraft(ctx, response, input.Attachments)
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
