# ADR 0031: Independent approval-gated Google Tasks route

- Status: superseded in its OAuth availability decision by
  [ADR 0036](0036-user-owned-google-oauth-first.md); its Tasks API contract is retained
- Date: 2026-08-14

## Context

Google Tasks uses the same Google OAuth authority as Gmail and Google Calendar,
but its task scopes and provider identity are distinct. Corresync's production
Google OAuth application is still awaiting approval. Code may be present in an
RC, but no released Google route may select an account, open a browser, read a
keyring grant, or contact Google while that approval gate is closed.

The public Tasks API also differs from richer task providers. Due values are
date-only, incremental change is bounded `updatedMin` polling rather than a
delta token or webhook, source links are output-only, and assigned tasks have
provider restrictions. The accepted task domain in ADR 0028 exposes task-list
discovery and task CRUD; it does not expose task-list mutation.

## Decision

Add a closed `google-tasks` task route with its own desktop OAuth client,
loopback redirect, OS-keyring authorization handle, read-only flag, and pinned
`https://tasks.googleapis.com` API base. Configuration schema v8 adds only this
typed, secret-free payload. Migration from v7 preserves existing routes and
consent exactly and cannot manufacture or accept a Google Tasks payload under
the older version.

The route requests `openid`, `email`, and exactly one of `tasks.readonly` or
`tasks`. It never shares or expands a Gmail/Calendar authorization handle.
Login confirms the verified OpenID email against the configured account before
exposing the task adapter. The generated Google desktop-client credential may
enter only at the existing OAuth boundary and is never represented in config.

The release-owned `GoogleOAuthApproved` gate remains false. Runtime provider
availability, account setup, OAuth profile selection, session activation, and
ordinary task calls all fail before authorization or provider access while it
is false. Approval alone does not enable the route: changing the gate requires
a separate reviewed release and opt-in live evidence.

The adapter implements bounded task-list discovery and the shared task
list/get/create/update/complete/reopen/delete ports. It maps due values only as
canonical dates, uses exact ETags and `If-Match` for writes, preserves subtasks
and ordering through typed fields, and projects safe HTTPS source links as
untrusted output data. It rejects deletion of an assigned task because Google
documents that deletion may also delete its source task.

Incremental reads use bounded `updatedMin` polling with deletion tombstones.
The cursor binds its watermark, page token, and scan-start time to the existing
provider/account/list envelope. An expired page token causes one bounded reset;
the adapter never assumes webhook support.

Google's task-list create/update/delete endpoints are not exposed. Adding
provider-specific list writes would violate ADR 0028's shared application
boundary. Any future list mutation must first add provider-neutral capability,
preview/commit, CLI, JSON, daemon, MCP, and cross-provider contracts.

## Consequences

RC builds can carry and test the complete adapter without making a Google
availability claim or starting Google authentication. Task-only scope grants
remain isolated from mail and calendar grants. Date-only, assigned-task,
source-link, search, reminder, recurrence, and polling limitations appear as
capabilities or degradations instead of silent coercion.

The route remains deterministic-only and live-unobserved until an explicitly
authorized observation is recorded. Documentation and the website describe it
as coming soon and approval-pending, while Outlook Web, Microsoft Graph,
Todoist, JMAP, IMAP/SMTP, and CalDAV routes remain available according to their
own evidence.

This decision extends
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md),
[ADR 0022](0022-google-desktop-client-credential.md),
[ADR 0026](0026-approval-gated-gmail-api-route.md), and
[ADR 0028](0028-provider-neutral-task-domain.md) without weakening them.
