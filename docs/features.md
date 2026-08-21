# Feature and evidence matrix

CLI and MCP call the same typed application use cases. A missing operation is
not available through a raw protocol escape hatch.

## Provider routes

<!-- markdownlint-disable MD013 -->
| Provider ID | Mail | Calendar | Tasks | Authentication | v0.8 evidence |
| --- | --- | --- | --- | --- | --- |
| `microsoft-owa` | Mail | Selectable calendars, Teams meeting link | — | Visible browser-owned Outlook Web session | Implemented; synthetic contracts; live-unobserved |
| `google` / `google-tasks` | Gmail API; no permanent delete | Selectable Google calendars and Google Meet when advertised | Google Tasks with an independent task-only grant | Explicit user-owned Desktop OAuth client; external client-credential handle and OS-keyring grants | Implemented; synthetic API/application contracts; live-unobserved |
| `microsoft-graph` | Mail | Selectable calendars, Teams meeting link | Microsoft To Do | Explicit BYO public OAuth client; grant in OS keyring | Implemented; synthetic adapter/integration contracts; live-unobserved |
| `todoist` | — | — | Todoist | Explicit BYO public OAuth client with PKCE; grant in OS keyring | Implemented; synthetic adapter/integration contracts; live-unobserved |
| `ticktick` | — | — | TickTick | Explicit BYO confidential OAuth client; external secret handle and OS-keyring grant | Implemented; synthetic adapter/integration contracts; live-unobserved |
| `jmap` | Mail | — | — | OS keyring or approved credential helper | Implemented; synthetic RFC 8620 contracts; live-unobserved |
| `imap-smtp` | IMAP read/manage, SMTP draft/send | — | — | OS keyring or approved credential helper | Implemented; synthetic protocol contracts; live-unobserved |
| `caldav` | — | Calendar | VTODO tasks | OS keyring or approved credential helper | Calendar and tasks implemented with separate routes; synthetic contracts; live-unobserved |
<!-- markdownlint-enable MD013 -->

Task routes use a separate provider selection described in the
[task contract](tasks.md). Microsoft To Do is active only through the explicit
`microsoft-graph` task route, Todoist through `todoist`, and TickTick through
the explicit `ticktick` confidential-client route. Google Tasks uses an
explicit user-owned Desktop client. Other configured task routes remain
unavailable until their dependent provider issue ships.

Mail and calendar are selected independently. For example, one account may use
IMAP/SMTP for mail and CalDAV for calendar. `pop3` is reserved without a route
builder and cannot be selected.

iCloud onboarding composes `imap-smtp` and `caldav` into one reviewed account.
It uses the documented iCloud Mail address families or a complete Apple SRV
signal set, one shared external credential handle by default, and an explicit
OS-owned app-specific-password enrollment prompt. The underlying route status,
capabilities, and failures remain independent; this is a preset, not a new
provider adapter or capability claim.

“Implemented” means the typed route and synthetic contracts ship in v0.8. It is
not a universal provider-compatibility claim. See
[compatibility evidence](compatibility.md) before connecting a live account.

Discovery uses DNS and well-known metadata without credentials. It returns
ranked evidence, confidence, required authentication, and availability; it
never authenticates or silently adds an account. Interactive `corr setup`
coordinates current local setup state, uses the same evidence, explains
explicit route choices, previews the account, and adds it only after
confirmation. It separately offers multi-account continuation and reviewed,
independently verified agent-host integration. `corr setup ADDRESS` remains deterministic
and may add only a safely auto-selectable first-party route. `corr account add`
requires explicit provider selection whenever discovery is ambiguous.
Microsoft domain or hosted-MX evidence offers both Outlook Web and Microsoft
Graph, but Graph is always marked as an explicit OAuth choice and is never
selected as a fallback.
Google evidence identifies a route that needs a user-owned Desktop OAuth
client. Guided setup can validate and import the downloaded client into the OS
keyring, but discovery and account addition start no sign-in. The routes pin
the Gmail, Calendar, and Tasks API bases; Google Tasks uses an independent
task-only grant.
Workspace policy may still require administrator approval or block API access;
Corresync never silently falls back to another route.

The optional public checker on the Providers page is a deliberately smaller
projection of that knowledge for first-time visitors. Its browser code sends
only a normalized domain, and its Worker queries only a fixed public DNS
resolver for bounded provider-family signals. It does not perform well-known
HTTP probes, receive an address local part, authenticate, add an account, or
guarantee compatibility. Unknown and conflicting results direct the user to
the fuller local discovery command. See
[ADR 0027](adr/0027-domain-only-public-compatibility-checker.md).

