package caldav

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	webcaldav "github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	fixtureCalendarPath = "/user/calendars/main/"
	fixtureObjectPath   = "/user/calendars/main/event.ics"
)

type fixtureBackend struct {
	mu              sync.Mutex
	objects         map[string]webcaldav.CalendarObject
	version         int
	putConditions   []string
	deleteMatches   []string
	scheduleMatches []string
}

func newFixtureBackend() *fixtureBackend {
	return &fixtureBackend{
		objects: map[string]webcaldav.CalendarObject{
			fixtureObjectPath: {
				Path: fixtureObjectPath, ETag: "v1",
				Data: fixtureCalendar("fixture-event", nil),
			},
		},
		version: 1,
	}
}

func (*fixtureBackend) CurrentUserPrincipal(context.Context) (string, error) {
	return "/user/", nil
}

func (*fixtureBackend) CalendarHomeSetPath(context.Context) (string, error) {
	return "/user/calendars/", nil
}

func (*fixtureBackend) CreateCalendar(context.Context, *webcaldav.Calendar) error {
	return errors.New("calendar creation is unavailable in the fixture")
}

func (*fixtureBackend) ListCalendars(context.Context) ([]webcaldav.Calendar, error) {
	return []webcaldav.Calendar{{
		Path: fixtureCalendarPath, Name: "Synthetic",
		SupportedComponentSet: []string{ical.CompEvent},
	}}, nil
}

func (*fixtureBackend) GetCalendar(
	_ context.Context,
	calendarPath string,
) (*webcaldav.Calendar, error) {
	if calendarPath != fixtureCalendarPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("calendar not found"))
	}
	return &webcaldav.Calendar{
		Path: calendarPath, Name: "Synthetic",
		SupportedComponentSet: []string{ical.CompEvent},
	}, nil
}

func (backend *fixtureBackend) GetCalendarObject(
	_ context.Context,
	objectPath string,
	_ *webcaldav.CalendarCompRequest,
) (*webcaldav.CalendarObject, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	object, exists := backend.objects[objectPath]
	if !exists {
		return nil, webdav.NewHTTPError(http.StatusNotFound, errors.New("object not found"))
	}
	copy := object
	return &copy, nil
}

func (backend *fixtureBackend) ListCalendarObjects(
	_ context.Context,
	calendarPath string,
	_ *webcaldav.CalendarCompRequest,
) ([]webcaldav.CalendarObject, error) {
	return backend.objectsFor(calendarPath), nil
}

func (backend *fixtureBackend) QueryCalendarObjects(
	_ context.Context,
	calendarPath string,
	_ *webcaldav.CalendarQuery,
) ([]webcaldav.CalendarObject, error) {
	return backend.objectsFor(calendarPath), nil
}

func (backend *fixtureBackend) objectsFor(
	calendarPath string,
) []webcaldav.CalendarObject {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	objects := make([]webcaldav.CalendarObject, 0, len(backend.objects))
	for _, object := range backend.objects {
		if pathWithin(object.Path, calendarPath) {
			objects = append(objects, object)
		}
	}
	return objects
}

func (backend *fixtureBackend) PutCalendarObject(
	_ context.Context,
	objectPath string,
	calendar *ical.Calendar,
	options *webcaldav.PutCalendarObjectOptions,
) (*webcaldav.CalendarObject, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	existing, exists := backend.objects[objectPath]
	backend.putConditions = append(
		backend.putConditions,
		string(options.IfNoneMatch)+"|"+string(options.IfMatch),
	)
	if options.IfNoneMatch.IsWildcard() && exists {
		return nil, webdav.NewHTTPError(
			http.StatusPreconditionFailed,
			errors.New("object already exists"),
		)
	}
	if options.IfMatch.IsSet() {
		matches, err := options.IfMatch.MatchETag(existing.ETag)
		if err != nil || !exists || !matches {
			return nil, webdav.NewHTTPError(
				http.StatusPreconditionFailed,
				errors.New("ETag changed"),
			)
		}
	}
	backend.version++
	object := webcaldav.CalendarObject{
		Path: objectPath, ETag: fmt.Sprintf("v%d", backend.version),
		Data: calendar,
	}
	backend.objects[objectPath] = object
	copy := object
	return &copy, nil
}

