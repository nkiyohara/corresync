package todoist

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumCursorJSONBytes = 64 << 10

type pendingChange struct {
	Kind    string `json:"k"`
	ID      string `json:"i"`
	Version string `json:"v,omitempty"`
}

type cursorEnvelope struct {
	Version int             `json:"v"`
	Token   string          `json:"t"`
	Members []string        `json:"m,omitempty"`
	Pending []pendingChange `json:"p,omitempty"`
}

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
) (application.TaskChangePage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskChangePage{}, err
	}
	projectID, err := decodeID("tdl1_", input.ListID)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	state := cursorEnvelope{Version: 1, Token: "*"}
	if input.Cursor != nil {
		state, err = decodeCursor(input.Cursor.Value)
		if err != nil {
			return application.TaskChangePage{}, err
		}
	}
	reset := false
	if len(state.Pending) == 0 {
		state, reset, err = client.advanceSync(ctx, projectID, state)
		if err != nil {
			return application.TaskChangePage{}, err
		}
	}
	changes, next, err := client.drainChanges(ctx, input.ListID, projectID, state, input.Limit)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	encoded, err := encodeCursor(next)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	return application.TaskChangePage{
		Changes: changes,
		Cursor: application.TaskCursor{
			Provider: domain.ProviderTodoist, Account: input.Account,
			ListID: input.ListID, Mode: application.TaskSyncToken,
			Value: encoded,
		},
		Reset: reset,
	}, nil
}

func (client *Client) advanceSync(
	ctx context.Context,
	projectID string,
	state cursorEnvelope,
) (cursorEnvelope, bool, error) {
	resourceTypes, _ := marshalJSON([]string{"items", "reminders"})
	var response syncResponse
	if _, err := client.api.DoForm(
		ctx, http.MethodPost, "sync", nil,
		url.Values{
			"sync_token": {state.Token}, "resource_types": {resourceTypes},
		},
		&response, false, nil, http.StatusOK,
	); err != nil {
		return cursorEnvelope{}, false, err
	}
	if response.SyncToken == "" || len(response.SyncToken) > 4096 ||
		strings.ContainsAny(response.SyncToken, "\r\n\x00") {
		return cursorEnvelope{}, false, errors.New("todoist returned a malformed sync token")
	}
	reset := state.Token != "*" && response.FullSync
	full := state.Token == "*" || response.FullSync
	members := make(map[string]bool, len(state.Members)+len(response.Items))
	if !full {
		for _, id := range state.Members {
			members[id] = true
		}
	}
	pending := make(map[string]pendingChange)
	for _, remote := range response.Items {
		if !validID(remote.ID) || remote.ProjectID != "" && !validID(remote.ProjectID) {
			return cursorEnvelope{}, false, errors.New("todoist sync returned an invalid task identity")
		}
		activeHere := remote.ProjectID == projectID && !remote.Deleted &&
			!remote.Checked && remote.CompletedAt == ""
		if activeHere {
			members[remote.ID] = true
			pending[remote.ID] = pendingChange{Kind: "u", ID: remote.ID}
		} else if members[remote.ID] {
			delete(members, remote.ID)
			pending[remote.ID] = pendingChange{
				Kind: "d", ID: remote.ID,
				Version: tombstoneVersion(remote.ID, response.SyncToken),
			}
		}
	}
	for _, changed := range response.Reminders {
		if !validID(changed.ID) || !validID(changed.ItemID) {
			return cursorEnvelope{}, false, errors.New("todoist sync returned an invalid reminder identity")
		}
		if members[changed.ItemID] {
			pending[changed.ItemID] = pendingChange{Kind: "u", ID: changed.ItemID}
		}
	}
	memberIDs := make([]string, 0, len(members))
	for id := range members {
		memberIDs = append(memberIDs, id)
	}
	sort.Strings(memberIDs)
	pendingIDs := make([]string, 0, len(pending))
	for id := range pending {
		pendingIDs = append(pendingIDs, id)
	}
	sort.Strings(pendingIDs)
	queued := make([]pendingChange, 0, len(pendingIDs))
	for _, id := range pendingIDs {
		queued = append(queued, pending[id])
	}
	return cursorEnvelope{
		Version: 1, Token: response.SyncToken,
		Members: memberIDs, Pending: queued,
	}, reset, nil
}

