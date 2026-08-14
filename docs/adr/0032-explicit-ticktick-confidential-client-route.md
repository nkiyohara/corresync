# ADR 0032: Explicit TickTick confidential-client task route

- Status: accepted
- Date: 2026-08-14

## Context

TickTick's Open API exposes task projects, typed task reads and writes,
assignment, filtering, search, and a user time-zone preference. Its OAuth
contract is not a public-client flow: an application receives a client ID and
client secret, and the authorization-code exchange authenticates that pair
with HTTP Basic. The published contract documents neither PKCE nor a refresh
grant. It also exposes no account identity endpoint.

Corresync configuration cannot contain a secret. Discovery cannot obtain one,
and an OAuth route cannot inherit identity or authority from an address,
another service, or another account. TickTick also documents project and group
mutation, comments, focus, habits, and other APIs beyond the provider-neutral
task-list and task boundary accepted in ADR 0028.

## Decision

Add a closed `ticktick` task route in configuration schema v9. It contains the
fixed `https://api.ticktick.com` base, client ID, fixed-port loopback redirect,
an OS-keyring handle for the Corresync-owned OAuth grant, a separately
consented external-credential handle for the client secret, and a read-only
flag. The two handles must differ, and neither may be reused by another
credential binding or account. Migration from v8 preserves existing
configuration exactly, creates no TickTick route, and rejects a hidden
TickTick payload under the older schema.

The client secret is resolved only when an explicit login requires a new
authorization-code exchange. A valid stored grant does not open the external
secret owner. Mutable owner buffers are overwritten after use, and the secret
is never stored by Corresync. The OAuth grant remains in the OS keyring.
Authorization uses state and the exact registered loopback redirect, but does
not invent PKCE or refresh semantics that the provider does not document. Any
refresh token returned unexpectedly is discarded; expiry requires another
interactive authorization. Personal API tokens are not accepted.

TickTick exposes no stable delegated-user identity claim. A TickTick task-only
account therefore needs no email address and is isolated by its opaque local
account ID, dedicated authorization handle, external-secret consent, session,
cursor, and audit context. The missing remote identity check is always
reported as a degradation rather than inferred from branding or project data.

The adapter exposes bounded project discovery, list/get/search, create,
update, complete, delete, recurrence, subtasks/checklists, one assignee,
labels, ordering, date-only and IANA-zoned time, and bounded full-snapshot
polling. Provider ETags or exact snapshot digests are revalidated immediately
before writes, but optimistic concurrency remains false because the API does
not document an atomic write precondition. A missing create result or a
partial multi-call assignment result is an unknown outcome and is not retried.

Reopen, reminder replacement, comments, focus, habits, project/list mutation,
groups, columns, tags as independent objects, and other provider APIs remain
unavailable. Recurring completion is rejected because the documented endpoint
does not establish portable occurrence semantics. Removing an existing start,
due, or recurrence value is also rejected because the provider does not
document empty or null update semantics for those fields. Filter/search
results stop at the provider's documented 200-task bound; Corresync does not
claim a complete snapshot when no continuation contract exists.

## Consequences

TickTick becomes an available explicit task route with deterministic synthetic
contracts and an opt-in, read-only live harness. It remains live-unobserved
until a content-free observation is committed against the exact revision.
Users must register a TickTick application and keep its client secret in the
OS credential store or an explicitly approved helper; Corresync never becomes
a hosted token relay or shared OAuth secret owner.

The contract audit used TickTick's official
[Open API documentation](https://developer.ticktick.com/docs/openapi.md),
retrieved 2026-08-14 with SHA-256
`01fba20af5a1f33c756264c7873694b1f9fd4d812343e420ff4a4d94d83d7368`.
This decision extends
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md) and
[ADR 0028](0028-provider-neutral-task-domain.md) without weakening secret,
identity, preview/commit, or account-isolation boundaries.
