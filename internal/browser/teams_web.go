package browser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

const (
	teamsWebOrigin        = "https://teams.microsoft.com"
	teamsSemanticRevision = "teams-web-dom-v1"
	teamsMaximumDOMRows   = application.MaxMessageCollectionItems
)

type teamsBrowserState struct {
	workspaceID string
	actor       application.MessageActor
	revision    string
}

type teamsObservationSnapshot struct {
	State       string `json:"state"`
	WorkspaceID string `json:"workspaceId"`
	ActorID     string `json:"actorId"`
	DisplayName string `json:"displayName"`
	List        bool   `json:"list"`
	History     bool   `json:"history"`
	Search      bool   `json:"search"`
	Send        bool   `json:"send"`
	Edit        bool   `json:"edit"`
	Delete      bool   `json:"delete"`
	Reactions   bool   `json:"reactions"`
	Create      bool   `json:"create"`
	Membership  bool   `json:"membership"`
}

// ObserveTeams waits for a recognized first-party Teams application shell.
// While sign-in is in progress it observes only location metadata; scripts are
// never evaluated against Microsoft identity-provider pages.
func (browser *Browser) ObserveTeams(
	ctx context.Context,
	origin string,
) (teamscontract.Observation, error) {
	if err := browser.validateTeamsOrigin(origin); err != nil {
		return teamscontract.Observation{}, err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	if err := chromedp.Run(operationContext, chromedp.Navigate(origin+"/v2/")); err != nil {
		return teamscontract.Observation{}, err
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var location string
		if err := chromedp.Run(operationContext, chromedp.Location(&location)); err != nil {
			return teamscontract.Observation{}, err
		}
		target, _ := url.Parse(location)
		if browserRequestOrigin(target) == teamsWebOrigin && teamsApplicationPath(target.Path) {
			var snapshot teamsObservationSnapshot
			if err := chromedp.Run(
				operationContext,
				chromedp.Evaluate(teamsObservationScript, &snapshot),
			); err != nil {
				return teamscontract.Observation{}, err
			}
			if snapshot.State == "ready" {
				observation, err := mapTeamsObservation(snapshot)
				if err != nil {
					return teamscontract.Observation{}, err
				}
				browser.teamsState = &teamsBrowserState{
					workspaceID: observation.WorkspaceID,
					actor:       observation.Actor,
					revision:    observation.Revision,
				}
				return observation, nil
			}
			if snapshot.State != "loading" {
				return teamscontract.Observation{}, errors.New(
					"the Teams Web DOM exposed an unrecognized application shell",
				)
			}
		}
		select {
		case <-operationContext.Done():
			return teamscontract.Observation{}, operationContext.Err()
		case <-ticker.C:
		}
	}
}

func (browser *Browser) validateTeamsOrigin(origin string) error {
	if browser == nil || browser.sessions != nil {
		return errors.New("the Teams Web route requires a browser-owned session without authorization observation")
	}
	parsed, err := url.Parse(origin)
	if err != nil || browserRequestOrigin(parsed) != teamsWebOrigin ||
		parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("the Teams Web origin is malformed")
	}
	if _, allowed := browser.allowedOrigins[teamsWebOrigin]; !allowed {
		return errors.New("the Teams Web origin is outside the browser boundary")
	}
	return nil
}

func teamsApplicationPath(path string) bool {
	return path == "/" || strings.HasPrefix(path, "/v2/") || strings.HasPrefix(path, "/l/")
}

func navigateTeamsApplication(ctx context.Context, target string) error {
	parsed, err := url.Parse(target)
	if err != nil || browserRequestOrigin(parsed) != teamsWebOrigin ||
		!teamsApplicationPath(parsed.Path) || parsed.User != nil {
		return errors.New("the Teams Web navigation target is malformed")
	}
	var location string
	if err := chromedp.Run(ctx, chromedp.Navigate(target), chromedp.Location(&location)); err != nil {
		return err
	}
	finalTarget, _ := url.Parse(location)
	if browserRequestOrigin(finalTarget) != teamsWebOrigin ||
		!teamsApplicationPath(finalTarget.Path) {
		return errors.New(
			"the Teams Web app left its approved origin; complete reauthentication in the visible browser",
		)
	}
	return nil
}

