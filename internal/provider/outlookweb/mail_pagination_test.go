package outlookweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

// Model a result-set cap independently of the returned page. The old request
// incorrectly recreated a set of only Limit matches before applying Offset.
func cappedSearchClient(t *testing.T, count int, folder application.MailFolder, query string) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request findItemEnvelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		body := request.Body
		if body.QueryString == nil || body.QueryString.Value != query || len(body.ParentFolderIDs) != 1 ||
			body.ParentFolderIDs[0].ID != folder.ID || body.Traversal != "Shallow" {
			t.Errorf("search query or exact folder scope changed: %+v", body)
			return
		}
		if len(body.SortOrder) != 1 || body.SortOrder[0].Order != "Descending" ||
			body.SortOrder[0].Path.FieldURI != "item:DateTimeReceived" {
			t.Error("search lost received-date descending order")
		}
		total := min(count, body.QueryString.MaxResultsCount)
		start := min(body.Paging.Offset, total)
		end := min(start+body.Paging.MaxEntriesReturned, total)
		items := make([]findItemMessage, 0, end-start)
		for index := start; index < end; index++ {
			items = append(items, findItemMessage{
				ItemID: itemID{ID: fmt.Sprintf("synthetic-%05d", index)},
				ReceivedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).
					Add(-time.Duration(index) * time.Hour).Format(time.RFC3339),
			})
		}
		response := responseEnvelope[findItemResponseBody]{Body: findItemResponseBody{
			ResponseMessages: responseMessages[findItemResponseMessage]{Items: []findItemResponseMessage{{
				ResponseClass: "Success", ResponseCode: "NoError",
				RootFolder: findItemFolder{Items: items, TotalItemsInView: total, IncludesLastItem: end == total && (end == count || start == total)},
			}}},
		}}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return testClient(t, server, nil)
}

func TestSearchPaginationReachesEveryMatch(t *testing.T) {
	for _, size := range []int{5, 50} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			folder := application.MailFolder{Kind: application.MailFolderDistinguished, ID: "sentitems"}
			const count = 113
			client := cappedSearchClient(t, count, folder, "synthetic")
			for offset := 0; offset < count; offset += size {
				page, err := client.SearchMessages(t.Context(), application.MailSearchInput{
					Account: "work", Folder: folder, Query: "synthetic", Offset: offset, Limit: size,
				})
				if err != nil {
					t.Fatal(err)
				}
				want := min(size, count-offset)
				if len(page.Messages) != want || page.IncludesLastItem != (offset+want == count) {
					t.Fatalf("offset %d: got %d items, terminal=%v", offset, len(page.Messages), page.IncludesLastItem)
				}
				for index, message := range page.Messages {
					if message.ID != fmt.Sprintf("synthetic-%05d", offset+index) {
						t.Fatalf("missing, duplicate, or misordered match at %d: %s", offset+index, message.ID)
					}
				}
			}
		})
	}
}

