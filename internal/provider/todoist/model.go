package todoist

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Inbox          bool   `json:"inbox_project"`
	Archived       bool   `json:"is_archived"`
	Deleted        bool   `json:"is_deleted"`
	CanAssignTasks bool   `json:"can_assign_tasks"`
}

type due struct {
	Date      string `json:"date"`
	TimeZone  string `json:"timezone"`
	Recurring bool   `json:"is_recurring"`
	String    string `json:"string"`
	Language  string `json:"lang"`
}

type deadline struct {
	Date     string `json:"date"`
	Language string `json:"lang"`
}

type duration struct {
	Amount int    `json:"amount"`
	Unit   string `json:"unit"`
}

type task struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	ProjectID      string    `json:"project_id"`
	SectionID      string    `json:"section_id"`
	ParentID       string    `json:"parent_id"`
	Content        string    `json:"content"`
	Description    string    `json:"description"`
	Priority       int       `json:"priority"`
	Due            *due      `json:"due"`
	Deadline       *deadline `json:"deadline"`
	ChildOrder     int       `json:"child_order"`
	Labels         []string  `json:"labels"`
	ResponsibleUID string    `json:"responsible_uid"`
	Checked        bool      `json:"checked"`
	Deleted        bool      `json:"is_deleted"`
	AddedAt        string    `json:"added_at"`
	UpdatedAt      string    `json:"updated_at"`
	CompletedAt    string    `json:"completed_at"`
	Duration       *duration `json:"duration"`
	NoteCount      int       `json:"note_count"`
}

type reminder struct {
	ID           string `json:"id"`
	ItemID       string `json:"item_id"`
	Type         string `json:"type"`
	MinuteOffset int    `json:"minute_offset"`
	Due          *due   `json:"due"`
	Deleted      bool   `json:"is_deleted"`
}

type page[T any] struct {
	Results    []T    `json:"results"`
	NextCursor string `json:"next_cursor"`
}

func (client *Client) taskView(
	listID string,
	remote task,
	reminders []reminder,
) (application.Task, error) {
	if !validTask(remote) || remote.ProjectID == "" || remote.Content == "" {
		return application.Task{}, errors.New("todoist returned an invalid task")
	}
	encodedID, err := encodeID("tdt1_", remote.ID)
	if err != nil {
		return application.Task{}, err
	}
	parentID := ""
	if remote.ParentID != "" {
		parentID, err = encodeID("tdt1_", remote.ParentID)
		if err != nil {
			return application.Task{}, err
		}
	}
	priority, err := readPriority(remote.Priority)
	if err != nil {
		return application.Task{}, err
	}
	start, err := readDue(remote.Due)
	if err != nil {
		return application.Task{}, fmt.Errorf("todoist task due: %w", err)
	}
	canonicalDue, err := readDeadline(remote.Deadline)
	if err != nil {
		return application.Task{}, fmt.Errorf("todoist task deadline: %w", err)
	}
	completed, err := readCompletion(remote.CompletedAt)
	if err != nil {
		return application.Task{}, err
	}
	status := application.TaskStatusNeedsAction
	if remote.Checked || completed != nil {
		status = application.TaskStatusCompleted
		if completed == nil {
			return application.Task{}, errors.New("todoist completed task omitted its completion time")
		}
	}
	canonicalReminders, reminderDegradations, err := readReminders(reminders)
	if err != nil {
		return application.Task{}, err
	}
	var recurrence *application.TaskRecurrence
	if remote.Due != nil && remote.Due.Recurring {
		rule, encodeErr := encodeRecurrence(*remote.Due)
		if encodeErr != nil {
			return application.Task{}, encodeErr
		}
		recurrence = &application.TaskRecurrence{
			Frequency:    application.TaskRecurrenceProvider,
			ProviderRule: rule,
		}
	}
	assignees := []application.TaskAssignee(nil)
	if remote.ResponsibleUID != "" {
		assigneeID, encodeErr := encodeID("tda1_", remote.ResponsibleUID)
		if encodeErr != nil {
			return application.Task{}, encodeErr
		}
		assignees = []application.TaskAssignee{{
			ID: assigneeID, Self: remote.ResponsibleUID == client.userID,
		}}
	}
	version, err := encodeVersion(remote)
	if err != nil {
		return application.Task{}, err
	}
	result := application.Task{
		ID: encodedID, Version: version, ListID: listID, ParentID: parentID,
		Title: remote.Content, Notes: remote.Description, Status: status,
		Priority: priority, Start: start, Due: canonicalDue,
		CompletedAt: completed, Reminders: canonicalReminders,
		Recurrence: recurrence, Assignees: assignees,
		Labels:       append([]string(nil), remote.Labels...),
		Order:        strconv.Itoa(remote.ChildOrder),
		Degradations: reminderDegradations,
	}
	if remote.SectionID != "" {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.sections",
			Reason:  "the Todoist section is not represented by the canonical task contract",
			Lossy:   true,
		})
	}
	if remote.Duration != nil {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.duration",
			Reason:  "Todoist duration is not represented by the canonical task contract",
			Lossy:   true,
		})
	}
	if remote.NoteCount > 0 {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.comments",
			Reason:  "Todoist comments are not represented by the canonical task contract",
			Lossy:   true,
		})
	}
	if recurrence != nil {
		result.Degradations = append(result.Degradations, domain.Degradation{
			Feature: "tasks.recurrence",
			Reason:  "Todoist recurrence is preserved as an exact provider rule",
		})
	}
	return result, nil
}

