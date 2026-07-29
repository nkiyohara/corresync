# ADR 0019: Thin terminal workspace and interaction contract

- Status: accepted
- Date: 2026-07-29

## Context

Corresync has a command-oriented CLI and an MCP stdio server, but a human who
works through mail and calendar throughout the day also benefits from a
persistent terminal workspace. A terminal UI can make navigation, refresh,
review, and partial provider state easier to understand without turning the
presentation layer into another application core.

A stateful UI introduces risks that one-shot commands do not. A selected row
can become stale while a refresh is in flight, a cancelled write can still
have reached the provider, a daemon upgrade can invalidate previews, and
mailbox text can contain terminal control sequences or text crafted to look
like a trusted prompt. Convenience must not create a second policy engine, a
provider escape hatch, or a way around sensitive-read and preview/commit
boundaries.

## Decision

Add `corr ui` as a thin, local presentation adapter over the same typed
application use cases and authenticated daemon IPC used by the CLI and MCP.
This ADR defines the contract for later implementation; it does not claim that
the TUI currently ships.

### Dependency and trust boundaries

The TUI belongs at the outer transport layer:

```text
terminal input and rendering
          |
          v
ephemeral TUI state and typed intents
          |
          v
authenticated, versioned local daemon client
          |
          v
application use cases -> policy/approval/audit -> provider ports
```

The TUI may depend on presentation components, the closed daemon client, and
stable application projections. Domain, application, daemon, and provider
packages must not import it. It must not import a provider adapter, open a
browser profile, resolve a credential, read a cookie or token, construct a
provider request, invoke `corr` as a subprocess, or duplicate a policy check.
There is no generic action, arbitrary property editor, or TUI-only use case.

The session owner remains the sole owner of authenticated clients, policy,
approval tokens, audit, account routing, and provider capability observations.
The TUI owns only ephemeral presentation state: current screen, focus,
viewport, filters, form text, selected stable identities, request generations,
and content already returned by an approved typed read. It persists no mailbox
cache, sync database, authorization material, approval token, or draft outside
the existing application contracts.

Mail, calendar, provider errors, and monitoring events are untrusted data.
They can be displayed or selected as sanitized data and can never define a key
binding, command, style escape, URL action, confirmation prompt, or executable
argument. Copying uses terminal-native selection of the rendered text; the TUI
never emits a clipboard control sequence carrying untrusted content.

### Request, refresh, and cancellation ownership

Each view load has its own cancellable context and monotonically increasing
local generation. Navigation, filter changes, account changes, and shutdown
cancel obsolete reads. A response is applied only when its generation and
complete route identity still match the visible screen. Late results are
discarded; they never restore an old selection or replace a newer error.

The TUI may schedule bounded read refreshes while visible. Refresh starts no
authentication, capability probe with side effects, monitoring mode, agent,
or write. Provider monitoring and durable event collection remain separately
configured session-owner responsibilities under
[ADR 0014](0014-opt-in-monitoring-and-dispatch.md).

Items are addressed by stable account ID, provider provenance, exact container,
opaque object ID, and version/change key where available. A visual row number
or slice index is never an operation target. After refresh:

- an unchanged identity remains selected;
- a missing identity becomes an explicit stale selection;
- a changed version may remain visible but cannot reuse an earlier review;
- an account, route, or container change clears the selection and related form;
- a partial page cannot prove that a missing item was deleted.

Cancellation before a read completes has no side effect. Cancellation after a
consequential commit begins does not prove that the provider did nothing. Such
a result is `unknown outcome`, the approval is not reused, and the TUI does not
retry automatically. It directs the human to refresh or reconcile using a
read-only operation. Reversible writes follow their typed result contract and
also do not receive presentation-layer retries.

### Daemon lifecycle

The TUI authenticates the local endpoint and negotiates the exact daemon
protocol before loading content. On endpoint loss or version mismatch it:

1. stops refresh timers and cancels in-flight reads;
2. disables sensitive reads, previews, and commits;
3. discards local approval values and marks edited forms unsent;
4. uses only the status/shutdown replacement path accepted by
   [ADR 0003](0003-authenticated-local-session-owner.md);
5. reconnects, reloads account/session state, and requires a fresh selection
   and preview.

No read or write is replayed across daemon replacement. An expired provider
session is different from daemon loss: the screen retains only safe local
context, disables provider actions, and tells the user to run the explicit
local authentication flow. The TUI does not start OAuth or browser sign-in in
response to an agent, mailbox value, refresh, or timer.

### Screen and state model

Every data screen has one primary state and zero or more bounded notices.
Transitions are driven by typed results, not error-string matching.

- `loading`: keep previous data visibly stale when safe, show progress, and
  allow cancellation or navigation.
- `ready`: show the capture time, exact account/route context, result bounds,
  and available typed actions.
- `empty`: state which exact account, container, filter, and time range
  produced no items; do not imply that the account is empty.
- `partial`: preserve successful account-scoped results, name failed or
  uninspected scopes without private payloads, and never present the set as
  complete.
- `degraded`: show the typed feature, bounded reason, and lossy status before
  an affected action.
- `expired-session`: disable provider actions and point to explicit local
  reauthentication without opening or automating login.
- `stale-selection`: keep the old value visually distinct for inspection, but
  require refresh and exact reselection before an action.
- `conflict`: show that the provider version changed, discard the approval,
  and require reload plus a new preview.
- `unknown-outcome`: do not retry; show the exact attempted operation class
  and a read-only reconciliation path.
