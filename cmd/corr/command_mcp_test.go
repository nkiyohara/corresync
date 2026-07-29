package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
)

func TestMCPConfigGeneratorsProduceNativeClientDocuments(t *testing.T) {
	t.Parallel()

	executable := filepath.Join(t.TempDir(), "corresync")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	tests := []struct {
		name      string
		arguments []string
		decode    func(*testing.T, []byte)
	}{
		{
			name: "Codex TOML",
			arguments: []string{
				"--config", configPath, "mcp", "config", "codex",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document codexMCPDocument
				if err := toml.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Codex document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Command != executable || server.DefaultApproval != "writes" || server.ToolTimeout != 360 {
					t.Fatalf("unexpected Codex server: %+v", server)
				}
				if len(server.Arguments) != 4 || server.Arguments[1] != configPath {
					t.Fatalf("unexpected Codex arguments: %#v", server.Arguments)
				}
			},
		},
		{
			name: "Claude Code JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "claude-code",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Claude Code document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Type != "stdio" || server.Command != executable {
					t.Fatalf("unexpected Claude Code server: %+v", server)
				}
			},
		},
		{
			name: "GitHub Copilot CLI JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "github-copilot",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode GitHub Copilot CLI document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Type != "stdio" || !reflect.DeepEqual(server.Tools, []string{"*"}) || server.TimeoutMS != 360_000 {
					t.Fatalf("unexpected GitHub Copilot CLI server: %+v", server)
				}
			},
		},
		{
			name: "Gemini CLI JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "gemini-cli",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Gemini CLI document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Command != executable || server.Description == "" || server.TimeoutMS != 360_000 {
					t.Fatalf("unexpected Gemini CLI server: %+v", server)
				}
			},
		},
		{
			name: "Qwen Code JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "qwen-code",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Qwen Code document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Command != executable || server.Type != "" || server.Enabled != nil {
					t.Fatalf("unexpected Qwen Code server: %+v", server)
				}
			},
		},
		{
			name: "Qoder JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "qoder",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Qoder document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Command != executable || len(server.Arguments) != 4 {
					t.Fatalf("unexpected Qoder server: %+v", server)
				}
			},
		},
		{
			name: "Kimi Code JSON",
			arguments: []string{
				"--config", configPath, "mcp", "config", "kimi-code",
				"--name", "outlook_work", "--executable", executable,
			},
			decode: func(t *testing.T, data []byte) {
				t.Helper()
				var document jsonMCPDocument
				if err := json.Unmarshal(data, &document); err != nil {
					t.Fatalf("decode Kimi Code document: %v", err)
				}
				server := document.Servers["outlook_work"]
				if server.Enabled == nil || !*server.Enabled || server.ToolTimeoutMS != 360_000 {
					t.Fatalf("unexpected Kimi Code server: %+v", server)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := run(context.Background(), test.arguments, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
			}
			test.decode(t, stdout.Bytes())
		})
	}
}

func TestMCPConfigRejectsUnsafeClientName(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &stderr, buildinfo.Current())
	command := mcpCodexConfigCommand{mcpClientConfigFlags: mcpClientConfigFlags{Name: "bad.name"}}
	if err := command.Run(app); err == nil {
		t.Fatal("Run() unexpectedly accepted unsafe MCP client name")
	}
}

