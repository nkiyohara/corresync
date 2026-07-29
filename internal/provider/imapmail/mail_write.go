package imapmail

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
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/emersion/go-smtp"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	composition, err := client.compose(ctx, input)
	if err != nil {
		return application.MailDraft{}, err
	}
	raw, messageID, err := client.buildMessage(composition)
	if err != nil {
		return application.MailDraft{}, err
	}
	id, changeKey, err := client.appendMessage(
		ctx,
		application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "drafts",
		},
		[]string{imap.DraftFlag, imap.SeenFlag},
		raw,
		messageID,
		"draft",
	)
	if err != nil {
		return application.MailDraft{}, err
	}
	return application.MailDraft{ID: id, ChangeKey: changeKey}, nil
}

func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	draft := application.MailDraftInput{
		Account: input.Account, To: input.To, CC: input.CC, BCC: input.BCC,
		Subject: input.Subject, Body: input.Body, BodyFormat: input.BodyFormat,
		ComposeMode: input.ComposeMode, ReferenceMessageID: input.ReferenceMessageID,
		ReferenceChangeKey: input.ReferenceChangeKey,
		Attachments:        append([]application.MailFileAttachment(nil), input.Attachments...),
	}
	composition, err := client.compose(ctx, draft)
	if err != nil {
		return application.MailSendResult{}, err
	}
	raw, messageID, err := client.buildMessage(composition)
	if err != nil {
		return application.MailSendResult{}, err
	}
	if !client.observed.Sent {
		return application.MailSendResult{}, errors.New(
			"IMAP Sent mailbox is unavailable; SMTP submission was not attempted",
		)
	}
	recipients := append(append(
		append([]string(nil), composition.To...),
		composition.CC...,
	), composition.BCC...)
	err = client.withSMTP(ctx, func(connection *smtp.Client) error {
		if err := connection.Mail(client.sender, nil); err != nil {
			return err
		}
		for _, recipient := range recipients {
			if err := connection.Rcpt(recipient, nil); err != nil {
				return err
			}
		}
		writer, err := connection.Data()
		if err != nil {
			return err
		}
		if _, err := writer.Write(raw); err != nil {
			_ = writer.Close()
			return fmt.Errorf("%w: write SMTP DATA: %w", application.ErrWriteOutcomeUnknown, err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("%w: finish SMTP DATA: %w", application.ErrWriteOutcomeUnknown, err)
		}
		return nil
	})
	if err != nil {
		return application.MailSendResult{}, err
	}
	id, changeKey, err := client.appendMessage(
		ctx,
		application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "sentitems",
		},
		[]string{imap.SeenFlag},
		raw,
		messageID,
		"sent message",
	)
	if err != nil {
		return application.MailSendResult{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			fmt.Errorf(
				"SMTP accepted the message but its IMAP Sent copy was not confirmed: %w",
				err,
			),
		)
	}
	return application.MailSendResult{ID: id, ChangeKey: changeKey}, nil
}

func (client *Client) appendMessage(
	ctx context.Context,
	folder application.MailFolder,
	flags []string,
	raw []byte,
	messageID string,
	kind string,
) (id, changeKey string, returnErr error) {
	returnErr = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		mailbox, err := client.resolveMailbox(connection, folder)
		if err != nil {
			return err
		}
		if err := connection.Append(
			mailbox,
			flags,
			time.Now(),
			bytes.NewReader(raw),
		); err != nil {
			return fmt.Errorf(
				"%w: append IMAP %s: %w",
				application.ErrWriteOutcomeUnknown,
				kind,
				err,
			)
		}
		status, err := connection.Select(mailbox, true)
		if err != nil {
			return err
		}
		criteria := imap.NewSearchCriteria()
		criteria.Header = textproto.MIMEHeader{
			"Message-Id": []string{messageID},
		}
		uids, err := connection.UidSearch(criteria)
		if err != nil {
			return err
		}
		if len(uids) != 1 {
			return fmt.Errorf(
				"%w: appended IMAP %s could not be identified uniquely",
				application.ErrWriteOutcomeUnknown,
				kind,
			)
		}
		messages, err := fetchUIDs(connection, uids, metadataItems)
		if err != nil {
			return err
		}
		if len(messages) != 1 {
			return fmt.Errorf(
				"%w: appended IMAP %s metadata was omitted",
				application.ErrWriteOutcomeUnknown,
				kind,
			)
		}
		id, err = encodeMessageID(messageReference{
			Mailbox: mailbox, UIDValidity: status.UidValidity, UID: uids[0],
		})
		if err != nil {
			return err
		}
		changeKey = snapshot(status, messages[0])
		return nil
	})
	return id, changeKey, returnErr
}

