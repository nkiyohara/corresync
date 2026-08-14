# Task contract

Corresync models To Do items and reminders without pretending every provider
has the same feature set. The canonical contract gives every adapter one target
and one safety model. A configured task route is not by itself a capability
claim. Microsoft To Do via an explicitly authorized Microsoft Graph route is
implemented against synthetic contracts; the other task routes remain
unavailable until their dependent provider issues are implemented and tested.

A staged task route never disables an implemented mail or calendar route on the
same account: those services may sign in while task status reports an explicit
unavailable degradation. A task-only account is active only when its selected
adapter is implemented.

## Canonical model

A task belongs to one opaque account and one provider task list. Its provider
ID, list ID, object ID, and version remain opaque. Results always carry that
provenance; cross-account results add only the local alias used for display.

The normalized fields are:

- title and notes;
- `needs_action`, `in_progress`, `completed`, or `cancelled` status;
- `none`, `low`, `normal`, `high`, or `urgent` priority;
- parent, ordering, checklist items, assignees, labels, and attachment links;
- start, due, reminders, portable or provider recurrence, and completion time;
- provenance-only links to Corresync mail, calendar, task, or external objects.

Time meaning is never inferred:

- `date` is exactly `YYYY-MM-DD` with no time zone;
- `floating_datetime` is a local wall time with no offset or zone;
- `zoned_datetime` is RFC3339 plus an IANA time zone whose offset must match.

The cross-account display order groups these time kinds instead of comparing a
date or floating wall time as though it were an absolute instant. Within a
kind, the normalized value provides deterministic ordering; tasks without a
due value follow tasks with one.

Portable recurrence supports daily, weekly, monthly, and yearly frequency,
bounded interval, weekdays, and either count or until. A provider rule is a
separate opaque form and cannot be mixed with portable fields. An unsupported
form fails or appears as an explicit degradation; it is never silently
rewritten.

Linked sources identify the exact originating account, provider, and object.
They do not authorize a fetch or write. Self-links are rejected now, before a
future copy or move workflow can form a loop. Corresync does not introduce a
generic work-item domain or continuous mirroring.

## Bounds

All strings must be valid UTF-8 and contain no NUL. Important limits are:

<!-- markdownlint-disable MD013 -->
| Value | Limit |
| --- | ---: |
| Provider page | 100 entries |
| Provider offset | 10,000 |
| Cross-account page | 50 entries; offset 400 |
| Title | 4,096 bytes |
| Notes | 1 MiB |
| List name or search query | 1,024 bytes |
| Each reminders/checklist/assignees/labels/links/sources collection | 256 entries |
| Cursor or URL | 8,192 bytes |
| CLI canonical JSON document | 4 MiB |
| Encoded single-account result page | 8 MiB |
| Per-account cross-account projection workset | 2 MiB |
| Encoded cross-account result page | 12 MiB |
<!-- markdownlint-enable MD013 -->

Notes in a write review contain a bounded 500-rune preview, byte length, and
SHA-256 digest. The operation digest still binds the complete notes. Collection
replacement and nullable temporal/recurrence replacement require explicit
flags so omission never means deletion accidentally.

## Capabilities and degradation

Each authenticated task route reports a closed capability set: read,
cross-list read, search, create, update, complete, reopen, delete, optimistic
concurrency, reminders, recurrence, subtasks, checklist, assignments, labels,
attachments, linked sources, ordering, the three time kinds, and sync modes.

Capability is observed for the signed-in account. It is never inferred from
the provider ID or email domain. A field requiring an unobserved capability is
rejected before adapter access. Route and object degradations are included in
reads; route degradations are also included in every write review before
approval. Each write review also repeats the route's observed task capabilities,
including whether optimistic concurrency is available.

The table below is a development matrix, not a universal support claim.
`target` means the dependent issue requires a typed mapping, `limited` means
that issue names a known constraint, and `observe` means no capability may be
asserted until adapter evidence exists. `synthetic` means the route has
deterministic adapter contracts but no commit-bound live observation.

