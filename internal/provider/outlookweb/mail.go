package outlookweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
)

const maxOWAMailPageSize = 25

var ErrMailSearchUnavailable = errors.New("outlook web mailbox search is unavailable")

type folderKind uint8

const (
	folderDistinguished folderKind = iota + 1
	folderOpaque
)

type folderRef struct {
	kind folderKind
	id   string
}

type listMessagesRequest struct {
	Folder               folderRef
	Query                string
	SearchFolderIdentity string
	Offset               int
	Limit                int
	TimeZone             string
}

type mailAddress struct {
	Name    string
	Address string
}

type mailSummary struct {
	ID             string
	ChangeKey      string
	Subject        string
	From           mailAddress
	ReceivedAt     string
	Importance     string
	IsRead         bool
	HasAttachments bool
}

type mailPage struct {
	Messages         []mailSummary
	TotalItemsInView int
	IncludesLastItem bool
}

type findItemEnvelope struct {
	Type   string          `json:"__type"`
	Header requestHeader   `json:"Header"`
	Body   findItemRequest `json:"Body"`
}

type findItemRequest struct {
	Type              string            `json:"__type"`
	ItemShape         itemResponseShape `json:"ItemShape"`
	ShapeName         string            `json:"ShapeName,omitempty"`
	Paging            indexedPageView   `json:"Paging"`
	ParentFolderIDs   []folderID        `json:"ParentFolderIds"`
	Traversal         string            `json:"Traversal"`
	ViewFilter        string            `json:"ViewFilter"`
	FocusedViewFilter int               `json:"FocusedViewFilter"`
	SortOrder         []sortResult      `json:"SortOrder"`
	IsWarmUpSearch    *bool             `json:"IsWarmUpSearch,omitempty"`
	QueryString       *queryString      `json:"QueryString,omitempty"`
	SearchFolderID    string            `json:"SearchFolderIdentity,omitempty"`
}

type queryString struct {
	Type                  string `json:"__type"`
	Value                 string `json:"Value"`
	MaxResultsCount       int    `json:"MaxResultsCount"`
	ResetCache            bool   `json:"ResetCache"`
	ReturnDeletedItems    bool   `json:"ReturnDeletedItems"`
	ReturnHighlightTerms  bool   `json:"ReturnHighlightTerms"`
	WaitForSearchComplete bool   `json:"WaitForSearchComplete"`
}

type itemResponseShape struct {
	Type                 string        `json:"__type"`
	BaseShape            string        `json:"BaseShape"`
	BodyType             string        `json:"BodyType,omitempty"`
	AdditionalProperties []propertyURI `json:"AdditionalProperties,omitempty"`
}

type propertyURI struct {
	Type     string `json:"__type"`
	FieldURI string `json:"FieldURI"`
}

type indexedPageView struct {
	Type               string `json:"__type"`
	BasePoint          string `json:"BasePoint"`
	Offset             int    `json:"Offset"`
	MaxEntriesReturned int    `json:"MaxEntriesReturned"`
}

type sortResult struct {
	Type  string      `json:"__type"`
	Order string      `json:"Order"`
	Path  propertyURI `json:"Path"`
}

type folderID struct {
	Type string `json:"__type"`
	ID   string `json:"Id"`
}

type findItemResponseBody struct {
	ResponseMessages responseMessages[findItemResponseMessage] `json:"ResponseMessages"`
}

type findItemResponseMessage struct {
	ResponseClass string         `json:"ResponseClass"`
	ResponseCode  string         `json:"ResponseCode"`
	RootFolder    findItemFolder `json:"RootFolder"`
}

type findItemFolder struct {
	Items            []findItemMessage `json:"Items"`
	TotalItemsInView int               `json:"TotalItemsInView"`
	IncludesLastItem bool              `json:"IncludesLastItemInRange"`
}

type findItemMessage struct {
	ItemID         itemID    `json:"ItemId"`
	Subject        string    `json:"Subject"`
	From           recipient `json:"From"`
	ReceivedAt     string    `json:"DateTimeReceived"`
	Importance     string    `json:"Importance"`
	IsRead         bool      `json:"IsRead"`
	HasAttachments bool      `json:"HasAttachments"`
}

type itemID struct {
	Type      string `json:"__type,omitempty"`
	ID        string `json:"Id"`
	ChangeKey string `json:"ChangeKey,omitempty"`
}

type recipient struct {
	Mailbox mailbox `json:"Mailbox"`
}

type mailbox struct {
	Name         string `json:"Name"`
	EmailAddress string `json:"EmailAddress"`
}

