# ADR 0028: Provider-neutral task domain and route boundary

- Status: accepted
- Date: 2026-08-14

## Context

Corresync has provider-neutral mail and calendar use cases, but task providers
disagree about time, recurrence, reminders, hierarchy, assignment, ordering,
and incremental change tracking. Adding adapters before defining the boundary
would either discard provider meaning or expose arbitrary provider actions.

Tasks also introduce new routes. Microsoft To Do may use Graph while mail uses
Outlook Web; a CalDAV account may expose VEVENT and VTODO in distinct
collections; Apple Reminders and desktop task applications use local operating
system automation; and Any.do is an upstream remote MCP service. None of those
routes may inherit authorization merely because another service exists on the
same account.

## Decision

Add an optional task route to the existing stable account identity. Mail,
calendar, and tasks may select different providers. Each adapter instance,
authorization handle, session, cursor, cache, and audit context remains scoped
to one opaque account ID and one explicit route. Configuration schema v6 adds
the closed `accounts.<alias>.tasks.provider` field. Migration from v5 creates
no task route and grants no new authority.

Configuration schema v7 adds the typed `tasks.caldav` VTODO payload. Migration
from v6 preserves every existing route and consent without manufacturing a
CalDAV task route. VEVENT and VTODO collection discovery, credential consent,
adapter session, capability observation, cursor, and application service
remain independent even when they use the same server and external credential
handle.

Configuration schema v8 adds the independent, approval-gated
`tasks.google_tasks` payload. Migration from v7 preserves every existing route
and consent and cannot select or authorize Google Tasks. See ADR 0031.

Configuration schema v9 adds the independently authorized
`tasks.ticktick` payload. Migration from v8 preserves every existing route and
consent, rejects hidden TickTick payloads in an older file, and grants no new
authority. See ADR 0032.

The canonical model contains task lists and versioned tasks with bounded title
and notes, status, priority, parent, ordering, checklist, assignee, labels,
links, reminders, recurrence, and completion time. Date-only, floating local
datetime, and RFC3339 datetime plus IANA time zone are distinct types. An
adapter must not manufacture a time or silently convert one kind to another.

Task capabilities are closed and observed after explicit sign-in. They cover
reads, cross-list reads, search, typed writes, optimistic concurrency, feature
fields, time kinds, and advertised sync modes. False means unavailable or not
confirmed. Unsupported input fails before provider access. Any representable
loss is a bounded degradation on results and write reviews; a silent lossy
mapping is forbidden.

The read surface is list lists, list, get, search, and bounded incremental
sync. An opaque sync cursor is bound to its provider, account, list, and mode.
It cannot be moved between routes or used as write authority. Polling, delta,
sync-token, local-notification, and upstream-MCP modes describe how an adapter
invalidates or advances its own account-local state; they do not create a
hosted Corresync webhook service.

Create, update, complete, reopen, and delete are separate typed application
ports. Every task write requires a server-issued preview and a separate commit,
including providers that have their own permission prompt. Update, state, and
delete inputs bind one exact task version. An adapter may synthesize a safe
version only when its provider contract proves the snapshot semantics and
reports any concurrency limitation. A consequential provider call is attempted
at most once; an ambiguous outcome is not retried.

Mail, calendar, external, and task source links preserve account, provider,
and object provenance. A link is data, not authority to fetch, copy, move, or
mirror an object. A task cannot link to itself. No copy/move surface is added by
this decision, and any future workflow must reject cycles before preview.
Continuous cross-provider mirroring is outside the first task release.

CLI, stable JSON, daemon IPC, and MCP expose the same application types. The
CLI accepts the canonical create/update document instead of accumulating a
provider-shaped flag dialect. Daemon protocol v22 adds the closed task methods.
MCP exposes distinct prepare and commit tools and classifies returned task
content as private, untrusted provider data.

The canonical contract and synthetic conformance fixtures do not activate a
provider. Every task adapter remains unavailable until its dependent issue
adds a closed route, synthetic provider contracts, and any required explicit
authorization. Capability claims remain live-unobserved until recorded under
the normal compatibility-evidence process.

## Consequences

Provider integrations have a larger up-front mapping cost, but callers receive
one reviewable model with exact provenance and explicit limitations. Rich
providers do not force generic properties into the core, and narrow providers
cannot claim fields they discard.

Task-only accounts are valid configuration. Until their selected adapter is
implemented, account views show the configured route as unavailable and login
fails before authentication or provider access. Existing accounts gain no task
route during migration.

An upstream remote MCP provider is an external provider transport, not a new
Corresync server transport. Its tools and schemas must be allowlisted and
bounded by its adapter; unrelated Corresync content is never forwarded.

This decision amends ADRs
[0008](0008-provider-neutral-product-scope.md),
[0009](0009-provider-capability-degradation-contracts.md),
[0010](0010-account-identity-and-isolation.md), and
[0015](0015-per-service-provider-routes.md) for tasks without weakening their
authentication, capability, isolation, or secret-handling rules.
