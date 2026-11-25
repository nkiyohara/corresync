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

The daemon independently expires unfinished browser interactions, including
after abrupt CLI termination. Completed sessions cancel this timer and remain
owned by the daemon. The UI expires after ten minutes; the configured browser
login timeout may end it earlier. Completion, cancellation, disconnect, and
command exit clean up only
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

## Amendment: 0.9.5

The 0.9.1 to 0.9.4 implementation never completed against a real daemon: the
client rejected every pending graphical result because it carried a frame and no
text view. The synthetic tests used a stand-in client and missed it. The fix
keeps the decision above and changes these details:

- A default `corr auth login` on a Linux host without a display starts the
  `--web` view itself. `--json` still stops with an explanation.
- A pending graphical result carries a frame or, while the page navigates, no
  frame. It never carries a text view. A frame taken across a navigation is
  retried briefly and each screenshot request is bounded, because Chromium
  does not answer one whose page was replaced. Input is still dispatched at
  most once; a missing frame after input is not an error.
- A new explicit start for an account replaces its unfinished login, whether
  the earlier command is gone or still running. The earlier browser is closed.
  Its session ID stays bound to the earlier caller and is never handed over, so
  this is equivalent to cancelling and starting again. SIGHUP now cancels the
  command like Ctrl-C, unless the process was started with SIGHUP ignored.
- The login view ends with the daemon's reason when the login cannot start, or
  after five consecutive failed screen reads. The human CLI prints that reason;
  the page shows fixed guidance only.
- The `--host` helper appends the usual install directories to the remote
  `PATH`, because a non-interactive SSH command does not read the login profile.
- A reused profile's restored tabs are closed at launch so the owned tab is the
  only page.

The private daemon protocol is unchanged: the wire fields are the same and no
released client accepted a graphical result before. On 2026-11-25 the view was
run on a Linux host without a display against the real Outlook Web sign-in
page, up to credential entry. A completed provider login from a Mac remains
unobserved.
