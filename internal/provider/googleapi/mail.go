package googleapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	AttachmentID string `json:"attachmentId"`
	Size         int    `json:"size"`
	Data         string `json:"data"`
}

type gmailPart struct {
	PartID   string        `json:"partId"`
	MimeType string        `json:"mimeType"`
	Filename string        `json:"filename"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody     `json:"body"`
	Parts    []gmailPart   `json:"parts"`
}

type gmailMessage struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"threadId"`
	LabelIDs     []string  `json:"labelIds"`
	Snippet      string    `json:"snippet"`
	HistoryID    string    `json:"historyId"`
	InternalDate string    `json:"internalDate"`
	SizeEstimate int       `json:"sizeEstimate"`
	Payload      gmailPart `json:"payload"`
	Raw          string    `json:"raw"`
}

type gmailList struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
	NextPageToken      string `json:"nextPageToken"`
	ResultSizeEstimate int    `json:"resultSizeEstimate"`
}

type gmailLabel struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	MessagesTotal  int    `json:"messagesTotal"`
	MessagesUnread int    `json:"messagesUnread"`
	ThreadsTotal   int    `json:"threadsTotal"`
	ThreadsUnread  int    `json:"threadsUnread"`
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MailListInput,
) (application.MailPage, error) {
	label, query, err := client.mailFolder(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	return client.listMessages(ctx, label, query, input.Offset, input.Limit)
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MailSearchInput,
) (application.MailPage, error) {
	label, folderQuery, err := client.mailFolder(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	query := strings.TrimSpace(folderQuery + " " + input.Query)
	return client.listMessages(ctx, label, query, input.Offset, input.Limit)
}

func (client *Client) listMessages(
	ctx context.Context,
	label, query string,
	offset, limit int,
) (application.MailPage, error) {
	if offset+limit > 500 {
		return application.MailPage{}, errors.New(
			"gmail offset window cannot exceed 500 messages",
		)
	}
	values := url.Values{
		"maxResults": {strconv.Itoa(offset + limit)},
	}
	if label != "" {
		values.Set("labelIds", label)
	}
	if query != "" {
		values.Set("q", query)
	}
	var listing gmailList
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "gmail/v1/users/me/messages", values,
		nil, &listing, false, nil, http.StatusOK,
	); err != nil {
		return application.MailPage{}, err
	}
	if offset > len(listing.Messages) {
		offset = len(listing.Messages)
	}
	end := min(offset+limit, len(listing.Messages))
	page := application.MailPage{
		Messages:         make([]application.MailSummary, 0, end-offset),
		TotalItemsInView: listing.ResultSizeEstimate,
		IncludesLastItem: listing.NextPageToken == "" && end == len(listing.Messages),
	}
	for _, item := range listing.Messages[offset:end] {
		message, err := client.getMessage(ctx, item.ID, "metadata")
		if err != nil {
			return application.MailPage{}, err
		}
		summary, err := messageSummary(message)
		if err != nil {
			return application.MailPage{}, err
		}
		page.Messages = append(page.Messages, summary)
	}
	return page, nil
}

func (client *Client) getMessage(
	ctx context.Context,
	id, format string,
) (gmailMessage, error) {
	values := url.Values{"format": {format}}
	if format == "metadata" {
		for _, header := range []string{
			"Subject", "From", "To", "Cc", "Reply-To", "Message-ID", "References",
		} {
			values.Add("metadataHeaders", header)
		}
	}
	var message gmailMessage
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet,
		"gmail/v1/users/me/messages/"+escaped(id),
		values, nil, &message, false, nil, http.StatusOK,
	); err != nil {
		return gmailMessage{}, err
	}
	if message.ID != id || !validGoogleID(message.ID) ||
		!validGoogleID(message.HistoryID) {
		return gmailMessage{}, errors.New("gmail returned an invalid message identity")
	}
	return message, nil
}

func messageSummary(message gmailMessage) (application.MailSummary, error) {
	id, err := encodeMessageID(message.ID)
	if err != nil {
		return application.MailSummary{}, err
	}
	from := parseGmailAddress(headerValue(message.Payload.Headers, "From"))
	importance := ""
	if slices.Contains(message.LabelIDs, "IMPORTANT") {
		importance = "high"
	}
	return application.MailSummary{
		ID: id, ChangeKey: encodeHistoryID(message.HistoryID),
		Subject:        headerValue(message.Payload.Headers, "Subject"),
		From:           from,
		ReceivedAt:     millisecondsTime(message.InternalDate),
		Importance:     importance,
		IsRead:         !slices.Contains(message.LabelIDs, "UNREAD"),
		HasAttachments: gmailHasAttachments(message.Payload),
	}, nil
}

