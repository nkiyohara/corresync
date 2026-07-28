# Corresync agent plugin

This plugin teaches compatible coding agents when and how to use the local
Corresync mail and calendar MCP server. It contains no credentials,
mailbox data, or bundled authentication.

Register the MCP server first:

```console
corresync mcp setup codex
# or: corresync mcp setup claude-code
# or: corresync mcp setup github-copilot
# or: corresync mcp setup gemini-cli
# or: corresync mcp setup qwen-code
# or: corresync mcp setup qoder
```

Claude Code can install the shared Skill from this repository:

```console
claude plugin marketplace add nkiyohara/corresync
claude plugin install corresync@corresync
```

Start a new agent session, then simply ask:

```text
Check Outlook and summarize the messages that need my attention.
```

See the project [MCP guide](../../docs/mcp.md) for Codex, Claude Code, GitHub
Copilot CLI, Gemini CLI, Qwen Code, Qoder, Kimi Code CLI, manual
configuration, migration, and troubleshooting.
