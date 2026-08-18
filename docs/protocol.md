# Provider protocol boundary

Corresync presents typed mail/calendar/task models while preserving the
capabilities, degradations, and provenance of each configured provider route.
Provider protocols are replaceable outer adapters; none of their wire formats
is part of the public CLI, MCP, domain, or stable JSON contract.

## Shared transport rules

Every provider adapter must:

- connect only to the explicitly selected HTTPS/TLS endpoint or provider API;
- reject redirects that could carry authorization across an origin boundary;
- bound request, response, collection, body, and attachment sizes;
- return typed results with account and provider provenance;
- declare reviewed attendee-notification and cancellation-disposition
  semantics when constructing a calendar service;
- map authentication expiry, throttling, conflicts, and unsupported behavior
  into stable application errors and capability degradations;
- keep response bodies, authorization material, private queries, addresses,
  and provider request IDs out of ordinary errors and audit records;
- make at most one network attempt for a consequential write;
- report an unknown outcome when a remote commit cannot be established; and
- expose only closed typed operations, never raw actions, URLs, headers,
  methods, MIME parts, Graph properties, JMAP method calls, or WebDAV bodies.

The shared stateless API transport may repeat an idempotent read at most twice
after its first attempt. It retries only transport/read failures and HTTP
502/503/504, uses short bounded backoff, and opens a five-second circuit after
the third failure. HTTP 429 is returned to the provider adapter without an
automatic retry and opens an account-route-local throttle circuit for a
bounded `Retry-After` interval. A consequential write always has one network
attempt. Stateful JMAP, IMAP, SMTP, and WebDAV operations are not placed behind
this generic replay policy; their protocol-specific state and partial-outcome
rules remain authoritative.

Discovery is a separate unauthenticated boundary. DNS and well-known metadata
may suggest ranked routes, but discovery cannot read credentials, start OAuth,
launch a login, create an account, or choose a provider silently.

## Shared application operations

The closed application surface covers:

- bounded folder discovery, mail list/search, explicit body and attachment
  reads;
- save-only drafts, reviewed send/reply/forward, move, read state, and reviewed
  permanent deletion;
- bounded selectable-calendar discovery and calendar list, reviewed
  create/update/cancel, and provider-supported online-meeting creation;
- bounded task-list discovery, task list/get/search/sync, and mandatory-preview
  create/update/complete/reopen/delete;
- isolated cross-account mail, agenda, and task projections;
- content-free session/capability status and local monitor/event state.

Adding a protocol operation does not expose it automatically. It also needs a
typed application port, normalized result, capability and effect review,
synthetic contract coverage, CLI/MCP parity where appropriate, and
documentation of any degradation.

Lists remain metadata-first. Bodies, attachment bytes, attendee detail, and
meeting links are exposed only by the narrow use case that needs them.
Consequential operations use the server-enforced preview/commit protocol.

Task adapters additionally preserve date-only, floating datetime, and zoned
datetime meaning; advertise closed feature and sync-mode capabilities; bind
cursors to provider/account/list/mode; and return linked-source provenance as
data rather than authority. They cannot accept arbitrary provider properties,
remote MCP tools, scripts, or copy/move actions. See [tasks.md](tasks.md).

## Outlook Web

The undocumented Outlook Web service is isolated in
`internal/provider/outlookweb`. Requests target the configured HTTPS origin and
the closed `/owa/service.svc?action=<registered-action>` registry.

The browser authorizes a request only after exact-origin validation and while
its owning context remains live. Redirects are not followed. Browser exit and
authorization-observer loss require interaction again; HTTP 401, 403, and
login timeout become `session expired`. Only reads may retry; `Retry-After` is
bounded. OWA actions retain their synthetic `__type` metadata and are
normalized behind typed contracts.

The registry includes only the actions required for implemented folder, mail,
attachment, calendar, and reviewed write operations. The typed `FindFolder`
action also performs a bounded deep scan below the distinguished calendar,
filters exact calendar folder classes, and reports effective read/write rights.
Shared/delegated mailbox headers are added only for an explicitly configured
route and never broaden the signed-in user's existing permissions.

OWA can provision a Teams join URL as a provider-native calendar-event creation
property. Teams chat, channels, calls, recordings, and meeting lifecycle
management are not exposed.

## Google

The Gmail, Google Calendar, and Google Tasks adapters use an explicitly selected
user-owned Desktop OAuth client. Account setup records only reviewed external
credential handles and starts no authentication. MCP cannot initiate OAuth;
only explicit local CLI login may create or expand an authorization. After
that boundary, the session owner may resolve the already consented client
credential when Google requires a token refresh; MCP cannot invoke that
resolution directly and never receives the credential or grant.
Corresync-managed Google OAuth remains separately disabled.

Gmail uses the pinned
Gmail API with `gmail.modify`. Reads, MIME traversal, pagination, identifiers,
attachments, and provider payloads are bounded. Draft, send, read-state, label,
archive, Trash, and untrash mutations map to closed typed endpoints. The
immediate permanent-delete endpoint is never called; the provider-neutral hard
delete operation fails locally and directs the user to Trash. Confirmed partial
mutations require reconciliation and are not retried. Push watches, durable
history cursors, and scheduled send remain unavailable.

Google Calendar is a bounded REST adapter sharing the same OAuth grant.
When the authenticated primary calendar advertises `hangoutsMeet`, a reviewed
provider-native online-meeting request uses a unique conference request ID and
returns only that event's Meet link.

