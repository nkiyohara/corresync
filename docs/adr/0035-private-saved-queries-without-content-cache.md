# ADR 0035: Keep saved queries private without a content cache

Status: Accepted

Date: 2026-08-14

## Context

Issue 46 asks for responsive everyday mail and calendar reads, private saved
queries, explicit freshness, and an evidence-backed decision about local
caching. A general cache would create a second copy of provider metadata and
could easily grow into an accidental mail or calendar archive. It would also
need encryption and key ownership, retention, migration, corruption recovery,
offline semantics, and reliable invalidation across every provider before it
could be represented honestly as current state.

Corresync already performs bounded live reads. Provider adapters may own an
account-local cursor where a public contract supports incremental sync, and the
separately consented monitor owns its bounded queue and deduplication state.
Neither is a reusable content projection.

A saved mail query can itself reveal private intent, names, projects, or
relationships even when it stores no result. It therefore needs a private data
lifecycle distinct from public or portable configuration.

## Decision

Corresync does not persist a general mail or calendar metadata/content cache in
v0.9. Saved-query execution always calls the selected account's typed live
mail or calendar use case. Every result states `source: live_provider`, an
absolute `fetchedAt`, and explicit `cached: false` and `stale: false` fields.
An unavailable provider returns a typed failure; Corresync never returns an old
successful page as though it were current.

Saved query definitions are private account-local state:

- the path is derived only from the stable opaque account ID;
- the directory and file are owner-only, bounded, strict JSON;
- one catalog contains at most 64 definitions and no provider result;
- a deterministic revision binds review and apply across concurrent clients;
- account removal purges the catalog with the rest of that account's owned
  state;
- an explicit local purge is the recovery path for a corrupt or incompatible
  catalog.

Mail definitions contain one typed folder, bounded provider query, page limit,
and display time zone. Calendar definitions contain one typed calendar and a
bounded relative start/window. Corresync does not invent calendar text search
for providers whose application port has no such contract. Definitions cannot
contain credentials, authentication material, bodies, attachment content,
attendee lists, message/event results, arbitrary provider properties, or a
generic action.

CLI and MCP call the same application service. Listing, showing, and running a
definition are reads. Saving, replacing, deleting, or purging is a local write;
MCP must use caller-bound preview/commit and CLI must require explicit approval.
MCP cannot turn a saved query into monitoring, notifications, runner execution,
or egress.

Provider-native incremental cursors remain inside the exact account, route,
and adapter that owns them. They may accelerate a typed provider sync but do
not authorize a cross-provider cache, survive account removal, or change a
live result into an offline result. Cursor reset and retention gaps remain
explicit degradations.

The v0.9 resource budget is deliberately small and mechanically bounded:

- startup loads zero saved-query catalogs and starts zero refresh workers;
- one account holds at most 64 definitions in a catalog no larger than 256 KiB;
- one run loads only its account catalog and returns at most one existing
  bounded mail or calendar page;
- persisted provider-result content, global projection state, and offline
  refresh memory are all zero bytes;
- catalog validation at the 64-definition boundary has an allocation-reporting
  benchmark, while deterministic tests enforce the count and file-size limits.

## Consequences

Saved queries become convenient without creating a durable shadow mailbox.
Offline execution is intentionally unavailable and truthful. Repeated runs may
cost more provider requests than a cache, so page and time-window limits remain
small and provider throttling remains visible.

If measured performance later justifies a content cache, a new ADR must define
the exact fields, encryption/key owner, retention, stale schema, invalidation,
purge, migration, resource budgets, and provider evidence before any cached
result becomes reachable.
