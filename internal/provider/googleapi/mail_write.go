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
			headerValue(message.Payload.Headers, "Reply-To")+","+
				headerValue(message.Payload.Headers, "From"),
			client.address,
		)
	case application.MailComposeReplyAll:
		result.To, err = gmailAddresses(
			headerValue(message.Payload.Headers, "Reply-To")+","+
				headerValue(message.Payload.Headers, "From")+","+
				headerValue(message.Payload.Headers, "To"),
			client.address,
		)
		if err == nil {
			result.CC, err = gmailAddresses(
				headerValue(message.Payload.Headers, "Cc"),
				client.address,
			)
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
	return application.MailReadStateResult{}, errors.New(
		"gmail does not expose an atomic historyId precondition for message label updates",
	)
}

func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	return application.MailMoveResult{}, errors.New(
		"gmail does not expose an atomic historyId precondition for message moves",
	)
}

func (*Client) DeleteMail(
	context.Context,
	application.MailDeleteInput,
) error {
	return errors.New(
		"permanent Gmail deletion requires a broader mail.google.com grant; move to Trash instead",
	)
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
