# ADR 0038: Human graphical login over SSH

- Status: Accepted
- Date: 2026-10-22
- Related: ADR 0006, ADR 0020, issue #199

## Context

A remote terminal cannot open its localhost URL on the user's Mac. Text-only
Outlook login is difficult for interactive identity-provider forms.

## Decision

An explicit human CLI invocation can relay a bounded graphical viewport of the
account's dedicated Outlook Web browser. Authentication remains browser-owned.
This extends the existing terminal relay; it grants no additional account access.
Ordinary operations and MCP cannot start or control this flow.

`--web` binds a temporary UI to IPv4 loopback and prints separate local-machine
SSH forwarding instructions. `--host` runs a versioned bounded JSON-lines helper
over authenticated SSH standard input/output and serves the UI on the user's
local loopback. Existing SSH host-key checking, configuration, and jump hosts
remain in force. The helper does not open a remote TCP listener. Both modes use
the private daemon protocol; neither exposes daemon IPC or raw CDP to HTTP.

A random 256-bit pairing capability is delivered in the human CLI URL fragment,
removed from browser history on load, and held in tab session storage solely for
bounded reload. The server validates Host, exact Origin, capability, controller,
and action schema. One controller owns the session. Frames and human input are
transient, bounded, and never logged or recorded. No arbitrary script, URL,
selector, clipboard read, or browser storage API is available. The viewport has
fixed logical dimensions and scales to the local window. Origins omit URL paths,
queries, and fragments. Non-HTTPS pages cannot receive input or produce frames.

The UI expires after ten minutes; the configured browser login timeout may end
it earlier. Completion, cancellation, disconnect, and command exit clean up only
this invocation's UI and unfinished interaction. Successful authentication
preserves the daemon-owned browser. Unknown input outcomes are never replayed.
Reload is limited to the same tab/controller and lifetime. Closing a tab alone
leaves the bounded session available until cancellation or expiry.

Private daemon protocol v27 adds the bounded graphical interaction fields. The
SSH wire has its own v1 negotiation; mismatches fail before input. Use the same
Corresync release on both hosts.

## Consequences

The initial implementation supports Outlook Web, as does the terminal relay.
Teams Web is a separate browser route. Local platform passkeys, USB keys, client
certificates, and device policy cannot be forwarded or bypassed. Orca is optional;
its own port-forwarding behavior is not claimed verified. Synthetic contracts
cover transport and browser input; real Mac-to-Linux provider login remains
live-unobserved until the maintainer explicitly performs it.
