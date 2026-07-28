# Architecture

Corresync is a local-first mail and calendar bridge. Its provider-neutral
product scope is accepted in
[ADR 0008](adr/0008-provider-neutral-product-scope.md), and the coordinated
rename from `owa-bridge` is defined in
[ADR 0011](adr/0011-coordinated-corresync-rename.md).

## Reading this document

Sections marked **Shipped** describe the current release. Sections marked
**Accepted direction** describe decisions that an accepted ADR records and that
no release provides yet.

Exactly one provider adapter ships today: Outlook Web. Nothing in the accepted
direction is available in a release. A capability reaches
[features](features.md) or [compatibility evidence](compatibility.md) only with
synthetic fixture contract tests and a documented opt-in live observation, so
those two pages remain the authority on what works.

Command examples below use the `corresync` name the current release installs.
[ADR 0011](adr/0011-coordinated-corresync-rename.md) defines the rename and the
finite compatibility inputs used only while migrating local state and direct
installations.

## Goals

Corresync gives a signed-in human a coherent mail and calendar surface for
accounts they already control:

- a discoverable CLI for people and shell scripts;
- an MCP server for Claude Code, Codex, and other compatible clients;
- mail and calendar operations with identical behavior on both surfaces;
- explicit, deterministic safety controls around side effects;
- one safety model shared by every provider adapter;
- authentication that preserves the organization's normal sign-in controls, and
  that is browser-owned wherever a web adapter is used.

The current release meets these goals for one provider. Extending them across
providers is the subject of ADRs 0008 to 0014.

## Scope boundaries

Corresync does not provide a hosted relay, a multi-user server, a Microsoft
Graph compatibility layer, a Teams collaboration surface, or any mechanism for
bypassing access policy. Tenant-wide and administrative access, unattended
credential login, and TLS interception are permanently out of scope, not
deferred.

Microsoft Graph is never an implicit dependency, an automatic fallback, or a
capability probe. Optional Graph support is accepted only for an explicit user
selection or an authorization the user already granted; see
[ADR 0012](adr/0012-credential-free-discovery-and-explicit-selection.md). No
release implements it.

A Teams join link provisioned as part of one calendar event is calendar scope;
see [ADR 0005](adr/0005-calendar-hosted-teams-links.md). The provider-neutral
scope does not widen that boundary: Teams chat, channels, calls, recordings, and
meeting lifecycle management stay out of scope.

## System context (shipped)

```text
┌──────────────────── local machine ─────────────────────┐
│                                                        │
│  CLI ──┐                                               │
│        ├── application ── policy ── audit              │
│  MCP ──┘        │            │                         │
│                 │            └── approval tokens       │
│                 │                                      │
│                 └── provider port                      │
│                         │                              │
│  dedicated browser ── session owner                    │
│                         │                              │
│              Outlook Web adapter                       │
│                                                        │
└─────────────────────────┼──────────────────────────────┘
                          │ TLS
                 Outlook Web service
```

## Dependency rule

Dependencies point inward:

```text
adapters (CLI, MCP)               -> application -> domain
provider transports (Outlook Web) -> application ports
platform (browser, IPC, keyring)  -> application ports
```

The domain package cannot import browser, protocol, CLI, MCP, persistence, or
operating-system packages. A command is represented once as typed input and
output. Adapters translate but do not contain business behavior, and a provider
adapter holds no policy or approval logic. See
[ADR 0009](adr/0009-provider-capability-degradation-contracts.md).

## Runtime topology (shipped)

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
version before invoking mailbox operations. When an installed binary changes but
the exact config digest does not, the next client may inspect and gracefully
stop the authenticated old owner through stable lifecycle controls before
starting the current binary. The stop is bound to the inspected credential
generation, and the old browser closes before its singleton lock is released.
Mail, calendar, login, preview, and commit calls never use that compatibility
path. It never binds TCP. See
[ADR 0003](adr/0003-authenticated-local-session-owner.md).

The default MCP transport is stdio. Optional Streamable HTTP support may be
added for advanced local deployments, but must bind to loopback, validate the
`Origin` header, and require authentication.

## Release update boundary (shipped)

Interactive CLI startup may read cached public stable-release metadata and
display a TTY-only notice. It never updates in the background. The explicit
`corresync update` command follows the detected installation owner: package-managed
installs receive their exact external upgrade command, while a direct install
uses the signed, rollback-capable flow in
[ADR 0007](adr/0007-explicit-verified-self-update.md). MCP, daemon, completion,
machine-readable output, and mailbox use cases do not enter this local
release-management path.

Provenance verification already accepts exactly two enumerated release-workflow
identities, `nkiyohara/owa-bridge` and `nkiyohara/corresync`, each bound to the
requested tag. That finite allowlist exists so an installed binary can verify
and apply the first renamed release. It is not a repository-name pattern, and it
is removed when the compatibility window in
[ADR 0011](adr/0011-coordinated-corresync-rename.md) closes.

## Session lifecycle (shipped)

1. `corresync auth login` launches a dedicated browser profile visibly by default;
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
7. `corresync auth status` exposes only account aliases and content-free lifecycle
   state. `corresync auth logout` shuts down the config-scoped owner, closes all
   browsers, and discards in-memory sessions and approvals.

