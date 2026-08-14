package googletasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

type pollingCursor struct {
	Version   int    `json:"v"`
	Watermark string `json:"w,omitempty"`
	PageToken string `json:"p,omitempty"`
	ScanStart string `json:"s,omitempty"`
	Pages     int    `json:"n,omitempty"`
}

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
) (application.TaskChangePage, error) {
	if err := client.requireRead(); err != nil {
		return application.TaskChangePage{}, err
	}
	listID, err := decodeID("gtl1_", input.ListID)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	cursor := pollingCursor{Version: 1}
	if input.Cursor != nil {
		cursor, err = decodeCursor(input.Cursor.Value)
		if err != nil {
			return application.TaskChangePage{}, err
		}
	}
	if cursor.ScanStart == "" {
		cursor.ScanStart = client.now().UTC().Format(time.RFC3339Nano)
	}
	response, err := client.poll(ctx, listID, input.Limit, cursor)
	reset := false
	if err != nil && cursor.PageToken != "" &&
		(restapi.IsStatus(err, http.StatusBadRequest) || restapi.IsStatus(err, http.StatusGone)) {
		cursor = pollingCursor{
			Version: 1, ScanStart: client.now().UTC().Format(time.RFC3339Nano),
		}
		response, err = client.poll(ctx, listID, input.Limit, cursor)
		reset = err == nil
	}
	if err != nil {
		return application.TaskChangePage{}, err
	}
	if len(response.Items) > input.Limit || response.NextPageToken != "" &&
		(!validPageToken(response.NextPageToken) ||
			response.NextPageToken == cursor.PageToken ||
			cursor.Pages >= maximumPageCalls-1) {
		return application.TaskChangePage{}, errors.New("google Tasks returned an invalid polling page")
	}
	changes := make([]application.TaskChange, 0, len(response.Items))
	for _, remote := range response.Items {
		if !validProviderID(remote.ID) || remote.Updated == "" {
			return application.TaskChangePage{}, errors.New("google Tasks returned an invalid polling item")
		}
		if _, parseErr := time.Parse(time.RFC3339Nano, remote.Updated); parseErr != nil {
			return application.TaskChangePage{}, errors.New("google Tasks returned an invalid polling timestamp")
		}
		if remote.Deleted {
			id, _ := encodeID("gtt1_", remote.ID)
			changes = append(changes, application.TaskChange{
				Kind: application.TaskChangeDelete, TaskID: id,
				Version: tombstoneVersion(remote),
			})
			continue
		}
		view, viewErr := client.taskView(input.ListID, remote)
		if viewErr != nil {
			return application.TaskChangePage{}, viewErr
		}
		changes = append(changes, application.TaskChange{
			Kind: application.TaskChangeUpsert, Task: &view,
		})
	}
	if response.NextPageToken != "" {
		cursor.PageToken = response.NextPageToken
		cursor.Pages++
	} else {
		cursor = pollingCursor{Version: 1, Watermark: cursor.ScanStart}
	}
	encoded, err := encodeCursor(cursor)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	return application.TaskChangePage{
		Changes: changes,
		Cursor: application.TaskCursor{
			Provider: domain.ProviderGoogleTasks, Account: input.Account,
			ListID: input.ListID, Mode: application.TaskSyncPolling, Value: encoded,
		},
		Reset: reset,
	}, nil
}

func (client *Client) poll(
	ctx context.Context,
	listID string,
	limit int,
	cursor pollingCursor,
) (taskPage, error) {
	query := queryValues(
		"maxResults", strconv.Itoa(limit),
		"showAssigned", "true",
		"showCompleted", "true",
		"showDeleted", "true",
		"showHidden", "true",
	)
	if cursor.Watermark != "" {
		query.Set("updatedMin", cursor.Watermark)
	}
	if cursor.PageToken != "" {
		query.Set("pageToken", cursor.PageToken)
	}
	var response taskPage
	_, err := client.api.DoJSON(
		ctx, http.MethodGet, taskCollection(listID), query,
		nil, &response, false, nil, http.StatusOK,
	)
	return response, err
}

func encodeCursor(cursor pollingCursor) (string, error) {
	if err := validateCursor(cursor); err != nil {
		return "", err
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	result := "gtc1_" + base64.RawURLEncoding.EncodeToString(raw)
	if len(result) > application.MaxTaskCursorBytes {
		return "", errors.New("google task polling cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeCursor(value string) (pollingCursor, error) {
	if !strings.HasPrefix(value, "gtc1_") || len(value) > application.MaxTaskCursorBytes {
		return pollingCursor{}, errors.New("task cursor is not a Google polling cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "gtc1_"))
	if err != nil || len(raw) > 16<<10 {
		return pollingCursor{}, errors.New("google task polling cursor is malformed")
	}
	var cursor pollingCursor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return pollingCursor{}, errors.New("google task polling cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pollingCursor{}, errors.New("google task polling cursor has trailing content")
	}
	if err := validateCursor(cursor); err != nil {
		return pollingCursor{}, err
	}
	return cursor, nil
}

func validateCursor(cursor pollingCursor) error {
	if cursor.Version != 1 {
		return errors.New("google task polling cursor version is unsupported")
	}
	for _, value := range []string{cursor.Watermark, cursor.ScanStart} {
		if value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || parsed.Location() != time.UTC {
				return errors.New("google task polling cursor timestamp is malformed")
			}
		}
	}
	if cursor.PageToken != "" {
		if !validPageToken(cursor.PageToken) || cursor.ScanStart == "" ||
			cursor.Pages < 1 || cursor.Pages >= maximumPageCalls {
			return errors.New("google task polling cursor page is malformed")
		}
	} else if cursor.ScanStart != "" || cursor.Pages != 0 {
		return errors.New("google task polling cursor has an incomplete scan")
	}
	return nil
}