func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	var result application.MailMoveResult
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		_, err := requireState(connection, reference, input.ChangeKey)
		if err != nil {
			return err
		}
		supported, err := connection.Support("MOVE")
		if err != nil {
			return err
		}
		destination, err := client.resolveMailbox(connection, input.Destination)
		if err != nil {
			return err
		}
		set := new(imap.SeqSet)
		set.AddNum(reference.UID)
		if supported {
			if err := connection.UidMove(set, destination); err != nil {
				return fmt.Errorf(
					"%w: execute IMAP MOVE: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
		} else {
			uidPlus, err := connection.Support("UIDPLUS")
			if err != nil {
				return err
			}
			if !uidPlus {
				return errors.New(
					"IMAP MOVE and UIDPLUS are unavailable; safe move fallback is disabled",
				)
			}
			if err := connection.UidCopy(set, destination); err != nil {
				return fmt.Errorf(
					"%w: execute IMAP UID COPY move fallback: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
			if err := connection.UidStore(
				set,
				imap.FormatFlagsOp(imap.AddFlags, true),
				[]interface{}{imap.DeletedFlag},
				nil,
			); err != nil {
				return fmt.Errorf(
					"%w: mark IMAP move source deleted: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
			status, err := connection.Execute(&imap.Command{
				Name: "UID",
				Arguments: []interface{}{
					imap.RawString("EXPUNGE"),
					set,
				},
			}, nil)
			if err != nil {
				return fmt.Errorf(
					"%w: execute IMAP UID EXPUNGE move fallback: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
			if err := status.Err(); err != nil {
				return fmt.Errorf(
					"%w: execute IMAP UID EXPUNGE move fallback: %w",
					application.ErrWriteOutcomeUnknown,
					err,
				)
			}
		}
		result = application.MailMoveResult{}
		return nil
	})
	return result, err
}

func (client *Client) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	var result application.MailReadStateResult
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		status, err := requireState(connection, reference, input.ChangeKey)
		if err != nil {
			return err
		}
		set := new(imap.SeqSet)
		set.AddNum(reference.UID)
		var operation imap.FlagsOp = imap.AddFlags
		if input.State == application.MailReadStateUnread {
			operation = imap.RemoveFlags
		}
		if err := connection.UidStore(
			set,
			imap.FormatFlagsOp(operation, true),
			[]interface{}{imap.SeenFlag},
			nil,
		); err != nil {
			return err
		}
		messages, err := fetchUIDs(connection, []uint32{reference.UID}, metadataItems)
		if err != nil {
			return err
		}
		if len(messages) != 1 {
			return errors.New("IMAP updated message metadata was omitted")
		}
		result = application.MailReadStateResult{
			ID: input.MessageID, ChangeKey: snapshot(status, messages[0]),
			State: input.State,
		}
		return nil
	})
	return result, err
}

func (client *Client) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
) error {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return err
	}
	return client.withIMAP(ctx, func(connection *imapclient.Client) error {
		if _, err := requireState(connection, reference, input.ChangeKey); err != nil {
			return err
		}
		supported, err := connection.Support("UIDPLUS")
		if err != nil {
			return err
		}
		if !supported {
			return errors.New(
				"IMAP UIDPLUS is unavailable; unsafe mailbox-wide expunge is disabled",
			)
		}
		set := new(imap.SeqSet)
		set.AddNum(reference.UID)
		if err := connection.UidStore(
			set,
			imap.FormatFlagsOp(imap.AddFlags, true),
			[]interface{}{imap.DeletedFlag},
			nil,
		); err != nil {
			return err
		}
		status, err := connection.Execute(&imap.Command{
			Name: "UID",
			Arguments: []interface{}{
				imap.RawString("EXPUNGE"),
				set,
			},
		}, nil)
		if err != nil {
			return fmt.Errorf("%w: execute UID EXPUNGE: %w", application.ErrWriteOutcomeUnknown, err)
		}
		if err := status.Err(); err != nil {
			return fmt.Errorf("%w: execute UID EXPUNGE: %w", application.ErrWriteOutcomeUnknown, err)
		}
		return nil
	})
}

func requireState(
	connection *imapclient.Client,
	reference messageReference,
	expected string,
) (*imap.MailboxStatus, error) {
	status, err := connection.Select(reference.Mailbox, false)
	if err != nil {
		return nil, err
	}
	if status.UidValidity != reference.UIDValidity {
		return nil, errors.New("IMAP UIDVALIDITY changed")
	}
	messages, err := fetchUIDs(connection, []uint32{reference.UID}, metadataItems)
	if err != nil {
		return nil, err
	}
	if len(messages) != 1 || snapshot(status, messages[0]) != expected {
		return nil, errors.New("IMAP message changed or was not found")
	}
	return status, nil
}

