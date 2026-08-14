package application

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxTaskPageSize          = 100
	MaxTaskPageOffset        = 10_000
	MaxTaskTitleBytes        = 4096
	MaxTaskNotesBytes        = 1 << 20
	MaxTaskListNameBytes     = 1024
	MaxTaskQueryBytes        = 1024
	MaxTaskCollectionEntries = 256
	MaxTaskReminderMinutes   = 5 * 365 * 24 * 60
	MaxTaskCursorBytes       = 8192
)

// TaskTemporalKind preserves provider time meaning instead of coercing every
// value to an absolute instant.
type TaskTemporalKind string

const (
	TaskTemporalDate     TaskTemporalKind = "date"
	TaskTemporalFloating TaskTemporalKind = "floating_datetime"
	TaskTemporalZoned    TaskTemporalKind = "zoned_datetime"
)

// TaskTemporal is a date-only, floating local time, or zoned instant.
type TaskTemporal struct {
	Kind     TaskTemporalKind `json:"kind"`
	Value    string           `json:"value"`
	TimeZone string           `json:"timeZone,omitempty"`
}

func (temporal TaskTemporal) Validate() error {
	switch temporal.Kind {
	case TaskTemporalDate:
		parsed, err := time.Parse(time.DateOnly, temporal.Value)
		if err != nil || parsed.Format(time.DateOnly) != temporal.Value || temporal.TimeZone != "" {
			return errors.New("task date must be YYYY-MM-DD without a time zone")
		}
	case TaskTemporalFloating:
		const layout = "2006-01-02T15:04:05"
		parsed, err := time.Parse(layout, temporal.Value)
		if err != nil || parsed.Format(layout) != temporal.Value || temporal.TimeZone != "" {
			return errors.New("floating task datetime must omit an offset and time zone")
		}
	case TaskTemporalZoned:
		instant, err := time.Parse(time.RFC3339, temporal.Value)
		if err != nil || len(temporal.TimeZone) > 128 || temporal.TimeZone == "" ||
			strings.TrimSpace(temporal.TimeZone) != temporal.TimeZone || strings.ContainsAny(temporal.TimeZone, "\r\n\x00") {
			return errors.New("zoned task datetime requires RFC3339 and an IANA time zone")
		}
		location, err := time.LoadLocation(temporal.TimeZone)
		if err != nil {
			return errors.New("zoned task datetime has an unknown IANA time zone")
		}
		_, suppliedOffset := instant.Zone()
		_, locationOffset := instant.In(location).Zone()
		if suppliedOffset != locationOffset {
			return errors.New("zoned task datetime offset does not match its IANA time zone")
		}
	default:
		return errors.New("task temporal kind is invalid")
	}
	return nil
}

type TaskStatus string

const (
	TaskStatusNeedsAction TaskStatus = "needs_action"
	TaskStatusInProgress  TaskStatus = "in_progress"
	TaskStatusCompleted   TaskStatus = "completed"
	TaskStatusCancelled   TaskStatus = "cancelled"
)

func (status TaskStatus) Validate() error {
	switch status {
	case TaskStatusNeedsAction, TaskStatusInProgress, TaskStatusCompleted, TaskStatusCancelled:
		return nil
	default:
		return errors.New("task status is invalid")
	}
}

type TaskPriority string

const (
	TaskPriorityNone   TaskPriority = "none"
	TaskPriorityLow    TaskPriority = "low"
	TaskPriorityNormal TaskPriority = "normal"
	TaskPriorityHigh   TaskPriority = "high"
	TaskPriorityUrgent TaskPriority = "urgent"
)

func (priority TaskPriority) Validate() error {
	switch priority {
	case TaskPriorityNone, TaskPriorityLow, TaskPriorityNormal, TaskPriorityHigh, TaskPriorityUrgent:
		return nil
	default:
		return errors.New("task priority is invalid")
	}
}

type TaskReminderKind string

const (
	TaskReminderAbsolute      TaskReminderKind = "absolute"
	TaskReminderRelativeDue   TaskReminderKind = "relative_due"
	TaskReminderRelativeStart TaskReminderKind = "relative_start"
)

type TaskReminder struct {
	Kind          TaskReminderKind `json:"kind"`
	At            *TaskTemporal    `json:"at,omitempty"`
	OffsetMinutes int              `json:"offsetMinutes,omitempty"`
}

