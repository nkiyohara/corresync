package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

// ListCalendarFolders uses the already registered, typed OWA FindFolder action
// to enumerate the bounded hierarchy below the distinguished calendar. It
// never exposes a generic folder action through the application boundary.
func (client *Client) ListCalendarFolders(
	ctx context.Context,
	input application.CalendarFolderListInput,
) (application.CalendarFolderPage, error) {
	if err := input.Validate(); err != nil {
		return application.CalendarFolderPage{}, err
	}
	calendars := []application.CalendarFolderSummary{{
		ID: "calendar", DisplayName: "Calendar",
		IsDefault: true, CanEdit: true, AccessRole: "owner",
	}}
	seen := map[string]struct{}{"calendar": {}}
	const providerPageSize = application.MaxCalendarFolderPageSize
	remoteOffset := 0
	expectedRemoteTotal := -1
	for {
		payload := findFolderEnvelope{
			Type:   "FindFolderJsonRequest:#Exchange",
			Header: newRequestHeader("UTC"),
			Body: findFolderRequest{
				Type:        "FindFolderRequest:#Exchange",
				FolderShape: newFolderResponseShape(),
				Paging: indexedPageView{
					Type:               "IndexedPageView:#Exchange",
					BasePoint:          "Beginning",
					Offset:             remoteOffset,
					MaxEntriesReturned: providerPageSize,
				},
				ParentFolderIDs: []folderID{{
					Type: "DistinguishedFolderId:#Exchange",
					ID:   "calendar",
				}},
				ReturnParentFolder: true,
				Traversal:          "Deep",
			},
		}
		result, err := client.findFoldersWithPayload(
			ctx,
			payload,
			providerPageSize,
		)
		if err != nil {
			return application.CalendarFolderPage{}, err
		}
		if expectedRemoteTotal == -1 {
			expectedRemoteTotal = result.TotalItemsInView
		} else if result.TotalItemsInView != expectedRemoteTotal {
			return application.CalendarFolderPage{}, errors.New(
				"OWA calendar folder total changed during discovery",
			)
		}
		for _, folder := range result.Folders {
			if err := validateFindFolderItem(folder); err != nil {
				return application.CalendarFolderPage{}, fmt.Errorf(
					"invalid calendar folder in OWA response: %w",
					err,
				)
			}
			if !strings.EqualFold(folder.FolderClass, "IPF.Appointment") ||
				strings.EqualFold(folder.DistinguishedID, "calendar") {
				continue
			}
			if _, exists := seen[folder.FolderID.ID]; exists {
				return application.CalendarFolderPage{}, errors.New(
					"OWA calendar discovery returned a duplicate folder",
				)
			}
			seen[folder.FolderID.ID] = struct{}{}
			canEdit := folder.EffectiveRights.CreateContents &&
				folder.EffectiveRights.Modify
			accessRole := "unknown"
			if canEdit {
				accessRole = "writer"
			} else if folder.EffectiveRights.Read {
				accessRole = "reader"
			}
			calendars = append(
				calendars,
				application.CalendarFolderSummary{
					ID:          folder.FolderID.ID,
					DisplayName: folder.DisplayName,
					CanEdit:     canEdit,
					AccessRole:  accessRole,
				},
			)
		}
		if result.IncludesLastItem {
			break
		}
		if len(result.Folders) == 0 {
			return application.CalendarFolderPage{}, errors.New(
				"OWA calendar folder discovery made no pagination progress",
			)
		}
		remoteOffset += len(result.Folders)
		if remoteOffset > application.MaxCalendarFolderOffset {
			return application.CalendarFolderPage{}, errors.New(
				"OWA calendar folder hierarchy exceeds the bounded scan",
			)
		}
	}
	start := min(input.Offset, len(calendars))
	end := min(start+input.Limit, len(calendars))
	return application.CalendarFolderPage{
		Calendars:        calendars[start:end],
		TotalCalendars:   len(calendars),
		IncludesLastItem: end == len(calendars),
	}, nil
}