func (backend *fixtureBackend) DeleteCalendarObject(
	_ context.Context,
	objectPath string,
) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if _, exists := backend.objects[objectPath]; !exists {
		return webdav.NewHTTPError(http.StatusNotFound, errors.New("object not found"))
	}
	delete(backend.objects, objectPath)
	return nil
}

func fixtureCalendar(uid string, attendees []string) *ical.Calendar {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetText(ical.PropSummary, "Synthetic event")
	event.Props.SetDateTime(
		ical.PropDateTimeStart,
		time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC),
	)
	event.Props.SetDateTime(
		ical.PropDateTimeEnd,
		time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC),
	)
	event.Props.Set(calendarAddressProp(
		ical.PropOrganizer,
		"reader@example.invalid",
		"",
		"Reader",
	))
	setAttendees(event, attendees, nil)
	calendar := ical.NewCalendar()
	calendar.Props.SetText(ical.PropProductID, "-//Corresync Test//EN")
	calendar.Props.SetText(ical.PropVersion, "2.0")
	calendar.Children = append(calendar.Children, event.Component)
	return calendar
}

func fixtureRecurringEvent(uid string) ical.Event {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetText(ical.PropSummary, "Weekly synthetic event")
	event.Props.SetDateTime(
		ical.PropDateTimeStart,
		time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC),
	)
	event.Props.SetDateTime(
		ical.PropDateTimeEnd,
		time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC),
	)
	rule := ical.NewProp(ical.PropRecurrenceRule)
	rule.SetValueType(ical.ValueRecurrence)
	rule.Value = "FREQ=WEEKLY;COUNT=3"
	event.Props.Set(rule)
	return *event
}

