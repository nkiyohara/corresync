# Outlook Web protocol boundary

Outlook Web's internal service is undocumented and can change without notice.
`Corresync` treats it as a replaceable transport adapter, not as part of the
domain or public CLI/MCP contract.

## HTTP invariants

- Every request targets one configured HTTPS origin and
  `/owa/service.svc?action=<registered-action>`.
- The browser session authorizes the final request only after an exact-origin
  check.
- Redirects are never followed. This prevents authorization from crossing an
  origin boundary and makes unexpected login redirects explicit.
- Request and response bodies are bounded before decoding.
- Error values contain HTTP status and a sanitized request ID, never a response
  body or authorization material.
- HTTP 401, 403, and OWA's 440 login timeout become a stable
  `session expired` error.
- Only read and sensitive-read actions can retry. Writes make exactly one
  network attempt because a remote service may have committed before returning
  an error.
- A write transport failure after submission, or a malformed success response
  whose postcondition cannot be established, becomes a stable unknown-outcome
  error and content-free audit event. Callers are told to inspect remote state,
  not to retry automatically.
- `Retry-After` is honored for retryable reads and capped at 30 seconds.

## Closed action registry

Protocol actions are an immutable enum compiled into the OWA package. There is
no parser from arbitrary strings and no raw-action tool. Each action carries a
conservative risk classification used to decide whether transport retries are
safe:

- Read: `FindFolder`, `FindItem`, `GetCalendarView`, and
  `GetUserAvailabilityInternal`.
- Sensitive read: `GetItem` and `GetAttachment`.
- Reversible write: `MoveItem`.
- External write: `CreateItem`, `CreateAttachment`, `SendItem`,
  `CreateCalendarEvent`, `UpdateItem`, and `UpdateCalendarEvent`.
- Destructive write: `DeleteItem`.

Adding an action requires a typed request/response contract, synthetic fixture,
effect review, and explicit application use case. Registering an action alone
does not expose it through CLI or MCP.

The application classifies the more specific typed use case independently. For
example, a save-only `CreateItem` draft is a reversible write for policy, while
the broader protocol action remains an external write for retry purposes. This
deliberate asymmetry prevents an ambiguous network failure from creating a
duplicate draft.

## Implemented contracts

- `FindFolder` discovers a bounded shallow or deep page under the message
  folder root, a supported distinguished parent, or one opaque parent ID. It
  returns folder names, IDs, classes, and aggregate counts, but not rules,
  permissions, delegates, or item content.
- `FindItem` lists bounded message metadata from a distinguished or opaque
  folder. Its OWA JSON contract uses `Paging` with an
  `IndexedPageView:#Exchange` value (not the similarly named SOAP
  `IndexedPageItemView` element) and sorts by received time descending so
  pagination has an explicit order.
- `FindItem` also performs a folder-scoped AQS search through a typed
  `QueryStringType:#Exchange` value. The search uses the OWA mail-list shape,
  waits for completion, has an independent 50-result page limit, and returns
  the same body-free metadata schema as listing. Its per-request search-folder
  identity is random; the private query is never written to audit data. The
  CLI and MCP cannot supply an action name or arbitrary request fields.
- `GetItem` reads a maximum 1 MiB plain-text body plus bounded file-attachment
  metadata for one explicit message ID. HTML, bulk IDs, MIME, headers, and
  attachment content are not requested. `GetAttachment` separately returns one
  explicit file attachment up to 2 MiB; item attachments are not exposed.
- `MoveItem` moves exactly one opaque item ID and change key to one typed
  distinguished or discovered opaque destination under the selected account.
  The response may contain one new item identity or omit it; multiple returned
  items are rejected. The request has no bulk surface and is attempted once.
  Any ambiguous HTTP or transport failure becomes an unknown outcome that
  callers must reconcile by listing mailbox state.
