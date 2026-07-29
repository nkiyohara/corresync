package googleapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/nkiyohara/corresync/internal/application"
)

type gmailComposition struct {
	To, CC, BCC []string
	Subject     string
	Body        string
	BodyFormat  application.MailBodyFormat
	Attachments []application.MailFileAttachment
	ThreadID    string
	InReplyTo   string
	References  string
}

func (client *Client) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	composition, err := client.composition(ctx, input)
	if err != nil {
		return application.MailDraft{}, err
	}
	raw, err := buildGmailMessage(client.address, composition)
	if err != nil {
		return application.MailDraft{}, err
	}
	request := map[string]any{
		"message": map[string]any{
			"raw": base64.RawURLEncoding.EncodeToString(raw),
		},
	}
	if composition.ThreadID != "" {
		request["message"].(map[string]any)["threadId"] = composition.ThreadID
	}
	var response struct {
		ID      string       `json:"id"`
		Message gmailMessage `json:"message"`
	}
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, "gmail/v1/users/me/drafts", nil,
		request, &response, true, nil, http.StatusOK,
	); err != nil {
		return application.MailDraft{}, err
	}
	if !validGoogleID(response.Message.ID) ||
		!validGoogleID(response.Message.HistoryID) {
		return application.MailDraft{}, fmt.Errorf(
			"%w: Gmail draft response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeMessageID(response.Message.ID)
	if err != nil {
		return application.MailDraft{}, err
	}
	return application.MailDraft{
		ID: id, ChangeKey: encodeHistoryID(response.Message.HistoryID),
	}, nil
}

func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	composition, err := client.composition(ctx, application.MailDraftInput{
		Account: input.Account, To: input.To, CC: input.CC, BCC: input.BCC,
		Subject: input.Subject, Body: input.Body, BodyFormat: input.BodyFormat,
		ComposeMode: input.ComposeMode, ReferenceMessageID: input.ReferenceMessageID,
		ReferenceChangeKey: input.ReferenceChangeKey,
		Attachments:        append([]application.MailFileAttachment(nil), input.Attachments...),
	})
	if err != nil {
		return application.MailSendResult{}, err
	}
	raw, err := buildGmailMessage(client.address, composition)
	if err != nil {
		return application.MailSendResult{}, err
	}
	request := map[string]any{"raw": base64.RawURLEncoding.EncodeToString(raw)}
	if composition.ThreadID != "" {
		request["threadId"] = composition.ThreadID
	}
	var response gmailMessage
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost, "gmail/v1/users/me/messages/send", nil,
		request, &response, true, nil, http.StatusOK,
	); err != nil {
		return application.MailSendResult{}, err
	}
	if !validGoogleID(response.ID) || !validGoogleID(response.HistoryID) {
		return application.MailSendResult{}, fmt.Errorf(
			"%w: Gmail send response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	id, err := encodeMessageID(response.ID)
	if err != nil {
		return application.MailSendResult{}, err
	}
	return application.MailSendResult{
		ID: id, ChangeKey: encodeHistoryID(response.HistoryID),
	}, nil
}

func (client *Client) composition(
	ctx context.Context,
	input application.MailDraftInput,
) (gmailComposition, error) {
	result := gmailComposition{
		To: append([]string(nil), input.To...), CC: append([]string(nil), input.CC...),
		BCC: append([]string(nil), input.BCC...), Subject: input.Subject,
		Body: input.Body, BodyFormat: input.EffectiveBodyFormat(),
		Attachments: append([]application.MailFileAttachment(nil), input.Attachments...),
	}
	mode := input.EffectiveComposeMode()
	if mode == application.MailComposeNew {
		return result, nil
	}
	reference, err := decodeMessageID(input.ReferenceMessageID)
	if err != nil {
		return gmailComposition{}, err
	}
	message, err := client.requireMessage(
		ctx, reference.ID, input.ReferenceChangeKey, "full",
	)
	if err != nil {
		return gmailComposition{}, err
	}
	result.ThreadID = message.ThreadID
	result.InReplyTo = headerValue(message.Payload.Headers, "Message-ID")
	result.References = strings.TrimSpace(
		headerValue(message.Payload.Headers, "References") + " " + result.InReplyTo,
	)
	switch mode {
	case application.MailComposeNew:
		return result, nil
	case application.MailComposeReply:
		result.To, err = gmailAddresses(
			gmailReplyTarget(message.Payload.Headers),
			client.address,
		)
	case application.MailComposeReplyAll:
		result.To, err = gmailAddresses(
			gmailReplyTarget(message.Payload.Headers)+","+
				headerValue(message.Payload.Headers, "To"),
			client.address,
		)
		if err == nil {
			result.CC, err = gmailAddresses(
				headerValue(message.Payload.Headers, "Cc"),
				client.address,
			)
			result.CC = gmailAddressesExcluding(result.CC, result.To)
		}
	case application.MailComposeForward:
		body, err := client.GetMessageBody(ctx, application.MailBodyInput{
			MessageID: input.ReferenceMessageID,
		})
		if err != nil {
			return gmailComposition{}, err
		}
		if body.ChangeKey != input.ReferenceChangeKey {
			return gmailComposition{}, errors.New(
				"gmail message changed while composing the forward",
			)
		}
		if body.Text != "" {
			if result.Body != "" {
				result.Body += "\n\n"
			}
			result.Body += "---------- Forwarded message ----------\n" + body.Text
		}
	default:
		return gmailComposition{}, errors.New("unsupported Gmail compose mode")
	}
	if err != nil {
		return gmailComposition{}, err
	}
	if mode == application.MailComposeReply ||
		mode == application.MailComposeReplyAll {
		count := len(result.To) + len(result.CC)
		if count == 0 {
			return gmailComposition{}, errors.New(
				"gmail reference message has no valid reply recipient",
			)
		}
		if count > application.MaxMailRecipients {
			return gmailComposition{}, fmt.Errorf(
				"gmail reply has %d recipients; maximum is %d",
				count,
				application.MaxMailRecipients,
			)
		}
	}
	if result.Subject == "" {
		source := headerValue(message.Payload.Headers, "Subject")
		prefix := "Re: "
		if mode == application.MailComposeForward {
			prefix = "Fwd: "
		}
		if strings.HasPrefix(strings.ToLower(source), strings.ToLower(prefix)) {
			result.Subject = source
		} else {
			result.Subject = prefix + source
		}
	}
	if len(result.Body) > application.MaxMailDraftBodyBytes {
		return gmailComposition{}, errors.New("composed Gmail body exceeds the limit")
	}
	return result, nil
}

func gmailReplyTarget(headers []gmailHeader) string {
	if value := headerValue(headers, "Reply-To"); strings.TrimSpace(value) != "" {
		return value
	}
	return headerValue(headers, "From")
}

func gmailAddresses(raw, exclude string) ([]string, error) {
	raw = strings.Trim(raw, " ,")
	if raw == "" {
		return nil, nil
	}
	values, err := mail.ParseAddressList(raw)
	if err != nil {
		return nil, errors.New("gmail message contains malformed address headers")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(value.Address)
		if strings.EqualFold(value.Address, exclude) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value.Address)
	}
	return result, nil
}

