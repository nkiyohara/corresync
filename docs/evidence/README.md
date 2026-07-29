# Live evidence index

Live compatibility requires a content-free record tied to the exact commit
that was exercised. Passing fixtures, cross-compilation, or an observation of
an earlier build does not promote a current implementation to “observed.”

## v0.8 marker

The v0.8 provider routes, native platform IPC implementations, and MCP clients
are **live-unobserved**. No live observation record is committed for the v0.8
code.

Published distribution evidence remains bound to its exact tag and workflow;
it does not promote provider or native-platform rows to live-observed.
Historical Outlook Web, local terminal, macOS IPC, Codex, and Claude notes did
not capture an exact commit and therefore remain non-evidentiary context.

The [v0.8 acceptance map](acceptance-v0.8.md) links roadmap criteria to code
and deterministic tests. It does not promote any row to live-observed.

## Record template

Add a separate Markdown file only after an authorized opt-in run. Keep it
content-free:

```text
Commit:
Release candidate or local build:
Observation date:
OS and architecture:
Browser/keyring family and version:
Provider ID and broad deployment class:
Capability or operation:
Content-free result stage:
```

Do not record account aliases, IDs, addresses, tenant or endpoint names,
message/calendar identifiers, subjects, recipients, attendees, bodies,
queries, source paths, screenshots, provider payloads, credentials, approval
values, join URLs, request IDs, or runner arguments.

If the observed implementation surface changes after the recorded commit, the
corresponding row in `docs/compatibility.md` returns to `live-unobserved` until
the new commit is exercised.
