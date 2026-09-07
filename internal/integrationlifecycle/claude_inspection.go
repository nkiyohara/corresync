package integrationlifecycle

import (
	"errors"
	"path/filepath"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

// Inspect one scoped registration without executing it. Claude's get command
// resolves scope precedence and its human argv display loses argument boundaries.
// Writes continue through the official scoped CLI command.
func inspectClaudeRegistration(environment Environment, request Request) (Inspection, error) {
	if environment.ClaudeConfigOverride {
		return Inspection{State: StateUnavailable, Scope: request.Scope, Detail: "A custom CLAUDE_CONFIG_DIR is active; manage this registration in Claude until its scoped configuration path can be verified."}, nil
	}
	path := filepath.Join(environment.HomeDirectory, ".claude.json")
	validationRequest := request
	validationRequest.Scope = agenthost.ScopeUser
	switch request.Scope {
	case agenthost.ScopeUser, agenthost.ScopeLocal:
		if !filepath.IsAbs(environment.HomeDirectory) {
			return Inspection{}, errors.New("claude configuration home must be absolute")
		}
	case agenthost.ScopeProject:
		path = filepath.Join(request.ProjectDirectory, ".mcp.json")
		validationRequest = request
	case agenthost.ScopeWorkspace:
		return Inspection{}, errors.New("unsupported Claude configuration scope")
	default:
		return Inspection{}, errors.New("unsupported Claude configuration scope")
	}
	if err := validateTargetParents(path, validationRequest, environment); err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	snapshot, err := loadJSONSnapshot(path)
	if err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	inspection := Inspection{State: StateAbsent, Scope: request.Scope, Path: path, Fingerprint: snapshot.fingerprint, Detail: "The named registration is absent from the requested Claude scope."}
	document := snapshot.document
	if request.Scope == agenthost.ScopeLocal {
		projects, exists := document["projects"]
		if !exists {
			return inspection, nil
		}
		projectMap, ok := projects.(map[string]any)
		if !ok {
			return malformedClaudeInspection(inspection), nil
		}
		project, exists := projectMap[request.ProjectDirectory]
		if !exists {
			return inspection, nil
		}
		document, ok = project.(map[string]any)
		if !ok {
			return malformedClaudeInspection(inspection), nil
		}
	}
	adapter := jsonAdapter{shape: shapeMCPServers}
	servers, err := adapter.serverMap(document, false)
	if err != nil {
		return malformedClaudeInspection(inspection), nil //nolint:nilerr // Inspection reports malformed host state as a typed result.
	}
	entry, exists := servers[request.ServerName]
	if !exists {
		return inspection, nil
	}
	inspection.State = adapter.classifyEntry(request, entry)
	if fields, ok := entry.(map[string]any); ok {
		if transport, exists := fields["type"]; exists && transport != "stdio" {
			inspection.State = StateNameConflict
		}
	}
	switch inspection.State {
	case StateHealthy:
		inspection.Detail = "The requested Claude scope contains the exact Corresync executable and argument array."
	case StateDisabled:
		inspection.Detail = "The scoped Corresync registration is disabled."
	case StateStalePath:
		inspection.Detail = "The scoped Corresync registration has stale launch fields."
	case StateNameConflict, StateAbsent, StateVersionDrift, StateMalformed, StateUnreadable, StateUnavailable:
		inspection.Detail = "The name in the requested Claude scope belongs to another integration."
	}
	return inspection, nil
}

func malformedClaudeInspection(inspection Inspection) Inspection {
	inspection.State = StateMalformed
	inspection.Detail = "The requested Claude scope has an incompatible registration object shape."
	return inspection
}