func TestCalDAVListsLocalAndServerExpandedRecurrence(t *testing.T) {
	t.Parallel()

	client := &Client{username: "reader@example.invalid"}
	windowStart := time.Date(2026, time.July, 27, 0, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	master := fixtureRecurringEvent("recurring-event")
	local, err := client.calendarObjectViews(
		fixtureCalendarPath,
		fixtureObjectPath,
		"v1",
		[]ical.Event{master},
		windowStart,
		windowEnd,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 3 ||
		local[0].Start != "2026-07-28T09:00:00Z" ||
		local[1].Start != "2026-08-04T09:00:00Z" ||
		local[2].Start != "2026-08-11T09:00:00Z" ||
		local[0].ID == local[1].ID ||
		local[1].ID == local[2].ID {
		t.Fatalf("local recurrence = %#v", local)
	}

	expanded := make([]ical.Event, 0, len(local))
	for index, start := range []time.Time{
		time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 4, 9, 0, 0, 0, time.UTC),
		time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC),
	} {
		event := fixtureRecurringEvent("recurring-event")
		event.Props.Del(ical.PropRecurrenceRule)
		event.Props.SetDateTime(ical.PropDateTimeStart, start)
		event.Props.SetDateTime(ical.PropDateTimeEnd, start.Add(time.Hour))
		recurrenceID := ical.NewProp(ical.PropRecurrenceID)
		recurrenceID.SetDateTime(start)
		event.Props.Set(recurrenceID)
		event.Props.SetText(ical.PropSummary, fmt.Sprintf("Instance %d", index+1))
		expanded = append(expanded, event)
	}
	serverExpanded, err := client.calendarObjectViews(
		fixtureCalendarPath,
		fixtureObjectPath,
		"v1",
		expanded,
		windowStart,
		windowEnd,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(serverExpanded) != 3 ||
		serverExpanded[0].Subject != "Instance 1" ||
		serverExpanded[0].ID == serverExpanded[1].ID ||
		serverExpanded[1].ID == serverExpanded[2].ID {
		t.Fatalf("server recurrence = %#v", serverExpanded)
	}
}

func TestCalDAVOriginalTimePreservesFloatingAndZoneSemantics(t *testing.T) {
	t.Parallel()

	floating := ical.NewProp(ical.PropDateTimeStart)
	floating.Value = "20260728T090000"
	value, zone, isFloating := calDAVOriginalTime(floating, "")
	if value != floating.Value || zone != "" || !isFloating {
		t.Fatalf(
			"floating semantics = %q, %q, %t",
			value,
			zone,
			isFloating,
		)
	}

	zoned := ical.NewProp(ical.PropDateTimeStart)
	zoned.Value = "20260728T090000"
	zoned.Params.Set(ical.ParamTimezoneID, "Europe/London")
	value, zone, isFloating = calDAVOriginalTime(zoned, "")
	if value != zoned.Value || zone != "Europe/London" || isFloating {
		t.Fatalf(
			"zoned semantics = %q, %q, %t",
			value,
			zone,
			isFloating,
		)
	}
}

func newFixtureServer(
	t *testing.T,
) (*httptest.Server, *fixtureBackend, *http.Client) {
	return newFixtureServerMode(t, false)
}

func newFixtureServerMode(
	t *testing.T,
	scheduling bool,
) (*httptest.Server, *fixtureBackend, *http.Client) {
	t.Helper()
	backend := newFixtureBackend()
	var encoded bytes.Buffer
	if err := ical.NewEncoder(&encoded).Encode(
		backend.objects[fixtureObjectPath].Data,
	); err != nil {
		t.Fatalf("encode fixture calendar: %v", err)
	}
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
		if scheduling && request.Method == "PROPFIND" &&
			request.URL.Path == "/user/" {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
				http.Error(writer, "fixture read failed", http.StatusInternalServerError)
				return
			}
			request.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte("schedule-outbox-URL")) {
				writer.Header().Set("Content-Type", "application/xml")
				writer.WriteHeader(http.StatusMultiStatus)
				_, _ = io.WriteString(
					writer,
					`<?xml version="1.0" encoding="utf-8"?>`+
						`<D:multistatus xmlns:D="DAV:" `+
						`xmlns:C="urn:ietf:params:xml:ns:caldav">`+
						`<D:response><D:href>/user/</D:href><D:propstat>`+
						`<D:prop><C:calendar-user-address-set>`+
						`<D:href>mailto:reader@example.invalid</D:href>`+
						`</C:calendar-user-address-set>`+
						`<C:schedule-outbox-URL><D:href>/user/outbox/</D:href>`+
						`</C:schedule-outbox-URL></D:prop>`+
						`<D:status>HTTP/1.1 200 OK</D:status>`+
						`</D:propstat></D:response></D:multistatus>`,
				)
				return
			}
		}
		if scheduling && request.Method == http.MethodHead &&
			pathWithin(request.URL.Path, fixtureCalendarPath) {
			backend.mu.Lock()
			_, exists := backend.objects[request.URL.Path]
			backend.mu.Unlock()
			if !exists {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Schedule-Tag", `"schedule-v1"`)
			writer.WriteHeader(http.StatusOK)
			return
		}
		if scheduling && request.Header.Get("If-Schedule-Tag-Match") != "" {
			backend.mu.Lock()
			backend.scheduleMatches = append(
				backend.scheduleMatches,
				request.Header.Get("If-Schedule-Tag-Match"),
			)
			backend.mu.Unlock()
		}
		if request.Method == http.MethodDelete {
			backend.mu.Lock()
			object := backend.objects[request.URL.Path]
			backend.deleteMatches = append(
				backend.deleteMatches,
				request.Header.Get("If-Match"),
			)
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
	httpClient := &http.Client{Transport: transport}
	return server, backend, httpClient
}

func TestClientUsesTLSDiscoveryAndConditionalCalendarWrites(t *testing.T) {
	t.Parallel()
	server, backend, httpClient := newFixtureServer(t)
	password := []byte("synthetic-secret")
	client, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: password, Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedPassword := client.password
	t.Cleanup(func() { _ = client.Close() })

	calendars, err := client.ListCalendarFolders(
		t.Context(),
		application.CalendarFolderListInput{Limit: 10},
	)
	if err != nil || len(calendars.Calendars) != 1 ||
		!calendars.Calendars[0].IsDefault ||
		calendars.Calendars[0].AccessRole != "unknown" ||
		calendars.Calendars[0].ID == "" {
		t.Fatalf("ListCalendarFolders() = %#v, %v", calendars, err)
	}
	page, err := client.ListCalendarEvents(t.Context(), application.CalendarListInput{
		Calendar: application.CalendarFolder{
			Kind: application.CalendarFolderDistinguished,
			ID:   "calendar",
		},
		Start: "2026-07-28T00:00:00Z",
		End:   "2026-07-29T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ChangeKey != "v1" ||
		page.Events[0].OriginalStart != "20260728T090000Z" ||
		page.Events[0].OriginalStartTimeZone != "UTC" ||
		page.Events[0].OriginalStartFloating {
		t.Fatalf("ListCalendarEvents() = %#v", page)
	}
	event := page.Events[0]

	staleSubject := "Must not write"
	if _, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: event.ID, ChangeKey: "stale", Subject: &staleSubject,
		},
	); err == nil || !strings.Contains(err.Error(), "changed before write") {
		t.Fatalf("stale UpdateCalendarEvent() error = %v", err)
	}

	subject := "Conditionally updated"
	start := "2026-07-28T09:00:00Z"
	end := "2026-07-28T10:00:00Z"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: event.ID, ChangeKey: event.ChangeKey, Subject: &subject,
			Start: &start, End: &end, ReplaceRecurrence: true,
			Recurrence: &application.CalendarRecurrence{
				Pattern:  application.CalendarRecurrenceWeekly,
				Interval: 1, DaysOfWeek: []string{"Tuesday"},
				NumberOfOccurrences: 4,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	putConditions := append([]string(nil), backend.putConditions...)
	stored := backend.objects[fixtureObjectPath]
	backend.mu.Unlock()
	if len(putConditions) != 1 || putConditions[0] != `|"v1"` {
		t.Fatalf("PUT conditions = %#v", putConditions)
	}
	recurrence := stored.Data.Events()[0].Props.Get(ical.PropRecurrenceRule)
	if recurrence == nil ||
		recurrence.Value != "FREQ=WEEKLY;BYDAY=TU;COUNT=4" {
		t.Fatalf("stored recurrence = %#v", recurrence)
	}

	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID: event.ID, ChangeKey: updated.ChangeKey,
		},
	); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	deleteMatches := append([]string(nil), backend.deleteMatches...)
	backend.mu.Unlock()
	if len(deleteMatches) != 1 ||
		deleteMatches[0] != `"`+updated.ChangeKey+`"` {
		t.Fatalf("DELETE If-Match = %#v", deleteMatches)
	}

	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Subject:  "Created safely",
			Start:    "2026-07-29T09:00:00Z",
			End:      "2026-07-29T10:00:00Z",
			TimeZone: "UTC",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.ChangeKey == "" {
		t.Fatalf("CreateCalendarEvent() = %#v", created)
	}
	backend.mu.Lock()
	lastCondition := backend.putConditions[len(backend.putConditions)-1]
	backend.mu.Unlock()
	if lastCondition != "*|" {
		t.Fatalf("create PUT condition = %q", lastCondition)
	}

	_, err = client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Subject:           "Invited event",
			Start:             "2026-07-30T09:00:00Z",
			End:               "2026-07-30T10:00:00Z",
			TimeZone:          "UTC",
			RequiredAttendees: []string{"guest@example.invalid"},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "invitations require") {
		t.Fatalf("attendee CreateCalendarEvent() error = %v", err)
	}
	if _, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{OnlineMeeting: true},
	); err == nil || !strings.Contains(err.Error(), "cannot provision a Teams meeting") {
		t.Fatalf("Teams CreateCalendarEvent() error = %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	for index, value := range ownedPassword {
		if value != 0 {
			t.Fatalf("owned credential byte %d was not zeroed", index)
		}
	}
	if string(password) != "synthetic-secret" {
		t.Fatal("New() modified its caller-owned credential buffer")
	}
}

