package caldav

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	webcaldav "github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	fixtureTaskListPath = "/user/calendars/tasks/"
	fixtureTaskPath     = "/user/calendars/tasks/task.ics"
)

type taskFixtureBackend struct {
	*fixtureBackend
	capabilityStatus int
	components       []string
}

func newTaskFixtureBackend() *taskFixtureBackend {
	base := newFixtureBackend()
	base.objects = map[string]webcaldav.CalendarObject{
		fixtureTaskPath: {
			Path: fixtureTaskPath, ETag: "v1",
			Data: fixtureTaskCalendar("fixture-task"),
		},
	}
	return &taskFixtureBackend{
		fixtureBackend: base,
		components:     []string{ical.CompToDo},
	}
}

func (backend *taskFixtureBackend) ListCalendars(context.Context) ([]webcaldav.Calendar, error) {
	return []webcaldav.Calendar{{
		Path: fixtureTaskListPath, Name: "Synthetic tasks",
		SupportedComponentSet: append([]string(nil), backend.components...),
	}}, nil
}

func (backend *taskFixtureBackend) GetCalendar(
	_ context.Context,
	calendarPath string,
) (*webcaldav.Calendar, error) {
	if calendarPath != fixtureTaskListPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("task list not found"))
	}
	return &webcaldav.Calendar{
		Path: fixtureTaskListPath, Name: "Synthetic tasks",
		SupportedComponentSet: append([]string(nil), backend.components...),
	}, nil
}

func fixtureTaskCalendar(uid string) *ical.Calendar {
	task := ical.NewComponent(ical.CompToDo)
	task.Props.SetText(ical.PropUID, uid)
	task.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	task.Props.SetText(ical.PropSummary, "Synthetic task")
	task.Props.SetText(ical.PropDescription, "Bounded notes")
	task.Props.SetText(ical.PropStatus, "IN-PROCESS")
	priority := ical.NewProp(ical.PropPriority)
	priority.SetValueType(ical.ValueInt)
	priority.Value = "3"
	task.Props.Set(priority)
	percent := ical.NewProp(ical.PropPercentComplete)
	percent.SetValueType(ical.ValueInt)
	percent.Value = "50"
	task.Props.Set(percent)
	start := ical.NewProp(ical.PropDateTimeStart)
	start.SetValueType(ical.ValueDateTime)
	start.Value = "20260814T090000"
	task.Props.Set(start)
	due := ical.NewProp(ical.PropDue)
	due.SetValueType(ical.ValueDateTime)
	due.Value = "20260815T090000"
	task.Props.Set(due)
	recurrence := ical.NewProp(ical.PropRecurrenceRule)
	recurrence.SetValueType(ical.ValueRecurrence)
	recurrence.Value = "FREQ=WEEKLY;INTERVAL=2;BYDAY=FR"
	task.Props.Set(recurrence)
	parent := ical.NewProp(ical.PropRelatedTo)
	parent.SetText("parent-task")
	parent.Params.Set(ical.ParamRelationshipType, "PARENT")
	task.Props.Set(parent)
	categories := ical.NewProp(ical.PropCategories)
	categories.SetTextList([]string{"work", "review"})
	task.Props.Set(categories)
	extension := ical.NewProp("X-SYNTHETIC-STATE")
	extension.SetText("preserve-me")
	task.Props.Set(extension)
	alarm := ical.NewComponent(ical.CompAlarm)
	alarm.Props.SetText(ical.PropAction, "DISPLAY")
	alarm.Props.SetText(ical.PropDescription, "Synthetic reminder")
	trigger := ical.NewProp(ical.PropTrigger)
	trigger.SetDuration(-30 * time.Minute)
	trigger.Params.Set(ical.ParamRelated, "END")
	alarm.Props.Set(trigger)
	task.Children = append(task.Children, alarm)
	providerAlarm := ical.NewComponent(ical.CompAlarm)
	providerAlarm.Props.SetText(ical.PropAction, "EMAIL")
	providerAlarm.Props.SetText(ical.PropDescription, "Provider-managed reminder")
	providerTrigger := ical.NewProp(ical.PropTrigger)
	providerTrigger.SetDuration(-time.Hour)
	providerAlarm.Props.Set(providerTrigger)
	task.Children = append(task.Children, providerAlarm)
	return newTaskCalendar(task)
}