## Mail

<!-- markdownlint-disable MD013 -->
| Capability | CLI | MCP | Safety |
| --- | --- | --- | --- |
| Discover folders | `corr mail folders` | `mail_list_folders` | Bounded metadata |
| List messages | `corr mail list` | `mail_list` | Metadata only |
| Search one account | `corr mail search` | `mail_search` | Bounded provider query |
| Search all accounts | `corr mail search --all-accounts` | `mail_search_all` | Isolated fan-out with provenance and partial failures |
| Read body | `corr mail body` | `mail_get_body` + optional commit | Explicit sensitive read |
| Retrieve attachment | `corr mail attachment` | `mail_get_attachment` + optional commit | One bounded file |
| Save draft | `corr mail draft` | `mail_create_draft` + optional commit | Save-only; never sends |
| Send/reply/forward | `corr mail send` | `mail_send` + `mail_send_commit` | Exact preview and commit |
| Send saved draft | `corr mail send-draft` | `mail_send_draft` + `mail_send_draft_commit` | Exact provider draft ID and version |
| Move | `corr mail move` | `mail_move` + optional commit | Exact source version where provider supports it |
| Read/unread state | `corr mail mark` | `mail_set_read_state` + optional commit | Reviewed versioned update |
| Permanent delete | `corr mail delete` | `mail_delete` + `mail_delete_commit` | Destructive approval |
<!-- markdownlint-enable MD013 -->

Lists exclude body and attachment content. Attachment reads are separately
bounded. Compose supports text or HTML, new/reply/reply-all/forward modes, and
bounded file attachments. Every write is attempted once; an unknown outcome is
reported and never automatically retried.

Provider differences remain visible:

- The Gmail API adapter uses bounded native search, message, attachment,
  label, draft, send, modify, Trash, and untrash operations. It never calls the
  immediate permanent-delete endpoint; move to Trash is available instead.
  Confirmed partial mutations require reconciliation. Push history and
  scheduled send are not exposed. A configured mail route requests the
  provider-documented `gmail.modify` scope only at explicit local CLI login;
- Graph query syntax differs from Outlook AQS. Reply/forward and move
  revalidate the exact reviewed source before invoking actions that expose no
  atomic source ETag precondition. Permanent deletion uses Graph's explicit
  `permanentDelete` action after revalidation. Successful asynchronous sends
  may return no sent-item identity. Sending an existing saved draft is
  unavailable because Graph cannot make its change key an atomic precondition
  of the send action;
- JMAP exposes incremental state and strong state preconditions where the
  server supports them; a missing Submission capability blocks send while
  save-only drafts and mail reads remain available, and a read-only account
  reports every write unavailable. Its submission state is separate from the
  reviewed email state, so exact-version saved-draft send is unavailable;
- IMAP/SMTP saves accepted submissions to the discovered Sent mailbox and
  returns the appended message identity. It fails before SMTP when no Sent
  mailbox exists. Message move uses native MOVE or a targeted UIDPLUS
  copy/delete/UID-EXPUNGE fallback; permanent delete requires UIDPLUS. Any
  failure after APPEND, STORE, COPY, MOVE, or deleted marking requires remote
  reconciliation. Exact saved-draft send additionally requires observed
  Drafts and Sent mailboxes plus UIDPLUS: it submits the immutable reviewed UID
  bytes once, confirms the Sent copy, and removes only that draft UID;
- Outlook Web supports explicit shared/delegated mailbox routing only when the
  signed-in user already has that permission. Its exact saved-draft send binds
  both ItemId and ChangeKey to Exchange SendItem, which performs the native
  Drafts-to-Sent transition.

## Calendar

<!-- markdownlint-disable MD013 -->
| Capability | CLI | MCP | Safety |
| --- | --- | --- | --- |
| Discover calendars | `corr calendar folders` | `calendar_list_folders` | Bounded metadata and opaque IDs |
| List one account | `corr calendar list` | `calendar_list` | Bounded absolute window |
| List all accounts | `corr agenda list --all-accounts` | `agenda_list` | Normalized isolated projection |
| Create | `corr calendar create` | `calendar_create` + `calendar_create_commit` | Mandatory preview and commit |
| Update supported fields | `corr calendar update` | `calendar_update` + `calendar_update_commit` | Exact event version |
| Cancel | `corr calendar cancel` | `calendar_cancel` + `calendar_cancel_commit` | Destructive approval |
| Provider meeting link | create flag/capability | typed create field | Only when the selected provider reports support |
<!-- markdownlint-enable MD013 -->

