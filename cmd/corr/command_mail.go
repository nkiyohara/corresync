package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

type mailCommand struct {
	Folders    mailFoldersCommand    `cmd:"" help:"Discover mail folders and their opaque IDs."`
	List       mailListCommand       `cmd:"" help:"List message metadata in a folder."`
	Search     mailSearchCommand     `cmd:"" help:"Search message metadata in one folder with the provider query language."`
	Body       mailBodyCommand       `cmd:"" help:"Review and read plain text for one explicit message ID."`
	Attachment mailAttachmentCommand `cmd:"" help:"Review and retrieve one bounded file attachment."`
	Move       mailMoveCommand       `cmd:"" help:"Review and move one versioned message to one folder."`
	Mark       mailMarkCommand       `cmd:"" help:"Review and mark one versioned message read or unread."`
	Delete     mailDeleteCommand     `cmd:"" help:"Review and permanently delete one versioned message."`
	Draft      mailDraftCommand      `cmd:"" help:"Review and save a new, reply, or forward draft without sending."`
	Send       mailSendCommand       `cmd:"" help:"Review and send one new message, reply, or forward."`
	SendDraft  mailSendDraftCommand  `cmd:"" help:"Review and send one exact saved draft version."`
}

type mailFoldersCommand struct {
	Account   string `help:"Configured account alias; defaults to default_account."`
	Parent    string `default:"msgfolderroot" help:"Well-known parent folder name."`
	ParentID  string `name:"parent-id" help:"Opaque parent folder ID; takes precedence over parent."`
	Traversal string `default:"deep" enum:"shallow,deep" help:"Folder hierarchy traversal."`
	Offset    int    `default:"0" help:"Zero-based page offset."`
	Limit     int    `default:"100" help:"Folders to return (1-100)."`
	TimeZone  string `name:"time-zone" default:"UTC" help:"Provider time-zone identifier."`
	JSON      bool   `help:"Write the stable machine-readable schema."`
}

type mailSendCommand struct {
	Account            string   `help:"Configured account alias; defaults to default_account."`
	To                 []string `help:"Bare To recipient; repeat for multiple recipients."`
	CC                 []string `name:"cc" help:"Bare Cc recipient; repeat for multiple recipients."`
	BCC                []string `name:"bcc" help:"Bare Bcc recipient; repeat for multiple recipients."`
	Subject            string   `help:"Message subject; CR/LF are rejected."`
	BodyFile           string   `name:"body-file" help:"Text or HTML body file, or - for stdin."`
	BodyFormat         string   `name:"body-format" default:"text" enum:"text,html" help:"Message body format."`
	Mode               string   `default:"new" enum:"new,reply,reply-all,forward" help:"Composition mode."`
	ReferenceMessageID string   `name:"reference-message-id" help:"Exact source message ID for replies or forwards."`
	ReferenceChangeKey string   `name:"reference-change-key" help:"Exact source change key for replies or forwards."`
	Attachments        []string `name:"attachment" type:"path" help:"File to attach; repeat as needed."`
	Approve            bool     `help:"Send the exact preview generated from these arguments."`
	JSON               bool     `help:"Write the stable machine-readable schema."`
}

type mailSendDraftCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	DraftID        string `name:"draft-id" help:"Exact saved draft ID returned by mail draft or mail list."`
	DraftChangeKey string `name:"draft-change-key" help:"Exact saved draft change key returned with the draft ID."`
	Approve        bool   `help:"Send the exact saved draft preview generated from these arguments."`
	JSON           bool   `help:"Write the stable machine-readable schema."`
}

