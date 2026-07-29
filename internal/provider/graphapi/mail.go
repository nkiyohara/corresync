package graphapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/nkiyohara/corresync/internal/application"
)

type graphEmailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type graphRecipient struct {
	EmailAddress graphEmailAddress `json:"emailAddress"`
}

type graphItemBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type graphMessage struct {
	ODataETag      string         `json:"@odata.etag"`
	ID             string         `json:"id"`
	ChangeKey      string         `json:"changeKey"`
	Subject        string         `json:"subject"`
	From           graphRecipient `json:"from"`
	ReceivedAt     string         `json:"receivedDateTime"`
	Importance     string         `json:"importance"`
	IsRead         bool           `json:"isRead"`
	HasAttachments bool           `json:"hasAttachments"`
	IsDraft        bool           `json:"isDraft"`
	Body           graphItemBody  `json:"body"`
}

type graphMessagePage struct {
	Count    int            `json:"@odata.count"`
	NextLink string         `json:"@odata.nextLink"`
	Value    []graphMessage `json:"value"`
}

type graphAttachment struct {
	ODataType    string `json:"@odata.type"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	Size         int    `json:"size"`
	IsInline     bool   `json:"isInline"`
	ContentID    string `json:"contentId"`
	ContentBytes string `json:"contentBytes"`
}

type graphAttachmentReference struct {
	MessageID   string `json:"messageId"`
	MessageETag string `json:"messageEtag"`
	Attachment  string `json:"attachment"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	IsInline    bool   `json:"isInline"`
	ContentID   string `json:"contentId,omitempty"`
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MailListInput,
) (application.MailPage, error) {
	resource, err := client.messageCollection(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	return client.listMessages(ctx, resource, "", input.Offset, input.Limit)
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MailSearchInput,
) (application.MailPage, error) {
	resource, err := client.messageCollection(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	return client.listMessages(
		ctx,
		resource,
		`"`+strings.ReplaceAll(input.Query, `"`, `\"`)+`"`,
		input.Offset,
		input.Limit,
	)
}

func (client *Client) listMessages(
	ctx context.Context,
	resource, search string,
	offset, limit int,
) (application.MailPage, error) {
	query := url.Values{
		"$select": {
			"id,changeKey,subject,from,receivedDateTime,importance,isRead,hasAttachments",
		},
		"$top":   {strconv.Itoa(limit)},
		"$skip":  {strconv.Itoa(offset)},
		"$count": {"true"},
	}
	headers := http.Header{}
	if search == "" {
		query.Set("$orderby", "receivedDateTime desc")
	} else {
		query.Set("$search", search)
		headers.Set("ConsistencyLevel", "eventual")
	}
	var response graphMessagePage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		resource,
		query,
		nil,
		&response,
		false,
		headers,
		http.StatusOK,
	); err != nil {
		return application.MailPage{}, err
	}
	if len(response.Value) > limit {
		return application.MailPage{}, errors.New("graph returned an oversized message page")
	}
	page := application.MailPage{
		Messages:         make([]application.MailSummary, 0, len(response.Value)),
		TotalItemsInView: response.Count,
		IncludesLastItem: response.NextLink == "",
	}
	for _, message := range response.Value {
		summary, err := graphMessageSummary(message)
		if err != nil {
			return application.MailPage{}, err
		}
		page.Messages = append(page.Messages, summary)
	}
	return page, nil
}

func graphMessageSummary(message graphMessage) (application.MailSummary, error) {
	if !validGraphID(message.ID) || !validETag(message.ODataETag) {
		return application.MailSummary{}, errors.New(
			"graph returned an invalid message identity",
		)
	}
	id, err := encodeMessageID(message.ID)
	if err != nil {
		return application.MailSummary{}, err
	}
	importance := strings.ToLower(message.Importance)
	if importance == "normal" {
		importance = ""
	}
	return application.MailSummary{
		ID: id, ChangeKey: encodeETag(message.ODataETag),
		Subject: message.Subject,
		From: application.MailAddress{
			Name: message.From.EmailAddress.Name, Address: message.From.EmailAddress.Address,
		},
		ReceivedAt: message.ReceivedAt, Importance: importance,
		IsRead: message.IsRead, HasAttachments: message.HasAttachments,
	}, nil
}

func (client *Client) getMessage(
	ctx context.Context,
	id string,
	body bool,
) (graphMessage, error) {
	selectFields := "id,changeKey,subject,from,receivedDateTime,importance,isRead,hasAttachments,isDraft"
	headers := http.Header{}
	if body {
		selectFields += ",body"
		headers.Set("Prefer", `outlook.body-content-type="text"`)
	}
	var message graphMessage
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		"me/messages/"+escaped(id),
		url.Values{"$select": {selectFields}},
		nil,
		&message,
		false,
		headers,
		http.StatusOK,
	); err != nil {
		return graphMessage{}, err
	}
	if message.ID != id || !validGraphID(message.ID) ||
		!validETag(message.ODataETag) {
		return graphMessage{}, errors.New("graph returned an invalid message identity")
	}
	return message, nil
}