func newTaskFixtureServer(
	t *testing.T,
) (*httptest.Server, *taskFixtureBackend, *http.Client) {
	return newTaskFixtureServerWithCapabilityStatus(t, http.StatusMultiStatus)
}

func newTaskFixtureServerWithCapabilityStatus(
	t *testing.T,
	capabilityStatus int,
) (*httptest.Server, *taskFixtureBackend, *http.Client) {
	t.Helper()
	backend := newTaskFixtureBackend()
	backend.capabilityStatus = capabilityStatus
	handler := &webcaldav.Handler{Backend: backend}
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "reader@example.invalid" || password != "synthetic-secret" {
			http.Error(writer, "authentication required", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			http.Error(writer, "fixture read failed", http.StatusInternalServerError)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if request.Method == "PROPFIND" && request.URL.Path == "/user/calendars/" &&
			bytes.Contains(body, []byte("current-user-privilege-set")) {
			if backend.capabilityStatus != http.StatusMultiStatus {
				writer.WriteHeader(backend.capabilityStatus)
				return
			}
			writeTaskCapabilityFixture(writer)
			return
		}
		if request.Method == "REPORT" && request.URL.Path == fixtureTaskListPath &&
			bytes.Contains(body, []byte("sync-collection")) {
			writeTaskSyncFixture(t, writer, backend, string(body))
			return
		}
		if request.Method == http.MethodDelete {
			backend.mu.Lock()
			object := backend.objects[request.URL.Path]
			backend.deleteMatches = append(backend.deleteMatches, request.Header.Get("If-Match"))
			backend.mu.Unlock()
			if request.Header.Get("If-Match") != `"`+object.ETag+`"` {
				http.Error(writer, "ETag changed", http.StatusPreconditionFailed)
				return
			}
		}
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	return server, backend, &http.Client{Transport: transport}
}

func TestCalDAVVTODOUnsupportedCapabilityProbeDegradesToReadOnly(t *testing.T) {
	t.Parallel()
	server, _, httpClient := newTaskFixtureServerWithCapabilityStatus(
		t, http.StatusMethodNotAllowed,
	)
	client, err := NewTasks(t.Context(), TaskOptions{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	capabilities := client.TaskCapabilities()
	if !capabilities.Read || capabilities.Create || capabilities.Update ||
		len(capabilities.SyncModes) != 0 ||
		!hasTaskDegradation(client.TaskDegradations(), "tasks.write") ||
		!hasTaskDegradation(client.TaskDegradations(), "tasks.sync") {
		t.Fatalf("read-only task capabilities = %+v, degradations = %+v", capabilities, client.TaskDegradations())
	}
}

func writeTaskCapabilityFixture(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(writer, `<?xml version="1.0" encoding="utf-8"?>`+
		`<D:multistatus xmlns:D="DAV:"><D:response>`+
		`<D:href>`+fixtureTaskListPath+`</D:href><D:propstat><D:prop>`+
		`<D:current-user-privilege-set><D:privilege><D:write-content/>`+
		`</D:privilege></D:current-user-privilege-set>`+
		`<D:supported-report-set><D:supported-report><D:report>`+
		`<D:sync-collection/></D:report></D:supported-report></D:supported-report-set>`+
		`<D:sync-token>https://sync.example.invalid/1</D:sync-token>`+
		`</D:prop><D:status>HTTP/1.1 200 OK</D:status>`+
		`</D:propstat></D:response></D:multistatus>`)
}

func writeTaskSyncFixture(
	t *testing.T,
	writer http.ResponseWriter,
	backend *taskFixtureBackend,
	body string,
) {
	t.Helper()
	if strings.Contains(body, "expired-token") {
		writer.Header().Set("Content-Type", "application/xml")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, `<D:error xmlns:D="DAV:"><D:valid-sync-token/></D:error>`)
		return
	}
	backend.mu.Lock()
	object, exists := backend.objects[fixtureTaskPath]
	backend.mu.Unlock()
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(http.StatusMultiStatus)
	if strings.Contains(body, "https://sync.example.invalid/1") {
		_, _ = io.WriteString(writer, `<?xml version="1.0" encoding="utf-8"?>`+
			`<D:multistatus xmlns:D="DAV:"><D:response><D:href>`+
			fixtureTaskPath+`</D:href><D:status>HTTP/1.1 404 Not Found</D:status>`+
			`</D:response><D:sync-token>https://sync.example.invalid/2</D:sync-token>`+
			`</D:multistatus>`)
		return
	}
	if !exists {
		t.Fatal("sync fixture object is missing")
	}
	var encoded bytes.Buffer
	if err := ical.NewEncoder(&encoded).Encode(object.Data); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(writer, `<?xml version="1.0" encoding="utf-8"?>`+
		`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`+
		`<D:response><D:href>`+fixtureTaskPath+`</D:href><D:propstat><D:prop>`+
		`<D:getetag>&quot;`+object.ETag+`&quot;</D:getetag><C:calendar-data>`+
		xmlEscape(encoded.String())+`</C:calendar-data></D:prop>`+
		`<D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`+
		`<D:sync-token>https://sync.example.invalid/1</D:sync-token>`+
		`</D:multistatus>`)
}

func newTaskFixtureClient(
	t *testing.T,
) (*Client, *taskFixtureBackend) {
	t.Helper()
	server, backend, httpClient := newTaskFixtureServer(t)
	client, err := NewTasks(t.Context(), TaskOptions{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, backend
}

func TestCalDAVVTODOReadWriteAndExtensionRoundTrip(t *testing.T) {
	t.Parallel()
	client, backend := newTaskFixtureClient(t)
	capabilities := client.TaskCapabilities()
	if !capabilities.Read || !capabilities.Create || !capabilities.Update ||
		!capabilities.OptimisticConcurrency ||
		!slicesEqual(capabilities.SyncModes, []application.TaskSyncMode{application.TaskSyncToken}) {
		t.Fatalf("TaskCapabilities() = %#v", capabilities)
	}
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{Limit: 10})
	if err != nil || len(lists.Lists) != 1 || !lists.Lists[0].Default ||
		!lists.Lists[0].Editable {
		t.Fatalf("ListTaskLists() = %#v, %v", lists, err)
	}
	listID := lists.Lists[0].ID
	page, err := client.ListTasks(t.Context(), application.TaskReadInput{
		ListID: listID, Limit: 10,
	})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("ListTasks() = %#v, %v", page, err)
	}
	task := page.Tasks[0]
	if task.Title != "Synthetic task" || task.Status != application.TaskStatusInProgress ||
		task.Priority != application.TaskPriorityHigh ||
		task.Start == nil || task.Start.Kind != application.TaskTemporalFloating ||
		task.Due == nil || task.Due.Kind != application.TaskTemporalFloating ||
		task.Recurrence == nil || task.Recurrence.ProviderRule == "" ||
		len(task.Reminders) != 1 || task.Reminders[0].Kind != application.TaskReminderRelativeDue ||
		len(task.Labels) != 2 || task.ParentID == "" ||
		!hasTaskDegradation(task.Degradations, "tasks.reminders") ||
		!hasTaskDegradation(task.Degradations, "tasks.percent_complete") ||
		!hasTaskDegradation(task.Degradations, "tasks.provider_extensions") {
		t.Fatalf("task projection = %#v", task)
	}

	updatedTitle := "Updated without losing extensions"
	updated, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		ListID: listID, TaskID: task.ID, Version: task.Version,
		Title: &updatedTitle,
	})
	if err != nil || updated.Title != updatedTitle || updated.Version == task.Version {
		t.Fatalf("UpdateTask() = %#v, %v", updated, err)
	}
	updated, err = client.UpdateTask(t.Context(), application.TaskUpdateInput{
		ListID: updated.ListID, TaskID: updated.ID, Version: updated.Version,
		ReplaceReminders: true,
		Reminders: []application.TaskReminder{{
			Kind: application.TaskReminderRelativeStart, OffsetMinutes: -30,
		}},
	})
	if err != nil || len(updated.Reminders) != 1 ||
		updated.Reminders[0].Kind != application.TaskReminderRelativeStart ||
		updated.Reminders[0].OffsetMinutes != -30 {
		t.Fatalf("UpdateTask(reminders) = %#v, %v", updated, err)
	}
	backend.mu.Lock()
	stored := backend.objects[fixtureTaskPath]
	putConditions := append([]string(nil), backend.putConditions...)
	backend.mu.Unlock()
	master, err := taskMaster(stored.Data, "fixture-task")
	if err != nil {
		t.Fatal(err)
	}
	extension, err := master.Props.Text("X-SYNTHETIC-STATE")
	if err != nil || extension != "preserve-me" {
		t.Fatalf("preserved extension = %q, %v", extension, err)
	}
	providerAlarmPreserved := false
	for _, child := range master.Children {
		action, _ := child.Props.Text(ical.PropAction)
		providerAlarmPreserved = providerAlarmPreserved ||
			child.Name == ical.CompAlarm && action == "EMAIL"
	}
	if !providerAlarmPreserved {
		t.Fatal("provider-managed VTODO alarm was not preserved")
	}
	if len(putConditions) != 2 || putConditions[0] != `|"v1"` ||
		putConditions[1] != `|"v2"` {
		t.Fatalf("PUT conditions = %#v", putConditions)
	}
	if _, err := client.UpdateTask(t.Context(), application.TaskUpdateInput{
		ListID: listID, TaskID: task.ID, Version: "stale", Title: &updatedTitle,
	}); err == nil || !strings.Contains(err.Error(), "changed before write") {
		t.Fatalf("stale UpdateTask() error = %v", err)
	}

	completed, err := client.CompleteTask(t.Context(), application.TaskStateInput{
		ListID: listID, TaskID: updated.ID, Version: updated.Version,
	})
	if err != nil || completed.Status != application.TaskStatusCompleted || completed.CompletedAt == nil {
		t.Fatalf("CompleteTask() = %#v, %v", completed, err)
	}
	reopened, err := client.ReopenTask(t.Context(), application.TaskStateInput{
		ListID: listID, TaskID: completed.ID, Version: completed.Version,
	})
	if err != nil || reopened.Status != application.TaskStatusNeedsAction || reopened.CompletedAt != nil {
		t.Fatalf("ReopenTask() = %#v, %v", reopened, err)
	}

	created, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		ListID: listID, Title: "Created task", Priority: application.TaskPriorityNormal,
		ParentID: task.ID,
		Start: &application.TaskTemporal{
			Kind: application.TaskTemporalZoned, Value: "2026-08-20T08:00:00+01:00",
			TimeZone: "Europe/London",
		},
		Due: &application.TaskTemporal{
			Kind: application.TaskTemporalZoned, Value: "2026-08-20T10:00:00+01:00",
			TimeZone: "Europe/London",
		},
		Recurrence: &application.TaskRecurrence{
			Frequency: application.TaskRecurrenceWeekly, Interval: 2,
			DaysOfWeek: []string{"thursday"}, Count: 3,
		},
		Reminders: []application.TaskReminder{
			{Kind: application.TaskReminderRelativeStart, OffsetMinutes: -15},
			{
				Kind: application.TaskReminderAbsolute,
				At: &application.TaskTemporal{
					Kind: application.TaskTemporalZoned, Value: "2026-08-20T07:30:00+01:00",
					TimeZone: "Europe/London",
				},
			},
		},
	})
	if err != nil || created.ID == "" || created.Version == "" ||
		created.Start == nil || created.Start.Kind != application.TaskTemporalZoned ||
		created.Start.TimeZone != "Europe/London" ||
		created.Due == nil || created.Due.Kind != application.TaskTemporalZoned ||
		created.Recurrence == nil ||
		created.Recurrence.Frequency != application.TaskRecurrenceProvider ||
		created.Recurrence.ProviderRule != "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH;COUNT=3" ||
		created.ParentID == "" ||
		len(created.Reminders) != 2 ||
		created.Reminders[0].Kind != application.TaskReminderRelativeStart ||
		created.Reminders[1].Kind != application.TaskReminderAbsolute ||
		created.Reminders[1].At == nil ||
		created.Reminders[1].At.Value != "2026-08-20T06:30:00Z" ||
		created.Reminders[1].At.TimeZone != "UTC" {
		t.Fatalf("CreateTask() = %#v, %v", created, err)
	}
	createdReference, err := decodeTaskID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	createdObject := backend.objects[createdReference.Path]
	backend.mu.Unlock()
	createdMaster, err := taskMaster(createdObject.Data, "")
	if err != nil {
		t.Fatal(err)
	}
	absoluteUTC := false
	for _, child := range createdMaster.Children {
		trigger := child.Props.Get(ical.PropTrigger)
		absoluteUTC = absoluteUTC || trigger != nil &&
			trigger.ValueType() == ical.ValueDateTime &&
			trigger.Value == "20260820T063000Z" &&
			trigger.Params.Get(ical.ParamTimezoneID) == ""
	}
	if !absoluteUTC {
		t.Fatal("absolute VTODO alarm was not emitted as UTC")
	}
	parent, err := client.GetTask(t.Context(), application.TaskGetInput{
		ListID: listID, TaskID: created.ParentID,
	})
	if err != nil || parent.ID != task.ID || parent.Title != updatedTitle {
		t.Fatalf("GetTask(created parent) = %#v, %v", parent, err)
	}
	dateOnly, err := client.CreateTask(t.Context(), application.TaskCreateInput{
		ListID: listID, Title: "Date-only task", Priority: application.TaskPriorityNone,
		Start: &application.TaskTemporal{
			Kind: application.TaskTemporalDate, Value: "2026-08-21",
		},
		Due: &application.TaskTemporal{
			Kind: application.TaskTemporalDate, Value: "2026-08-22",
		},
	})
	if err != nil || dateOnly.Start == nil || dateOnly.Due == nil ||
		dateOnly.Start.Kind != application.TaskTemporalDate ||
		dateOnly.Due.Kind != application.TaskTemporalDate {
		t.Fatalf("CreateTask(date-only) = %#v, %v", dateOnly, err)
	}
	if err := client.DeleteTask(t.Context(), application.TaskDeleteInput{
		ListID: listID, TaskID: dateOnly.ID, Version: dateOnly.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteTask(t.Context(), application.TaskDeleteInput{
		ListID: listID, TaskID: created.ID, Version: created.Version,
	}); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	lastDelete := backend.deleteMatches[len(backend.deleteMatches)-1]
	backend.mu.Unlock()
	if lastDelete != `"`+created.Version+`"` {
		t.Fatalf("DELETE If-Match = %q", lastDelete)
	}
}

func TestCalDAVVTODORejectsNonPortableWritesBeforeProviderAccess(t *testing.T) {
	t.Parallel()
	client, backend := newTaskFixtureClient(t)
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	listID := lists.Lists[0].ID
	tests := []struct {
		name  string
		input application.TaskCreateInput
		want  string
	}{
		{
			name: "mixed temporal kinds",
			input: application.TaskCreateInput{
				ListID: listID, Title: "mixed", Priority: application.TaskPriorityNone,
				Start: &application.TaskTemporal{
					Kind: application.TaskTemporalDate, Value: "2026-08-20",
				},
				Due: &application.TaskTemporal{
					Kind: application.TaskTemporalZoned, Value: "2026-08-21T09:00:00Z",
					TimeZone: "UTC",
				},
			},
			want: "same temporal kind",
		},
		{
			name: "due before start",
			input: application.TaskCreateInput{
				ListID: listID, Title: "backwards", Priority: application.TaskPriorityNone,
				Start: &application.TaskTemporal{
					Kind: application.TaskTemporalDate, Value: "2026-08-22",
				},
				Due: &application.TaskTemporal{
					Kind: application.TaskTemporalDate, Value: "2026-08-21",
				},
			},
			want: "later than DTSTART",
		},
		{
			name: "recurrence without start",
			input: application.TaskCreateInput{
				ListID: listID, Title: "recurring", Priority: application.TaskPriorityNone,
				Recurrence: &application.TaskRecurrence{
					Frequency: application.TaskRecurrenceDaily, Interval: 1,
				},
			},
			want: "requires DTSTART",
		},
		{
			name: "floating absolute alarm",
			input: application.TaskCreateInput{
				ListID: listID, Title: "alarm", Priority: application.TaskPriorityNone,
				Reminders: []application.TaskReminder{{
					Kind: application.TaskReminderAbsolute,
					At: &application.TaskTemporal{
						Kind:  application.TaskTemporalFloating,
						Value: "2026-08-21T09:00:00",
					},
				}},
			},
			want: "requires a zoned datetime",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.CreateTask(t.Context(), test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CreateTask() error = %v", err)
			}
		})
	}
	backend.mu.Lock()
	putConditions := append([]string(nil), backend.putConditions...)
	backend.mu.Unlock()
	if len(putConditions) != 0 {
		t.Fatalf("invalid writes reached CalDAV: %#v", putConditions)
	}
}