func TestMCPSetupUsesOfficialClientCommands(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	executable := filepath.Join(t.TempDir(), "corresync")
	if err := os.WriteFile(executable, []byte("test executable"), 0o600); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	tests := []struct {
		name        string
		client      string
		wantArgs    []string
		run         func(*runtime) error
		wantMessage string
	}{
		{
			name:   "Codex",
			client: "codex",
			wantArgs: []string{
				"mcp", "add", "outlook_work", "--", executable,
				"--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpCodexSetupCommand{mcpSetupFlags: mcpSetupFlags{
					mcpClientConfigFlags: mcpClientConfigFlags{Name: "outlook_work", Executable: executable},
				}}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with Codex",
		},
		{
			name:   "Claude Code",
			client: "claude",
			wantArgs: []string{
				"mcp", "add", "--scope", "project", "outlook_work", "--", executable,
				"--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpClaudeCodeSetupCommand{
					mcpSetupFlags: mcpSetupFlags{mcpClientConfigFlags: mcpClientConfigFlags{
						Name: "outlook_work", Executable: executable,
					}},
					Scope: "project",
				}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with Claude Code",
		},
		{
			name:   "GitHub Copilot CLI",
			client: "copilot",
			wantArgs: []string{
				"mcp", "add", "outlook_work",
				"--type", "stdio", "--tools", "*", "--timeout", "360000",
				"--", executable, "--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpGitHubCopilotSetupCommand{mcpSetupFlags: mcpSetupFlags{
					mcpClientConfigFlags: mcpClientConfigFlags{Name: "outlook_work", Executable: executable},
				}}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with GitHub Copilot CLI",
		},
		{
			name:   "Gemini CLI",
			client: "gemini",
			wantArgs: []string{
				"mcp", "add", "--scope", "project",
				"--description", "Local-first guarded mail and calendar",
				"--timeout", "360000", "outlook_work", executable, "--",
				"--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpGeminiCLISetupCommand{
					mcpSetupFlags: mcpSetupFlags{mcpClientConfigFlags: mcpClientConfigFlags{
						Name: "outlook_work", Executable: executable,
					}},
					Scope: "project",
				}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with Gemini CLI",
		},
		{
			name:   "Qwen Code",
			client: "qwen",
			wantArgs: []string{
				"mcp", "add", "--scope", "project",
				"--description", "Local-first guarded mail and calendar",
				"outlook_work", executable,
				"--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpQwenCodeSetupCommand{
					mcpSetupFlags: mcpSetupFlags{mcpClientConfigFlags: mcpClientConfigFlags{
						Name: "outlook_work", Executable: executable,
					}},
					Scope: "project",
				}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with Qwen Code",
		},
		{
			name:   "Qoder",
			client: "qodercli",
			wantArgs: []string{
				"mcp", "add", "-s", "user", "outlook_work", "--", executable,
				"--config", configPath, "mcp", "serve",
			},
			run: func(app *runtime) error {
				command := mcpQoderSetupCommand{
					mcpSetupFlags: mcpSetupFlags{mcpClientConfigFlags: mcpClientConfigFlags{
						Name: "outlook_work", Executable: executable,
					}},
					Scope: "user",
				}
				return command.Run(app)
			},
			wantMessage: "Registered MCP server \"outlook_work\" with Qoder",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app := newRuntime(t.Context(), configPath, &stdout, &stderr, buildinfo.Current())
			var gotClient string
			var gotArgs []string
			app.runCommand = func(
				_ context.Context,
				_, _ io.Writer,
				name string,
				arguments ...string,
			) error {
				gotClient = name
				gotArgs = append([]string(nil), arguments...)
				return nil
			}
			if err := test.run(app); err != nil {
				t.Fatalf("setup Run() error = %v", err)
			}
			if gotClient != test.client || !reflect.DeepEqual(gotArgs, test.wantArgs) {
				t.Fatalf("client invocation = %q %#v, want %q %#v", gotClient, gotArgs, test.client, test.wantArgs)
			}
			if !strings.Contains(stdout.String(), test.wantMessage) {
				t.Fatalf("stdout = %q, want message %q", stdout.String(), test.wantMessage)
			}
			if !strings.Contains(stdout.String(), "Start a new ") {
				t.Fatalf("stdout = %q, want fresh-session guidance", stdout.String())
			}
		})
	}
}

func TestMCPConfigDefaultsToDescriptiveServerName(t *testing.T) {
	t.Parallel()

	executable := filepath.Join(t.TempDir(), "corresync")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(t.Context(), []string{
		"--config", configPath, "mcp", "config", "qwen-code", "--executable", executable,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	var document jsonMCPDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document.Servers["corresync"]; !exists {
		t.Fatalf("default server name missing from %#v", document.Servers)
	}
}

func TestMCPSetupDryRunDoesNotInvokeClient(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatalf("save config: %v", err)
	}
	executable := filepath.Join(t.TempDir(), "corresync binary")
	if err := os.WriteFile(executable, []byte("test executable"), 0o600); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(t.Context(), []string{
		"--config", configPath,
		"mcp", "setup", "codex",
		"--name", "work_mail",
		"--executable", executable,
		"--dry-run",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "codex mcp add work_mail -- \"") || !strings.Contains(got, "mcp serve") {
		t.Fatalf("dry-run output = %q", got)
	}
}

func TestMCPSetupRequiresInitializedConfiguration(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(t.Context(), filepath.Join(t.TempDir(), "missing.toml"), &stdout, &stderr, buildinfo.Current())
	command := mcpCodexSetupCommand{mcpSetupFlags: mcpSetupFlags{
		mcpClientConfigFlags: mcpClientConfigFlags{Name: "corresync", Executable: os.Args[0]},
	}}
	if err := command.Run(app); err == nil || !strings.Contains(err.Error(), "corr config init") {
		t.Fatalf("Run() error = %v, want config initialization guidance", err)
	}
}
