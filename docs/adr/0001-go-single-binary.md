# ADR 0001: Go and a single distributable binary

- Status: accepted
- Date: 2026-07-17
- Amended: 2026-07-28 by
  [ADR 0016](0016-short-corr-command.md)

## Context

The project needs a cross-platform CLI, local daemon, browser session manager,
and MCP server. Installation must not require users to manage a language runtime
or a dependency tree.

## Decision

Use supported Go releases and produce one `corr` primary binary. During the
finite command transition defined by ADR 0016, the same command package also
produces an identical `corresync` compatibility entry. Use the official MCP Go
SDK. Keep browser control behind an interface so its CDP implementation can be
changed without affecting commands or tool contracts.

## Consequences

GitHub Releases can provide native archives for macOS, Linux, and Windows. The
same artifact can implement CLI, daemon, and MCP subcommands. The project accepts
the cost of implementing polished terminal behavior without a Node or Python
runtime.
