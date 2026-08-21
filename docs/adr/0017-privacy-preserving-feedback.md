# ADR 0017: Privacy-preserving local feedback

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-08-03 by
  [ADR 0025](0025-opt-in-public-error-feedback.md), which adds a separate,
  smaller, explicit opt-in public issue path while preserving this manual flow.
- Amended: 2026-08-21 to add one bounded local crash record without adding raw
  panic logging, telemetry, or automatic crash upload.

## Context

Useful bug reports need build, platform, installation, configuration-health,
and provider-capability context. Raw command output is unsafe: errors,
arguments, configuration, MCP frames, and diagnostics can contain addresses,
queries, private paths, opaque identifiers, credential references, mailbox
content, or authorization material.

Automatic crash upload would introduce telemetry and a new hosted trust
boundary. Even opening an issue with hidden submission or copying a partial
report before review would make accidental disclosure likely.

## Decision

Add `corr feedback` as a local deterministic report generator. Its schema
accepts only allowlisted public build atoms, coarse platform and installation
data, fixed collection states, aggregate provider IDs/capabilities, and an
optional sanitized last-error record.

The explicitly requested report may also include one sanitized last-crash
record. That record accepts only public build atoms, a UTC occurrence time,
fixed process-role and boundary enums, a deterministic local identifier, and
at most 32 Corresync source symbols with line numbers. Source paths, panic
values, runtime argument values, arbitrary strings, and non-Corresync frames
are not representable.

Raw errors, argument values, account IDs/aliases/addresses, endpoints, private
paths, environment values, queries, mail/calendar/import/event content,
credential references, helper arguments/results, authorization material,
approval tokens, and provider request IDs are not representable in the report
input.

The last-error store is:

- one bounded record, replaced rather than appended;
- owner-only and symlink-safe;
- composed from fixed generalized error classes, command/subcommand
  placeholders, flag names without values, and a deterministic local hash;
- visibly degraded when missing, malformed, oversized, or unsafe.

The separate last-crash store follows the same replace-not-append,
owner-only, bounded, atomic, and symlink-safe rules. Corresync guards its
process and owned goroutine boundaries and writes the reduced record. The
top-level process boundary converts the panic to one fixed diagnostic and an
immediate nonzero exit; owned goroutines re-raise after recording, and a daemon
request panic closes every listener before net/http can recover the connection.
No boundary continues serving uncertain state. The ordinary content-free audit
remains the operation trail, so crash diagnostics do not introduce a second
verbose operation log.

Report generation performs no network request and disables automatic update
discovery. The complete report is printed before any selected external action.
`--copy`, `--save PATH`, and `--open-github` are mutually exclusive:

- copy invokes the platform clipboard adapter only after display;
- save creates a new owner-only file without overwrite or symlink following;
- open launches a prefilled GitHub issue page only after display, states that a
  GitHub account is required, and never submits.

The user remains responsible for reading the visible report before sharing it.
Security reports continue through private vulnerability reporting, not the
public feedback path.

## Consequences

Support reports become reproducible without making telemetry part of the
product. Some failures lose diagnostic specificity; maintainers must request a
smaller synthetic reproducer rather than asking for raw mailbox or
authorization data.

Adding a report field requires a privacy review, bounded type, deterministic
serialization, representative-secret tests, and documentation. An arbitrary
map, raw string catch-all, or automatic upload is an architectural change and
requires a new ADR.

The last-crash field raises the local feedback report schema from version 1 to
version 2. It is included only by explicit `--last-error` review and is not
eligible for the default-off automatic public feedback path.
