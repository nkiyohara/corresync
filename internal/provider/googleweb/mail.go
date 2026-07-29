package googleweb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

type mailReference struct {
	ThreadID string `json:"threadId"`
}

type folderReference struct {
	Fragment string `json:"fragment"`
}

var googleMailFolders = []struct {
	fragment, name, distinguished string
}{
	{"inbox", "Inbox", "inbox"},
	{"all", "All Mail", "msgfolderroot"},
	{"sent", "Sent", "sentitems"},
	{"drafts", "Drafts", "drafts"},
	{"trash", "Trash", "deleteditems"},
}

func (client *Client) ListMailFolders(
	_ context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	if !client.mail {
		return application.MailFolderPage{}, errors.New(
			"google Web mail is not configured",
		)
	}
	start := min(input.Offset, len(googleMailFolders))
	end := min(start+input.Limit, len(googleMailFolders))
	page := application.MailFolderPage{
		Folders:          make([]application.MailFolderSummary, 0, end-start),
		TotalFolders:     len(googleMailFolders),
		IncludesLastItem: end == len(googleMailFolders),
	}
	for _, folder := range googleMailFolders[start:end] {
		id, err := encodeReference(
			"ggwf1_",
			folderReference{Fragment: folder.fragment},
		)
		if err != nil {
			return application.MailFolderPage{}, err
		}
		page.Folders = append(page.Folders, application.MailFolderSummary{
			ID: id, DisplayName: folder.name,
			DistinguishedID: folder.distinguished,
			Class:           "mail",
		})
	}
	return page, nil
}

func (client *Client) ListMessages(
	ctx context.Context,
	input application.MailListInput,
) (application.MailPage, error) {
	fragment, err := client.mailFragment(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	return client.mailRows(ctx, fragment, input.Offset, input.Limit)
}

func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MailSearchInput,
) (application.MailPage, error) {
	fragment, err := client.mailFragment(input.Folder)
	if err != nil {
		return application.MailPage{}, err
	}
	query := strings.TrimSpace("in:" + fragment + " " + input.Query)
	return client.mailRows(
		ctx,
		"search/"+url.PathEscape(query),
		input.Offset,
		input.Limit,
	)
}

func (client *Client) mailRows(
	ctx context.Context,
	fragment string,
	offset, limit int,
) (application.MailPage, error) {
	if !client.mail {
		return application.MailPage{}, errors.New(
			"google Web mail is not configured",
		)
	}
	target := client.mailOrigin.String() + "/mail/u/0/#" + fragment
	snapshot, err := client.driver.GoogleMailRows(ctx, target)
	if err != nil {
		return application.MailPage{}, err
	}
	rows := snapshot.Rows
	start := min(offset, len(rows))
	end := min(start+limit, len(rows))
	page := application.MailPage{
		Messages:         make([]application.MailSummary, 0, end-start),
		TotalItemsInView: len(rows),
		// Gmail virtualizes and pages its DOM. Never claim the visible snapshot
		// is the final remote item.
		IncludesLastItem: false,
	}
	for _, row := range rows[start:end] {
		if err := validateWebValue("thread ID", row.ID, 1024); err != nil {
			return application.MailPage{}, err
		}
		id, err := encodeReference(
			"ggwm1_",
			mailReference{ThreadID: row.ID},
		)
		if err != nil {
			return application.MailPage{}, err
		}
		subject := boundedWebText(row.Subject, 512)
		if subject == "" {
			subject = boundedWebText(row.Text, 512)
		}
		change := sha256.Sum256([]byte(row.ID + "\x00" + row.Text))
		page.Messages = append(page.Messages, application.MailSummary{
			ID: id, ChangeKey: hex.EncodeToString(change[:]),
			Subject: subject,
			From: application.MailAddress{
				Name:    boundedWebText(row.FromName, 320),
				Address: boundedWebText(row.FromAddress, 320),
			},
			IsRead: !row.Unread, HasAttachments: row.HasAttachments,
		})
	}
	return page, nil
}

func (client *Client) GetMessageBody(
	ctx context.Context,
	input application.MailBodyInput,
) (application.MailBody, error) {
	var reference mailReference
	if err := decodeReference(input.MessageID, "ggwm1_", &reference); err != nil ||
		validateWebValue("thread ID", reference.ThreadID, 1024) != nil {
		return application.MailBody{}, errors.New(
			"message ID is not a Google Web thread identifier",
		)
	}
	target := client.mailOrigin.String() + "/mail/u/0/#all/" +
		url.PathEscape(reference.ThreadID)
	text, err := client.driver.GoogleMailBody(ctx, target)
	if err != nil {
		return application.MailBody{}, err
	}
	return application.MailBody{
		ID: input.MessageID, Text: boundedWebText(text, 1<<20),
	}, nil
}

func (client *Client) GetMailAttachment(
	context.Context,
	application.MailAttachmentInput,
) (application.MailAttachment, error) {
	return application.MailAttachment{}, errors.New(
		"google Web attachment retrieval is unavailable; select google-api when permitted",
	)
}

func (client *Client) CreateMailDraft(
	context.Context,
	application.MailDraftInput,
) (application.MailDraft, error) {
	return application.MailDraft{}, googleWebWriteUnavailable()
}

func (client *Client) SendMail(
	context.Context,
	application.MailSendInput,
) (application.MailSendResult, error) {
	return application.MailSendResult{}, googleWebWriteUnavailable()
}

func (client *Client) MoveMail(
	context.Context,
	application.MailMoveInput,
) (application.MailMoveResult, error) {
	return application.MailMoveResult{}, googleWebWriteUnavailable()
}

func (client *Client) SetMailReadState(
	context.Context,
	application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	return application.MailReadStateResult{}, googleWebWriteUnavailable()
}

func (client *Client) DeleteMail(
	context.Context,
	application.MailDeleteInput,
) error {
	return googleWebWriteUnavailable()
}

func (client *Client) mailFragment(folder application.MailFolder) (string, error) {
	switch folder.Kind {
	case application.MailFolderDistinguished:
		for _, candidate := range googleMailFolders {
			if folder.ID == candidate.distinguished {
				return candidate.fragment, nil
			}
		}
		if folder.ID == "archive" {
			return "all", nil
		}
	case application.MailFolderOpaque:
		var reference folderReference
		if decodeReference(folder.ID, "ggwf1_", &reference) == nil {
			for _, candidate := range googleMailFolders {
				if reference.Fragment == candidate.fragment {
					return reference.Fragment, nil
				}
			}
		}
	}
	return "", errors.New("mail folder is not a Google Web identifier")
}

func googleWebWriteUnavailable() error {
	return errors.New(
		"google Web writes are unavailable; use the explicitly consented google-api route when organization policy permits",
	)
}

func validateWebValue(name, value string, maximum int) error {
	if value == "" || len(value) > maximum ||
		strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("google Web returned an invalid " + name)
	}
	return nil
}

func boundedWebText(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		value = value[:maximum]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
