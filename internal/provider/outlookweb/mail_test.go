package outlookweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestFindItemRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildFindItemEnvelope(listMessagesRequest{
		Folder: folderRef{kind: folderDistinguished, id: "inbox"},
		Limit:  25,
	})
	if err != nil {
		t.Fatalf("buildFindItemEnvelope() error = %v", err)
	}
	actual, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := readFixture(t, "find_item_request.json")
	assertJSONEqual(t, actual, want)
	if !bytes.HasPrefix(actual, []byte(`{"__type":`)) {
		t.Fatalf("request does not encode __type first: %s", actual)
	}
}

func TestSearchFindItemRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildFindItemEnvelope(listMessagesRequest{
		Folder:               folderRef{kind: folderDistinguished, id: "inbox"},
		Query:                `subject:"Quarterly plan" from:alice`,
		SearchFolderIdentity: "00112233-4455-4677-8899-aabbccddeeff",
		Offset:               5,
		Limit:                25,
		TimeZone:             "UTC",
	})
	if err != nil {
		t.Fatalf("buildFindItemEnvelope() error = %v", err)
	}
	actual, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, actual, readFixture(t, "search_item_request.json"))
}

func TestListMessagesNormalizesGoldenResponse(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "find_item_response.json")
	expectedRequest := readFixture(t, "find_item_request.json")
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
	page, err := client.ListMessages(context.Background(), application.MailListInput{
		Account: "work",
		Folder:  application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit:   25,
	})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if len(page.Messages) != 2 || page.TotalItemsInView != 42 || page.IncludesLastItem {
		t.Fatalf("unexpected page: %+v", page)
	}
	first := page.Messages[0]
	if first.ID != "synthetic-message-1" || first.From.Address != "alice@example.invalid" ||
		!first.HasAttachments || first.IsRead {
		t.Fatalf("unexpected first message: %+v", first)
	}
}

func TestListMessagesReturnsSanitizedProtocolError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Error",
            "ResponseCode":"ErrorAccessDenied",
            "MessageText":"private server detail"
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.ListMessages(context.Background(), application.MailListInput{
		Account: "work",
		Folder:  application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit:   25,
	})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.ResponseCode != "ErrorAccessDenied" {
		t.Fatalf("ListMessages() error = %v, want ErrorAccessDenied", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte("private")) {
		t.Fatalf("ProtocolError exposed server detail: %v", err)
	}
}

func TestListMessagesValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	for _, input := range []application.MailListInput{
		{},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Limit: 101},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Limit: 25, Offset: -1},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Limit: 25, TimeZone: "UTC\nInjected"},
	} {
		if _, err := client.ListMessages(context.Background(), input); err == nil {
			t.Fatalf("ListMessages(%+v) unexpectedly succeeded", input)
		}
	}
}

func TestListMessagesRejectsUnsupportedOWAWindowBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for an unsupported OWA result window")
	}))
	defer server.Close()
	client := testClient(t, server, nil)

	_, err := client.ListMessages(context.Background(), application.MailListInput{
		Account:  "work",
		Folder:   application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit:    100,
		TimeZone: "UTC",
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("use offset to request another page")) {
		t.Fatalf("ListMessages() error = %v, want actionable OWA paging error", err)
	}
}

func TestMailReadsRejectIANATimeZoneBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for an IANA OWA mail time zone")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	folder := application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}

	_, listErr := client.ListMessages(context.Background(), application.MailListInput{
		Account: "work", Folder: folder, Limit: 25, TimeZone: "Europe/London",
	})
	if listErr == nil || !bytes.Contains([]byte(listErr.Error()), []byte("Exchange/Windows identifier")) {
		t.Fatalf("ListMessages() error = %v, want actionable time-zone error", listErr)
	}

	_, searchErr := client.SearchMessages(context.Background(), application.MailSearchInput{
		Account: "work", Folder: folder, Query: "synthetic", Limit: 25, TimeZone: "Europe/London",
	})
	if searchErr == nil || !bytes.Contains([]byte(searchErr.Error()), []byte("Exchange/Windows identifier")) {
		t.Fatalf("SearchMessages() error = %v, want actionable time-zone error", searchErr)
	}
}

