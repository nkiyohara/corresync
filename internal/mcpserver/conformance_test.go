package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modernProtocolVersion = "2026-07-28"
	legacyProtocolVersion = "2025-11-25"
)

var expectedToolNames = []string{
	"account_add",
	"account_add_commit",
	"account_discover",
	"account_list",
	"account_remove",
	"account_remove_commit",
	"account_rename",
	"account_rename_commit",
	"account_show",
	"account_status",
	"agenda_list",
	"calendar_cancel",
	"calendar_cancel_commit",
	"calendar_create",
	"calendar_create_commit",
	"calendar_list",
	"calendar_list_folders",
	"calendar_update",
	"calendar_update_commit",
	"event_acknowledge",
	"events_list",
	"mail_create_draft",
	"mail_create_draft_commit",
	"mail_delete",
	"mail_delete_commit",
	"mail_get_attachment",
	"mail_get_attachment_commit",
	"mail_get_body",
	"mail_get_body_commit",
	"mail_list",
	"mail_list_folders",
	"mail_move",
	"mail_move_commit",
	"mail_search",
	"mail_search_all",
	"mail_send",
	"mail_send_commit",
	"mail_send_draft",
	"mail_send_draft_commit",
	"mail_set_read_state",
	"mail_set_read_state_commit",
	"monitor_status",
	"settings_show",
	"settings_update",
	"settings_update_commit",
	"task_complete",
	"task_complete_commit",
	"task_create",
	"task_create_commit",
	"task_delete",
	"task_delete_commit",
	"task_get",
	"task_list",
	"task_list_all",
	"task_lists",
	"task_reopen",
	"task_reopen_commit",
	"task_search",
	"task_sync",
	"task_update",
	"task_update_commit",
}

var (
	expectedReadOnlyTools = stringSet(
		"account_discover", "account_list", "account_show", "account_status", "settings_show",
		"monitor_status", "events_list", "calendar_list_folders", "calendar_list", "agenda_list",
		"mail_list_folders", "mail_get_body", "mail_get_body_commit", "mail_get_attachment",
		"mail_get_attachment_commit", "mail_list", "mail_search", "mail_search_all", "task_lists",
		"task_list", "task_list_all", "task_get", "task_search", "task_sync",
	)
	expectedDestructiveTools = stringSet(
		"account_remove", "account_remove_commit", "calendar_cancel_commit",
		"mail_delete", "mail_delete_commit", "task_delete", "task_delete_commit",
	)
	expectedClosedWorldTools = stringSet(
		"account_list", "account_show", "account_status", "settings_show", "settings_update",
		"settings_update_commit", "account_add", "account_add_commit", "account_rename",
		"account_rename_commit", "account_remove", "account_remove_commit", "monitor_status",
		"events_list", "event_acknowledge",
	)
)

