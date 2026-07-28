package application

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestCalendarServiceListsBoundedSelectableFolders(t *testing.T) {
	t.Parallel()
	reader := &fakeCalendarReader{folderPage: CalendarFolderPage{
		Calendars: []CalendarFolderSummary{{
			ID: "calendar-1", DisplayName: "Work",
			IsDefault: true, CanEdit: true, AccessRole: "owner",
			TimeZone: "Europe/London",
		}},
		TotalCalendars: 1, IncludesLastItem: true,
	}}
	service, recorder := testCalendarService(t, reader)
	service.provenance = domain.Provenance{
		AccountID: "acc_00000000000000000000000000000001",
		Provider:  domain.ProviderCalDAV,
	}
	page, err := service.ListFolders(
		t.Context(),
		CalendarFolderListInput{
			Account: "acc_00000000000000000000000000000001",
			Limit:   10,
		},
		domain.Caller{Surface: "cli", Instance: "process-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Calendars) != 1 ||
		page.Calendars[0].Provenance.CalendarID != "calendar-1" ||
		len(recorder.events) != 2 ||
		recorder.events[1].Operation.Name != "calendar.folders" {
		t.Fatalf("page=%+v audit=%+v", page, recorder.events)
	}
}

func TestCalendarFolderPageRejectsProviderContractViolations(t *testing.T) {
	t.Parallel()
	input := CalendarFolderListInput{
		Account: "acc_00000000000000000000000000000001",
		Limit:   1,
	}
	for _, page := range []CalendarFolderPage{
		{
			Calendars: []CalendarFolderSummary{
				{ID: "one", DisplayName: "One", IsDefault: true, AccessRole: "reader"},
				{ID: "two", DisplayName: "Two", IsDefault: true, AccessRole: "reader"},
			},
			TotalCalendars: 2,
		},
		{
			Calendars: []CalendarFolderSummary{{
				ID: "one", DisplayName: "One",
				CanEdit: true, AccessRole: "reader",
			}},
			TotalCalendars: 1,
		},
	} {
		if err := validateCalendarFolderPage(page, input); err == nil {
			t.Fatalf("validateCalendarFolderPage(%+v) unexpectedly succeeded", page)
		}
	}
}
