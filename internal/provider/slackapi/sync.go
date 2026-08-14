package slackapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type slackSyncState struct {
	Since string `json:"since"`
	Page  string `json:"page,omitempty"`
	High  string `json:"high"`
}

func (client *Client) SyncMessages(
	ctx context.Context,
	input application.MessageSyncInput,
) (application.MessageChangePage, error) {
	if err := client.requireCapability(client.capabilities.IncrementalSync, "message synchronization"); err != nil {
		return application.MessageChangePage{}, err
	}
	if !validSlackID(input.ConversationID) {
		return application.MessageChangePage{}, errors.New("slack polling requires one exact conversation")
	}
	state := slackSyncState{Since: "0", High: "0"}
	if input.Cursor != nil {
		decoded, err := decodeSlackSyncState(input.Cursor.Opaque)
		if err != nil {
			return application.MessageChangePage{}, err
		}
		state = decoded
	}
	query := url.Values{
		"channel": {input.ConversationID}, "cursor": {state.Page},
		"limit": {strconv.Itoa(min(input.Limit, slackHistoryPageLimit))},
	}
	if state.Since != "0" {
		query.Set("oldest", state.Since)
		query.Set("inclusive", "false")
	}
	var response struct {
		slackEnvelope
		Messages []slackMessage `json:"messages"`
		HasMore  bool           `json:"has_more,omitempty"`
	}
	result, err := client.api.DoJSON(
		ctx, http.MethodGet, "conversations.history", query, nil, &response,
		false, nil, http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil {
		return application.MessageChangePage{}, err
	}
	if err := validateSlackResponse(result, response.slackEnvelope, false); err != nil {
		return application.MessageChangePage{}, err
	}
	if len(response.Messages) > min(input.Limit, slackHistoryPageLimit) {
		return application.MessageChangePage{}, errors.New("slack returned too many message changes")
	}
	if response.HasMore && response.Metadata.NextCursor == "" {
		return application.MessageChangePage{}, errors.New("slack omitted the required history continuation cursor")
	}
	changes := make([]application.MessageChange, 0, len(response.Messages))
	for _, source := range response.Messages {
		summary, err := mapSlackSummary(source, input.ConversationID)
		if err != nil {
			return application.MessageChangePage{}, err
		}
		if newerSlackTimestamp(source.TS, state.High) {
			state.High = source.TS
		}
		changes = append(changes, application.MessageChange{
			Kind: application.MessageChangeUpsert, Message: &summary,
		})
	}
	state.Page = response.Metadata.NextCursor
	hasMore := state.Page != ""
	if !hasMore {
		state.Since = state.High
	}
	encoded, err := encodeSlackSyncState(state)
	if err != nil {
		return application.MessageChangePage{}, err
	}
	degradations := slackWarningDegradations(response.slackEnvelope)
	return application.MessageChangePage{
		Changes: changes,
		Cursor: application.MessageCursor{
			Version: 1, Account: input.Account, Provider: domain.MessagingProviderSlack,
			Route: domain.MessagingRouteSlackAPI, WorkspaceID: input.WorkspaceID,
			ConversationID: input.ConversationID, Opaque: encoded,
		},
		HasMore: hasMore, ObservedAt: time.Now().UTC(), Degradations: degradations,
	}, nil
}

func encodeSlackSyncState(state slackSyncState) (string, error) {
	if err := state.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	result := "sly1_" + base64.RawURLEncoding.EncodeToString(encoded)
	if len(result) > application.MaxMessageCursorBytes {
		return "", errors.New("slack sync cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeSlackSyncState(value string) (slackSyncState, error) {
	raw, found := strings.CutPrefix(value, "sly1_")
	if !found || raw == "" || len(value) > application.MaxMessageCursorBytes {
		return slackSyncState{}, errors.New("slack sync cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return slackSyncState{}, errors.New("slack sync cursor is malformed")
	}
	var state slackSyncState
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return slackSyncState{}, errors.New("slack sync cursor is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return slackSyncState{}, errors.New("slack sync cursor has trailing data")
	}
	if err := state.validate(); err != nil {
		return slackSyncState{}, err
	}
	return state, nil
}

func (state slackSyncState) validate() error {
	for _, value := range []string{state.Since, state.High} {
		if value != "0" && !validSlackTimestamp(value) {
			return errors.New("slack sync cursor timestamp is malformed")
		}
	}
	if state.Page != "" && (len(state.Page) > application.MaxMessageCursorBytes || strings.ContainsAny(state.Page, "\r\n\x00")) {
		return errors.New("slack sync page cursor is malformed")
	}
	if state.High == "0" && state.Since != "0" {
		return errors.New("slack sync cursor high-water mark is missing")
	}
	return nil
}

func newerSlackTimestamp(candidate, current string) bool {
	if current == "0" {
		return true
	}
	left, leftErr := slackTime(candidate)
	right, rightErr := slackTime(current)
	return leftErr == nil && rightErr == nil && left.After(right)
}