func parseGmailAddress(raw string) application.MailAddress {
	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return application.MailAddress{}
	}
	return application.MailAddress{Name: parsed.Name, Address: parsed.Address}
}

func headerValue(headers []gmailHeader, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func gmailHasAttachments(part gmailPart) bool {
	if part.Filename != "" || part.Body.AttachmentID != "" {
		return true
	}
	for _, child := range part.Parts {
		if gmailHasAttachments(child) {
			return true
		}
	}
	return false
}

type gmailAttachmentReference struct {
	MessageID    string `json:"messageId"`
	HistoryID    string `json:"historyId"`
	AttachmentID string `json:"attachmentId"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	Size         int    `json:"size"`
	Inline       bool   `json:"inline"`
	ContentID    string `json:"contentId,omitempty"`
}

func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailBody{}, err
	}
	message, err := client.getMessage(ctx, reference.ID, "full")
	if err != nil {
		return application.MailBody{}, err
	}
	body := application.MailBody{
		ID: input.MessageID, ChangeKey: encodeHistoryID(message.HistoryID),
		Attachments: make([]application.MailAttachmentMetadata, 0, 4),
	}
	var plain, htmlBody strings.Builder
	if err := walkGmailParts(
		message.Payload,
		func(part gmailPart, content []byte) error {
			disposition := strings.ToLower(headerValue(part.Headers, "Content-Disposition"))
			inline := strings.HasPrefix(disposition, "inline")
			if part.Filename != "" || part.Body.AttachmentID != "" {
				if len(body.Attachments) >= application.MaxMailAttachmentMetadata {
					return errors.New("gmail attachment metadata count exceeds the limit")
				}
				attachmentID, err := encodeReference("gga1_", gmailAttachmentReference{
					MessageID: message.ID, HistoryID: message.HistoryID,
					AttachmentID: part.Body.AttachmentID, Name: part.Filename,
					ContentType: part.MimeType, Size: part.Body.Size, Inline: inline,
					ContentID: strings.Trim(
						headerValue(part.Headers, "Content-ID"),
						"<>",
					),
				})
				if err != nil {
					return err
				}
				body.Attachments = append(body.Attachments, application.MailAttachmentMetadata{
					ID: attachmentID, Kind: "file", Name: part.Filename,
					ContentType: part.MimeType, Size: part.Body.Size, IsInline: inline,
					ContentID: strings.Trim(
						headerValue(part.Headers, "Content-ID"),
						"<>",
					),
				})
				return nil
			}
			switch strings.ToLower(part.MimeType) {
			case "text/plain":
				return appendBoundedText(&plain, string(content))
			case "text/html":
				return appendBoundedText(&htmlBody, string(content))
			default:
				return nil
			}
		},
	); err != nil {
		return application.MailBody{}, err
	}
	if plain.Len() != 0 {
		body.Text = plain.String()
	} else {
		body.Text, err = htmlText(htmlBody.String())
		if err != nil {
			return application.MailBody{}, err
		}
	}
	return body, nil
}

func walkGmailParts(
	part gmailPart,
	visit func(gmailPart, []byte) error,
) error {
	if len(part.Parts) != 0 {
		for _, child := range part.Parts {
			if err := walkGmailParts(child, visit); err != nil {
				return err
			}
		}
		return nil
	}
	var content []byte
	if part.Body.Data != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			return errors.New("gmail MIME part data is malformed")
		}
		limit := application.MaxMailBodyBytes
		if part.Filename != "" || part.Body.AttachmentID != "" {
			limit = application.MaxMailAttachmentBytes
		}
		if len(decoded) > limit {
			return errors.New("gmail inline MIME part exceeds the limit")
		}
		content = decoded
	}
	return visit(part, content)
}

func (client *Client) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
) (application.MailAttachment, error) {
	var reference gmailAttachmentReference
	if err := decodeReference(input.AttachmentID, "gga1_", &reference); err != nil ||
		!validGoogleID(reference.MessageID) ||
		!validGoogleID(reference.HistoryID) ||
		!validGoogleID(reference.AttachmentID) ||
		reference.Size < 0 ||
		reference.Size > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, errors.New(
			"attachment ID is not a Google identifier",
		)
	}
	message, err := client.getMessage(ctx, reference.MessageID, "metadata")
	if err != nil {
		return application.MailAttachment{}, err
	}
	if message.HistoryID != reference.HistoryID {
		return application.MailAttachment{}, errors.New(
			"gmail source message changed before attachment read",
		)
	}
	var response gmailBody
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet,
		"gmail/v1/users/me/messages/"+escaped(reference.MessageID)+
			"/attachments/"+escaped(reference.AttachmentID),
		nil, nil, &response, false, nil, http.StatusOK,
	); err != nil {
		return application.MailAttachment{}, err
	}
	content, err := base64.RawURLEncoding.DecodeString(response.Data)
	if err != nil || len(content) != reference.Size ||
		len(content) > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, errors.New(
			"gmail attachment content did not match reviewed metadata",
		)
	}
	return application.MailAttachment{
		MailAttachmentMetadata: application.MailAttachmentMetadata{
			ID: input.AttachmentID, Kind: "file", Name: reference.Name,
			ContentType: reference.ContentType, Size: len(content),
			IsInline: reference.Inline, ContentID: reference.ContentID,
		},
		ContentBase64: base64.StdEncoding.EncodeToString(content),
	}, nil
}

func (client *Client) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	if input.Parent.Kind != application.MailFolderDistinguished ||
		!strings.EqualFold(input.Parent.ID, "msgfolderroot") {
		return application.MailFolderPage{}, errors.New(
			"gmail labels are flat and can only be listed from msgfolderroot",
		)
	}
	var response struct {
		Labels []gmailLabel `json:"labels"`
	}
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "gmail/v1/users/me/labels", nil,
		nil, &response, false, nil, http.StatusOK,
	); err != nil {
		return application.MailFolderPage{}, err
	}
	slices.SortFunc(response.Labels, func(left, right gmailLabel) int {
		return strings.Compare(left.Name, right.Name)
	})
	if input.Offset > len(response.Labels) {
		input.Offset = len(response.Labels)
	}
	end := min(input.Offset+input.Limit, len(response.Labels))
	page := application.MailFolderPage{
		Folders:      make([]application.MailFolderSummary, 0, end-input.Offset),
		TotalFolders: len(response.Labels), IncludesLastItem: end == len(response.Labels),
	}
	for _, label := range response.Labels[input.Offset:end] {
		id, err := encodeReference("ggl1_", struct {
			ID string `json:"id"`
		}{ID: label.ID})
		if err != nil {
			return application.MailFolderPage{}, err
		}
		page.Folders = append(page.Folders, application.MailFolderSummary{
			ID: id, DisplayName: label.Name, Class: "label",
			DistinguishedID: gmailDistinguishedLabel(label.ID),
			TotalItemCount:  label.MessagesTotal, UnreadItemCount: label.MessagesUnread,
		})
	}
	return page, nil
}

func (client *Client) mailFolder(
	folder application.MailFolder,
) (label, query string, err error) {
	if folder.Kind == application.MailFolderDistinguished {
		switch strings.ToLower(folder.ID) {
		case "inbox":
			return "INBOX", "", nil
		case "drafts":
			return "DRAFT", "", nil
		case "sentitems":
			return "SENT", "", nil
		case "deleteditems":
			return "TRASH", "", nil
		case "archive":
			return "", "-in:inbox -in:trash -in:spam", nil
		case "msgfolderroot":
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unsupported Gmail folder %q", folder.ID)
		}
	}
	var reference struct {
		ID string `json:"id"`
	}
	if err := decodeReference(folder.ID, "ggl1_", &reference); err != nil ||
		!validGoogleID(reference.ID) {
		return "", "", errors.New("folder ID is not a Google label identifier")
	}
	return reference.ID, "", nil
}

func gmailDistinguishedLabel(id string) string {
	switch id {
	case "INBOX":
		return "inbox"
	case "DRAFT":
		return "drafts"
	case "SENT":
		return "sentitems"
	case "TRASH":
		return "deleteditems"
	default:
		return ""
	}
}
