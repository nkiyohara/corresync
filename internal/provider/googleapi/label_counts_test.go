package googleapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func TestGmailFolderCountsComeFromSelectedLabelDetails(t *testing.T) {
	t.Parallel()
	details := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gmail/v1/users/me/labels" {
			writeGoogleJSON(t, w, map[string]any{"labels": []gmailLabel{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me/labels/")
		details = append(details, id)
		writeGoogleJSON(t, w, map[string]any{"id": id, "messagesTotal": 12, "messagesUnread": 3})
	}))
	defer server.Close()
	api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{api: api, mail: true}
	defer func() { _ = client.Close() }()
	page, err := client.ListMailFolders(t.Context(), application.MailFolderListInput{Parent: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "msgfolderroot"}, Offset: 1, Limit: 1})
	if err != nil || len(page.Folders) != 1 {
		t.Fatalf("folders=%+v, %v", page, err)
	}
	if len(details) != 1 || details[0] != "b" || page.Folders[0].TotalItemCount != 12 || page.Folders[0].UnreadItemCount != 3 {
		t.Fatalf("page=%+v; detail requests=%v", page, details)
	}
}
func TestGmailFolderCountsRejectOmittedOrWrongIdentityDetails(t *testing.T) {
	t.Parallel()
	for _, detail := range []map[string]any{{"id": "a"}, {"id": "wrong", "messagesTotal": 12, "messagesUnread": 3}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/gmail/v1/users/me/labels" {
				writeGoogleJSON(t, w, map[string]any{"labels": []gmailLabel{{ID: "a", Name: "A"}}})
				return
			}
			writeGoogleJSON(t, w, detail)
		}))
		api, err := restapi.New(restapi.Options{BaseURL: server.URL, HTTP: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		client := &Client{api: api, mail: true}
		_, err = client.ListMailFolders(t.Context(), application.MailFolderListInput{Parent: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "msgfolderroot"}, Limit: 1})
		_ = client.Close()
		server.Close()
		if err == nil {
			t.Fatalf("accepted unavailable counts: %v", detail)
		}
	}
}
