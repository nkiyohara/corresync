package imapmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"

	"github.com/nkiyohara/corresync/internal/application"
)

type exactIMAPDraft struct {
	reference messageReference
	raw       []byte
	parsed    parsedMIME
	to        []string
	cc        []string
	bcc       []string
	messageID string
	changeKey string
}

// GetMailDraftSnapshot fetches immutable UID content and verifies the exact
// flag/version snapshot before it is shown for approval.
func (client *Client) GetMailDraftSnapshot(
	ctx context.Context,
	input application.MailDraftSendInput,
) (application.MailDraftSnapshot, error) {
	if err := input.Validate(); err != nil {
		return application.MailDraftSnapshot{}, err
	}
	if err := client.requireExactDraftSendCapabilities(); err != nil {
		return application.MailDraftSnapshot{}, err
	}
	draft, err := client.exactDraft(ctx, input)
	if err != nil {
		return application.MailDraftSnapshot{}, err
	}
	rawSubject, err := singleDraftHeader(draft.parsed.Header, "Subject")
	if err != nil {
		return application.MailDraftSnapshot{}, err
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(rawSubject)
	if err != nil {
		return application.MailDraftSnapshot{}, errors.New("IMAP draft subject is malformed")
	}
	attachments := make([]application.MailDraftAttachmentSnapshot, 0, len(draft.parsed.Attachments))
	for _, attachment := range draft.parsed.Attachments {
		attachments = append(attachments, application.MailDraftAttachmentSnapshot{
			Name: attachment.Name, ContentType: attachment.ContentType,
			Bytes: len(attachment.Content), Inline: attachment.Inline,
		})
	}
	return application.MailDraftSnapshot{
		ID: input.DraftID, ChangeKey: input.DraftChangeKey,
		To: draft.to, CC: draft.cc, BCC: draft.bcc,
		Subject: subject, Body: draft.parsed.Text,
		BodyFormat: application.MailBodyText, Attachments: attachments,
	}, nil
}

// SendMailDraft submits the exact immutable UID bytes once, stores the sent
// copy, and then removes only that reviewed draft UID. No stage is retried.
func (client *Client) SendMailDraft(
	ctx context.Context,
	input application.MailDraftSendInput,
) (application.MailSendResult, error) {
	if err := input.Validate(); err != nil {
		return application.MailSendResult{}, err
	}
	if err := client.requireExactDraftSendCapabilities(); err != nil {
		return application.MailSendResult{}, err
	}
	draft, err := client.exactDraft(ctx, input)
	if err != nil {
		return application.MailSendResult{}, err
	}
	recipients := append(append(
		append([]string(nil), draft.to...),
		draft.cc...,
	), draft.bcc...)
	if len(recipients) == 0 || len(recipients) > application.MaxMailRecipients {
		return application.MailSendResult{}, errors.New("IMAP saved draft recipient count is invalid")
	}
	outbound, err := removeBlindCopyHeaders(draft.raw)
	if err != nil {
		return application.MailSendResult{}, err
	}
	if err := client.submitMessage(ctx, recipients, outbound); err != nil {
		return application.MailSendResult{}, err
	}
	sent, err := client.storeSentMessage(ctx, outbound, draft.messageID)
	if err != nil {
		return application.MailSendResult{}, err
	}
	if err := client.deleteExactMessage(ctx, draft.reference, draft.changeKey); err != nil {
		return application.MailSendResult{}, errors.Join(
			application.ErrWriteOutcomeUnknown,
			fmt.Errorf("SMTP accepted the message but its exact IMAP draft was not removed: %w", err),
		)
	}
	return sent, nil
}

func (client *Client) requireExactDraftSendCapabilities() error {
	if client.observed.Drafts && client.observed.Sent && client.observed.UIDPlus {
		return nil
	}
	return fmt.Errorf(
		"%w: IMAP Drafts, Sent, and UIDPLUS are required",
		application.ErrExactDraftSendUnavailable,
	)
}

func (client *Client) exactDraft(
	ctx context.Context,
	input application.MailDraftSendInput,
) (exactIMAPDraft, error) {
	reference, err := decodeMessageID(input.DraftID)
	if err != nil {
		return exactIMAPDraft{}, err
	}
	var draft exactIMAPDraft
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		draftsMailbox, err := client.resolveMailbox(ctx, connection, application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "drafts",
		})
		if err != nil {
			return err
		}
		if reference.Mailbox != draftsMailbox {
			return errors.New("IMAP message is not in the selected Drafts mailbox")
		}
		status, message, raw, err := fetchRawMessage(ctx, connection, reference)
		if err != nil {
			return err
		}
		changeKey := snapshot(status, message)
		if changeKey != input.DraftChangeKey {
			return errors.New("IMAP saved draft changed or was not found")
		}
		if !hasFlag(message.Flags, imap.DraftFlag) {
			return errors.New("IMAP message is no longer marked as a draft")
		}
		parsed, err := parseMIME(raw)
		if err != nil {
			return err
		}
		to, err := draftHeaderAddresses(parsed.Header, "To")
		if err != nil {
			return err
		}
		cc, err := draftHeaderAddresses(parsed.Header, "Cc")
		if err != nil {
			return err
		}
		bcc, err := draftHeaderAddresses(parsed.Header, "Bcc")
		if err != nil {
			return err
		}
		from, err := draftHeaderAddresses(parsed.Header, "From")
		if err != nil || len(from) != 1 || !strings.EqualFold(from[0], client.sender) {
			return errors.New("IMAP saved draft sender does not match the configured account")
		}
		rawMessageID, err := singleDraftHeader(parsed.Header, "Message-Id")
		if err != nil {
			return err
		}
		messageID, err := normalizeMessageID(rawMessageID)
		if err != nil {
			return errors.New("IMAP saved draft has no valid Message-ID")
		}
		draft = exactIMAPDraft{
			reference: reference, raw: raw, parsed: parsed,
			to: to, cc: cc, bcc: bcc, messageID: messageID, changeKey: changeKey,
		}
		return nil
	})
	return draft, err
}

