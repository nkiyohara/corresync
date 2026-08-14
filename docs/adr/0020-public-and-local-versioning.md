# ADR 0020: Public and local versioning policy

- Status: accepted
- Date: 2026-07-29

## Context

Corresync has several interfaces with different audiences and lifetimes:
human CLI text, script-facing commands and JSON, MCP tools and resources,
secret-free configuration, the private daemon protocol, provider capability
and degradation records, and local audit/state schemas.

Calling all of them "stable" without defining stability creates incompatible
expectations. A human-readable table can improve without breaking a script,
while renaming one JSON field or MCP tool can. A daemon protocol can require
exact-version replacement without being a public network API. A configuration
migration can preserve user intent while still refusing an unsafe old default.

Compatibility also cannot preserve behavior that violates Corresync's trust
boundaries. No old client, configuration, or provider route may request secret
storage in core, cross-account writes, automatic authentication, ambiguous
write retries, or another behavior that current policy forbids.

## Decision

Adopt the following compatibility policy as the contract for the 1.0 release
line. Before 1.0, patch releases follow it and minor releases identify any
necessary provisional break explicitly. A surface becomes part of the 1.0
baseline only when it is documented and covered by the compatibility fixtures
required below.

Corresync follows SemVer for the product release:

- a patch fixes behavior without changing a documented accepted input,
  meaning, effect, or machine output;
- a minor release may add commands, optional fields, tools, resources,
  capabilities, and migrated schema versions;
- a major release is required to remove or reinterpret a supported public
  surface after its deprecation window.

Security and correctness exceptions are narrower than ordinary breaking
changes. A release may reject an unsafe input immediately when continuing
would violate a permanent invariant or cause an unreviewed effect. It must
name the affected versions, explain recovery or migration, and must not emulate
the unsafe behavior behind a compatibility flag.

### Surface inventory

The release candidate maintains one reviewed inventory with these classes:

- Human presentation includes styled tables, icons, prose, and progress.
  Meaning and safety prompts are stable; layout and wording are not machine
  APIs.
- CLI invocation includes command paths, documented long flags, and exit
  status. It is a stable public interface.
- CLI JSON includes documented fields, types, enum meanings, and omission
  rules. It is a stable public machine interface.
- MCP includes tool/resource names, schemas, annotations, and effects. It is a
  stable negotiated machine interface.
- Configuration includes the TOML version, fields, defaults, and validation.
  It is a versioned durable user interface.
- Daemon IPC includes the envelope version, closed methods, and typed payloads.
  It is an exact-version private local interface.
- Local records include queue, cursor, audit, staging, and feedback state. They
  are versioned durable private data.
- Provider evidence includes capability fields and degradation feature codes.
  It is a stable normalized observation interface.

Source package APIs below `internal/`, provider wire formats, undocumented
human output spacing, fixture implementation details, and temporary files are
not public compatibility surfaces. They remain subject to the permanent
security and data-integrity rules.

### CLI invocation and text

Documented command paths, long flag names, argument meanings, defaults that
change effects, and exit statuses are stable. The accepted exit-status classes
remain:

- `0`: success or an intentional preview;
- `1`: runtime, policy, provider, or compatibility failure;
- `2`: command usage failure.

Adding an optional command or flag is additive. Making an optional flag
required, changing a default in a way that changes account, target, data
release, or effect, reusing a flag for another meaning, or moving a command
path is breaking.

Human-readable output is for humans. Scripts must use `--json`. Wording,
column width, icons, ordering chosen only for presentation, progress, and color
may change, but safety prompts must continue to identify the exact account,
target, effect, and whether the operation is previewed, committed, degraded,
conflicted, or unknown.

Documented shell completion is generated from the current accepted command
tree. Completion emits candidates only: it never emits notices, update checks,
browser prompts, or deprecation warnings into the shell protocol.

### CLI JSON and errors

Each `--json` command writes exactly one unstyled JSON value to stdout.
Diagnostics and deprecation records use stderr. Success fields obey
[the stable JSON contract](../json.md).

Within a major release:

- a minor release may add an optional field whose omission preserves the old
  meaning;
- existing field names, types, requiredness, null/omission meaning, units,
  bounds, provenance, effects, and security classification do not change;
- a field is not repurposed after deprecation;
- a closed enum gains a value only when its documented consumer contract
  already requires unknown-value handling; otherwise that is breaking;
- array ordering changes only when documented as unordered;
- an error does not become an empty or partial success.

