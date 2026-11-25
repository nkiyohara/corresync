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

## Amendment: login view usability

The decision above stands. The view becomes easier to use, with these bounded
additions:

- The page shows one state at a time (starting, loading, ready, check the
  screen, reconnecting, or how the login ended) and the time left before the
  link expires. The loopback server adds the remaining lifetime to its own
  page response; this is not daemon protocol.
- Paste sends the local clipboard text as one existing `text` action within
  the 4 KiB bound, and only on the person's explicit Paste button or
  Ctrl/Cmd+V. The page reads the local clipboard at that moment only; nothing
  reads or writes the remote page's clipboard.
- A new `move` action carries only a viewport position and is dispatched as a
  mouse move with no button, so hover menus open and nothing is activated. The
  page sends at most five a second while the pointer is over the picture,
  never queues one behind other input, and drops a move that fails. A failed
  move does not stop input or count as a lost input. Moves are input like any
  other: never repeated automatically, never sent to a non-HTTPS page.
  Private daemon protocol 31 accepts the action, so client and daemon must
  still be the same release.
- An unchanged frame is not sent again. The loopback server keeps the last
  frame in memory for the life of the view, tells the page by a frame number
  that its picture is current, and sends the picture again to a reloaded tab.
  The `--host` helper keeps a SHA-256 digest of the last frame it sent and
  marks an identical one as unchanged instead. Frame identity includes the
  origin, so the label always matches the picture. Frames are still never
  logged or written anywhere.
- The SSH wire becomes version 2 because it carries `move`. A newer local side
  stops at the first request against an older helper, before any input. A
  newer helper still answers version 1 requests in version 1 and refuses a
  move in them; an older local side ignores the new optional `unchanged` and
  `expiresIn` result fields and sees an omitted frame as "keep the picture".
  Using the same release on both computers remains the supported setup.
- The page refreshes about four times a second for a few seconds after input
  and once a second when idle or after a failed read, so the five
  consecutive unreadable screens that end the view still take about five
  seconds.

The Content-Security-Policy, pairing, Host and Origin checks, single
controller, request bounds, and at-most-once input dispatch are unchanged.