func TestModernProtocolConformance(t *testing.T) {
	t.Parallel()

	server, err := New(&fakeBackend{}, Options{Version: "v0.9.0-rc.1", Instance: "conformance"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	recorder := &wireRecorder{}
	serverSession, err := server.Connect(t.Context(), &recordingTransport{
		delegate: serverTransport,
		recorder: recorder,
	}, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{
		Name: "corresync-conformance-client", Version: "v1",
	}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	initialized := clientSession.InitializeResult()
	if initialized == nil || initialized.ProtocolVersion != modernProtocolVersion {
		t.Fatalf("negotiated protocol = %+v, want %s", initialized, modernProtocolVersion)
	}
	if initialized.ServerInfo == nil || initialized.ServerInfo.Name != Name ||
		initialized.ServerInfo.Version != "v0.9.0-rc.1" {
		t.Fatalf("server info = %+v", initialized.ServerInfo)
	}
	if clientSession.ID() != "" {
		t.Fatalf("stdio-compatible session ID = %q, want empty", clientSession.ID())
	}

	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	assertToolCatalog(t, tools.Tools)
	templates, err := clientSession.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	assertResourceTemplates(t, templates.ResourceTemplates)
	if _, err := clientSession.ReadResource(t.Context(), &mcp.ReadResourceParams{
		URI: "corresync://monitor/work",
	}); err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}

	requests, responses := recorder.snapshot()
	methods := make(map[string]bool, len(requests))
	for _, encoded := range requests {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(encoded, &request); err != nil {
			t.Fatalf("decode recorded request: %v", err)
		}
		methods[request.Method] = true
		meta, ok := request.Params["_meta"].(map[string]any)
		if !ok {
			t.Errorf("%s request has no object _meta: %s", request.Method, encoded)
			continue
		}
		if got := meta[mcp.MetaKeyProtocolVersion]; got != modernProtocolVersion {
			t.Errorf("%s protocol metadata = %v, want %s", request.Method, got, modernProtocolVersion)
		}
		info, infoOK := meta[mcp.MetaKeyClientInfo].(map[string]any)
		if !infoOK || info["name"] != "corresync-conformance-client" {
			t.Errorf("%s client info = %#v", request.Method, meta[mcp.MetaKeyClientInfo])
		}
		if _, ok := meta[mcp.MetaKeyClientCapabilities].(map[string]any); !ok {
			t.Errorf("%s client capabilities = %#v", request.Method, meta[mcp.MetaKeyClientCapabilities])
		}
	}
	for _, method := range []string{
		"server/discover", "tools/list", "resources/templates/list", "resources/read",
	} {
		if !methods[method] {
			t.Errorf("modern wire did not carry %s", method)
		}
	}
	for _, forbidden := range []string{"initialize", "notifications/initialized"} {
		if methods[forbidden] {
			t.Errorf("modern wire unexpectedly carried legacy method %s", forbidden)
		}
	}
	assertDiscoveryServerInfo(t, responses)
}

func TestLegacyProtocolConformance(t *testing.T) {
	t.Parallel()

	server, err := New(&fakeBackend{}, Options{Version: "v0.9.0-rc.1", Instance: "legacy"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	connection := connectRawClient(t, server)

	initialize := rawCall(t, connection, 1, "initialize", &mcp.InitializeParams{
		ProtocolVersion: legacyProtocolVersion,
		ClientInfo: &mcp.Implementation{
			Name: "legacy-conformance-client", Version: "v1",
		},
		Capabilities: &mcp.ClientCapabilities{},
	})
	var initialized mcp.InitializeResult
	decodeRawResult(t, initialize, &initialized)
	if initialized.ProtocolVersion != legacyProtocolVersion {
		t.Fatalf("legacy protocol = %q, want %q", initialized.ProtocolVersion, legacyProtocolVersion)
	}
	if initialized.ServerInfo == nil || initialized.ServerInfo.Name != Name {
		t.Fatalf("legacy server info = %+v", initialized.ServerInfo)
	}
	initializedParams, err := json.Marshal(&mcp.InitializedParams{})
	if err != nil {
		t.Fatal(err)
	}
	initializedNotification := &jsonrpc.Request{
		Method: "notifications/initialized", Params: initializedParams,
	}
	if err := connection.Write(t.Context(), initializedNotification); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}

	toolResponse := rawCall(t, connection, 2, "tools/list", &mcp.ListToolsParams{})
	var tools mcp.ListToolsResult
	decodeRawResult(t, toolResponse, &tools)
	assertToolCatalog(t, tools.Tools)
	templateResponse := rawCall(
		t, connection, 3, "resources/templates/list", &mcp.ListResourceTemplatesParams{},
	)
	var templates mcp.ListResourceTemplatesResult
	decodeRawResult(t, templateResponse, &templates)
	assertResourceTemplates(t, templates.ResourceTemplates)
}

func TestModernProtocolRejectsInvalidNegotiationMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params map[string]any
		code   int64
	}{
		{
			name:   "missing client capabilities",
			method: "server/discover",
			params: map[string]any{"_meta": map[string]any{
				mcp.MetaKeyProtocolVersion: modernProtocolVersion,
				mcp.MetaKeyClientInfo:      map[string]any{"name": "client", "version": "v1"},
			}},
			code: jsonrpc.CodeInvalidParams,
		},
		{
			name:   "unsupported protocol version",
			method: "server/discover",
			params: modernParams("2099-01-01"),
			code:   mcp.CodeUnsupportedProtocolVersion,
		},
		{
			name:   "removed initialize handshake",
			method: "initialize",
			params: func() map[string]any {
				params := modernParams(modernProtocolVersion)
				params["protocolVersion"] = modernProtocolVersion
				return params
			}(),
			code: jsonrpc.CodeMethodNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, err := New(&fakeBackend{}, Options{Version: "dev", Instance: "invalid"})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			response := rawCall(t, connectRawClient(t, server), 1, test.method, test.params)
			var wireError *jsonrpc.Error
			if !errors.As(response.Error, &wireError) || wireError.Code != test.code {
				t.Fatalf("response error = %v, want JSON-RPC code %d", response.Error, test.code)
			}
		})
	}
}