// ListMessages implements the application mail port using the OWA contract.
func (client *Client) ListMessages(
	ctx context.Context,
	input application.MailListInput,
) (application.MailPage, error) {
	if err := input.Validate(); err != nil {
		return application.MailPage{}, err
	}
	if input.Limit > maxOWAMailPageSize {
		return application.MailPage{}, fmt.Errorf(
			"outlook web mail limit must be between 1 and %d; use offset to request another page",
			maxOWAMailPageSize,
		)
	}
	if err := validateMailZone(input.TimeZone); err != nil {
		return application.MailPage{}, err
	}
	kind := folderOpaque
	folderID := input.Folder.ID
	if input.Folder.Kind == application.MailFolderDistinguished {
		kind = folderDistinguished
		folderID = strings.ToLower(folderID)
	}
	page, err := client.listMessages(ctx, listMessagesRequest{
		Folder:   folderRef{kind: kind, id: folderID},
		Offset:   input.Offset,
		Limit:    input.Limit,
		TimeZone: input.TimeZone,
	})
	if err != nil {
		return application.MailPage{}, err
	}
	return normalizeMailPage(page), nil
}

// SearchMessages implements a bounded AQS search through the registered
// FindItem action. It cannot select a different action or return message bodies.
func (client *Client) SearchMessages(
	ctx context.Context,
	input application.MailSearchInput,
) (application.MailPage, error) {
	if err := input.Validate(); err != nil {
		return application.MailPage{}, err
	}
	if err := validateMailZone(input.TimeZone); err != nil {
		return application.MailPage{}, err
	}
	kind := folderOpaque
	folderID := input.Folder.ID
	if input.Folder.Kind == application.MailFolderDistinguished {
		kind = folderDistinguished
		folderID = strings.ToLower(folderID)
	}
	searchID, err := newRequestID(client.random)
	if err != nil {
		return application.MailPage{}, err
	}
	page, err := client.listMessages(ctx, listMessagesRequest{
		Folder:               folderRef{kind: kind, id: folderID},
		Query:                input.Query,
		SearchFolderIdentity: searchID,
		Offset:               input.Offset,
		Limit:                input.Limit,
		TimeZone:             input.TimeZone,
	})
	if err != nil {
		var httpFailure *HTTPError
		if errors.As(err, &httpFailure) && httpFailure.StatusCode >= http.StatusInternalServerError {
			return application.MailPage{}, errors.Join(ErrMailSearchUnavailable, err)
		}
		return application.MailPage{}, err
	}
	return normalizeMailPage(page), nil
}

func normalizeMailPage(page mailPage) application.MailPage {
	result := application.MailPage{
		Messages:         make([]application.MailSummary, 0, len(page.Messages)),
		TotalItemsInView: page.TotalItemsInView,
		IncludesLastItem: page.IncludesLastItem,
	}
	for _, message := range page.Messages {
		result.Messages = append(result.Messages, application.MailSummary{
			ID:             message.ID,
			ChangeKey:      message.ChangeKey,
			Subject:        message.Subject,
			From:           application.MailAddress{Name: message.From.Name, Address: message.From.Address},
			ReceivedAt:     message.ReceivedAt,
			Importance:     message.Importance,
			IsRead:         message.IsRead,
			HasAttachments: message.HasAttachments,
		})
	}
	return result
}

func (client *Client) listMessages(ctx context.Context, input listMessagesRequest) (mailPage, error) {
	payload, err := buildFindItemEnvelope(input)
	if err != nil {
		return mailPage{}, err
	}
	var response responseEnvelope[findItemResponseBody]
	if err := client.Call(ctx, FindItem, payload, &response); err != nil {
		return mailPage{}, err
	}
	if len(response.Body.ResponseMessages.Items) != 1 {
		return mailPage{}, errors.New("OWA FindItem returned an unexpected response count")
	}
	message := response.Body.ResponseMessages.Items[0]
	if err := checkResponse(FindItem.Name(), message.ResponseClass, message.ResponseCode); err != nil {
		return mailPage{}, err
	}
	if message.RootFolder.TotalItemsInView < 0 || len(message.RootFolder.Items) > input.Limit {
		return mailPage{}, errors.New("OWA FindItem returned an invalid result window")
	}

	page := mailPage{
		Messages:         make([]mailSummary, 0, len(message.RootFolder.Items)),
		TotalItemsInView: message.RootFolder.TotalItemsInView,
		IncludesLastItem: message.RootFolder.IncludesLastItem,
	}
	for _, item := range message.RootFolder.Items {
		if err := validateOpaqueID("message", item.ItemID.ID); err != nil {
			return mailPage{}, fmt.Errorf("invalid message in OWA response: %w", err)
		}
		page.Messages = append(page.Messages, mailSummary{
			ID:             item.ItemID.ID,
			ChangeKey:      item.ItemID.ChangeKey,
			Subject:        item.Subject,
			From:           mailAddress{Name: item.From.Mailbox.Name, Address: item.From.Mailbox.EmailAddress},
			ReceivedAt:     item.ReceivedAt,
			Importance:     item.Importance,
			IsRead:         item.IsRead,
			HasAttachments: item.HasAttachments,
		})
	}
	return page, nil
}