func TestListMessagesPreservesOpaqueFolderID(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "find_item_response.json")
	requestBodies := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requestBodies <- body
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	const folderID = "AAMkCaseSensitiveFolderID=="
	_, err := client.ListMessages(context.Background(), application.MailListInput{
		Account: "work",
		Folder:  application.MailFolder{Kind: application.MailFolderOpaque, ID: folderID},
		Limit:   25,
	})
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	var request struct {
		Body struct {
			ParentFolderIDs []struct {
				Type string `json:"__type"`
				ID   string `json:"Id"`
			} `json:"ParentFolderIds"`
		} `json:"Body"`
	}
	if err := json.Unmarshal(<-requestBodies, &request); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(request.Body.ParentFolderIDs) != 1 ||
		request.Body.ParentFolderIDs[0].ID != folderID ||
		request.Body.ParentFolderIDs[0].Type != "FolderId:#Exchange" {
		t.Fatalf("opaque folder ID changed in request: %+v", request.Body.ParentFolderIDs)
	}
}

func TestSearchMessagesUsesBoundedTypedContract(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "find_item_response.json")
	requestBodies := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requestBodies <- body
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	page, err := client.SearchMessages(context.Background(), application.MailSearchInput{
		Account:  "work",
		Folder:   application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Query:    `subject:"Quarterly plan" from:alice`,
		Limit:    25,
		TimeZone: "UTC",
	})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if len(page.Messages) != 2 {
		t.Fatalf("unexpected search page: %+v", page)
	}
	var request struct {
		Body struct {
			ShapeName            string      `json:"ShapeName"`
			SearchFolderIdentity string      `json:"SearchFolderIdentity"`
			QueryString          queryString `json:"QueryString"`
		} `json:"Body"`
	}
	if err := json.Unmarshal(<-requestBodies, &request); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if request.Body.ShapeName != "MailListItem" || !validUUID(request.Body.SearchFolderIdentity) ||
		request.Body.QueryString.Value != `subject:"Quarterly plan" from:alice` ||
		request.Body.QueryString.MaxResultsCount != 25 || !request.Body.QueryString.WaitForSearchComplete ||
		request.Body.QueryString.ReturnDeletedItems || request.Body.QueryString.ReturnHighlightTerms {
		t.Fatalf("unexpected search request: %+v", request.Body)
	}
}

func TestSearchMessagesValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid search input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	for _, input := range []application.MailSearchInput{
		{},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Limit: 25, TimeZone: "UTC"},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Query: "hello", Limit: 51, TimeZone: "UTC"},
		{Account: "work", Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"}, Query: "hello\nworld", Limit: 25, TimeZone: "UTC"},
	} {
		if _, err := client.SearchMessages(context.Background(), input); err == nil {
			t.Fatalf("SearchMessages(%+v) unexpectedly succeeded", input)
		}
	}
}

func TestSearchMessagesClassifiesProviderServerFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := testClient(t, server, func(options *Options) {
		options.ReadAttempts = 1
	})

	_, err := client.SearchMessages(context.Background(), application.MailSearchInput{
		Account:  "work",
		Folder:   application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Query:    "synthetic",
		Limit:    25,
		TimeZone: "UTC",
	})
	if !errors.Is(err, ErrMailSearchUnavailable) {
		t.Fatalf("SearchMessages() error = %v, want ErrMailSearchUnavailable", err)
	}
	var httpFailure *HTTPError
	if !errors.As(err, &httpFailure) || httpFailure.StatusCode != http.StatusInternalServerError {
		t.Fatalf("SearchMessages() error = %v, want retained HTTP 500 metadata", err)
	}
}

func TestListMessagesRejectsOversizedResultWindow(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError",
            "RootFolder":{"TotalItemsInView":2,"IncludesLastItemInRange":false,
              "Items":[{"ItemId":{"Id":"message-1"}},{"ItemId":{"Id":"message-2"}}]}
          }]}}
        }`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.ListMessages(context.Background(), application.MailListInput{
		Account: "work",
		Folder:  application.MailFolder{Kind: application.MailFolderDistinguished, ID: "inbox"},
		Limit:   1,
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("invalid result window")) {
		t.Fatalf("ListMessages() error = %v, want invalid result window", err)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path) // #nosec G304 -- fixed testdata directory and test-selected fixture name.
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return data
}

func assertJSONEqual(t *testing.T, actual, expected []byte) {
	t.Helper()
	var actualValue any
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		t.Fatalf("actual JSON is invalid: %v\n%s", err, actual)
	}
	var expectedValue any
	if err := json.Unmarshal(expected, &expectedValue); err != nil {
		t.Fatalf("expected JSON is invalid: %v\n%s", err, expected)
	}
	actualNormalized, _ := json.Marshal(actualValue)
	expectedNormalized, _ := json.Marshal(expectedValue)
	if !bytes.Equal(actualNormalized, expectedNormalized) {
		t.Fatalf("JSON mismatch\nactual: %s\nexpected: %s", actual, expected)
	}
}
