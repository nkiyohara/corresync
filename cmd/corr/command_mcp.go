package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/integrationbundle"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
	"github.com/nkiyohara/corresync/internal/mcpserver"
)

var (
	_ mcpserver.Backend          = (*daemonMCPBackend)(nil)
	_ mcpserver.MessagingBackend = (*daemonMCPBackend)(nil)
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
	KimiCode      mcpKimiCodeSetupCommand      `cmd:"" name:"kimi-code" help:"Register with Kimi Code using kimi mcp add."`
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

type mcpKimiCodeSetupCommand struct{ mcpSetupFlags }

type codexMCPDocument = integrationlifecycle.CodexDocument
type jsonMCPDocument = integrationlifecycle.JSONDocument

var mcpClientNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (command *mcpCodexConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDCodex, command.mcpClientConfigFlags)
}

func (command *mcpClaudeCodeConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDClaudeCode, command.mcpClientConfigFlags)
}

func (command *mcpGitHubCopilotConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDGitHubCopilot, command.mcpClientConfigFlags)
}

func (command *mcpGeminiCLIConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDGeminiCLI, command.mcpClientConfigFlags)
}

func (command *mcpQwenCodeConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDQwenCode, command.mcpClientConfigFlags)
}

func (command *mcpQoderConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDQoder, command.mcpClientConfigFlags)
}

func (command *mcpKimiCodeConfigCommand) Run(app *runtime) error {
	return writeRenderedMCPConfig(app, agenthost.IDKimiCode, command.mcpClientConfigFlags)
}

func writeRenderedMCPConfig(app *runtime, host agenthost.ID, flags mcpClientConfigFlags) error {
	name, executable, arguments, err := resolveMCPClientConfig(app, flags.Name, flags.Executable)
	if err != nil {
		return err
	}
	encoded, err := integrationlifecycle.RenderConfig(host, name, executable, arguments)
	if err != nil {
		return err
	}
	_, err = app.stdout.Write(encoded)
	return err
}

func (command *mcpCodexSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDCodex, agenthost.ScopeUser, name, executable, arguments, command.DryRun)
}

func (command *mcpClaudeCodeSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDClaudeCode, agenthost.Scope(command.Scope), name, executable, arguments, command.DryRun)
}

func (command *mcpGitHubCopilotSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDGitHubCopilot, agenthost.ScopeUser, name, executable, arguments, command.DryRun)
}

func (command *mcpGeminiCLISetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDGeminiCLI, agenthost.Scope(command.Scope), name, executable, arguments, command.DryRun)
}

func (command *mcpQwenCodeSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDQwenCode, agenthost.Scope(command.Scope), name, executable, arguments, command.DryRun)
}

func (command *mcpQoderSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDQoder, agenthost.Scope(command.Scope), name, executable, arguments, command.DryRun)
}

func (command *mcpKimiCodeSetupCommand) Run(app *runtime) error {
	name, executable, arguments, err := resolveMCPSetup(app, command.Name, command.Executable)
	if err != nil {
		return err
	}
	return applyCatalogMCPSetup(app, agenthost.IDKimiCode, agenthost.ScopeUser, name, executable, arguments, command.DryRun)
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
	executable, err = verifyIntegrationExecutable(executable)
	if err != nil {
		return "", "", nil, err
	}
	return name, executable, arguments, nil
}

type mcpSetupClient struct {
	Label   string
	Command string
	Verify  []string
}

func applyCatalogMCPSetup(
	app *runtime,
	hostID agenthost.ID,
	scope agenthost.Scope,
	name string,
	executable string,
	serverArguments []string,
	dryRun bool,
) error {
	host, ok := app.agentHosts.Lookup(string(hostID))
	if !ok {
		return fmt.Errorf("agent-host catalog is missing %q", hostID)
	}
	projectDirectory := ""
	var err error
	if scope != agenthost.ScopeUser {
		projectDirectory, err = app.workingDirectory()
		if err != nil {
			return fmt.Errorf("resolve project directory: %w", err)
		}
		projectDirectory, err = filepath.Abs(projectDirectory)
		if err != nil {
			return fmt.Errorf("resolve project directory: %w", err)
		}
		projectDirectory = filepath.Clean(projectDirectory)
	}
	request := integrationlifecycle.Request{
		Operation: integrationlifecycle.OperationSetup,
		Host:      hostID, Scope: scope, ServerName: name,
		Executable: executable, Arguments: serverArguments, ProjectDirectory: projectDirectory,
	}
	add, verify, _, _, ok, err := integrationlifecycle.OfficialCommands(request)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("agent host %q has no verified setup adapter", hostID)
	}
	return applyMCPSetup(app, mcpSetupClient{
		Label: host.DisplayName, Command: add.Executable, Verify: verify.Arguments,
	}, name, add.Arguments, dryRun)
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
	launch := integrationbundle.PortableLaunch()
	arguments := append([]string{"--config", configPath}, launch.Args...)
	return name, filepath.Clean(absoluteExecutable), arguments, nil
}
