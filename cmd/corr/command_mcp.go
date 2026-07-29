package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/mcpserver"
)

type mcpCommand struct {
	Serve  mcpServeCommand  `cmd:"" help:"Run the local MCP server."`
	Config mcpConfigCommand `cmd:"" help:"Generate client configuration without changing client files."`
	Setup  mcpSetupCommand  `cmd:"" help:"Register the server through a client's official CLI."`
}

type mcpServeCommand struct {
	Stdio bool `default:"true" help:"Use newline-delimited JSON over stdin/stdout."`
}

func (command *mcpServeCommand) Run(app *runtime) (returnErr error) {
	if !command.Stdio {
		return errors.New("stdio is the only supported MCP transport")
	}
	backend, err := newDaemonMCPBackend(app)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, backend.Close()) }()
	server, err := mcpserver.New(backend, mcpserver.Options{
		Version:  app.info.Version,
		Instance: fmt.Sprintf("process-%d", app.processID),
	})
	if err != nil {
		return err
	}
	return server.Run(app.context, &mcp.StdioTransport{})
}

type mcpConfigCommand struct {
	Codex         mcpCodexConfigCommand         `cmd:"" help:"Print a Codex config.toml fragment."`
	ClaudeCode    mcpClaudeCodeConfigCommand    `cmd:"" name:"claude-code" help:"Print a Claude Code MCP JSON document."`
	GitHubCopilot mcpGitHubCopilotConfigCommand `cmd:"" name:"github-copilot" help:"Print a GitHub Copilot CLI MCP JSON document."`
	GeminiCLI     mcpGeminiCLIConfigCommand     `cmd:"" name:"gemini-cli" help:"Print a Gemini CLI settings.json fragment."`
	QwenCode      mcpQwenCodeConfigCommand      `cmd:"" name:"qwen-code" help:"Print a Qwen Code settings.json fragment."`
	Qoder         mcpQoderConfigCommand         `cmd:"" help:"Print a Qoder project MCP JSON document."`
	KimiCode      mcpKimiCodeConfigCommand      `cmd:"" name:"kimi-code" help:"Print a Kimi Code mcp.json document."`
}

type mcpSetupCommand struct {
	Codex         mcpCodexSetupCommand         `cmd:"" help:"Register with Codex using codex mcp add."`
	ClaudeCode    mcpClaudeCodeSetupCommand    `cmd:"" name:"claude-code" help:"Register with Claude Code using claude mcp add."`
	GitHubCopilot mcpGitHubCopilotSetupCommand `cmd:"" name:"github-copilot" help:"Register with GitHub Copilot CLI using copilot mcp add."`
	GeminiCLI     mcpGeminiCLISetupCommand     `cmd:"" name:"gemini-cli" help:"Register with Gemini CLI using gemini mcp add."`
	QwenCode      mcpQwenCodeSetupCommand      `cmd:"" name:"qwen-code" help:"Register with Qwen Code using qwen mcp add."`
	Qoder         mcpQoderSetupCommand         `cmd:"" help:"Register with Qoder using qodercli mcp add."`
}

type mcpClientConfigFlags struct {
	Name       string `default:"corresync" help:"Client-side MCP server name."`
	Executable string `type:"path" help:"Corresync executable path; defaults to this process."`
}

type mcpCodexConfigCommand struct{ mcpClientConfigFlags }
type mcpClaudeCodeConfigCommand struct{ mcpClientConfigFlags }
type mcpGitHubCopilotConfigCommand struct{ mcpClientConfigFlags }
type mcpGeminiCLIConfigCommand struct{ mcpClientConfigFlags }
type mcpQwenCodeConfigCommand struct{ mcpClientConfigFlags }
type mcpQoderConfigCommand struct{ mcpClientConfigFlags }
type mcpKimiCodeConfigCommand struct{ mcpClientConfigFlags }

type mcpSetupFlags struct {
	mcpClientConfigFlags
	DryRun bool `help:"Print the official client command without running it."`
}

type mcpCodexSetupCommand struct{ mcpSetupFlags }

type mcpGitHubCopilotSetupCommand struct{ mcpSetupFlags }

