# ADR 0024: Shared everyday settings boundary

- Status: accepted
- Date: 2026-08-03

## Context

Everyday configuration initially had two uneven surfaces: typed `config set`
commands and a numbered interactive menu. The menu duplicated validation and
did not teach direct commands. MCP could rename accounts through its existing
preview/commit boundary but could not inspect or change the update, safety,
default-account, or browser-login settings presented by the CLI.

Adding independent CLI and MCP implementations would allow dependency rules,
such as automatic installation requiring automatic checks, to drift. Direct
MCP writes would also bypass the caller-bound review and session-owner restart
used by other reversible configuration changes.

## Decision

Define a provider-neutral `SettingsService` in the application layer for the
bounded everyday settings set. It owns friendly key names, value
normalization, dependency rules, plain-language descriptions, equivalent CLI
commands, and optimistic previous-value checks. A local adapter projects and
atomically updates the complete validated TOML configuration.

The interactive `corr settings` surface uses Huh v2 for arrow-key selection,
validated input, Esc cancellation, and an explicit line-oriented accessible
mode. It calls the shared service for non-account settings and the existing
account lifecycle service for alias changes. Each visible choice includes its
current state, effect, or equivalent direct command.

MCP adds one read-only `settings_show` tool and the reversible-write pair
`settings_update` / `settings_update_commit`. Preview binds the normalized
review, including dependent changes and expected previous value, to the
existing short-lived caller-specific approval. Commit atomically rejects stale
reviews, writes the validated configuration, records the execution, and
restarts the sole local session owner. Alias changes remain under
`account_rename` because they preserve a stable account identity and already
have a dedicated lifecycle contract.

## Consequences

CLI and MCP now agree on values, validation, automatic dependencies, and error
messages. Interactive use is discoverable without making scripts depend on a
TUI, and screen readers retain a stable prompt mode. MCP can help with settings
without gaining an unreviewed configuration-write primitive.

Huh adds Bubble Tea and Bubbles as terminal-only dependencies. The application
service and MCP adapter do not depend on terminal packages, and the form's
input and output remain injectable for deterministic tests.
