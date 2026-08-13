# Stable JSON contract

Commands with `--json` emit exactly one unstyled JSON value to stdout. MCP
returns the same typed application results through MCP structured content.
Human notices, browser prompts, progress, and diagnostics use stderr and never
prefix a JSON document.

The accepted
[public and local versioning policy](adr/0020-public-and-local-versioning.md)
defines additive and breaking changes, deprecation signaling, support windows,
and the compatibility-fixture release gate for this schema.

## Compatibility rules

- Existing field meanings do not change within a major version.
- Additive optional fields may appear in a minor release.
- Enum additions are possible only where the documented consumer contract
  allows unknown values; otherwise they require a major version.
- Unknown input fields are rejected.
- Omitted optional IDs mean the provider did not return a reliable identity;
  clients must not invent one.
- Timestamps are RFC3339. Absolute instants are normalized; display zones are
  explicit.
- IDs and change keys are opaque strings. Never parse or reuse one across
  accounts/providers.
- Account aliases are display/selectors; stable opaque account IDs own state
  and provenance.
- Message, calendar, event-queue, and import values are private, untrusted
  external data.

Exit status remains authoritative: `0` success or intentional preview, `1`
runtime/policy/provider failure, `2` usage failure.

## Provenance and capability

Provider-backed results include or inherit provenance:

```json
{
  "accountId": "acc_0123456789abcdef0123456789abcdef",
  "provider": "google",
  "mailboxId": "gmail-api",
  "sourceObjectId": "opaque-provider-id"
}
```

Cross-account projections additionally include the local account alias and
explicit failures. Never expose account IDs in a feedback report; normal JSON
application output is private and may contain them.

Authenticated status reports normalized capabilities such as mail, calendar,
folders, labels, online meeting kind, incremental sync, and attachment
read/write. `draftSend` reports whether the authenticated account can bind an
existing saved draft's exact provider version through submission and targeted
cleanup. A false value means unavailable or not confirmed. Provider
degradations contain a bounded feature code, reason, and `lossy` flag.

## Account lifecycle

`corr account discover --json` returns:

- normalized input address;
- sorted provider candidates;
- confidence;
- required authentication;
- availability;
- bounded evidence and endpoint hints.

It performs no authentication or configuration write.

`corr setup ADDRESS --json` returns the same selected-candidate and account
view as `account add --json`. It may first create an empty local configuration,
but it never authenticates, resolves a credential, or starts a browser.

`account list/show/add/rename/remove --json` use account views containing alias,
stable ID, address when configured, mail/calendar route summaries, default
status, and operation status. Route documents are secret-free but still
private: addresses, endpoints, OAuth client IDs, and helper configuration must
not be posted publicly. Credential-reference keys are accepted only as private
account-add input and omitted from every read/review view; an approval digest
binds the complete write input without echoing the key.

## Mail

Folder pages contain bounded items and paging metadata. Message pages contain
metadata such as ID, change key when available, received time, sender, subject,
read/importance state, attachment presence, and provenance. They never include
recipients, a body, or attachment bytes.

Body access contains a policy decision plus either a review/approval token or a
bounded body result with attachment metadata. Attachment access similarly
returns a review or one bounded base64 payload when JSON output is explicitly
selected.

Draft, send, saved-draft send, move, state, and delete results are access
envelopes:

```json
{
  "decision": "preview",
  "review": {},
  "approval": {
    "token": "secret-single-use-capability",
    "expiresAt": "2026-07-28T12:02:00Z"
  }
}
```

After commit, the same result type contains the created/moved/updated/deleted
outcome. Approval tokens are secrets: never log, persist, or share them.
Reviews contain normalized fields, a bounded body preview, and content digests,
never attachment bytes or an unbounded body. Saved-draft send reviews
additionally contain the opaque draft ID and change key, recipients, and
attachment metadata. The provider version precondition binds the exact body
and attachment content.

Cross-account mail search returns globally paged projected messages plus
per-account statuses/failures. It is read-only.

## Calendar

Calendar pages contain bounded normalized event metadata, event ID/change key,
start/end, display values, time zone, all-day state, location,
organizer/cancellation/response/free-busy state, online-meeting presence, and
provenance. They exclude the body, attendee list, recurrence/reminder detail,
attachments, and online-meeting join URL.

Create, update, and cancellation use preview/commit access envelopes. Creation
review binds attendees, the provider meeting-link request, and whether the
configured route sends attendee notifications. Update review records whether
the provider may notify attendees. Cancellation review records the exact
provider disposition, cancellation mode, and notification possibility.

