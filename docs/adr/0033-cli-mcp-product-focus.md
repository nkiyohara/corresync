# ADR 0033: CLI and MCP product focus

- Status: accepted
- Date: 2026-08-14
- Supersedes: the persistent terminal-workspace decision in
  [ADR 0019](0019-thin-terminal-workspace.md)

## Context

Corresync already exposes one typed application core through a command-oriented
CLI, stable JSON, and a local MCP server. The proposed persistent `corr ui`
workspace and a later native graphical workspace would add substantial
presentation, lifecycle, accessibility, packaging, and security work without
improving the provider-neutral contracts that automation clients need most.

The product still benefits from small human interactions. Guided setup,
account and provider selection, handoff to the consent-gated external
credential owner defined by
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md), settings
forms, and explicit confirmation are easier to understand as short-lived
terminal prompts than as long command lines. Those prompts are part of a CLI
command; they are not a persistent mail, calendar, or task workspace.

## Decision

Focus product development on CLI, JSON, and MCP surfaces. Do not implement or
roadmap a persistent `corr ui` Inbox, Agenda, event center, or native graphical
workspace. Reliability, projections, monitoring, and task features must be
designed first for the shared application use cases and exposed through CLI
and MCP where appropriate.

Retain bounded interactive modes inside explicit CLI commands, including
`corr setup`, `corr settings`, account/provider choice, reviewed credential
enrollment handoff, and local confirmation. An interactive CLI mode:

- starts only because the human invoked the corresponding command;
- is short-lived and completes, cancels, or fails back to the shell;
- calls the same typed application services as non-interactive CLI and MCP;
- owns no provider logic, durable mailbox state, policy, credentials, or
  approval authority;
- keeps a line-oriented accessible mode and deterministic non-interactive
  command or JSON alternatives where automation needs them;
- never turns untrusted provider content into instructions, key bindings,
  executable arguments, authorization, or terminal control and escape
  sequences.

Terminal form libraries may be used for these bounded prompts. Their presence
does not create a TUI product surface or justify application behavior that is
unavailable to CLI and MCP peers.

ADR 0019 remains a historical record of the security requirements that would
apply if a persistent terminal workspace were proposed again, but its decision
to add `corr ui` is no longer accepted. Reintroducing a persistent TUI or a GUI
requires a new product-scope ADR and explicit roadmap approval.

## Consequences

The former Terminal Workspace and AI-native GUI milestones are closed as not
planned. Work that is useful without those interfaces—such as session recovery,
bounded refresh, account-local projections, and opt-in monitoring—continues as
CLI/MCP work. UI-specific child issues are closed rather than kept as an
implicit future commitment.

The active next-minor milestone can describe MCP/CLI daily-driver reliability
without reserving a version for an abandoned interface. Documentation and
issue labels must distinguish a bounded interactive CLI prompt from a
persistent TUI so setup usability does not regress under this decision.