func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailBody{}, err
	}
	message, err := client.getMessage(ctx, reference.ID, true)
	if err != nil {
		return application.MailBody{}, err
	}
	body := message.Body.Content
	if strings.EqualFold(message.Body.ContentType, "html") {
		body, err = graphHTMLText(body)
		if err != nil {
			return application.MailBody{}, err
		}
	}
	if len(body) > application.MaxMailBodyBytes {
		return application.MailBody{}, errors.New("graph message body exceeds the configured limit")
	}
	attachments, err := client.listAttachments(
		ctx,
		message.ID,
		message.ODataETag,
	)
	if err != nil {
		return application.MailBody{}, err
	}
	return application.MailBody{
		ID: input.MessageID, ChangeKey: encodeETag(message.ODataETag),
		Text: body, Attachments: attachments,
	}, nil
}

func (client *Client) listAttachments(
	ctx context.Context,
	messageID, messageETag string,
) ([]application.MailAttachmentMetadata, error) {
	var response struct {
		NextLink string            `json:"@odata.nextLink"`
		Value    []graphAttachment `json:"value"`
	}
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		"me/messages/"+escaped(messageID)+"/attachments",
		url.Values{
			"$select": {"id,name,contentType,size,isInline,contentId"},
			"$top":    {strconv.Itoa(application.MaxMailAttachmentMetadata)},
		},
		nil,
		&response,
		false,
		nil,
		http.StatusOK,
	); err != nil {
		return nil, err
	}
	if response.NextLink != "" ||
		len(response.Value) > application.MaxMailAttachmentMetadata {
		return nil, errors.New("graph attachment metadata exceeds the configured limit")
	}
	result := make([]application.MailAttachmentMetadata, 0, len(response.Value))
	for _, attachment := range response.Value {
		if !validGraphID(attachment.ID) || attachment.Size < 0 {
			return nil, errors.New("graph returned invalid attachment metadata")
		}
		kind := "file"
		if attachment.ODataType != "" &&
			attachment.ODataType != "#microsoft.graph.fileAttachment" {
			kind = "item"
		}
		id, err := encodeReference("mga1_", graphAttachmentReference{
			MessageID: messageID, MessageETag: messageETag,
			Attachment: attachment.ID, Name: attachment.Name,
			ContentType: attachment.ContentType, Size: attachment.Size,
			IsInline: attachment.IsInline, ContentID: attachment.ContentID,
		})
		if err != nil {
			return nil, err
		}
		result = append(result, application.MailAttachmentMetadata{
			ID: id, Kind: kind, Name: attachment.Name,
			ContentType: attachment.ContentType, Size: attachment.Size,
			IsInline: attachment.IsInline, ContentID: attachment.ContentID,
		})
	}
	return result, nil
}

func (client *Client) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
) (application.MailAttachment, error) {
	var reference graphAttachmentReference
	if err := decodeReference(input.AttachmentID, "mga1_", &reference); err != nil ||
		!validGraphID(reference.MessageID) ||
		!validETag(reference.MessageETag) ||
		!validGraphID(reference.Attachment) ||
		reference.Size < 0 ||
		reference.Size > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, errors.New(
			"attachment ID is not a Graph identifier",
		)
	}
	message, err := client.getMessage(ctx, reference.MessageID, false)
	if err != nil {
		return application.MailAttachment{}, err
	}
	if message.ODataETag != reference.MessageETag {
		return application.MailAttachment{}, errors.New(
			"graph source message changed before attachment read",
		)
	}
	var attachment graphAttachment
	if _, err := client.api.DoJSON(
		ctx,
		http.MethodGet,
		"me/messages/"+escaped(reference.MessageID)+
			"/attachments/"+escaped(reference.Attachment),
		nil,
		nil,
		&attachment,
		false,
		nil,
		http.StatusOK,
	); err != nil {
		return application.MailAttachment{}, err
	}
	if attachment.ID != reference.Attachment ||
		attachment.ODataType != "#microsoft.graph.fileAttachment" {
		return application.MailAttachment{}, errors.New(
			"graph attachment is not a downloadable file",
		)
	}
	content, err := base64.StdEncoding.DecodeString(attachment.ContentBytes)
	if err != nil ||
		len(content) != reference.Size ||
		len(content) > application.MaxMailAttachmentBytes {
		return application.MailAttachment{}, errors.New(
			"graph attachment content did not match reviewed metadata",
		)
	}
	return application.MailAttachment{
		MailAttachmentMetadata: application.MailAttachmentMetadata{
			ID: input.AttachmentID, Kind: "file", Name: reference.Name,
			ContentType: reference.ContentType, Size: len(content),
			IsInline: reference.IsInline, ContentID: reference.ContentID,
		},
		ContentBase64: base64.StdEncoding.EncodeToString(content),
	}, nil
}

