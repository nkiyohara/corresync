# ADR 0037: Per-account service switches

- Status: Accepted
- Date: 2026-10-22

## Context

People need to disable Mail, Calendar, Tasks, or Messaging independently without
removing their account or losing the configuration needed to enable it again.

## Decision

Schema v12 adds `disabled_services` to each account. An absent list enables all
configured services. It never creates a route, chooses Graph, or authorizes a
provider. Existing explicit route selection and consent remain mandatory.

Settings exposes a checkbox for each configured service. The equivalent typed
settings key is `accounts.<opaque-account-id>.services`, whose value is the
comma-separated enabled services or `none`. Reviews bind stable account identity,
the complete account revision, and the previous selection. Renames or route
changes invalidate an outstanding review.

Runtime projection removes disabled routes before authentication and dispatch;
the stored configuration retains them. Disabling mail also disables its monitor.
Applying a switch ends existing sessions and approvals through the same private
owner lifecycle used by settings changes. Enabling a route does not sign in.
MCP uses the existing server-enforced settings preview/commit protocol.

## Consequences

All-off accounts are valid stored configurations but cannot authenticate. Schema
v11 migrates without changing authority or enabled services. Older binaries must
refuse schema v12. Live provider verification remains a separate opt-in.
