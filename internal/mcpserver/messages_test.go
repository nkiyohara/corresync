package mcpserver

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

var expectedMessagingToolNames = []string{
	"conversation_create",
	"conversation_create_commit",
	"conversation_membership",
	"conversation_membership_commit",
	"message_conversations",
	"message_delete",
	"message_delete_commit",
	"message_edit",
	"message_edit_commit",
	"message_get",
	"message_get_attachment",
	"message_get_attachment_commit",
	"message_get_commit",
	"message_list",
	"message_react",
	"message_react_commit",
	"message_search",
	"message_send",
	"message_send_commit",
	"message_sync",
}

func TestPendingMessagingSurfaceHasTypedToolsAndResources(t *testing.T) {
	t.Parallel()

	backend := &fakeMessagingBackend{fakeBackend: &fakeBackend{}}
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "dev"}, nil)
	addMessagingSurface(server, backend, domain.Caller{Surface: "mcp", Instance: "message-test"})
	client := connectTestClient(t, server)

	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Title == "" || tool.Description == "" || tool.Annotations == nil ||
			tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Errorf("messaging tool metadata is incomplete: %+v", tool)
		}
		resolveSchema(t, tool.InputSchema)
		resolveSchema(t, tool.OutputSchema)
	}
	sort.Strings(names)
	if !slices.Equal(names, expectedMessagingToolNames) {
		t.Fatalf("messaging tools = %#v, want %#v", names, expectedMessagingToolNames)
	}

	templates, err := client.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTemplates := map[string]string{
		"corresync://conversations/{account}/{workspace}":           "message_conversations",
		"corresync://messages/{account}/{workspace}/{conversation}": "messages",
	}
	if len(templates.ResourceTemplates) != len(wantTemplates) {
		t.Fatalf("messaging resource count = %d, want %d", len(templates.ResourceTemplates), len(wantTemplates))
	}
	for _, template := range templates.ResourceTemplates {
		if wantTemplates[template.URITemplate] != template.Name || template.Title == "" ||
			template.Description == "" || template.MIMEType != "application/json" {
			t.Errorf("unexpected messaging resource template: %+v", template)
		}
	}
}

func TestMessagingToolsBindAccountAndDecodeAttachments(t *testing.T) {
	t.Parallel()

	backend := &fakeMessagingBackend{fakeBackend: &fakeBackend{}}
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "dev"}, nil)
	addMessagingTools(server, backend, domain.Caller{Surface: "mcp", Instance: "message-routing"})
	client := connectTestClient(t, server)
	account := "acc_00000000000000000000000000000001"
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "message_send",
		Arguments: map[string]any{
			"account": account, "workspaceId": "workspace-1",
			"actor":          map[string]any{"id": "actor-1", "mode": "delegated_user"},
			"conversationId": "conversation-1",
			"content":        map[string]any{"format": "plain", "text": "Synthetic message"},
			"attachments": []any{map[string]any{
				"name": "synthetic.txt", "mediaType": "text/plain", "contentBase64": "c3ludGhldGlj",
			}},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("message_send result = %+v, error = %v", result, err)
	}
	if backend.sendInput.Account != domain.AccountID(account) ||
		backend.sendInput.WorkspaceID != "workspace-1" ||
		backend.sendInput.ConversationID != "conversation-1" ||
		len(backend.sendInput.Attachments) != 1 || string(backend.sendInput.Attachments[0].Data) != "synthetic" {
		t.Fatalf("message send route or attachment changed: %+v", backend.sendInput)
	}

	result, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "message_send_commit", Arguments: map[string]any{"token": "approval-message-1"},
	})
	if err != nil || result.IsError || backend.commitToken != "approval-message-1" {
		t.Fatalf("message_send_commit result = %+v, error = %v, token = %q", result, err, backend.commitToken)
	}
}

