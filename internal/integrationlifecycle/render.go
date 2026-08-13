package integrationlifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type CodexDocument struct {
	Servers map[string]CodexServer `toml:"mcp_servers"`
}

type CodexServer struct {
	Command         string   `toml:"command"`
	Arguments       []string `toml:"args"`
	StartupTimeout  int      `toml:"startup_timeout_sec"`
	ToolTimeout     int      `toml:"tool_timeout_sec"`
	DefaultApproval string   `toml:"default_tools_approval_mode"`
	Enabled         bool     `toml:"enabled"`
	Required        bool     `toml:"required"`
}

type JSONDocument struct {
	Servers map[string]JSONServer `json:"mcpServers"`
}

type JSONServer struct {
	Type             string            `json:"type,omitempty"`
	Command          string            `json:"command"`
	Arguments        []string          `json:"args"`
	Env              map[string]string `json:"env,omitempty"`
	Tools            []string          `json:"tools,omitempty"`
	Description      string            `json:"description,omitempty"`
	TimeoutMS        int               `json:"timeout,omitempty"`
	Enabled          *bool             `json:"enabled,omitempty"`
	StartupTimeoutMS int               `json:"startupTimeoutMs,omitempty"`
	ToolTimeoutMS    int               `json:"toolTimeoutMs,omitempty"`
}

// RenderConfig emits one standalone host fragment from the same launch model
// used by lifecycle plans. It cannot represent credentials or host approvals.
func RenderConfig(host agenthost.ID, name, executable string, arguments []string) ([]byte, error) {
	request := Request{
		Operation: OperationSetup, Host: host, Scope: agenthost.ScopeUser,
		ServerName: name, Executable: executable, Arguments: arguments,
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if host == agenthost.IDCodex {
		document := CodexDocument{Servers: map[string]CodexServer{name: {
			Command: executable, Arguments: arguments,
			StartupTimeout: 30, ToolTimeout: 360,
			DefaultApproval: "writes", Enabled: true, Required: false,
		}}}
		encoded, err := toml.Marshal(document)
		if err != nil {
			return nil, fmt.Errorf("encode Codex MCP config: %w", err)
		}
		return encoded, nil
	}
	server, ok := jsonServerForHost(host)
	if !ok {
		return nil, fmt.Errorf("host %q has no standalone MCP config renderer", host)
	}
	server.Command = executable
	server.Arguments = arguments
	document := JSONDocument{Servers: map[string]JSONServer{name: server}}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode %s MCP config: %w", host, err)
	}
	return encoded.Bytes(), nil
}

func jsonServerForHost(host agenthost.ID) (JSONServer, bool) {
	//nolint:exhaustive // False is deliberate for hosts without this legacy JSON renderer.
	switch host {
	case agenthost.IDClaudeCode:
		return JSONServer{Type: "stdio"}, true
	case agenthost.IDGitHubCopilot:
		return JSONServer{Type: "stdio", Tools: []string{"*"}, TimeoutMS: 360_000}, true
	case agenthost.IDGeminiCLI:
		return JSONServer{Description: "Local-first guarded mail and calendar", TimeoutMS: 360_000}, true
	case agenthost.IDQwenCode, agenthost.IDQoder, agenthost.IDCursor, agenthost.IDWindsurf, agenthost.IDCline:
		return JSONServer{}, true
	case agenthost.IDKimiCode:
		enabled := true
		return JSONServer{Enabled: &enabled, StartupTimeoutMS: 30_000, ToolTimeoutMS: 360_000}, true
	default:
		return JSONServer{}, false
	}
}
