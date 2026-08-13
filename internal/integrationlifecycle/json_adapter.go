package integrationlifecycle

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type Environment struct {
	HomeDirectory    string
	ConfigDirectory  string
	BundleDirectory  string
	ManagedDirectory string
	GOOS             string
}

type jsonShape string

const (
	shapeMCPServers jsonShape = "mcp_servers"
	shapeOpenCode   jsonShape = "opencode_v2"
	shapeZed        jsonShape = "zed_context_servers"
	shapeVSCode     jsonShape = "vscode_servers"
)

type jsonAdapter struct {
	host   agenthost.ID
	scopes []agenthost.Scope
	shape  jsonShape
	path   func(Environment, Request) (string, error)
}

var hostAutoApprovalFields = []string{
	"alwaysAllow", "always_allow", "always-allow",
	"autoApprove", "auto_approve", "auto-approve",
	"autoApproved", "auto_approved", "auto-approved",
}

var jsonAdapters = map[agenthost.ID]jsonAdapter{
	agenthost.IDVSCode: {
		host: agenthost.IDVSCode, scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeWorkspace}, shape: shapeVSCode,
		path: func(environment Environment, request Request) (string, error) {
			if request.Scope == agenthost.ScopeWorkspace {
				if err := validateProjectDirectory(request.ProjectDirectory); err != nil {
					return "", err
				}
				return filepath.Join(request.ProjectDirectory, ".vscode", "mcp.json"), nil
			}
			return filepath.Join(environment.ConfigDirectory, "Code", "User", "mcp.json"), nil
		},
	},
	agenthost.IDCursor: {
		host: agenthost.IDCursor, scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject}, shape: shapeMCPServers,
		path: userOrProjectPath(".cursor", "mcp.json"),
	},
	agenthost.IDWindsurf: {
		host: agenthost.IDWindsurf, scopes: []agenthost.Scope{agenthost.ScopeUser}, shape: shapeMCPServers,
		path: func(environment Environment, _ Request) (string, error) {
			return filepath.Join(environment.HomeDirectory, ".codeium", "windsurf", "mcp_config.json"), nil
		},
	},
	agenthost.IDOpenCode: {
		host: agenthost.IDOpenCode, scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject}, shape: shapeOpenCode,
		path: func(environment Environment, request Request) (string, error) {
			if request.Scope == agenthost.ScopeProject {
				if err := validateProjectDirectory(request.ProjectDirectory); err != nil {
					return "", err
				}
				return filepath.Join(request.ProjectDirectory, ".opencode", "opencode.json"), nil
			}
			return filepath.Join(environment.ConfigDirectory, "opencode", "opencode.json"), nil
		},
	},
	agenthost.IDCline: {
		host: agenthost.IDCline, scopes: []agenthost.Scope{agenthost.ScopeUser}, shape: shapeMCPServers,
		path: func(environment Environment, _ Request) (string, error) {
			return filepath.Join(environment.HomeDirectory, ".cline", "data", "settings", "cline_mcp_settings.json"), nil
		},
	},
	agenthost.IDRooCode: {
		host: agenthost.IDRooCode, scopes: []agenthost.Scope{agenthost.ScopeProject}, shape: shapeMCPServers,
		path: func(_ Environment, request Request) (string, error) {
			if err := validateProjectDirectory(request.ProjectDirectory); err != nil {
				return "", err
			}
			return filepath.Join(request.ProjectDirectory, ".roo", "mcp.json"), nil
		},
	},
	agenthost.IDZed: {
		host: agenthost.IDZed, scopes: []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject}, shape: shapeZed,
		path: func(environment Environment, request Request) (string, error) {
			if request.Scope == agenthost.ScopeProject {
				if err := validateProjectDirectory(request.ProjectDirectory); err != nil {
					return "", err
				}
				return filepath.Join(request.ProjectDirectory, ".zed", "settings.json"), nil
			}
			name := "zed"
			if environment.GOOS == "darwin" {
				name = "Zed"
			}
			return filepath.Join(environment.ConfigDirectory, name, "settings.json"), nil
		},
	},
}

func userOrProjectPath(parts ...string) func(Environment, Request) (string, error) {
	return func(environment Environment, request Request) (string, error) {
		root := environment.HomeDirectory
		if request.Scope == agenthost.ScopeProject {
			if err := validateProjectDirectory(request.ProjectDirectory); err != nil {
				return "", err
			}
			root = request.ProjectDirectory
		}
		return filepath.Join(append([]string{root}, parts...)...), nil
	}
}

func validateProjectDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("project scope requires an explicit clean absolute project directory")
	}
	return nil
}

