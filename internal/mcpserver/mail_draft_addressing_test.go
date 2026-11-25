package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestMailDraftToolsExplainReplyModesAndAddressing(t *testing.T) {
	t.Parallel()

	server, err := New(&fakeBackend{}, Options{Version: "dev", Instance: "test-server"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tools, err := connectTestClient(t, server).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	tests := []struct {
		name string
		want []string
	}{
		{name: "mail_create_draft", want: []string{
			"reply (only the source sender or Reply-To)",
			"reply_all (the sender plus the other visible To/Cc participants)",
			"intended audience", "unknown rather than empty", "sourceParticipantsNotIncluded",
			`"addressing"`, `"resolved"`,
		}},
		{name: "mail_create_draft_commit", want: []string{"same addressing review", `"addressing"`}},
		{name: "mail_send", want: []string{"reply (source sender only)", "reply_all (sender plus the other To/Cc)"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tool := toolNamed(tools.Tools, test.name)
			if tool == nil {
				t.Fatalf("missing tool %q", test.name)
			}
			input, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			output, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			catalog := tool.Description + string(input) + string(output)
			for _, want := range test.want {
				if !strings.Contains(catalog, want) {
					t.Errorf("%s catalog is missing %q", test.name, want)
				}
			}
			if strings.HasPrefix(test.name, "mail_create_draft") &&
				tool.Meta["io.github.nkiyohara.corresync/data-classification"] != "private-untrusted" {
				t.Errorf("%s classification = %v", test.name, tool.Meta["io.github.nkiyohara.corresync/data-classification"])
			}
		})
	}
}

func TestMailCreateDraftStructuredAddressingMatchesOutputSchema(t *testing.T) {
	t.Parallel()

	omitted := 1
	tests := map[string]*application.MailDraftAddressingReview{
		"unresolved": {Status: application.MailAddressingUnresolved},
		"verified empty": {Status: application.MailAddressingResolved, Resolved: &application.MailResolvedAddressing{
			To: []string{}, CC: []string{}, Subject: "Notes",
		}},
		"reply drops a participant": {Status: application.MailAddressingResolved, Resolved: &application.MailResolvedAddressing{
			To: []string{"alice@example.invalid"}, CC: []string{}, Subject: "Re: Plan",
			SourceParticipantsNotIncluded: &omitted,
		}},
	}
	for name, addressing := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeBackend{draftAccess: application.MailDraftAccess{
				Status: "completed", Draft: &application.MailDraft{ID: "draft-1"},
				Review: application.MailReview{ComposeMode: application.MailComposeReply}, Addressing: addressing,
			}}
			server, err := New(backend, Options{Version: "dev", Instance: "test-server"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := connectTestClient(t, server).CallTool(t.Context(), &mcp.CallToolParams{
				Name: "mail_create_draft",
				Arguments: map[string]any{
					"composeMode": "reply", "referenceMessageId": "source-1",
					"referenceChangeKey": "source-version-1", "body": "Hello",
				},
			})
			if err != nil || result.IsError {
				t.Fatalf("mail_create_draft failed: result=%+v error=%v", result, err)
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var decoded application.MailDraftAccess
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Addressing == nil || decoded.Addressing.Status != addressing.Status ||
				(decoded.Addressing.Resolved == nil) != (addressing.Resolved == nil) {
				t.Fatalf("structured addressing = %s", encoded)
			}
		})
	}
}
