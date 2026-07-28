package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

type findFolderEnvelope struct {
	Type   string            `json:"__type"`
	Header requestHeader     `json:"Header"`
	Body   findFolderRequest `json:"Body"`
}

type findFolderRequest struct {
	Type               string              `json:"__type"`
	FolderShape        folderResponseShape `json:"FolderShape"`
	Paging             indexedPageView     `json:"Paging"`
	ParentFolderIDs    []folderID          `json:"ParentFolderIds"`
	ReturnParentFolder bool                `json:"ReturnParentFolder"`
	Traversal          string              `json:"Traversal"`
}

type folderResponseShape struct {
	Type                 string        `json:"__type"`
	BaseShape            string        `json:"BaseShape"`
	AdditionalProperties []propertyURI `json:"AdditionalProperties"`
}

type findFolderResponseBody struct {
	ResponseMessages responseMessages[findFolderResponseMessage] `json:"ResponseMessages"`
}

type findFolderResponseMessage struct {
	ResponseClass string           `json:"ResponseClass"`
	ResponseCode  string           `json:"ResponseCode"`
	RootFolder    findFolderResult `json:"RootFolder"`
}

type findFolderResult struct {
	Folders          []findFolderItem `json:"Folders"`
	TotalItemsInView int              `json:"TotalItemsInView"`
	IncludesLastItem bool             `json:"IncludesLastItemInRange"`
}

type findFolderItem struct {
	FolderID         itemID `json:"FolderId"`
	ParentFolderID   itemID `json:"ParentFolderId"`
	DisplayName      string `json:"DisplayName"`
	FolderClass      string `json:"FolderClass"`
	DistinguishedID  string `json:"DistinguishedFolderId"`
	ChildFolderCount int    `json:"ChildFolderCount"`
	TotalCount       int    `json:"TotalCount"`
	UnreadCount      int    `json:"UnreadCount"`
}

// ListMailFolders implements bounded folder discovery using OWA FindFolder.
func (client *Client) ListMailFolders(
	ctx context.Context,
	input application.MailFolderListInput,
) (application.MailFolderPage, error) {
	if err := input.Validate(); err != nil {
		return application.MailFolderPage{}, err
	}
	payload, err := buildFindFolderEnvelope(input)
	if err != nil {
		return application.MailFolderPage{}, err
	}
	var response responseEnvelope[findFolderResponseBody]
	if err := client.Call(ctx, FindFolder, payload, &response); err != nil {
		return application.MailFolderPage{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return application.MailFolderPage{}, errors.New("OWA FindFolder returned an unexpected response count")
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(FindFolder.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return application.MailFolderPage{}, err
	}
	if message.RootFolder.TotalItemsInView < 0 {
		return application.MailFolderPage{}, errors.New("OWA FindFolder returned a negative total")
	}
	if len(message.RootFolder.Folders) > input.Limit {
		return application.MailFolderPage{}, fmt.Errorf("OWA FindFolder returned more than %d folders", input.Limit)
	}
	page := application.MailFolderPage{
		Folders:          make([]application.MailFolderSummary, 0, len(message.RootFolder.Folders)),
		TotalFolders:     message.RootFolder.TotalItemsInView,
		IncludesLastItem: message.RootFolder.IncludesLastItem,
	}
	for _, folder := range message.RootFolder.Folders {
		if err := validateFindFolderItem(folder); err != nil {
			return application.MailFolderPage{}, fmt.Errorf("invalid folder in OWA response: %w", err)
		}
		page.Folders = append(page.Folders, application.MailFolderSummary{
			ID:               folder.FolderID.ID,
			ChangeKey:        folder.FolderID.ChangeKey,
			ParentID:         folder.ParentFolderID.ID,
			DisplayName:      folder.DisplayName,
			Class:            folder.FolderClass,
			DistinguishedID:  folder.DistinguishedID,
			ChildFolderCount: folder.ChildFolderCount,
			TotalItemCount:   folder.TotalCount,
			UnreadItemCount:  folder.UnreadCount,
		})
	}
	return page, nil
}

func buildFindFolderEnvelope(input application.MailFolderListInput) (findFolderEnvelope, error) {
	if err := input.Validate(); err != nil {
		return findFolderEnvelope{}, err
	}
	parentType := "FolderId:#Exchange"
	parentID := input.Parent.ID
	if input.Parent.Kind == application.MailFolderDistinguished {
		parentType = "DistinguishedFolderId:#Exchange"
		parentID = strings.ToLower(parentID)
	}
	properties := []string{
		"folder:DisplayName",
		"folder:ParentFolderId",
		"folder:FolderClass",
		"folder:DistinguishedFolderId",
		"folder:ChildFolderCount",
		"folder:TotalCount",
		"folder:UnreadCount",
	}
	additional := make([]propertyURI, 0, len(properties))
	for _, property := range properties {
		additional = append(additional, propertyURI{
			Type: "PropertyUri:#Exchange", FieldURI: property,
		})
	}
	traversal := "Shallow"
	if input.Traversal == application.MailFolderTraversalDeep {
		traversal = "Deep"
	}
	return findFolderEnvelope{
		Type:   "FindFolderJsonRequest:#Exchange",
		Header: newRequestHeader(input.TimeZone),
		Body: findFolderRequest{
			Type: "FindFolderRequest:#Exchange",
			FolderShape: folderResponseShape{
				Type: "FolderResponseShape:#Exchange", BaseShape: "IdOnly",
				AdditionalProperties: additional,
			},
			Paging: indexedPageView{
				Type: "IndexedPageView:#Exchange", BasePoint: "Beginning",
				Offset: input.Offset, MaxEntriesReturned: input.Limit,
			},
			ParentFolderIDs:    []folderID{{Type: parentType, ID: parentID}},
			ReturnParentFolder: true,
			Traversal:          traversal,
		},
	}, nil
}

func validateFindFolderItem(folder findFolderItem) error {
	if err := validateOpaqueID("mail folder", folder.FolderID.ID); err != nil {
		return err
	}
	if folder.ParentFolderID.ID != "" {
		if err := validateOpaqueID("mail folder parent", folder.ParentFolderID.ID); err != nil {
			return err
		}
	}
	if folder.ChildFolderCount < 0 || folder.TotalCount < 0 || folder.UnreadCount < 0 {
		return errors.New("mail folder counts must not be negative")
	}
	if len(folder.DisplayName) > 4096 || len(folder.FolderClass) > 256 ||
		len(folder.DistinguishedID) > 128 {
		return errors.New("mail folder metadata exceeds limits")
	}
	return nil
}
