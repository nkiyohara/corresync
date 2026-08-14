package googletasks

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type taskList struct {
	ID      string `json:"id"`
	ETag    string `json:"etag"`
	Title   string `json:"title"`
	Updated string `json:"updated"`
}

type taskListPage struct {
	NextPageToken string     `json:"nextPageToken"`
	Items         []taskList `json:"items"`
}

type taskLink struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Link        string `json:"link"`
}

type assignmentInfo struct {
	LinkToTask  string `json:"linkToTask"`
	SurfaceType string `json:"surfaceType"`
}

type task struct {
	ID             string          `json:"id"`
	ETag           string          `json:"etag"`
	Title          string          `json:"title"`
	Updated        string          `json:"updated"`
	Parent         string          `json:"parent"`
	Position       string          `json:"position"`
	Notes          string          `json:"notes"`
	Status         string          `json:"status"`
	Due            string          `json:"due"`
	Completed      string          `json:"completed"`
	Deleted        bool            `json:"deleted"`
	Hidden         bool            `json:"hidden"`
	Links          []taskLink      `json:"links"`
	WebViewLink    string          `json:"webViewLink"`
	AssignmentInfo *assignmentInfo `json:"assignmentInfo"`
}

type taskPage struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []task `json:"items"`
}

func (client *Client) taskView(listID string, remote task) (application.Task, error) {
	if !validTask(remote) || remote.Deleted {
		return application.Task{}, errors.New("google Tasks returned an invalid active task")
	}
	id, _ := encodeID("gtt1_", remote.ID)
	parentID := ""
	var err error
	if remote.Parent != "" {
		parentID, err = encodeID("gtt1_", remote.Parent)
		if err != nil {
			return application.Task{}, err
		}
	}
	status, err := readStatus(remote.Status)
	if err != nil {
		return application.Task{}, err
	}
	due, err := readDue(remote.Due)
	if err != nil {
		return application.Task{}, err
	}
	completed, err := readCompletion(remote.Completed)
	if err != nil {
		return application.Task{}, err
	}
	result := application.Task{
		ID: id, Version: encodeETag(remote.ETag), ListID: listID,
		ParentID: parentID, Title: remote.Title, Notes: remote.Notes,
		Status: status, Priority: application.TaskPriorityNone,
		Due: due, CompletedAt: completed, Order: remote.Position,
	}
	if result.Title == "" {
		result.Title = "Untitled Google task"
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.title", Reason: "an empty Google task title is displayed with a local placeholder", Lossy: true,
		})
	}
	if status == application.TaskStatusCompleted && completed == nil {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "completion_time", Reason: "Google marked the task completed without a completion timestamp",
		})
	}
	if status != application.TaskStatusCompleted && completed != nil {
		return application.Task{}, errors.New("google Tasks returned a completion time for an incomplete task")
	}
	if remote.Hidden {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.hidden", Reason: "Google hid this completed task when its list was cleared",
		})
	}
	if remote.AssignmentInfo != nil {
		result.Assignees = []application.TaskAssignee{{ID: "google_current_user", Self: true}}
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.assigned_writes", Reason: "this task is assigned from " + assignmentSurface(remote.AssignmentInfo.SurfaceType) + "; source metadata and nesting are read-only",
		})
	}
	result.Sources, result.Attachments, err = client.readLinks(remote)
	if err != nil {
		return application.Task{}, err
	}
	return result, nil
}

func (client *Client) readLinks(remote task) (
	[]application.TaskLinkedSource,
	[]application.TaskAttachmentLink,
	error,
) {
	links := append([]taskLink(nil), remote.Links...)
	if remote.AssignmentInfo != nil && remote.AssignmentInfo.LinkToTask != "" {
		links = append(links, taskLink{
			Type: "assignment", Description: assignmentSurface(remote.AssignmentInfo.SurfaceType),
			Link: remote.AssignmentInfo.LinkToTask,
		})
	}
	if remote.WebViewLink != "" {
		links = append(links, taskLink{Type: "web", Description: "Google Tasks", Link: remote.WebViewLink})
	}
	if len(links) > application.MaxTaskCollectionEntries {
		return nil, nil, errors.New("google task contains too many links")
	}
	sources := make([]application.TaskLinkedSource, 0, len(links))
	attachments := make([]application.TaskAttachmentLink, 0, len(links))
	seen := make(map[string]bool, len(links))
	for _, remoteLink := range links {
		if seen[remoteLink.Link] {
			continue
		}
		seen[remoteLink.Link] = true
		if err := validHTTPSLink(remoteLink.Link); err != nil {
			return nil, nil, err
		}
		name := strings.TrimSpace(remoteLink.Description)
		if name == "" {
			name = strings.TrimSpace(remoteLink.Type)
		}
		if name == "" {
			name = "Google task source"
		}
		if len(name) > 1024 {
			return nil, nil, errors.New("google task link description is too large")
		}
		digest := sha256.Sum256([]byte(remoteLink.Link))
		objectID := "gtx1_" + hex.EncodeToString(digest[:])
		sources = append(sources, application.TaskLinkedSource{
			Kind: application.TaskSourceExternal, Account: client.account,
			Provider: domain.ProviderGoogleTasks, ObjectID: objectID, URL: remoteLink.Link,
		})
		attachments = append(attachments, application.TaskAttachmentLink{
			Name: name, URL: remoteLink.Link,
		})
	}
	return sources, attachments, nil
}