func resolveJSONAdapter(environment Environment, request Request) (jsonAdapter, string, bool, error) {
	adapter, ok := jsonAdapters[request.Host]
	if !ok || !slices.Contains(adapter.scopes, request.Scope) {
		return jsonAdapter{}, "", false, nil
	}
	if !filepath.IsAbs(environment.HomeDirectory) || !filepath.IsAbs(environment.ConfigDirectory) {
		return jsonAdapter{}, "", false, errors.New("integration home and config directories must be absolute")
	}
	path, err := adapter.path(environment, request)
	if err != nil {
		return jsonAdapter{}, "", false, err
	}
	return adapter, filepath.Clean(path), true, nil
}

func (adapter jsonAdapter) expectedEntry(request Request) map[string]any {
	arguments := slices.Clone(request.Arguments)
	switch adapter.shape {
	case shapeOpenCode:
		return map[string]any{
			"type": "local", "command": append([]string{request.Executable}, arguments...),
		}
	case shapeZed:
		return map[string]any{
			"source": "custom", "enabled": true,
			"command": request.Executable, "args": arguments,
		}
	case shapeVSCode:
		return map[string]any{"type": "stdio", "command": request.Executable, "args": arguments}
	case shapeMCPServers:
		return map[string]any{"command": request.Executable, "args": arguments}
	}
	return nil
}

func (adapter jsonAdapter) serverMap(document map[string]any, create bool) (map[string]any, error) {
	path := []string{"mcpServers"}
	switch adapter.shape {
	case shapeOpenCode:
		path = []string{"mcp", "servers"}
	case shapeZed:
		path = []string{"context_servers"}
	case shapeVSCode:
		path = []string{"servers"}
	case shapeMCPServers:
	}
	cursor := document
	for _, name := range path {
		value, exists := cursor[name]
		if !exists {
			if !create {
				return nil, nil
			}
			nested := make(map[string]any)
			cursor[name] = nested
			cursor = nested
			continue
		}
		nested, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("host configuration field %q must be an object", strings.Join(path, "."))
		}
		cursor = nested
	}
	return cursor, nil
}

func (adapter jsonAdapter) classifyEntry(request Request, raw any) State {
	entry, ok := raw.(map[string]any)
	if !ok {
		return StateNameConflict
	}
	expected := adapter.expectedEntry(request)
	wantCommand := expected["command"]
	gotCommand := entry["command"]
	if adapter.shape == shapeOpenCode {
		command, ok := gotCommand.([]any)
		if !ok || len(command) == 0 {
			return StateNameConflict
		}
		first, _ := command[0].(string)
		staleCommand := first != request.Executable
		if staleCommand && !ownedExecutable(first) {
			return StateNameConflict
		}
		if disabled, ok := entry["disabled"].(bool); ok && disabled {
			return StateDisabled
		}
		if enabled, ok := entry["enabled"].(bool); ok && !enabled {
			return StateDisabled
		}
		if staleCommand {
			return StateStalePath
		}
		if hasHostAutoApproval(entry) {
			return StateStalePath
		}
		want := append([]string{request.Executable}, request.Arguments...)
		if !equalStringArray(command, want) || entry["type"] != "local" {
			return StateStalePath
		}
		return StateHealthy
	}
	command, ok := gotCommand.(string)
	if !ok {
		return StateNameConflict
	}
	staleCommand := command != wantCommand
	if staleCommand && !ownedExecutable(command) {
		return StateNameConflict
	}
	if disabled, ok := entry["disabled"].(bool); ok && disabled {
		return StateDisabled
	}
	if enabled, ok := entry["enabled"].(bool); ok && !enabled {
		return StateDisabled
	}
	if staleCommand {
		return StateStalePath
	}
	if hasHostAutoApproval(entry) {
		return StateStalePath
	}
	if adapter.shape == shapeZed && (entry["source"] != "custom" || entry["enabled"] != true) {
		return StateStalePath
	}
	if adapter.shape == shapeVSCode && entry["type"] != "stdio" {
		return StateStalePath
	}
	if !equalStringArrayValue(entry["args"], request.Arguments) {
		return StateStalePath
	}
	return StateHealthy
}

func hasHostAutoApproval(entry map[string]any) bool {
	for _, name := range hostAutoApprovalFields {
		if _, exists := entry[name]; exists {
			return true
		}
	}
	return false
}

func equalStringArrayValue(value any, expected []string) bool {
	values, ok := value.([]any)
	if !ok {
		return len(expected) == 0 && (value == nil || reflect.DeepEqual(value, []any{}))
	}
	return equalStringArray(values, expected)
}

func equalStringArray(values []any, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	for index := range values {
		value, ok := values[index].(string)
		if !ok || value != expected[index] {
			return false
		}
	}
	return true
}