func (reminder TaskReminder) Validate() error {
	switch reminder.Kind {
	case TaskReminderAbsolute:
		if reminder.At == nil || reminder.OffsetMinutes != 0 {
			return errors.New("absolute task reminder requires only an exact time")
		}
		if reminder.At.Kind == TaskTemporalDate {
			return errors.New("absolute task reminder requires a datetime")
		}
		return reminder.At.Validate()
	case TaskReminderRelativeDue, TaskReminderRelativeStart:
		if reminder.At != nil || reminder.OffsetMinutes < -MaxTaskReminderMinutes ||
			reminder.OffsetMinutes > MaxTaskReminderMinutes {
			return errors.New("relative task reminder offset is invalid")
		}
		return nil
	default:
		return errors.New("task reminder kind is invalid")
	}
}

type TaskRecurrenceFrequency string

const (
	TaskRecurrenceDaily    TaskRecurrenceFrequency = "daily"
	TaskRecurrenceWeekly   TaskRecurrenceFrequency = "weekly"
	TaskRecurrenceMonthly  TaskRecurrenceFrequency = "monthly"
	TaskRecurrenceYearly   TaskRecurrenceFrequency = "yearly"
	TaskRecurrenceProvider TaskRecurrenceFrequency = "provider"
)

type TaskRecurrence struct {
	Frequency    TaskRecurrenceFrequency `json:"frequency"`
	Interval     int                     `json:"interval"`
	DaysOfWeek   []string                `json:"daysOfWeek,omitempty"`
	Count        int                     `json:"count,omitempty"`
	Until        *TaskTemporal           `json:"until,omitempty"`
	ProviderRule string                  `json:"providerRule,omitempty"`
}

func (recurrence TaskRecurrence) Validate() error {
	switch recurrence.Frequency {
	case TaskRecurrenceDaily, TaskRecurrenceWeekly, TaskRecurrenceMonthly, TaskRecurrenceYearly:
		if recurrence.ProviderRule != "" {
			return errors.New("portable task recurrence cannot include a provider rule")
		}
	case TaskRecurrenceProvider:
		if err := validateTaskText("task provider recurrence", recurrence.ProviderRule, 4096, false, false); err != nil {
			return err
		}
		if recurrence.Interval != 0 || len(recurrence.DaysOfWeek) != 0 || recurrence.Count != 0 || recurrence.Until != nil {
			return errors.New("provider task recurrence cannot mix portable recurrence fields")
		}
		return nil
	default:
		return errors.New("task recurrence frequency is invalid")
	}
	if recurrence.Interval < 1 || recurrence.Interval > 999 || recurrence.Count < 0 || recurrence.Count > 9999 ||
		recurrence.Count > 0 && recurrence.Until != nil {
		return errors.New("task recurrence range is invalid")
	}
	if recurrence.Until != nil {
		if err := recurrence.Until.Validate(); err != nil {
			return err
		}
	}
	if len(recurrence.DaysOfWeek) > 7 {
		return errors.New("task recurrence has too many weekdays")
	}
	seen := make(map[string]bool, len(recurrence.DaysOfWeek))
	for _, day := range recurrence.DaysOfWeek {
		switch day {
		case "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
		default:
			return errors.New("task recurrence weekday is invalid")
		}
		if seen[day] {
			return errors.New("task recurrence weekday is duplicated")
		}
		seen[day] = true
	}
	return nil
}

type TaskAssignee struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Self        bool   `json:"self,omitempty"`
}

func (assignee TaskAssignee) Validate() error {
	if err := validateOpaqueValue("task assignee ID", assignee.ID); err != nil {
		return err
	}
	return validateTaskText("task assignee display name", assignee.DisplayName, 1024, true, true)
}

type TaskChecklistItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
	Order     string `json:"order,omitempty"`
}

func (item TaskChecklistItem) Validate() error {
	if err := TaskChecklistItemInput(item).validate(true); err != nil {
		return err
	}
	return nil
}

// TaskChecklistItemInput permits an omitted ID for a provider-created item.
// Returned checklist items always use TaskChecklistItem and require the
// provider's opaque identity.
type TaskChecklistItemInput struct {
	ID        string `json:"id,omitempty"`
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
	Order     string `json:"order,omitempty"`
}