- `UpdateItem` changes only `IsRead` on one opaque item ID and change key. The
  contract uses `NeverOverwrite`, `SaveOnly`, `SendToNone`, and suppressed read
  receipts. It accepts no field URI, generic item shape, or batch from an
  adapter. A response may omit its refreshed ID; multiple items are rejected.
- `GetCalendarView` lists event metadata in a maximum 31-day absolute window.
  The adapter normalizes request and response times to UTC, accepts both direct
  `Body.Items` and response-message envelopes, and bounds the decoded event
  count. Bodies, attendees, attachments, and join URLs are not exposed.
- `CreateItem` creates exactly one text or HTML new-message, reply, reply-all,
  or forward in the distinguished drafts folder with both disposition fields
  set to `SaveOnly`. Response shapes bind an exact reference item ID and change
  key. Recipients are bounded and validated as bare addresses. A bounded
  `CreateAttachment` batch can then add file attachments and refresh the draft
  change key. Neither write is retried.
- `CreateItem` also sends a reviewed composition without attachments using
  `SendAndSaveCopy`. Compositions with attachments use `SaveOnly`, one
  `CreateAttachment` batch, then `SendItem` for the exact refreshed draft.
  The application always requires an exact external-write preview and commit.
  A successful response may omit the sent-copy ID; no write is retried and any
  partial multi-step outcome is reported conservatively.
- `CreateCalendarEvent` creates exactly one bounded calendar
  item in the selected primary or opaque calendar. Its JSON body remains the
  closed `CreateItemRequest:#Exchange` shape, while the specialized action is
  required for online-meeting properties. RFC3339 inputs use UTC by default or
  the reviewed Exchange/Windows time-zone context. The item uses plain text,
  optional all-day boundaries, reminders, a closed daily/weekly/
  absolute-monthly/absolute-yearly recurrence shape, bounded unique bare
  attendee addresses, `Busy` free/busy state, and an enhanced plain-text
  location. It selects `SendToAllAndSaveCopy` only when
  attendees are present and `SendToNone` otherwise. A reviewed
  `teamsMeeting=true` adds only `IsOnlineMeeting=true` and the closed
  `TeamsForBusiness` provider; the single-event commit result may include the
  returned join URL, while bulk calendar list never does. The application
  always requires an exact external-write preview and commit, the response must
  contain exactly one event ID, and submission is never retried.
- `UpdateCalendarEvent` applies a fixed set of calendar `SetItemField` values to
  one exact event ID and change key: subject, plain-text body, start plus end,
  time-zone IDs, enhanced locations, all-day status, reminder, and complete
  required/optional attendee replacement. The request
  repeats the exact identity in `EventId` and `ItemChange.ItemId`, uses the
  default event scope, and requires exactly one updated item in a successful
  response. Empty strings are explicit clears; omitted fields remain unchanged.
  Attendee notification follows OWA's calendar policy and may occur for meeting
  changes. Recurrence editing, arbitrary field URIs, and batches are not
  exposed.
- `DeleteItem` cancels one exact event ID and change key with
  `MoveToDeletedItems` and `SendToAllAndSaveCopy`. The application classifies it
  as destructive and always requires a caller-bound preview/commit. A response
  must contain exactly one successful result. The request is attempted once and
  an ambiguous result must be reconciled against Calendar and Deleted Items.
- `DeleteItem` also performs a separate reviewed `HardDelete` for one exact
  message ID and change key. It suppresses receipts, never retries, and cannot
  be mistaken for the reversible `MoveItem` flow.

Explicit shared/delegated mailbox aliases add bounded `X-AnchorMailbox` and
`X-OWA-ExplicitLogonUser` headers only after browser authorization is applied.
They do not alter the exact configured origin or grant mailbox permissions.

## Compatibility workflow

Protocol structs preserve OWA's JSON `__type` metadata. Synthetic golden
fixtures verify encoding and response normalization. Live smoke tests are
separate, opt-in commands and initially cover read-only operations. A protocol
change is released only after capability detection can distinguish supported,
degraded, and unavailable behavior.