func TestMessagingResourcesRemainMetadataOnlyAndRouteBound(t *testing.T) {
	t.Parallel()

	backend := &fakeMessagingBackend{fakeBackend: &fakeBackend{}}
	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "dev"}, nil)
	addMessagingSurface(server, backend, domain.Caller{Surface: "mcp", Instance: "message-resource"})
	client := connectTestClient(t, server)
	uri := "corresync://messages/acc_00000000000000000000000000000001/workspace-1/conversation-1"
	result, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 || result.Contents[0].URI != uri {
		t.Fatalf("messaging resource result = %+v", result)
	}
	if backend.listInput.Account != "acc_00000000000000000000000000000001" ||
		backend.listInput.WorkspaceID != "workspace-1" ||
		backend.listInput.ConversationID != "conversation-1" || backend.listInput.Limit != 50 {
		t.Fatalf("messaging resource route changed: %+v", backend.listInput)
	}
	if _, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: "corresync://messages/acc_00000000000000000000000000000001/workspace%2Fescape/conversation-1",
	}); err == nil {
		t.Fatal("messaging resource accepted an encoded route separator")
	}
}

func TestMessageAttachmentInputsAreStrictlyBounded(t *testing.T) {
	t.Parallel()

	if _, err := decodeMessageAttachments([]MessageAttachmentUploadInput{{
		Name: "synthetic.txt", ContentBase64: "not-base64!",
	}}); err == nil {
		t.Fatal("messaging attachment accepted malformed base64")
	}
	tooMany := make([]MessageAttachmentUploadInput, application.MaxMessageCollectionItems+1)
	if _, err := decodeMessageAttachments(tooMany); err == nil {
		t.Fatal("messaging attachments accepted an unbounded collection")
	}
}

type fakeMessagingBackend struct {
	*fakeBackend
	listInput   application.MessageListInput
	sendInput   application.MessageSendInput
	commitToken string
}

func (backend *fakeMessagingBackend) ListConversations(context.Context, application.ConversationListInput, domain.Caller) (application.ConversationPage, error) {
	return application.ConversationPage{}, nil
}

func (backend *fakeMessagingBackend) ListMessages(_ context.Context, input application.MessageListInput, _ domain.Caller) (application.MessagePage, error) {
	backend.listInput = input
	return application.MessagePage{}, nil
}

func (backend *fakeMessagingBackend) SearchMessages(context.Context, application.MessageSearchInput, domain.Caller) (application.MessagePage, error) {
	return application.MessagePage{}, nil
}

func (backend *fakeMessagingBackend) GetMessage(context.Context, application.MessageGetInput, domain.Caller) (application.MessageSensitiveAccess, error) {
	return application.MessageSensitiveAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitGetMessage(context.Context, string, domain.Caller) (application.MessageSensitiveAccess, error) {
	return application.MessageSensitiveAccess{}, nil
}

func (backend *fakeMessagingBackend) GetMessageAttachment(context.Context, application.MessageAttachmentGetInput, domain.Caller) (application.MessageSensitiveAccess, error) {
	return application.MessageSensitiveAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitGetMessageAttachment(context.Context, string, domain.Caller) (application.MessageSensitiveAccess, error) {
	return application.MessageSensitiveAccess{}, nil
}

func (backend *fakeMessagingBackend) SyncMessages(context.Context, application.MessageSyncInput, domain.Caller) (application.MessageChangePage, error) {
	return application.MessageChangePage{}, nil
}

func (backend *fakeMessagingBackend) SendMessage(_ context.Context, input application.MessageSendInput, _ domain.Caller) (application.MessageWriteAccess, error) {
	backend.sendInput = input
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitSendMessage(_ context.Context, token string, _ domain.Caller) (application.MessageWriteAccess, error) {
	backend.commitToken = token
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) EditMessage(context.Context, application.MessageEditInput, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitEditMessage(context.Context, string, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) DeleteMessage(context.Context, application.MessageDeleteInput, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitDeleteMessage(context.Context, string, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) ReactToMessage(context.Context, application.MessageReactionInput, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitMessageReaction(context.Context, string, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CreateConversation(context.Context, application.ConversationCreateInput, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitCreateConversation(context.Context, string, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) ChangeConversationMembership(context.Context, application.ConversationMembershipInput, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}

func (backend *fakeMessagingBackend) CommitConversationMembership(context.Context, string, domain.Caller) (application.MessageWriteAccess, error) {
	return application.MessageWriteAccess{}, nil
}
