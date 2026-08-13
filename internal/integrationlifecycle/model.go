// Package integrationlifecycle plans and applies Corresync integration changes
// through host-owned command and configuration boundaries. Plans contain typed
// executable/argv and file operations; they never contain a shell program,
// provider credential, mailbox content, or calendar content.
package integrationlifecycle

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

const SchemaVersion = 1

type Operation string

const (
	OperationSetup  Operation = "setup"
	OperationRepair Operation = "repair"
	OperationRemove Operation = "remove"
)

type State string

const (
	StateAbsent       State = "absent"
	StateHealthy      State = "healthy"
	StateDisabled     State = "disabled"
	StateStalePath    State = "stale_path"
	StateVersionDrift State = "version_drift"
	StateNameConflict State = "name_conflict"
	StateMalformed    State = "malformed"
	StateUnreadable   State = "unreadable"
	StateUnavailable  State = "adapter_unavailable"
)

type Component string

const (
	ComponentMCP       Component = "mcp"
	ComponentSkill     Component = "skill"
	ComponentPlugin    Component = "plugin"
	ComponentExtension Component = "extension"
	ComponentPower     Component = "power"
	ComponentSource    Component = "package_source"
	ComponentStage     Component = "managed_package"
)

type ActionKind string

const (
	ActionCommand ActionKind = "command"
	ActionJSON    ActionKind = "json_merge"
	ActionYAML    ActionKind = "yaml_merge"
	ActionPackage ActionKind = "package_stage"
	ActionSkill   ActionKind = "skill_sync"
)

type Command struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"workingDirectory,omitempty"`
}

type FileChange struct {
	Path          string `json:"path"`
	Entry         string `json:"entry"`
	Normalization bool   `json:"normalizesFormatting"`
}

type Action struct {
	Kind    ActionKind     `json:"kind"`
	Purpose string         `json:"purpose"`
	Command *Command       `json:"command,omitempty"`
	File    *FileChange    `json:"file,omitempty"`
	Package *PackageChange `json:"package,omitempty"`
}

type PackageChange struct {
	Source         string    `json:"source"`
	Target         string    `json:"target"`
	Version        string    `json:"version"`
	Kind           Component `json:"kind"`
	SourceSHA256   string    `json:"sourceSha256"`
	PreviousSHA256 string    `json:"previousSha256"`
	Remove         bool      `json:"remove,omitempty"`
}

type ComponentInspection struct {
	Component       Component `json:"component"`
	State           State     `json:"state"`
	Version         string    `json:"version,omitempty"`
	ExpectedVersion string    `json:"expectedVersion,omitempty"`
	Fingerprint     string    `json:"fingerprint,omitempty"`
	Detail          string    `json:"detail"`
}

type Inspection struct {
	State       State                 `json:"state"`
	Scope       agenthost.Scope       `json:"scope"`
	Path        string                `json:"path,omitempty"`
	Fingerprint string                `json:"fingerprint,omitempty"`
	Detail      string                `json:"detail"`
	Components  []ComponentInspection `json:"components,omitempty"`
}

type Plan struct {
	SchemaVersion  int             `json:"schemaVersion"`
	RequestBinding string          `json:"requestBinding"`
	Operation      Operation       `json:"operation"`
	Host           agenthost.ID    `json:"host"`
	DisplayName    string          `json:"displayName"`
	Scope          agenthost.Scope `json:"scope"`
	ServerName     string          `json:"serverName"`
	Components     []Component     `json:"components"`
	Previous       Inspection      `json:"previous"`
	Actions        []Action        `json:"actions"`
	Verification   []Action        `json:"verification"`
	Rollback       []Action        `json:"rollback"`
	ReloadRequired bool            `json:"reloadRequired"`
	Blocked        bool            `json:"blocked"`
	Reason         string          `json:"reason,omitempty"`
}

type Request struct {
	Operation        Operation
	Host             agenthost.ID
	Scope            agenthost.Scope
	ServerName       string
	Executable       string
	Arguments        []string
	ProjectDirectory string
}

type ResultStatus string

const (
	ResultAppliedVerified ResultStatus = "applied_and_verified"
	ResultReloadRequired  ResultStatus = "applied_reload_required"
	ResultAlreadyCurrent  ResultStatus = "already_current"
	ResultAlreadyAbsent   ResultStatus = "already_absent"
	ResultSkipped         ResultStatus = "skipped_by_user"
	ResultBlocked         ResultStatus = "blocked_before_change"
	ResultFailedPreserved ResultStatus = "failed_previous_state_preserved"
	ResultFailedChanged   ResultStatus = "failed_after_change"
)

type Result struct {
	Host     agenthost.ID `json:"host"`
	Status   ResultStatus `json:"status"`
	Changed  bool         `json:"changed"`
	Verified bool         `json:"verified"`
	Message  string       `json:"message"`
}

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (request Request) Validate() error {
	switch request.Operation {
	case OperationSetup, OperationRepair, OperationRemove:
	default:
		return fmt.Errorf("unsupported integration operation %q", request.Operation)
	}
	if request.Host == "" {
		return errors.New("integration host is required")
	}
	if !serverNamePattern.MatchString(request.ServerName) {
		return errors.New("integration server name must contain only letters, numbers, underscores, and hyphens")
	}
	if !filepath.IsAbs(request.Executable) {
		return errors.New("corresync executable path must be absolute")
	}
	if filepath.Clean(request.Executable) != request.Executable {
		return errors.New("corresync executable path must be clean")
	}
	if request.Scope == "" {
		return errors.New("integration scope is required")
	}
	if request.Scope != agenthost.ScopeUser {
		if request.ProjectDirectory == "" || !filepath.IsAbs(request.ProjectDirectory) ||
			filepath.Clean(request.ProjectDirectory) != request.ProjectDirectory {
			return errors.New("non-user integration scope requires an explicit clean absolute project directory")
		}
	}
	for _, argument := range request.Arguments {
		if strings.IndexFunc(argument, func(r rune) bool { return r == 0 || r == '\r' || r == '\n' }) >= 0 {
			return errors.New("integration server argument contains a forbidden control character")
		}
	}
	return nil
}

func cloneCommand(command Command) Command {
	command.Arguments = slices.Clone(command.Arguments)
	return command
}

func commandAction(purpose string, command Command) Action {
	cloned := cloneCommand(command)
	return Action{Kind: ActionCommand, Purpose: purpose, Command: &cloned}
}