<!-- markdownlint-disable MD013 -->
| Provider route | CRUD/state | Reminder/recurrence | Hierarchy/metadata | Time contract | Sync candidate | Evidence issue |
| --- | --- | --- | --- | --- | --- | --- |
| `microsoft-graph` | synthetic; read-only or CRUD/state | one absolute reminder; portable recurrence plus exact provider-rule preservation | checklist, categories, typed linked sources; no assignments | zoned datetime; Windows names canonicalized through pinned CLDR | delta with safe reset | [#108](https://github.com/nkiyohara/corresync/issues/108) |
| `microsoft-web-tasks` | phased target; delete requires ambiguity evidence | observe | modern To Do fields limited | observe | bounded polling | [#109](https://github.com/nkiyohara/corresync/issues/109) |
| `todoist` | target | target, plan-sensitive | subtasks, labels, assignees target | floating/zoned target | sync cursor; webhook optional | [#110](https://github.com/nkiyohara/corresync/issues/110) |
| `caldav` | target with ETag | VTODO recurrence/alarm target | RELATED-TO and categories target; assignment observe | date and datetime round trip target | sync token or polling | [#111](https://github.com/nkiyohara/corresync/issues/111) |
| `google-tasks` | target with ETag and assigned-task restrictions | recurrence/reminder limited | subtasks, ordering, source links target; labels limited | due is date-only; time loss must be reviewed | bounded `updatedMin` polling | [#112](https://github.com/nkiyohara/corresync/issues/112) |
| `apple-reminders` | target after explicit full Reminders permission | recurrence and alarms target | URL target; hierarchy/labels observe | EventKit components require exact mapping | local notification then bounded refetch | [#113](https://github.com/nkiyohara/corresync/issues/113) |
| `ticktick` | documented Open API target | observe and report absent fields | subtasks target; other client-only fields limited | observe | bounded polling | [#114](https://github.com/nkiyohara/corresync/issues/114) |
| `anydo-mcp` | allowlisted negotiated tools only | observe from reviewed schema | personal/workspace/calendar/grocery distinctions required | observe | upstream MCP; no implicit retry | [#115](https://github.com/nkiyohara/corresync/issues/115) |
| `things` | documented local automation only; one-way operations explicit | observe | tags and deep links target; unsupported fields reviewed | observe | bounded polling/local invalidation | [#116](https://github.com/nkiyohara/corresync/issues/116) |
| `omnifocus` | fixed Omni Automation bridge target | recurrence target | projects, hierarchy, tags and deep links target | defer/due mapping target | bounded polling/local invalidation | [#117](https://github.com/nkiyohara/corresync/issues/117) |
<!-- markdownlint-enable MD013 -->

## Microsoft To Do through Graph

The `microsoft-graph` task route is independent of mail and calendar. A
task-only grant requests `Tasks.Read`; a writable route requests
`Tasks.ReadWrite`. `offline_access` and `User.Read` support refresh and exact
delegated-user confirmation against the explicitly configured account address;
task-only setup does not run provider discovery. Existing mail/calendar grants
are never expanded silently: configuration requires the separate task approval
bit, and a stored grant whose recorded scopes do not cover the selected service
set starts a fresh explicit authorization. Routes share one authorization only
when the public client, redirect, keyring handle, API base, and Microsoft cloud
all match exactly.

The adapter supports task-list discovery, list/get, CRUD, complete/reopen,
categories, checklist replacement, typed Corresync linked resources, one
absolute reminder, portable recurrence, and delta synchronization. A linked
resource carries a validated Corresync provenance envelope and provider deep
link; unrelated provider links stay untouched and are not projected as trusted
Corresync sources. Checklist replacement preserves validated existing item IDs,
updates those items in place, and creates or removes only the requested
difference. Search, hierarchy, assignment, ordering, attachments,
date-only values, floating datetimes, urgent priority, and cancelled status are
reported unavailable instead of approximated.

Corresync-originated notes use Graph's plain-text task body so whitespace
round-trips without an HTML wrapper. A provider-originated HTML task body is
projected as bounded plain text with an explicit lossy degradation.

Graph may return IANA or Windows time-zone identifiers. Corresync preserves the
local datetime and converts Windows identifiers through the territory-neutral
mapping generated from pinned Unicode CLDR 48.2. Non-portable recurrence—such
as a different weekly boundary or a monthly day that differs from the task
anchor—is retained as an opaque Graph provider rule rather than silently
rewritten.

Delta cursors are process-independent opaque URLs bound to the exact account,
provider, task list, configured HTTPS origin, and API base. A `410 Gone` cursor
starts a fresh bounded snapshot and marks the result `reset`; cross-origin,
cross-list, malformed, and oversized cursors fail before network access. Hosted
webhooks are not required.

Core task writes re-read the exact ETag immediately before submission and send
the matching conditional header, but the published To Do update contract does
not document atomic `If-Match` behavior. Optimistic concurrency therefore stays
false and the limitation appears in every write review. Checklist and linked-
resource replacement is a bounded multi-request assembly; partial completion
returns `write outcome unknown` and is never retried automatically.

Global, GCC High, and DoD endpoint/authority pairs are closed configuration
choices. Microsoft Graph operated by 21Vianet is rejected before OAuth because
the To Do APIs are unavailable there. All current evidence is synthetic and
the opt-in live harness never logs list names, titles, notes, or cursor values.

## Reads, writes, and sync

`corr tasks lists/list/get/search/sync` and the corresponding MCP tools route to
one account. `corr tasks list --all-accounts` and `task_list_all` fan out over
isolated services, retain provenance, and return explicit per-account partial
failures. They create no merged writable store.

Create and update accept the strict canonical JSON fixture shape. Complete,
reopen, and delete require list ID, task ID, and provider version. All five
writes first return `approval_required`; only the matching commit can consume
the caller-bound, single-use token. Delete is destructive. No provider call is
automatically retried after submission.

Sync cursors contain provider, account, list, mode, and opaque value. Input and
output cursors must match the selected route and an advertised mode. A cursor
is data, never authorization. Polling, delta, sync-token, notification, and
remote-MCP modes remain account-local adapter strategies.

The synthetic public fixtures are
[`task-create-v1.json`](../testdata/contracts/task-create-v1.json) and
[`task-v1.json`](../testdata/contracts/task-v1.json). Application, CLI, daemon,
and MCP tests consume the same types or fixture documents. They contain no
credential or personal data and are not live provider evidence.
