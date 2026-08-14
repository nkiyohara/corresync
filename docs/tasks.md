# Task contract

Corresync models To Do items and reminders without pretending every provider
has the same feature set. The canonical contract ships before its provider
adapters so each adapter has one target and one safety model. A configured task
route is not a capability claim: the current foundation reports all task
adapters as unavailable until their dependent provider issue is implemented
and tested.

A staged task route never disables an implemented mail or calendar route on the
same account: those services may sign in while task status reports an explicit
unavailable degradation. A task-only account has no active service and fails
before authentication.

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

The table below is a development matrix, not a support claim. `target` means
the dependent issue requires a typed mapping, `limited` means that issue names
a known constraint, and `observe` means no capability may be asserted until
adapter evidence exists. Every row is currently **contract only**.

<!-- markdownlint-disable MD013 -->
| Provider route | CRUD/state | Reminder/recurrence | Hierarchy/metadata | Time contract | Sync candidate | Evidence issue |
| --- | --- | --- | --- | --- | --- | --- |
| `microsoft-graph` | target | target | checklist, categories, linked source target; assignment observe | date/time-zone round trip target | delta | [#108](https://github.com/nkiyohara/corresync/issues/108) |
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
