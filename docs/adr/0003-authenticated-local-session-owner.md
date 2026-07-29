# ADR 0003: Authenticated local session owner

- Status: accepted
- Date: 2026-07-17
- Amended: 2026-07-28

## Context

Opening a dedicated browser for every CLI call is slow, creates competing
profile owners, and makes preview tokens unusable across adapter lifetimes.
Giving an MCP process the browser session also places authorization material in
an agent-facing process. A long-lived owner is needed, but a loopback TCP server
would create an unnecessary ambient network surface.

## Decision

Run one session-owner daemon per absolute config-path and state-directory
namespace. It owns browsers, authenticated provider clients and in-memory
authorization, the policy guard, approval tokens, and audit recorder. CLI and
MCP remain unprivileged adapters.

Use a Unix-domain socket on Linux and macOS. Hold a non-blocking file lock for
singleton ownership, set the socket and runtime directory to owner-only modes,
and verify every accepted connection has the daemon's effective UID. Use a
byte-mode Windows named pipe whose protected DACL grants only SYSTEM and the
current user; the pipe implementation rejects remote clients.

Add defense in depth with a randomly generated 256-bit bearer credential. Rotate
it after acquiring the listener, store it in an owner-only state file, compare it
in constant time before reading a request body, and remove it only when it still
belongs to the shutting-down daemon.

Carry strict versioned JSON calls over HTTP/1.1 semantics on the local stream.
Expose only a closed typed method registry. Bound headers, bodies, responses,
concurrency, and shutdown. Disable redirects, compression, TCP dialing, and
automatic application-call retries.

Increment the protocol version whenever the closed method or wire schema grows
or changes. A client and daemon with different versions must fail before a
mailbox operation rather than partially negotiating an older surface. Protocol
version 2 adds bounded `mail.folders.list` discovery; version 3 adds bounded
`mail.search`; version 4 adds typed `mail.move` preview/commit calls without
adding an arbitrary action escape hatch. Version 5 adds the single-field
`mail.set_read_state` preview/commit contract.
Protocol version 6 adds mandatory-preview `calendar.create` and
`calendar.commit_create` calls and raises the authenticated IPC and OWA request
bounds to 8 MiB so a valid 1 MiB plain-text body remains representable after
worst-case JSON escaping.
Protocol version 7 adds versioned `calendar.update` and destructive
`calendar.cancel` preview/commit calls without adding a generic mutation
method.
Protocol version 8 adds the optional caller-bound `login.terminal` state
machine. Its path-free page projection and one-key actions remain on the same
owner-only authenticated IPC and never expose authorization material.
Protocol version 10 adds bounded attachment reads, reviewed permanent deletion,
and the expanded typed mail and calendar surface. Protocol version 11 adds
content-free `session.status`; it exposes only configured aliases, lifecycle
state, and an optional capture time. Protocol version 12 replaces editable
aliases in routing contracts with stable opaque account IDs, carries provider
capabilities and provenance, and binds consequential operations to explicit
mailbox or calendar targets.
Protocol version 13 adds isolated cross-account mail-search and agenda
projections with provenance and partial-failure results. Protocol version 14
adds content-free monitor status plus bounded local event listing and
acknowledgement; monitor configuration and purge are intentionally absent.
Protocol version 15 adds the selected calendar route's typed attendee-
notification and cancellation-disposition semantics to review results.
Protocol version 16 adds bounded `calendar.folders.list` discovery so every
provider can expose selectable opaque calendar IDs through the same typed
application, CLI, and MCP boundary. Protocol version 17 extends content-free
session status with separate mail and calendar provider identities so a
mixed-provider account is never collapsed to a misleading primary route.
Protocol version 18 adds the provider-neutral online-meeting creation field and
provider-bound review result without reinterpreting the v0.7 Teams-only field.

Before a client reads the rotating bearer, it authenticates the endpoint
itself. On Unix it opens the runtime directory without following symlinks,
validates ownership/type/mode, pins and validates the singleton lock and socket,
proves an active owner, connects, verifies peer UID, then rechecks directory,
socket, and lock identities. Untrusted `XDG_RUNTIME_DIR` values, symlinks,
regular files, FIFOs, permissive directories, wrong owners, socket squatters,
and replacement races fail closed. The legacy migration client follows the
same endpoint-authentication path. Listener-side checks and the rotating bearer
remain defense in depth rather than substitutes.

Keep `status` and `shutdown` as the only stable lifecycle controls across
protocol versions. After an authenticated daemon proves that a request was
rejected solely because of its envelope version, a newer client may retry
read-only status inspection and graceful shutdown using the exact version
reported by that daemon. It must compare the returned config digest before
replacement. The status snapshot and shutdown request must use the same
in-memory rotating credential so a delayed concurrent updater cannot stop a
newer owner. Drain active work and close the old browser before releasing the
singleton endpoint; start the current binary only afterward. No mail, calendar,
login, preview, or commit call may use this compatibility path.

## Consequences

- Provider authorization never crosses the daemon boundary.
- A stolen approval token remains caller-bound and short-lived, while IPC also
  requires same-user OS access and the rotating credential.
- Multiple configs can run concurrently without revealing their paths in socket
  or pipe names.
- Windows requires the small Microsoft `go-winio` dependency for named pipes;
  Unix peer verification uses the already-pinned `x/sys` package.
- Crash recovery may leave a socket or credential file, but singleton ownership
  validates and safely replaces only current-user, expected file types.
- Installing a newer binary does not leave an incompatible owner blocking the
  next command. Automatic replacement drains active calls but intentionally
  discards release-bound in-memory sessions and previews.
