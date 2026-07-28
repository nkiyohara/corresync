package outlookweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func testDraftInput() application.MailDraftInput {
	return application.MailDraftInput{
		Account: "work", To: []string{"alice@example.invalid"},
		Subject: "Synthetic draft", Body: "Draft body",
	}
}

func TestCreateDraftRequestMatchesGoldenFixture(t *testing.T) {
	t.Parallel()

	actual := marshalJSON(t, buildCreateDraftEnvelope(testDraftInput()))
	want := readFixture(t, "create_draft_request.json")
	assertJSONEqual(t, actual, want)
}

func TestCreateDraftBuildsHTMLReplyAllContract(t *testing.T) {
	t.Parallel()

	input := application.MailDraftInput{
		Account: "work", Body: "<p>Synthetic reply</p>", BodyFormat: application.MailBodyHTML,
		ComposeMode:        application.MailComposeReplyAll,
		ReferenceMessageID: "message-1", ReferenceChangeKey: "change-1",
	}
	payload := buildCreateDraftEnvelope(input)
	if payload.Body.ComposeOperation != "replyAll" {
		t.Fatalf("compose operation = %q", payload.Body.ComposeOperation)
	}
	item, ok := payload.Body.Items[0].(responseMessage)
	if !ok || item.Type != "ReplyAllToItem:#Exchange" || item.NewBodyContent.BodyType != "HTML" ||
		item.ReferenceItemID.ID != "message-1" || item.ReferenceItemID.ChangeKey != "change-1" {
		t.Fatalf("unexpected reply item: %+v", payload.Body.Items[0])
	}
}

func TestCreateMailDraftNormalizesResponseAndNeverRetries(t *testing.T) {
	t.Parallel()

	fixture := readFixture(t, "create_draft_response.json")
	expectedRequest := readFixture(t, "create_draft_request.json")
	requestBodies := make(chan []byte, 1)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
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
	draft, err := client.CreateMailDraft(context.Background(), testDraftInput())
	if err != nil {
		t.Fatalf("CreateMailDraft() error = %v", err)
	}
	assertJSONEqual(t, <-requestBodies, expectedRequest)
	if draft.ID != "synthetic-draft-1" || draft.ChangeKey != "synthetic-change-1" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if calls.Load() != 1 {
		t.Fatalf("CreateItem calls = %d, want exactly 1", calls.Load())
	}
}

func TestCreateMailDraftMarksMalformedSuccessResponseUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"Body":{"ResponseMessages":{"Items":[{"ResponseClass":"Success","ResponseCode":"NoError","Items":[]}]}}}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	_, err := client.CreateMailDraft(t.Context(), testDraftInput())
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("CreateMailDraft() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}
