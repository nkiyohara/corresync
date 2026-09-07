package integrationlifecycle

import (
	"encoding/json"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

// Only exact, named absence results establish that a failing command did not
// find a registration. Empty output, generic failures and other names do not.
func knownMissingRegistration(request Request, output string) bool {
	output = strings.TrimPrefix(strings.TrimSpace(output), "Error: ")
	if request.Host == agenthost.IDCodex {
		return output == "No MCP server named '"+request.ServerName+"' found."
	}
	if request.Host == agenthost.IDClaudeCode {
		return output == "No MCP server found with name: "+request.ServerName ||
			output == "No MCP server named \""+request.ServerName+"\"."
	}
	return false
}

// Claude's get command resolves across scopes. A result from another scope
// neither proves absence nor grants authority to mutate the requested scope.
func inspectionScopeMatches(request Request, output string) bool {
	if request.Host != agenthost.IDClaudeCode {
		return true
	}
	var entry map[string]any
	if json.Unmarshal([]byte(output), &entry) == nil && entry != nil {
		scope, ok := entry["scope"].(string)
		return ok && scope == string(request.Scope)
	}
	scope := ""
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || !strings.EqualFold(key, "scope") {
			continue
		}
		if scope != "" {
			return false
		}
		switch strings.TrimSpace(value) {
		case "Local config (private to you in this project)":
			scope = "local"
		case "Project config (shared via .mcp.json)":
			scope = "project"
		case "User config (available in all your projects)":
			scope = "user"
		default:
			return false
		}
	}
	return scope != "" && scope == string(request.Scope)
}