func (client *Client) drainChanges(
	ctx context.Context,
	listID, projectID string,
	state cursorEnvelope,
	limit int,
) ([]application.TaskChange, cursorEnvelope, error) {
	count := min(limit, len(state.Pending))
	selected := append([]pendingChange(nil), state.Pending[:count]...)
	upsertIDs := make([]string, 0, count)
	for _, change := range selected {
		if change.Kind == "u" {
			upsertIDs = append(upsertIDs, change.ID)
		}
	}
	remotes, err := client.tasksByIDs(ctx, projectID, upsertIDs)
	if err != nil {
		return nil, cursorEnvelope{}, err
	}
	reminders, err := client.remindersForTasks(ctx, upsertIDs)
	if err != nil {
		return nil, cursorEnvelope{}, err
	}
	changes := make([]application.TaskChange, 0, count)
	for _, change := range selected {
		switch change.Kind {
		case "u":
			remote, exists := remotes[change.ID]
			if !exists {
				return nil, cursorEnvelope{}, errors.New("todoist sync upsert was absent from the active task API")
			}
			view, err := client.taskView(listID, remote, reminders[change.ID])
			if err != nil {
				return nil, cursorEnvelope{}, err
			}
			changes = append(changes, application.TaskChange{
				Kind: application.TaskChangeUpsert, Task: &view,
			})
		case "d":
			id, err := encodeID("tdt1_", change.ID)
			if err != nil {
				return nil, cursorEnvelope{}, err
			}
			changes = append(changes, application.TaskChange{
				Kind: application.TaskChangeDelete, TaskID: id,
				Version: change.Version,
			})
		default:
			return nil, cursorEnvelope{}, errors.New("todoist cursor contains an unknown change kind")
		}
	}
	state.Pending = append([]pendingChange(nil), state.Pending[count:]...)
	return changes, state, nil
}

func (client *Client) tasksByIDs(
	ctx context.Context,
	projectID string,
	ids []string,
) (map[string]task, error) {
	result := make(map[string]task, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	if len(ids) > application.MaxTaskPageSize {
		return nil, errors.New("todoist task ID batch exceeds the configured bound")
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, errors.New("todoist task ID batch is malformed")
		}
	}
	query := url.Values{
		"ids": {strings.Join(ids, ",")}, "limit": {strconv.Itoa(len(ids))},
	}
	var response page[task]
	if _, err := client.api.DoJSON(
		ctx, http.MethodGet, "tasks", query, nil, &response,
		false, nil, http.StatusOK,
	); err != nil {
		return nil, err
	}
	if response.NextCursor != "" || len(response.Results) != len(ids) {
		return nil, errors.New("todoist returned an incomplete task ID batch")
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	for _, remote := range response.Results {
		if !requested[remote.ID] || result[remote.ID].ID != "" ||
			!validTask(remote) || remote.ProjectID != projectID ||
			remote.Deleted || remote.Checked || remote.CompletedAt != "" {
			return nil, errors.New("todoist task ID batch escaped its selected route")
		}
		result[remote.ID] = remote
	}
	return result, nil
}

func encodeCursor(value cursorEnvelope) (string, error) {
	if err := validateCursor(value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := writer.Write(encoded); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	result := "tdc1_" + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	if len(result) > application.MaxTaskCursorBytes {
		return "", errors.New("todoist sync state exceeds the canonical cursor limit")
	}
	return result, nil
}

func decodeCursor(value string) (cursorEnvelope, error) {
	if !strings.HasPrefix(value, "tdc1_") || len(value) > application.MaxTaskCursorBytes {
		return cursorEnvelope{}, errors.New("task cursor is not a Todoist sync token")
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "tdc1_"))
	if err != nil {
		return cursorEnvelope{}, errors.New("todoist task cursor is malformed")
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return cursorEnvelope{}, errors.New("todoist task cursor is malformed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, maximumCursorJSONBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(raw) > maximumCursorJSONBytes {
		return cursorEnvelope{}, errors.New("todoist task cursor is malformed or too large")
	}
	var envelope cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return cursorEnvelope{}, errors.New("todoist task cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return cursorEnvelope{}, errors.New("todoist task cursor has trailing content")
	}
	if err := validateCursor(envelope); err != nil {
		return cursorEnvelope{}, err
	}
	return envelope, nil
}

func validateCursor(value cursorEnvelope) error {
	if value.Version != 1 || value.Token == "" || len(value.Token) > 4096 ||
		strings.ContainsAny(value.Token, "\r\n\x00") ||
		len(value.Members) > 10_000 || len(value.Pending) > 10_000 {
		return errors.New("todoist task cursor is malformed")
	}
	if !sort.StringsAreSorted(value.Members) || slices.ContainsFunc(
		value.Members, func(id string) bool { return !validID(id) },
	) {
		return errors.New("todoist task cursor membership is malformed")
	}
	for index, id := range value.Members {
		if index > 0 && value.Members[index-1] == id {
			return errors.New("todoist task cursor membership is duplicated")
		}
	}
	previous := ""
	for _, change := range value.Pending {
		if !validID(change.ID) || change.ID <= previous ||
			change.Kind != "u" && change.Kind != "d" ||
			change.Kind == "d" && change.Version == "" ||
			change.Kind == "u" && change.Version != "" {
			return errors.New("todoist task cursor pending changes are malformed")
		}
		previous = change.ID
	}
	return nil
}

func tombstoneVersion(id, token string) string {
	digest := sha256.Sum256([]byte(id + "\x00" + token))
	return "tdvd1_" + hex.EncodeToString(digest[:])
}
