# ADR 0013: Read-only import and a local staging boundary

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-07-28

## Context

Importing from Apple Mail, Thunderbird, Outlook for Windows or Mac, and archive
files is the fastest way to onboard an existing user. It is also the most
dangerous surface in the roadmap.

Another application's profile contains passwords, OAuth tokens, and cookies
that are readable but not ours to reuse. Reading those directories can require
an operating-system privacy grant the user should understand before approving.
A naive import can write thousands of messages into a live mailbox, which is
unreviewable in a preview and effectively irreversible.

Local caches are not archives, either. An IMAP profile's cache is a disposable
copy of server state, while POP mail, local-only folders, and exported archives
may exist nowhere else.

## Decision

Treat account configuration, authentication, and local data as three separate
operations rather than one migration.

Scanning and preview are strictly read-only. They open source profiles and files
read-only; they never modify, move, lock, or delete a source, and never contact
a provider to write. Any operating-system privacy permission required is named
and explained before it is requested, including what will be read and what will
not.

Secrets are never silently reused. Passwords, OAuth tokens, refresh tokens,
cookies, and browser session material belonging to another application are not
read, copied, or reused even when the files are readable. An imported account
reauthenticates through the normal route, and an operating-system credential
facility is used only through the explicit consent flow in
[ADR 0012](0012-credential-free-discovery-and-explicit-selection.md).

Imported data lands only in a local staging area owned by this project. Import
never writes to a remote provider. Uploading staged data is a separate,
explicitly requested operation that goes through the preview and commit boundary
of [ADR 0004](0004-preview-commit.md), resolves one exact account and container
under [ADR 0010](0010-account-identity-and-isolation.md), runs in bounded
batches, and keeps its own audit record.

For IMAP-class accounts, reconnect and resynchronize server state instead of
importing a local cache. Import applies to POP mail, local-only folders, and
archive files.

Deduplicate mail by `Message-ID` together with conservative metadata and content
hashes, and calendar entries by their iCalendar `UID` and recurrence identifier.
Where identity is ambiguous, keep both copies and report the conflict rather
than merging.

Preserve raw MIME, original dates, flags, folder and label structure, and source
provenance. Report every lossy mapping, such as folders against labels,
recurrence, or provider-specific metadata, as part of the preview and using the
degradation contract of
[ADR 0009](0009-provider-capability-degradation-contracts.md).

Mobile platforms use exported files or reauthentication. Reading another
application's sandbox is not attempted.

## Consequences

Import is slower and more explicit than a single migrate command, and it costs
local disk for staged data that a server may already hold. Users reauthenticate
every imported account, which is the intended consequence of refusing to inherit
another application's credentials.

Staged data is a new local asset containing message content and metadata, so it
inherits this project's owner-only permissions and redaction rules rather than a
weaker convention for imported files.

Archive-format support is a separate question. Which PST and OLM implementation
is acceptable on licensing, fidelity, and security grounds remains open, and
this decision deliberately does not settle it.

## Initial implementation

The shipped import surface stops at bounded, read-only, account-local staging
and safe staging purge. It recognizes implemented exports/archives, Maildir,
and Thunderbird profiles and reports unsupported formats explicitly. There is
no remote upload command, no provider authentication, and no reuse of another
application's credentials. Adding staged-data upload remains a separate future
architectural and product decision.
