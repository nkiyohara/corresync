package imapmail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"

	"github.com/nkiyohara/corresync/internal/application"
)

var metadataItems = []imap.FetchItem{
	imap.FetchUid,
	imap.FetchEnvelope,
	imap.FetchFlags,
	imap.FetchInternalDate,
	imap.FetchRFC822Size,
	imap.FetchBodyStructure,
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
	query string,
	offset, limit int,
) (application.MailPage, error) {
	var page application.MailPage
	err := client.withIMAP(ctx, func(connection *imapclient.Client) error {
		mailbox, err := client.resolveMailbox(connection, folder)
		if err != nil {
			return err
		}
		status, err := connection.Select(mailbox, true)
		if err != nil {
			return err
		}
		criteria := imap.NewSearchCriteria()
		if query != "" {
			criteria.Text = []string{query}
		}
		uids, err := connection.UidSearch(criteria)
		if err != nil {
			return err
		}
		sort.Slice(uids, func(left, right int) bool { return uids[left] > uids[right] })
		page.TotalItemsInView = len(uids)
		start := min(offset, len(uids))
		end := min(start+limit, len(uids))
		page.IncludesLastItem = end == len(uids)
		page.Messages = make([]application.MailSummary, 0, end-start)
		if start == end {
			return nil
		}
		messages, err := fetchUIDs(connection, uids[start:end], metadataItems)
		if err != nil {
			return err
		}
		byUID := make(map[uint32]*imap.Message, len(messages))
		for _, message := range messages {
			byUID[message.Uid] = message
		}
		for _, uid := range uids[start:end] {
			message := byUID[uid]
			if message == nil {
				return errors.New("IMAP omitted requested message metadata")
			}
			summary, err := mailSummary(mailbox, status, message)
			if err != nil {
				return err
			}
			page.Messages = append(page.Messages, summary)
		}
		return nil
	})
	return page, err
}

func mailSummary(
	mailbox string,
	status *imap.MailboxStatus,
	message *imap.Message,
) (application.MailSummary, error) {
	id, err := encodeMessageID(messageReference{
		Mailbox: mailbox, UIDValidity: status.UidValidity, UID: message.Uid,
	})
	if err != nil {
		return application.MailSummary{}, err
	}
	var from application.MailAddress
	subject, receivedAt := "", message.InternalDate.UTC().Format("2006-01-02T15:04:05Z")
	if message.Envelope != nil {
		subject = message.Envelope.Subject
		if len(message.Envelope.From) != 0 {
			from.Name = message.Envelope.From[0].PersonalName
			from.Address = message.Envelope.From[0].Address()
		}
	}
	importance := "normal"
	if hasFlag(message.Flags, imap.FlaggedFlag) ||
		hasFlag(message.Flags, imap.ImportantFlag) {
		importance = "high"
	}
	return application.MailSummary{
		ID: id, ChangeKey: snapshot(status, message), Subject: subject,
		From: from, ReceivedAt: receivedAt, Importance: importance,
		IsRead:         hasFlag(message.Flags, imap.SeenFlag),
		HasAttachments: hasIMAPAttachments(message.BodyStructure),
	}, nil
}

func hasIMAPAttachments(structure *imap.BodyStructure) bool {
	if structure == nil {
		return false
	}
	found := false
	structure.Walk(func(_ []int, part *imap.BodyStructure) bool {
		if strings.EqualFold(part.Disposition, "attachment") ||
			part.DispositionParams["filename"] != "" ||
			part.Params["name"] != "" {
			found = true
			return false
		}
		return !found
	})
	return found
}

