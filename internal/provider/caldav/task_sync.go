package caldav

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-ical"
	webcaldav "github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type taskSyncMultiStatus struct {
	SyncToken string `xml:"sync-token"`
	Responses []struct {
		Href      string `xml:"href"`
		Status    string `xml:"status"`
		PropStats []struct {
			Status string `xml:"status"`
			Prop   struct {
				ETag         string `xml:"getetag"`
				CalendarData string `xml:"calendar-data"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

type taskSyncResult struct {
	token   string
	changes []application.TaskChange
}

func (client *Client) SyncTasks(
	ctx context.Context,
	input application.TaskSyncInput,
) (application.TaskChangePage, error) {
	taskListPath, err := client.taskListFor(input.ListID)
	if err != nil {
		return application.TaskChangePage{}, err
	}
	if !client.taskListInfo[taskListPath].sync {
		return application.TaskChangePage{}, errors.New("the selected CalDAV task list does not advertise RFC 6578 sync")
	}
	token := ""
	if input.Cursor != nil {
		token = input.Cursor.Value
		if !validSyncToken(token) {
			return application.TaskChangePage{}, errors.New("CalDAV task sync cursor is malformed")
		}
	}
	result, invalid, err := client.syncTaskList(
		ctx, taskListPath, token, input.Limit,
	)
	reset := false
	if err != nil {
		return application.TaskChangePage{}, err
	}
	if invalid {
		result, invalid, err = client.syncTaskList(
			ctx, taskListPath, "", input.Limit,
		)
		if err != nil {
			return application.TaskChangePage{}, err
		}
		if invalid {
			return application.TaskChangePage{}, errors.New("CalDAV rejected an initial RFC 6578 sync")
		}
		reset = true
	}
	return application.TaskChangePage{
		Changes: result.changes,
		Cursor: application.TaskCursor{
			Provider: domain.ProviderCalDAV,
			Account:  input.Account,
			ListID:   input.ListID,
			Mode:     application.TaskSyncToken,
			Value:    result.token,
		},
		Reset: reset,
	}, nil
}

func (client *Client) syncTaskList(
	ctx context.Context,
	taskListPath, token string,
	limit int,
) (taskSyncResult, bool, error) {
	target := *client.endpoint
	target.Path = taskListPath
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:sync-token>` + xmlEscape(token) + `</D:sync-token>` +
		`<D:sync-level>1</D:sync-level><D:limit><D:nresults>` +
		strconv.Itoa(limit) + `</D:nresults></D:limit><D:prop>` +
		`<D:getetag/><C:calendar-data/></D:prop></D:sync-collection>`
	request, err := http.NewRequestWithContext(
		ctx, "REPORT", target.String(), strings.NewReader(body),
	)
	if err != nil {
		return taskSyncResult{}, false, err
	}
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	request.Header.Set("Depth", "0")
	response, err := (*authorizedHTTPClient)(client).Do(request)
	if err != nil {
		return taskSyncResult{}, false, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusForbidden {
		var remoteError struct {
			ValidSyncToken *struct{} `xml:"valid-sync-token"`
		}
		decoder := xml.NewDecoder(response.Body)
		if err := decoder.Decode(&remoteError); err == nil && remoteError.ValidSyncToken != nil {
			return taskSyncResult{}, true, nil
		}
		return taskSyncResult{}, false, errors.New("CalDAV task sync was forbidden")
	}
	if response.StatusCode != http.StatusMultiStatus {
		return taskSyncResult{}, false, fmt.Errorf(
			"CalDAV task sync returned HTTP %d",
			response.StatusCode,
		)
	}
	var remote taskSyncMultiStatus
	decoder := xml.NewDecoder(response.Body)
	if err := decoder.Decode(&remote); err != nil {
		return taskSyncResult{}, false, err
	}
	remote.SyncToken = strings.TrimSpace(remote.SyncToken)
	if !validSyncToken(remote.SyncToken) || remote.SyncToken == "" {
		return taskSyncResult{}, false, errors.New("CalDAV task sync omitted a valid next token")
	}
	changes := make([]application.TaskChange, 0, len(remote.Responses))
	seen := make(map[string]bool, len(remote.Responses))
	for _, item := range remote.Responses {
		objectPath, ok := client.davPath(item.Href)
		if !ok || !pathWithin(objectPath, taskListPath) {
			if pathCleanEqual(objectPath, taskListPath) {
				continue
			}
			return taskSyncResult{}, false, errors.New("CalDAV task sync escaped the selected list")
		}
		if seen[objectPath] {
			return taskSyncResult{}, false, errors.New("CalDAV task sync returned a duplicate object")
		}
		seen[objectPath] = true
		if strings.Contains(item.Status, " 404 ") {
			id, err := encodeTaskID(taskReference{
				List: taskListPath, Path: objectPath,
			})
			if err != nil {
				return taskSyncResult{}, false, err
			}
			changes = append(changes, application.TaskChange{
				Kind:    application.TaskChangeDelete,
				TaskID:  id,
				Version: taskTombstoneVersion(objectPath, remote.SyncToken),
			})
			continue
		}
		etag, calendarData, ok := successfulTaskSyncProperties(item.PropStats)
		if !ok {
			if strings.Contains(item.Status, " 507 ") {
				continue
			}
			return taskSyncResult{}, false, errors.New("CalDAV task sync returned an unusable object")
		}
		calendar, err := ical.NewDecoder(strings.NewReader(calendarData)).Decode()
		if err != nil {
			return taskSyncResult{}, false, err
		}
		master, err := taskMaster(calendar, "")
		if err != nil {
			return taskSyncResult{}, false, err
		}
		view, err := client.taskView(taskListPath, webcaldav.CalendarObject{
			Path: objectPath, ETag: etag, Data: calendar,
		}, master)
		if err != nil {
			return taskSyncResult{}, false, err
		}
		changes = append(changes, application.TaskChange{
			Kind: application.TaskChangeUpsert, Task: &view,
		})
		if len(changes) > limit {
			return taskSyncResult{}, false, errors.New("CalDAV task sync exceeded the requested limit")
		}
	}
	return taskSyncResult{token: remote.SyncToken, changes: changes}, false, nil
}

func successfulTaskSyncProperties(propstats []struct {
	Status string `xml:"status"`
	Prop   struct {
		ETag         string `xml:"getetag"`
		CalendarData string `xml:"calendar-data"`
	} `xml:"prop"`
}) (string, string, bool) {
	for _, propstat := range propstats {
		if !strings.Contains(propstat.Status, " 200 ") {
			continue
		}
		etag := strongETag(strings.TrimSpace(propstat.Prop.ETag))
		if validObjectETag(etag) && propstat.Prop.CalendarData != "" {
			return etag, propstat.Prop.CalendarData, true
		}
	}
	return "", "", false
}

func xmlEscape(value string) string {
	var encoded bytes.Buffer
	_ = xml.EscapeText(&encoded, []byte(value))
	return encoded.String()
}

func taskTombstoneVersion(objectPath, token string) string {
	digest := sha256.Sum256([]byte(objectPath + "\x00" + token))
	return "cdtv1_" + hex.EncodeToString(digest[:16])
}

func pathCleanEqual(left, right string) bool {
	return strings.TrimSuffix(left, "/") == strings.TrimSuffix(right, "/")
}
