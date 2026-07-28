# ADR 0014: Opt-in monitoring, events, and dispatch boundaries

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-07-28

## Context

A bridge that notices new mail is far more useful than one that waits to be
asked. It is also the point at which a reviewed, human-invoked tool becomes an
unattended process holding a live mailbox and an outbound network path.

"Add a watcher" conflates three different risks: collecting data at all,
executing a model or command on it, and sending message content off the machine.
Each has a different blast radius, and a single switch would grant all three.

Message content is attacker-controlled. Anyone can send mail to a monitored
mailbox, so an event payload is untrusted input that reaches an automated
consumer without a human reading it first.

## Decision

Monitoring, dispatch, and data egress are three separate consent boundaries.
Each is disabled by default, each is enabled only by an explicit human
configuration action scoped to an account and a rule, and enabling one never
implies another. A fresh installation, an upgrade, and an account import all
leave every one disabled.

Modes are strictly ordered:

```text
off -> notify -> queue -> agent
```

`off` collects nothing. `notify` emits a local notification on a platform with
a validated adapter. `queue` persists matching events in a durable local
outbox. `agent` dispatches to an explicitly configured runner. `notify` and
`queue` require no AI provider, and neither implies that one exists. Linux uses
`notify-send` and macOS uses `osascript`, with native argument separation and a
short execution timeout. Windows `notify` is unavailable until the product
installs a registered AppUserModelID, so setup fails before configuration
changes while `queue` and `agent` remain usable.

The pipeline is a provider watcher, cursor and watermark recovery,
deduplication, a metadata-first policy filter, a durable local queue, and then
sinks. Every adapter persists its own cursor and recovers after a restart or a
missed notification. Duplicate delivery is identifiable and safe rather than
assumed away. Disabling monitoring stops collection and requires an explicit
choice to retain or purge the local queue.

Events are metadata first: event identifier, account, source object identity,
sender, subject, received time, and a trust marker. Bodies and attachments are
fetched only when both the policy and the destination require them, within size
limits. Content inclusion and remote egress default to false, and the effect of
enabling either is disclosed at the moment of enabling, including that message
content will leave the machine.

Mailbox and calendar content is data, never instructions. An automatically
triggered agent is read-only by default, and every external write still passes
through the preview and commit boundary of
[ADR 0004](0004-preview-commit.md) with the account and target binding of
[ADR 0010](0010-account-identity-and-isolation.md). A message, a calendar event,
an MCP client, or an automatically started agent can never broaden its own
watcher scope, filters, accounts, tools, or egress. Those change only through
human configuration.

The monitoring MCP surface is read-only with one local exception. It may expose
`monitor_status` and `events_list`, and an `event_acknowledge` whose only target
is the local event queue. No MCP tool enables monitoring, changes mode, widens a
filter, enables automatic agent execution, enables egress, or purges monitoring
state. Separate account lifecycle tools may add, rename, or remove a route
through the server-enforced caller-bound preview/commit protocol. Account add
cannot authenticate or resolve a credential and explicitly requires a later
local CLI login; account removal discloses its local-state and owned-grant
effects. Resource change notifications may be published where a client
subscribes, but a resource update is not a model turn, carries no write
authority, and implies no permission to start a hosted-model request. New
implementation does not depend on deprecated MCP Sampling.

Operational safety is part of the contract: exclude sent and draft folders,
detect self-generated messages where a provider makes that possible, rate-limit
and debounce dispatch, support quiet hours and batching, bound body and
attachment sizes, and stop dispatch through a circuit breaker. Audit detection,
filtering, queueing, destination, the fields released, the agent result, and
acknowledgement, under the existing redaction rules.

## Consequences

Monitoring does nothing until a human configures it, and enabling it usefully
takes three deliberate decisions instead of one. That friction is the feature:
it is what stops an installed bridge from becoming an unattended agent by
default, including after an upgrade that the user did not read about.

The durable queue and audit trail are new local assets holding mailbox metadata,
so they inherit owner-only permissions and the existing redaction rules. Loop
prevention and self-message detection are best-effort against providers that do
not reliably identify their own sends, which is why the rate limit and circuit
breaker exist as a backstop rather than as tuning.

Read-only MCP monitoring means an agent can observe that something happened and
still cannot act on it except through the same reviewed path a human uses. That
asymmetry is intended, and it is what makes it safe to tell an agent about mail
it did not ask for.