func (item TaskChecklistItemInput) Validate() error {
	return item.validate(false)
}

func (item TaskChecklistItemInput) validate(requireID bool) error {
	if requireID || item.ID != "" {
		if err := validateOpaqueValue("task checklist item ID", item.ID); err != nil {
			return err
		}
	}
	if err := validateTaskText("task checklist item title", item.Title, MaxTaskTitleBytes, false, false); err != nil {
		return err
	}
	return validateTaskText("task checklist item order", item.Order, 1024, true, true)
}

type TaskAttachmentLink struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	ContentType string `json:"contentType,omitempty"`
}

func (attachment TaskAttachmentLink) Validate() error {
	if err := validateTaskText("task attachment name", attachment.Name, 1024, false, true); err != nil {
		return err
	}
	if err := validateTaskURL(attachment.URL); err != nil {
		return err
	}
	return validateTaskText("task attachment content type", attachment.ContentType, 255, true, true)
}

type TaskSourceKind string

const (
	TaskSourceMail     TaskSourceKind = "mail"
	TaskSourceCalendar TaskSourceKind = "calendar"
	TaskSourceTask     TaskSourceKind = "task"
	TaskSourceExternal TaskSourceKind = "external"
)

// TaskLinkedSource connects a task to its source without authorizing a copy,
// mirror, move, or cross-provider write.
type TaskLinkedSource struct {
	Kind     TaskSourceKind    `json:"kind"`
	Account  domain.AccountID  `json:"account"`
	Provider domain.ProviderID `json:"provider"`
	ObjectID string            `json:"objectId"`
	URL      string            `json:"url,omitempty"`
}

func (source TaskLinkedSource) Validate() error {
	switch source.Kind {
	case TaskSourceMail, TaskSourceCalendar, TaskSourceTask, TaskSourceExternal:
	default:
		return errors.New("task linked source kind is invalid")
	}
	if err := source.Account.ValidateOpaque(); err != nil {
		return fmt.Errorf("task linked source account: %w", err)
	}
	if err := source.Provider.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("task linked source object ID", source.ObjectID); err != nil {
		return err
	}
	if source.URL != "" {
		return validateTaskURL(source.URL)
	}
	return nil
}

type TaskSyncMode string

const (
	TaskSyncPolling      TaskSyncMode = "polling"
	TaskSyncDelta        TaskSyncMode = "delta"
	TaskSyncToken        TaskSyncMode = "sync_token"
	TaskSyncNotification TaskSyncMode = "notification"
	TaskSyncRemoteMCP    TaskSyncMode = "remote_mcp"
)

func (mode TaskSyncMode) Validate() error {
	switch mode {
	case TaskSyncPolling, TaskSyncDelta, TaskSyncToken, TaskSyncNotification, TaskSyncRemoteMCP:
		return nil
	default:
		return errors.New("task sync mode is invalid")
	}
}

type TaskCapabilities struct {
	Read                  bool           `json:"read"`
	CrossListRead         bool           `json:"crossListRead"`
	Search                bool           `json:"search"`
	Create                bool           `json:"create"`
	Update                bool           `json:"update"`
	Complete              bool           `json:"complete"`
	Reopen                bool           `json:"reopen"`
	Delete                bool           `json:"delete"`
	OptimisticConcurrency bool           `json:"optimisticConcurrency"`
	Reminders             bool           `json:"reminders"`
	Recurrence            bool           `json:"recurrence"`
	Subtasks              bool           `json:"subtasks"`
	Checklist             bool           `json:"checklist"`
	Assignments           bool           `json:"assignments"`
	Labels                bool           `json:"labels"`
	Attachments           bool           `json:"attachments"`
	LinkedSources         bool           `json:"linkedSources"`
	Ordering              bool           `json:"ordering"`
	DateOnly              bool           `json:"dateOnly"`
	FloatingDateTime      bool           `json:"floatingDateTime"`
	ZonedDateTime         bool           `json:"zonedDateTime"`
	SyncModes             []TaskSyncMode `json:"syncModes,omitempty"`
}