The normalized contract includes bounded subject/body, absolute start/end,
time zone, location, all-day state, reminder, supported recurrence creation,
replacement and removal, and required/optional attendees. Capability and
degradation records state when a provider cannot preserve a field. Google Calendar
discovers selectable calendars and provisions a unique Google Meet link only
when the authenticated calendar advertises that conference solution. Graph discovers
selectable calendars and reports Teams meeting support. Outlook Web can
provision a Teams join link as a creation property. `--online-meeting` selects
the configured provider's native service; the transitional `--teams-meeting`
alias requires a Teams-capable route. CalDAV discovers VEVENT
collections, maps typed events through WebDAV/iCalendar, safely consumes
server-expanded recurrence or performs bounded local expansion, and uses
conditional writes. A recurring-instance update materializes a
`RECURRENCE-ID` exception; cancelling one instance adds `EXDATE` to the master
and removes a matching exception without deleting the series.

Create/update/cancel reviews also name the selected route's attendee-
notification and cancellation disposition. Outlook Web, Google Calendar, and Graph
use their reviewed provider-managed notification behavior. CalDAV detects RFC
6638 server-managed scheduling on the authenticated principal. When available,
attendee create/update/cancel operations use that route and schedule-tag
preconditions; when unavailable, attendee writes fail before changing the
calendar object and the account reports an explicit degradation.

Teams chat, channels, calls, recordings, and meeting lifecycle management are
outside scope.

## Tasks

<!-- markdownlint-disable MD013 -->
| Capability | CLI | MCP | Safety |
| --- | --- | --- | --- |
| Discover lists | `corr tasks lists` | `task_lists` | Bounded metadata and observed capabilities |
| List one account | `corr tasks list` | `task_list` | Exact account/list provenance |
| List all accounts | `corr tasks list --all-accounts` | `task_list_all` | Isolated read-only projection with partial failures |
| Get/search | `corr tasks get/search` | `task_get`, `task_search` | Bounded private untrusted data |
| Incremental changes | `corr tasks sync` | `task_sync` | Cursor bound to provider, account, list, and mode |
| Create/update | strict JSON plus optional `--approve` | `task_create/update` plus matching commit | Mandatory typed preview; versioned update |
| Complete/reopen | exact IDs/version plus optional `--approve` | separate preview/commit pairs | Mandatory typed preview |
| Delete | exact IDs/version plus optional `--approve` | `task_delete` + `task_delete_commit` | Mandatory destructive preview |
<!-- markdownlint-enable MD013 -->

Unsupported fields fail before provider access. Representable loss appears in
task results and the exact write review. Linked mail or event provenance is
data only and does not create a copy, move, mirror, or generic work-item action.
Microsoft To Do implements these surfaces except search, which is explicitly
unavailable. It supports zoned times, one absolute reminder, recurrence,
checklists, categories, typed linked sources, and delta cursors. Writes that
assemble checklist or linked-resource changes report partial outcomes instead
of retrying. Todoist implements the same task commands except search, with
plan-observed labels/reminders/deadlines, subtasks, one assignee, exact provider
recurrence, and bounded sync-token cursors. Its scheduling date and deadline
remain distinct, and unmapped provider fields are visible degradations. CalDAV
implements VTODO list/read/search, ETag-bound CRUD/state writes, RELATED-TO
parents, categories, alarms, date/floating/zoned time, recurrence, and RFC 6578
sync-token reset. Unknown iCalendar properties stay attached to the exact
object during updates and are reported as degradations. TickTick implements
project/Inbox reads, search, create/update/complete/delete, recurrence,
checklists, labels, ordering, one assignee, and bounded full-snapshot polling.
Its missing identity, refresh, atomic-concurrency, reopen, reminder, and
unpageable 200-result contracts remain explicit. Other task providers remain
unavailable. Google Tasks implements task-list discovery, task CRUD/state,
subtasks, ordering, date-only due values, output-only source links, exact ETag
conditions, and bounded deletion-aware polling through an explicitly
configured, user-owned Desktop OAuth client.

## Accounts and projections

- Every account has a stable opaque ID independent of its editable alias and
  address.
- Profile, cursor, import, queue, deduplication, and policy state are keyed by
  that ID.
- Mail, calendar, and task provider routes are independent.
- Rename preserves identity and state. Remove requires approval and an explicit
  replacement when removing the default account.
- Remove purges account-local state and any unshared OAuth grant owned by
  Corresync; external standards credential records remain owned by their
  keyring/helper.
- Cross-account search, agenda, and task projection merge normalized results deterministically,
  retain provenance, enforce global bounds, and return explicit partial
  failures.
- Writes always select exactly one account; there is no broadcast write.

## Import staging