type graphFolder struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	ParentFolderID   string `json:"parentFolderId"`
	ChildFolderCount int    `json:"childFolderCount"`
	TotalItemCount   int    `json:"totalItemCount"`
	UnreadItemCount  int    `json:"unreadItemCount"`
}

const (
	graphFolderPageSize        = 100
	maximumGraphFolders        = 10_000
	maximumGraphFolderRequests = 10_001
)

type graphFolderPage struct {
	Count    *int          `json:"@odata.count"`
	NextLink string        `json:"@odata.nextLink"`
	Value    []graphFolder `json:"value"`
}

func (client *Client) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	resource, err := client.folderCollection(input.Parent)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	if input.Traversal == application.MailFolderTraversalDeep {
		return client.listMailFolderHierarchy(ctx, resource, input)
	}
	query := graphFolderQuery(input.Limit)
	query.Set("$skip", strconv.Itoa(input.Offset))
	var response graphFolderPage
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, resource, query, nil, &response, false, nil,
		http.StatusOK,
	); err != nil {
		return application.MailFolderPage{}, err
	}
	if response.Count == nil ||
		*response.Count < len(response.Value) ||
		len(response.Value) > input.Limit {
		return application.MailFolderPage{}, errors.New(
			"graph returned an invalid folder page",
		)
	}
	folders, err := graphFolderSummaries(response.Value)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	return application.MailFolderPage{
		Folders: folders, TotalFolders: *response.Count,
		IncludesLastItem: response.NextLink == "",
	}, nil
}

func (client *Client) listMailFolderHierarchy(
	ctx context.Context,
	resource string,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	queue := []string{resource}
	seen := make(map[string]struct{})
	folders := make([]graphFolder, 0, 64)
	requests := 0
	for len(queue) != 0 {
		if requests >= maximumGraphFolderRequests {
			return application.MailFolderPage{}, errors.New(
				"graph mail folder hierarchy exceeded the request bound",
			)
		}
		current := queue[0]
		queue = queue[1:]
		children, err := client.graphFolderCollection(
			ctx,
			current,
			maximumGraphFolders-len(folders),
			&requests,
		)
		if err != nil {
			return application.MailFolderPage{}, err
		}
		for _, child := range children {
			if !validGraphID(child.ID) {
				return application.MailFolderPage{}, errors.New(
					"graph returned an invalid mail folder identity",
				)
			}
			if _, exists := seen[child.ID]; exists {
				return application.MailFolderPage{}, errors.New(
					"graph returned a duplicate mail folder identity",
				)
			}
			seen[child.ID] = struct{}{}
			folders = append(folders, child)
			if child.ChildFolderCount > 0 {
				queue = append(
					queue,
					"me/mailFolders/"+escaped(child.ID)+"/childFolders",
				)
			}
		}
	}
	total := len(folders)
	start := min(input.Offset, total)
	end := min(start+input.Limit, total)
	summaries, err := graphFolderSummaries(folders[start:end])
	if err != nil {
		return application.MailFolderPage{}, err
	}
	return application.MailFolderPage{
		Folders: summaries, TotalFolders: total,
		IncludesLastItem: end == total,
	}, nil
}