func TestCalDAVSchedulingGuardsAttendeeUpdatesAndCancellation(t *testing.T) {
	t.Parallel()

	server, backend, httpClient := newFixtureServerMode(t, true)
	client, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"), Client: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if !client.SchedulingAvailable() ||
		client.calendarIdentity() != "reader@example.invalid" {
		t.Fatalf(
			"scheduling = %t identity = %q",
			client.SchedulingAvailable(),
			client.calendarIdentity(),
		)
	}

	created, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{
			Calendar: application.CalendarFolder{
				Kind: application.CalendarFolderDistinguished,
				ID:   "calendar",
			},
			Subject:           "Invited event",
			Start:             "2026-07-30T09:00:00Z",
			End:               "2026-07-30T10:00:00Z",
			TimeZone:          "UTC",
			RequiredAttendees: []string{"guest@example.invalid"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	subject := "Updated invitation"
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: created.ID, ChangeKey: created.ChangeKey,
			Subject: &subject,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := decodeEventID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	stored := backend.objects[reference.Path]
	backend.mu.Unlock()
	events := stored.Data.Events()
	if len(events) != 1 {
		t.Fatalf("scheduled object events = %d", len(events))
	}
	sequence, err := events[0].Props.Get(ical.PropSequence).Int()
	if err != nil || sequence != 1 {
		t.Fatalf("scheduled SEQUENCE = %d error = %v", sequence, err)
	}

	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID: created.ID, ChangeKey: updated.ChangeKey,
		},
	); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	scheduleMatches := append([]string(nil), backend.scheduleMatches...)
	backend.mu.Unlock()
	if len(scheduleMatches) != 2 ||
		scheduleMatches[0] != `"schedule-v1"` ||
		scheduleMatches[1] != `"schedule-v1"` {
		t.Fatalf("If-Schedule-Tag-Match = %#v", scheduleMatches)
	}
}

