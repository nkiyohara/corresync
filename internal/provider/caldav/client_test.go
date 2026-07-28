package caldav

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
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
	mu            sync.Mutex
	objects       map[string]webcaldav.CalendarObject
	version       int
	putConditions []string
	deleteMatches []string
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

func newFixtureServer(
	t *testing.T,
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
	if len(page.Events) != 1 || page.Events[0].ChangeKey != "v1" {
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
	updated, err := client.UpdateCalendarEvent(
		t.Context(),
		application.CalendarUpdateInput{
			EventID: event.ID, ChangeKey: event.ChangeKey, Subject: &subject,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	putConditions := append([]string(nil), backend.putConditions...)
	backend.mu.Unlock()
	if len(putConditions) != 1 || putConditions[0] != `|"v1"` {
		t.Fatalf("PUT conditions = %#v", putConditions)
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

	invited, err := client.CreateCalendarEvent(
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
	if err := client.CancelCalendarEvent(
		t.Context(),
		application.CalendarCancelInput{
			EventID: invited.ID, ChangeKey: invited.ChangeKey,
		},
	); err == nil || !strings.Contains(err.Error(), "scheduling cancellation is unavailable") {
		t.Fatalf("attendee CancelCalendarEvent() error = %v", err)
	}
	if _, err := client.CreateCalendarEvent(
		t.Context(),
		application.CalendarCreateInput{TeamsMeeting: true},
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