func TestCalDAVVTODOSyncTokenAndReset(t *testing.T) {
	t.Parallel()
	client, _ := newTaskFixtureClient(t)
	lists, err := client.ListTaskLists(t.Context(), application.TaskListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	listID := lists.Lists[0].ID
	initial, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: "acc_00000000000000000000000000000001",
		ListID:  listID, Limit: 10,
	})
	if err != nil || initial.Reset || len(initial.Changes) != 1 ||
		initial.Changes[0].Kind != application.TaskChangeUpsert ||
		initial.Cursor.Value != "https://sync.example.invalid/1" {
		t.Fatalf("initial SyncTasks() = %#v, %v", initial, err)
	}
	deleted, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: "acc_00000000000000000000000000000001",
		ListID:  listID, Limit: 10, Cursor: &initial.Cursor,
	})
	if err != nil || len(deleted.Changes) != 1 ||
		deleted.Changes[0].Kind != application.TaskChangeDelete ||
		deleted.Changes[0].TaskID != initial.Changes[0].Task.ID ||
		deleted.Cursor.Value != "https://sync.example.invalid/2" {
		t.Fatalf("incremental SyncTasks() = %#v, %v", deleted, err)
	}
	expired := initial.Cursor
	expired.Value = "expired-token"
	reset, err := client.SyncTasks(t.Context(), application.TaskSyncInput{
		Account: expired.Account, ListID: listID, Limit: 10, Cursor: &expired,
	})
	if err != nil || !reset.Reset || len(reset.Changes) != 1 ||
		reset.Changes[0].Kind != application.TaskChangeUpsert {
		t.Fatalf("reset SyncTasks() = %#v, %v", reset, err)
	}
}

