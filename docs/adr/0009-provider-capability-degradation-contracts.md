# ADR 0009: Provider boundary, capability, and degradation contracts

- Status: accepted
- Date: 2026-07-28

## Context

Providers disagree about fundamentals: folders against labels, recurrence
expressiveness, attendee responses, shared and delegated calendars,
online-meeting creation, reminders, attachment handling, free/busy, server-side
search, incremental synchronization, and scheduled send.

Two failure modes follow. False normalization presents one uniform model and
silently discards what the target cannot express, so a lost recurrence rule or
a dropped optional attendee is discovered only after the meeting goes wrong. A
leaky abstraction exposes provider fields directly, which moves the problem to
every caller and makes the MCP tool catalog unreviewable.

There is also a difference between what a provider supports and what this
signed-in user can actually do. Licensing, Conditional Access, a disabled
service, delegation, and administrative policy all change real behavior, and an
agent cannot discover that difference by experimenting against a live mailbox.

## Decision

Providers are adapters behind application ports. The domain must not import
them, and an adapter translates without containing policy or approval logic.

Capability is a per-account, post-authentication observation, never an inference
from a provider name, an email domain, or a configuration value. Each account
reports a typed, closed capability set that is versioned together with the
stable JSON and MCP contracts.

An asserted capability is evidence. An unasserted capability is the absence of
evidence, not proof that the provider refuses the operation, and the two must
never be conflated in a way that changes behavior: an operation that depends on
an unconfirmed capability fails closed with an explanation rather than quietly
selecting a degraded path. Distinguishing "unsupported" from "unconfirmed" in
the reported contract is a versioned contract change, not an adapter
convention.

Operations stay typed. There is no generic pass-through action, arbitrary
property update, or per-provider escape hatch in a release build. A
provider-specific feature reaches users as a named capability and typed fields,
which is deliberately slower than forwarding a raw request.

Degradation is explicit and typed. When a requested operation cannot be
represented exactly by the target, the use case either refuses it or names the
affected feature, the reason, and whether the mapping loses information, in the
preview and before a human approves. Silent lossy mapping is forbidden. Because
the loss report belongs to the previewed operation, a commit whose degradation
would differ is rejected like any other mismatch under
[ADR 0004](0004-preview-commit.md).

Degradation reasons are bounded, single-line, and content-free. They explain a
mapping, never quote a message body, subject, address, or provider payload.

Capability discovery must be free of side effects. Probing may not send a
message, create an item, alter server state, or request an authorization or
consent the user has not already granted; that constraint is the subject of
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md).

Each adapter ships redacted synthetic fixtures with contract tests and has a
documented opt-in live smoke test. A capability appears in public documentation
only with both, and the distinction between deterministic coverage and live
observation stays visible.

## Consequences

Callers read a capability set and a degradation report instead of assuming a
uniform model. In exchange, an agent can decide what is possible before
attempting a write, and a human sees what a provider will discard before
approving it. The cost is that an unconfirmed capability blocks an operation
that might have worked, which is the safer of the two errors.

Adding a provider costs more than wrapping its API. Every difference must be
named, typed, fixtured, and either exposed or refused. Undocumented protocol
behavior stays behind capability discovery rather than version assumptions,
which is the same treatment Outlook Web already receives under
[ADR 0002](0002-interactive-browser-session.md).
