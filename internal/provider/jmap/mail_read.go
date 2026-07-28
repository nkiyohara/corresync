package jmap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/nkiyohara/corresync/internal/application"
)

type getResponse[T any] struct {
	AccountID string   `json:"accountId"`
	State     string   `json:"state"`
	List      []T      `json:"list"`
	NotFound  []string `json:"notFound"`
}

type mailbox struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ParentID    string `json:"parentId"`
	Role        string `json:"role"`
	SortOrder   int    `json:"sortOrder"`
	TotalEmails int    `json:"totalEmails"`
	UnreadEmail int    `json:"unreadEmails"`
}

type emailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type emailPart struct {
	PartID      string      `json:"partId"`
	BlobID      string      `json:"blobId"`
	Size        int         `json:"size"`
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Charset     string      `json:"charset"`
	Disposition string      `json:"disposition"`
	CID         string      `json:"cid"`
	SubParts    []emailPart `json:"subParts"`
}

type bodyValue struct {
	Value             string `json:"value"`
	IsEncodingProblem bool   `json:"isEncodingProblem"`
	IsTruncated       bool   `json:"isTruncated"`
}

type email struct {
	ID            string                     `json:"id"`
	BlobID        string                     `json:"blobId"`
	ThreadID      string                     `json:"threadId"`
	MailboxIDs    map[string]bool            `json:"mailboxIds"`
	Keywords      map[string]bool            `json:"keywords"`
	Size          int                        `json:"size"`
	ReceivedAt    string                     `json:"receivedAt"`
	MessageID     []string                   `json:"messageId"`
	InReplyTo     []string                   `json:"inReplyTo"`
	References    []string                   `json:"references"`
	Sender        []emailAddress             `json:"sender"`
	From          []emailAddress             `json:"from"`
	To            []emailAddress             `json:"to"`
	CC            []emailAddress             `json:"cc"`
	BCC           []emailAddress             `json:"bcc"`
	Subject       string                     `json:"subject"`
	HasAttachment bool                       `json:"hasAttachment"`
	Preview       string                     `json:"preview"`
	TextBody      []emailPart                `json:"textBody"`
	HTMLBody      []emailPart                `json:"htmlBody"`
	Attachments   []emailPart                `json:"attachments"`
	BodyValues    map[string]bodyValue       `json:"bodyValues"`
	BodyStructure *emailPart                 `json:"bodyStructure"`
	Headers       map[string]json.RawMessage `json:"-"`
}

type emailQueryResponse struct {
	AccountID           string   `json:"accountId"`
	QueryState          string   `json:"queryState"`
	CanCalculateChanges bool     `json:"canCalculateChanges"`
	Position            int      `json:"position"`
	IDs                 []string `json:"ids"`
	Total               int      `json:"total"`
	Limit               int      `json:"limit"`
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MailListInput,
) (application.MailPage, error) {
	return client.queryMessages(ctx, input.Folder, "", input.Offset, input.Limit)
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MailSearchInput,
) (application.MailPage, error) {
	return client.queryMessages(ctx, input.Folder, input.Query, input.Offset, input.Limit)
}

func (client *Client) queryMessages(
	ctx context.Context,
	folder application.MailFolder,
	text string,
	offset, limit int,
) (application.MailPage, error) {
	mailboxID, err := client.resolveMailbox(ctx, folder)
	if err != nil {
		return application.MailPage{}, err
	}
	filter := map[string]any{"inMailbox": mailboxID}
	if text != "" {
		filter["text"] = text
	}
	var query emailQueryResponse
	if err := client.call(ctx, []string{mailCapability}, "Email/query", map[string]any{
		"accountId": client.accountID,
		"filter":    filter,
		"sort": []map[string]any{
			{"property": "receivedAt", "isAscending": false},
		},
		"position":       offset,
		"limit":          limit,
		"calculateTotal": true,
	}, &query); err != nil {
		return application.MailPage{}, err
	}
	page := application.MailPage{
		Messages:         []application.MailSummary{},
		TotalItemsInView: query.Total,
		IncludesLastItem: query.Position+len(query.IDs) >= query.Total,
	}
	if len(query.IDs) == 0 {
		return page, nil
	}
	response, err := client.getEmails(ctx, query.IDs, false)
	if err != nil {
		return application.MailPage{}, err
	}
	if len(response.NotFound) != 0 {
		return application.MailPage{}, errors.New("JMAP query result changed before metadata retrieval")
	}
	byID := make(map[string]email, len(response.List))
	for _, item := range response.List {
		byID[item.ID] = item
	}
	for _, id := range query.IDs {
		item, exists := byID[id]
		if !exists {
			return application.MailPage{}, errors.New("JMAP omitted a queried email")
		}
		page.Messages = append(page.Messages, mailSummary(item, response.State))
	}
	return page, nil
}

