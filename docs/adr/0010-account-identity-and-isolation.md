# ADR 0010: Account identity, isolation, and target-bound writes

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-07-28

## Context

Today an account is a local configuration alias for one Outlook origin, and one
session owner holds one browser session per configuration. Multiple accounts
break every assumption in that sentence.

Email addresses are not identity. Aliases, renamed identities, address changes,
and duplicate registrations are all ordinary, so keying local state on an
address eventually rebinds one person's stored state to a different mailbox.
A human-facing alias is no better: it exists to be renamed.

Two accounts on the same provider must not share a cookie jar, or the provider
treats them as one user and the second sign-in evicts the first. Cross-account
reads create the opposite hazard: once an inbox or agenda view merges results
from several accounts, committing a write against the wrong account or calendar
becomes a plausible accident rather than an exotic one.

## Decision

An account identifier is opaque, locally generated, and stable. It is not
derived from the email address, display name, tenant, provider object ID, or
configuration alias, because each of those is a mutable attribute of the account
rather than its identity. The human-facing alias becomes a separate, mutable
label: renaming it leaves the identifier and everything bound to it unchanged.
Identifiers are never reused after an account is removed.

Each account owns its state exclusively: browser profile and cookie jar,
credential handles, provider cursors and watermarks, rate limits and backoff,
caches, event queue, and audit context. Two accounts on the same provider and
the same origin share none of it.

Every result carries provenance: the account identifier, the provider, the
mailbox or calendar identifier, and the provider's own object identity. Where a
provider supplies a version or change key, the typed result continues to return
it alongside the identity so a caller can act safely on a specific version.
Provenance names one container, never both a mailbox and a calendar.

Reads may aggregate across accounts. An aggregated inbox, search, or agenda is a
projection over separate provider objects and never a writable merged store.
Display time zones may be normalized, but an event's original time zone and
floating-time semantics are preserved rather than rewritten.

Writes are target-bound. Every mutation resolves exactly one account and exactly
one mailbox or calendar before preview. A default account never resolves a
mutation issued from an aggregated context; ambiguity fails closed. The target
reference is part of the operation that the approval token binds, alongside the
account, the caller, and the normalized payload, so a commit cannot be
redirected to another account, another calendar, or a different object. Adding
the target to that binding changes the operation digest and is therefore a
versioned contract change, not a compatible addition. This extends
[ADR 0004](0004-preview-commit.md) rather than replacing it.

Removing an account purges its session material, cursors, queued events, and
cached data, and closes any browser that belongs to it. When the removed
account is the default, both the removal target and approved replacement remain
opaque IDs through the repository boundary and are resolved together inside
the latest atomic configuration transaction. Alias changes between preview,
purge, and commit cannot redirect the replacement.

## Consequences

The stable JSON and MCP contracts gain provenance and target fields, and
cross-account commands require an explicit target for any write. That is more
typing for an interactive user and more schema for an agent, and it is the
property that makes an aggregated view safe to offer at all.

The existing configuration alias keeps working as a label. Migration generates
an opaque identifier for it without discarding authenticated session state; see
[ADR 0011](0011-coordinated-corresync-rename.md).

Per-account isolation multiplies browser profiles, disk use, and daemon-managed
resources. The session owner already namespaces by configuration and state path
under [ADR 0003](0003-authenticated-local-session-owner.md), and accounts become
a second isolation boundary nested inside one owner rather than a reason to run
several owners.