func (client *Client) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	var page application.MailFolderPage
	err := client.withIMAP(ctx, func(connection *imapclient.Client) error {
		infos, err := listMailboxes(connection)
		if err != nil {
			return err
		}
		parent := ""
		if input.Parent.Kind == application.MailFolderOpaque {
			parent, err = decodeFolderID(input.Parent.ID)
			if err != nil {
				return err
			}
		} else if !strings.EqualFold(input.Parent.ID, "msgfolderroot") {
			parent, err = client.resolveMailbox(connection, input.Parent)
			if err != nil {
				return err
			}
		}
		sort.Slice(infos, func(left, right int) bool {
			return infos[left].Name < infos[right].Name
		})
		selected := make([]*imap.MailboxInfo, 0, len(infos))
		for _, info := range infos {
			if !mailboxSelectable(info) {
				continue
			}
			if parent == "" ||
				input.Traversal == application.MailFolderTraversalDeep &&
					isMailboxChild(info.Name, parent, info.Delimiter) ||
				input.Traversal == application.MailFolderTraversalShallow &&
					isDirectMailboxChild(info.Name, parent, info.Delimiter) {
				selected = append(selected, info)
			}
		}
		page.TotalFolders = len(selected)
		start := min(input.Offset, len(selected))
		end := min(start+input.Limit, len(selected))
		page.IncludesLastItem = end == len(selected)
		page.Folders = make([]application.MailFolderSummary, 0, end-start)
		for _, info := range selected[start:end] {
			status, statusErr := connection.Status(info.Name, []imap.StatusItem{
				imap.StatusMessages, imap.StatusUnseen, imap.StatusUidValidity,
			})
			if statusErr != nil {
				return statusErr
			}
			parentName := mailboxParent(info.Name, info.Delimiter)
			role := mailboxRole(info)
			page.Folders = append(page.Folders, application.MailFolderSummary{
				ID: encodeFolderID(info.Name),
				ChangeKey: fmt.Sprintf("imfstate1_%d_%d_%d",
					status.UidValidity, status.Messages, status.Unseen),
				ParentID:    parentFolderID(parentName),
				DisplayName: info.Name, Class: "mail", DistinguishedID: role,
				ChildFolderCount: childCount(info.Name, info.Delimiter, infos),
				TotalItemCount:   int(status.Messages), UnreadItemCount: int(status.Unseen),
			})
		}
		return nil
	})
	return page, err
}

func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	reference, err := decodeMessageID(input.MessageID)
	if err != nil {
		return application.MailBody{}, err
	}
	var result application.MailBody
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		status, message, raw, err := fetchRawMessage(connection, reference)
		if err != nil {
			return err
		}
		parsed, err := parseMIME(raw)
		if err != nil {
			return err
		}
		changeKey := snapshot(status, message)
		attachments := make([]application.MailAttachmentMetadata, 0, len(parsed.Attachments))
		for _, attachment := range parsed.Attachments {
			id, err := encodeAttachmentID(attachmentReference{
				Message: input.MessageID, ChangeKey: changeKey, Part: attachment.Part,
				Name: attachment.Name, Type: attachment.ContentType,
				Size: len(attachment.Content), Inline: attachment.Inline,
				CID: attachment.ContentID,
			})
			if err != nil {
				return err
			}
			attachments = append(attachments, application.MailAttachmentMetadata{
				ID: id, Kind: "file", Name: attachment.Name,
				ContentType: attachment.ContentType, Size: len(attachment.Content),
				IsInline: attachment.Inline, ContentID: attachment.ContentID,
			})
		}
		result = application.MailBody{
			ID: input.MessageID, ChangeKey: changeKey,
			Text: parsed.Text, Attachments: attachments,
		}
		return nil
	})
	return result, err
}

func (client *Client) GetMailAttachment(
	ctx context.Context,
	input application.MailAttachmentInput,
) (application.MailAttachment, error) {
	reference, err := decodeAttachmentID(input.AttachmentID)
	if err != nil {
		return application.MailAttachment{}, err
	}
	messageReference, err := decodeMessageID(reference.Message)
	if err != nil {
		return application.MailAttachment{}, err
	}
	var result application.MailAttachment
	err = client.withIMAP(ctx, func(connection *imapclient.Client) error {
		status, message, raw, err := fetchRawMessage(connection, messageReference)
		if err != nil {
			return err
		}
		if snapshot(status, message) != reference.ChangeKey {
			return errors.New("IMAP source message changed before attachment retrieval")
		}
		parsed, err := parseMIME(raw)
		if err != nil {
			return err
		}
		for _, attachment := range parsed.Attachments {
			if attachment.Part != reference.Part ||
				attachment.Name != reference.Name ||
				attachment.ContentType != reference.Type ||
				len(attachment.Content) != reference.Size ||
				attachment.Inline != reference.Inline ||
				attachment.ContentID != reference.CID {
				continue
			}
			result = application.MailAttachment{
				MailAttachmentMetadata: application.MailAttachmentMetadata{
					ID: input.AttachmentID, Kind: "file",
					Name: attachment.Name, ContentType: attachment.ContentType,
					Size: len(attachment.Content), IsInline: attachment.Inline,
					ContentID: attachment.ContentID,
				},
				ContentBase64: encodeBase64(attachment.Content),
			}
			return nil
		}
		return errors.New("IMAP attachment no longer matches its source message")
	})
	return result, err
}

