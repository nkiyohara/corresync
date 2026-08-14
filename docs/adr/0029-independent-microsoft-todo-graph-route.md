# ADR 0029: Independent Microsoft To Do Graph route and cloud pairs

- Status: accepted
- Date: 2026-08-14

## Context

ADR 0028 defines a provider-neutral task domain but deliberately activates no
adapter. Microsoft To Do has a supported delegated Microsoft Graph API for
personal Microsoft accounts and eligible work or school accounts. It uses the
same Graph identity surface as existing mail and calendar routes, but adding
tasks to an existing grant changes authority and therefore cannot be inferred
from a shared provider ID or public client.

Microsoft Graph also has distinct Global, GCC High, DoD, and China deployments.
An independently configurable API base and authorization authority could cross
cloud boundaries or send a bearer to an arbitrary origin. The To Do API is not
available in Microsoft Graph operated by 21Vianet.

Task time and recurrence need additional care. Graph's `dateTimeTimeZone` may
contain a Windows or IANA identifier, while ADR 0028 requires canonical IANA
zoned datetimes. Graph recurrence contains fields, including weekly boundaries
and explicit monthly/yearly anchors, that the portable task recurrence does not
always represent.

## Decision

Activate `microsoft-graph` as the first task adapter. A task route contains a
closed `MicrosoftGraphTaskRoute` with one OAuth route and a read-only bit.
Task-only Graph configuration requires an explicit account address, which is
matched against `/me` on every login without running provider discovery.
`Tasks.Read` is requested only for a read-only task service;
`Tasks.ReadWrite` is requested only for a writable one. Mail, calendar, and
task services may use distinct grants. Identical grants are combined only when
their public client, redirect URI, keyring handle, API base, and cloud match,
and task configuration records a separate explicit approval. Stored grant
metadata is checked against the complete required scope set; a scope expansion
requires fresh interactive authorization.

Define one closed endpoint profile for each Microsoft cloud. Global, GCC High,
and DoD pair their exact v1.0 Graph API base with the corresponding Microsoft
identity-platform authorization and token endpoints. An omitted cloud preserves
the historical Global configuration meaning. China is a recognized profile so
mail/calendar routing remains explicit, but a task route using it is rejected
before keyring access, browser launch, or OAuth traffic. Arbitrary and cross-
cloud API bases are invalid.

The adapter implements typed list/task reads, CRUD, completion/reopen,
checklists, categories, Corresync-owned linked resources, reminders,
recurrence, and delta synchronization. Search and unsupported canonical fields
remain false capabilities. Third-party linked resources are not interpreted or
deleted unless an explicit replacement targets a Corresync-owned resource.
Multi-request related-resource assembly reports an unknown outcome after any
partial mutation and is never automatically retried.

Provider versions are exposed and re-read immediately before an exact task
write. Corresync sends the matching conditional header, but Microsoft To Do's
published update/delete contract does not document an atomic `If-Match`
guarantee. The adapter therefore does not advertise optimistic concurrency and
reports this limitation in write reviews.

Delta cursors remain opaque application data. The adapter accepts only the
configured HTTPS origin and API-base path for the exact selected list. Cursor
state has no in-memory dependency and can cross process restarts. A provider
`410 Gone` starts a fresh bounded snapshot and marks the reset explicitly;
hosted callbacks are not introduced.

Windows time-zone identifiers are converted using the territory-neutral
mapping generated from a pinned, digest-verified Unicode CLDR 48.2 source. The
runtime has no network dependency for that mapping. A recurrence is projected
as portable only when its anchor, zone, monthly/yearly values, and weekly
boundary can be reconstructed exactly. Otherwise its bounded Graph rule is
preserved as provider recurrence and cannot be submitted as a generic property.

## Consequences

Task-only Microsoft accounts can authorize and operate without provider
discovery while retaining delegated-user identity checks.
Adding tasks to an account never grants task authority implicitly, and adding a
mail or calendar route never expands a task-only grant. Account identity,
sessions, cursors, previews, and audits stay isolated under the existing stable
account ID.

National-cloud diagnostics can show the selected cloud and fixed API base
without exposing account or grant material. China users receive an availability
error instead of a misleading consent flow. Endpoint additions require a code
and test change rather than arbitrary configuration.

The implementation has synthetic provider contracts and an opt-in read-only
live harness whose output is content-free. It remains live-unobserved until an
authorized record is committed for the exact revision.

This decision applies ADR 0028 and amends
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md) and
[ADR 0015](0015-per-service-provider-routes.md) without changing their
credential-free discovery, explicit-selection, or secret-handling rules.