func mapTeamsObservation(snapshot teamsObservationSnapshot) (teamscontract.Observation, error) {
	if !boundedTeamsValue(snapshot.WorkspaceID) ||
		!boundedTeamsValue(snapshot.ActorID) {
		return teamscontract.Observation{}, errors.New(
			"the Teams Web app did not expose a bounded workspace and signed-in identity",
		)
	}
	actor := application.MessageActor{
		ID: snapshot.ActorID, Mode: application.MessageActorDelegatedUser,
		DisplayName: boundedTeamsText(snapshot.DisplayName, 1024),
	}
	capabilities := application.MessageCapabilities{
		ListConversations:  snapshot.List,
		History:            snapshot.History,
		SensitiveRead:      snapshot.History,
		Search:             snapshot.Search,
		Send:               snapshot.Send,
		Reply:              snapshot.Send,
		Edit:               snapshot.Edit,
		Delete:             snapshot.Delete,
		Reactions:          snapshot.Reactions,
		CreateConversation: snapshot.Create,
		Membership:         snapshot.Membership,
		ActorMode:          application.MessageActorDelegatedUser,
	}
	if err := actor.Validate(true); err != nil {
		return teamscontract.Observation{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return teamscontract.Observation{}, err
	}
	return teamscontract.Observation{
		WorkspaceID: snapshot.WorkspaceID, Actor: actor,
		Capabilities: capabilities, Revision: teamsSemanticRevision,
	}, nil
}

func (browser *Browser) teamsOperation(
	ctx context.Context,
	workspaceID string,
	work func(context.Context) error,
) error {
	if browser == nil || browser.sessions != nil {
		return errors.New("the Teams Web browser-owned session is unavailable")
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	if browser.teamsState == nil || browser.teamsState.workspaceID != workspaceID ||
		browser.teamsState.revision != teamsSemanticRevision {
		return errors.New("the Teams Web semantic session does not match the selected workspace")
	}
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	return work(operationContext)
}

func (browser *Browser) teamsStateSnapshot() (teamsBrowserState, error) {
	if browser == nil {
		return teamsBrowserState{}, errors.New("the Teams Web semantic session is unavailable")
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	if browser.teamsState == nil || browser.teamsState.revision != teamsSemanticRevision {
		return teamsBrowserState{}, errors.New("the Teams Web semantic session is unavailable")
	}
	return *browser.teamsState, nil
}

type teamsConversationRow struct {
	ChatID           string `json:"chatId"`
	TeamID           string `json:"teamId"`
	ChannelID        string `json:"channelId"`
	Kind             string `json:"kind"`
	Visibility       string `json:"visibility"`
	Name             string `json:"name"`
	Topic            string `json:"topic"`
	MemberCount      int    `json:"memberCount"`
	MemberCountKnown bool   `json:"memberCountKnown"`
	LastActivityAt   string `json:"lastActivityAt"`
}

type teamsConversationSnapshot struct {
	State string                 `json:"state"`
	Rows  []teamsConversationRow `json:"rows"`
}

func (browser *Browser) TeamsListConversations(
	ctx context.Context,
	input application.ConversationListInput,
) (application.ConversationPage, error) {
	var snapshots []teamsConversationSnapshot
	err := browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(operationContext, teamsWebOrigin+"/v2/"); err != nil {
			return err
		}
		for _, section := range []string{"chat", "teams"} {
			expression, err := teamsCallExpression(teamsConversationSnapshotScript, section)
			if err != nil {
				return err
			}
			var snapshot teamsConversationSnapshot
			if err := chromedp.Run(
				operationContext,
				chromedp.Evaluate(expression, &snapshot, teamsAwaitPromise),
			); err != nil {
				return err
			}
			if err := validateTeamsConversationSnapshot(snapshot); err != nil {
				return err
			}
			snapshots = append(snapshots, snapshot)
		}
		return nil
	})
	if err != nil {
		return application.ConversationPage{}, err
	}
	rows := mergeTeamsConversationRows(snapshots)
	conversations := make([]application.Conversation, 0, len(rows))
	for _, row := range rows {
		conversation, err := mapTeamsConversation(row)
		if err != nil {
			return application.ConversationPage{}, err
		}
		conversations = append(conversations, conversation)
	}
	start, err := teamsVisibleOffset(input.Cursor, teamsConversationDigest(conversations), len(conversations))
	if err != nil {
		return application.ConversationPage{}, err
	}
	end := min(start+input.Limit, len(conversations))
	next := ""
	if end < len(conversations) {
		next = encodeTeamsVisibleOffset(end, teamsConversationDigest(conversations))
	}
	return application.ConversationPage{
		Conversations: conversations[start:end], NextCursor: next,
		Partial: true, PartialReason: "provider_limit", ObservedAt: time.Now().UTC(),
	}, nil
}

func (browser *Browser) TeamsGetConversation(
	ctx context.Context,
	conversationID string,
) (application.Conversation, error) {
	state, err := browser.teamsStateSnapshot()
	if err != nil {
		return application.Conversation{}, err
	}
	locator, err := teamscontract.DecodeConversationID(conversationID)
	if err != nil {
		return application.Conversation{}, err
	}
	var snapshot teamsConversationSnapshot
	err = browser.teamsOperation(ctx, state.workspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(
			operationContext,
			teamsConversationURL(locator, state.workspaceID),
		); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsCurrentConversationScript,
			locator.ChatID, locator.TeamID, locator.ChannelID,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &snapshot, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.Conversation{}, err
	}
	if err := validateTeamsConversationSnapshot(snapshot); err != nil || len(snapshot.Rows) != 1 {
		return application.Conversation{}, errors.New("the Teams Web app did not expose the selected conversation")
	}
	conversation, err := mapTeamsConversation(snapshot.Rows[0])
	if err != nil || conversation.ID != conversationID {
		return application.Conversation{}, errors.New("the Teams Web app exposed a different conversation")
	}
	return conversation, nil
}

type teamsMessageRow struct {
	ID             string                          `json:"id"`
	ChatID         string                          `json:"chatId"`
	TeamID         string                          `json:"teamId"`
	ChannelID      string                          `json:"channelId"`
	ThreadRootID   string                          `json:"threadRootId"`
	AuthorID       string                          `json:"authorId"`
	AuthorName     string                          `json:"authorName"`
	CreatedAt      string                          `json:"createdAt"`
	UpdatedAt      string                          `json:"updatedAt"`
	Snippet        string                          `json:"snippet"`
	Content        string                          `json:"content"`
	Format         string                          `json:"format"`
	ReplyCount     int                             `json:"replyCount"`
	HasAttachments bool                            `json:"hasAttachments"`
	Deleted        bool                            `json:"deleted"`
	Links          []application.MessageLink       `json:"links"`
	Mentions       []application.MessageMention    `json:"mentions"`
	Reactions      []application.MessageReaction   `json:"reactions"`
	Attachments    []application.MessageAttachment `json:"attachments"`
}

type teamsMessageSnapshot struct {
	State string            `json:"state"`
	Rows  []teamsMessageRow `json:"rows"`
}

func (browser *Browser) TeamsListMessages(
	ctx context.Context,
	input application.MessageListInput,
) (application.MessagePage, error) {
	state, err := browser.teamsStateSnapshot()
	if err != nil {
		return application.MessagePage{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.MessagePage{}, err
	}
	var snapshot teamsMessageSnapshot
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		target := teamsConversationURL(locator, input.WorkspaceID)
		if input.ThreadRootID != "" {
			target = teamsMessageURL(locator, input.WorkspaceID, input.ThreadRootID, input.ThreadRootID)
		}
		if err := navigateTeamsApplication(operationContext, target); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsMessageSnapshotScript, false, locator.ChatID, locator.TeamID,
			locator.ChannelID, input.ThreadRootID, "",
		)
		if err != nil {
			return err
		}
		return chromedp.Run(operationContext, chromedp.Evaluate(expression, &snapshot))
	})
	if err != nil {
		return application.MessagePage{}, err
	}
	summaries, digest, err := mapTeamsMessageSummaries(snapshot, input.ConversationID, state.actor)
	if err != nil {
		return application.MessagePage{}, err
	}
	start, err := teamsVisibleOffset(input.Cursor, digest, len(summaries))
	if err != nil {
		return application.MessagePage{}, err
	}
	end := min(start+input.Limit, len(summaries))
	next := ""
	if end < len(summaries) {
		next = encodeTeamsVisibleOffset(end, digest)
	}
	return application.MessagePage{
		Messages: summaries[start:end], NextCursor: next,
		Partial: true, PartialReason: "provider_limit", ObservedAt: time.Now().UTC(),
	}, nil
}