func mailSummary(item email, state string) application.MailSummary {
	importance := "normal"
	if item.Keywords["$flagged"] {
		importance = "high"
	}
	from := firstAddress(item.From)
	return application.MailSummary{
		ID: item.ID, ChangeKey: state, Subject: item.Subject,
		From:       application.MailAddress{Name: from.Name, Address: from.Email},
		ReceivedAt: item.ReceivedAt, Importance: importance,
		IsRead: item.Keywords["$seen"], HasAttachments: item.HasAttachment,
	}
}

func (client *Client) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	response, err := client.getMailboxes(ctx)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	parentID := ""
	if input.Parent.Kind == application.MailFolderOpaque {
		parentID = input.Parent.ID
	} else if !strings.EqualFold(input.Parent.ID, "msgfolderroot") {
		parentID, err = mailboxByRole(response.List, distinguishedMailboxRole(input.Parent.ID))
		if err != nil {
			return application.MailFolderPage{}, err
		}
	}
	children := make([]mailbox, 0, len(response.List))
	for _, item := range response.List {
		if input.Traversal == application.MailFolderTraversalDeep {
			if parentID == "" || isMailboxDescendant(item.ID, parentID, response.List) {
				children = append(children, item)
			}
		} else if item.ParentID == parentID {
			children = append(children, item)
		}
	}
	sort.Slice(children, func(left, right int) bool {
		if children[left].SortOrder != children[right].SortOrder {
			return children[left].SortOrder < children[right].SortOrder
		}
		return children[left].Name < children[right].Name
	})
	total := len(children)
	start := min(input.Offset, total)
	end := min(start+input.Limit, total)
	page := application.MailFolderPage{
		Folders:          make([]application.MailFolderSummary, 0, end-start),
		TotalFolders:     total,
		IncludesLastItem: end == total,
	}
	for _, item := range children[start:end] {
		childCount := 0
		for _, candidate := range response.List {
			if candidate.ParentID == item.ID {
				childCount++
			}
		}
		page.Folders = append(page.Folders, application.MailFolderSummary{
			ID: item.ID, ChangeKey: response.State, ParentID: item.ParentID,
			DisplayName: item.Name, Class: "mail", DistinguishedID: item.Role,
			ChildFolderCount: childCount, TotalItemCount: item.TotalEmails,
			UnreadItemCount: item.UnreadEmail,
		})
	}
	return page, nil
}

func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	response, err := client.getEmails(ctx, []string{input.MessageID}, true)
	if err != nil {
		return application.MailBody{}, err
	}
	if len(response.List) != 1 || len(response.NotFound) != 0 {
		return application.MailBody{}, errors.New("JMAP email was not found")
	}
	item := response.List[0]
	text, err := boundedBodyText(item)
	if err != nil {
		return application.MailBody{}, err
	}
	attachments := make([]application.MailAttachmentMetadata, 0, len(item.Attachments))
	for _, part := range item.Attachments {
		id, err := encodeAttachmentID(attachmentReference{
			MessageID: item.ID, BlobID: part.BlobID, Name: part.Name,
			Type: part.Type, Size: part.Size, CID: part.CID,
			Inline: strings.EqualFold(part.Disposition, "inline"),
		})
		if err != nil {
			return application.MailBody{}, err
		}
		attachments = append(attachments, application.MailAttachmentMetadata{
			ID: id, Kind: "file", Name: part.Name, ContentType: part.Type,
			Size: part.Size, IsInline: strings.EqualFold(part.Disposition, "inline"),
			ContentID: part.CID,
		})
	}
	return application.MailBody{
		ID: item.ID, ChangeKey: response.State, Text: text, Attachments: attachments,
	}, nil
}

