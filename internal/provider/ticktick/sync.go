package ticktick

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
	"sort"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const maximumCursorJSONBytes = 64 << 10

type cursorMember struct {
	ID      string `json:"i"`
	Version string `json:"v"`
}

type pendingChange struct {
	Kind    string `json:"k"`
	ID      string `json:"i"`
	Version string `json:"v,omitempty"`
}

type cursorEnvelope struct {
	Version int             `json:"v"`
	Route   string          `json:"r"`
	Members []cursorMember  `json:"m,omitempty"`
	Pending []pendingChange `json:"p,omitempty"`
}

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
) (application.TaskChangePage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskChangePage{}, err
	}
	projectID, err := decodeID("ttl1_", input.ListID)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	route := cursorRoute(client.account, projectID)
	state := cursorEnvelope{Version: 1, Route: route}
	if input.Cursor != nil {
		state, err = decodeCursor(input.Cursor.Value, route)
		if err != nil {
			return application.TaskChangePage{}, err
		}
	}
	if len(state.Pending) == 0 {
		state, err = client.advanceSnapshot(ctx, projectID, state)
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
			Provider: domain.ProviderTickTick, Account: input.Account,
			ListID: input.ListID, Mode: application.TaskSyncPolling, Value: encoded,
		},
	}, nil
}

func (client *Client) advanceSnapshot(
	ctx context.Context,
	projectID string,
	state cursorEnvelope,
) (cursorEnvelope, error) {
	remotes, err := client.filterTasks(ctx, "open/v1/task/filter", map[string]any{
		"projectIds": []string{projectID},
	})
	if err != nil {
		return cursorEnvelope{}, err
	}
	if len(remotes) >= providerTaskCap {
		return cursorEnvelope{}, errors.New("ticktick polling snapshot reached the provider's unpageable 200-task limit")
	}
	previous := make(map[string]string, len(state.Members))
	for _, member := range state.Members {
		previous[member.ID] = member.Version
	}
	next := make(map[string]string, len(remotes))
	pending := make([]pendingChange, 0, len(remotes)+len(previous))
	for _, remote := range remotes {
		if remote.ProjectID != projectID {
			return cursorEnvelope{}, errors.New("ticktick polling returned a task outside the selected list")
		}
		version := encodeVersion(remote)
		next[remote.ID] = version
		if previous[remote.ID] != version {
			pending = append(pending, pendingChange{
				Kind: "u", ID: remote.ID, Version: version,
			})
		}
		delete(previous, remote.ID)
	}
	for id, version := range previous {
		pending = append(pending, pendingChange{
			Kind: "d", ID: id, Version: tombstoneVersion(id, version),
		})
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].ID < pending[right].ID })
	members := make([]cursorMember, 0, len(next))
	for id, version := range next {
		members = append(members, cursorMember{ID: id, Version: version})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].ID < members[right].ID })
	return cursorEnvelope{
		Version: 1, Route: state.Route, Members: members, Pending: pending,
	}, nil
}

func (client *Client) drainChanges(
	ctx context.Context,
	listID, projectID string,
	state cursorEnvelope,
	limit int,
) ([]application.TaskChange, cursorEnvelope, error) {
	count := min(limit, len(state.Pending))
	selected := append([]pendingChange(nil), state.Pending[:count]...)
	changes := make([]application.TaskChange, 0, count)
	for _, change := range selected {
		switch change.Kind {
		case "u":
			remote, err := client.getTask(ctx, projectID, change.ID)
			if restapi.IsStatus(err, http.StatusNotFound) {
				id, encodeErr := encodeID("ttt1_", change.ID)
				if encodeErr != nil {
					return nil, cursorEnvelope{}, encodeErr
				}
				changes = append(changes, application.TaskChange{
					Kind: application.TaskChangeDelete, TaskID: id,
					Version: tombstoneVersion(change.ID, change.Version),
				})
				state.Members = removeCursorMember(state.Members, change.ID)
				continue
			}
			if err != nil {
				return nil, cursorEnvelope{}, err
			}
			view, err := client.taskView(listID, remote)
			if err != nil {
				return nil, cursorEnvelope{}, err
			}
			changes = append(changes, application.TaskChange{
				Kind: application.TaskChangeUpsert, Task: &view,
			})
		case "d":
			id, err := encodeID("ttt1_", change.ID)
			if err != nil {
				return nil, cursorEnvelope{}, err
			}
			changes = append(changes, application.TaskChange{
				Kind: application.TaskChangeDelete, TaskID: id, Version: change.Version,
			})
		default:
			return nil, cursorEnvelope{}, errors.New("ticktick cursor contains an unknown change kind")
		}
	}
	state.Pending = append([]pendingChange(nil), state.Pending[count:]...)
	return changes, state, nil
}

