package outlookweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func validCalendarInput() application.CalendarListInput {
	return application.CalendarListInput{
		Account:  "work",
		Calendar: application.CalendarFolder{Kind: application.CalendarFolderDistinguished, ID: "calendar"},
		Start:    "2026-07-17T01:00:00+01:00",
		End:      "2026-07-18T01:00:00+01:00",
	}
}

func TestListCalendarFoldersDiscoversTypedCalendarHierarchy(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "find_calendar_folder_response.json")
	expectedRequest := readFixture(t, "find_calendar_folder_request.json")
	requests := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requests <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	page, err := testClient(t, server, nil).ListCalendarFolders(
		t.Context(),
		application.CalendarFolderListInput{Account: "work", Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, <-requests, expectedRequest)
	if len(page.Calendars) != 3 || page.TotalCalendars != 3 ||
		page.Calendars[0].ID != "calendar" ||
		!page.Calendars[0].IsDefault ||
		!page.Calendars[0].CanEdit ||
		page.Calendars[1].ID != "team-calendar" ||
		!page.Calendars[1].CanEdit ||
		page.Calendars[1].AccessRole != "writer" ||
		page.Calendars[2].ID != "birthdays-calendar" ||
		page.Calendars[2].CanEdit ||
		page.Calendars[2].AccessRole != "reader" ||
		!page.IncludesLastItem {
		t.Fatalf("ListCalendarFolders() = %#v, %v", page, err)
	}
}

