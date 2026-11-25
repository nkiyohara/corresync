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
	"github.com/nkiyohara/corresync/internal/daemonapi"
)

type mailCommand struct {
	Folders    mailFoldersCommand    `cmd:"" help:"List mail folders and their IDs."`
	List       mailListCommand       `cmd:"" help:"List messages in a folder."`
	Search     mailSearchCommand     `cmd:"" help:"Search messages in a folder, or in every account."`
	Body       mailBodyCommand       `cmd:"" help:"Read the text of a message."`
	Attachment mailAttachmentCommand `cmd:"" help:"Save a message attachment to a file."`
	Move       mailMoveCommand       `cmd:"" help:"Move a message to another folder."`
	Mark       mailMarkCommand       `cmd:"" help:"Mark a message as read or unread."`
	Delete     mailDeleteCommand     `cmd:"" help:"Delete a message permanently."`
	Draft      mailDraftCommand      `cmd:"" help:"Save a new message, reply, reply-all, or forward as a draft."`
	Send       mailSendCommand       `cmd:"" help:"Send a new message, or forward one."`
	SendDraft  mailSendDraftCommand  `cmd:"" help:"Send a saved draft."`
}

var mailHelpExamples = []helpExample{
	{"Search the inbox of every account:", []string{"mail", "search", "--all-accounts", "--query", "quarterly report"}},
	{"Read a message (IDs come from corr mail list --json):", []string{"mail", "body", "--message-id", "AAMkAGQ5NjE0Yz-message"}},
	{"Save a reply-all as a draft; nothing is sent:", []string{
		"mail", "draft", "--mode", "reply-all", "--reference-message-id", "AAMkAGQ5NjE0Yz-message",
		"--reference-change-key", "CQAAABYAAAD-key", "--body-file", "reply.txt",
	}},
	{"Send a new message (the first run shows a preview):", []string{
		"mail", "send", "--to", "ana@example.invalid", "--subject", "Lunch on Friday?", "--body-file", "note.txt",
	}},
}

// Help is the detail text of `corr mail --help`.
func (mailCommand) Help() string {
	return helpWithExamples(`
Commands that change mail show a preview first and change nothing. Run the
same command again with --approve added to carry it out; the preview ends with
that command. Sending and deleting always show a preview. Moving, marking, and
saving a draft show one when the preview_reversible_writes setting is on, and
reading a message or attachment does when preview_sensitive_reads is on.

Message IDs and change keys come from corr mail list --json or corr mail
search --json. A change key names one version of a message: if the message
changed after you listed it, list it again.

To answer a message, save a draft first: --mode reply addresses only the sender
(or Reply-To), and --mode reply-all keeps everyone else on To and Cc. Check the
recipients the draft shows, then send it with corr mail send-draft.`, mailHelpExamples)
}

type mailFoldersCommand struct {
	Account   string `help:"Account alias (default: your default account)."`
	Parent    string `default:"msgfolderroot" help:"Folder to list under, by name; the default is the top of the mailbox."`
	ParentID  string `name:"parent-id" placeholder:"ID" help:"Folder to list under, by ID; overrides --parent."`
	Traversal string `default:"deep" enum:"shallow,deep" help:"shallow lists direct subfolders only; deep lists all of them."`
	Offset    int    `default:"0" help:"Skip this many results, for paging."`
	Limit     int    `default:"100" help:"Most folders to show (1-100)."`
	TimeZone  string `name:"time-zone" default:"UTC" help:"Time zone the provider uses for dates."`
	JSON      bool   `help:"Print JSON for scripts and agents."`
}

type mailSendCommand struct {
	Account            string   `help:"Account alias (default: your default account)."`
	To                 []string `placeholder:"ADDRESS" help:"Recipient address; repeat for more."`
	CC                 []string `name:"cc" placeholder:"ADDRESS" help:"Cc address; repeat for more."`
	BCC                []string `name:"bcc" placeholder:"ADDRESS" help:"Bcc address; repeat for more."`
	Subject            string   `help:"Subject, on one line."`
	BodyFile           string   `name:"body-file" placeholder:"FILE" help:"File with the message text or HTML, or - to read standard input."`
	BodyFormat         string   `name:"body-format" default:"text" enum:"text,html" help:"Format of the body file: text or html."`
	Mode               string   `default:"new" enum:"new,reply,reply-all,forward" help:"new or forward: you give the recipients. To reply (sender or Reply-To only) or reply-all (sender plus the other To/Cc), use corr mail draft, then send-draft."`
	ReferenceMessageID string   `name:"reference-message-id" placeholder:"ID" help:"ID of the message to forward, from corr mail list --json."`
	ReferenceChangeKey string   `name:"reference-change-key" placeholder:"KEY" help:"Change key of that message, from the same output."`
	Attachments        []string `name:"attachment" type:"path" placeholder:"FILE" help:"File to attach; repeat for more."`
	Approve            bool     `help:"Send the message instead of showing a preview."`
	JSON               bool     `help:"Print JSON for scripts and agents."`
}

