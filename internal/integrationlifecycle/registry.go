package integrationlifecycle

import (
	"fmt"
	"slices"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type commandAdapter struct {
	host       agenthost.ID
	executable string
	scopes     []agenthost.Scope
	add        func(Request) []string
	inspect    func(Request) []string
	remove     func(Request) []string
	list       bool
}

var commandAdapters = map[agenthost.ID]commandAdapter{
	agenthost.IDCodex: {
		host: agenthost.IDCodex, executable: "codex", scopes: []agenthost.Scope{agenthost.ScopeUser},
		add: func(r Request) []string {
			return append([]string{"mcp", "add", r.ServerName, "--", r.Executable}, r.Arguments...)
		},
		inspect: func(r Request) []string { return []string{"mcp", "get", r.ServerName} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", r.ServerName} },
	},
	agenthost.IDClaudeCode: {
		host: agenthost.IDClaudeCode, executable: "claude",
		scopes: []agenthost.Scope{agenthost.ScopeLocal, agenthost.ScopeProject, agenthost.ScopeUser},
		add: func(r Request) []string {
			return append([]string{"mcp", "add", "--scope", string(r.Scope), r.ServerName, "--", r.Executable}, r.Arguments...)
		},
		inspect: func(r Request) []string { return []string{"mcp", "get", r.ServerName} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", "--scope", string(r.Scope), r.ServerName} },
	},
	agenthost.IDGitHubCopilot: {
		host: agenthost.IDGitHubCopilot, executable: "copilot", scopes: []agenthost.Scope{agenthost.ScopeUser},
		add: func(r Request) []string {
			arguments := make([]string, 0, 11+len(r.Arguments))
			arguments = append(arguments, "mcp", "add", r.ServerName, "--type", "stdio", "--tools", "*", "--timeout", "360000", "--", r.Executable)
			return append(arguments, r.Arguments...)
		},
		inspect: func(r Request) []string { return []string{"mcp", "get", r.ServerName, "--json"} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", r.ServerName} },
	},
	agenthost.IDGeminiCLI: {
		host: agenthost.IDGeminiCLI, executable: "gemini",
		scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject}, list: true,
		add: func(r Request) []string {
			arguments := make([]string, 0, 11+len(r.Arguments))
			arguments = append(arguments, "mcp", "add", "--scope", string(r.Scope), "--description", "Local-first guarded mail and calendar", "--timeout", "360000", r.ServerName, r.Executable, "--")
			return append(arguments, r.Arguments...)
		},
		inspect: func(Request) []string { return []string{"mcp", "list"} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", "--scope", string(r.Scope), r.ServerName} },
	},
	agenthost.IDQwenCode: {
		host: agenthost.IDQwenCode, executable: "qwen",
		scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject}, list: true,
		add: func(r Request) []string {
			arguments := make([]string, 0, 8+len(r.Arguments))
			arguments = append(arguments, "mcp", "add", "--scope", string(r.Scope), "--description", "Local-first guarded mail and calendar", r.ServerName, r.Executable)
			return append(arguments, r.Arguments...)
		},
		inspect: func(Request) []string { return []string{"mcp", "list"} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", r.ServerName} },
	},
	agenthost.IDQoder: {
		host: agenthost.IDQoder, executable: "qodercli",
		scopes: []agenthost.Scope{agenthost.ScopeLocal, agenthost.ScopeProject, agenthost.ScopeUser}, list: true,
		add: func(r Request) []string {
			return append([]string{"mcp", "add", "-s", string(r.Scope), r.ServerName, "--", r.Executable}, r.Arguments...)
		},
		inspect: func(Request) []string { return []string{"mcp", "list"} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", r.ServerName} },
	},
	agenthost.IDKimiCode: {
		host: agenthost.IDKimiCode, executable: "kimi", scopes: []agenthost.Scope{agenthost.ScopeUser}, list: true,
		add: func(r Request) []string {
			return append([]string{"mcp", "add", r.ServerName, "--", r.Executable}, r.Arguments...)
		},
		inspect: func(Request) []string { return []string{"mcp", "list"} },
		remove:  func(r Request) []string { return []string{"mcp", "remove", r.ServerName} },
	},
}

// OfficialCommands returns the reviewed executable/argv lifecycle for a Phase
// A host. The caller executes argv directly and must not join it into a shell
// program. The boolean is false for config-only and catalog-only hosts.
func OfficialCommands(request Request) (add, inspect, remove Command, list bool, ok bool, err error) {
	if err := request.Validate(); err != nil {
		return Command{}, Command{}, Command{}, false, false, err
	}
	adapter, ok := commandAdapters[request.Host]
	if !ok {
		return Command{}, Command{}, Command{}, false, false, nil
	}
	if !slices.Contains(adapter.scopes, request.Scope) {
		return Command{}, Command{}, Command{}, false, false, fmt.Errorf(
			"%s does not support %s integration scope", request.Host, request.Scope,
		)
	}
	directory := ""
	if request.Scope != agenthost.ScopeUser {
		directory = request.ProjectDirectory
	}
	return Command{Executable: adapter.executable, Arguments: adapter.add(request), WorkingDirectory: directory},
		Command{Executable: adapter.executable, Arguments: adapter.inspect(request), WorkingDirectory: directory},
		Command{Executable: adapter.executable, Arguments: adapter.remove(request), WorkingDirectory: directory},
		adapter.list, true, nil
}