func TestListCalendarFoldersRejectsProviderPaginationWithoutProgress(
	t *testing.T,
) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"Body":{"ResponseMessages":{"Items":[{"ResponseClass":"Success","ResponseCode":"NoError","RootFolder":{"Folders":[],"TotalItemsInView":1,"IncludesLastItemInRange":false}}]}}}`,
		))
	}))
	defer server.Close()
	_, err := testClient(t, server, nil).ListCalendarFolders(
		t.Context(),
		application.CalendarFolderListInput{Account: "work", Limit: 10},
	)
	if err == nil {
		t.Fatal("ListCalendarFolders() accepted pagination without progress")
	}
}

func TestCalendarViewRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildCalendarViewEnvelope(validCalendarInput())
	if err != nil {
		t.Fatalf("buildCalendarViewEnvelope() error = %v", err)
	}
	actual := marshalJSON(t, payload)
	want := readFixture(t, "get_calendar_view_request.json")
	assertJSONEqual(t, actual, want)
}

func TestListCalendarEventsNormalizesGoldenResponse(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "get_calendar_view_response.json")
	expectedRequest := readFixture(t, "get_calendar_view_request.json")
	requestBodies := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requestBodies <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	page, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	if err != nil {
		t.Fatalf("ListCalendarEvents() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if len(page.Events) != 2 || page.Start != validCalendarInput().Start || page.End != validCalendarInput().End {
		t.Fatalf("unexpected page: %+v", page)
	}
	first := page.Events[0]
	if first.ID != "synthetic-event-1" || first.Start != "2026-07-17T09:00:00Z" ||
		first.OriginalStart != "2026-07-17T09:00:00.000" ||
		first.OriginalStartTimeZone != "UTC" ||
		first.Location != "Room 1" || first.Organizer.Address != "alice@example.invalid" ||
		!first.IsOnlineMeeting || first.IsCancelled {
		t.Fatalf("unexpected first event: %+v", first)
	}
	if page.Events[1].Location != "Home office" {
		t.Fatalf("string calendar location was not normalized: %+v", page.Events[1])
	}
}

func TestListCalendarEventsAcceptsResponseMessageVariant(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError",
            "CalendarView":{"Items":[]}
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	page, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	if err != nil {
		t.Fatalf("ListCalendarEvents() error = %v", err)
	}
	if page.Events == nil || len(page.Events) != 0 {
		t.Fatalf("unexpected empty page: %+v", page)
	}
}

func TestListCalendarEventsReturnsSanitizedProtocolError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Error","ResponseCode":"ErrorAccessDenied",
            "MessageText":"private server detail"
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.ListCalendarEvents(context.Background(), validCalendarInput())
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.ResponseCode != "ErrorAccessDenied" {
		t.Fatalf("ListCalendarEvents() error = %v, want ErrorAccessDenied", err)
	}
}

func TestListCalendarEventsValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := validCalendarInput()
	input.End = input.Start
	if _, err := client.ListCalendarEvents(context.Background(), input); err == nil {
		t.Fatal("ListCalendarEvents() unexpectedly accepted an empty window")
	}
}

func TestCalendarViewPreservesOpaqueCalendarID(t *testing.T) {
	t.Parallel()

	input := validCalendarInput()
	input.Calendar = application.CalendarFolder{
		Kind: application.CalendarFolderOpaque,
		ID:   "AAMkCaseSensitiveCalendarID==",
	}
	payload, err := buildCalendarViewEnvelope(input)
	if err != nil {
		t.Fatalf("buildCalendarViewEnvelope() error = %v", err)
	}
	folder := payload.Body.CalendarID.BaseFolderID
	if folder.ID != input.Calendar.ID || folder.Type != "FolderId:#Exchange" {
		t.Fatalf("opaque calendar ID changed: %+v", folder)
	}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return data
}

func TestCalendarWritesConvertAbsoluteInstantsToExchangeZone(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, start, end, zone, wantStart, wantEnd, wantDate string }{
		{"summer UTC input", "2026-07-20T09:00:00Z", "2026-07-20T10:00:00Z", "GMT Standard Time", "2026-07-20T10:00:00.000", "2026-07-20T11:00:00.000", "2026-07-20"},
		{"equivalent summer offset", "2026-07-20T10:00:00+01:00", "2026-07-20T11:00:00+01:00", "GMT Standard Time", "2026-07-20T10:00:00.000", "2026-07-20T11:00:00.000", "2026-07-20"},
		{"spring transition", "2026-03-29T00:30:00Z", "2026-03-29T01:30:00Z", "GMT Standard Time", "2026-03-29T00:30:00.000", "2026-03-29T02:30:00.000", "2026-03-29"},
		{"after autumn transition", "2026-10-25T02:00:00Z", "2026-10-25T03:00:00Z", "GMT Standard Time", "2026-10-25T02:00:00.000", "2026-10-25T03:00:00.000", "2026-10-25"},
		{"repeated hour in UTC", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z", "UTC", "2026-10-25T00:30:00.000", "2026-10-25T01:30:00.000", "2026-10-25"},
		{"winter", "2026-12-20T09:00:00Z", "2026-12-20T10:00:00Z", "GMT Standard Time", "2026-12-20T09:00:00.000", "2026-12-20T10:00:00.000", "2026-12-20"},
		{"previous local date", "2026-07-20T00:30:00Z", "2026-07-20T01:30:00Z", "Pacific Standard Time", "2026-07-19T17:30:00.000", "2026-07-19T18:30:00.000", "2026-07-19"},
		{"default UTC", "2026-07-20T00:30:00+01:00", "2026-07-20T01:30:00+01:00", "", "2026-07-19T23:30:00.000", "2026-07-20T00:30:00.000", "2026-07-19"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testCalendarCreateInput()
			input.Start = test.start
			input.End = test.end
			input.TimeZone = test.zone
			input.Recurrence = &application.CalendarRecurrence{Pattern: application.CalendarRecurrenceDaily, Interval: 1, NumberOfOccurrences: 2}
			created, err := buildCalendarCreateEnvelope(input)
			if err != nil {
				t.Fatal(err)
			}
			item := created.Body.Items[0]
			if item.Start != test.wantStart || item.End != test.wantEnd {
				t.Fatalf("create boundaries = %q, %q", item.Start, item.End)
			}
			if got := item.Recurrence.RecurrenceRange.(numberedRecurrenceRange).StartDate; got != test.wantDate {
				t.Fatalf("create recurrence date = %q", got)
			}
			update := application.CalendarUpdateInput{Account: "work", EventID: "event", ChangeKey: "version", Start: owaStringPointer(test.start), End: owaStringPointer(test.end), ReplaceRecurrence: true, Recurrence: input.Recurrence}
			if test.zone != "" {
				update.TimeZone = owaStringPointer(test.zone)
			}
			updated, err := buildCalendarUpdateEnvelope(update)
			if err != nil {
				t.Fatal(err)
			}
			var start, end, date string
			for _, field := range updated.Body.ItemChange.Updates {
				switch field.Path.FieldURI {
				case "Start":
					start = *field.Item.Start
				case "End":
					end = *field.Item.End
				case "Recurrence":
					date = field.Item.Recurrence.RecurrenceRange.(numberedRecurrenceRange).StartDate
				}
			}
			if start != test.wantStart || end != test.wantEnd || date != test.wantDate {
				t.Fatalf("update boundaries/date = %q, %q, %q", start, end, date)
			}
			if *update.Start != test.start || *update.End != test.end {
				t.Fatal("normalization mutated input pointers")
			}
		})
	}
}

func TestCalendarWritesRejectUnrepresentableZonesAndBoundariesBeforeNetwork(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid calendar write reached network") }))
	defer server.Close()
	client := testClient(t, server, nil)
	tests := []struct {
		name, start, end, zone string
		allDay                 bool
	}{
		{"unknown Windows zone", "2026-07-20T09:00:00Z", "2026-07-20T10:00:00Z", "Unknown Standard Time", false},
		{"IANA zone", "2026-07-20T09:00:00Z", "2026-07-20T10:00:00Z", "Europe/London", false},
		{"false local midnight", "2026-07-20T00:00:00Z", "2026-07-21T00:00:00Z", "GMT Standard Time", true},
		{"first repeated hour", "2026-10-25T00:30:00Z", "2026-10-25T02:30:00Z", "GMT Standard Time", false},
		{"second repeated hour", "2026-10-25T01:30:00Z", "2026-10-25T02:30:00Z", "GMT Standard Time", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := testCalendarCreateInput()
			input.Start = test.start
			input.End = test.end
			input.TimeZone = test.zone
			input.AllDay = test.allDay
			if _, err := client.CreateCalendarEvent(t.Context(), input); err == nil {
				t.Fatal("create accepted invalid boundary")
			}
			update := application.CalendarUpdateInput{Account: "work", EventID: "event", ChangeKey: "version", Start: owaStringPointer(test.start), End: owaStringPointer(test.end), TimeZone: owaStringPointer(test.zone), AllDay: &test.allDay}
			if _, err := client.UpdateCalendarEvent(t.Context(), update); err == nil {
				t.Fatal("update accepted invalid boundary")
			}
		})
	}
}

func TestDefaultCalendarUsesObservedRights(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rights      map[string]bool
		omitDefault bool
		wantEdit    bool
		wantRole    string
	}{
		{name: "read only", rights: map[string]bool{"Read": true}, wantRole: "reader"},
		{name: "writer", rights: map[string]bool{"Read": true, "CreateContents": true, "Modify": true}, wantEdit: true, wantRole: "writer"},
		{name: "create only", rights: map[string]bool{"Read": true, "CreateContents": true}, wantRole: "reader"},
		{name: "rights absent", wantRole: "unknown"},
		{name: "default absent", omitDefault: true, wantRole: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			folders := []any{}
			if !test.omitDefault {
				folder := map[string]any{"FolderId": map[string]string{"Id": "default-calendar"}, "DisplayName": "Observed calendar", "FolderClass": "IPF.Appointment", "DistinguishedFolderId": "calendar"}
				if test.rights != nil {
					folder["EffectiveRights"] = test.rights
				}
				folders = append(folders, folder)
			}
			response := map[string]any{"Body": map[string]any{"ResponseMessages": map[string]any{"Items": []any{map[string]any{"ResponseClass": "Success", "ResponseCode": "NoError", "RootFolder": map[string]any{"Folders": folders, "TotalItemsInView": len(folders), "IncludesLastItemInRange": true}}}}}}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
			defer server.Close()
			page, err := testClient(t, server, nil).ListCalendarFolders(t.Context(), application.CalendarFolderListInput{Account: "work", Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Calendars) != 1 {
				t.Fatalf("calendars = %+v", page.Calendars)
			}
			calendar := page.Calendars[0]
			if calendar.ID != "calendar" || !calendar.IsDefault || calendar.CanEdit != test.wantEdit || calendar.AccessRole != test.wantRole {
				t.Fatalf("default calendar = %+v", calendar)
			}
			if !test.omitDefault && calendar.DisplayName != "Observed calendar" {
				t.Fatalf("observed display name lost: %+v", calendar)
			}
		})
	}
}

func TestCalendarWritesValidateRecurrenceAgainstConvertedDate(t *testing.T) {
	t.Parallel()
	input := testCalendarCreateInput()
	input.Start = "2026-07-19T23:30:00Z"
	input.End = "2026-07-20T00:30:00Z"
	input.TimeZone = "GMT Standard Time"
	input.Recurrence = &application.CalendarRecurrence{Pattern: application.CalendarRecurrenceDaily, Interval: 1, EndDate: "2026-07-19"}
	if _, err := buildCalendarCreateEnvelope(input); err == nil {
		t.Fatal("create accepted recurrence ending before its local start date")
	}
	update := application.CalendarUpdateInput{Account: "work", EventID: "event", ChangeKey: "version", Start: &input.Start, End: &input.End, TimeZone: &input.TimeZone, ReplaceRecurrence: true, Recurrence: input.Recurrence}
	if _, err := buildCalendarUpdateEnvelope(update); err == nil {
		t.Fatal("update accepted recurrence ending before its local start date")
	}
}