func TestToolResultsConformToAdvertisedOutputSchemas(t *testing.T) {
	t.Parallel()

	server, err := New(&fakeBackend{}, Options{Version: "dev", Instance: "schemas"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	client := connectTestClient(t, server)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	assertToolCatalog(t, listed.Tools)

	for _, tool := range listed.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			input := schemaObject(t, tool.InputSchema)
			arguments := syntheticRequiredProperties(t, input)
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
				Name: tool.Name, Arguments: arguments,
			})
			if err != nil || result.IsError {
				t.Fatalf("CallTool() result = %+v, error = %v, arguments = %#v", result, err, arguments)
			}
			resolved := resolveSchema(t, tool.OutputSchema)
			if err := resolved.Validate(result.StructuredContent); err != nil {
				t.Fatalf("structured result does not match outputSchema: %v\nresult: %#v", err, result.StructuredContent)
			}
		})
	}
}

func TestPendingMessagingRouteIsAbsentFromStableAccountAddSchema(t *testing.T) {
	t.Parallel()

	server, err := New(&fakeBackend{}, Options{Version: "dev", Instance: "closed-messaging-schema"})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := connectTestClient(t, server).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "account_add" {
			continue
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), `"messages"`) {
			t.Fatalf("pending messaging route leaked into account_add schema: %s", encoded)
		}
		return
	}
	t.Fatal("account_add tool is missing")
}

func assertToolCatalog(t *testing.T, tools []*mcp.Tool) {
	t.Helper()
	gotNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		gotNames = append(gotNames, tool.Name)
		if tool.Name == "" || tool.Title == "" || tool.Description == "" {
			t.Errorf("tool identity is incomplete: %+v", tool)
		}
		input := schemaObject(t, tool.InputSchema)
		output := schemaObject(t, tool.OutputSchema)
		if input["type"] != "object" || output["type"] != "object" {
			t.Errorf("%s schemas are not objects: input=%#v output=%#v", tool.Name, input["type"], output["type"])
		}
		resolveSchema(t, tool.InputSchema)
		resolveSchema(t, tool.OutputSchema)
		assertToolSafety(t, tool)
	}
	sort.Strings(gotNames)
	if !slices.Equal(gotNames, expectedToolNames) {
		t.Errorf("tool names =\n%s\nwant =\n%s", strings.Join(gotNames, "\n"), strings.Join(expectedToolNames, "\n"))
	}
}

func assertToolSafety(t *testing.T, tool *mcp.Tool) {
	t.Helper()
	if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil ||
		tool.Annotations.OpenWorldHint == nil {
		t.Errorf("%s has incomplete annotations: %+v", tool.Name, tool.Annotations)
		return
	}
	if got, want := tool.Annotations.ReadOnlyHint, expectedReadOnlyTools[tool.Name]; got != want {
		t.Errorf("%s readOnlyHint = %t, want %t", tool.Name, got, want)
	}
	if got, want := *tool.Annotations.DestructiveHint, expectedDestructiveTools[tool.Name]; got != want {
		t.Errorf("%s destructiveHint = %t, want %t", tool.Name, got, want)
	}
	if got, want := *tool.Annotations.OpenWorldHint, !expectedClosedWorldTools[tool.Name]; got != want {
		t.Errorf("%s openWorldHint = %t, want %t", tool.Name, got, want)
	}
	encoded, err := json.Marshal(tool.Annotations)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
		if _, ok := wire[field]; !ok {
			t.Errorf("%s omits %s on the wire: %s", tool.Name, field, encoded)
		}
	}
	for _, key := range []string{
		"io.github.nkiyohara.corresync/data-classification",
		"io.github.nkiyohara.corresync/effect",
	} {
		if value, ok := tool.Meta[key].(string); !ok || value == "" {
			t.Errorf("%s has invalid %s metadata: %#v", tool.Name, key, tool.Meta[key])
		}
	}
}