type mailSendDraftCommand struct {
	Account        string `help:"Account alias (default: your default account)."`
	DraftID        string `name:"draft-id" placeholder:"ID" help:"Draft ID, printed by corr mail draft or listed by corr mail list --folder drafts --json."`
	DraftChangeKey string `name:"draft-change-key" placeholder:"KEY" help:"Change key of that draft; the draft must not have changed since."`
	Approve        bool   `help:"Send the draft instead of showing a preview."`
	JSON           bool   `help:"Print JSON for scripts and agents."`
}

type mailDraftCommand struct {
	Account            string   `help:"Account alias (default: your default account)."`
	To                 []string `placeholder:"ADDRESS" help:"Recipient address; repeat for more."`
	CC                 []string `name:"cc" placeholder:"ADDRESS" help:"Cc address; repeat for more."`
	BCC                []string `name:"bcc" placeholder:"ADDRESS" help:"Bcc address; repeat for more."`
	Subject            string   `help:"Subject, on one line."`
	BodyFile           string   `name:"body-file" placeholder:"FILE" help:"File with the draft text or HTML, or - to read standard input."`
	BodyFormat         string   `name:"body-format" default:"text" enum:"text,html" help:"Format of the body file: text or html."`
	Mode               string   `default:"new" enum:"new,reply,reply-all,forward" help:"Who the draft goes to: new or forward (the recipients you give), reply (sender or Reply-To only), or reply-all (sender plus the other To/Cc). Replies take their recipients from the original message."`
	ReferenceMessageID string   `name:"reference-message-id" placeholder:"ID" help:"ID of the message to reply to or forward, from corr mail list --json."`
	ReferenceChangeKey string   `name:"reference-change-key" placeholder:"KEY" help:"Change key of that message, from the same output."`
	Attachments        []string `name:"attachment" type:"path" placeholder:"FILE" help:"File to attach; repeat for more."`
	Approve            bool     `help:"Save the draft instead of showing a preview, when your settings ask for one."`
	JSON               bool     `help:"Print JSON for scripts and agents."`
}

type mailBodyCommand struct {
	Account   string `help:"Account alias (default: your default account)."`
	MessageID string `name:"message-id" placeholder:"ID" help:"Message ID, from corr mail list --json (required)."`
	Approve   bool   `help:"Read the message when your settings ask for a preview first."`
	JSON      bool   `help:"Print JSON for scripts and agents."`
}

type mailAttachmentCommand struct {
	Account      string `help:"Account alias (default: your default account)."`
	AttachmentID string `name:"attachment-id" placeholder:"ID" help:"Attachment ID, listed by corr mail body (required)."`
	Output       string `type:"path" placeholder:"FILE" help:"New file to save to; an existing file is never overwritten."`
	Approve      bool   `help:"Read the attachment when your settings ask for a preview first."`
	JSON         bool   `help:"Print the content as base64 in JSON instead of saving a file."`
}

type mailListCommand struct {
	Account  string `help:"Account alias (default: your default account)."`
	Folder   string `default:"inbox" help:"Folder by name, such as inbox, sentitems, drafts, or archive."`
	FolderID string `name:"folder-id" placeholder:"ID" help:"Folder by ID, from corr mail folders; overrides --folder."`
	Offset   int    `default:"0" help:"Skip this many results, for paging."`
	Limit    int    `default:"25" help:"Most messages to show (1-100)."`
	TimeZone string `name:"time-zone" default:"UTC" help:"Time zone the provider uses for dates."`
	JSON     bool   `help:"Print JSON for scripts and agents."`
}

type mailSearchCommand struct {
	Account     string `help:"Account alias (default: your default account)."`
	AllAccounts bool   `name:"all-accounts" help:"Search every mail account; read-only, and results stay labelled by account."`
	Folder      string `default:"inbox" help:"Folder by name, such as inbox, sentitems, drafts, or archive."`
	FolderID    string `name:"folder-id" placeholder:"ID" help:"Folder by ID, from corr mail folders; not with --all-accounts."`
	Query       string `help:"Search words, in your mail provider's search syntax (required; up to 1024 bytes)."`
	Offset      int    `default:"0" help:"Skip this many results, for paging."`
	Limit       int    `default:"25" help:"Most messages to show (1-50)."`
	TimeZone    string `name:"time-zone" default:"UTC" help:"Time zone the provider uses for dates; --all-accounts needs UTC."`
	JSON        bool   `help:"Print JSON for scripts and agents."`
}