Google Tasks uses a separate task-only provider route and authorization handle
with exactly `tasks.readonly` or `tasks` plus verified OpenID identity. Its
bounded REST adapter exposes the shared task-list discovery and task CRUD/state
ports, date-only due values, ETag conditions, subtasks, ordering, output-only
source links, and deletion-aware `updatedMin` polling. It never reuses or widens
the Gmail/Calendar grant.

## Microsoft Graph

The Graph adapter is an explicit route, never an automatic fallback for a
Microsoft address. It uses a public OAuth client and an OS-keyring grant; no
client secret or hosted relay is supported.

Graph search retains provider query syntax. Reply, forward, and move report the
absence of an atomic source ETag where applicable, and a successful send may
not return a sent-item identity. Permanent message deletion revalidates the
reviewed source, binds the delegated account's immutable user identity, and
invokes Graph's explicit `permanentDelete` action once. Provider-native
calendar creation can request a Teams meeting link only through the typed
supported field. A response or attachment send creates and assembles a draft
first; if submission is not confirmed, the retained draft is reported as a
partial outcome and is never recreated automatically.

## JMAP

The JMAP adapter uses a selected session endpoint and an external credential
handle. It validates advertised capabilities, keeps account and mailbox IDs
opaque, batches only within application limits, and exposes incremental state
when the server supports it.

The session resource and every advertised API, upload, and download target must
share one exact HTTPS origin, including the effective port. This deliberately
rejects cross-origin JMAP deployments so Basic authorization cannot be
redirected to a server that was not explicitly configured.

JMAP method calls and arbitrary property maps never cross the adapter boundary.
State mismatch becomes a visible conflict or degradation rather than an
unreviewed retry.

A server may provide usable mail without JMAP Submission. In that case reads
and save-only drafts remain available while send reports a `mail.send`
degradation. A read-only advertised account likewise remains readable and
rejects every write explicitly. JMAP identity is resolved before a send draft
is created; if submission then fails, the retained draft is reported as a
partial outcome requiring reconciliation. If attachment upload succeeds before
draft creation fails, retained blobs are likewise reported as a partial
outcome.

## IMAP and SMTP Submission

The standards mail route separates IMAP reads/organization from SMTP
Submission drafts/sends while keeping one account identity. Endpoints, ports,
and TLS modes are closed configuration fields. Plaintext transport and silent
TLS downgrade are rejected.

The adapter advertises only behavior supported by server capabilities. MIME is
parsed and constructed behind bounded typed operations; callers cannot submit
raw commands or arbitrary headers. Server literal declarations are bounded
before client allocation from the first greeting onward. A forward-only capture
feeds each response exactly once to the pinned client parser, separately bounds
parser-recognized literal payload and control bytes, and stops excessive parser
CPU work internally. STARTTLS is negotiated with a bounded pre-TLS control
exchange, then the complete decrypted IMAP stream is checked. Reply message IDs
and References are normalized as bounded angle-bracket identifiers before
header construction. SMTP acceptance without a returned message identity is
represented explicitly. Errors after a mutating IMAP command or an accepted
SMTP submission are partial outcomes requiring reconciliation; Corresync does
not repeat the operation automatically.

The staged `google` route does not reuse this adapter. Generic `imap-smtp`
routes continue to use an explicitly approved external credential and cannot
inherit a Google OAuth grant.

## CalDAV

The CalDAV route uses an explicit HTTPS collection endpoint and external
credential handle. WebDAV and iCalendar documents are parsed with bounds and
mapped to the normalized event contract. Conditional writes use the available
entity version; unsupported fields remain visible as degradations.

Callers cannot submit arbitrary WebDAV methods, XML, iCalendar properties, or
collection paths. The adapter detects RFC 6638 server-managed scheduling on
the authenticated principal. When the capability is present, attendee
create/update/cancel uses the reviewed scheduling behavior and schedule-tag
preconditions; without it, attendee writes fail before the calendar object is
changed.

Recurring-instance writes remain scoped to that instance. Update creates or
replaces a `RECURRENCE-ID` exception. Cancellation adds the occurrence to the
master's `EXDATE`, removes any matching exception, advances `SEQUENCE`, and
updates the calendar object conditionally rather than deleting the series.

The independent CalDAV task route selects only VTODO collections. It maps the
closed task contract to RFC 5545 fields, uses strong ETags for exact
read-modify-write operations, and uses RFC 6578 sync tokens only when every
selected collection advertises that report. Invalid tokens trigger one
bounded full reset. RFC 5545 temporal consistency and UTC absolute-alarm rules
are enforced before writes. Unknown iCalendar properties remain on their exact
object during an update and are surfaced as degradations; no generic property
API is exposed. Servers that do not expose privilege or sync properties remain
readable but advertise neither writes nor incremental sync.

The explicit TickTick task route uses its fixed Open API and a separately
consented confidential OAuth client. It maps projects and Inbox, task reads and
search, create/update/complete/delete, recurrence, checklists, labels, ordering,
one assignee, dates, and zoned times to the same task ports. It exposes neither
provider project mutation nor arbitrary TickTick actions. Polling is a bounded
full snapshot because the provider documents no delta token, webhook, or
continuation beyond its 200-task result cap; an incomplete snapshot fails
closed. Reopen, reminder replacement, atomic write preconditions, and remote
identity confirmation remain explicit degradations.

## Compatibility workflow

Deterministic tests use synthetic fixtures with no credentials or personal
data. They cover encoding, malformed input, bounds, capability mapping,
degradations, conflict handling, and ambiguous writes. Cross-compilation covers
platform-specific code.

Live mailbox observations are separate, opt-in, and never part of the default
test command or CI. A live failure is reduced to a synthetic reproducer before
it enters the repository. See [compatibility evidence](compatibility.md) and
the [manual test checklist](manual-test-checklist.md).