func draftHeaderAddresses(header mail.Header, field string) ([]string, error) {
	value, err := singleDraftHeader(header, field)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := mail.ParseAddressList(value)
	if err != nil {
		return nil, fmt.Errorf("IMAP draft %s recipients are malformed", field)
	}
	addresses := make([]string, 0, len(parsed))
	for _, address := range parsed {
		if address == nil || !bareAddress(address.Address) {
			return nil, fmt.Errorf("IMAP draft %s recipients are malformed", field)
		}
		addresses = append(addresses, address.Address)
	}
	return addresses, nil
}

func singleDraftHeader(header mail.Header, field string) (string, error) {
	values := header[textproto.CanonicalMIMEHeaderKey(field)]
	if len(values) > 1 {
		return "", fmt.Errorf("IMAP draft contains more than one %s header", field)
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

func removeBlindCopyHeaders(raw []byte) ([]byte, error) {
	separator, newline := []byte("\r\n\r\n"), []byte("\r\n")
	headerEnd := bytes.Index(raw, separator)
	if headerEnd < 0 {
		separator, newline = []byte("\n\n"), []byte("\n")
		headerEnd = bytes.Index(raw, separator)
	}
	if headerEnd < 0 {
		return nil, errors.New("IMAP draft has malformed message headers")
	}
	lines := bytes.Split(raw[:headerEnd], newline)
	var output bytes.Buffer
	output.Grow(len(raw))
	skipping := false
	for _, line := range lines {
		if len(line) == 0 {
			return nil, errors.New("IMAP draft has malformed message headers")
		}
		if line[0] == ' ' || line[0] == '\t' {
			if skipping {
				continue
			}
		} else {
			colon := bytes.IndexByte(line, ':')
			if colon < 1 {
				return nil, errors.New("IMAP draft has malformed message headers")
			}
			name := string(line[:colon])
			skipping = strings.EqualFold(name, "Bcc") || strings.EqualFold(name, "Resent-Bcc")
			if skipping {
				continue
			}
		}
		output.Write(line)
		output.Write(newline)
	}
	output.Write(newline)
	output.Write(raw[headerEnd+len(separator):])
	return output.Bytes(), nil
}

var _ application.MailDraftSender = (*Client)(nil)
