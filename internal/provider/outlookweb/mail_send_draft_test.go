package outlookweb

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestExactDraftSnapshotAndSendUseTheReviewedChangeKey(t *testing.T) {
	t.Parallel()
	input := application.MailDraftSendInput{
		Account: "work", DraftID: "draft-1", DraftChangeKey: "change-1",
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		if !bytes.Contains(body, []byte(`"Id":"draft-1"`)) ||
			!bytes.Contains(body, []byte(`"ChangeKey":"change-1"`)) {
			t.Errorf("request did not bind exact draft identity: %s", body)
		}
		switch request.URL.Query().Get("action") {
		case "GetItem":
			if call != 1 || !bytes.Contains(body, []byte(`"message:IsDraft"`)) {
				t.Errorf("snapshot request = %s", body)
			}
			_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError",
            "Items":[{
              "ItemId":{"Id":"draft-1","ChangeKey":"change-1"},
              "Subject":"Reviewed draft","IsDraft":true,
              "Body":{"BodyType":"Text","Value":"Exact saved body"},
              "ToRecipients":[{"Mailbox":{"EmailAddress":"alice@example.invalid"}}],
              "CcRecipients":[],"BccRecipients":[],
              "Attachments":[{
                "__type":"FileAttachment:#Exchange",
                "AttachmentId":{"Id":"attachment-1"},
                "Name":"fixture.txt","ContentType":"text/plain","Size":7,
                "IsInline":false,"ContentId":""
              }]
            }]
          }]}}
        }`))
		case "SendItem":
			if call != 2 || !bytes.Contains(body, []byte(`"SaveItemToFolder":true`)) {
				t.Errorf("send request = %s", body)
			}
			_, _ = writer.Write([]byte(`{
          "Body":{"ResponseMessages":{"Items":[{
            "ResponseClass":"Success","ResponseCode":"NoError"
          }]}}
        }`))
		default:
			t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
		}
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	snapshot, err := client.GetMailDraftSnapshot(t.Context(), input)
	if err != nil {
		t.Fatalf("GetMailDraftSnapshot() error = %v", err)
	}
	if snapshot.ID != input.DraftID || snapshot.ChangeKey != input.DraftChangeKey ||
		len(snapshot.To) != 1 || snapshot.To[0] != "alice@example.invalid" ||
		snapshot.Body != "Exact saved body" || len(snapshot.Attachments) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if _, err := client.SendMailDraft(t.Context(), input); err != nil {
		t.Fatalf("SendMailDraft() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestDraftSnapshotRejectsChangedResponseIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
      "Body":{"ResponseMessages":{"Items":[{
        "ResponseClass":"Success","ResponseCode":"NoError",
        "Items":[{
          "ItemId":{"Id":"draft-1","ChangeKey":"change-2"},
          "IsDraft":true,"Body":{"BodyType":"Text","Value":"changed"},
          "ToRecipients":[{"Mailbox":{"EmailAddress":"alice@example.invalid"}}]
        }]
      }]}}
    }`))
	}))
	defer server.Close()

	_, err := testClient(t, server, nil).GetMailDraftSnapshot(
		t.Context(),
		application.MailDraftSendInput{
			Account: "work", DraftID: "draft-1", DraftChangeKey: "change-1",
		},
	)
	if err == nil {
		t.Fatal("GetMailDraftSnapshot() accepted a changed response identity")
	}
}