func gmailAddressesExcluding(
	values, excluded []string,
) []string {
	seen := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		seen[strings.ToLower(value)] = struct{}{}
	}
	return slices.DeleteFunc(values, func(value string) bool {
		_, exists := seen[strings.ToLower(value)]
		return exists
	})
}

func (client *Client) requireMessage(
	ctx context.Context,
	id, changeKey, format string,
) (gmailMessage, error) {
	expected, err := decodeHistoryID(changeKey)
	if err != nil {
		return gmailMessage{}, err
	}
	message, err := client.getMessage(ctx, id, format)
	if err != nil {
		return gmailMessage{}, err
	}
	if message.HistoryID != expected {
		return gmailMessage{}, errors.New("gmail message changed before write")
	}
	return message, nil
}

func (client *Client) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	message, err := client.requireMessage(
		ctx, reference.ID, input.ChangeKey, "metadata",
	)
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	unread := slices.Contains(message.LabelIDs, "UNREAD")
	wantUnread := input.State == application.MailReadStateUnread
	if unread == wantUnread {
		return application.MailReadStateResult{
			ID: input.MessageID, ChangeKey: input.ChangeKey, State: input.State,
		}, nil
	}
	request := map[string]any{}
	if wantUnread {
		request["addLabelIds"] = []string{"UNREAD"}
	} else {
		request["removeLabelIds"] = []string{"UNREAD"}
	}
	updated, err := client.modifyMessage(ctx, reference.ID, request)
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	if slices.Contains(updated.LabelIDs, "UNREAD") != wantUnread {
		return application.MailReadStateResult{}, fmt.Errorf(
			"%w: Gmail read-state response did not contain the requested state",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.MailReadStateResult{
		ID: input.MessageID, ChangeKey: encodeHistoryID(updated.HistoryID),
		State: input.State,
	}, nil
}

func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	message, err := client.requireMessage(
		ctx, reference.ID, input.ChangeKey, "metadata",
	)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	resource := "gmail/v1/users/me/messages/" + escaped(reference.ID)
	var updated gmailMessage
	if input.Destination.Kind == application.MailFolderDistinguished &&
		strings.EqualFold(input.Destination.ID, "deleteditems") {
		_, err = client.api.DoJSON(
			ctx, http.MethodPost, resource+"/trash", nil,
			map[string]any{}, &updated, true, nil, http.StatusOK,
		)
	} else {
		add, remove, resolveErr := client.gmailMoveLabels(
			ctx, input.Destination, message.LabelIDs,
		)
		if resolveErr != nil {
			return application.MailMoveResult{}, resolveErr
		}
		untrashed := false
		if slices.Contains(message.LabelIDs, "TRASH") {
			if _, err = client.api.DoJSON(
				ctx, http.MethodPost, resource+"/untrash", nil,
				map[string]any{}, &message, true, nil, http.StatusOK,
			); err != nil {
				return application.MailMoveResult{}, err
			}
			untrashed = true
			if err := validateModifiedGmailMessage(reference.ID, message); err != nil {
				return application.MailMoveResult{}, err
			}
		}
		if len(add) == 0 && len(remove) == 0 {
			updated = message
		} else {
			updated, err = client.modifyMessage(ctx, reference.ID, map[string]any{
				"addLabelIds": add, "removeLabelIds": remove,
			})
			if err != nil && untrashed {
				return application.MailMoveResult{}, fmt.Errorf(
					"%w: Gmail removed the message from Trash but did not confirm the destination label update: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
		}
	}
	if err != nil {
		return application.MailMoveResult{}, err
	}
	if err := validateModifiedGmailMessage(reference.ID, updated); err != nil {
		return application.MailMoveResult{}, err
	}
	return application.MailMoveResult{
		ID: input.MessageID, ChangeKey: encodeHistoryID(updated.HistoryID),
	}, nil
}

func (client *Client) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
) error {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return err
	}
	if _, err := client.requireMessage(
		ctx, reference.ID, input.ChangeKey, "metadata",
	); err != nil {
		return err
	}
	_, err = client.api.DoJSON(
		ctx, http.MethodDelete,
		"gmail/v1/users/me/messages/"+escaped(reference.ID),
		nil, nil, nil, true, nil, http.StatusNoContent,
	)
	return err
}