func (browser *Browser) TeamsGetMessage(
	ctx context.Context,
	input application.MessageGetInput,
) (application.Message, error) {
	state, err := browser.teamsStateSnapshot()
	if err != nil {
		return application.Message{}, err
	}
	locator, err := teamscontract.DecodeConversationID(input.ConversationID)
	if err != nil {
		return application.Message{}, err
	}
	var snapshot teamsMessageSnapshot
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		target := teamsMessageURL(locator, input.WorkspaceID, input.ThreadRootID, input.MessageID)
		if err := navigateTeamsApplication(operationContext, target); err != nil {
			return err
		}
		expression, err := teamsCallExpression(
			teamsMessageSnapshotScript, true, locator.ChatID, locator.TeamID,
			locator.ChannelID, input.ThreadRootID, input.MessageID,
		)
		if err != nil {
			return err
		}
		return chromedp.Run(operationContext, chromedp.Evaluate(expression, &snapshot))
	})
	if err != nil {
		return application.Message{}, err
	}
	if snapshot.State != "rows" || len(snapshot.Rows) != 1 || snapshot.Rows[0].ID != input.MessageID {
		return application.Message{}, errors.New("the Teams Web app did not expose the selected message")
	}
	return mapTeamsMessage(snapshot.Rows[0], input.ConversationID, state.actor)
}