func TestCalDAVVTODOOnlyDoesNotSatisfyCalendarDiscovery(t *testing.T) {
	t.Parallel()
	server, _, httpClient := newTaskFixtureServer(t)
	_, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err == nil || !strings.Contains(err.Error(), "no VEVENT collection") {
		t.Fatalf("New(VTODO-only) error = %v", err)
	}
}

func TestCalDAVMixedCollectionKeepsEventAndTaskRoutesDistinct(t *testing.T) {
	t.Parallel()
	server, backend, httpClient := newTaskFixtureServer(t)
	backend.components = []string{ical.CompEvent, ical.CompToDo}
	events, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	tasks, err := NewTasks(t.Context(), TaskOptions{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tasks.Close() })
	if len(events.calendars) != 1 || len(events.taskLists) != 0 ||
		len(tasks.calendars) != 0 || len(tasks.taskLists) != 1 ||
		events.calendarPath != fixtureTaskListPath ||
		tasks.taskListPath != fixtureTaskListPath {
		t.Fatalf("mixed routes: events=%+v tasks=%+v", events, tasks)
	}
}

func hasTaskDegradation(values []domain.Degradation, feature string) bool {
	for _, value := range values {
		if value.Feature == feature {
			return true
		}
	}
	return false
}

func slicesEqual[T comparable](left, right []T) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