type mailMoveCommand struct {
	Account       string `help:"Account alias (default: your default account)."`
	MessageID     string `name:"message-id" placeholder:"ID" help:"Message ID, from corr mail list --json."`
	ChangeKey     string `name:"change-key" placeholder:"KEY" help:"Change key of that message, from the same output."`
	Destination   string `default:"archive" help:"Folder to move to, by name."`
	DestinationID string `name:"destination-id" placeholder:"ID" help:"Folder to move to, by ID from corr mail folders; overrides --destination."`
	Approve       bool   `help:"Move the message instead of showing a preview, when your settings ask for one."`
	JSON          bool   `help:"Print JSON for scripts and agents."`
}

type mailMarkCommand struct {
	Account   string `help:"Account alias (default: your default account)."`
	MessageID string `name:"message-id" placeholder:"ID" help:"Message ID, from corr mail list --json."`
	ChangeKey string `name:"change-key" placeholder:"KEY" help:"Change key of that message, from the same output."`
	State     string `help:"read or unread (required)."`
	Approve   bool   `help:"Mark the message instead of showing a preview, when your settings ask for one."`
	JSON      bool   `help:"Print JSON for scripts and agents."`
}

type mailDeleteCommand struct {
	Account   string `help:"Account alias (default: your default account)."`
	MessageID string `name:"message-id" placeholder:"ID" help:"Message ID, from corr mail list --json."`
	ChangeKey string `name:"change-key" placeholder:"KEY" help:"Change key of that message, from the same output."`
	Approve   bool   `help:"Delete the message permanently instead of showing a preview."`
	JSON      bool   `help:"Print JSON for scripts and agents."`
}

func (command *mailFoldersCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, accountID, err := resolveAccount(configuration, command.Account)
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
	return writeMailFolderTable(app, page, emptyResult{message: fmt.Sprintf(
		"No folders under %s in account %s.", mailFolderLabel(parent), sanitizeCell(alias, 64),
	)})
}

func (command *mailListCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, accountID, err := resolveAccount(configuration, command.Account)
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
	return writeMailTable(app, page, emptyResult{
		message: fmt.Sprintf("No messages in %s in account %s.", mailFolderLabel(folder), sanitizeCell(alias, 64)),
		next:    printableCommand(withAccount([]string{commandName, "mail", "folders"}, command.Account)...),
	})
}