func assertResourceTemplates(t *testing.T, templates []*mcp.ResourceTemplate) {
	t.Helper()
	want := map[string]string{
		"corresync://events/{account}":  "events",
		"corresync://monitor/{account}": "monitor_status",
	}
	if len(templates) != len(want) {
		t.Fatalf("resource template count = %d, want %d", len(templates), len(want))
	}
	for _, template := range templates {
		if want[template.URITemplate] != template.Name || template.Title == "" ||
			template.Description == "" || template.MIMEType != "application/json" {
			t.Errorf("unexpected resource template: %+v", template)
		}
	}
}

type recordingTransport struct {
	delegate mcp.Transport
	recorder *wireRecorder
}

func (transport *recordingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := transport.delegate.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &recordingConnection{Connection: connection, recorder: transport.recorder}, nil
}

func (*recordingTransport) SupportsProtocolVersion(string) bool { return true }

type recordingConnection struct {
	mcp.Connection
	recorder *wireRecorder
}

func (connection *recordingConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	message, err := connection.Connection.Read(ctx)
	if err == nil {
		connection.recorder.recordRequest(message)
	}
	return message, err
}

func (connection *recordingConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	connection.recorder.recordResponse(message)
	return connection.Connection.Write(ctx, message)
}

type wireRecorder struct {
	mu        sync.Mutex
	requests  [][]byte
	responses [][]byte
}

func (recorder *wireRecorder) recordRequest(message jsonrpc.Message) {
	recorder.record(message, true)
}

func (recorder *wireRecorder) recordResponse(message jsonrpc.Message) {
	recorder.record(message, false)
}

func (recorder *wireRecorder) record(message jsonrpc.Message, request bool) {
	encoded, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		panic(fmt.Sprintf("encode recorded MCP message: %v", err))
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if request {
		recorder.requests = append(recorder.requests, encoded)
		return
	}
	recorder.responses = append(recorder.responses, encoded)
}

func (recorder *wireRecorder) snapshot() (requests, responses [][]byte) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return slices.Clone(recorder.requests), slices.Clone(recorder.responses)
}

func assertDiscoveryServerInfo(t *testing.T, responses [][]byte) {
	t.Helper()
	for _, encoded := range responses {
		var response struct {
			Result struct {
				Meta map[string]any `json:"_meta"`
			} `json:"result"`
		}
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatalf("decode recorded response: %v", err)
		}
		info, ok := response.Result.Meta[mcp.MetaKeyServerInfo].(map[string]any)
		if ok && info["name"] == Name && info["version"] == "v0.9.0-rc.1" {
			return
		}
	}
	t.Error("server/discover response omitted Corresync serverInfo metadata")
}

func connectRawClient(t *testing.T, server *mcp.Server) mcp.Connection {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	connection, err := clientTransport.Connect(t.Context())
	if err != nil {
		t.Fatalf("client transport Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if connection.SessionID() != "" {
		t.Fatalf("raw stdio-compatible session ID = %q, want empty", connection.SessionID())
	}
	return connection
}

func rawCall(
	t *testing.T,
	connection mcp.Connection,
	id int64,
	method string,
	params any,
) *jsonrpc.Response {
	t.Helper()
	requestID, err := jsonrpc.MakeID(float64(id))
	if err != nil {
		t.Fatalf("construct %s request ID: %v", method, err)
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode %s params: %v", method, err)
	}
	call := &jsonrpc.Request{ID: requestID, Method: method, Params: encodedParams}
	if err := connection.Write(t.Context(), call); err != nil {
		t.Fatalf("write %s call: %v", method, err)
	}
	message, err := connection.Read(t.Context())
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("%s response type = %T", method, message)
	}
	if response.ID.Raw() != id {
		t.Fatalf("%s response ID = %v, want %d", method, response.ID.Raw(), id)
	}
	return response
}