func (capabilities TaskCapabilities) Validate() error {
	if len(capabilities.SyncModes) > 5 {
		return errors.New("task capabilities have too many sync modes")
	}
	seen := make(map[TaskSyncMode]bool, len(capabilities.SyncModes))
	for _, mode := range capabilities.SyncModes {
		if err := mode.Validate(); err != nil {
			return err
		}
		if seen[mode] {
			return errors.New("task capabilities duplicate a sync mode")
		}
		seen[mode] = true
	}
	return nil
}

type TaskList struct {
	ID           string               `json:"id"`
	Version      string               `json:"version,omitempty"`
	DisplayName  string               `json:"displayName"`
	Editable     bool                 `json:"editable"`
	Default      bool                 `json:"default"`
	Capabilities TaskCapabilities     `json:"capabilities"`
	Degradations []domain.Degradation `json:"degradations,omitempty"`
	Provenance   domain.Provenance    `json:"provenance"`
}

func (list TaskList) Validate() error {
	if err := validateOpaqueValue("task list ID", list.ID); err != nil {
		return err
	}
	if list.Version != "" {
		if err := validateOpaqueValue("task list version", list.Version); err != nil {
			return err
		}
	}
	if err := validateTaskText("task list name", list.DisplayName, MaxTaskListNameBytes, false, false); err != nil {
		return err
	}
	if err := list.Capabilities.Validate(); err != nil {
		return err
	}
	if err := validateTaskDegradations(list.Degradations); err != nil {
		return err
	}
	if err := list.Provenance.Validate(); err != nil {
		return err
	}
	if list.Provenance.TaskListID != list.ID || list.Provenance.SourceObjectID != "" {
		return errors.New("task list provenance does not match its ID")
	}
	return nil
}

type Task struct {
	ID           string               `json:"id"`
	Version      string               `json:"version"`
	ListID       string               `json:"listId"`
	ParentID     string               `json:"parentId,omitempty"`
	Title        string               `json:"title"`
	Notes        string               `json:"notes,omitempty"`
	Status       TaskStatus           `json:"status"`
	Priority     TaskPriority         `json:"priority"`
	Start        *TaskTemporal        `json:"start,omitempty"`
	Due          *TaskTemporal        `json:"due,omitempty"`
	CompletedAt  *TaskTemporal        `json:"completedAt,omitempty"`
	Reminders    []TaskReminder       `json:"reminders,omitempty"`
	Recurrence   *TaskRecurrence      `json:"recurrence,omitempty"`
	Checklist    []TaskChecklistItem  `json:"checklist,omitempty"`
	Assignees    []TaskAssignee       `json:"assignees,omitempty"`
	Labels       []string             `json:"labels,omitempty"`
	Attachments  []TaskAttachmentLink `json:"attachments,omitempty"`
	Sources      []TaskLinkedSource   `json:"sources,omitempty"`
	Order        string               `json:"order,omitempty"`
	Capabilities TaskCapabilities     `json:"capabilities"`
	Degradations []domain.Degradation `json:"degradations,omitempty"`
	Provenance   domain.Provenance    `json:"provenance"`
}

func (task Task) Validate() error {
	for name, value := range map[string]string{
		"task ID": task.ID, "task version": task.Version, "task list ID": task.ListID,
	} {
		if err := validateOpaqueValue(name, value); err != nil {
			return err
		}
	}
	if task.ParentID != "" {
		if err := validateOpaqueValue("parent task ID", task.ParentID); err != nil {
			return err
		}
		if task.ParentID == task.ID {
			return errors.New("task cannot be its own parent")
		}
	}
	if err := validateTaskText("task title", task.Title, MaxTaskTitleBytes, false, false); err != nil {
		return err
	}
	if err := validateTaskText("task notes", task.Notes, MaxTaskNotesBytes, true, false); err != nil {
		return err
	}
	if err := task.Status.Validate(); err != nil {
		return err
	}
	if err := task.Priority.Validate(); err != nil {
		return err
	}
	for _, temporal := range []*TaskTemporal{task.Start, task.Due, task.CompletedAt} {
		if temporal != nil {
			if err := temporal.Validate(); err != nil {
				return err
			}
		}
	}
	if task.Status == TaskStatusCompleted && task.CompletedAt == nil &&
		!hasTaskDegradation(task.Degradations, "completion_time") {
		return errors.New("completed task requires a completion time")
	}
	if task.Status != TaskStatusCompleted && task.CompletedAt != nil {
		return errors.New("incomplete task cannot have a completion time")
	}
	if task.Recurrence != nil {
		if err := task.Recurrence.Validate(); err != nil {
			return err
		}
	}
	if err := validateTaskCollections(task); err != nil {
		return err
	}
	if err := task.Capabilities.Validate(); err != nil {
		return err
	}
	if err := validateTaskDegradations(task.Degradations); err != nil {
		return err
	}
	if err := task.Provenance.Validate(); err != nil {
		return err
	}
	if task.Provenance.TaskListID != task.ListID || task.Provenance.SourceObjectID != task.ID {
		return errors.New("task provenance does not match its identity")
	}
	return validateTaskText("task order", task.Order, 1024, true, true)
}