This is the browser-owned form of authentication required wherever a web adapter
is used; see [ADR 0002](adr/0002-interactive-browser-session.md) and
[ADR 0006](adr/0006-text-terminal-browser-login.md).

## Outlook Web transport (shipped)

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

## Provider adapters (accepted direction)

Providers become adapters behind application ports sharing one typed core.
Candidate adapters are the standards family (JMAP, IMAP, SMTP Submission,
CalDAV, and POP3 as a constrained import mode), Google API and Google
web-session adapters, and optional explicitly selected Microsoft Graph. Outlook
Web remains the default Microsoft route, because tenants that block third-party
applications leave it as the only route their users are permitted to take.

Capability is a per-account observation made after sign-in, never an inference
from a provider name or an email domain. An asserted capability is evidence; an
unasserted one is the absence of evidence rather than proof of refusal, and an
operation that depends on an unconfirmed capability fails closed instead of
silently choosing a degraded path. Where a provider cannot represent a requested
operation exactly, the use case either refuses it or names the affected feature,
the reason, and whether the mapping is lossy, in the preview and before
approval. Silent normalization is forbidden. See
[ADR 0009](adr/0009-provider-capability-degradation-contracts.md).

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

Under the accepted direction this boundary also carries a target: every mutation
resolves exactly one account and exactly one mailbox or calendar before preview,
and the approval token binds that target alongside the account, caller, and
payload, so a commit cannot be redirected to a different account or container.
Adding the target to that binding changes the operation digest and is therefore
a versioned contract change. See
[ADR 0010](adr/0010-account-identity-and-isolation.md).

## Accounts and routing (accepted direction)

An account identifier is opaque, locally generated, and stable, and it is not
derived from an email address, display name, tenant, provider object ID, or
configuration alias. The human-facing alias is a separate mutable label.

Each account isolates its browser profile and cookie jar, credential handles,
provider cursors, rate limits, caches, and audit context, including two accounts
on the same provider and origin. Every result carries provenance: account,
provider, mailbox or calendar identifier, and the provider's own object
identity.

Reads may aggregate across accounts. An aggregated inbox, search, or agenda is a
projection over separate provider objects and never a writable merged store, and
display normalization preserves an event's original time-zone and floating-time
semantics.

## Discovery and provider selection (accepted direction)

Onboarding resolves a destination before authenticating. Discovery is
credential-free: it collects evidence from well-known domains, MX and SRV
records, standards `well-known` endpoints, and autoconfiguration, scores
candidates with the evidence that produced them, requires valid TLS, and never
sends a password, token, or cookie to a candidate endpoint. Domain inference is
evidence, never proof, and manual override is always available.

Automatic selection may choose only a first-party interactive browser session or
an authorization the user already granted. It never initiates a Graph
authorization, never initiates a managed Google Workspace third-party API
authorization, and never submits an administrator review request.

The configuration schema still cannot represent a password, app-specific
password, OAuth token, cookie, or refresh token. A standards provider that
requires a secret reads it through a narrow credential port backed either by an
operating-system credential facility or by a user-configured helper command,
after an explicit per-account human consent step. Configuration stores only the
backend and its key reference, and no MCP tool can read a secret or trigger a
credential prompt. See
[ADR 0012](adr/0012-credential-free-discovery-and-explicit-selection.md).

## Import and staging (accepted direction)

Account configuration, authentication, and local data are three separate
operations. Scanning and preview are strictly read-only, explain any
operating-system privacy permission before requesting it, and never reuse
another application's passwords, tokens, cookies, or session material. Imported
data lands in a local staging area; uploading it to a provider is a separate
reviewed operation that uses the same preview and commit boundary. See
[ADR 0013](adr/0013-read-only-import-staging.md).

## Monitoring, events, and dispatch (accepted direction)

Monitoring, agent dispatch, and data egress are three separate consent
boundaries. All default to disabled on a fresh install, after an upgrade, and
after an account import, and enabling one never enables another. The modes are
`off`, `notify`, `queue`, and `agent`; `notify` and `queue` work with no AI
provider configured.

Events are metadata first, with bodies fetched only when policy and destination
require them. Automatically triggered agents are read-only by default, and every
external write still passes through preview and commit. The MCP surface is
read-only apart from acknowledging a local event, and no MCP tool can enable
monitoring, widen a filter, enable automatic execution, or enable egress. See
[ADR 0014](adr/0014-opt-in-monitoring-and-dispatch.md).

## Compatibility

- Operating systems: macOS, Linux, and Windows.
- Architectures: amd64 and arm64.
- Toolchain: Go 1.26 or newer.
- Interfaces: stable CLI JSON schema and MCP tool contracts.
- MCP: stdio first; Streamable HTTP optional.
- Outlook: capability-tested against Outlook on the web, not inferred from a
  desktop Outlook installation.

Compatibility claims require a fixture contract test and a documented live smoke
test. Live tests are opt-in and must never be part of the default suite. A
provider adapter is listed only once it has both, and the distinction between
deterministic coverage and live observation stays visible on the
[compatibility evidence](compatibility.md) page.
