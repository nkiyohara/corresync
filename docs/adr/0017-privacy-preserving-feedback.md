# ADR 0017: Privacy-preserving local feedback

- Status: accepted
- Date: 2026-07-28

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