func validateTaskCollections(task Task) error {
	if err := validateTaskCollectionLengths(
		len(task.Reminders), len(task.Checklist), len(task.Assignees), len(task.Labels),
		len(task.Attachments), len(task.Sources),
	); err != nil {
		return err
	}
	for _, item := range task.Checklist {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return validateSharedTaskCollections(
		task.Reminders, task.Assignees, task.Labels, task.Attachments, task.Sources,
		task.Provenance, task.ID,
	)
}

func validateTaskMutationCollections(
	reminders []TaskReminder,
	checklist []TaskChecklistItemInput,
	assignees []TaskAssignee,
	labels []string,
	attachments []TaskAttachmentLink,
	sources []TaskLinkedSource,
) error {
	if err := validateTaskCollectionLengths(
		len(reminders), len(checklist), len(assignees), len(labels),
		len(attachments), len(sources),
	); err != nil {
		return err
	}
	for _, item := range checklist {
		if err := item.Validate(); err != nil {
			return err
		}
	}
	return validateSharedTaskCollections(
		reminders, assignees, labels, attachments, sources, domain.Provenance{}, "",
	)
}

func validateTaskCollectionLengths(lengths ...int) error {
	if slices.ContainsFunc(lengths, func(length int) bool { return length > MaxTaskCollectionEntries }) {
		return errors.New("task contains an oversized collection")
	}
	return nil
}

func validateSharedTaskCollections(
	reminders []TaskReminder,
	assignees []TaskAssignee,
	labels []string,
	attachments []TaskAttachmentLink,
	sources []TaskLinkedSource,
	provenance domain.Provenance,
	taskID string,
) error {
	for _, reminder := range reminders {
		if err := reminder.Validate(); err != nil {
			return err
		}
	}
	for _, assignee := range assignees {
		if err := assignee.Validate(); err != nil {
			return err
		}
	}
	if err := validateUniqueTaskText("task label", labels, 1024); err != nil {
		return err
	}
	for _, attachment := range attachments {
		if err := attachment.Validate(); err != nil {
			return err
		}
	}
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if taskID != "" && source.Kind == TaskSourceTask && source.Account == provenance.AccountID &&
			source.Provider == provenance.Provider && source.ObjectID == taskID {
			return errors.New("task source linkage forms a self-loop")
		}
	}
	return nil
}

type TaskListPage struct {
	Lists   []TaskList `json:"lists"`
	Offset  int        `json:"offset"`
	Limit   int        `json:"limit"`
	HasMore bool       `json:"hasMore"`
}

