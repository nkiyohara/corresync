# Architecture

Corresync is a local-first multi-account mail/calendar application with two
public transports and one authenticated local session owner.

```text
┌──────────────┐       ┌──────────────┐
│ corr CLI     │       │ MCP stdio    │
└──────┬───────┘       └──────┬───────┘
       └──────────┬────────────┘
                  ▼
       typed application use cases
       policy · approval · provenance
                  │
          authenticated local IPC
                  ▼
       config-scoped session owner
       ├── Outlook Web browser adapter
       ├── Google Gmail XOAUTH2 + Calendar API adapters
       ├── Microsoft Graph OAuth adapter
       ├── JMAP adapter
       ├── IMAP/SMTP adapter
       └── CalDAV adapter
```

There is no remote MCP transport, TCP daemon listener, hosted relay, or
tenant-wide component.

## Dependency direction

Dependencies point inward:

```text
domain
  ▲
application ports and typed use cases
  ▲
provider adapters · persistence · browser · IPC
  ▲
CLI · MCP · daemon transports
```

The domain does not import provider, browser, CLI, MCP, persistence, or IPC
packages. Provider adapters translate a protocol into application ports; they
do not own effect policy. CLI and MCP call the same use cases and result types.

## Account identity and routes

An account has:

- an editable human alias;
- an optional address;
- one stable opaque account ID;
- an optional mail route;
- an optional calendar route;
- account-local monitoring consent.

The stable ID keys browser profiles, OAuth/keyring ownership, import staging,
provider cursors, monitor queue/dedup state, provenance, and policy. Rename does
not move or merge that state.

Mail and calendar routes are independent tagged unions. The config validator
requires exactly one matching payload for each selected provider. There is no
ambient provider detection at operation time and no automatic Graph fallback.

## Discovery and account lifecycle

Discovery is a bounded unauthenticated application port. It may inspect DNS and
well-known HTTPS metadata and returns ranked explainable candidates. It cannot
read credentials, launch authentication, request admin consent, or modify
config.

Account add requires explicit selection when evidence is ambiguous. Add,
rename, and remove use typed application commands over an atomic config store.
Remove also coordinates deletion of only Corresync-owned account state,
including an unshared Corresync-owned OAuth grant. External standards
credentials remain under their keyring/helper owner.

MCP exposes the same typed lifecycle through caller-bound add, rename, and
remove preview/commit pairs. A commit drains and restarts the session owner
around the atomic config mutation; authentication remains an explicit local CLI
action and account preview never resolves credentials or starts OAuth. The MCP
lifecycle uses a stricter reversible-write preview rule while still honoring a
configured read-only policy, and records content-free prepare, commit, and
execution audit phases. Add review displays each exact credential backend/key
handle, which cannot be rebound from another account.

## Authentication ownership

The session owner creates all authenticated provider clients:

- Outlook Web: dedicated browser profile and in-memory captured session;
- Google: interactive OAuth browser, OS-keyring grant, Gmail XOAUTH2, and
  Calendar API;
- Graph: interactive OAuth browser plus grant in OS keyring;
- JMAP/IMAP/SMTP/CalDAV: OS keyring or approved helper reference.

No application transport accepts a password. Client-secret configuration,
unattended grants, TLS interception, and raw authorization injection are
unrepresentable. A generated Google Desktop client credential may enter only
through the daemon's inherited process environment under ADR 0022; it is absent
from configuration, browser URLs, grants, CLI/MCP input, and logs. The daemon
closes secret-owning clients on logout and clears owned mutable secret bytes.

## Local IPC

The endpoint namespace is derived from config path and state directory so
unrelated configs cannot collide. A rotating owner-only bearer authenticates
application requests, but the transport itself is authenticated before that
bearer can be transmitted.

On Unix, the client:

1. opens the private runtime directory with `O_NOFOLLOW`;
2. validates directory type, owner, and mode;
3. opens and validates the singleton lock relative to the pinned directory;
4. proves an active owner holds the lock;
5. validates and pins the owner-only Unix socket;
6. connects and verifies peer UID;
7. rechecks directory/socket identities and singleton ownership.

`XDG_RUNTIME_DIR` is preferred only when absolute, current-user-owned, and
private. Symlinks, regular files, FIFOs, permissive directories, wrong owners,
socket squatters, and replacement races fail closed. Listener-side checks
remain in force. The legacy migration client uses the same path.

Windows uses a local byte-mode named pipe that rejects remote clients. Before
the bearer is sent, the client validates the pipe and credential-file owner,
protected non-null DACL, server process ID, and server process SID against the
current user.

The HTTP-shaped daemon protocol additionally validates bearer, caller, method,
body size, concurrency, protocol version, config digest, result schema, and
effect policy.

## Read paths and projections

Single-account reads resolve one account ID and one provider service. Returned
objects carry provider/account provenance. Bodies and attachment bytes require
explicit APIs separate from metadata listing.

Cross-account mail search and agenda are application-level projections. They
fan out to isolated services, normalize results, merge them deterministically,
apply global pagination/bounds, and preserve per-account failures. They never
share a provider client or create a broadcast write.