func buildFindItemEnvelope(input listMessagesRequest) (findItemEnvelope, error) {
	if input.Folder.kind != folderDistinguished && input.Folder.kind != folderOpaque {
		return findItemEnvelope{}, errors.New("mail folder is required")
	}
	if input.Offset < 0 {
		return findItemEnvelope{}, errors.New("mail offset must not be negative")
	}
	if input.Limit < 1 || input.Limit > application.MaxMailPageSize {
		return findItemEnvelope{}, fmt.Errorf("mail limit must be between 1 and %d", application.MaxMailPageSize)
	}
	if err := validateMailZone(input.TimeZone); err != nil {
		return findItemEnvelope{}, err
	}
	searching := input.Query != "" || input.SearchFolderIdentity != ""
	if searching {
		if input.Query == "" || strings.TrimSpace(input.Query) != input.Query ||
			len(input.Query) > application.MaxMailSearchQueryBytes || !utf8.ValidString(input.Query) ||
			strings.ContainsAny(input.Query, "\r\n\x00") {
			return findItemEnvelope{}, errors.New("invalid mail search query")
		}
		if input.Limit > application.MaxMailSearchPageSize {
			return findItemEnvelope{}, fmt.Errorf("mail search limit must be between 1 and %d", application.MaxMailSearchPageSize)
		}
		if !validUUID(input.SearchFolderIdentity) {
			return findItemEnvelope{}, errors.New("invalid mail search identity")
		}
	}
	typeName := "FolderId:#Exchange"
	if input.Folder.kind == folderDistinguished {
		typeName = "DistinguishedFolderId:#Exchange"
	}
	properties := []string{
		"item:Subject",
		"message:From",
		"item:DateTimeReceived",
		"item:Importance",
		"message:IsRead",
		"item:HasAttachments",
	}
	additional := make([]propertyURI, 0, len(properties))
	for _, property := range properties {
		additional = append(additional, propertyURI{Type: "PropertyUri:#Exchange", FieldURI: property})
	}
	envelope := findItemEnvelope{
		Type:   "FindItemJsonRequest:#Exchange",
		Header: newRequestHeader(input.TimeZone),
		Body: findItemRequest{
			Type: "FindItemRequest:#Exchange",
			ItemShape: itemResponseShape{
				Type:                 "ItemResponseShape:#Exchange",
				BaseShape:            "IdOnly",
				AdditionalProperties: additional,
			},
			Paging: indexedPageView{
				Type:               "IndexedPageView:#Exchange",
				BasePoint:          "Beginning",
				Offset:             input.Offset,
				MaxEntriesReturned: input.Limit,
			},
			ParentFolderIDs:   []folderID{{Type: typeName, ID: input.Folder.id}},
			Traversal:         "Shallow",
			ViewFilter:        "All",
			FocusedViewFilter: -1,
			SortOrder: []sortResult{{
				Type:  "SortResults:#Exchange",
				Order: "Descending",
				Path: propertyURI{
					Type:     "PropertyUri:#Exchange",
					FieldURI: "item:DateTimeReceived",
				},
			}},
		},
	}
	if searching {
		warmUp := false
		envelope.Body.ShapeName = "MailListItem"
		envelope.Body.ItemShape.AdditionalProperties = nil
		envelope.Body.IsWarmUpSearch = &warmUp
		envelope.Body.QueryString = &queryString{
			Type:                  "QueryStringType:#Exchange",
			Value:                 input.Query,
			MaxResultsCount:       input.Limit,
			ResetCache:            true,
			ReturnDeletedItems:    false,
			ReturnHighlightTerms:  false,
			WaitForSearchComplete: true,
		}
		envelope.Body.SearchFolderID = input.SearchFolderIdentity
	}
	return envelope, nil
}

func validateMailZone(zone string) error {
	if err := validateZone(zone); err != nil {
		return err
	}
	if strings.Contains(zone, "/") {
		return errors.New(
			"outlook web mail time zone must be an Exchange/Windows identifier, " +
				`for example "GMT Standard Time"; omit timeZone to use UTC`,
		)
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validateOpaqueID(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s ID must not be empty", kind)
	}
	if len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s ID is malformed", kind)
	}
	return nil
}