func (client *Client) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
) (application.MailAttachment, error) {
	reference, err := decodeAttachmentID(input.AttachmentID)
	if err != nil {
		return application.MailAttachment{}, err
	}
	if reference.Size < 0 || reference.Size > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, fmt.Errorf(
			"mail attachment exceeds %d bytes",
			application.MaxMailAttachmentBytes,
		)
	}
	message, err := client.getEmails(ctx, []string{reference.MessageID}, true)
	if err != nil {
		return application.MailAttachment{}, err
	}
	if len(message.List) != 1 || len(message.NotFound) != 0 ||
		!containsAttachment(message.List[0].Attachments, reference) {
		return application.MailAttachment{}, errors.New(
			"JMAP attachment no longer matches its source email",
		)
	}
	content, err := client.download(
		ctx,
		reference.BlobID,
		reference.Name,
		reference.Type,
		application.MaxMailAttachmentBytes,
	)
	if err != nil {
		return application.MailAttachment{}, err
	}
	if len(content) != reference.Size {
		return application.MailAttachment{}, errors.New("JMAP attachment size changed")
	}
	return application.MailAttachment{
		MailAttachmentMetadata: application.MailAttachmentMetadata{
			ID: input.AttachmentID, Kind: "file", Name: reference.Name,
			ContentType: reference.Type, Size: reference.Size,
			IsInline: reference.Inline, ContentID: reference.CID,
		},
		ContentBase64: base64.StdEncoding.EncodeToString(content),
	}, nil
}

func (client *Client) getMailboxes(ctx context.Context) (getResponse[mailbox], error) {
	var response getResponse[mailbox]
	err := client.call(ctx, []string{mailCapability}, "Mailbox/get", map[string]any{
		"accountId": client.accountID,
		"properties": []string{
			"id", "name", "parentId", "role", "sortOrder", "totalEmails", "unreadEmails",
		},
	}, &response)
	return response, err
}

func (client *Client) getEmails(
	ctx context.Context,
	ids []string,
	body bool,
) (getResponse[email], error) {
	properties := []string{
		"id", "blobId", "threadId", "mailboxIds", "keywords", "size",
		"receivedAt", "messageId", "inReplyTo", "references", "sender",
		"from", "to", "cc", "bcc", "subject", "hasAttachment", "preview",
	}
	arguments := map[string]any{
		"accountId":  client.accountID,
		"ids":        ids,
		"properties": properties,
	}
	if body {
		arguments["properties"] = append(
			properties,
			"textBody", "htmlBody", "attachments", "bodyValues",
		)
		arguments["fetchTextBodyValues"] = true
		arguments["fetchHTMLBodyValues"] = true
		arguments["maxBodyValueBytes"] = application.MaxMailBodyBytes
	}
	var response getResponse[email]
	err := client.call(ctx, []string{mailCapability}, "Email/get", arguments, &response)
	return response, err
}

func (client *Client) resolveMailbox(
	ctx context.Context,
	folder application.MailFolder,
) (string, error) {
	if folder.Kind == application.MailFolderOpaque {
		return folder.ID, nil
	}
	role := distinguishedMailboxRole(folder.ID)
	if role == "" {
		return "", fmt.Errorf("JMAP does not map distinguished folder %q", folder.ID)
	}
	response, err := client.getMailboxes(ctx)
	if err != nil {
		return "", err
	}
	return mailboxByRole(response.List, role)
}

func distinguishedMailboxRole(value string) string {
	switch strings.ToLower(value) {
	case "inbox":
		return "inbox"
	case "archive":
		return "archive"
	case "deleteditems":
		return "trash"
	case "drafts":
		return "drafts"
	case "sentitems":
		return "sent"
	default:
		return ""
	}
}

func mailboxByRole(mailboxes []mailbox, role string) (string, error) {
	var selected string
	for _, item := range mailboxes {
		if item.Role == role {
			if selected != "" {
				return "", fmt.Errorf("JMAP has more than one %s mailbox", role)
			}
			selected = item.ID
		}
	}
	if selected == "" {
		return "", fmt.Errorf("JMAP has no %s mailbox", role)
	}
	return selected, nil
}