func TestSearchShrinkingLookaheadPreservesJapaneseQueryAndArchiveScope(t *testing.T) {
	for _, folder := range []application.MailFolder{
		{Kind: application.MailFolderDistinguished, ID: "archive"},
		{Kind: application.MailFolderOpaque, ID: "synthetic-archive-folder"},
	} {
		t.Run(folder.ID, func(t *testing.T) {
			const query = `subject:"四半期 計画" received:2019-01-01..2020-12-31`
			client := cappedSearchClient(t, 53, folder, query)
			for _, window := range [][2]int{{0, 50}, {50, 1}, {51, 1}, {52, 1}, {53, 1}} {
				page, err := client.SearchMessages(t.Context(), application.MailSearchInput{
					Account: "work", Folder: folder, Query: query, Offset: window[0], Limit: window[1],
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Messages) != min(window[1], 53-window[0]) || page.IncludesLastItem != (window[0] >= 52) {
					t.Fatalf("window %v: incorrect page %+v", window, page)
				}
			}
		})
	}
}

type owaProjectionReader struct {
	application.ProjectionReader
	client *Client
}

const paginationAccount domain.AccountID = "acc_00000000000000000000000000000001"

func (reader owaProjectionReader) ProjectionAccounts(context.Context) ([]application.ProjectionAccount, error) {
	return []application.ProjectionAccount{{
		Account: paginationAccount, Alias: "synthetic", MailProvider: domain.ProviderMicrosoftOWA, Authenticated: true,
		Capabilities: &domain.Capabilities{Mail: true},
		Services: application.ServiceAuthenticationStatuses{Mail: &application.ServiceAuthenticationStatus{
			Service: application.AuthenticationServiceMail, Provider: domain.ProviderMicrosoftOWA,
			State: application.AuthenticationStateAuthenticated,
		}},
	}}, nil
}

func TestSearchRejectsFalseTerminalAndStalledPages(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				items := make([]findItemMessage, count)
				if count != 0 {
					items[0].ItemID.ID = "synthetic-result"
				}
				_ = json.NewEncoder(w).Encode(responseEnvelope[findItemResponseBody]{Body: findItemResponseBody{
					ResponseMessages: responseMessages[findItemResponseMessage]{Items: []findItemResponseMessage{{
						ResponseClass: "Success", ResponseCode: "NoError",
						RootFolder: findItemFolder{Items: items, TotalItemsInView: 53, IncludesLastItem: true},
					}}},
				}})
			}))
			defer server.Close()
			page, err := testClient(t, server, nil).SearchMessages(t.Context(), application.MailSearchInput{
				Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
				Query: "synthetic", Offset: 50, Limit: 1,
			})
			if count == 0 {
				if err == nil {
					t.Fatal("stalled search returned successful exhaustion")
				}
			} else if err != nil || page.IncludesLastItem {
				t.Fatalf("contradictory terminal flag was trusted: %+v, %v", page, err)
			}
		})
	}
}

func TestSearchWindowBounds(t *testing.T) {
	input := listMessagesRequest{
		Folder: folderRef{kind: folderDistinguished, id: "inbox"}, Query: "synthetic",
		SearchFolderIdentity: "00112233-4455-4677-8899-aabbccddeeff",
		Offset:               application.MaxMailOffset, Limit: application.MaxMailSearchPageSize,
	}
	envelope, err := buildFindItemEnvelope(input)
	if err != nil || envelope.Body.QueryString.MaxResultsCount != 10051 || envelope.Body.Paging.MaxEntriesReturned != 50 {
		t.Fatalf("maximum bounded window: %+v, %v", envelope, err)
	}
	input.Offset++
	if _, err := buildFindItemEnvelope(input); err == nil {
		t.Fatal("out-of-range offset accepted")
	}
}

func (reader owaProjectionReader) SearchMail(ctx context.Context, input application.MailSearchInput, _ domain.Caller) (application.MailPage, error) {
	page, err := reader.client.SearchMessages(ctx, input)
	for index := range page.Messages {
		page.Messages[index].Provenance = domain.Provenance{
			AccountID: input.Account, Provider: domain.ProviderMicrosoftOWA,
			MailboxID: input.Folder.ID, SourceObjectID: page.Messages[index].ID,
		}
	}
	return page, err
}

func TestSearchProjectionDoesNotReportFalseExhaustion(t *testing.T) {
	folder := application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}
	client := cappedSearchClient(t, 113, folder, "synthetic")
	service, err := application.NewProjectionService(owaProjectionReader{client: client})
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{0, 50, 100} {
		page, err := service.SearchAllMail(t.Context(), application.MailProjectionInput{
			Folder: folder, Query: "synthetic", Offset: offset, Limit: 50, TimeZone: "UTC",
		}, domain.Caller{Surface: "mcp", Instance: "synthetic-test"})
		if err != nil {
			t.Fatal(err)
		}
		if !page.Complete || page.HasMore != (offset < 100) || len(page.Messages) != min(50, 113-offset) ||
			len(page.Accounts) != 1 || page.Accounts[0].FetchedItems != 113 || !page.Accounts[0].Exhausted {
			t.Fatalf("offset %d: incorrect projection %+v", offset, page)
		}
	}
}