The general stable CLI error contract is the exit-status class and the absence
of a success JSON value. Human error prose on stderr is not a parseable API.
Authentication action-required failures are the first bounded exception. They
use contract version `1`, stable codes, content-free account/service routing
metadata, executable-plus-argv recovery, and an explicit no-automatic-retry
policy. The JSON error string is a complete legacy fallback; MCP additionally
carries the same object as structured error content. Provider response text
and authentication material never enter this contract. Adding a field follows
the public JSON rules; changing the meaning of an existing field, code, or
retry policy is breaking. Any other future JSON error envelope requires its
own schema version and stable bounded code before scripts are told to consume
it. Raw provider errors, response bodies, queries, paths, tokens, or mailbox
values never become error compatibility fields.

### MCP tools and resources

MCP version negotiation completes before Corresync publishes its catalog.
Unsupported protocol versions fail initialization; Corresync does not expose a
partial catalog guessed from client branding.

Tool and resource-template names, input and output schemas, annotations, and
effect classes are stable together:

- a new tool or resource template is additive;
- a new optional input with a conservative default is additive;
- a new optional output field follows the JSON rules;
- a required input, rename, removal, type change, wider data release, or change
  from read-only to write/destructive is breaking;
- a preview tool and its commit tool never collapse into one call;
- changing an annotation never weakens application policy and is breaking when
  it would make a client present the effect less cautiously;
- a resource update remains data and never becomes permission to start a model
  turn.

MCP deprecation is carried in protocol-valid catalog metadata and the
description while the old operation remains callable. When the negotiated MCP
revision has no standard deprecation field, Corresync uses a documented,
namespaced `_meta` record. It never writes prose, warnings, or logs to MCP
stdout outside valid JSON-RPC messages. Stderr remains diagnostic and contains
no mailbox content or approval value.

### Configuration and migrations

Configuration remains strict, bounded, secret-free TOML with a required
integer version. Unknown fields, missing versions, unsupported future versions,
and ambiguous values fail before authentication, provider access, daemon
replacement, or a write.

An additive field has a fail-safe default. In particular, monitoring, agent
execution, egress, authentication, provider selection, and broader write
authority always default to disabled or explicit selection, including after
migration.

Changing shape or meaning increments the configuration version and supplies a
deterministic one-way migration for every supported older version. Migration:

1. parses the old schema strictly and within current bounds;
2. preserves stable account identity and account-local ownership when the old
   schema has that identity;
3. creates new identity once when the old schema did not;
4. never imports another application's credentials or migrates transient IPC
   authorization;
5. writes atomically under the existing lock and owner-only permissions;
6. preserves a documented rollback copy or stops with recovery instructions;
7. is idempotent when the candidate is started again.

The current pre-1.0 baseline accepts the v2 single-route configuration through
an in-memory migration to v3 and migrates the legacy v1 default installation
through its explicit rollback-safe path. The exact versions supported by 1.0
are frozen by its release evidence; support is not inferred merely because a
decoder type remains in source.

Downgrade is never automatic. A binary that sees a newer config or durable
state schema fails without rewriting it. A release may provide an explicit
export or rollback tool, but it cannot discard fields it does not understand.

### Daemon IPC and replacement

Daemon IPC is authenticated private local IPC, not a remotely supported API.
Every envelope carries one exact integer protocol version. Any method,
request/response shape, bound, or semantic change increments it, including an
otherwise additive method.

The per-service authentication status and action-required response shape raise
the private daemon protocol from 22 to 23. Older owners are replaced through
the existing authenticated mismatch path; no read, write, approval, or login
is translated or replayed during replacement.

Client and daemon versions must match before an application operation. On a
proved mismatch, only the content-free status inspection and graceful shutdown
replacement path from
[ADR 0003](0003-authenticated-local-session-owner.md) may use the daemon's
reported version. It authenticates the same owner generation and config digest
before shutdown.

No login, read, preview, commit, monitoring, import, or lifecycle operation is
replayed or translated across a mismatch. Replacement drains the old owner,
discards its in-memory sessions and approvals, starts the candidate, and
requires a fresh call. A failed replacement leaves a clear stopped or
incompatible state; it never falls through to an old daemon method.

Old standalone clients therefore fail closed against a newer owner rather than
receiving a negotiated subset. Package managers and self-update replace the
binary as one unit and the next command performs the authenticated owner
replacement.

### Capability, degradation, and audit records

Normalized capability fields and their meanings are versioned with CLI JSON
and MCP. Adding a false-by-default optional capability is additive. Reversing
the meaning of a boolean, changing an online-meeting kind, inferring capability
from provider branding, or treating unconfirmed as supported is breaking and
may also violate a permanent invariant.

