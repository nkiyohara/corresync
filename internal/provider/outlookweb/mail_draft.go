package outlookweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/application"
)

type createItemEnvelope struct {
	Type   string            `json:"__type"`
	Header requestHeader     `json:"Header"`
	Body   createItemRequest `json:"Body"`
}

type createItemRequest struct {
	Type                     string         `json:"__type"`
	Items                    []any          `json:"Items"`
	ClientSupportsIRM        bool           `json:"ClientSupportsIrm"`
	MessageDisposition       string         `json:"MessageDisposition"`
	SendMeetingInvitations   string         `json:"SendMeetingInvitations"`
	SavedItemFolderID        targetFolderID `json:"SavedItemFolderId"`
	SuppressReadReceipts     bool           `json:"SuppressReadReceipts"`
	ComposeOperation         string         `json:"ComposeOperation"`
	MessageDispositionString string         `json:"MessageDispositionString"`
}

type responseMessage struct {
	Type                       string           `json:"__type"`
	Subject                    string           `json:"Subject,omitempty"`
	NewBodyContent             bodyContent      `json:"NewBodyContent"`
	ReferenceItemID            itemID           `json:"ReferenceItemId"`
	ToRecipients               []draftRecipient `json:"ToRecipients,omitempty"`
	CCRecipients               []draftRecipient `json:"CcRecipients,omitempty"`
	BCCRecipients              []draftRecipient `json:"BccRecipients,omitempty"`
	IsDeliveryReceiptRequested bool             `json:"IsDeliveryReceiptRequested"`
	IsReadReceiptRequested     bool             `json:"IsReadReceiptRequested"`
}

type draftMessage struct {
	Type                       string           `json:"__type"`
	Subject                    string           `json:"Subject"`
	Body                       bodyContent      `json:"Body"`
	ToRecipients               []draftRecipient `json:"ToRecipients"`
	CCRecipients               []draftRecipient `json:"CcRecipients"`
	BCCRecipients              []draftRecipient `json:"BccRecipients"`
	Importance                 string           `json:"Importance"`
	Sensitivity                string           `json:"Sensitivity"`
	IsDeliveryReceiptRequested bool             `json:"IsDeliveryReceiptRequested"`
	IsReadReceiptRequested     bool             `json:"IsReadReceiptRequested"`
}

type draftRecipient struct {
	EmailAddress string `json:"EmailAddress"`
	RoutingType  string `json:"RoutingType"`
	MailboxType  string `json:"MailboxType"`
}

type createItemResponseBody struct {
	ResponseMessages responseMessages[createItemResponseMessage] `json:"ResponseMessages"`
}

type createItemResponseMessage struct {
	ResponseClass string             `json:"ResponseClass"`
	ResponseCode  string             `json:"ResponseCode"`
	Items         []createItemResult `json:"Items"`
}

type createItemResult struct {
	ItemID itemID `json:"ItemId"`
}

// CreateMailDraft saves one new-message or response draft without sending it.
func (client *Client) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	if err := input.Validate(application.MaxMailRecipients); err != nil {
		return application.MailDraft{}, err
	}
	payload := buildCreateDraftEnvelope(input)
	var response responseEnvelope[createItemResponseBody]
	if err := client.Call(ctx, CreateItem, payload, &response); err != nil {
		return application.MailDraft{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailDraft{}, classifyPostRequestError(
			CreateItem, errors.New("OWA CreateItem returned an unexpected response count"),
		)
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkWriteResponse(CreateItem, message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailDraft{}, err
	}
	if len(message.Items) != 1 {
		return application.MailDraft{}, classifyPostRequestError(
			CreateItem, errors.New("OWA CreateItem did not return exactly one draft"),
		)
	}
	item := message.Items[0]
	if err := validateOpaqueID("draft", item.ItemID.ID); err != nil {
		return application.MailDraft{}, classifyPostRequestError(
			CreateItem, fmt.Errorf("invalid draft in OWA response: %w", err),
		)
	}
	if item.ItemID.ChangeKey != "" {
		if err := validateOpaqueID("draft change key", item.ItemID.ChangeKey); err != nil {
			return application.MailDraft{}, classifyPostRequestError(
				CreateItem, fmt.Errorf("invalid draft in OWA response: %w", err),
			)
		}
	}
	draft := application.MailDraft{ID: item.ItemID.ID, ChangeKey: item.ItemID.ChangeKey}
	if len(input.Attachments) == 0 {
		return draft, nil
	}
	attached, err := client.createFileAttachments(ctx, draft, input.Attachments)
	if err != nil {
		return application.MailDraft{}, err
	}
	return attached, nil
}

func buildCreateDraftEnvelope(input application.MailDraftInput) createItemEnvelope {
	bodyType := "Text"
	if input.EffectiveBodyFormat() == application.MailBodyHTML {
		bodyType = "HTML"
	}
	composeOperation := "newMail"
	var item any = draftMessage{
		Type:    "Message:#Exchange",
		Subject: input.Subject,
		Body: bodyContent{
			Type: "BodyContentType:#Exchange", BodyType: bodyType, Value: input.Body,
		},
		ToRecipients:               draftRecipients(input.To),
		CCRecipients:               draftRecipients(input.CC),
		BCCRecipients:              draftRecipients(input.BCC),
		Importance:                 "Normal",
		Sensitivity:                "Normal",
		IsDeliveryReceiptRequested: false,
		IsReadReceiptRequested:     false,
	}
	if input.EffectiveComposeMode() != application.MailComposeNew {
		typeName := "ReplyToItem:#Exchange"
		composeOperation = "reply"
		switch input.EffectiveComposeMode() {
		case application.MailComposeNew, application.MailComposeReply:
		case application.MailComposeReplyAll:
			typeName = "ReplyAllToItem:#Exchange"
			composeOperation = "replyAll"
		case application.MailComposeForward:
			typeName = "ForwardItem:#Exchange"
			composeOperation = "forward"
		}
		item = responseMessage{
			Type: typeName, Subject: input.Subject,
			NewBodyContent: bodyContent{
				Type: "BodyContentType:#Exchange", BodyType: bodyType, Value: input.Body,
			},
			ReferenceItemID: itemID{
				Type: "ItemId:#Exchange", ID: input.ReferenceMessageID, ChangeKey: input.ReferenceChangeKey,
			},
			ToRecipients: draftRecipients(input.To), CCRecipients: draftRecipients(input.CC),
			BCCRecipients: draftRecipients(input.BCC),
		}
	}
	return createItemEnvelope{
		Type:   "CreateItemJsonRequest:#Exchange",
		Header: newRequestHeader(defaultZone),
		Body: createItemRequest{
			Type:                   "CreateItemRequest:#Exchange",
			Items:                  []any{item},
			ClientSupportsIRM:      true,
			MessageDisposition:     "SaveOnly",
			SendMeetingInvitations: "SendToNone",
			SavedItemFolderID: targetFolderID{
				Type:         "TargetFolderId:#Exchange",
				BaseFolderID: folderID{Type: "DistinguishedFolderId:#Exchange", ID: "drafts"},
			},
			SuppressReadReceipts:     true,
			ComposeOperation:         composeOperation,
			MessageDispositionString: "SaveOnly",
		},
	}
}

func draftRecipients(addresses []string) []draftRecipient {
	recipients := make([]draftRecipient, 0, len(addresses))
	for _, address := range addresses {
		recipients = append(recipients, draftRecipient{
			EmailAddress: address,
			RoutingType:  "SMTP",
			MailboxType:  "Mailbox",
		})
	}
	return recipients
}
