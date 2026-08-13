# Corresync agent plugin

This plugin teaches compatible coding agents when and how to use the local
Corresync multi-account mail/calendar MCP server and declares that server over
stdio. It is a thin package: install the matching Corresync CLI separately and
make `corr` available on the host's `PATH`. The plugin contains no executable,
account, credential, mailbox data, private configuration, or bundled
authentication.

The combined plugin tree works with Claude Code, GitHub Copilot CLI, and VS
Code's documented Claude-plugin compatibility. Claude Code can install it from
this repository:

```console
claude plugin marketplace add nkiyohara/corresync
claude plugin install corresync@corresync
```

The plugin's `.mcp.json` and shared Agent Skill remove the separate MCP
registration step for a host that installs the complete native package. For a
config-only installation, or until a host lifecycle adapter installs the
package, use the explicit local setup command:

```console
corr mcp setup HOST
```

Start a new agent session, then ask:

```text
Check all my configured inboxes and calendars and summarize what needs attention.
```

See the project [MCP guide](../../docs/mcp.md) for the Codex plugin, the shared
Claude/Copilot/VS Code tree, Gemini CLI extension, Kiro Power, Claude Desktop
MCPB, config-only hosts, provider behavior, migration, and troubleshooting.

This package supports local agent sessions only. Hosted ChatGPT, Kiro Web, and
remote sandboxes cannot reach its local stdio server. Provider authentication
and write policy stay inside the local Corresync core; no tool is auto-approved.