func validTask(value task) bool {
	if !validID(value.ID) || !validID(value.ProjectID) ||
		value.ParentID != "" && !validID(value.ParentID) ||
		value.ResponsibleUID != "" && !validID(value.ResponsibleUID) ||
		value.SectionID != "" && !validID(value.SectionID) ||
		value.ChildOrder < -2147483648 || value.ChildOrder > 2147483647 ||
		value.NoteCount < 0 {
		return false
	}
	for _, timestamp := range []string{value.AddedAt, value.UpdatedAt, value.CompletedAt} {
		if timestamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
				return false
			}
		}
	}
	return true
}

func validID(value string) bool {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "tmp-") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func encodeID(prefix, value string) (string, error) {
	if !validID(value) {
		return "", errors.New("todoist identifier is malformed")
	}
	return prefix + base64.RawURLEncoding.EncodeToString([]byte(value)), nil
}

func decodeID(prefix, value string) (string, error) {
	if !strings.HasPrefix(value, prefix) {
		return "", errors.New("identifier does not belong to the Todoist route")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || !validID(string(decoded)) {
		return "", errors.New("todoist identifier is malformed")
	}
	return string(decoded), nil
}

func readPriority(value int) (application.TaskPriority, error) {
	switch value {
	case 1:
		return application.TaskPriorityNone, nil
	case 2:
		return application.TaskPriorityNormal, nil
	case 3:
		return application.TaskPriorityHigh, nil
	case 4:
		return application.TaskPriorityUrgent, nil
	default:
		return "", errors.New("todoist returned an unknown task priority")
	}
}

func writePriority(value application.TaskPriority) (int, error) {
	switch value {
	case application.TaskPriorityNone:
		return 1, nil
	case application.TaskPriorityNormal:
		return 2, nil
	case application.TaskPriorityHigh:
		return 3, nil
	case application.TaskPriorityUrgent:
		return 4, nil
	case application.TaskPriorityLow:
		return 0, errors.New("todoist has no exact low-priority value")
	default:
		return 0, errors.New("task priority is invalid")
	}
}

func readDue(value *due) (*application.TaskTemporal, error) {
	if value == nil {
		return nil, nil
	}
	if value.Date == "" {
		return nil, errors.New("due date is empty")
	}
	if len(value.Date) == len(time.DateOnly) {
		result := &application.TaskTemporal{Kind: application.TaskTemporalDate, Value: value.Date}
		return result, result.Validate()
	}
	if value.TimeZone == "" {
		parsed, err := parseFloating(value.Date)
		if err != nil {
			return nil, err
		}
		return &application.TaskTemporal{
			Kind:  application.TaskTemporalFloating,
			Value: parsed.Format("2006-01-02T15:04:05"),
		}, nil
	}
	location, err := time.LoadLocation(value.TimeZone)
	if err != nil {
		return nil, errors.New("due date has an unknown IANA time zone")
	}
	instant, err := time.Parse(time.RFC3339Nano, value.Date)
	if err != nil {
		local, floatingErr := parseFloating(value.Date)
		if floatingErr != nil {
			return nil, errors.New("zoned due date is malformed")
		}
		instant = time.Date(
			local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(),
			local.Second(), local.Nanosecond(), location,
		)
	}
	local := instant.In(location)
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: local.Format(time.RFC3339), TimeZone: value.TimeZone,
	}, nil
}

func parseFloating(value string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999", "2006-01-02T15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("floating due date is malformed")
}

func readDeadline(value *deadline) (*application.TaskTemporal, error) {
	if value == nil {
		return nil, nil
	}
	result := &application.TaskTemporal{
		Kind: application.TaskTemporalDate, Value: value.Date,
	}
	return result, result.Validate()
}

func readCompletion(value string) (*application.TaskTemporal, error) {
	if value == "" {
		return nil, nil
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("todoist completion time is malformed")
	}
	return &application.TaskTemporal{
		Kind:  application.TaskTemporalZoned,
		Value: instant.UTC().Format(time.RFC3339), TimeZone: "UTC",
	}, nil
}

