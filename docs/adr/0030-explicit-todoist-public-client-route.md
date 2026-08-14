# ADR 0030: Explicit Todoist public-client task route

- Status: accepted
- Date: 2026-08-14

## Context

ADR 0028 defines the provider-neutral task contract. Todoist API v1 exposes
typed REST reads, `/api/v1/sync` commands and cursors, delegated OAuth, and
account-plan limits. It also has semantics that cannot be inferred from field
names: Todoist `due` schedules when work starts, while `deadline` is the date by
which it must finish. Comments, duration, sections, and location reminders have
no canonical field.

Todoist supports public clients with PKCE through dynamic client registration
and HTTPS OAuth Client ID Metadata documents. Their published redirect metadata
requires HTTPS, with localhost described as a testing exception. Corresync's
accepted native application flow instead binds an HTTP `127.0.0.1` loopback
callback. Assuming an undocumented production exception or adding a hosted
redirect relay would violate the explicit-route and local-first decisions in
ADR 0012.

Todoist refresh tokens rotate on successful refresh. Two local Corresync
processes using the same grant must not both consume one refresh token and then
overwrite the replacement in the OS keyring.

## Decision

Activate `todoist` as an explicit task route using a user-supplied public client
ID, PKCE, the fixed Todoist authorization and token endpoints, and the existing
browser-owned loopback callback. The configuration stores only the public
client metadata and an approved OS-keyring handle. It cannot represent a client
secret, personal API token, configurable Todoist host, or hosted relay. Account
addition performs no discovery or authentication. Unlike providers that accept
an ephemeral native-app port, the Todoist route requires the fixed loopback port
registered for that public client. Login requests `data:read` for a read-only
route or `data:read_write,data:delete` for a writable route and confirms the
delegated email address through a read-only sync probe.

Refresh is serialized by a private cross-process lock derived from a digest of
the authorization handle. After acquiring it, the process reloads the current
grant, reuses a refresh another process already completed, or performs one
refresh and stores the rotated grant before releasing the lock. The lock path
contains no account address, token, or keyring handle.

The adapter pins `https://api.todoist.com/api/v1` and uses only current API v1
contracts. REST provides bounded project and task reads. `/sync` provides
command UUIDs, temporary-ID mapping, and an account-local sync token. A create
batches dependent reminder commands against the temporary ID, requires a
distinct stable mapping before any later provider request, and treats missing,
mixed, or malformed command results as an unknown write outcome. No write is
retried automatically.

Map Todoist `due` to canonical `start` and Todoist `deadline` to canonical
`due`. Date-only, floating, and IANA-zoned scheduling values round trip. The
provider priority scale maps exactly except canonical `low`, which is rejected.
Recurrence is a bounded, read-derived Todoist provider rule; Corresync does not
submit natural-language recurrence it invented. Canonical completion archives
the selected repeating task with `item_complete`; advancing only its current
occurrence would return a new active occurrence and cannot satisfy the
canonical completed-result contract.

Plan-derived labels, reminders, and deadlines are enabled only from
`user_plan_limits.current`. Assignment is submitted only after the selected
project reports `can_assign_tasks`. Sections, duration, comments, location
reminders, occurrence-only recurring completion, and the lack of an atomic
provider version precondition remain explicit degradations.

The sync cursor contains the provider token, sorted selected-project membership,
and bounded pending changes. This permits moves and deletes to remain isolated
to the exact task list and lets a later process drain a partial page before
contacting `/sync` again. Compressed and expanded cursor sizes are bounded; an
oversized project state stops instead of creating an unbounded local token.
Webhooks and hosted callback infrastructure are not introduced.

## Consequences

Todoist can be used as a task-only route or alongside unrelated mail and
calendar routes without sharing authority. Capabilities and plan restrictions
come from the signed-in account, not an address domain or provider brand. The
implementation has synthetic contract evidence only and remains
live-unobserved until a documented opt-in observation is recorded for the exact
revision.

Dynamic registration can be reconsidered only if Todoist publishes and Corresync
tests a production native-loopback contract that preserves these boundaries.
Adding comments, sections, duration, or occurrence-level completion requires a
canonical-domain decision rather than a Todoist-only property escape hatch.

Primary provider contract:
[Todoist API v1](https://developer.todoist.com/api/v1/).

This decision applies ADR 0028 and amends
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md) and
[ADR 0015](0015-per-service-provider-routes.md) without changing their
credential-free discovery or secret-handling requirements.
