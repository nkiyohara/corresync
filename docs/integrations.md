# Local agent-host integrations

Corresync keeps agent-host discovery separate from agent configuration. You can
inspect what is installed before deciding whether another application's files
or official CLI should change:

```console
corr integrations detect
corr integrations list
corr integrations show codex
corr integrations detect --json
```

Detection is local, read-only, content-free, and bounded. It checks the current
process's `PATH`, a fixed set of common user-local manager directories, known
desktop application locations, and the existence—not contents—of a small set
of documented configuration footprints. It does not execute an agent, source a
shell profile, enumerate extension directories, read conversation history, or
inspect credentials. `--refresh` bypasses an in-process cache used by the
guided coordinator; a standalone CLI process ordinarily starts with no cache.

## How to read a result

- `confirmed` means an executable or application installation was found.
- `probable` means only a known configuration footprint exists.
- `selected_missing` means a coordinator supplied a previously selected host
  that was not found in the current local, SSH, or WSL context.
- `not_found` means the bounded probes found no evidence. It does not prove the
  product is absent.
- `unsupported_surface` means the product was detected, but Corresync has no
  reviewed local integration for that surface.

Detection, support, connection, and packaging are independent fields. A
detected host is not necessarily connected. A host that supports MCP does not
necessarily consume an Agent Skill, native plugin, extension, Power, or MCPB.
Connection remains `not_inspected` until an explicit lifecycle operation reads
the minimum relevant host-owned state; that work is not performed by detect.

`verified` in the catalog means Corresync has a deterministic adapter or package
contract for that surface. It is not a claim that the current machine or every
upstream version has been live-observed. `experimental`, `config_only`, and
`catalog_only` make narrower support explicit.

`marketplaceSurface` means the host has a relevant marketplace or gallery
format; it does not claim that Corresync is listed there. Only the separate
`published` field can make that claim, and it remains false until the exact
package/version is externally published and post-verified.

## Catalog

The following matrix is generated from the same typed catalog used by the CLI,
existing `corr mcp setup` compatibility commands, and the guided coordinator.
CI fails if it drifts.

[Generated agent-host catalog](generated/agent-hosts.md)

Existing MCP setup remains backward compatible:

```console
corr mcp setup codex --dry-run
corr mcp setup codex
```

Those commands now take display names, official client executables, adapter
identity, and verification arguments from the catalog. They continue to use
an executable plus argv, never a shell command string. Multi-host planning,
repair, removal, and native bundle installation build on this catalog but have
their own explicit preview/apply boundary.

Absolute evidence locations can identify a local username or installation
layout. They are suitable for private local diagnostics and structured setup,
not for public issue reports. JSON contains no environment values, file
contents, tokens, provider credentials, mail, calendar data, or agent
conversations.