func readReminders(values []reminder) (
	[]application.TaskReminder,
	[]domain.Degradation,
	error,
) {
	result := make([]application.TaskReminder, 0, len(values))
	var degradations []domain.Degradation
	for _, value := range values {
		if value.Deleted {
			continue
		}
		if !validID(value.ID) || !validID(value.ItemID) {
			return nil, nil, errors.New("todoist returned an invalid reminder")
		}
		switch value.Type {
		case "relative":
			if value.MinuteOffset < 0 {
				return nil, nil, errors.New("todoist returned a malformed relative reminder")
			}
			result = append(result, application.TaskReminder{
				Kind:          application.TaskReminderRelativeStart,
				OffsetMinutes: -value.MinuteOffset,
			})
		case "absolute":
			at, err := readDue(value.Due)
			if err != nil || at == nil || at.Kind == application.TaskTemporalDate {
				return nil, nil, errors.New("todoist returned a malformed absolute reminder")
			}
			result = append(result, application.TaskReminder{
				Kind: application.TaskReminderAbsolute, At: at,
			})
		case "location":
			degradations = append(degradations, domain.Degradation{
				Feature: "tasks.location_reminders",
				Reason:  "location reminders are not represented by the canonical task contract",
				Lossy:   true,
			})
		default:
			return nil, nil, errors.New("todoist returned an unknown reminder type")
		}
	}
	return result, degradations, nil
}

type recurrenceEnvelope struct {
	Version  int    `json:"v"`
	String   string `json:"s"`
	Date     string `json:"d"`
	Language string `json:"l,omitempty"`
	TimeZone string `json:"z,omitempty"`
}

func encodeRecurrence(value due) (string, error) {
	if !value.Recurring || value.String == "" || len(value.String) > 2048 {
		return "", errors.New("todoist recurrence rule is malformed")
	}
	encoded, err := json.Marshal(recurrenceEnvelope{
		Version: 1, String: value.String, Date: value.Date, Language: value.Language,
		TimeZone: value.TimeZone,
	})
	if err != nil {
		return "", err
	}
	return "todoist1_" + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeRecurrence(value string) (recurrenceEnvelope, error) {
	if !strings.HasPrefix(value, "todoist1_") || len(value) > 4096 {
		return recurrenceEnvelope{}, errors.New("recurrence is not a Todoist provider rule")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "todoist1_"))
	if err != nil || len(raw) > 3072 {
		return recurrenceEnvelope{}, errors.New("todoist recurrence rule is malformed")
	}
	var envelope recurrenceEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return recurrenceEnvelope{}, errors.New("todoist recurrence rule is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		envelope.Version != 1 || envelope.String == "" || envelope.Date == "" ||
		len(envelope.String) > 2048 || len(envelope.Language) > 16 ||
		len(envelope.TimeZone) > 128 {
		return recurrenceEnvelope{}, errors.New("todoist recurrence rule is malformed")
	}
	if envelope.TimeZone != "" {
		if _, err := time.LoadLocation(envelope.TimeZone); err != nil {
			return recurrenceEnvelope{}, errors.New("todoist recurrence has an unknown time zone")
		}
	}
	return envelope, nil
}

type versionEnvelope struct {
	Version     int    `json:"v"`
	ID          string `json:"i"`
	ProjectID   string `json:"p"`
	UpdatedAt   string `json:"u"`
	Checked     bool   `json:"c,omitempty"`
	CompletedAt string `json:"a,omitempty"`
	Digest      string `json:"d"`
}

func encodeVersion(value task) (string, error) {
	if !validTask(value) {
		return "", errors.New("cannot version malformed Todoist task")
	}
	snapshot, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(snapshot)
	envelope := versionEnvelope{
		Version: 1, ID: value.ID, ProjectID: value.ProjectID,
		UpdatedAt: value.UpdatedAt, Checked: value.Checked,
		CompletedAt: value.CompletedAt, Digest: hex.EncodeToString(digest[:]),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return "tdv1_" + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeVersion(value string) (versionEnvelope, error) {
	if !strings.HasPrefix(value, "tdv1_") || len(value) > 2048 {
		return versionEnvelope{}, errors.New("task version is not a Todoist snapshot")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "tdv1_"))
	if err != nil || len(raw) > 1536 {
		return versionEnvelope{}, errors.New("todoist task version is malformed")
	}
	var envelope versionEnvelope
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return versionEnvelope{}, errors.New("todoist task version is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		envelope.Version != 1 || !validID(envelope.ID) ||
		!validID(envelope.ProjectID) || len(envelope.Digest) != sha256.Size*2 {
		return versionEnvelope{}, errors.New("todoist task version is malformed")
	}
	if _, err := hex.DecodeString(envelope.Digest); err != nil {
		return versionEnvelope{}, errors.New("todoist task version is malformed")
	}
	return envelope, nil
}

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}
