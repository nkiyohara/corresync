# Architecture

## Goals

`owa-bridge` gives a signed-in user a coherent Outlook Web automation surface:

- a discoverable CLI for people and shell scripts;
- an MCP server for Claude Code, Codex, and other compatible clients;
- mail and calendar operations with identical behavior on both surfaces;
- explicit, deterministic safety controls around side effects;
- a browser-owned session that preserves the organization's normal sign-in
  controls.

It does not provide a hosted relay, multi-user server, Graph compatibility
layer, Teams collaboration surface, or a mechanism for bypassing access
policy. A Teams join link provisioned as part of one Outlook calendar event is
calendar scope; see [ADR 0005](adr/0005-calendar-hosted-teams-links.md).

## System context

```text
┌───────────────────── local machine ─────────────────────┐
│                                                         │
│  CLI ───────┐                                           │
│             ├─ application ─ policy ─ transport ─────┐  │
│  MCP ───────┘          │                       │      │  │
│                        ├─ audit                 │      │  │
│                        └─ capabilities          │      │  │
│                                                │      │  │
│  dedicated browser profile ─ session owner ────┘      │  │
│                                                       │  │
└───────────────────────────────────────────────────────┼──┘
                                                        │ TLS
                                                Outlook Web
```

## Dependency rule

Dependencies point inward:

```text
adapters (CLI, MCP) -> application -> domain
transport (OWA)    -> application ports
platform (browser, IPC, keyring) -> application ports
```

The domain package cannot import browser, protocol, CLI, MCP, persistence, or
operating-system packages. A command is represented once as typed input and
output. Adapters translate, but do not contain business behavior.

## Runtime topology

The long-lived local daemon owns the browser and authenticated session. CLI and
MCP processes communicate with it over operating-system IPC. This gives the
project one session owner, prevents competing browser profiles, and keeps
session material out of agent processes.

Each absolute config path and state directory derives a separate, opaque daemon
namespace. Linux and macOS use a Unix socket protected by a non-blocking
singleton lock, owner-only mode, and same-effective-user peer credentials.
Windows uses a byte-mode named pipe restricted by ACL to the current user and
SYSTEM, with remote clients rejected. Both transports also require a rotating
256-bit credential from an owner-only state file.

The wire format is strict, versioned JSON over HTTP semantics on that local
stream. It has a closed method registry, bounded request/response bodies,
bounded concurrency, no redirects, and no automatic retry of application
operations. Clients also reject a stale config digest or different executable
version before invoking mailbox operations. When an installed binary changes
but the exact config digest does not, the next client may inspect and gracefully
stop the authenticated old owner through stable lifecycle controls before
starting the current binary. The stop is bound to the inspected credential
generation, and the old browser closes before its singleton lock is released.
Mail, calendar, login, preview, and commit calls never use that compatibility
path. It never binds TCP. See [ADR
0003](adr/0003-authenticated-local-session-owner.md).

The default MCP transport is stdio. Optional Streamable HTTP support may be
added for advanced local deployments, but must bind to loopback, validate the
`Origin` header, and require authentication.

## Release update boundary

Interactive CLI startup may read cached public stable-release metadata and
display a TTY-only notice. It never updates in the background. The explicit
`owa update` command follows the detected installation owner: package-managed
installs receive their exact external upgrade command, while a direct install
uses the signed, rollback-capable flow in [ADR
0007](adr/0007-explicit-verified-self-update.md). MCP, daemon, completion,
machine-readable output, and Outlook application use cases do not enter this
local release-management path.

## Session lifecycle

1. `owa auth login` launches a dedicated browser profile visibly by default;
   `--terminal` explicitly selects a bounded text relay for an SSH TTY.
2. The user completes the normal interactive sign-in flow in the browser or by
   relaying controls and individual keys to its headless page.
3. The session owner observes only the minimum first-party request metadata
   needed to execute Outlook Web operations.
4. Short-lived authorization material remains in memory whenever possible.
5. The browser profile is stored using Chromium's platform protections. The
   project never stores a username, password, or refresh token in its config.
   The terminal relay never receives a complete form value.
6. Expiry causes an explicit transition back to `needs_login`; it never falls
   back to credential automation.
7. `owa auth status` exposes only account aliases and content-free lifecycle
   state. `owa auth logout` shuts down the config-scoped owner, closes all
   browsers, and discards in-memory sessions and approvals.

## OWA transport

OWA is an undocumented, changeable protocol. It is therefore implemented as a
replaceable adapter with:

- capability discovery instead of version assumptions;
- typed operations rather than a public arbitrary-action escape hatch;
- captured, redacted fixtures for deterministic contract tests;
- bounded retries that distinguish idempotent reads from writes;
- request identifiers and postcondition checks for ambiguous write outcomes;
- protocol diagnostics that never log credentials or message bodies by
  default.

The preferred operation family is OWA's current `service.svc` surface. Any use
of a legacy Outlook REST endpoint must be isolated behind a separate capability
and must not be required for core behavior.

## Safety model

Every use case declares an effect class:

| Class | Examples | Default behavior |
| --- | --- | --- |
| Read | search, message metadata, agenda | execute |
| Sensitive read | body, attachment | execute with audit event |
| Reversible write | create draft, mark read | policy dependent |
| External write | send, invite, respond | preview then exact commit |
| Destructive write | delete, cancel meeting | preview then exact commit |

Preview returns a normalized representation, warnings, and a short-lived token
bound to the exact operation hash. Commit rejects modified, expired, replayed,
or differently scoped operations. MCP annotations communicate intent to the
host, but server-side policy remains authoritative. See
[ADR 0004](adr/0004-preview-commit.md).

## Compatibility

- Operating systems: macOS, Linux, and Windows.
- Architectures: amd64 and arm64.
- Toolchain: Go 1.26 or newer.
- Interfaces: stable CLI JSON schema and MCP tool contracts.
- MCP: stdio first; Streamable HTTP optional.
- Outlook: capability-tested against Outlook on the web, not inferred from a
  desktop Outlook installation.

Compatibility claims require a fixture contract test and a documented live
smoke test. Live tests are opt-in and must never be part of the default suite.