type mailComposition struct {
	To, CC, BCC []string
	Subject     string
	Body        string
	BodyFormat  application.MailBodyFormat
	Attachments []application.MailFileAttachment
	InReplyTo   string
	References  string
}

const (
	maximumReferencesBytes = 8 << 10
	maximumReferenceIDs    = 100
)

func (client *Client) compose(
	ctx context.Context,
	input application.MailDraftInput,
) (mailComposition, error) {
	result := mailComposition{
		To: append([]string(nil), input.To...), CC: append([]string(nil), input.CC...),
		BCC: append([]string(nil), input.BCC...), Subject: input.Subject,
		Body: input.Body, BodyFormat: input.EffectiveBodyFormat(),
		Attachments: append([]application.MailFileAttachment(nil), input.Attachments...),
	}
	if input.EffectiveComposeMode() == application.MailComposeNew {
		return result, nil
	}
	reference, err := decodeMessageID(input.ReferenceMessageID)
	if err != nil {
		return mailComposition{}, err
	}
	var parsed parsedMIME
	var envelope *imap.Envelope
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		status, message, raw, err := fetchRawMessage(connection, reference)
		if err != nil {
			return err
		}
		if snapshot(status, message) != input.ReferenceChangeKey {
			return errors.New("IMAP reference message changed")
		}
		envelope = message.Envelope
		parsed, err = parseMIME(raw)
		return err
	})
	if err != nil {
		return mailComposition{}, err
	}
	if envelope == nil {
		return mailComposition{}, errors.New("IMAP reference envelope is missing")
	}
	result.InReplyTo, result.References = inheritedReplyHeaders(
		input.EffectiveComposeMode(),
		envelope.MessageId,
		parsed.Header.Get("References"),
	)
	switch input.EffectiveComposeMode() {
	case application.MailComposeNew:
		return result, nil
	case application.MailComposeReply:
		result.To, err = derivedIMAPAddresses(
			imapReplyTarget(envelope),
			nil,
			client.sender,
		)
	case application.MailComposeReplyAll:
		result.To, err = derivedIMAPAddresses(
			imapReplyTarget(envelope),
			envelope.To,
			client.sender,
		)
		if err == nil {
			result.CC, err = derivedIMAPAddresses(
				envelope.Cc,
				nil,
				client.sender,
			)
			result.CC = imapAddressesExcluding(result.CC, result.To)
		}
	case application.MailComposeForward:
		if parsed.Text != "" {
			if result.Body != "" {
				result.Body += "\n\n"
			}
			result.Body += "---------- Forwarded message ----------\n" + parsed.Text
		}
	default:
		return mailComposition{}, errors.New("unsupported IMAP compose mode")
	}
	if err != nil {
		return mailComposition{}, err
	}
	if input.EffectiveComposeMode() == application.MailComposeReply ||
		input.EffectiveComposeMode() == application.MailComposeReplyAll {
		count := len(result.To) + len(result.CC)
		if count == 0 {
			return mailComposition{}, errors.New(
				"IMAP reference message has no valid reply recipient",
			)
		}
		if count > application.MaxMailRecipients {
			return mailComposition{}, fmt.Errorf(
				"IMAP reply has %d recipients; maximum is %d",
				count,
				application.MaxMailRecipients,
			)
		}
	}
	if result.Subject == "" {
		prefix := "Re: "
		if input.EffectiveComposeMode() == application.MailComposeForward {
			prefix = "Fwd: "
		}
		if strings.HasPrefix(strings.ToLower(envelope.Subject), strings.ToLower(prefix)) {
			result.Subject = envelope.Subject
		} else {
			result.Subject = prefix + envelope.Subject
		}
	}
	if len(result.Body) > application.MaxMailDraftBodyBytes {
		return mailComposition{}, fmt.Errorf(
			"composed mail body exceeds %d bytes",
			application.MaxMailDraftBodyBytes,
		)
	}
	return result, nil
}

func imapReplyTarget(envelope *imap.Envelope) []*imap.Address {
	if len(envelope.ReplyTo) != 0 {
		return envelope.ReplyTo
	}
	return envelope.From
}