func (browser *Browser) TeamsSearchMessages(
	ctx context.Context,
	input application.MessageSearchInput,
) (application.MessagePage, error) {
	state, err := browser.teamsStateSnapshot()
	if err != nil {
		return application.MessagePage{}, err
	}
	var snapshot teamsMessageSnapshot
	err = browser.teamsOperation(ctx, input.WorkspaceID, func(operationContext context.Context) error {
		if err := navigateTeamsApplication(operationContext, teamsWebOrigin+"/v2/"); err != nil {
			return err
		}
		expression, err := teamsCallExpression(teamsSearchScript, input.Query)
		if err != nil {
			return err
		}
		return chromedp.Run(
			operationContext,
			chromedp.Evaluate(expression, &snapshot, teamsAwaitPromise),
		)
	})
	if err != nil {
		return application.MessagePage{}, err
	}
	if snapshot.State != "rows" && snapshot.State != "empty" {
		return application.MessagePage{}, errors.New("the Teams Web search DOM is unrecognized")
	}
	summaries := make([]application.MessageSummary, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		conversationID, err := teamsRowConversationID(row.ChatID, row.TeamID, row.ChannelID)
		if err != nil {
			return application.MessagePage{}, err
		}
		if input.ConversationID != "" && conversationID != input.ConversationID {
			continue
		}
		message, err := mapTeamsMessage(row, conversationID, state.actor)
		if err != nil {
			return application.MessagePage{}, err
		}
		summaries = append(summaries, message.Summary)
	}
	digest := teamsMessageDigest(summaries)
	start, err := teamsVisibleOffset(input.Cursor, digest, len(summaries))
	if err != nil {
		return application.MessagePage{}, err
	}
	end := min(start+input.Limit, len(summaries))
	next := ""
	if end < len(summaries) {
		next = encodeTeamsVisibleOffset(end, digest)
	}
	return application.MessagePage{
		Messages: summaries[start:end], NextCursor: next,
		Partial: true, PartialReason: "provider_limit", ObservedAt: time.Now().UTC(),
	}, nil
}

func teamsConversationURL(locator teamscontract.Locator, workspaceID string) string {
	if locator.IsChat() {
		return teamsWebOrigin + "/l/chat/" + url.PathEscape(locator.ChatID) + "/conversations"
	}
	query := url.Values{"groupId": {locator.TeamID}, "tenantId": {workspaceID}}
	return teamsWebOrigin + "/l/team/" + url.PathEscape(locator.ChannelID) + "/conversations?" + query.Encode()
}

func teamsMessageURL(locator teamscontract.Locator, workspaceID, threadRootID, messageID string) string {
	if locator.IsChat() {
		query := url.Values{"context": {`{"contextType":"chat"}`}}
		return teamsWebOrigin + "/l/message/" + url.PathEscape(locator.ChatID) + "/" +
			url.PathEscape(messageID) + "?" + query.Encode()
	}
	parent := threadRootID
	if parent == "" {
		parent = messageID
	}
	query := url.Values{
		"tenantId": {workspaceID}, "groupId": {locator.TeamID}, "parentMessageId": {parent},
	}
	return teamsWebOrigin + "/l/message/" + url.PathEscape(locator.ChannelID) + "/" +
		url.PathEscape(messageID) + "?" + query.Encode()
}