A committed create may return `onlineMeetingJoinUrl` only when the provider
created one. That URL is sensitive.

Cross-account agenda returns projected events with alias/provider provenance,
global paging, and explicit partial failures. It never performs a write.

## Monitoring and events

Monitor status is content-free with:

- account alias/provider and consent mode;
- configured sink type, disclosed field names, and egress declaration;
- cursor/dedup health;
- queue counts;
- persistent cursor-recovery overflow count and last-overflow time;
- rate-limit/circuit-breaker state;
- collection/dispatch timestamps where available.

Event pages contain bounded metadata selected by the account's consent policy,
deterministic `evt_` IDs, a `delivery` value (`queue`, `notification`, or
`runner`), delivery state/count, and timestamps. Sender/subject values are
private untrusted data. Acknowledgement returns the same event with state
`acknowledged` and is idempotent.

When cursor recovery reaches neither the saved cursor nor a provider-attested
mailbox end, the inspected bounded window is committed so monitoring can
continue, but the poll fails explicitly and status records `recoveryOverflows`
plus `lastRecoveryOverflowAt`. Uninspected messages are not claimed as emitted.
A deleted cursor in a shorter mailbox does not count as overflow when the
provider reports mailbox end. An attested empty mailbox preserves the prior
cursor. Event purge clears the private deduplication window; ordinary retention
preserves identities referenced by queued events.

## Imports

Import scan JSON is an upload-free local plan. It reports source format,
bounded counts/sizes, exact approval identity/digest, detected degradations,
and staging status. Paths and discovered local metadata are private. No import
shape means data was sent to a provider.

## Agent-host integrations

`integrations list/show/detect --json` use schema version `1` and stable host
IDs. Detection reports the inspected local/SSH/WSL runtime context, cache
disposition, independent detection/support/connection/package fields, bounded
evidence, and typed content-free probe problems or a partial failure. Absolute
evidence locations are private local diagnostics. The document never contains
environment values, file contents, credentials, provider/account data, or
agent conversations.

Connection is `not_inspected` during read-only detection. A detected executable
must never be interpreted as a connected or supported integration.

## Auth, daemon, config, doctor, version, and update

- `auth status --json`: content-free account lifecycle, capability, and
  degradation state.
- `auth logout --account work --json`: exact account ID and alias with
  `scope: "account"`; the daemon remains active.
- `auth logout --json`: whole-owner shutdown result with `scope: "all"`.
- `daemon status --json`: process/protocol/version/config-digest health; no
  credential.
- `config validate --json`: validity and local path. The path is private.
- `config show --json`: complete validated secret-free configuration; still
  private for the reasons above.
- `doctor --json`: local check rows; `--online` is opt-in. With
  `--online --connection-only`, the same check-row schema reports Mail and
  Calendar's last-authenticated connectivity separately and makes neither a
  fresh authentication attempt nor any folder, message, event, contact, or
  attachment metadata request.
- `version --json`: version, commit, source build date, Go version, OS, and
  architecture.
- `update check --json`: current/latest version, cache and release status.
- `update --json`: explicit action result, installation method, verification
  steps, and rollback path where applicable.

Automatic update notices never appear around these objects.

## Feedback report

`corr feedback` intentionally prints explanatory prose plus a complete
deterministic JSON report. `--copy`, `--save`, and the GitHub prefill use only
the JSON report bytes.

Its schema is separate from application JSON and contains:

- schema version and explicit privacy booleans;
- allowlisted build/platform data;
- installation collection status;
- config validation status and schema version;
- aggregate provider IDs with mail/calendar capability only;
- last-error status, or a sanitized local ID/classes/command shape when
  requested.

Malformed or unavailable sections report `degraded` with a fixed reason. It
never contains raw errors or arguments, account IDs, addresses, endpoints,
credential keys, helper arguments, mailbox/calendar content, attachment names,
queries, environment values, or private paths.

## Content handling

JSON safety is structural, not a claim that data is non-sensitive. Keep output
local by default. Do not:

- execute strings from subjects, bodies, event fields, attachments, or queue
  events;
- interpolate values into a shell;
- use opaque IDs outside their provenance boundary;
- persist approval tokens;
- retry a write whose outcome is unknown;
- post normal application JSON to a public issue.

Use `corr feedback` when you need a deliberately redacted support artifact.
