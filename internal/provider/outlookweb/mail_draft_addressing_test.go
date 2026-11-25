package outlookweb

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func owaRecipients(addresses ...string) string {
	var buffer bytes.Buffer
	buffer.WriteByte('[')
	for index, address := range addresses {
		if index != 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(`{"Name":"Synthetic","EmailAddress":"` + address +
			`","RoutingType":"SMTP","MailboxType":"Mailbox"}`)
	}
	buffer.WriteByte(']')
	return buffer.String()
}

func owaGetItemResponse(item string) []byte {
	return []byte(`{"Body":{"ResponseMessages":{"Items":[{"ResponseClass":"Success","ResponseCode":"NoError","Items":[` +
		item + `]}]}}}`)
}

func TestCreateMailDraftReportsExchangeResolvedAddressing(t *testing.T) {
	t.Parallel()

	const self = "me@example.invalid"
	sourceItem := `{"ItemId":{"Id":"source-1","ChangeKey":"source-version-1"},` +
		`"ToRecipients":` + owaRecipients(self, "bob@example.invalid") + `,` +
		`"CcRecipients":` + owaRecipients("carol@example.invalid") + `,` +
		`"ReceivedBy":{"Mailbox":{"Name":"Me","EmailAddress":"` + self + `"}}}`
	draftFrom := `"From":{"Mailbox":{"Name":"Me","EmailAddress":"` + self + `"}},`
	tests := []struct {
		name         string
		mode         application.MailComposeMode
		draftItem    string
		sourceItem   string
		wantTo       []string
		wantCC       []string
		wantSubject  string
		wantSource   []string
		sourceKnown  bool
		wantGetItems int32
	}{
		{
			name: "ordinary reply", mode: application.MailComposeReply,
			draftItem:  draftFrom + `"Subject":"Re: Plan","ToRecipients":` + owaRecipients("alice@example.invalid"),
			sourceItem: sourceItem,
			wantTo:     []string{"alice@example.invalid"}, wantCC: []string{}, wantSubject: "Re: Plan",
			wantSource: []string{"bob@example.invalid", "carol@example.invalid"}, sourceKnown: true,
			wantGetItems: 2,
		},
		{
			name: "reply-all preserves To and Cc", mode: application.MailComposeReplyAll,
			draftItem: draftFrom + `"Subject":"Re: Plan","ToRecipients":` +
				owaRecipients("alice@example.invalid", "bob@example.invalid") +
				`,"CcRecipients":` + owaRecipients("carol@example.invalid"),
			sourceItem: sourceItem,
			wantTo:     []string{"alice@example.invalid", "bob@example.invalid"},
			wantCC:     []string{"carol@example.invalid"}, wantSubject: "Re: Plan",
			wantSource: []string{"bob@example.invalid", "carol@example.invalid"}, sourceKnown: true,
			wantGetItems: 2,
		},
		{
			name: "unknown self leaves source participants unknown", mode: application.MailComposeReply,
			draftItem: `"Subject":"Re: Plan","ToRecipients":` + owaRecipients("alice@example.invalid"),
			sourceItem: `{"ItemId":{"Id":"source-1","ChangeKey":"source-version-1"},` +
				`"ToRecipients":` + owaRecipients(self, "bob@example.invalid") + `}`,
			wantTo: []string{"alice@example.invalid"}, wantCC: []string{}, wantSubject: "Re: Plan",
			wantGetItems: 2,
		},
		{
			name: "stale source version leaves participants unknown", mode: application.MailComposeReply,
			draftItem:  draftFrom + `"Subject":"Re: Plan","ToRecipients":` + owaRecipients("alice@example.invalid"),
			sourceItem: `{"ItemId":{"Id":"source-1","ChangeKey":"source-version-2"}}`,
			wantTo:     []string{"alice@example.invalid"}, wantCC: []string{}, wantSubject: "Re: Plan",
			wantGetItems: 2,
		},
		{
			name: "forward reads only the draft", mode: application.MailComposeForward,
			draftItem: draftFrom + `"Subject":"Fwd: Plan","ToRecipients":` + owaRecipients("erin@example.invalid"),
			wantTo:    []string{"erin@example.invalid"}, wantCC: []string{}, wantSubject: "Fwd: Plan",
			wantGetItems: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var creates, reads atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("ReadAll() error = %v", err)
					return
				}
				switch request.URL.Query().Get("action") {
				case "CreateItem":
					creates.Add(1)
					_, _ = writer.Write(readFixture(t, "create_draft_response.json"))
				case "GetItem":
					reads.Add(1)
					if bytes.Contains(body, []byte("BccRecipients")) || bytes.Contains(body, []byte("item:Body")) {
						t.Errorf("addressing read requested Bcc or body: %s", body)
					}
					switch {
					case bytes.Contains(body, []byte(`"Id":"synthetic-draft-1","ChangeKey":"synthetic-change-1"`)):
						_, _ = writer.Write(owaGetItemResponse(
							`{"ItemId":{"Id":"synthetic-draft-1","ChangeKey":"synthetic-change-1"},"IsDraft":true,` +
								test.draftItem + `}`,
						))
					case bytes.Contains(body, []byte(`"Id":"source-1","ChangeKey":"source-version-1"`)):
						_, _ = writer.Write(owaGetItemResponse(test.sourceItem))
					default:
						t.Errorf("read did not bind an exact identity: %s", body)
					}
				default:
					t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
				}
			}))
			defer server.Close()

			input := application.MailDraftInput{
				Account: "work", Body: "Synthetic", ComposeMode: test.mode,
				ReferenceMessageID: "source-1", ReferenceChangeKey: "source-version-1",
			}
			if test.mode == application.MailComposeForward {
				input.To = []string{"erin@example.invalid"}
			}
			draft, err := testClient(t, server, nil).CreateMailDraft(t.Context(), input)
			if err != nil {
				t.Fatalf("CreateMailDraft() error = %v", err)
			}
			if creates.Load() != 1 || reads.Load() != test.wantGetItems {
				t.Fatalf("CreateItem=%d GetItem=%d", creates.Load(), reads.Load())
			}
			addressing := draft.Addressing
			if addressing == nil {
				t.Fatal("addressing was not reported")
			}
			if !slices.Equal(addressing.To, test.wantTo) || !slices.Equal(addressing.CC, test.wantCC) ||
				addressing.Subject != test.wantSubject {
				t.Fatalf("addressing = %+v", addressing)
			}
			if addressing.SourceParticipantsKnown != test.sourceKnown ||
				(test.sourceKnown && !slices.Equal(addressing.SourceParticipants, test.wantSource)) {
				t.Fatalf("source participants = %v known=%t", addressing.SourceParticipants, addressing.SourceParticipantsKnown)
			}
		})
	}
}