func (client *Client) modifyMessage(
	ctx context.Context,
	id string,
	request map[string]any,
) (gmailMessage, error) {
	var response gmailMessage
	if _, err := client.api.DoJSON(
		ctx, http.MethodPost,
		"gmail/v1/users/me/messages/"+escaped(id)+"/modify",
		nil, request, &response, true, nil, http.StatusOK,
	); err != nil {
		return gmailMessage{}, err
	}
	if err := validateModifiedGmailMessage(id, response); err != nil {
		return gmailMessage{}, err
	}
	return response, nil
}

func validateModifiedGmailMessage(id string, message gmailMessage) error {
	if message.ID != id || !validGoogleID(message.ID) ||
		!validGoogleID(message.HistoryID) {
		return fmt.Errorf(
			"%w: Gmail write response omitted message identity",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return nil
}

func (client *Client) gmailMoveLabels(
	ctx context.Context,
	destination application.MailFolder,
	current []string,
) (add, remove []string, err error) {
	if destination.Kind == application.MailFolderDistinguished {
		switch strings.ToLower(destination.ID) {
		case "inbox":
			add = []string{"INBOX"}
			remove = presentLabels(current, "SPAM")
		case "archive":
			remove = presentLabels(current, "INBOX", "SPAM")
		case "deleteditems":
			return nil, nil, errors.New("gmail Trash must use the trash action")
		case "drafts", "sentitems":
			return nil, nil, fmt.Errorf(
				"gmail does not permit moving a message into %s",
				destination.ID,
			)
		default:
			return nil, nil, fmt.Errorf(
				"unsupported Gmail move destination %q",
				destination.ID,
			)
		}
		return compactLabelChanges(add, remove, current)
	}
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(destination.ID, "ggl1_", &reference); err != nil ||
		!validGoogleID(reference.ID) {
		return nil, nil, errors.New("folder ID is not a Google label identifier")
	}
	values := url.Values{"fields": {"id,type"}}
	var label gmailLabel
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "gmail/v1/users/me/labels/"+escaped(reference.ID),
		values, nil, &label, false, nil, http.StatusOK,
	); err != nil {
		return nil, nil, err
	}
	if label.ID != reference.ID || label.Type != "user" {
		return nil, nil, errors.New(
			"gmail move destination is not a writable user label",
		)
	}
	add = []string{reference.ID}
	remove = presentLabels(current, "INBOX", "SPAM")
	return compactLabelChanges(add, remove, current)
}

func compactLabelChanges(
	add, remove, current []string,
) ([]string, []string, error) {
	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		currentSet[label] = struct{}{}
	}
	add = slices.DeleteFunc(add, func(label string) bool {
		_, exists := currentSet[label]
		return exists
	})
	remove = slices.DeleteFunc(remove, func(label string) bool {
		_, exists := currentSet[label]
		return !exists
	})
	if len(add) > 100 || len(remove) > 100 {
		return nil, nil, errors.New("gmail label update exceeds the limit")
	}
	return add, remove, nil
}