func validateTeamsConversationSnapshot(snapshot teamsConversationSnapshot) error {
	if len(snapshot.Rows) > teamsMaximumDOMRows {
		return errors.New("the Teams Web conversation DOM exceeds the configured limit")
	}
	switch snapshot.State {
	case "rows":
		if len(snapshot.Rows) == 0 {
			return errors.New("the Teams Web conversation row marker omitted rows")
		}
	case "empty":
		if len(snapshot.Rows) != 0 {
			return errors.New("the Teams Web empty conversation marker included rows")
		}
	default:
		return errors.New("the Teams Web conversation DOM is unrecognized")
	}
	return nil
}

func mergeTeamsConversationRows(snapshots []teamsConversationSnapshot) []teamsConversationRow {
	result := make([]teamsConversationRow, 0)
	seen := make(map[string]struct{})
	for _, snapshot := range snapshots {
		for _, row := range snapshot.Rows {
			key := row.ChatID + "\x00" + row.TeamID + "\x00" + row.ChannelID
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, row)
		}
	}
	return result
}

func mapTeamsConversation(row teamsConversationRow) (application.Conversation, error) {
	id, err := teamsRowConversationID(row.ChatID, row.TeamID, row.ChannelID)
	if err != nil {
		return application.Conversation{}, err
	}
	kind := application.ConversationKind(row.Kind)
	if err := kind.Validate(); err != nil {
		return application.Conversation{}, errors.New("the Teams Web app exposed an unknown conversation kind")
	}
	visibility := application.ConversationVisibility(row.Visibility)
	if err := visibility.Validate(); err != nil {
		return application.Conversation{}, errors.New("the Teams Web app exposed an unknown conversation visibility")
	}
	containerID := ""
	if row.ChannelID != "" {
		containerID, err = teamscontract.EncodeTeamID(row.TeamID)
		if err != nil {
			return application.Conversation{}, err
		}
	}
	lastActivity, err := optionalTeamsTime(row.LastActivityAt)
	if err != nil {
		return application.Conversation{}, err
	}
	versionSource := strings.Join([]string{
		id, row.Name, row.Topic, strconv.Itoa(row.MemberCount), strconv.FormatBool(row.MemberCountKnown),
		row.Visibility, lastActivity,
	}, "\x00")
	version := sha256.Sum256([]byte(versionSource))
	return application.Conversation{
		ID: id, ContainerID: containerID, Version: "twcv1_" + hex.EncodeToString(version[:]),
		Kind: kind, Visibility: visibility,
		Name: boundedTeamsText(row.Name, 4096), Topic: boundedTeamsText(row.Topic, 8192),
		MemberCount: row.MemberCount, MemberCountKnown: row.MemberCountKnown,
		LastActivityAt: lastActivity,
	}, nil
}

func teamsRowConversationID(chatID, teamID, channelID string) (string, error) {
	if chatID != "" && teamID == "" && channelID == "" {
		return teamscontract.EncodeChatID(chatID)
	}
	if chatID == "" && teamID != "" && channelID != "" {
		return teamscontract.EncodeChannelID(teamID, channelID)
	}
	return "", errors.New("the Teams Web app exposed a malformed conversation locator")
}

func mapTeamsMessageSummaries(
	snapshot teamsMessageSnapshot,
	conversationID string,
	actor application.MessageActor,
) ([]application.MessageSummary, string, error) {
	if len(snapshot.Rows) > teamsMaximumDOMRows ||
		snapshot.State != "rows" && snapshot.State != "empty" {
		return nil, "", errors.New("the Teams Web message DOM is unrecognized or unbounded")
	}
	if snapshot.State == "rows" && len(snapshot.Rows) == 0 ||
		snapshot.State == "empty" && len(snapshot.Rows) != 0 {
		return nil, "", errors.New("the Teams Web message DOM state is inconsistent")
	}
	result := make([]application.MessageSummary, 0, len(snapshot.Rows))
	for _, row := range snapshot.Rows {
		message, err := mapTeamsMessage(row, conversationID, actor)
		if err != nil {
			return nil, "", err
		}
		result = append(result, message.Summary)
	}
	return result, teamsMessageDigest(result), nil
}