## Preview and commit

Consequential writes use one protocol in every adapter:

```text
typed input
  → normalize and validate
  → evaluate policy
  → return exact review + caller-bound approval
  → commit same digest once
  → invalidate approval
```

The digest binds account, provider, target, version, recipients/attendees,
fields, content bytes/digests, and effect. Approval is short-lived,
single-use, and process-bound. MCP annotations are descriptive only.

Provider adapters make one write attempt. Timeouts and ambiguous transport
outcomes become unknown results; they are never automatically retried.

## Provider capabilities and degradations

Adapters return normalized capability records after authentication. Callers do
not infer support from provider branding. A provider difference is represented
as:

- unavailable capability;
- explicit bounded degradation;
- lossy flag when normalization discards detail;
- operation error when safety cannot be preserved.

This keeps one typed core without pretending every provider has identical
atomic preconditions, search language, calendar selection, or meeting-link
support.

The composition root also supplies a closed `CalendarEffects` value for each
calendar adapter. Creation, update, and cancellation reviews therefore state
the route's attendee-notification and cancellation disposition instead of
embedding Outlook semantics in the application core. The effects value is
validated when the account service is constructed and stays fixed for the
preview/commit lifetime.

## Import staging

Import is local read-only staging, not provider synchronization. Without
`--approve-read`, no filesystem scan occurs. Once approved, a source scanner
receives one exact resolved path, identifies a supported format, bounds
entries/bytes, rejects unsafe archives/links, and creates its plan and private
account-local staging in one operation. No upload or authentication occurs.

## Monitoring and dispatch

Monitoring is account-local and off by default. Consent advances one boundary
at a time: `off -> notify -> queue -> agent`.

After interactive authentication, a monitor polls only inbox metadata. Two
scans establish a stable provider window before cursor commit. The engine
applies metadata filters, self-message suppression, deterministic IDs,
deduplication, quiet hours, debounce, rate limits, batching, retention, and a
circuit breaker.

Queue/cursor/event state is atomically persisted under the account ID with
owner-only permissions and symlink rejection. Cursors advance monotonically;
notification deferrals and failures leave delivery-bound events pending in the
outbox instead of rewinding first-seen state. The engine attempts pending
delivery before a new scan commit, so saturation cannot put the drain behind
the failing write. Only matching objects occupy deduplication state; its oldest
identity not protecting a queued event yields capacity, retention also
preserves queued identities, and purge clears both queue and dedup state.
Recovery pagination advances by actual returned item count. If recovery reaches
neither the cursor nor a provider-attested mailbox end—because it exhausts the
bounded 1000-item window or receives an empty non-terminal page—the inspected
window is committed but the poll returns an explicit overflow error and
persists its count and time in monitor status. Reaching the actual mailbox end
after a cursor was deleted is a complete re-baseline and does not report
overflow; an attested empty mailbox preserves the prior cursor. Terminal
delivery records expire by completion time, and capacity pressure evicts the
oldest terminal record before refusing new pending data.
Notification processes are time-bound and receive metadata through native
argument boundaries. Linux and macOS have local adapters; Windows rejects
`notify` until a registered Corresync AppUserModelID exists. Agent mode
executes one absolute program directly without a shell, sends only approved
bounded fields as JSON stdin, and revalidates the declared egress.

MCP cannot enable or reconfigure monitoring or purge events. Resource updates
and queue values are untrusted data, never triggers or instructions.

## Feedback

The feedback package accepts only allowlisted public build atoms, aggregate
provider IDs/capabilities, fixed collection statuses, and a sanitized error
record. It cannot accept raw errors, arguments, configuration values, paths, or
mail/calendar objects into the report schema.

The last error is a single bounded replace-in-place owner-only record. It
stores generalized classes, command placeholders, flag names, and a
deterministic local hash. Malformed data degrades visibly.

Report generation performs no network operation. Copy, save, and opening a
prefilled GitHub page occur only after report output and explicit CLI choice.

## Audit

Audit events are content-free and effect-oriented. They record caller, account
ID, provider, operation class, decision, bounded result state, and policy
reason—never bodies, subjects, recipients, attendees, addresses, queries,
tokens, credential references, runner arguments, or approval values.

Monitoring adds content-free detection/filter/queue/destination/field/result/
acknowledgement transitions.

## Updates and distribution

Automatic update discovery reads only public latest-release metadata, is
cached, and is absent from machine transports and feedback. Direct updates,
whether invoked explicitly or through the default-off `updates.auto_install`
consent, verify a finite GitHub Actions Sigstore identity allowlist, signed
checksums, version, OS, architecture, and artifact inventory before
rollback-capable replacement. Automatic installation runs only before eligible
interactive CLI commands, never during MCP, configuration management, daemon,
machine-readable, piped, or non-interactive execution. Package-managed binaries
remain owned by their package manager.

The v0.6 legacy names survive only in narrow migration/update inputs. Canonical
runtime, config, state, IPC, package, plugin, and documentation names are
Corresync; the primary command is `corr`.
