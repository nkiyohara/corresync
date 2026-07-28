# Provider protocol boundary

Corresync presents one typed mail/calendar model while preserving the
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
- isolated cross-account mail and agenda projections;
- content-free session/capability status and local monitor/event state.

Adding a protocol operation does not expose it automatically. It also needs a
typed application port, normalized result, capability and effect review,
synthetic contract coverage, CLI/MCP parity where appropriate, and
documentation of any degradation.

Lists remain metadata-first. Bodies, attachment bytes, attendee detail, and
meeting links are exposed only by the narrow use case that needs them.
Consequential operations use the server-enforced preview/commit protocol.

## Outlook Web

The undocumented Outlook Web service is isolated in
`internal/provider/outlookweb`. Requests target the configured HTTPS origin and
the closed `/owa/service.svc?action=<registered-action>` registry.

The browser authorizes a request only after exact-origin validation. Redirects
are not followed. HTTP 401, 403, and login timeout become `session expired`.
Only reads may retry; `Retry-After` is bounded. OWA actions retain their
synthetic `__type` metadata and are normalized behind typed contracts.

The registry includes only the actions required for implemented folder, mail,
attachment, calendar, and reviewed write operations. Shared/delegated mailbox
headers are added only for an explicitly configured route and never broaden
the signed-in user's existing permissions.

OWA can provision a Teams join URL as a calendar-event creation property.
Teams chat, channels, calls, recordings, and meeting lifecycle management are
not exposed.

## Google API

The Google adapter uses an explicitly selected public OAuth client and a grant
held by the operating-system credential facility. It implements bounded Gmail
and primary Google Calendar operations.

Gmail search retains provider query syntax. Label/move operations do not claim
an atomic history precondition, least-privilege scopes exclude permanent
delete, and send may not yield a stable sent-item identity. The adapter does
not enable push/history monitoring or scheduled send. Calendar operations do
not claim online-meeting creation.

## Google Web

The managed Google Web route opens only the exact Gmail and Google Calendar
origins in one dedicated visible browser profile. Authentication, SSO, MFA,
account selection, and organization notices remain browser-owned. The adapter
never reads cookies, browser storage, or authorization tokens.

After confirming the configured identity on both surfaces, a bounded semantic
DOM driver returns visible mail and calendar metadata. It treats the snapshot
as read-only and incomplete: pagination beyond the rendered set is not
invented, unsupported details are degradations, and all draft/send/organization
and calendar mutation operations fail as unavailable without touching the
browser.

## Microsoft Graph

The Graph adapter is an explicit route, never an automatic fallback for a
Microsoft address. It uses a public OAuth client and an OS-keyring grant; no
client secret or hosted relay is supported.

Graph search retains provider query syntax. Reply, forward, and move report the
absence of an atomic source ETag where applicable, and a successful send may
not return a sent-item identity. The destructive permanent-delete use case is
unavailable because Graph message DELETE does not guarantee irreversible
disposal. Calendar creation can request a Teams meeting link only through the
typed supported field.

## JMAP

The JMAP adapter uses a selected session endpoint and an external credential
handle. It validates advertised capabilities, keeps account and mailbox IDs
opaque, batches only within application limits, and exposes incremental state
when the server supports it.

JMAP method calls and arbitrary property maps never cross the adapter boundary.
State mismatch becomes a visible conflict or degradation rather than an
unreviewed retry.

A server may provide usable mail without JMAP Submission. In that case reads
remain available while draft/send report a `mail.write`/`mail.send`
degradation. A read-only advertised account likewise remains readable and
rejects every write explicitly.

## IMAP and SMTP Submission

The standards mail route separates IMAP reads/organization from SMTP
Submission drafts/sends while keeping one account identity. Endpoints, ports,
and TLS modes are closed configuration fields. Plaintext transport and silent
TLS downgrade are rejected.

The adapter advertises only behavior supported by server capabilities. MIME is
parsed and constructed behind bounded typed operations; callers cannot submit
raw commands or arbitrary headers. SMTP acceptance without a returned message
identity is represented explicitly.

## CalDAV

The CalDAV route uses an explicit HTTPS collection endpoint and external
credential handle. WebDAV and iCalendar documents are parsed with bounds and
mapped to the normalized event contract. Conditional writes use the available
entity version; unsupported fields remain visible as degradations.

Callers cannot submit arbitrary WebDAV methods, XML, iCalendar properties, or
collection paths. The current adapter is calendar-object storage, not a CalDAV
scheduling agent: reviews claim no attendee notification, and cancellation of
an attendee event is refused rather than silently deleting without notice.

## Compatibility workflow

Deterministic tests use synthetic fixtures with no credentials or personal
data. They cover encoding, malformed input, bounds, capability mapping,
degradations, conflict handling, and ambiguous writes. Cross-compilation covers
platform-specific code.

Live mailbox observations are separate, opt-in, and never part of the default
test command or CI. A live failure is reduced to a synthetic reproducer before
it enters the repository. See [compatibility evidence](compatibility.md) and
the [manual test checklist](manual-test-checklist.md).