type mcpGeminiCLISetupCommand struct {
	mcpSetupFlags
	Scope string `default:"user" enum:"project,user" help:"Gemini CLI configuration scope."`
}

type mcpClaudeCodeSetupCommand struct {
	mcpSetupFlags
	Scope string `default:"user" enum:"local,project,user" help:"Claude Code configuration scope."`
}

type mcpQwenCodeSetupCommand struct {
	mcpSetupFlags
	Scope string `default:"user" enum:"project,user" help:"Qwen Code configuration scope."`
}

type mcpQoderSetupCommand struct {
	mcpSetupFlags
	Scope string `default:"user" enum:"local,project,user" help:"Qoder configuration scope."`
}

type codexMCPDocument struct {
	Servers map[string]codexMCPServer `toml:"mcp_servers"`
}

type codexMCPServer struct {
	Command         string   `toml:"command"`
	Arguments       []string `toml:"args"`
	StartupTimeout  int      `toml:"startup_timeout_sec"`
	ToolTimeout     int      `toml:"tool_timeout_sec"`
	DefaultApproval string   `toml:"default_tools_approval_mode"`
	Enabled         bool     `toml:"enabled"`
	Required        bool     `toml:"required"`
}

type jsonMCPDocument struct {
	Servers map[string]jsonMCPServer `json:"mcpServers"`
}

type jsonMCPServer struct {
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

var mcpClientNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (command *mcpCodexConfigCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPClientConfig(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	document := codexMCPDocument{Servers: map[string]codexMCPServer{
		name: {
			Command:         executable,
			Arguments:       arguments,
			StartupTimeout:  30,
			ToolTimeout:     360,
			DefaultApproval: "writes",
			Enabled:         true,
			Required:        false,
		},
	}}
	encoded, err := toml.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode Codex MCP config: %w", err)
	}
	_, err = app.stdout.Write(encoded)
	return err
}

func (command *mcpClaudeCodeConfigCommand) Run(app *runtime) error {
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{Type: "stdio"})
}

func (command *mcpGitHubCopilotConfigCommand) Run(app *runtime) error {
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{
		Type:      "stdio",
		Tools:     []string{"*"},
		TimeoutMS: 360_000,
	})
}

func (command *mcpGeminiCLIConfigCommand) Run(app *runtime) error {
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{
		Description: "Local-first guarded mail and calendar",
		TimeoutMS:   360_000,
	})
}

func (command *mcpQwenCodeConfigCommand) Run(app *runtime) error {
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{})
}

func (command *mcpQoderConfigCommand) Run(app *runtime) error {
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{})
}

func (command *mcpKimiCodeConfigCommand) Run(app *runtime) error {
	enabled := true
	return writeJSONMCPConfig(app, command.mcpClientConfigFlags, jsonMCPServer{
		Enabled:          &enabled,
		StartupTimeoutMS: 30_000,
		ToolTimeoutMS:    360_000,
	})
}

func writeJSONMCPConfig(app *runtime, flags mcpClientConfigFlags, server jsonMCPServer) error {
	name, executable, arguments, err := resolveMCPClientConfig(app, flags.Name, flags.Executable)
	if err != nil {
		return err
	}
	server.Command = executable
	server.Arguments = arguments
	document := jsonMCPDocument{Servers: map[string]jsonMCPServer{name: server}}
	encoder := json.NewEncoder(app.stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func (command *mcpCodexSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 5+len(arguments))
	clientArguments = append(clientArguments, "mcp", "add", name, "--", executable)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "Codex", Command: "codex", Verify: []string{"mcp", "get", name},
	}, name, clientArguments, command.DryRun)
}

func (command *mcpClaudeCodeSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 7+len(arguments))
	clientArguments = append(clientArguments, "mcp", "add", "--scope", command.Scope, name, "--", executable)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "Claude Code", Command: "claude", Verify: []string{"mcp", "get", name},
	}, name, clientArguments, command.DryRun)
}

