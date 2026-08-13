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

Native package identity, the shared local stdio launch contract, thin versus
self-contained distribution, and config-only metadata are generated from a
separate reviewed bundle specification:

[Generated integration bundles](generated/integration-bundles.md)

`internal/integrationbundle/spec.json` is canonical public metadata. The
Codex/OpenAI and Claude/Copilot/VS Code manifests, `.mcp.json`, Gemini
extension, Kiro Power, config-only host metadata, and this matrix are generated
source snapshots. The Agent Skill remains the canonical workflow guidance.
Release builds render the exact tag version into a fresh staging tree; the
platform-universal MCPB and release `server.json` registry manifest use the
same identity and version source.

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

## Previewable lifecycle

Use one lifecycle for official client commands, documented JSON/JSONC files,
and goose YAML:

```console
corr integrations plan codex claude-code
corr integrations setup codex claude-code
corr integrations doctor
corr integrations repair codex
corr integrations remove codex
```

`plan` and `doctor` are read-only. Setup, repair, and removal print one plan
grouped by host and require a terminal confirmation; automation must pass the
explicit `--yes` flag. `--json` is always preview-only and is rejected with
`--yes`, so consuming a plan can never apply it implicitly. Project,
workspace, and local scope use an explicit `--project` root or the displayed
current working directory; Corresync never silently changes an unrelated
repository.

Each host completes independently. A later failure does not undo an earlier
verified host, and rerunning resumes only absent or stale components. Results
distinguish applied-and-verified, reload-required, already-current,
user-skipped, blocked-before-change, failed-with-previous-state-preserved, and
failed-after-change. A new host session or reload is normally required even
after structural verification.

Inspection classifies the exact named registration and managed package as
`absent`, `healthy`, `disabled`, `stale_path`, `version_drift`,
`name_conflict`, `malformed`, `unreadable`, or `adapter_unavailable`. It reads
no agent conversation, provider credential, mail, or calendar data. Host
command output is time- and size-bounded, retained only in memory for
classification, and replaced in plans by a content fingerprint and fixed
diagnostic text.

<!-- markdownlint-disable MD013 -->
| Host | Reviewed scope | Mutation surface | Skill/package behavior |
| --- | --- | --- | --- |
| Codex | user | official CLI | private local plugin marketplace; matching plugin Skill only |
| Claude Code | local, project, user | official CLI | private scoped marketplace; matching plugin Skill only |
| GitHub Copilot CLI | user | official CLI | local plugin containing the matching Skill only |
| Gemini CLI | user, project | official CLI | matching Skill extension at user scope; MCP-only at project scope |
| Qwen Code | user, project | official CLI | MCP only |
| Qoder | local, project, user | official CLI | MCP only |
| Kimi Code CLI | user | official CLI | MCP only |
| VS Code | user, workspace | documented JSON/JSONC | personal/workspace portable Skill |
| Cursor | user, project | documented JSON/JSONC | MCP only; no undocumented Skill path is guessed |
| Windsurf | user | documented JSON/JSONC | MCP only |
| OpenCode | user, project | documented JSON/JSONC | global/project portable Skill |
| Cline | user | documented JSON/JSONC | global portable Skill |
| Roo Code | project | documented JSON/JSONC | MCP only |
| Zed | user, project | documented JSON/JSONC | global/project portable Skill |
| goose | user | documented YAML | MCP extension only |
<!-- markdownlint-enable MD013 -->

Native Skill packages are rendered from the assets shipped beside the exact
`corr` installation, copied to a private Corresync-managed directory, and
installed through the host's official command. Their duplicate MCP declaration
is removed before staging: the active MCP registration always retains the
previewed absolute `corr` path and explicit secret-free Corresync config path.
Gemini's non-interactive `--consent` install flag is used only after Corresync
has displayed that exact package plan and received terminal confirmation (or
explicit `--yes`); it does not approve any MCP tool invocation.
Portable Skills use documented host locations, a private ownership/version
marker, compare-and-swap replacement, and reference tracking. Corresync never
overwrites or removes an unmarked user-authored Skill with the same name.

File adapters acquire a sidecar lock, validate the complete bounded document,
reject unsafe types, owners, modes, symlinks, schemas, and name conflicts, then
change only the named Corresync entry. JSON/JSONC writes normalize the whole
document and remove comments; goose YAML preserves comments where the YAML
library supports it. Writes use
a private same-filesystem temporary file, sync and atomic replacement, and one
permission-preserving `.corresync.bak` recovery copy. No adapter writes a
credential, secret, environment token, or host-level auto-approval rule.

On Windows, lifecycle paths inherit the signed-in user's filesystem ACLs;
Go's synthesized Unix owner and mode bits are not treated as ACL evidence.
Corresync still rejects every reparse point (including directory junctions),
unsafe file types, and post-preview changes. Directory metadata cannot be
flushed portably on Windows; after a crash, `doctor` and `repair` re-inspect
the actual state, and the bounded managed recovery copy remains available.

Absolute evidence locations can identify a local username or installation
layout. They are suitable for private local diagnostics and structured setup,
not for public issue reports. JSON contains no environment values, file
contents, tokens, provider credentials, mail, calendar data, or agent
conversations.