func derivedIMAPAddresses(
	primary, secondary []*imap.Address,
	exclude string,
) ([]string, error) {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	result := make([]string, 0, len(primary)+len(secondary))
	for _, address := range append(append([]*imap.Address{}, primary...), secondary...) {
		if address == nil {
			return nil, errors.New(
				"IMAP reference envelope contains a malformed recipient address",
			)
		}
		value := address.Address()
		key := strings.ToLower(value)
		if !bareAddress(value) {
			return nil, errors.New(
				"IMAP reference envelope contains a malformed recipient address",
			)
		}
		if strings.EqualFold(value, exclude) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func imapAddressesExcluding(
	values, excluded []string,
) []string {
	seen := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		seen[strings.ToLower(value)] = struct{}{}
	}
	result := values[:0]
	for _, value := range values {
		if _, exists := seen[strings.ToLower(value)]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func (client *Client) buildMessage(
	composition mailComposition,
) ([]byte, string, error) {
	if composition.InReplyTo != "" {
		normalized, err := normalizeMessageID(composition.InReplyTo)
		if err != nil || normalized != composition.InReplyTo {
			return nil, "", errors.New("mail In-Reply-To value is malformed")
		}
	}
	if composition.References != "" {
		normalized, err := normalizeReferences(composition.References, "")
		if err != nil || normalized != composition.References {
			return nil, "", errors.New("mail References value is malformed")
		}
	}
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	messageID := fmt.Sprintf(
		"<%s@corresync.invalid>",
		base64.RawURLEncoding.EncodeToString(random),
	)
	var body bytes.Buffer
	headers := textproto.MIMEHeader{
		"From":         []string{client.sender},
		"To":           []string{strings.Join(composition.To, ", ")},
		"Cc":           []string{strings.Join(composition.CC, ", ")},
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
		"From", "To", "Cc", "Subject", "Date", "Message-Id",
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
		return body.Bytes(), messageID, errors.Join(err, writer.Close())
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
		return nil, "", err
	}
	quoted := quotedprintable.NewWriter(bodyPart)
	if _, err := quoted.Write([]byte(composition.Body)); err != nil {
		return nil, "", err
	}
	if err := quoted.Close(); err != nil {
		return nil, "", err
	}
	for _, attachment := range composition.Attachments {
		contentType := attachment.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		header := textproto.MIMEHeader{
			"Content-Type":              []string{contentType},
			"Content-Disposition":       []string{fmt.Sprintf("attachment; filename=%q", attachment.Name)},
			"Content-Transfer-Encoding": []string{"base64"},
		}
		part, err := multipartWriter.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := encoder.Write(attachment.Content); err != nil {
			return nil, "", err
		}
		if err := encoder.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := multipartWriter.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), messageID, nil
}

func normalizeMessageID(value string) (string, error) {
	if len(value) < 5 || len(value) > 998 ||
		value[0] != '<' || value[len(value)-1] != '>' {
		return "", errors.New("message ID must use angle-bracket form")
	}
	inner := value[1 : len(value)-1]
	if !strings.Contains(inner, "@") || strings.ContainsAny(inner, "<>") {
		return "", errors.New("message ID has an invalid identifier")
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return "", errors.New("message ID contains whitespace or a control character")
		}
	}
	return value, nil
}

func normalizeReferences(existing, current string) (string, error) {
	if len(existing)+len(current)+1 > maximumReferencesBytes {
		return "", errors.New("reference chain exceeds the configured limit")
	}
	values := strings.Fields(existing)
	if current != "" {
		values = append(values, current)
	}
	if len(values) > maximumReferenceIDs {
		return "", errors.New("reference chain contains too many message IDs")
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		messageID, err := normalizeMessageID(value)
		if err != nil {
			return "", err
		}
		if _, exists := seen[messageID]; exists {
			continue
		}
		seen[messageID] = struct{}{}
		normalized = append(normalized, messageID)
	}
	return strings.Join(normalized, " "), nil
}

func inheritedReplyHeaders(
	mode application.MailComposeMode,
	messageID string,
	existing string,
) (string, string) {
	if mode == application.MailComposeForward {
		return "", ""
	}
	current, err := normalizeMessageID(messageID)
	if err != nil {
		return "", ""
	}
	return current, inheritValidReferences(existing, current)
}

func inheritValidReferences(existing, current string) string {
	seen := make(map[string]struct{}, maximumReferenceIDs)
	references := make([]string, 0, maximumReferenceIDs)
	total := 0
	reserved := len(current)
	if current != "" {
		reserved++
	}
	for offset := 0; offset < len(existing) &&
		len(references) < maximumReferenceIDs-1; {
		openOffset := strings.IndexByte(existing[offset:], '<')
		if openOffset < 0 {
			break
		}
		open := offset + openOffset
		closeOffset := strings.IndexByte(existing[open+1:], '>')
		if closeOffset < 0 {
			break
		}
		close := open + 1 + closeOffset
		candidate := existing[open : close+1]
		offset = close + 1
		normalized, err := normalizeMessageID(candidate)
		if err != nil {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate ||
			total+len(normalized)+1+reserved > maximumReferencesBytes {
			continue
		}
		seen[normalized] = struct{}{}
		references = append(references, normalized)
		total += len(normalized) + 1
	}
	if current != "" {
		if _, duplicate := seen[current]; !duplicate {
			references = append(references, current)
		}
	}
	return strings.Join(references, " ")
}