func (command *mailSearchCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	if command.AllAccounts {
		if command.Account != "" {
			return withHint(
				errors.New("mail search --all-accounts searches every account, so it cannot be combined with --account"),
				"Leave out --account, or leave out --all-accounts to search one account.",
			)
		}
		if command.FolderID != "" {
			return withHint(
				errors.New("mail search --all-accounts cannot use --folder-id, because a folder ID belongs to one account"),
				"Use --folder with a folder name such as inbox or archive.",
			)
		}
		folder := application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   command.Folder,
		}
		input := application.MailProjectionInput{
			Folder: folder,
			Query:  command.Query, Offset: command.Offset,
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
		return writeMailProjectionTable(app, page, emptyResult{message: fmt.Sprintf(
			"No messages in %s match %s in any account.",
			mailFolderLabel(folder), quoteForSentence(command.Query),
		)})
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	alias, accountID, err := resolveAccount(configuration, command.Account)
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
	empty := emptyResult{message: fmt.Sprintf(
		"No messages in %s in account %s match %s.",
		mailFolderLabel(folder), sanitizeCell(alias, 64), quoteForSentence(command.Query),
	)}
	if command.FolderID == "" {
		empty.next = printableCommand(
			commandName, "mail", "search", "--all-accounts", "--folder", command.Folder, "--query", command.Query,
		)
	}
	return writeMailTable(app, page, empty)
}

// mailFolderLabel names a folder in a sentence: inbox, or folder ID AAMk….
func mailFolderLabel(folder application.MailFolder) string {
	if folder.Kind == application.MailFolderOpaque {
		return "folder " + sanitizeCell(folder.ID, 80)
	}
	return sanitizeCell(folder.ID, 64)
}

func writeMailProjectionTable(
	app *runtime,
	page application.MailProjectionPage,
	empty emptyResult,
) error {
	if len(page.Messages) == 0 {
		if err := app.writeEmpty(empty); err != nil {
			return err
		}
	} else {
		writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(
			writer,
			"ACCOUNT\tPROVIDER\tRECEIVED\tFROM\tSUBJECT\tFLAGS\tMESSAGE ID",
		); err != nil {
			return err
		}
		flagged := false
		for _, projected := range page.Messages {
			message := projected.Message
			flags := mailFlags(message)
			flagged = flagged || flags != ""
			if _, err := fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sanitizeCell(projected.AccountAlias, 64),
				sanitizeCell(string(message.Provenance.Provider), 64),
				sanitizeCell(message.ReceivedAt, 30),
				sanitizeCell(mailSender(message), 32),
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
		if err := writeMailFlagLegend(app, flagged); err != nil {
			return err
		}
	}
	return writeProjectionNotices(app, page.Accounts, page.Failures)
}

// writeProjectionNotices reports, after an all-accounts table, each account
// whose results are limited or missing.
func writeProjectionNotices(
	app *runtime,
	accounts []application.ProjectionAccountStatus,
	failures []application.ProjectionFailure,
) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	for _, status := range accounts {
		for _, degradation := range status.Degradations {
			if _, err := view.printf(
				"%s  Limited results from %s (%s): %s\n",
				view.warning(),
				sanitizeCell(status.Alias, 64),
				sanitizeCell(string(status.Provider), 64),
				sanitizeCell(degradation.Reason, 512),
			); err != nil {
				return err
			}
		}
	}
	for _, failure := range failures {
		if _, err := view.printf(
			"%s  Missing results from %s (%s): %s\n",
			view.warning(),
			sanitizeCell(failure.Alias, 64),
			sanitizeCell(string(failure.Provider), 64),
			sanitizeCell(failure.Reason, 512),
		); err != nil {
			return err
		}
		if failure.Authentication != nil {
			if _, err := fmt.Fprintln(app.stdout, view.nextStep(printableCommand(
				commandName, "auth", "login", "--account", failure.Authentication.Alias,
			))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (command *mailMoveCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
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
		return withMessageIDHint(err, command.Account, command.MessageID, command.ChangeKey)
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
			if err := writeMailMoveReview(app.stdout, access.Review, false); err != nil {
				return err
			}
			return app.writePreviewEnd(command, "mail", "move")
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
	moved := access.Review.Destination
	if moved.ID == "" {
		moved = destination
	}
	if access.Moved.ID == "" {
		_, err = fmt.Fprintf(
			app.stdout,
			"Moved the message to %s. The provider did not return its new ID; list that folder to find it.\n",
			sanitizeCell(moveDestinationLabel(moved), 120),
		)
		return err
	}
	_, err = fmt.Fprintf(
		app.stdout, "Moved the message to %s. Its new ID is %s\n",
		sanitizeCell(moveDestinationLabel(moved), 120),
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

// withMessageIDHint says where message IDs and change keys come from when a
// command was run without them.
func withMessageIDHint(err error, account, messageID, changeKey string) error {
	if messageID != "" && changeKey != "" {
		return err
	}
	return withHint(err, printableCommand(withAccount([]string{commandName, "mail", "list", "--json"}, account)...))
}

// withAccount adds --account to a suggested command when the user chose one.
func withAccount(words []string, account string) []string {
	if account == "" {
		return words
	}
	return append(words, "--account", account)
}

func (command *mailMarkCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailReadStateInput{
		Account: accountID, MessageID: command.MessageID, ChangeKey: command.ChangeKey,
		State: application.MailReadState(command.State),
	}
	if err := input.Validate(); err != nil {
		return withMessageIDHint(err, command.Account, command.MessageID, command.ChangeKey)
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
			if err := writeMailReadStateReview(app.stdout, access.Review, false); err != nil {
				return err
			}
			return app.writePreviewEnd(command, "mail", "mark")
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
		app.stdout, "Marked the message as %s.\n", sanitizeCell(string(access.Updated.State), 16),
	)
	return err
}

func (command *mailDeleteCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailDeleteInput{
		Account: accountID, MessageID: command.MessageID, ChangeKey: command.ChangeKey,
	}
	if err := input.Validate(); err != nil {
		return withMessageIDHint(err, command.Account, command.MessageID, command.ChangeKey)
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
		if err := writeMailDeleteReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "mail", "delete")
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
	_, err = fmt.Fprintln(app.stdout, "Deleted the message permanently.")
	return err
}

func (command *mailBodyCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailBodyInput{Account: accountID, MessageID: command.MessageID}
	if err := input.Validate(); err != nil {
		return withMessageIDHint(err, command.Account, command.MessageID, "-")
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
			if err := writeMailBodyReview(app.stdout, command.MessageID, false); err != nil {
				return err
			}
			return app.writePreviewEnd(command, "mail", "body")
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
		"Message %s (private; the text below comes from the sender and is not checked):\n%s\n",
		sanitizeCell(access.Body.ID, 80),
		sanitizeTerminalText(access.Body.Text),
	)
	if err != nil {
		return err
	}
	if len(access.Body.Attachments) != 0 {
		if _, err := fmt.Fprintln(
			app.stdout,
			"Attachments (private; names and types come from the sender):",
		); err != nil {
			return err
		}
		for _, attachment := range access.Body.Attachments {
			if _, err := fmt.Fprintf(
				app.stdout, "- %s (%s, %s, %d bytes%s), ID: %s\n",
				sanitizeCell(attachment.Name, 128), sanitizeCell(attachment.Kind, 16),
				sanitizeCell(attachment.ContentType, 128),
				attachment.Size, inlineAttachmentLabel(attachment.IsInline),
				sanitizeCell(attachment.ID, 4096),
			); err != nil {
				return err
			}
		}
		view := newConsoleView(app, app.stdout, app.interactiveStdout())
		if _, err := fmt.Fprintln(app.stdout, view.nextStep(printableCommand(withAccount(
			[]string{commandName, "mail", "attachment", "--attachment-id", "ID", "--output", "FILE"},
			command.Account,
		)...))); err != nil {
			return err
		}
	}
	return nil
}

func (command *mailAttachmentCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	if !command.JSON && command.Output == "" {
		return withHint(
			errors.New("choose a file to save the attachment to with --output, or print it as base64 with --json"),
			printableCommand(withAccount([]string{
				commandName, "mail", "attachment", "--attachment-id", attachmentIDOrPlaceholder(command.AttachmentID),
				"--output", "FILE",
			}, command.Account)...),
		)
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailAttachmentInput{Account: accountID, AttachmentID: command.AttachmentID}
	if err := input.Validate(); err != nil {
		if command.AttachmentID == "" {
			return withHint(err, "Attachment IDs are listed by corr mail body --message-id ID.")
		}
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
			if err := writeMailAttachmentReview(app.stdout, command.AttachmentID, false); err != nil {
				return err
			}
			return app.writePreviewEnd(command, "mail", "attachment")
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
	if err != nil || len(content) > application.MaxMailAttachmentReadBytes {
		return errors.New("mail attachment response contained malformed base64 content")
	}
	file, err := os.OpenFile(command.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit CLI output.
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return withHint(
				fmt.Errorf("create attachment output: %w", err),
				"Choose a new file name with --output; Corresync never overwrites a file.",
			)
		}
		return fmt.Errorf("create attachment output: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write attachment output: %w", err)
	}
	_, err = fmt.Fprintf(
		app.stdout, "Saved the attachment (%d bytes) to %s\n",
		len(content), sanitizeCell(command.Output, 4096),
	)
	return err
}

func attachmentIDOrPlaceholder(id string) string {
	if id == "" {
		return "ID"
	}
	return id
}

func inlineAttachmentLabel(inline bool) string {
	if inline {
		return ", inline"
	}
	return ""
}

func (command *mailDraftCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
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
		return command.explainValidation(err, input, configuration.Policy.MaxRecipients)
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
			if err := writeDraftReview(app.stdout, access.Review, false); err != nil {
				return err
			}
			return app.writePreviewEnd(command, "mail", "draft")
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
	if err := writeSavedDraft(app.stdout, access); err != nil {
		return err
	}
	if access.Draft.ChangeKey == "" {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = fmt.Fprintln(app.stdout, view.nextStep(printableCommand(withAccount([]string{
		commandName, "mail", "send-draft", "--draft-id", access.Draft.ID,
		"--draft-change-key", access.Draft.ChangeKey,
	}, command.Account)...)))
	return err
}

// explainValidation adds a next step when a reply draft was given its own
// recipients: reply and reply-all take them from the original message.
func (command *mailDraftCommand) explainValidation(
	err error,
	input application.MailDraftInput,
	maxRecipients int,
) error {
	mode := input.EffectiveComposeMode()
	if mode != application.MailComposeReply && mode != application.MailComposeReplyAll ||
		len(input.To)+len(input.CC)+len(input.BCC) == 0 {
		return err
	}
	withoutRecipients := input
	withoutRecipients.To, withoutRecipients.CC, withoutRecipients.BCC = nil, nil, nil
	if withoutRecipients.Validate(maxRecipients) != nil {
		return err
	}
	suggested := *command
	suggested.To, suggested.CC, suggested.BCC = nil, nil, nil
	suggested.Mode = "reply-all"
	flags, _ := commandLineWords(&suggested)
	return withHint(err, printableCommand(append([]string{commandName, "mail", "draft"}, flags...)...))
}

// writeSavedDraft reports the provider-resolved addressing of a saved draft so
// that the audience can be checked before the draft is called ready.
func writeSavedDraft(writer io.Writer, access application.MailDraftAccess) error {
	review := access.Review
	if _, err := fmt.Fprintf(
		writer,
		"Saved draft %s. Nothing was sent.\n",
		sanitizeCell(access.Draft.ID, 80),
	); err != nil {
		return err
	}
	fields := []reviewField{{"Mode", sanitizeCell(string(review.ComposeMode), 16)}}
	if review.ReferenceMessageID != "" {
		fields = append(fields, reviewField{"Original message", fmt.Sprintf(
			"%s (change key %s)",
			sanitizeCell(review.ReferenceMessageID, 4096),
			sanitizeCell(review.ReferenceChangeKey, 4096),
		)})
	}
	addressing := access.Addressing
	if addressing == nil || addressing.Status != application.MailAddressingResolved ||
		addressing.Resolved == nil {
		fields = append(fields, reviewField{"Bcc", mailReviewRecipientsOrNone(review.BCC)})
		if err := writeReviewFields(writer, fields); err != nil {
			return err
		}
		_, err := fmt.Fprintln(
			writer,
			"This provider did not report the saved To, Cc, and subject (unknown, not empty). "+
				"Check the draft's recipients in your mail app before sending it.",
		)
		return err
	}
	resolved := addressing.Resolved
	fields = append(fields,
		reviewField{"To", mailReviewRecipientsOrNone(resolved.To)},
		reviewField{"Cc", mailReviewRecipientsOrNone(resolved.CC)},
		reviewField{"Bcc", mailReviewRecipientsOrNone(review.BCC)},
		reviewField{"Subject", sanitizeCell(resolved.Subject, 4*application.MaxMailSubjectBytes)},
	)
	if err := writeReviewFields(writer, fields); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "To, Cc, and subject are shown as the provider saved them."); err != nil {
		return err
	}
	if omitted := resolved.SourceParticipantsNotIncluded; omitted != nil && *omitted > 0 {
		hint := "check that this is who you meant to write to before sending"
		if review.ComposeMode == application.MailComposeReply {
			hint = "to include them, save a new draft with --mode reply-all"
		}
		if _, err := fmt.Fprintf(
			writer,
			"Note: %d To/Cc %s of the original message %s not on this draft; %s.\n",
			*omitted, plural(*omitted, "recipient", "recipients"), plural(*omitted, "is", "are"), hint,
		); err != nil {
			return err
		}
	}
	return nil
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func mailReviewRecipientsOrNone(recipients []string) string {
	if len(recipients) == 0 {
		return "(none)"
	}
	return mailReviewRecipients(recipients)
}

func (command *mailSendCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
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
		return command.explainSendFailure(err)
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("mail send did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		if err := writeSendReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "mail", "send")
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
	recipients := len(access.Review.To) + len(access.Review.CC) + len(access.Review.BCC)
	_, err = fmt.Fprintf(
		app.stdout, "Sent the message to %d %s. The request was made once and is not retried.\n",
		recipients, plural(recipients, "recipient", "recipients"),
	)
	return err
}

// explainSendFailure points a reply or reply-all send, which the daemon
// refuses, to the draft that shows its recipients first.
func (command *mailSendCommand) explainSendFailure(err error) error {
	var remote *daemonapi.Error
	if command.Mode != "reply" && command.Mode != "reply-all" || !errors.As(err, &remote) ||
		remote.Code != "operation_failed" {
		return err
	}
	if _, signIn := signInRequest(err); signIn {
		return err
	}
	draft := mailDraftCommand{
		Account: command.Account, Subject: command.Subject, BodyFile: command.BodyFile,
		BodyFormat: command.BodyFormat, Mode: command.Mode,
		ReferenceMessageID: command.ReferenceMessageID, ReferenceChangeKey: command.ReferenceChangeKey,
		Attachments: command.Attachments,
	}
	flags, _ := commandLineWords(&draft)
	return withHint(err, printableCommand(append([]string{commandName, "mail", "draft"}, flags...)...))
}

func (command *mailSendDraftCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	_, accountID, err := resolveAccount(configuration, command.Account)
	if err != nil {
		return err
	}
	input := application.MailDraftSendInput{
		Account: accountID, DraftID: command.DraftID,
		DraftChangeKey: command.DraftChangeKey,
	}
	if err := input.Validate(); err != nil {
		if command.DraftID == "" || command.DraftChangeKey == "" {
			return withHint(err, printableCommand(withAccount(
				[]string{commandName, "mail", "list", "--folder", "drafts", "--json"}, command.Account,
			)...))
		}
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
		if err := writeSendDraftReview(app.stdout, access.Review, false); err != nil {
			return err
		}
		return app.writePreviewEnd(command, "mail", "send-draft")
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
	recipients := len(access.Review.To) + len(access.Review.CC) + len(access.Review.BCC)
	_, err = fmt.Fprintf(
		app.stdout,
		"Sent draft %s to %d %s. The request was made once, for the version you previewed, and is not retried.\n",
		sanitizeCell(access.Review.DraftID, 80),
		recipients, plural(recipients, "recipient", "recipients"),
	)
	return err
}

func writeSendReview(writer io.Writer, review application.MailReview, committing bool) error {
	action := "Preview: this message has not been sent."
	if committing {
		action = "Sending this message now:"
	}
	return writeMailContentReview(writer, review, action, true)
}

func writeDraftReview(writer io.Writer, review application.MailReview, committing bool) error {
	action := "Preview: no draft has been saved."
	if committing {
		action = "Saving this draft now; it will not be sent:"
	}
	return writeMailContentReview(writer, review, action, false)
}

func writeSendDraftReview(
	writer io.Writer,
	review application.MailDraftSendReview,
	committing bool,
) error {
	action := "Preview: this draft has not been sent."
	if committing {
		action = "Sending this draft now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	fields := []reviewField{
		{"Will send", "yes"},
		{"Draft ID", sanitizeCell(review.DraftID, 4096)},
		{"Change key", sanitizeCell(review.DraftChangeKey, 4096)},
		{"To", mailReviewRecipients(review.To)},
		{"Cc", mailReviewRecipients(review.CC)},
		{"Bcc", mailReviewRecipients(review.BCC)},
		{"Subject", sanitizeCell(review.Subject, 998)},
		{"Body format", sanitizeCell(string(review.BodyFormat), 16)},
		{"Body size", bodySizeLabel(review.BodyBytes, review.BodySHA256)},
	}
	for _, attachment := range review.Attachments {
		inline := ""
		if attachment.Inline {
			inline = ", inline"
		}
		fields = append(fields, reviewField{"Attachment", fmt.Sprintf(
			"%s (%s, %d bytes%s)",
			sanitizeCell(attachment.Name, 255),
			sanitizeCell(attachment.ContentType, 255),
			attachment.Bytes,
			inline,
		)})
	}
	if err := writeReviewFields(writer, fields); err != nil {
		return err
	}
	return writeReviewBody(writer, review.BodyPreview)
}

func writeMailContentReview(
	writer io.Writer,
	review application.MailReview,
	action string,
	sends bool,
) error {
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	fields := []reviewField{
		{"Will send", previewYesNo(sends)},
		{"Mode", sanitizeCell(string(review.ComposeMode), 16)},
		{"To", mailReviewRecipients(review.To)},
		{"Cc", mailReviewRecipients(review.CC)},
		{"Bcc", mailReviewRecipients(review.BCC)},
		{"Subject", sanitizeCell(review.Subject, 998)},
		{"Body format", sanitizeCell(string(review.BodyFormat), 16)},
		{"Body size", bodySizeLabel(review.BodyBytes, review.BodySHA256)},
	}
	if review.ReferenceMessageID != "" {
		fields = append(fields, reviewField{"Original message", fmt.Sprintf(
			"%s (change key %s)",
			sanitizeCell(review.ReferenceMessageID, 4096),
			sanitizeCell(review.ReferenceChangeKey, 4096),
		)})
	}
	for _, attachment := range review.Attachments {
		fields = append(fields, reviewField{"Attachment", fmt.Sprintf(
			"%s (%s, %d bytes, SHA-256 %s)",
			sanitizeCell(attachment.Name, 255), sanitizeCell(attachment.ContentType, 255),
			attachment.Bytes, sanitizeCell(attachment.SHA256, 64),
		)})
	}
	if err := writeReviewFields(writer, fields); err != nil {
		return err
	}
	return writeReviewBody(writer, review.BodyPreview)
}

func bodySizeLabel(bytes int, digest string) string {
	return fmt.Sprintf("%d bytes (SHA-256 %s)", bytes, sanitizeCell(digest, 64))
}

// writeReviewBody prints the start of a body under the preview fields.
func writeReviewBody(writer io.Writer, preview string) error {
	if preview == "" {
		return nil
	}
	_, err := fmt.Fprintf(writer, "Body:\n%s\n", sanitizeTerminalText(preview))
	return err
}

// Recipient lists are already bounded by the application contract. Preserve
// every reviewed destination while neutralizing terminal controls.
func mailReviewRecipients(recipients []string) string {
	value := strings.Join(recipients, ", ")
	return sanitizeCell(value, len(value)+1)
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
		if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > application.MaxMailSendAttachmentBytes {
			_ = file.Close()
			return nil, fmt.Errorf("mail attachment %q must be a regular file between 1 and %d bytes", path, application.MaxMailSendAttachmentBytes)
		}
		totalBytes += info.Size()
		if totalBytes > application.MaxMailAttachmentTotalBytes {
			_ = file.Close()
			return nil, fmt.Errorf(
				"mail attachments total %d bytes; maximum is %d",
				totalBytes, application.MaxMailAttachmentTotalBytes,
			)
		}
		content, readErr := io.ReadAll(io.LimitReader(file, application.MaxMailSendAttachmentBytes+1))
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
	action := "Preview: the message has not been moved."
	if committing {
		action = "Moving this message now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{
		{"Message ID", sanitizeCell(review.MessageID, 4096)},
		{"Change key", sanitizeCell(review.ChangeKey, 4096)},
		{"Move to", sanitizeCell(moveDestinationLabel(review.Destination), 4096)},
	})
}

func writeMailReadStateReview(
	writer io.Writer,
	review application.MailReadStateReview,
	committing bool,
) error {
	action := "Preview: the message has not been marked."
	if committing {
		action = "Marking this message now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{
		{"Message ID", sanitizeCell(review.MessageID, 4096)},
		{"Change key", sanitizeCell(review.ChangeKey, 4096)},
		{"Mark as", sanitizeCell(string(review.State), 16)},
	})
}

func writeMailDeleteReview(writer io.Writer, review application.MailDeleteReview, committing bool) error {
	action := "Preview: nothing has been deleted. Approving deletes this message permanently; " +
		"the provider cannot undo it."
	if committing {
		action = "Deleting this message permanently now; the provider cannot undo it:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{
		{"Message ID", sanitizeCell(review.MessageID, 4096)},
		{"Change key", sanitizeCell(review.ChangeKey, 4096)},
		{"Delete type", sanitizeCell(review.DeleteType, 32)},
	})
}

func writeMailBodyReview(writer io.Writer, messageID string, committing bool) error {
	action := "Preview: the message has not been read. Your settings ask before message text is read."
	if committing {
		action = "Reading this message now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{{"Message ID", sanitizeCell(messageID, 4096)}})
}

func writeMailAttachmentReview(writer io.Writer, attachmentID string, committing bool) error {
	action := "Preview: the attachment has not been read. Your settings ask before attachments are read."
	if committing {
		action = "Reading this attachment now:"
	}
	if _, err := fmt.Fprintln(writer, action); err != nil {
		return err
	}
	return writeReviewFields(writer, []reviewField{{"Attachment ID", sanitizeCell(attachmentID, 4096)}})
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

func writeMailTable(app *runtime, page application.MailPage, empty emptyResult) error {
	if len(page.Messages) == 0 {
		return app.writeEmpty(empty)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "RECEIVED\tFROM\tSUBJECT\tFLAGS\tMESSAGE ID"); err != nil {
		return err
	}
	flagged := false
	for _, message := range page.Messages {
		flags := mailFlags(message)
		flagged = flagged || flags != ""
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(message.ReceivedAt, 30),
			sanitizeCell(mailSender(message), 32),
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
	return writeMailFlagLegend(app, flagged)
}

func mailSender(message application.MailSummary) string {
	if message.From.Name != "" {
		return message.From.Name
	}
	return message.From.Address
}

func mailFlags(message application.MailSummary) string {
	flags := ""
	if !message.IsRead {
		flags += "U"
	}
	if message.HasAttachments {
		flags += "A"
	}
	return flags
}

func writeMailFlagLegend(app *runtime, flagged bool) error {
	if !flagged {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.muted("Flags: U unread, A has attachments."))
	return err
}

func writeMailFolderTable(app *runtime, page application.MailFolderPage, empty emptyResult) error {
	if len(page.Folders) == 0 {
		return app.writeEmpty(empty)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tTYPE\tMESSAGES\tUNREAD\tSUBFOLDERS\tFOLDER ID"); err != nil {
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
