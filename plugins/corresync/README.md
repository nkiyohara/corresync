# Corresync agent plugin

This plugin teaches compatible coding agents when and how to use the local
Corresync multi-account mail/calendar MCP server. It contains no credentials,
mailbox data, or bundled authentication.

Register the MCP server first:

```console
corr mcp setup codex
# or: corr mcp setup claude-code
# or: corr mcp setup github-copilot
# or: corr mcp setup gemini-cli
# or: corr mcp setup qwen-code
# or: corr mcp setup qoder
```

Claude Code can install the shared Skill from this repository:

```console
claude plugin marketplace add nkiyohara/corresync
claude plugin install corresync@corresync
```

Start a new agent session, then ask:

```text
Check all my configured inboxes and calendars and summarize what needs attention.
```

See the project [MCP guide](../../docs/mcp.md) for Codex, Claude Code, GitHub
Copilot CLI, Gemini CLI, Qwen Code, Qoder, Kimi Code CLI, manual configuration,
provider behavior, migration, and troubleshooting. The plugin adds guidance
only; provider authentication and write policy stay inside the local Corresync
core.
