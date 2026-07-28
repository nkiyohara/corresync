package outlookweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func validFolderListInput() application.MailFolderListInput {
	return application.MailFolderListInput{
		Account:   "work",
		Parent:    application.MailFolder{Kind: application.MailFolderDistinguished, ID: "msgfolderroot"},
		Traversal: application.MailFolderTraversalDeep,
		Limit:     100,
		TimeZone:  "UTC",
	}
}

func TestFindFolderRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	payload, err := buildFindFolderEnvelope(validFolderListInput())
	if err != nil {
		t.Fatalf("buildFindFolderEnvelope() error = %v", err)
	}
	actual, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, actual, readFixture(t, "find_folder_request.json"))
}

func TestListMailFoldersNormalizesGoldenResponse(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "find_folder_response.json")
	expectedRequest := readFixture(t, "find_folder_request.json")
	requests := make(chan []byte, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		requests <- body
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	page, err := client.ListMailFolders(context.Background(), validFolderListInput())
	if err != nil {
		t.Fatalf("ListMailFolders() error = %v", err)
	}
	assertJSONEqual(t, <-requests, expectedRequest)
	if len(page.Folders) != 2 || page.TotalFolders != 2 || !page.IncludesLastItem {
		t.Fatalf("unexpected page: %+v", page)
	}
	if page.Folders[0].ID != "synthetic-folder-1" || page.Folders[0].UnreadItemCount != 3 ||
		page.Folders[1].DistinguishedID != "archive" {
		t.Fatalf("unexpected folders: %+v", page.Folders)
	}
}

func TestListMailFoldersValidatesBeforeNetwork(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server must not be called for invalid input")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	input := validFolderListInput()
	input.Limit = 101
	if _, err := client.ListMailFolders(context.Background(), input); err == nil {
		t.Fatal("ListMailFolders() unexpectedly accepted an invalid limit")
	}
}

func TestListMailFoldersRejectsInvalidResponseCounts(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{"ResponseClass":"Success","ResponseCode":"NoError","RootFolder":{"Folders":[{"FolderId":{"Id":"folder"},"TotalCount":-1}],"TotalItemsInView":1,"IncludesLastItemInRange":true}}]}}}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	if _, err := client.ListMailFolders(context.Background(), validFolderListInput()); err == nil {
		t.Fatal("ListMailFolders() accepted a negative folder count")
	}
}