func assignmentSurface(value string) string {
	switch value {
	case "DOCUMENT":
		return "Google Docs"
	case "SPACE":
		return "Google Chat"
	case "GMAIL":
		return "Gmail"
	case "", "CONTEXT_TYPE_UNSPECIFIED":
		return "an unspecified Google surface"
	default:
		return "an unrecognized Google surface"
	}
}

func readStatus(value string) (application.TaskStatus, error) {
	switch value {
	case "needsAction":
		return application.TaskStatusNeedsAction, nil
	case "completed":
		return application.TaskStatusCompleted, nil
	default:
		return "", errors.New("google Tasks returned an unknown task status")
	}
}

func readDue(value string) (*application.TaskTemporal, error) {
	if value == "" {
		return nil, nil
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("google task due date is malformed")
	}
	return &application.TaskTemporal{
		Kind: application.TaskTemporalDate, Value: instant.Format(time.DateOnly),
	}, nil
}

func readCompletion(value string) (*application.TaskTemporal, error) {
	if value == "" {
		return nil, nil
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("google task completion time is malformed")
	}
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: instant.UTC().Format(time.RFC3339), TimeZone: "UTC",
	}, nil
}

func writeDue(value *application.TaskTemporal) (any, error) {
	if value == nil {
		return nil, nil
	}
	if value.Kind != application.TaskTemporalDate {
		return nil, errors.New("google Tasks accepts only a date-only due value")
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return value.Value + "T00:00:00.000Z", nil
}

func validTaskList(value taskList) bool {
	if !validProviderID(value.ID) || value.Title == "" || len(value.Title) > 1024 ||
		!validETag(value.ETag) {
		return false
	}
	if value.Updated != "" {
		_, err := time.Parse(time.RFC3339Nano, value.Updated)
		return err == nil
	}
	return true
}

func validTask(value task) bool {
	if !validProviderID(value.ID) || !validETag(value.ETag) ||
		value.Parent != "" && !validProviderID(value.Parent) ||
		!validGoogleText(value.Title, 1024, true) ||
		!validGoogleText(value.Notes, 8192, true) ||
		len(value.Position) > 1024 || len(value.Links) > application.MaxTaskCollectionEntries {
		return false
	}
	if value.Updated == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value.Updated)
	return err == nil
}

func validGoogleText(value string, maximumRunes int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && utf8.RuneCountInString(value) <= maximumRunes
}

func validateGoogleWriteText(name, value string, maximumRunes int, allowEmpty bool) error {
	if !validGoogleText(value, maximumRunes, allowEmpty) {
		return fmt.Errorf("google task %s is malformed or exceeds %d characters", name, maximumRunes)
	}
	return nil
}

func validProviderID(value string) bool {
	return value != "" && len(value) <= 2048 && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/?#\r\n\x00")
}

func validETag(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\r\n\x00")
}

func validPageToken(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}

func validHTTPSLink(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || len(value) > 8192 {
		return errors.New("google task returned an unsafe source link")
	}
	return nil
}

func encodeID(prefix, value string) (string, error) {
	if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("google task identifier is malformed")
	}
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(value)), nil
}

func decodeID(prefix, value string) (string, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("identifier does not belong to the Google Tasks route")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || !validProviderID(string(decoded)) {
		return "", errors.New("google task identifier is malformed")
	}
	return string(decoded), nil
}

func encodeETag(value string) string {
	return "gtv1_" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeETag(value string) (string, error) {
	if !strings.HasPrefix(value, "gtv1_") {
		return "", errors.New("task version is not a Google Tasks ETag")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "gtv1_"))
	if err != nil || !validETag(string(decoded)) {
		return "", errors.New("google task ETag is malformed")
	}
	return string(decoded), nil
}

func tombstoneVersion(remote task) string {
	if validETag(remote.ETag) {
		return encodeETag(remote.ETag)
	}
	digest := sha256.Sum256([]byte(remote.ID + "\x00" + remote.Updated))
	return "gtd1_" + hex.EncodeToString(digest[:])
}

func taskCollection(listID string) string {
	return "tasks/v1/lists/" + url.PathEscape(listID) + "/tasks"
}

func taskResource(listID, taskID string) string {
	return taskCollection(listID) + "/" + url.PathEscape(taskID)
}

func decodeRoute(listValue, taskValue string) (string, string, error) {
	listID, err := decodeID("gtl1_", listValue)
	if err != nil {
		return "", "", err
	}
	taskID, err := decodeID("gtt1_", taskValue)
	if err != nil {
		return "", "", err
	}
	return listID, taskID, nil
}

func queryValues(values ...string) url.Values {
	result := make(url.Values, len(values)/2)
	for index := 0; index+1 < len(values); index += 2 {
		result.Set(values[index], values[index+1])
	}
	return result
}

func writeAssemblyError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: assemble Google Tasks write result: %w", application.ErrWriteOutcomeUnknown, err)
}