func presentLabels(current []string, labels ...string) []string {
	result := make([]string, 0, len(labels))
	for _, label := range labels {
		if slices.Contains(current, label) {
			result = append(result, label)
		}
	}
	return result
}

func buildGmailMessage(
	sender string,
	composition gmailComposition,
) ([]byte, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	messageID := fmt.Sprintf(
		"<%s@corresync.invalid>",
		base64.RawURLEncoding.EncodeToString(random),
	)
	var body bytes.Buffer
	headers := textproto.MIMEHeader{
		"From":         []string{sender},
		"To":           []string{strings.Join(composition.To, ", ")},
		"Cc":           []string{strings.Join(composition.CC, ", ")},
		"Bcc":          []string{strings.Join(composition.BCC, ", ")},
		"Subject":      []string{mime.QEncoding.Encode("utf-8", composition.Subject)},
		"Date":         []string{time.Now().UTC().Format(time.RFC1123Z)},
		"Message-Id":   []string{messageID},
		"MIME-Version": []string{"1.0"},
	}
	if composition.InReplyTo != "" {
		headers.Set("In-Reply-To", composition.InReplyTo)
	}
	if composition.References != "" {
		headers.Set("References", composition.References)
	}
	for _, name := range []string{
		"From", "To", "Cc", "Bcc", "Subject", "Date", "Message-Id",
		"In-Reply-To", "References", "MIME-Version",
	} {
		if value := headers.Get(name); value != "" {
			_, _ = fmt.Fprintf(&body, "%s: %s\r\n", name, value)
		}
	}
	if len(composition.Attachments) == 0 {
		contentType := "text/plain; charset=utf-8"
		if composition.BodyFormat == application.MailBodyHTML {
			contentType = "text/html; charset=utf-8"
		}
		_, _ = fmt.Fprintf(
			&body,
			"Content-Type: %s\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
			contentType,
		)
		writer := quotedprintable.NewWriter(&body)
		_, err := writer.Write([]byte(composition.Body))
		return body.Bytes(), errors.Join(err, writer.Close())
	}
	multipartWriter := multipart.NewWriter(&body)
	_, _ = fmt.Fprintf(
		&body,
		"Content-Type: multipart/mixed; boundary=%q\r\n\r\n",
		multipartWriter.Boundary(),
	)
	bodyHeader := textproto.MIMEHeader{
		"Content-Type":              []string{"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": []string{"quoted-printable"},
	}
	if composition.BodyFormat == application.MailBodyHTML {
		bodyHeader.Set("Content-Type", "text/html; charset=utf-8")
	}
	bodyPart, err := multipartWriter.CreatePart(bodyHeader)
	if err != nil {
		return nil, err
	}
	quoted := quotedprintable.NewWriter(bodyPart)
	if _, err := quoted.Write([]byte(composition.Body)); err != nil {
		return nil, err
	}
	if err := quoted.Close(); err != nil {
		return nil, err
	}
	for _, attachment := range composition.Attachments {
		contentType := attachment.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		part, err := multipartWriter.CreatePart(textproto.MIMEHeader{
			"Content-Type": []string{contentType},
			"Content-Disposition": []string{
				fmt.Sprintf("attachment; filename=%q", attachment.Name),
			},
			"Content-Transfer-Encoding": []string{"base64"},
		})
		if err != nil {
			return nil, err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := encoder.Write(attachment.Content); err != nil {
			return nil, err
		}
		if err := encoder.Close(); err != nil {
			return nil, err
		}
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

func appendBoundedText(builder *strings.Builder, value string) error {
	separator := 0
	if builder.Len() != 0 {
		separator = 1
	}
	if len(value) > application.MaxMailBodyBytes-builder.Len()-separator {
		return errors.New("gmail body exceeds the configured limit")
	}
	if separator != 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(value)
	return nil
}

func htmlText(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	root, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return "", errors.New("gmail HTML body is malformed")
	}
	var builder strings.Builder
	var walkErr error
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if walkErr != nil {
			return
		}
		if node.Type == html.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" {
				walkErr = appendBoundedText(&builder, text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return builder.String(), walkErr
}