type mailDraftCommand struct {
	Account            string   `help:"Configured account alias; defaults to default_account."`
	To                 []string `help:"Bare To recipient; repeat for multiple recipients."`
	CC                 []string `name:"cc" help:"Bare Cc recipient; repeat for multiple recipients."`
	BCC                []string `name:"bcc" help:"Bare Bcc recipient; repeat for multiple recipients."`
	Subject            string   `help:"Draft subject; CR/LF are rejected."`
	BodyFile           string   `name:"body-file" help:"Text or HTML body file, or - for stdin."`
	BodyFormat         string   `name:"body-format" default:"text" enum:"text,html" help:"Draft body format."`
	Mode               string   `default:"new" enum:"new,reply,reply-all,forward" help:"Composition mode."`
	ReferenceMessageID string   `name:"reference-message-id" help:"Exact source message ID for replies or forwards."`
	ReferenceChangeKey string   `name:"reference-change-key" help:"Exact source change key for replies or forwards."`
	Attachments        []string `name:"attachment" type:"path" help:"File to attach; repeat as needed."`
	Approve            bool     `help:"Save the exact preview generated from these arguments when policy requires approval."`
	JSON               bool     `help:"Write the stable machine-readable schema."`
}

type mailBodyCommand struct {
	Account   string `help:"Configured account alias; defaults to default_account."`
	MessageID string `name:"message-id" help:"Exact message ID returned by mail list (required)."`
	Approve   bool   `help:"Commit an in-process preview when sensitive reads require approval."`
	JSON      bool   `help:"Write the stable machine-readable schema."`
}

type mailAttachmentCommand struct {
	Account      string `help:"Configured account alias; defaults to default_account."`
	AttachmentID string `name:"attachment-id" help:"Exact attachment ID returned by mail body (required)."`
	Output       string `type:"path" help:"New local output file; existing files are never overwritten."`
	Approve      bool   `help:"Commit an in-process preview when sensitive reads require approval."`
	JSON         bool   `help:"Write base64 content in the stable machine-readable schema instead of a file."`
}

type mailListCommand struct {
	Account  string `help:"Configured account alias; defaults to default_account."`
	Folder   string `default:"inbox" help:"Well-known folder name."`
	FolderID string `name:"folder-id" help:"Opaque folder ID from folder discovery."`
	Offset   int    `default:"0" help:"Zero-based page offset."`
	Limit    int    `default:"25" help:"Messages to return (1-100)."`
	TimeZone string `name:"time-zone" default:"UTC" help:"Provider time-zone identifier."`
	JSON     bool   `help:"Write the stable machine-readable schema."`
}

type mailSearchCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	AllAccounts bool   `name:"all-accounts" help:"Search every configured mail account as an isolated read-only projection."`
	Folder      string `default:"inbox" help:"Well-known folder name."`
	FolderID    string `name:"folder-id" help:"Opaque folder ID from folder discovery; unavailable with all-accounts."`
	Query       string `help:"Provider search query (required; 1-1024 UTF-8 bytes)."`
	Offset      int    `default:"0" help:"Zero-based page offset."`
	Limit       int    `default:"25" help:"Messages to return (1-50)."`
	TimeZone    string `name:"time-zone" default:"UTC" help:"Provider time-zone identifier; all-accounts requires UTC."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type mailMoveCommand struct {
	Account       string `help:"Configured account alias; defaults to default_account."`
	MessageID     string `name:"message-id" help:"Exact message ID returned by mail list or search."`
	ChangeKey     string `name:"change-key" help:"Exact change key returned with the message ID."`
	Destination   string `default:"archive" help:"Well-known destination folder."`
	DestinationID string `name:"destination-id" help:"Opaque destination folder ID; takes precedence."`
	Approve       bool   `help:"Move the exact preview generated from these arguments when policy requires approval."`
	JSON          bool   `help:"Write the stable machine-readable schema."`
}

type mailMarkCommand struct {
	Account   string `help:"Configured account alias; defaults to default_account."`
	MessageID string `name:"message-id" help:"Exact message ID returned by mail list or search."`
	ChangeKey string `name:"change-key" help:"Exact change key returned with the message ID."`
	State     string `help:"Required target state: read or unread."`
	Approve   bool   `help:"Apply the exact preview generated from these arguments when policy requires approval."`
	JSON      bool   `help:"Write the stable machine-readable schema."`
}

type mailDeleteCommand struct {
	Account   string `help:"Configured account alias; defaults to default_account."`
	MessageID string `name:"message-id" help:"Exact message ID returned by mail list or search."`
	ChangeKey string `name:"change-key" help:"Exact change key returned with the message ID."`
	Approve   bool   `help:"Permanently delete the exact reviewed message version."`
	JSON      bool   `help:"Write the stable machine-readable schema."`
}

func (command *mailFoldersCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	parent := application.MailFolder{Kind: application.MailFolderDistinguished, ID: command.Parent}
	if command.ParentID != "" {
		parent = application.MailFolder{Kind: application.MailFolderOpaque, ID: command.ParentID}
	}
	input := application.MailFolderListInput{
		Account: accountID, Parent: parent,
		Traversal: application.MailFolderTraversal(command.Traversal),
		Offset:    command.Offset, Limit: command.Limit, TimeZone: command.TimeZone,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListMailFolders(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMailFolderTable(app, page)
}

func (command *mailListCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	folder := application.MailFolder{Kind: application.MailFolderDistinguished, ID: command.Folder}
	if command.FolderID != "" {
		folder = application.MailFolder{Kind: application.MailFolderOpaque, ID: command.FolderID}
	}
	input := application.MailListInput{
		Account:  accountID,
		Folder:   folder,
		Offset:   command.Offset,
		Limit:    command.Limit,
		TimeZone: command.TimeZone,
	}
	if err := input.Validate(); err != nil {
		return err
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListMail(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMailTable(app, page)
}

func (command *mailSearchCommand) Run(app *runtime) (returnErr error) {
	if command.AllAccounts {
		if command.Account != "" {
			return errors.New(
				"mail search --all-accounts cannot select one account",
			)
		}
		if command.FolderID != "" {
			return errors.New(
				"mail search --all-accounts cannot reuse one account-specific folder ID",
			)
		}
		input := application.MailProjectionInput{
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   command.Folder,
			},
			Query: command.Query, Offset: command.Offset,
			Limit: command.Limit, TimeZone: command.TimeZone,
		}
		if err := input.Validate(); err != nil {
			return err
		}
		client, _, err := app.openDaemon(app.context)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
		page, err := client.SearchAllMail(
			app.context,
			input,
			app.caller(),
		)
		if err != nil {
			return err
		}
		if command.JSON {
			return writeJSON(app.stdout, page)
		}
		return writeMailProjectionTable(app, page)
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	folder := application.MailFolder{Kind: application.MailFolderDistinguished, ID: command.Folder}
	if command.FolderID != "" {
		folder = application.MailFolder{Kind: application.MailFolderOpaque, ID: command.FolderID}
	}
	input := application.MailSearchInput{
		Account: accountID, Folder: folder, Query: command.Query,
		Offset: command.Offset, Limit: command.Limit, TimeZone: command.TimeZone,
	}
	if err := input.Validate(); err != nil {
		return err
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.SearchMail(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMailTable(app, page)
}

func writeMailProjectionTable(
	app *runtime,
	page application.MailProjectionPage,
) error {
	if len(page.Messages) == 0 {
		if _, err := fmt.Fprintln(app.stdout, "No messages."); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(
			writer,
			"ACCOUNT\tPROVIDER\tRECEIVED\tFROM\tSUBJECT\tFLAGS\tID",
		); err != nil {
			return err
		}
		for _, projected := range page.Messages {
			message := projected.Message
			from := message.From.Name
			if from == "" {
				from = message.From.Address
			}
			flags := ""
			if !message.IsRead {
				flags += "U"
			}
			if message.HasAttachments {
				flags += "A"
			}
			if _, err := fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sanitizeCell(projected.AccountAlias, 64),
				sanitizeCell(string(message.Provenance.Provider), 64),
				sanitizeCell(message.ReceivedAt, 30),
				sanitizeCell(from, 32),
				sanitizeCell(message.Subject, 72),
				flags,
				sanitizeCell(message.ID, 4096),
			); err != nil {
				return err
			}
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	for _, status := range page.Accounts {
		for _, degradation := range status.Degradations {
			if _, err := view.printf(
				"%s  %s (%s): %s\n",
				view.warning(),
				sanitizeCell(status.Alias, 64),
				sanitizeCell(string(status.Provider), 64),
				sanitizeCell(degradation.Reason, 512),
			); err != nil {
				return err
			}
		}
	}
	for _, failure := range page.Failures {
		if _, err := view.printf(
			"%s  Incomplete: %s (%s) · %s\n",
			view.warning(),
			sanitizeCell(failure.Alias, 64),
			sanitizeCell(string(failure.Provider), 64),
			sanitizeCell(failure.Reason, 512),
		); err != nil {
			return err
		}
	}
	return nil
}

func (command *mailMoveCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	destination := application.MailFolder{
		Kind: application.MailFolderDistinguished, ID: command.Destination,
	}
	if command.DestinationID != "" {
		destination = application.MailFolder{Kind: application.MailFolderOpaque, ID: command.DestinationID}
	}
	input := application.MailMoveInput{
		Account: accountID, MessageID: command.MessageID, ChangeKey: command.ChangeKey,
		Destination: destination,
	}
	if err := input.Validate(); err != nil {
		return err
	}

	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.MoveMail(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("mail move required approval without returning a preview")
		}
		if !command.Approve {
			if command.JSON {
				return writeJSON(app.stdout, access)
			}
			return writeMailMoveReview(app.stdout, access.Review, false)
		}
		if err := writeMailMoveReview(app.stderr, access.Review, true); err != nil {
			return err
		}
		access, err = client.CommitMailMove(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if access.Moved == nil {
		return errors.New("mail move completed without a result")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	if access.Moved.ID == "" {
		_, err = fmt.Fprintf(
			app.stdout, "Moved message to %s; the provider returned no new ID, so list the destination to refresh it.\n",
			sanitizeCell(moveDestinationLabel(access.Review.Destination), 120),
		)
		return err
	}
	_, err = fmt.Fprintf(
		app.stdout, "Moved message to %s; new ID: %s\n",
		sanitizeCell(moveDestinationLabel(access.Review.Destination), 120),
		sanitizeCell(access.Moved.ID, 4096),
	)
	return err
}

func moveDestinationLabel(folder application.MailFolder) string {
	if folder.Kind == application.MailFolderOpaque {
		return "folder-id:" + folder.ID
	}
	return folder.ID
}

func (command *mailMarkCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailReadStateInput{
		Account: accountID, MessageID: command.MessageID, ChangeKey: command.ChangeKey,
		State: application.MailReadState(command.State),
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.SetMailReadState(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("mail read-state update required approval without returning a preview")
		}
		if !command.Approve {
			if command.JSON {
				return writeJSON(app.stdout, access)
			}
			return writeMailReadStateReview(app.stdout, access.Review, false)
		}
		if err := writeMailReadStateReview(app.stderr, access.Review, true); err != nil {
			return err
		}
		access, err = client.CommitMailReadState(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if access.Updated == nil {
		return errors.New("mail read-state update completed without a result")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout, "Marked message %s.\n", sanitizeCell(string(access.Updated.State), 16),
	)
	return err
}

func (command *mailDeleteCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailDeleteInput{
		Account: accountID, MessageID: command.MessageID, ChangeKey: command.ChangeKey,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.DeleteMail(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("mail delete did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		return writeMailDeleteReview(app.stdout, access.Review, false)
	}
	if err := writeMailDeleteReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitMailDelete(app.context, access.Preview.Token, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "deleted" || access.Deleted == nil {
		return errors.New("mail delete commit completed without deleted status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintln(app.stdout, "Permanently deleted the exact message version.")
	return err
}

func (command *mailBodyCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailBodyInput{Account: accountID, MessageID: command.MessageID}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.GetMailBody(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("mail body read required approval without returning a preview")
		}
		if !command.Approve {
			if command.JSON {
				return writeJSON(app.stdout, access)
			}
			return writeMailBodyReview(app.stdout, command.MessageID, false)
		}
		if err := writeMailBodyReview(app.stderr, command.MessageID, true); err != nil {
			return err
		}
		access, err = client.CommitMailBody(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if access.Body == nil {
		return errors.New("mail body operation completed without a body")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Message %s (private, untrusted content):\n%s\n",
		sanitizeCell(access.Body.ID, 80),
		sanitizeTerminalText(access.Body.Text),
	)
	if err != nil {
		return err
	}
	if len(access.Body.Attachments) != 0 {
		if _, err := fmt.Fprintln(app.stdout, "Attachments (private, untrusted metadata):"); err != nil {
			return err
		}
		for _, attachment := range access.Body.Attachments {
			if _, err := fmt.Fprintf(
				app.stdout, "- %s (%s, %s, %d bytes%s): %s\n",
				sanitizeCell(attachment.Name, 128), sanitizeCell(attachment.Kind, 16),
				sanitizeCell(attachment.ContentType, 128),
				attachment.Size, inlineAttachmentLabel(attachment.IsInline),
				sanitizeCell(attachment.ID, 4096),
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (command *mailAttachmentCommand) Run(app *runtime) (returnErr error) {
	if !command.JSON && command.Output == "" {
		return errors.New("output is required unless json is selected")
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailAttachmentInput{Account: accountID, AttachmentID: command.AttachmentID}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.GetMailAttachment(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("mail attachment read required approval without returning a preview")
		}
		if !command.Approve {
			if command.JSON {
				return writeJSON(app.stdout, access)
			}
			return writeMailAttachmentReview(app.stdout, command.AttachmentID, false)
		}
		if err := writeMailAttachmentReview(app.stderr, command.AttachmentID, true); err != nil {
			return err
		}
		access, err = client.CommitMailAttachment(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if access.Attachment == nil {
		return errors.New("mail attachment operation completed without content")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	content, err := base64.StdEncoding.DecodeString(access.Attachment.ContentBase64)
	if err != nil || len(content) > application.MaxMailAttachmentBytes {
		return errors.New("mail attachment response contained malformed base64 content")
	}
	file, err := os.OpenFile(command.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit CLI output.
	if err != nil {
		return fmt.Errorf("create attachment output: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write attachment output: %w", err)
	}
	_, err = fmt.Fprintf(
		app.stdout, "Saved %d-byte attachment to %s.\n",
		len(content), sanitizeCell(command.Output, 4096),
	)
	return err
}

func inlineAttachmentLabel(inline bool) string {
	if inline {
		return ", inline"
	}
	return ""
}

func (command *mailDraftCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	body, err := readDraftBody(app, command.BodyFile)
	if err != nil {
		return err
	}
	attachments, err := readMailAttachments(command.Attachments)
	if err != nil {
		return err
	}
	input := application.MailDraftInput{
		Account: accountID, To: command.To, CC: command.CC, BCC: command.BCC,
		Subject: command.Subject, Body: body,
		BodyFormat:         application.MailBodyFormat(command.BodyFormat),
		ComposeMode:        parseMailComposeMode(command.Mode),
		ReferenceMessageID: command.ReferenceMessageID,
		ReferenceChangeKey: command.ReferenceChangeKey,
		Attachments:        attachments,
	}
	if err := input.Validate(configuration.Policy.MaxRecipients); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.CreateMailDraft(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("mail draft required approval without returning a preview")
		}
		if !command.Approve {
			if command.JSON {
				return writeJSON(app.stdout, access)
			}
			return writeDraftReview(app.stdout, access.Review, false)
		}
		if err := writeDraftReview(app.stderr, access.Review, true); err != nil {
			return err
		}
		access, err = client.CommitMailDraft(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if access.Draft == nil {
		return errors.New("mail draft operation completed without a draft ID")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Saved draft %s for %d recipient(s); nothing was sent.\n",
		sanitizeCell(access.Draft.ID, 80),
		len(access.Review.To)+len(access.Review.CC)+len(access.Review.BCC),
	)
	return err
}

func (command *mailSendCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	body, err := readDraftBody(app, command.BodyFile)
	if err != nil {
		return err
	}
	attachments, err := readMailAttachments(command.Attachments)
	if err != nil {
		return err
	}
	input := application.MailSendInput{
		Account: accountID, To: command.To, CC: command.CC, BCC: command.BCC,
		Subject: command.Subject, Body: body,
		BodyFormat:         application.MailBodyFormat(command.BodyFormat),
		ComposeMode:        parseMailComposeMode(command.Mode),
		ReferenceMessageID: command.ReferenceMessageID,
		ReferenceChangeKey: command.ReferenceChangeKey,
		Attachments:        attachments,
	}
	if err := input.Validate(configuration.Policy.MaxRecipients); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.SendMail(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("mail send did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		return writeSendReview(app.stdout, access.Review, false)
	}
	if err := writeSendReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitMailSend(app.context, access.Preview.Token, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "sent" || access.Sent == nil {
		return errors.New("mail send commit completed without sent status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout, "Sent message to %d recipient(s); the network request was attempted once.\n",
		len(access.Review.To)+len(access.Review.CC)+len(access.Review.BCC),
	)
	return err
}

func (command *mailSendDraftCommand) Run(app *runtime) (returnErr error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailDraftSendInput{
		Account: accountID, DraftID: command.DraftID,
		DraftChangeKey: command.DraftChangeKey,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.SendMailDraft(app.context, input, app.caller())
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("saved draft send did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		return writeSendDraftReview(app.stdout, access.Review, false)
	}
	if err := writeSendDraftReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = client.CommitMailSendDraft(
		app.context,
		access.Preview.Token,
		app.caller(),
	)
	if err != nil {
		return err
	}
	if access.Status != "sent" || access.Sent == nil {
		return errors.New("saved draft send commit completed without sent status")
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Sent saved draft %s to %d recipient(s); the exact version was attempted once.\n",
		sanitizeCell(access.Review.DraftID, 80),
		len(access.Review.To)+len(access.Review.CC)+len(access.Review.BCC),
	)
	return err
}

func writeSendReview(writer io.Writer, review application.MailReview, committing bool) error {
	action := "Preview only; nothing was sent. Rerun with --approve to send this exact content."
	if committing {
		action = "Committing this exact send now."
	}
	return writeMailContentReview(writer, review, action)
}

func writeDraftReview(writer io.Writer, review application.MailReview, committing bool) error {
	action := "Preview only; no draft was saved. Rerun with --approve to save this exact draft."
	if committing {
		action = "Saving this exact draft now; no message will be sent."
	}
	return writeMailContentReview(writer, review, action)
}

func writeSendDraftReview(
	writer io.Writer,
	review application.MailDraftSendReview,
	committing bool,
) error {
	action := "Preview only; the saved draft was not sent. Rerun with --approve to send this exact version."
	if committing {
		action = "Committing this exact saved draft version now."
	}
	_, err := fmt.Fprintf(
		writer,
		"%s\nDraft ID: %s\nChange key: %s\nTo: %s\nCc: %s\nBcc: %s\nSubject: %s\nBody format: %s\nBody (%d bytes, SHA-256 %s):\n%s\n",
		action,
		sanitizeCell(review.DraftID, 4096),
		sanitizeCell(review.DraftChangeKey, 4096),
		sanitizeCell(strings.Join(review.To, ", "), 512),
		sanitizeCell(strings.Join(review.CC, ", "), 512),
		sanitizeCell(strings.Join(review.BCC, ", "), 512),
		sanitizeCell(review.Subject, 998),
		sanitizeCell(string(review.BodyFormat), 16),
		review.BodyBytes,
		sanitizeCell(review.BodySHA256, 64),
		sanitizeTerminalText(review.BodyPreview),
	)
	if err != nil {
		return err
	}
	for _, attachment := range review.Attachments {
		if _, err := fmt.Fprintf(
			writer,
			"Attachment: %s (%s, %d bytes, inline=%t)\n",
			sanitizeCell(attachment.Name, 255),
			sanitizeCell(attachment.ContentType, 255),
			attachment.Bytes,
			attachment.Inline,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeMailContentReview(
	writer io.Writer,
	review application.MailReview,
	action string,
) error {
	_, err := fmt.Fprintf(
		writer,
		"%s\nMode: %s\nTo: %s\nCc: %s\nBcc: %s\nSubject: %s\nBody format: %s\nBody (%d bytes, SHA-256 %s):\n%s\n",
		action,
		sanitizeCell(string(review.ComposeMode), 16),
		sanitizeCell(strings.Join(review.To, ", "), 512),
		sanitizeCell(strings.Join(review.CC, ", "), 512),
		sanitizeCell(strings.Join(review.BCC, ", "), 512),
		sanitizeCell(review.Subject, 998), sanitizeCell(string(review.BodyFormat), 16), review.BodyBytes,
		sanitizeCell(review.BodySHA256, 64), sanitizeTerminalText(review.BodyPreview),
	)
	if err != nil {
		return err
	}
	for _, attachment := range review.Attachments {
		if _, err := fmt.Fprintf(
			writer, "Attachment: %s (%s, %d bytes, SHA-256 %s)\n",
			sanitizeCell(attachment.Name, 255), sanitizeCell(attachment.ContentType, 255),
			attachment.Bytes, sanitizeCell(attachment.SHA256, 64),
		); err != nil {
			return err
		}
	}
	return nil
}

func parseMailComposeMode(value string) application.MailComposeMode {
	if value == "reply-all" {
		return application.MailComposeReplyAll
	}
	return application.MailComposeMode(value)
}

func readMailAttachments(paths []string) ([]application.MailFileAttachment, error) {
	if len(paths) > application.MaxMailAttachments {
		return nil, fmt.Errorf("mail has %d attachments; maximum is %d", len(paths), application.MaxMailAttachments)
	}
	attachments := make([]application.MailFileAttachment, 0, len(paths))
	var totalBytes int64
	for _, path := range paths {
		file, err := os.Open(path) // #nosec G304 -- each path is an explicit CLI argument.
		if err != nil {
			return nil, fmt.Errorf("open mail attachment %q: %w", path, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("inspect mail attachment %q: %w", path, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > application.MaxMailAttachmentBytes {
			_ = file.Close()
			return nil, fmt.Errorf("mail attachment %q must be a regular file between 1 and %d bytes", path, application.MaxMailAttachmentBytes)
		}
		totalBytes += info.Size()
		if totalBytes > application.MaxMailAttachmentTotalBytes {
			_ = file.Close()
			return nil, fmt.Errorf(
				"mail attachments total %d bytes; maximum is %d",
				totalBytes, application.MaxMailAttachmentTotalBytes,
			)
		}
		content, readErr := io.ReadAll(io.LimitReader(file, application.MaxMailAttachmentBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read mail attachment %q: %w", path, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close mail attachment %q: %w", path, closeErr)
		}
		contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if contentType == "" {
			contentType = http.DetectContentType(content)
		}
		attachments = append(attachments, application.MailFileAttachment{
			Name: filepath.Base(path), ContentType: contentType, Content: content,
		})
	}
	return attachments, nil
}

func writeMailMoveReview(
	writer io.Writer,
	review application.MailMoveReview,
	committing bool,
) error {
	action := "Preview only; no message was moved. Rerun with --approve to move this exact version."
	if committing {
		action = "Moving this exact message version now."
	}
	_, err := fmt.Fprintf(
		writer, "%s\nMessage ID: %s\nChange key: %s\nDestination: %s\n",
		action,
		sanitizeCell(review.MessageID, 4096),
		sanitizeCell(review.ChangeKey, 4096),
		sanitizeCell(moveDestinationLabel(review.Destination), 4096),
	)
	return err
}

func writeMailReadStateReview(
	writer io.Writer,
	review application.MailReadStateReview,
	committing bool,
) error {
	action := "Preview only; read state was not changed. Rerun with --approve to change this exact version."
	if committing {
		action = "Changing this exact message version now."
	}
	_, err := fmt.Fprintf(
		writer, "%s\nMessage ID: %s\nChange key: %s\nTarget state: %s\n",
		action,
		sanitizeCell(review.MessageID, 4096),
		sanitizeCell(review.ChangeKey, 4096),
		sanitizeCell(string(review.State), 16),
	)
	return err
}

func writeMailDeleteReview(writer io.Writer, review application.MailDeleteReview, committing bool) error {
	action := "Preview only; nothing was deleted. Rerun with --approve to permanently delete this exact version."
	if committing {
		action = "Permanently deleting this exact message version now; the selected provider cannot undo it."
	}
	_, err := fmt.Fprintf(
		writer, "%s\nMessage ID: %s\nChange key: %s\nDelete type: %s\n",
		action, sanitizeCell(review.MessageID, 4096), sanitizeCell(review.ChangeKey, 4096),
		sanitizeCell(review.DeleteType, 32),
	)
	return err
}

func writeMailBodyReview(writer io.Writer, messageID string, committing bool) error {
	action := "Preview only; the private body was not read. Rerun with --approve to read this exact message."
	if committing {
		action = "Reading the private body of this exact message now."
	}
	_, err := fmt.Fprintf(
		writer, "%s\nMessage ID: %s\n",
		action, sanitizeCell(messageID, 4096),
	)
	return err
}

func writeMailAttachmentReview(writer io.Writer, attachmentID string, committing bool) error {
	action := "Preview only; the private attachment was not read. Rerun with --approve to retrieve it."
	if committing {
		action = "Reading this private attachment now."
	}
	_, err := fmt.Fprintf(
		writer, "%s\nAttachment ID: %s\n",
		action, sanitizeCell(attachmentID, 4096),
	)
	return err
}

func readDraftBody(app *runtime, path string) (body string, returnErr error) {
	return readPlainTextBody(app, path, "draft")
}

func readPlainTextBody(
	app *runtime,
	path string,
	kind string,
) (body string, returnErr error) {
	const maximumBytes = application.MaxMailDraftBodyBytes
	if path == "" {
		return "", nil
	}
	reader := app.stdin
	var file *os.File
	if path != "-" {
		opened, err := os.Open(path) // #nosec G304 -- the path is explicit CLI input.
		if err != nil {
			return "", fmt.Errorf("open %s body: %w", kind, err)
		}
		file = opened
		reader = file
	}
	if file != nil {
		defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximumBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s body: %w", kind, err)
	}
	if int64(len(data)) > maximumBytes {
		return "", fmt.Errorf("%s body exceeds %d bytes", kind, maximumBytes)
	}
	return string(data), nil
}

func writeMailTable(app *runtime, page application.MailPage) error {
	if len(page.Messages) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No messages.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "RECEIVED\tFROM\tSUBJECT\tFLAGS\tID"); err != nil {
		return err
	}
	for _, message := range page.Messages {
		from := message.From.Name
		if from == "" {
			from = message.From.Address
		}
		flags := ""
		if !message.IsRead {
			flags += "U"
		}
		if message.HasAttachments {
			flags += "A"
		}
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(message.ReceivedAt, 30),
			sanitizeCell(from, 32),
			sanitizeCell(message.Subject, 72),
			flags,
			sanitizeCell(message.ID, 4096),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeMailFolderTable(app *runtime, page application.MailFolderPage) error {
	if len(page.Folders) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No folders.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tCLASS\tITEMS\tUNREAD\tCHILDREN\tID"); err != nil {
		return err
	}
	for _, folder := range page.Folders {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%d\t%d\t%d\t%s\n",
			sanitizeCell(folder.DisplayName, 64),
			sanitizeCell(folder.Class, 32),
			folder.TotalItemCount,
			folder.UnreadItemCount,
			folder.ChildFolderCount,
			sanitizeCell(folder.ID, 4096),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func sanitizeCell(value string, maximumRunes int) string {
	value = stripTerminalSequences(value)
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) <= maximumRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximumRunes-1]) + "…"
}

func stripTerminalSequences(value string) string {
	result := make([]byte, 0, len(value))
	for index := 0; index < len(value); {
		if value[index] != 0x1b {
			result = append(result, value[index])
			index++
			continue
		}
		index++
		if index >= len(value) {
			break
		}
		switch value[index] {
		case '[': // Control Sequence Introducer.
			index++
			for index < len(value) {
				final := value[index]
				index++
				if final >= 0x40 && final <= 0x7e {
					break
				}
			}
		case ']': // Operating System Command terminated by BEL or ST.
			index++
			for index < len(value) {
				if value[index] == 0x07 {
					index++
					break
				}
				if value[index] == 0x1b && index+1 < len(value) && value[index+1] == '\\' {
					index += 2
					break
				}
				index++
			}
		default:
			index++
		}
	}
	return string(result)
}

func sanitizeTerminalText(value string) string {
	value = stripTerminalSequences(value)
	return strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return character
		}
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
}