func removeCursorMember(members []cursorMember, id string) []cursorMember {
	for index := range members {
		if members[index].ID == id {
			return append(members[:index], members[index+1:]...)
		}
	}
	return members
}

func tombstoneVersion(id, lastVersion string) string {
	digest := sha256.Sum256([]byte(id + "\x00" + lastVersion))
	return "ttx1_" + hex.EncodeToString(digest[:])
}

func encodeCursor(cursor cursorEnvelope) (string, error) {
	if err := validateCursor(cursor); err != nil {
		return "", err
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	if len(raw) > maximumCursorJSONBytes {
		return "", errors.New("ticktick task cursor state exceeds the configured limit")
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	result := "ttc1_" + base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	if len(result) > application.MaxTaskCursorBytes {
		return "", errors.New("ticktick task cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeCursor(value, route string) (cursorEnvelope, error) {
	if !strings.HasPrefix(value, "ttc1_") || len(value) > application.MaxTaskCursorBytes {
		return cursorEnvelope{}, errors.New("task cursor is not a TickTick polling cursor")
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "ttc1_"))
	if err != nil || len(compressed) > application.MaxTaskCursorBytes {
		return cursorEnvelope{}, errors.New("ticktick task cursor is malformed")
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return cursorEnvelope{}, errors.New("ticktick task cursor is malformed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, maximumCursorJSONBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(raw) > maximumCursorJSONBytes {
		return cursorEnvelope{}, errors.New("ticktick task cursor is malformed or oversized")
	}
	var cursor cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursorEnvelope{}, errors.New("ticktick task cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return cursorEnvelope{}, errors.New("ticktick task cursor has trailing content")
	}
	if err := validateCursor(cursor); err != nil {
		return cursorEnvelope{}, err
	}
	if cursor.Route != route {
		return cursorEnvelope{}, errors.New("ticktick task cursor belongs to a different account or list")
	}
	return cursor, nil
}

func validateCursor(cursor cursorEnvelope) error {
	if cursor.Version != 1 || !validCursorRoute(cursor.Route) ||
		len(cursor.Members) > providerTaskCap || len(cursor.Pending) > providerTaskCap*2 {
		return errors.New("ticktick task cursor version or size is invalid")
	}
	memberVersions := make(map[string]string, len(cursor.Members))
	for _, member := range cursor.Members {
		if !validProviderID(member.ID) || member.Version == "" || len(member.Version) > 2048 ||
			memberVersions[member.ID] != "" {
			return errors.New("ticktick task cursor member is malformed")
		}
		memberVersions[member.ID] = member.Version
	}
	pendingIDs := make(map[string]bool, len(cursor.Pending))
	for _, change := range cursor.Pending {
		if !validProviderID(change.ID) || pendingIDs[change.ID] || change.Version == "" ||
			len(change.Version) > 2048 ||
			change.Kind != "u" && change.Kind != "d" ||
			change.Kind == "u" && memberVersions[change.ID] != change.Version ||
			change.Kind == "d" && memberVersions[change.ID] != "" {
			return errors.New("ticktick task cursor change is malformed")
		}
		pendingIDs[change.ID] = true
	}
	return nil
}

func cursorRoute(account domain.AccountID, projectID string) string {
	digest := sha256.Sum256([]byte(string(account) + "\x00" + projectID))
	return "ttr1_" + hex.EncodeToString(digest[:])
}

func validCursorRoute(value string) bool {
	if !strings.HasPrefix(value, "ttr1_") || len(value) != len("ttr1_")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "ttr1_"))
	return err == nil
}