func decodeRawResult(t *testing.T, response *jsonrpc.Response, destination any) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("JSON-RPC error = %v", response.Error)
	}
	if err := json.Unmarshal(response.Result, destination); err != nil {
		t.Fatalf("decode JSON-RPC result: %v", err)
	}
}

func modernParams(version string) map[string]any {
	return map[string]any{"_meta": map[string]any{
		mcp.MetaKeyProtocolVersion: version,
		mcp.MetaKeyClientInfo: map[string]any{
			"name": "conformance-client", "version": "v1",
		},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}}
}

func schemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("decode schema object: %v", err)
	}
	return object
}

func resolveSchema(t *testing.T, value any) *jsonschema.Resolved {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatalf("decode JSON Schema: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve JSON Schema: %v", err)
	}
	return resolved
}

func syntheticRequiredProperties(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	properties, _ := root["properties"].(map[string]any)
	result := make(map[string]any)
	for _, name := range stringSlice(root["required"]) {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("required property %q has no object schema", name)
		}
		result[name] = syntheticSchemaValue(t, root, property, name)
	}
	return result
}

func syntheticSchemaValue(
	t *testing.T,
	root map[string]any,
	schema map[string]any,
	name string,
) any {
	t.Helper()
	if reference, _ := schema["$ref"].(string); reference != "" {
		schema = localSchemaReference(t, root, reference)
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		for _, value := range values {
			if value != nil {
				return value
			}
		}
		return values[0]
	}
	if value, ok := schema["const"]; ok {
		return value
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if alternatives, ok := schema[keyword].([]any); ok {
			for _, alternative := range alternatives {
				candidate, ok := alternative.(map[string]any)
				if ok && candidate["type"] != "null" {
					return syntheticSchemaValue(t, root, candidate, name)
				}
			}
		}
	}
	switch schema["type"] {
	case "object":
		return syntheticRequiredProperties(t, mergeSchemaRoot(root, schema))
	case "array":
		count := 0
		if minimum, ok := schema["minItems"].(float64); ok {
			count = int(minimum)
		}
		items := make([]any, 0, count)
		itemSchema, _ := schema["items"].(map[string]any)
		for range count {
			items = append(items, syntheticSchemaValue(t, root, itemSchema, name))
		}
		return items
	case "boolean":
		return false
	case "integer", "number":
		if minimum, ok := schema["minimum"].(float64); ok {
			return minimum
		}
		return float64(0)
	case "null":
		return nil
	default:
		if strings.Contains(strings.ToLower(name), "address") || name == "to" || name == "cc" || name == "bcc" {
			return "person@example.test"
		}
		return "synthetic"
	}
}

func mergeSchemaRoot(root, schema map[string]any) map[string]any {
	merged := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		merged[key] = value
	}
	for _, definitions := range []string{"$defs", "definitions"} {
		if _, ok := merged[definitions]; !ok {
			merged[definitions] = root[definitions]
		}
	}
	return merged
}

func localSchemaReference(t *testing.T, root map[string]any, reference string) map[string]any {
	t.Helper()
	if !strings.HasPrefix(reference, "#/") {
		t.Fatalf("schema contains non-local reference %q", reference)
	}
	var current any = root
	for _, component := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("schema reference %q traverses non-object component %q", reference, component)
		}
		current, ok = object[strings.ReplaceAll(strings.ReplaceAll(component, "~1", "/"), "~0", "~")]
		if !ok {
			t.Fatalf("schema reference %q is missing component %q", reference, component)
		}
	}
	result, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("schema reference %q does not resolve to an object", reference)
	}
	return result
}

func stringSlice(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func stringSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