func (command *mcpGitHubCopilotSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 11+len(arguments))
	clientArguments = append(clientArguments,
		"mcp", "add", name,
		"--type", "stdio",
		"--tools", "*",
		"--timeout", "360000",
		"--", executable,
	)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "GitHub Copilot CLI", Command: "copilot", Verify: []string{"mcp", "get", name},
	}, name, clientArguments, command.DryRun)
}

func (command *mcpGeminiCLISetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 11+len(arguments))
	clientArguments = append(clientArguments,
		"mcp", "add",
		"--scope", command.Scope,
		"--description", "Local-first guarded mail and calendar",
		"--timeout", "360000",
		name, executable, "--",
	)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "Gemini CLI", Command: "gemini", Verify: []string{"mcp", "list"},
	}, name, clientArguments, command.DryRun)
}

func (command *mcpQwenCodeSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 8+len(arguments))
	clientArguments = append(clientArguments,
		"mcp", "add", "--scope", command.Scope,
		"--description", "Local-first guarded mail and calendar",
		name, executable,
	)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "Qwen Code", Command: "qwen", Verify: []string{"mcp", "list"},
	}, name, clientArguments, command.DryRun)
}

func (command *mcpQoderSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	clientArguments := make([]string, 0, 7+len(arguments))
	clientArguments = append(clientArguments, "mcp", "add", "-s", command.Scope, name, "--", executable)
	clientArguments = append(clientArguments, arguments...)
	return applyMCPSetup(app, mcpSetupClient{
		Label: "Qoder", Command: "qodercli", Verify: []string{"mcp", "list"},
	}, name, clientArguments, command.DryRun)
}

func resolveMCPSetup(app *runtime, name, executable string) (string, string, []string, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", "", nil, fmt.Errorf(
			"load Corresync configuration before MCP setup (run `corr setup <email-address>` first): %w",
			err,
		)
	}
	if len(configuration.Accounts) == 0 {
		return "", "", nil, errors.New(
			"configure an account before MCP setup; run `corr setup <email-address>` first",
		)
	}
	name, executable, arguments, err := resolveMCPClientConfig(app, name, executable)
	if err != nil {
		return "", "", nil, err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspect Corresync executable %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", nil, fmt.Errorf("configured executable is not a regular file: %s", executable)
	}
	return name, executable, arguments, nil
}

type mcpSetupClient struct {
	Label   string
	Command string
	Verify  []string
}

func applyMCPSetup(app *runtime, client mcpSetupClient, name string, arguments []string, dryRun bool) error {
	if dryRun {
		_, err := fmt.Fprintf(app.stdout, "%s\n", formatCommand(client.Command, arguments))
		return err
	}
	if err := app.runCommand(app.context, app.stdout, app.stderr, client.Command, arguments...); err != nil {
		return fmt.Errorf("register MCP server with %s: %w", client.Label, err)
	}
	_, err := fmt.Fprintf(
		app.stdout,
		"Registered MCP server %q with %s.\nVerify with `%s`.\nStart a new %s session before asking it to use mail or calendar; existing sessions may retain their original tool catalog.\n",
		name,
		client.Label,
		formatCommand(client.Command, client.Verify),
		client.Label,
	)
	return err
}

func formatCommand(name string, arguments []string) string {
	parts := make([]string, 0, len(arguments)+1)
	parts = append(parts, quoteCommandArgument(name))
	for _, argument := range arguments {
		parts = append(parts, quoteCommandArgument(argument))
	}
	return strings.Join(parts, " ")
}

func quoteCommandArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\\\"'") {
		return value
	}
	return strconv.Quote(value)
}

func resolveMCPClientConfig(app *runtime, name, executable string) (string, string, []string, error) {
	if !mcpClientNamePattern.MatchString(name) {
		return "", "", nil, errors.New("MCP client name must contain only letters, numbers, underscores, and hyphens")
	}
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return "", "", nil, fmt.Errorf("resolve Corresync executable: %w", err)
		}
		executable = resolved
	}
	absoluteExecutable, err := filepath.Abs(executable)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve Corresync executable: %w", err)
	}
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return "", "", nil, err
	}
	arguments := []string{"--config", configPath, "mcp", "serve"}
	return name, filepath.Clean(absoluteExecutable), arguments, nil
}