A documented degradation feature code is stable. A minor release may add a
code; it does not reuse a code for a different loss. The bounded human reason
may improve and is not a machine key. The `lossy` value and affected operation
remain semantically stable. Provider-specific limitations stay visible rather
than being removed from output for compatibility.

Every durable audit, event-queue, cursor, import-staging, feedback, and similar
record has an explicit schema version before it needs migration. Readers accept
only documented older versions, validate bounds before allocation, and migrate
under the same account-local ownership and atomicity rules as the store.
Unsupported versions fail without truncation or partial rewrite.

Audit evolution never admits content that the current redaction contract
forbids. Old records with now-disallowed detail are not echoed during an error
or migration. Additive fields stay content-free, and event/audit meanings are
not silently normalized across accounts or providers.

### Deprecation protocol

Ordinary public removal requires a deprecation period of at least two minor
release lines before the next major release removes the surface. The release
that starts deprecation states:

- a stable deprecation code;
- the exact command, flag, JSON field, MCP operation, or config field;
- the replacement and semantic differences;
- the first release in which removal is permitted;
- whether migration is automatic, manual, or impossible.

Human CLI use receives a warning on stderr. JSON mode keeps its one stdout value
unchanged and emits a single bounded, machine-readable stderr record with a
schema version, deprecation code, replacement, and earliest removal release.
Completion mode suppresses warnings. MCP uses catalog metadata as described
above. Configuration validation and migration report deprecations without
printing secrets or private values.

A deprecation warning is emitted at most once per process and affected surface,
never during a commit after its preview, and never in a way that changes the
operation digest or result. Security removals may skip the ordinary window only
under the exception above.

### Compatibility fixtures and release gate

The first stable release records immutable, synthetic fixtures for its complete
surface inventory. Every later candidate exercises the oldest still-supported
fixture, not only the immediately previous release:

- CLI fixtures invoke supported command paths/flags and assert exit class,
  stdout channel, and warning isolation;
- JSON fixtures decode the oldest output shapes with the candidate consumer
  contract and confirm new outputs preserve required meanings;
- MCP fixtures initialize at every supported protocol revision and snapshot
  names, schemas, annotations, resources, and preview/commit pairs;
- configuration and durable-state fixtures migrate from every supported schema
  and prove atomicity, idempotence, identity preservation, safe defaults, and
  unsupported-downgrade refusal;
- daemon fixtures prove exact-match operation, authenticated mismatch
  inspection/replacement, and that no application call or write is replayed;
- capability/degradation/audit fixtures preserve code meaning, account
  isolation, redaction, and bounds.

Fixtures contain only synthetic identities and no credential, personal data,
live provider payload, or captured authorization. Opt-in live observations
remain separate and do not replace deterministic compatibility tests.

Removing an old fixture is itself a reviewed compatibility change and occurs
only after its documented support window ends. The 1.0 tag is blocked until
the conformance work tracked separately from this policy covers the frozen
baseline on every supported platform.

### Permanent invariants

No release, including a major release, deprecates or negotiates away:

- inward dependency direction and typed provider-neutral use cases;
- interactive, user-owned authentication and secret-free core/configuration;
- credential-free discovery and explicit provider selection;
- stable account identity, account-local state, and exact write targeting;
- server-enforced, caller/account/target/payload/effect-bound preview/commit;
- no automatic retry or replay after an unknown write outcome;
- valid TLS, exact-origin/redirect controls, and no authentication bypass;
- explicit capability evidence and visible degradations;
- bounded parsing, responses, concurrency, durable state, and audit redaction;
- synthetic default tests and separately authorized opt-in live observations;
- monitoring, runner execution, and egress as separate disabled-by-default
  consent boundaries.

An old input that cannot satisfy these rules fails closed with a typed or
bounded compatibility error. Compatibility is never authority to broaden an
account, route, credential, effect, or disclosure.

## Consequences

Integrators know which surfaces can evolve additively and which changes require
a migration or major release. Maintainers must inventory machine interfaces,
carry old fixtures for the declared window, and classify compatibility before
merging a change.

The exact daemon protocol deliberately remains stricter than public CLI, JSON,
and MCP compatibility. This costs a controlled local owner restart during
upgrades and prevents a more dangerous outcome: an old process partially
executing a new write contract.

The 1.0 milestone remains blocked on implementation and platform evidence for
these policies. Accepting the policy does not claim that every pre-1.0 shape or
provider route is already proven live-compatible.