func (client *Client) graphFolderCollection(
	ctx context.Context,
	resource string,
	maximum int,
	requests *int,
) ([]graphFolder, error) {
	if maximum < 1 {
		return nil, errors.New(
			"graph mail folder hierarchy exceeds the configured bound",
		)
	}
	query := graphFolderQuery(min(graphFolderPageSize, maximum))
	seenLinks := make(map[string]struct{})
	folders := make([]graphFolder, 0, min(graphFolderPageSize, maximum))
	expected := -1
	for {
		if *requests >= maximumGraphFolderRequests {
			return nil, errors.New(
				"graph mail folder hierarchy exceeded the request bound",
			)
		}
		(*requests)++
		var response graphFolderPage
		if _, err := client.api.DoJSON(
			ctx, http.MethodGet, resource, query, nil, &response, false, nil,
			http.StatusOK,
		); err != nil {
			return nil, err
		}
		if response.Count != nil {
			if *response.Count < 0 || *response.Count > maximum ||
				expected >= 0 && *response.Count != expected {
				return nil, errors.New(
					"graph mail folder count changed during pagination",
				)
			}
			expected = *response.Count
		}
		if expected < 0 ||
			len(response.Value) > graphFolderPageSize ||
			len(folders)+len(response.Value) > maximum {
			return nil, errors.New(
				"graph returned an invalid folder collection page",
			)
		}
		folders = append(folders, response.Value...)
		if response.NextLink == "" {
			if len(folders) != expected {
				return nil, errors.New(
					"graph folder collection ended before its reported count",
				)
			}
			return folders, nil
		}
		if len(response.Value) == 0 || len(folders) >= expected {
			return nil, errors.New(
				"graph folder pagination made no progress",
			)
		}
		if _, exists := seenLinks[response.NextLink]; exists {
			return nil, errors.New(
				"graph repeated a folder continuation URL",
			)
		}
		seenLinks[response.NextLink] = struct{}{}
		nextResource, nextQuery, err := client.folderContinuation(response.NextLink)
		if err != nil {
			return nil, err
		}
		resource, query = nextResource, nextQuery
	}
}

func graphFolderQuery(limit int) url.Values {
	return url.Values{
		"$select": {
			"id,displayName,parentFolderId,childFolderCount,totalItemCount,unreadItemCount",
		},
		"$top":   {strconv.Itoa(limit)},
		"$count": {"true"},
	}
}

func graphFolderSummaries(
	folders []graphFolder,
) ([]application.MailFolderSummary, error) {
	summaries := make([]application.MailFolderSummary, 0, len(folders))
	for _, folder := range folders {
		if !validGraphID(folder.ID) ||
			folder.ChildFolderCount < 0 ||
			folder.TotalItemCount < 0 ||
			folder.UnreadItemCount < 0 {
			return nil, errors.New(
				"graph returned invalid mail folder metadata",
			)
		}
		id, err := encodeFolderID(folder.ID)
		if err != nil {
			return nil, err
		}
		parentID := ""
		if validGraphID(folder.ParentFolderID) {
			parentID, err = encodeFolderID(folder.ParentFolderID)
			if err != nil {
				return nil, err
			}
		}
		summaries = append(summaries, application.MailFolderSummary{
			ID: id, ParentID: parentID, DisplayName: folder.DisplayName,
			Class: "folder", ChildFolderCount: folder.ChildFolderCount,
			TotalItemCount:  folder.TotalItemCount,
			UnreadItemCount: folder.UnreadItemCount,
		})
	}
	return summaries, nil
}

func (client *Client) messageCollection(
	folder application.MailFolder,
) (string, error) {
	id, err := client.graphFolder(folder)
	if err != nil {
		return "", err
	}
	return "me/mailFolders/" + escaped(id) + "/messages", nil
}

func (client *Client) folderCollection(
	parent application.MailFolder,
) (string, error) {
	if parent.Kind == application.MailFolderDistinguished &&
		strings.EqualFold(parent.ID, "msgfolderroot") {
		return "me/mailFolders", nil
	}
	id, err := client.graphFolder(parent)
	if err != nil {
		return "", err
	}
	return "me/mailFolders/" + escaped(id) + "/childFolders", nil
}

func (*Client) graphFolder(folder application.MailFolder) (string, error) {
	if folder.Kind == application.MailFolderDistinguished {
		switch strings.ToLower(folder.ID) {
		case "inbox", "archive", "deleteditems", "drafts", "sentitems":
			return strings.ToLower(folder.ID), nil
		default:
			return "", fmt.Errorf(
				"graph cannot address distinguished mail folder %q",
				folder.ID,
			)
		}
	}
	reference, err := decodeFolderID(folder.ID)
	if err != nil {
		return "", err
	}
	return reference.ID, nil
}

func graphHTMLText(value string) (string, error) {
	root, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return "", errors.New("graph HTML body is malformed")
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
				if builder.Len() != 0 {
					builder.WriteByte('\n')
				}
				if len(text) > application.MaxMailBodyBytes-builder.Len() {
					walkErr = errors.New("graph HTML body exceeds the configured limit")
					return
				}
				builder.WriteString(text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return builder.String(), walkErr
}