func fetchRawMessage(
	connection *imapclient.Client,
	reference messageReference,
) (*imap.MailboxStatus, *imap.Message, []byte, error) {
	status, err := connection.Select(reference.Mailbox, true)
	if err != nil {
		return nil, nil, nil, err
	}
	if status.UidValidity != reference.UIDValidity {
		return nil, nil, nil, errors.New("IMAP UIDVALIDITY changed")
	}
	section := &imap.BodySectionName{Peek: true}
	messages, err := fetchUIDs(connection, []uint32{reference.UID}, append(
		append([]imap.FetchItem(nil), metadataItems...),
		section.FetchItem(),
	))
	if err != nil {
		return nil, nil, nil, err
	}
	if len(messages) != 1 {
		return nil, nil, nil, errors.New("IMAP message was not found")
	}
	body := messages[0].GetBody(section)
	if body == nil {
		return nil, nil, nil, errors.New("IMAP message body was omitted")
	}
	raw, err := io.ReadAll(io.LimitReader(body, maximumRawMessageBytes+1))
	if err != nil {
		return nil, nil, nil, err
	}
	if len(raw) > maximumRawMessageBytes {
		return nil, nil, nil, fmt.Errorf(
			"IMAP message exceeds %d bytes",
			maximumRawMessageBytes,
		)
	}
	return status, messages[0], raw, nil
}

func fetchUIDs(
	connection *imapclient.Client,
	uids []uint32,
	items []imap.FetchItem,
) ([]*imap.Message, error) {
	set := new(imap.SeqSet)
	for _, uid := range uids {
		set.AddNum(uid)
	}
	return collectFetchedMessages(len(uids), func(messages chan *imap.Message) error {
		return connection.UidFetch(set, items, messages)
	})
}

func collectFetchedMessages(
	maximum int,
	command func(chan *imap.Message) error,
) ([]*imap.Message, error) {
	messages := make(chan *imap.Message)
	done := make(chan error, 1)
	go func() { done <- command(messages) }()
	result := make([]*imap.Message, 0, maximum)
	var limitErr error
	for message := range messages {
		if message == nil {
			limitErr = errors.New("IMAP returned an empty FETCH response")
			continue
		}
		if len(result) >= maximum {
			limitErr = fmt.Errorf(
				"IMAP returned more than %d requested messages",
				maximum,
			)
			continue
		}
		result = append(result, message)
	}
	commandErr := <-done
	if limitErr != nil || commandErr != nil {
		return nil, errors.Join(limitErr, commandErr)
	}
	return result, nil
}

func isMailboxChild(name, parent, delimiter string) bool {
	if delimiter == "" {
		return false
	}
	return strings.HasPrefix(name, parent+delimiter)
}

func isDirectMailboxChild(name, parent, delimiter string) bool {
	if !isMailboxChild(name, parent, delimiter) {
		return false
	}
	return !strings.Contains(strings.TrimPrefix(name, parent+delimiter), delimiter)
}

func mailboxParent(name, delimiter string) string {
	if delimiter == "" {
		return ""
	}
	at := strings.LastIndex(name, delimiter)
	if at < 0 {
		return ""
	}
	return name[:at]
}

func parentFolderID(name string) string {
	if name == "" {
		return ""
	}
	return encodeFolderID(name)
}

func childCount(name, delimiter string, infos []*imap.MailboxInfo) int {
	count := 0
	for _, info := range infos {
		if mailboxSelectable(info) &&
			isDirectMailboxChild(info.Name, name, delimiter) {
			count++
		}
	}
	return count
}

func mailboxSelectable(info *imap.MailboxInfo) bool {
	return info != nil && !hasFlag(info.Attributes, imap.NoSelectAttr)
}

func mailboxRole(info *imap.MailboxInfo) string {
	if strings.EqualFold(info.Name, imap.InboxName) {
		return "inbox"
	}
	for role, attribute := range map[string]string{
		"archive": imap.ArchiveAttr, "deleteditems": imap.TrashAttr,
		"drafts": imap.DraftsAttr, "sentitems": imap.SentAttr,
	} {
		if hasFlag(info.Attributes, attribute) {
			return role
		}
	}
	return ""
}