func TestClientRefusesUnsafeTLSAndRedirects(t *testing.T) {
	t.Parallel()
	server, _, _ := newFixtureServer(t)
	insecureTransport := http.DefaultTransport.(*http.Transport).Clone()
	insecureTransport.TLSClientConfig.InsecureSkipVerify = true
	if _, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"),
		Client:   &http.Client{Transport: insecureTransport},
	}); err == nil || !strings.Contains(err.Error(), "cannot be disabled") {
		t.Fatalf("New() insecure TLS error = %v", err)
	}

	redirectTarget := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, server.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectTarget.Close()
	roots := x509.NewCertPool()
	roots.AddCert(redirectTarget.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	_, err := New(t.Context(), Options{
		Endpoint: redirectTarget.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"),
		Client:   &http.Client{Transport: transport},
	})
	if err == nil || !strings.Contains(err.Error(), "redirects are not accepted") {
		t.Fatalf("New() redirect error = %v", err)
	}
}

func TestCalDAVEventTimesRequiresIANAZone(t *testing.T) {
	t.Parallel()
	start, _ := time.Parse(time.RFC3339, "2026-07-28T09:00:00+01:00")
	end := start.Add(time.Hour)
	convertedStart, convertedEnd, err := calDAVEventTimes(start, end, "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	if convertedStart.Location().String() != "Europe/London" ||
		convertedEnd.Sub(convertedStart) != time.Hour {
		t.Fatalf("calDAVEventTimes() = %v, %v", convertedStart, convertedEnd)
	}
	if _, _, err := calDAVEventTimes(start, end, "GMT Standard Time"); err == nil {
		t.Fatal("calDAVEventTimes() accepted a non-IANA zone")
	}
}
