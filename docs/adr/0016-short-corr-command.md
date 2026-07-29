# ADR 0016: Short `corr` command with a finite compatibility entry

- Status: accepted
- Date: 2026-07-28
- Supersedes: the primary-CLI and no-alias decisions in
  [ADR 0011](0011-coordinated-corresync-rename.md)

## Context

Corresync is a descriptive product and repository name, but it is unnecessarily
long for an interactive command used many times per day. Shorter alternatives
must still be recognizable and should avoid common names such as `csync` that
already collide with unrelated synchronization tools.

The v0.7 release already established `corresync` as the executable name.
Removing it immediately would strand scripts, MCP registrations, package
upgrades, and the v0.7 direct updater, which extracts an executable named
`corresync` from a Corresync release archive.

Product identity and command spelling are separate concerns. Configuration,
state, package, repository, plugin, MCP connection, registry, and application
identifiers benefit from the unambiguous `corresync` name and do not become
easier to use if shortened.

## Decision

The product remains **Corresync** and its canonical interactive command becomes
`corr`. The command source lives at `cmd/corr`; help, diagnostics, examples,
completion, manpages, MCP setup, and direct-update advice use `corr`.

Release archives and native packages also carry a `corresync` compatibility
entry built from the exact same Go package, version metadata, and compiler
flags. Package managers may implement that entry as a link to `corr`.
Compatibility does not receive a separate completion, manpage, or documentation
surface, so new configuration converges on `corr`.

The compatibility entry ships in the v0.8 and v0.9 release lines. It may be
removed no earlier than v0.10.0, after a stable release has announced removal.
The v0.7 updater-compatible archive filename remains
`corresync_<version>_<os>_<arch>`; the product, package, and release artifact
names also remain Corresync.

`corresync` remains correct for product-scoped identifiers, including:

- the repository and Go module;
- configuration, state, cache, browser-profile, and IPC directories;
- package-manager names;
- the MCP connection name and Registry ID;
- plugin, Skill, and website paths.

No `owa`, `owa-bridge`, or provider-specific public command or directory alias
is reintroduced by this decision.

Completion installation detects Bash, Zsh, or Fish and writes one canonical
`corr` completion file. Repeating installation compares exact contents and
does not append shell startup configuration or duplicate entries.

## Consequences

Fresh installations and documentation use a short command:

```text
corr config init
corr account list
corr mail list
corr update
```

Existing `corresync` invocations continue during a bounded migration window,
including a v0.7 direct update. Archives are temporarily larger because they
contain both executable names. Product-scoped paths remain stable, avoiding an
unrelated second state migration and duplicate session owners.