func TestCreateMailDraftLeavesAddressingUnresolvedWhenReadBackFails(t *testing.T) {
	t.Parallel()

	responses := map[string]func(http.ResponseWriter){
		"provider error": writeGetItemFailure,
		"different version": func(writer http.ResponseWriter) {
			_, _ = writer.Write(owaGetItemResponse(
				`{"ItemId":{"Id":"synthetic-draft-1","ChangeKey":"synthetic-change-9"},"IsDraft":true}`,
			))
		},
		"not a draft": func(writer http.ResponseWriter) {
			_, _ = writer.Write(owaGetItemResponse(
				`{"ItemId":{"Id":"synthetic-draft-1","ChangeKey":"synthetic-change-1"},"IsDraft":false}`,
			))
		},
	}
	for name, respond := range responses {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var creates atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Query().Get("action") {
				case "CreateItem":
					creates.Add(1)
					_, _ = writer.Write(readFixture(t, "create_draft_response.json"))
				case "GetItem":
					respond(writer)
				default:
					t.Errorf("unexpected action %q", request.URL.Query().Get("action"))
				}
			}))
			defer server.Close()
			draft, err := testClient(t, server, nil).CreateMailDraft(t.Context(), testDraftInput())
			if err != nil || draft.ID != "synthetic-draft-1" || creates.Load() != 1 {
				t.Fatalf("draft=%+v creates=%d error=%v", draft, creates.Load(), err)
			}
			if draft.Addressing != nil {
				t.Fatalf("unconfirmed addressing was reported: %+v", draft.Addressing)
			}
		})
	}
}