func isMailboxDescendant(id, parent string, mailboxes []mailbox) bool {
	parents := make(map[string]string, len(mailboxes))
	for _, item := range mailboxes {
		parents[item.ID] = item.ParentID
	}
	seen := make(map[string]struct{}, len(mailboxes))
	for current := id; current != ""; current = parents[current] {
		if current == parent {
			return id != parent
		}
		if _, exists := seen[current]; exists {
			return false
		}
		seen[current] = struct{}{}
	}
	return false
}

func firstAddress(addresses []emailAddress) emailAddress {
	if len(addresses) == 0 {
		return emailAddress{}
	}
	return addresses[0]
}

func boundedBodyText(item email) (string, error) {
	var builder strings.Builder
	for _, part := range item.TextBody {
		value, exists := item.BodyValues[part.PartID]
		if !exists || value.IsEncodingProblem || value.IsTruncated {
			continue
		}
		if err := appendBodyText(
			&builder,
			value.Value,
			application.MaxMailBodyBytes,
		); err != nil {
			return "", err
		}
	}
	if builder.Len() == 0 {
		for _, part := range item.HTMLBody {
			value, exists := item.BodyValues[part.PartID]
			if !exists || value.IsEncodingProblem || value.IsTruncated {
				continue
			}
			plain, err := htmlText(value.Value)
			if err != nil {
				return "", err
			}
			if err := appendBodyText(
				&builder,
				plain,
				application.MaxMailBodyBytes,
			); err != nil {
				return "", err
			}
		}
	}
	return builder.String(), nil
}

func appendBodyText(builder *strings.Builder, value string, maximum int) error {
	separatorBytes := 0
	if builder.Len() != 0 {
		separatorBytes = 1
	}
	if len(value) > maximum-builder.Len()-separatorBytes {
		return fmt.Errorf("mail body exceeds %d bytes", maximum)
	}
	if separatorBytes != 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(value)
	return nil
}

func htmlText(value string) (string, error) {
	if len(value) > application.MaxMailBodyBytes {
		return "", fmt.Errorf("mail HTML body exceeds %d bytes", application.MaxMailBodyBytes)
	}
	root, err := html.Parse(io.LimitReader(strings.NewReader(value), application.MaxMailBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("parse JMAP HTML body: %w", err)
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
				walkErr = appendBodyText(
					&builder,
					text,
					application.MaxMailBodyBytes,
				)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if walkErr != nil {
		return "", walkErr
	}
	return builder.String(), nil
}

func containsAttachment(parts []emailPart, reference attachmentReference) bool {
	for _, part := range parts {
		if part.BlobID == reference.BlobID &&
			part.Name == reference.Name &&
			part.Type == reference.Type &&
			part.Size == reference.Size &&
			part.CID == reference.CID &&
			strings.EqualFold(part.Disposition, "inline") == reference.Inline {
			return true
		}
	}
	return false
}

type attachmentReference struct {
	MessageID string `json:"messageId"`
	BlobID    string `json:"blobId"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int    `json:"size"`
	CID       string `json:"cid,omitempty"`
	Inline    bool   `json:"inline,omitempty"`
}

func encodeAttachmentID(reference attachmentReference) (string, error) {
	data, err := json.Marshal(reference)
	if err != nil {
		return "", fmt.Errorf("encode JMAP attachment ID: %w", err)
	}
	return "jma1_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeAttachmentID(value string) (attachmentReference, error) {
	if !strings.HasPrefix(value, "jma1_") {
		return attachmentReference{}, errors.New("attachment ID is not a JMAP identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "jma1_"))
	if err != nil || len(data) > 4096 {
		return attachmentReference{}, errors.New("JMAP attachment ID is malformed")
	}
	var reference attachmentReference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil ||
		reference.MessageID == "" || reference.BlobID == "" ||
		reference.Name == "" || reference.Size < 0 {
		return attachmentReference{}, errors.New("JMAP attachment ID is malformed")
	}
	return reference, nil
}