type TaskPage struct {
	Tasks   []Task `json:"tasks"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
	HasMore bool   `json:"hasMore"`
}

type TaskListInput struct {
	Account domain.AccountID `json:"account"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

func (input TaskListInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	return validateTaskPage(input.Offset, input.Limit)
}

type TaskReadInput struct {
	Account domain.AccountID `json:"account"`
	ListID  string           `json:"listId,omitempty"`
	Status  TaskStatus       `json:"status,omitempty"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

func (input TaskReadInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if input.ListID != "" {
		if err := validateOpaqueValue("task list ID", input.ListID); err != nil {
			return err
		}
	}
	if input.Status != "" {
		if err := input.Status.Validate(); err != nil {
			return err
		}
	}
	return validateTaskPage(input.Offset, input.Limit)
}

type TaskGetInput struct {
	Account domain.AccountID `json:"account"`
	ListID  string           `json:"listId"`
	TaskID  string           `json:"taskId"`
}

func (input TaskGetInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateOpaqueValue("task list ID", input.ListID); err != nil {
		return err
	}
	return validateOpaqueValue("task ID", input.TaskID)
}

type TaskSearchInput struct {
	Account domain.AccountID `json:"account"`
	ListID  string           `json:"listId,omitempty"`
	Query   string           `json:"query"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

func (input TaskSearchInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if input.ListID != "" {
		if err := validateOpaqueValue("task list ID", input.ListID); err != nil {
			return err
		}
	}
	if err := validateTaskText("task query", input.Query, MaxTaskQueryBytes, false, false); err != nil {
		return err
	}
	return validateTaskPage(input.Offset, input.Limit)
}

type TaskCursor struct {
	Provider domain.ProviderID `json:"provider"`
	Account  domain.AccountID  `json:"account"`
	ListID   string            `json:"listId"`
	Mode     TaskSyncMode      `json:"mode"`
	Value    string            `json:"value"`
}

func (cursor TaskCursor) Validate() error {
	if err := cursor.Provider.Validate(); err != nil {
		return err
	}
	if err := cursor.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := validateOpaqueValue("task cursor list ID", cursor.ListID); err != nil {
		return err
	}
	if err := cursor.Mode.Validate(); err != nil {
		return err
	}
	return validateTaskText("task cursor", cursor.Value, MaxTaskCursorBytes, false, true)
}

type TaskChangeKind string

const (
	TaskChangeUpsert TaskChangeKind = "upsert"
	TaskChangeDelete TaskChangeKind = "delete"
)

type TaskChange struct {
	Kind    TaskChangeKind `json:"kind"`
	Task    *Task          `json:"task,omitempty"`
	TaskID  string         `json:"taskId,omitempty"`
	Version string         `json:"version,omitempty"`
}

func (change TaskChange) Validate() error {
	switch change.Kind {
	case TaskChangeUpsert:
		if change.Task == nil || change.TaskID != "" || change.Version != "" {
			return errors.New("task upsert change is malformed")
		}
		return change.Task.Validate()
	case TaskChangeDelete:
		if change.Task != nil {
			return errors.New("task deletion change cannot contain task content")
		}
		if err := validateOpaqueValue("deleted task ID", change.TaskID); err != nil {
			return err
		}
		return validateOpaqueValue("deleted task version", change.Version)
	default:
		return errors.New("task change kind is invalid")
	}
}

type TaskChangePage struct {
	Changes []TaskChange `json:"changes"`
	Cursor  TaskCursor   `json:"cursor"`
	Reset   bool         `json:"reset"`
}

func validateTaskPage(offset, limit int) error {
	if offset < 0 || offset > MaxTaskPageOffset {
		return fmt.Errorf("task offset must be between 0 and %d", MaxTaskPageOffset)
	}
	if limit < 1 || limit > MaxTaskPageSize {
		return fmt.Errorf("task limit must be between 1 and %d", MaxTaskPageSize)
	}
	return nil
}

func validateTaskText(name, value string, maximum int, allowEmpty, singleLine bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if !utf8.ValidString(value) || len(value) > maximum || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s is malformed or too large", name)
	}
	if singleLine && strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be one line", name)
	}
	return nil
}

func validateUniqueTaskText(name string, values []string, maximum int) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if err := validateTaskText(name, value, maximum, false, true); err != nil {
			return err
		}
		folded := strings.ToLower(value)
		if seen[folded] {
			return fmt.Errorf("%s is duplicated", name)
		}
		seen[folded] = true
	}
	return nil
}

func validateTaskURL(raw string) error {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("task URL is malformed")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.User != nil {
		return errors.New("task URL is malformed")
	}
	switch parsed.Scheme {
	case "https":
		if parsed.Hostname() == "" {
			return errors.New("task HTTPS URL has no host")
		}
	case "things", "omnifocus":
	default:
		return errors.New("task URL scheme is unsupported")
	}
	return nil
}

func validateTaskDegradations(degradations []domain.Degradation) error {
	if len(degradations) > 64 {
		return errors.New("task degradations are unbounded")
	}
	for _, degradation := range degradations {
		if err := degradation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func hasTaskDegradation(degradations []domain.Degradation, feature string) bool {
	return slices.ContainsFunc(degradations, func(degradation domain.Degradation) bool {
		return degradation.Feature == feature && degradation.Lossy
	})
}
