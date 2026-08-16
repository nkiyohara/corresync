package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumMessageInputBytes = application.MaxMessageResultBytes

type messagesCommand struct {
	Conversations messageConversationsCommand `cmd:"" help:"List conversations in one workspace."`
	List          messageListCommand          `cmd:"" help:"List bounded message metadata."`
	Search        messageSearchCommand        `cmd:"" help:"Search bounded message metadata."`
	Get           messageGetCommand           `cmd:"" help:"Read one exact message, with approval when required."`
	Attachment    messageAttachmentCommand    `cmd:"" help:"Read one bounded attachment into a new private file."`
	Sync          messageSyncCommand          `cmd:"" help:"Read bounded account- and conversation-bound changes."`
	Send          messageSendCommand          `cmd:"" help:"Review and send a canonical message JSON document."`
	Edit          messageEditCommand          `cmd:"" help:"Review and edit a canonical versioned message JSON document."`
	Delete        messageDeleteCommand        `cmd:"" help:"Review and delete a canonical versioned message JSON document."`
	React         messageReactCommand         `cmd:"" help:"Review a canonical reaction JSON document."`
	Create        conversationCreateCommand   `cmd:"" help:"Review a canonical conversation JSON document."`
	Membership    messageMembershipCommand    `cmd:"" help:"Review a canonical membership change JSON document."`
}

type messageConversationsCommand struct {
	Account     string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	Cursor      string `help:"Opaque provider cursor returned by the previous page."`
	Limit       int    `default:"50" help:"Conversations to return (1-100)."`
	JSON        bool   `help:"Write the stable machine-readable schema."`
}

type messageListCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID    string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	ConversationID string `name:"conversation-id" required:"" help:"Exact conversation ID."`
	ThreadRootID   string `name:"thread-root-id" help:"Optional exact thread root ID."`
	Cursor         string `help:"Opaque provider cursor returned by the previous page."`
	Limit          int    `default:"50" help:"Messages to return (1-100)."`
	JSON           bool   `help:"Write the stable machine-readable schema."`
}

type messageSearchCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID    string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	ConversationID string `name:"conversation-id" help:"Optional exact conversation scope."`
	Query          string `required:"" help:"Provider-neutral text query."`
	Cursor         string `help:"Opaque provider cursor returned by the previous page."`
	Limit          int    `default:"50" help:"Messages to return (1-100)."`
	JSON           bool   `help:"Write the stable machine-readable schema."`
}

type messageGetCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID    string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	ConversationID string `name:"conversation-id" required:"" help:"Exact conversation ID."`
	ThreadRootID   string `name:"thread-root-id" help:"Optional exact thread root ID."`
	MessageID      string `name:"message-id" required:"" help:"Exact message ID."`
	Approve        bool   `help:"Commit the in-process preview when sensitive reads require approval."`
	JSON           bool   `help:"Write the stable machine-readable schema."`
}

type messageAttachmentCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID    string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	ConversationID string `name:"conversation-id" required:"" help:"Exact conversation ID."`
	ThreadRootID   string `name:"thread-root-id" help:"Optional exact thread root ID."`
	MessageID      string `name:"message-id" required:"" help:"Exact message ID."`
	AttachmentID   string `name:"attachment-id" required:"" help:"Exact attachment ID."`
	Output         string `type:"path" help:"New private output file; existing files are never overwritten."`
	Approve        bool   `help:"Commit the in-process preview when sensitive reads require approval."`
	JSON           bool   `help:"Write metadata and base64 content using the stable schema."`
}

type messageSyncCommand struct {
	Account        string `help:"Configured account alias; defaults to default_account."`
	WorkspaceID    string `name:"workspace-id" required:"" help:"Exact configured workspace ID."`
	ConversationID string `name:"conversation-id" help:"Optional exact conversation scope."`
	CursorFile     string `name:"cursor-file" type:"path" help:"Strict MessageCursor JSON file, or - for stdin."`
	Limit          int    `default:"50" help:"Changes to return (1-100)."`
	JSON           bool   `help:"Write the stable machine-readable schema."`
}

type messageFileWriteCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	File    string `type:"path" required:"" help:"Strict canonical messaging JSON file, or - for stdin."`
	Approve bool   `help:"Apply the exact preview generated from the document."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type messageSendCommand messageFileWriteCommand
type messageEditCommand messageFileWriteCommand
type messageDeleteCommand messageFileWriteCommand
type messageReactCommand messageFileWriteCommand
type conversationCreateCommand messageFileWriteCommand
type messageMembershipCommand messageFileWriteCommand

func (command *messageConversationsCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListConversations(app.context, application.ConversationListInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		Cursor: command.Cursor, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeConversationTable(app, page)
}

func (command *messageListCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.ListMessages(app.context, application.MessageListInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		ConversationID: command.ConversationID, ThreadRootID: command.ThreadRootID,
		Cursor: command.Cursor, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMessageTable(app, page.Messages, page.NextCursor)
}

func (command *messageSearchCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	page, err := client.SearchMessages(app.context, application.MessageSearchInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		ConversationID: command.ConversationID, Query: command.Query,
		Cursor: command.Cursor, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMessageTable(app, page.Messages, page.NextCursor)
}

func (command *messageGetCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.GetMessage(app.context, application.MessageGetInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		ConversationID: command.ConversationID, ThreadRootID: command.ThreadRootID,
		MessageID: command.MessageID,
	}, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("message read omitted its required preview")
		}
		if !command.Approve {
			return writeMessageSensitiveReview(app.stdout, access.Review, false)
		}
		if err := writeMessageSensitiveReview(app.stderr, access.Review, true); err != nil {
			return err
		}
		access, err = client.CommitGetMessage(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	if access.Message == nil {
		return errors.New("message read returned no message")
	}
	return writeMessage(app.stdout, *access.Message)
}

func (command *messageAttachmentCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	access, err := client.GetMessageAttachment(app.context, application.MessageAttachmentGetInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		ConversationID: command.ConversationID, ThreadRootID: command.ThreadRootID,
		MessageID: command.MessageID, AttachmentID: command.AttachmentID,
	}, app.caller())
	if err != nil {
		return err
	}
	if access.Status == "approval_required" {
		if access.Preview == nil {
			return errors.New("message attachment read omitted its required preview")
		}
		if !command.Approve {
			return writeMessageSensitiveReview(app.stdout, access.Review, false)
		}
		if err := writeMessageSensitiveReview(app.stderr, access.Review, true); err != nil {
			return err
		}
		access, err = client.CommitGetMessageAttachment(app.context, access.Preview.Token, app.caller())
		if err != nil {
			return err
		}
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	if access.Attachment == nil {
		return errors.New("message attachment read returned no content")
	}
	if command.Output == "" {
		return errors.New("output is required for binary attachment content; use --json for base64 output")
	}
	return writePrivateAttachment(command.Output, *access.Attachment)
}

func (command *messageSyncCommand) Run(app *runtime) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	var cursor *application.MessageCursor
	if command.CursorFile != "" {
		cursor = &application.MessageCursor{}
		if err := readMessageJSON(app, command.CursorFile, cursor); err != nil {
			return err
		}
	}
	page, err := client.SyncMessages(app.context, application.MessageSyncInput{
		Account: account, WorkspaceID: command.WorkspaceID,
		ConversationID: command.ConversationID, Cursor: cursor, Limit: command.Limit,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, page)
	}
	return writeMessageChanges(app, page)
}

func (command *messageSendCommand) Run(app *runtime) error {
	var input application.MessageSendInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.SendMessage(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitSendMessage(app.context, token, app.caller())
		})
}

func (command *messageEditCommand) Run(app *runtime) error {
	var input application.MessageEditInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.EditMessage(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitEditMessage(app.context, token, app.caller())
		})
}

func (command *messageDeleteCommand) Run(app *runtime) error {
	var input application.MessageDeleteInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.DeleteMessage(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitDeleteMessage(app.context, token, app.caller())
		})
}

func (command *messageReactCommand) Run(app *runtime) error {
	var input application.MessageReactionInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.ReactToMessage(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitMessageReaction(app.context, token, app.caller())
		})
}

func (command *conversationCreateCommand) Run(app *runtime) error {
	var input application.ConversationCreateInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.CreateConversation(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitCreateConversation(app.context, token, app.caller())
		})
}

func (command *messageMembershipCommand) Run(app *runtime) error {
	var input application.ConversationMembershipInput
	return (*messageFileWriteCommand)(command).run(app, &input,
		func(client *daemonapi.Client) (application.MessageWriteAccess, error) {
			return client.ChangeConversationMembership(app.context, input, app.caller())
		},
		func(client *daemonapi.Client, token string) (application.MessageWriteAccess, error) {
			return client.CommitConversationMembership(app.context, token, app.caller())
		})
}

func (command *messageFileWriteCommand) run(
	app *runtime,
	input any,
	prepare func(*daemonapi.Client) (application.MessageWriteAccess, error),
	commit func(*daemonapi.Client, string) (application.MessageWriteAccess, error),
) (returnErr error) {
	account, client, err := messageClient(app, command.Account)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	if err := readMessageJSON(app, command.File, input); err != nil {
		return err
	}
	if err := bindMessageInputAccount(input, account); err != nil {
		return err
	}
	access, err := prepare(client)
	if err != nil {
		return err
	}
	if access.Status != "approval_required" || access.Preview == nil {
		return errors.New("messaging write did not produce its mandatory preview")
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, access)
		}
		return writeMessageReview(app.stdout, access.Review, false)
	}
	if err := writeMessageReview(app.stderr, access.Review, true); err != nil {
		return err
	}
	access, err = commit(client, access.Preview.Token)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, access)
	}
	_, err = fmt.Fprintf(
		app.stdout,
		"Messaging operation %s; the provider request was attempted once.\n",
		sanitizeCell(access.Status, 64),
	)
	return err
}

func bindMessageInputAccount(input any, account domain.AccountID) error {
	var route *application.MessageWriteRoute
	switch typed := input.(type) {
	case *application.MessageSendInput:
		route = &typed.MessageWriteRoute
	case *application.MessageEditInput:
		route = &typed.MessageWriteRoute
	case *application.MessageDeleteInput:
		route = &typed.MessageWriteRoute
	case *application.MessageReactionInput:
		route = &typed.MessageWriteRoute
	case *application.ConversationCreateInput:
		route = &typed.MessageWriteRoute
	case *application.ConversationMembershipInput:
		route = &typed.MessageWriteRoute
	default:
		return errors.New("unsupported canonical messaging input")
	}
	if route.Account != "" {
		return errors.New("messaging JSON must omit account; use the --account routing option")
	}
	route.Account = account
	return nil
}

func messageClient(app *runtime, reference string) (domain.AccountID, *daemonapi.Client, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", nil, err
	}
	account, err := app.account(configuration, reference)
	if err != nil {
		return "", nil, err
	}
	client, _, err := app.openDaemon(app.context)
	return account, client, err
}

func readMessageJSON(app *runtime, path string, destination any) (returnErr error) {
	reader := app.stdin
	var file *os.File
	if path != "-" {
		opened, err := os.Open(path) // #nosec G304 -- explicit local CLI input.
		if err != nil {
			return fmt.Errorf("open messaging JSON: %w", err)
		}
		file = opened
		reader = file
		defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximumMessageInputBytes+1))
	if err != nil {
		return fmt.Errorf("read messaging JSON: %w", err)
	}
	if len(data) > maximumMessageInputBytes {
		return fmt.Errorf("messaging JSON exceeds %d bytes", maximumMessageInputBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode messaging JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("messaging JSON must contain exactly one value")
	}
	return nil
}

func writeConversationTable(app *runtime, page application.ConversationPage) error {
	if len(page.Conversations) == 0 {
		_, err := fmt.Fprintln(app.stdout, "No conversations.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "KIND\tVISIBILITY\tACTIVITY\tNAME\tID"); err != nil {
		return err
	}
	for _, conversation := range page.Conversations {
		name := conversation.Name
		if name == "" {
			name = conversation.Topic
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			sanitizeCell(string(conversation.Kind), 32),
			sanitizeCell(string(conversation.Visibility), 32),
			sanitizeCell(conversation.LastActivityAt, 32),
			sanitizeCell(name, 80), sanitizeCell(conversation.ID, 4096)); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return writeNextMessageCursor(app.stdout, page.NextCursor)
}

func writeMessageTable(app *runtime, messages []application.MessageSummary, nextCursor string) error {
	if len(messages) == 0 {
		if _, err := fmt.Fprintln(app.stdout, "No messages."); err != nil {
			return err
		}
		return writeNextMessageCursor(app.stdout, nextCursor)
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "CREATED\tAUTHOR\tREPLIES\tSNIPPET\tVERSION\tID"); err != nil {
		return err
	}
	for _, message := range messages {
		author := message.Author.DisplayName
		if author == "" {
			author = message.Author.ID
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s\n",
			sanitizeCell(message.CreatedAt, 32), sanitizeCell(author, 48),
			message.ReplyCount, sanitizeCell(message.Snippet, 96),
			sanitizeCell(message.Version, 4096), sanitizeCell(message.ID, 4096)); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return writeNextMessageCursor(app.stdout, nextCursor)
}

func writeNextMessageCursor(writer io.Writer, cursor string) error {
	if cursor == "" {
		return nil
	}
	_, err := fmt.Fprintf(writer, "Next cursor: %s\n", sanitizeCell(cursor, application.MaxMessageCursorBytes))
	return err
}

func writeMessage(writer io.Writer, message application.Message) error {
	author := message.Summary.Author.DisplayName
	if author == "" {
		author = message.Summary.Author.ID
	}
	if _, err := fmt.Fprintf(writer, "From: %s\nCreated: %s\nConversation: %s\nMessage: %s\n\n",
		sanitizeCell(author, 1024), sanitizeCell(message.Summary.CreatedAt, 64),
		sanitizeCell(message.Summary.ConversationID, 4096), sanitizeCell(message.Summary.ID, 4096)); err != nil {
		return err
	}
	_, err := fmt.Fprintln(writer, sanitizeTerminalText(message.Content.Text))
	return err
}

func writeMessageChanges(app *runtime, page application.MessageChangePage) error {
	upserts := make([]application.MessageSummary, 0, len(page.Changes))
	deletions := make([]application.MessageChange, 0, len(page.Changes))
	for _, change := range page.Changes {
		if change.Message != nil {
			upserts = append(upserts, *change.Message)
		} else {
			deletions = append(deletions, change)
		}
	}
	if len(upserts) > 0 {
		if err := writeMessageTable(app, upserts, ""); err != nil {
			return err
		}
	}
	for _, change := range deletions {
		if _, err := fmt.Fprintf(app.stdout, "Deleted message %s.\n", sanitizeCell(change.ID, 4096)); err != nil {
			return err
		}
	}
	if page.Reset {
		if _, err := fmt.Fprintln(app.stdout, "Cursor reset required; this page is a bounded snapshot."); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(page.Cursor)
	if err != nil {
		return fmt.Errorf("encode next message cursor: %w", err)
	}
	_, err = fmt.Fprintf(app.stdout, "Next cursor: %s\n", encoded)
	return err
}

func writeMessageSensitiveReview(
	writer io.Writer,
	review application.MessageSensitiveReview,
	committing bool,
) error {
	action := "Preview only; private message content was not read. Rerun with --approve to read this exact item."
	if committing {
		action = "Reading this exact private messaging item now."
	}
	_, err := fmt.Fprintf(writer, "%s\nWorkspace: %s\nConversation: %s\nMessage: %s\nAttachment: %s\n",
		action, sanitizeCell(review.WorkspaceID, 4096),
		sanitizeCell(review.ConversationID, 4096), sanitizeCell(review.MessageID, 4096),
		sanitizeCell(review.AttachmentID, 4096))
	return err
}

func writeMessageReview(
	writer io.Writer,
	review application.MessageWriteReview,
	committing bool,
) error {
	verb := "Preview"
	if committing {
		verb = "Approved"
	}
	if _, err := fmt.Fprintf(writer, "%s messaging operation %s — exact typed review follows.\n",
		verb, sanitizeCell(review.Action, 64)); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(review)
}

func writePrivateAttachment(path string, content application.MessageAttachmentContent) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit output; O_EXCL prevents overwrite.
	if err != nil {
		return fmt.Errorf("create private attachment output: %w", err)
	}
	complete := false
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
		if !complete {
			// The exact path was created by this invocation, so removing an
			// incomplete result cannot affect a pre-existing user file.
			returnErr = errors.Join(returnErr, os.Remove(path))
		}
	}()
	if _, err := file.Write(content.Data); err != nil {
		return fmt.Errorf("write private attachment output: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync private attachment output: %w", err)
	}
	complete = true
	return nil
}