`corr import scan` accepts one explicit local source path and supports bounded
inspection of recognized archives/exports, Maildir, and Thunderbird profiles.
Without `--approve-read`, it performs no filesystem scan and asks the operator
to review the privacy boundary. Rerunning with `--approve-read` grants read-only
access to that exact resolved source and creates the bounded account-local
staging plan in one operation. It never authenticates, uploads, sends, or
mutates the source. `corr import purge` removes only Corresync-owned staging.

## Monitoring and event dispatch

Monitoring defaults to `off` for every old, migrated, and new account. Consent
advances one boundary at a time:

```text
off → notify → queue → agent
```

Collection begins only after interactive account authentication. The monitor
uses two mailbox scans to establish a stable provider window before committing
its cursor. It ignores Sent and Drafts and suppresses self-message loops.

Account-local state provides deterministic event IDs, deduplication, atomic
cursor/event updates, acknowledgement, retention, quiet hours, debounce,
hourly limits, batching, and a circuit breaker. The provider cursor advances
monotonically with an atomic outbox commit. Notify-mode events remain pending
in that outbox through quiet hours, debounce, rate limiting, cancellation, or
adapter failure and are drained before new scan commits and again after a
successful commit; the cursor is never rewound.
Each event records whether it belongs to manual queueing, desktop notification,
or runner delivery so a later policy change cannot redirect old pending data.
Terminal deliveries expire by completion time under retention; if the 10,000
event bound is reached, the oldest terminal record yields capacity while
pending data is never evicted. Only matching messages occupy the bounded
deduplication window; the oldest identity not protecting a queued event yields
capacity, retention preserves identities for queued events, and an explicit
purge clears both queue and dedup state. Recovery advances by actual returned
item count. If it reaches neither the saved cursor nor a provider-attested
mailbox end within the 1000-message bound, the inspected window is committed
for continued operation, but status increments `recoveryOverflows`, records the
time, and the poll returns an explicit overflow error because uninspected
messages were not emitted. A missing cursor in a completely inspected shorter
mailbox is a normal rebaseline; an attested empty mailbox preserves the prior
cursor. Neither is an overflow.
Runner and notification completion preserve an acknowledgement that races with
delivery without redelivering the event. Desktop notification adapters are
local and time-bound: Linux uses `notify-send`, macOS uses `osascript`, and
Windows rejects `notify` until Corresync has a registered AppUserModelID. Agent
mode invokes one absolute executable directly—never through a shell—and sends
bounded JSON on stdin. A runner claiming remote egress requires a separate
explicit approval.

CLI exposes configuration, status, listing, acknowledgement, and purge. MCP
exposes only `monitor_status`, `events_list`, and `event_acknowledge`, plus
read-only monitor/event resources. Enabling, reconfiguring, and purging remain
CLI-only consequential actions.

## Privacy-preserving feedback

`corr feedback` creates a deterministic, redacted local report with allowlisted
build data, platform, installation method, config validation, aggregate
provider capabilities, and optionally the latest sanitized error class and
command shape.

Raw errors, argument values, account IDs, addresses, credentials, lookup keys,
mail/calendar/task content, attachment names, queries, environment values,
helper arguments, browser data, and private paths are excluded by construction.
The latest error record replaces the previous record rather than appending
history. Malformed or oversized records become visible degraded sections.

An explicit `corr feedback --last-error` also reviews the separately replaced
last crash: build and UTC time, fixed process/boundary codes, and a bounded
Corresync-symbol stack. Panic values, runtime arguments, source paths, request
data, identifiers, credentials, and provider content are structurally absent.
Crash recording never continues uncertain state and is excluded from automatic
public feedback.

Report generation makes no network request. Copy, save, and opening a prefilled
GitHub page each require an explicit flag after the complete report is shown.
Opening GitHub never submits an issue automatically.

A separate default-off `feedback.auto_submit` consent can use the signed-in
user's external GitHub CLI to create a public issue after an interactive command
failure. That automatic schema is smaller: validated build/platform atoms,
enumerated install method, command and flag names, a content-free fingerprint,
and fixed error classes only. It cannot represent config/provider/account data,
raw errors, values, paths, credentials, mail, or calendar content. Corresync
does not read the GitHub token; MCP, machine-output, configuration-management,
and non-interactive commands cannot submit.

## Intentionally absent

Corresync does not implement mailbox-rule mutation, delegate-permission
management, generic provider properties/actions, arbitrary recurrence,
item-attachment writes, automatic provider fallback, unattended credential
login, tenant-wide access, a remote MCP endpoint, a hosted relay, telemetry, or
raw crash upload.