func mapTeamsMessage(
	row teamsMessageRow,
	conversationID string,
	actor application.MessageActor,
) (application.Message, error) {
	if !boundedTeamsValue(row.ID) {
		return application.Message{}, errors.New("the Teams Web app exposed a malformed message identity")
	}
	created, err := teamsTime(row.CreatedAt)
	if err != nil {
		return application.Message{}, err
	}
	updated, err := optionalTeamsTime(row.UpdatedAt)
	if err != nil {
		return application.Message{}, err
	}
	author := application.MessageActor{Mode: application.MessageActorUnavailable}
	if row.AuthorID != "" {
		author = application.MessageActor{
			ID: row.AuthorID, Mode: application.MessageActorDelegatedUser,
			DisplayName: boundedTeamsText(row.AuthorName, 1024),
		}
	} else if !row.Deleted {
		return application.Message{}, errors.New("the Teams Web app exposed a message without an actor")
	}
	format := application.MessageFormat(row.Format)
	if format == "" {
		format = application.MessageFormatPlain
	}
	if err := format.Validate(); err != nil {
		return application.Message{}, errors.New("the Teams Web app exposed an unknown message format")
	}
	versionSource := strings.Join([]string{
		row.ID, updated, row.Content, row.Snippet, strconv.FormatBool(row.Deleted),
	}, "\x00")
	version := sha256.Sum256([]byte(versionSource))
	message := application.Message{
		Summary: application.MessageSummary{
			ID: row.ID, Version: "twmv1_" + hex.EncodeToString(version[:]),
			ConversationID: conversationID, ThreadRootID: row.ThreadRootID,
			Author: author, CreatedAt: created, UpdatedAt: updated,
			Snippet:    boundedTeamsText(row.Snippet, application.MaxMessageSnippetBytes),
			ReplyCount: row.ReplyCount, HasAttachments: row.HasAttachments,
			Deleted: row.Deleted,
		},
		Content: application.MessageContent{
			Format: format, Text: boundedTeamsText(row.Content, application.MaxMessageTextBytes),
		},
		Links: slices.Clone(row.Links), Mentions: slices.Clone(row.Mentions),
		Reactions: slices.Clone(row.Reactions), Attachments: slices.Clone(row.Attachments),
	}
	if row.AuthorID == actor.ID {
		message.Summary.Author = actor
	}
	return message, nil
}

func teamsConversationDigest(conversations []application.Conversation) string {
	values := make([]string, 0, len(conversations))
	for _, conversation := range conversations {
		values = append(values, conversation.ID+"\x00"+conversation.Version)
	}
	return teamsDigest(values)
}

func teamsMessageDigest(messages []application.MessageSummary) string {
	values := make([]string, 0, len(messages))
	for _, message := range messages {
		values = append(values, message.ConversationID+"\x00"+message.ID+"\x00"+message.Version)
	}
	return teamsDigest(values)
}

func teamsDigest(values []string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x01")))
	return hex.EncodeToString(digest[:])
}

func encodeTeamsVisibleOffset(offset int, digest string) string {
	return "twdo1_" + strconv.Itoa(offset) + "." + digest
}

func teamsVisibleOffset(value, expectedDigest string, maximum int) (int, error) {
	if value == "" {
		return 0, nil
	}
	raw, found := strings.CutPrefix(value, "twdo1_")
	if !found || strings.Count(raw, ".") != 1 {
		return 0, errors.New("the Teams Web visible-page cursor is malformed")
	}
	offsetRaw, digest, _ := strings.Cut(raw, ".")
	offset, err := strconv.Atoi(offsetRaw)
	if err != nil || offset < 1 || offset > maximum || digest != expectedDigest {
		return 0, errors.New("the Teams Web visible snapshot changed; restart pagination")
	}
	return offset, nil
}

func teamsTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("the Teams Web app exposed a malformed message timestamp")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func optionalTeamsTime(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return teamsTime(value)
}

func boundedTeamsValue(value string) bool {
	return value != "" && len(value) <= 1024 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func boundedTeamsText(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func teamsCallExpression(function string, arguments ...any) (string, error) {
	encoded := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		value, err := marshalScriptArgument(argument)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, value)
	}
	return "(" + function + ")(" + strings.Join(encoded, ",") + ")", nil
}

func marshalScriptArgument(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode fixed Teams Web semantic argument: %w", err)
	}
	return string(encoded), nil
}

func teamsAwaitPromise(parameters *runtime.EvaluateParams) *runtime.EvaluateParams {
	return parameters.WithAwaitPromise(true).WithUserGesture(true)
}