- `error`: show a sanitized typed class, a recovery action when one exists,
  and a feedback path; never render raw provider bodies.

Partial and degraded are notices that can accompany ready or empty data, but
they must remain visible until their affected scope is left or refreshed
successfully. An error cannot silently turn into empty. Unknown outcome cannot
silently turn into success merely because the item disappears from the current
page.

The initial workspace contains account/session status and exposes mail,
calendar, and local event views only when the corresponding typed use cases
exist. New screens require an existing application contract first. TUI
navigation never becomes evidence that a capability exists.

### Sensitive reads and writes

Metadata views stay separate from message bodies and attachment bytes.
Opening a sensitive read invokes the same policy and audit path as CLI/MCP and
shows the exact account and object first. Content is held only in bounded
memory for the visible screen, is cleared when leaving the account or locking
the UI, and is never placed in terminal title escape sequences, process
arguments, environment variables, logs, crash reports, or clipboard
automatically.

Consequential actions retain the server-enforced protocol from
[ADR 0004](0004-preview-commit.md):

```text
form -> typed preview -> dedicated review screen -> explicit commit
```

The review screen shows exact account, provider, target, recipients or
attendees, effect, provider-visible outcome, degradations, and expiry. Commit
is a separate key action available only from that review. Editing any field,
refreshing the target version, switching account, replacing the daemon, or
leaving the review discards the approval. A key repeat, terminal paste, mouse
event, or focus restoration cannot both preview and commit.

No irreversible operation has a global one-key shortcut. Destructive actions
require first choosing the item and action, then reviewing, then committing.
MCP annotations and color never substitute for the application checks.

### Key bindings and forms

Every screen provides a discoverable help action listing only the bindings
valid in that state. The footer shows the small primary set; full help explains
alternatives and effects. Navigation and actions use distinct bindings.
Unknown keys do nothing except provide bounded feedback. Key repeats are
coalesced for refresh and navigation and ignored for commit.

Keyboard-only operation is complete. Focus order is stable, the focused
control is identified without color, and escape/back never commits or discards
edited text without a prompt. Pasted text is form data, not a key sequence.
Forms validate bounds and required fields locally for prompt feedback, but the
application remains authoritative and revalidates the complete typed input.

An optional external editor executes one configured absolute executable by
direct argument vector with no shell. Corresync creates an owner-only temporary
file in a private directory, rejects links and replacements, supplies no
mailbox value through argv or environment, bounds the edited result, validates
UTF-8 and control characters, and deletes the file after import. The editor
can edit form text but cannot receive a credential, approval value, provider
request, or direct commit capability. Before launch, the file contains only
locally authored form text and trusted static template text; provider-originated
text is never prefilled because an editor can interpret modelines or file-local
directives from file content. Any explicit quoted-content insertion occurs
after the editor exits and is normalized as untrusted form data. The built-in
form remains available.

### Terminal rendering and accessibility

The renderer treats terminal output as a security boundary:

- strip or visibly escape C0/C1 controls, ANSI/OSC/DCS sequences, bidi
  overrides and isolates, and unsafe zero-width formatting from untrusted
  values;
- calculate width and truncation by grapheme cluster and terminal cell width,
  never by bytes;
- bound every cell, line, notice, and retained viewport;
- distinguish selected, focused, stale, degraded, and destructive states with
  text or shape as well as color;
- honor `NO_COLOR`, support monochrome, and avoid relying on Unicode glyphs
  when terminal capability is insufficient;
- handle resize and narrow terminals without hiding the account, action
  effect, approval state, or error class;
- never put untrusted text in the terminal title, hyperlink target, clipboard,
  notification, or emitted control sequence.

`corr ui` requires an interactive terminal with adequate size and a supported
input/output mode. Unsupported terminals fail with a plain actionable message
and leave ordinary CLI/JSON/MCP behavior untouched. Cleanup restores terminal
mode on normal exit, cancellation, panic recovery, and daemon loss.

### Compatibility

The TUI is an additive client of stable typed operations. With the TUI not
running, CLI text, CLI JSON, MCP stdio, configuration, daemon lifecycle, policy,
and provider behavior are unchanged. TUI-only presentation state is not a
stable machine interface and is never emitted on stdout used by JSON,
completion, or MCP.

Implementation must include deterministic model/update/render tests for every
state above; malicious terminal-string fixtures; Unicode-width, resize,
paste, and key-repeat tests; cancellation and stale-response races; daemon
replacement; and preview, conflict, expiry, and unknown-outcome transitions.
Provider fixtures and live mailbox tests remain outside the TUI package and
live tests remain opt-in.

## Non-goals

- provider selection logic, provider protocol logic, or capability inference;
- a local mailbox mirror, synchronization database, or offline source of truth;
- an embedded rich-text editor or HTML renderer;
- arbitrary provider actions, properties, scripts, macros, or plugins;
- unlimited key, theme, layout, or workflow customization;
- browser login automation, credential management, or a hosted/remote TUI;
- changing CLI, JSON, MCP, configuration, or daemon semantics to suit a screen.

## Consequences

The workspace can be responsive and familiar while all authority stays in the
existing application and session-owner boundaries. The cost is deliberate:
the TUI must surface partial, stale, degraded, conflict, and unknown states
instead of smoothing them into a simple list.

Later TUI issues can implement screens incrementally without reopening basic
trust and interaction decisions. Any feature that needs provider knowledge,
durable UI state, a new typed operation, a new daemon method, or weaker
review semantics requires its own application design and, when it changes an
accepted decision, a new ADR.
