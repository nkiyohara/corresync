# CLI

The CLI exposes the supported typed application surface. Deterministic
contracts are fully tested; live Outlook Web observations are recorded
separately in [compatibility evidence](compatibility.md).

## Configure

Create the strict, secret-free default configuration:

```console
corresync config init
corresync config validate
corresync config path
corresync config show
corresync config get policy.max_recipients
```

Use `--config /absolute/path/config.toml` before the command, or set
`CORRESYNC_CONFIG`, to select another file. Initialization refuses to replace an
existing path unless `--force` is explicit. A symlink, directory, or other
non-regular target is rejected even with `--force`.

Use `corresync config edit` to edit TOML through `VISUAL`, `EDITOR`, or the platform
default. The original file is left unchanged if the editor fails or the result
does not pass strict validation. `corresync config set <key> <value>` supports only
the typed keys documented in [configuration.md](configuration.md); it validates
the complete configuration before atomically replacing the file. Stop a
running session owner after a successful edit.

For a shared or delegated mailbox that the same signed-in user already has
permission to use in Outlook Web, add a separate account alias with
`mailbox = "shared@example.com"`, then pass `--account <alias>`. This performs
explicit OWA mailbox routing; it neither uses Microsoft Graph nor grants new
permissions.

## Diagnose and run the opt-in compatibility smoke test

Check the strict config, selected account, Chromium-family executable,
config-scoped local IPC, and any already-running session owner without opening
a browser:

```console
corresync doctor
corresync doctor --json
```

After local checks pass, explicitly opt into live compatibility testing:

```console
corresync doctor --online
```

The online form opens the normal interactive browser when needed, captures the
session only in daemon memory, and performs one bounded folder metadata read,
one inbox metadata read, and one one-hour calendar metadata read. The report
emits no folder, message, event, recipient, mailbox-count, authorization, or
response-body data. A failure is
non-zero and identifies the local, session, mail-contract, or calendar-contract
stage that failed.

An offline report marks an absent daemon as `skip`. An incompatible running
daemon is a failure with an explicit replacement command; diagnostics do not
silently repair state.

See [compatibility evidence](compatibility.md) for the live-test checklist and
the data that is safe to include in a report.

The offline and online reports include a non-failing `update` row. It reports a
cached stable-release comparison, skips development builds and opt-outs, and
never makes release-endpoint failure a doctor failure.

## Check for updates

```console
corresync update
corresync update check
corresync update check --json
```

`corresync update` performs a fresh comparison with the latest public stable GitHub
release. A direct installation downloads the matching archive, validates its
signed Sigstore provenance, SHA-256 checksum, version, OS, and architecture,
and replaces the executable with a rollback copy. A package-managed
installation is not modified; the command prints the exact Homebrew, WinGet,
Scoop, or native-package action.

`corresync update check` is read-only and cache-aware. It never suggests a
prerelease, downgrade, or unattended replacement. Results—including temporary
network failure—are cached for 24 hours. Human TTY output uses concise status
and progress styling; `--json`, pipes, MCP, daemon, and completion output are
unstyled. See the [install guide](install.md#stay-current) for privacy,
rollback, opt-out, and package-manager details.

## Authenticate

```console
corresync auth login
corresync auth login --account work --json
corresync auth login --account work --terminal
corresync auth status
corresync auth logout
```

The command connects to the config-scoped session owner, starting it if needed.
The daemon normally opens a visible dedicated browser profile and waits for
Outlook Web to emit an authorized request. `--terminal` instead launches that
profile headlessly and renders a bounded, text-only control list for an
interactive SSH TTY. It cannot be combined with `--json` or piped input.

The terminal relay sends individual keys to the focused browser element and
never accepts a password flag or complete form value. It handles ordinary DOM
forms but may require visible login for CAPTCHA, passkeys, security keys,
client certificates, native dialogs, or custom graphical controls. JSON output
from normal login contains only account alias, status, and capture time; the
authorization snapshot remains in the daemon.

`auth status` reports only configured aliases, the content-free state
`authenticated`, `pending`, or `signed_out`, and a capture time when one
exists. `auth logout` closes every dedicated browser and clears all in-memory
sessions and pending approvals owned by that config-scoped daemon. Protected
browser profiles remain on the device for the next interactive sign-in. The
former top-level `corresync login` spelling remains a hidden compatibility alias.

## Discover mail folders

```console
corresync mail folders
corresync mail folders --traversal shallow --parent inbox
corresync mail folders --parent-id 'opaque-discovered-id' --json
```

Folder discovery is bounded to 100 entries per page and starts at the message
folder root by default. It returns display names, opaque IDs, parent IDs, folder
class, and aggregate item/unread/child counts. It does not request rules,
permissions, delegates, item content, or mailbox identities. Use an exact ID
with `mail list --folder-id` to address a custom folder without relying on its
mutable display name.

## List mail metadata

```console
corresync mail list
corresync mail list --folder archive --limit 50
corresync mail list --folder-id 'opaque-discovered-id' --json
```

The human table sanitizes terminal control characters and includes the full,
copyable message ID. The JSON form returns the stable `MailPage` schema and
still excludes message bodies and attachment content.

## Search mail metadata

```console
corresync mail search --query 'subject:"Quarterly plan" from:alice'
corresync mail search --query 'kind:email attachment:report' --limit 50
corresync mail search \
  --folder-id 'opaque-discovered-id' \
  --query 'body:proposal NOT from:bot' \
  --json
```

Search is scoped to one distinguished or discovered folder and returns the
same metadata-only `MailPage` as `mail list`. The query uses Outlook's
user-facing AQS subset, is limited to 1024 UTF-8 bytes, and each result page is
limited to 50 entries. It is treated as private input: audit records contain an
operation digest, not the query. There is no raw OWA action, arbitrary JSON, or
message-body field in this interface. AQS operators and property support are
defined by [Microsoft's QueryString reference](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/querystring-querystringtype).

Read plain text for one exact ID returned by the list command:

```console
corresync mail body --message-id 'opaque-message-id'
corresync mail body --message-id 'opaque-message-id' --json
```

The body is bounded to 1 MiB and requested as plain text. Its result includes
bounded file-attachment metadata but not content. Retrieve one returned ID into
a new owner-only file, without overwriting an existing path:

```console
corresync mail attachment \
  --attachment-id 'opaque-attachment-id' \
  --output ./attachment.bin
```

Only metadata marked `kind: file` can be retrieved; attached Outlook items are
listed but not expanded. File content is limited to 2 MiB. `--json` returns it
as base64 instead of writing a file. Human body output strips terminal control
sequences; JSON
output escapes them. If
`preview_sensitive_reads` is enabled, add `--approve` to prepare and consume the
exact read in the same CLI process. MCP keeps preview and commit as separate
calls instead.

## Move one message

Use the exact message ID and change key returned by `mail list` or `mail search`:

```console
corresync mail move \
  --message-id 'opaque-message-id' \
  --change-key 'opaque-change-key' \
  --destination archive

corresync mail move \
  --message-id 'opaque-message-id' \
  --change-key 'opaque-change-key' \
  --destination-id 'opaque-folder-id' \
  --json
```

The command moves exactly one versioned item to one destination discovered
under the selected account. A stale change key fails closed instead of silently
moving a newer version.
`preview_reversible_writes = true` makes the first call return an exact preview;
rerun with `--approve` to consume it in the same CLI process. MCP always keeps
preview and commit as separate tool calls when policy requests approval.

`MoveItem` is attempted once. If Outlook may have committed before a 429, 5xx,
or transport failure, the command reports an unknown outcome and never retries;
list both the source and destination before acting again. Outlook can omit the
new item ID, in which case list the destination to refresh it.

## Mark one message read or unread

```console
corresync mail mark \
  --message-id 'opaque-message-id' \
  --change-key 'opaque-change-key' \
  --state read

corresync mail mark \
  --message-id 'opaque-message-id' \
  --change-key 'opaque-change-key' \
  --state unread \
  --json
```

This is a closed single-property update: the only accepted states are `read`
and `unread`, and callers cannot provide an OWA field name or arbitrary update
JSON. The exact change key and `NeverOverwrite` conflict resolution make stale
updates fail closed. Read receipts are suppressed. Like move and draft, policy
can require `--approve`; like every write, the network request is attempted
once and an ambiguous result is never retried automatically.

## Create a save-only draft

```console
printf 'Hello from Corresync.\n' | \
  corresync mail draft \
    --to alice@example.com \
    --cc bob@example.com \
    --subject 'Synthetic example' \
    --body-file -

corresync mail draft \
  --to alice@example.com \
  --subject 'From a file' \
  --body-file ./body.txt \
  --json
```

Repeat `--to`, `--cc`, or `--bcc` for additional bare addresses. The command
accepts the body only from a file or stdin so message content does not appear in
the process argument list. Subject and body injection characters, oversized
content, and recipient counts above `max_recipients` are rejected before any
network request.

Use `--body-format html` for reviewed HTML composition. Use `--mode reply`,
`reply-all`, or `forward` together with the exact source message ID and change
key. Reply recipients come from Outlook; forwards require explicit recipients.
Repeat `--attachment ./path` to add up to ten regular files under the per-file
and aggregate bounds.

This operation uses OWA `CreateItem` with `SaveOnly`; it does not send mail.
The transport makes exactly one attempt to avoid duplicate drafts after an
ambiguous timeout. If `preview_reversible_writes` is enabled, add `--approve`
to review and commit the exact draft in the same CLI process. MCP exposes the
preview and commit as distinct tool calls.

## Review and send a new message

Preview only:

```console
printf 'Hello from Corresync.\n' | \
  corresync mail send \
    --to alice@example.com \
    --subject 'Exact send preview' \
    --body-file -
```

After checking every recipient, subject, bounded body preview, body byte count,
and SHA-256 digest, repeat the exact command with `--approve`. The CLI prints
the normalized review to stderr immediately before committing and writes the
result to stdout. `--json` returns the same stable preview or result schema.

The same `--body-format`, `--mode`, reference-ID, and `--attachment` flags are
available for sending. Sending is always an external-write `preview -> commit`;
no configuration can
bypass it. The daemon binds the full immutable composition, account, caller,
expiry, and one-time token. A composition without attachments calls
`CreateItem` with `SendAndSaveCopy`. One with attachments saves the exact draft,
adds one bounded attachment batch, and submits that version with `SendItem`.
No write request retries. If any step has an unknown outcome, inspect Drafts
and Sent Items before creating another preview, because retrying could leave a
duplicate draft or message.

Delayed delivery, sending an independently selected existing draft, and item
attachments are not silently emulated.

## Permanently delete one message

```console
corresync mail delete \
  --message-id 'opaque-message-id' \
  --change-key 'opaque-change-key'
```

The first call is a destructive preview only. Repeat the exact command with
`--approve` to issue one `HardDelete` for that exact version. It does not move
the message to Deleted Items and cannot be undone through Outlook.

Each invocation connects to the same session-owning daemon for its selected
config. The daemon lazily opens one dedicated browser per account, retains the
in-memory session, executes through `MailService`, and appends content-free audit
events. CLI processes never receive Outlook authorization material.

## Session owner

```console
corresync daemon start
corresync daemon status
corresync daemon status --json
corresync daemon stop
```

`start` launches one background process for the selected absolute config path
and state directory. Ordinary `auth login`, `auth status`, mail, calendar, and
MCP commands start it automatically when absent. `serve` runs the same process
in the foreground for service managers and diagnostics. No TCP port is opened:
Linux and macOS use an
owner-only Unix socket with same-effective-user peer verification, while Windows
uses a local-only named pipe whose ACL grants the current user and SYSTEM.

Every start rotates an additional 256-bit bearer credential stored in a private
state file. Requests are strict, versioned, size-bounded JSON calls over the
local transport. `stop` is authenticated, drains active calls, and removes the
credential and socket. Stopping an idle daemon closes all account browsers;
later commands start a fresh owner and reuse the protected browser profiles.
After a binary update, the next command verifies the unchanged config digest,
drains an older release through the same authenticated lifecycle controls, and
starts the installed binary automatically. Config edits still require an
explicit `corresync daemon stop`.

## List calendar metadata

```console
corresync calendar list \
  --start 2026-07-17T00:00:00Z \
  --end 2026-07-18T00:00:00Z
corresync calendar list \
  --start 2026-07-17T00:00:00+01:00 \
  --end 2026-07-24T00:00:00+01:00 \
  --json
```

The interval is start-inclusive, end-exclusive, and limited to 31 days. The
transport converts absolute RFC3339 boundaries to UTC before calling
`GetCalendarView`. Results contain event metadata but exclude bodies, attendee
lists, attachments, and online-meeting join URLs. The human table includes the
full event ID and change key needed by later versioned update or cancellation
commands.

## Review and create a calendar event

Preview an appointment without attendees:

```console
printf 'Private planning notes.\n' | \
  corresync calendar create \
    --subject 'Planning block' \
    --start 2026-07-20T09:00:00+01:00 \
    --end 2026-07-20T10:00:00+01:00 \
    --location 'Room Example' \
    --body-file -
```

Add `--required-attendee` or `--optional-attendee` repeatedly to prepare a
meeting. Add `--teams-meeting` to ask Outlook to provision a Microsoft Teams
join link. The first call always prints the exact calendar, subject, start, end,
location, attendee sets, whether invitations will be sent, whether a Teams link
will be created, bounded body preview, body byte count, and SHA-256 digest. It
creates nothing.

After reviewing every field, repeat the exact command with `--approve`. The CLI
prints the review to stderr immediately before committing. OWA's specialized
`CreateCalendarEvent` action is attempted exactly once with
`SendToAllAndSaveCopy` when attendees are present, or `SendToNone` for an
appointment. A Teams request uses only the closed `TeamsForBusiness` provider;
the successful commit result includes the join URL when Outlook provisions it.
If submission has an unknown outcome, inspect the calendar before preparing
another event; an automatic retry could create a duplicate or send duplicate
invitations.

Use `--all-day` with midnight start and end boundaries in the reviewed time
zone. An optional `--time-zone` is an Exchange/Windows time-zone ID such as
`GMT Standard Time`; UTC is the default, so use `Z` or `+00:00` boundaries when
omitting it. `--reminder-minutes` enables a bounded reminder.
`--recurrence daily|weekly|monthly|yearly` combines with an interval, weekly
day(s), monthly/yearly day and month, and exactly one end date or count.

The body is plain text, read from a file or stdin, and bounded to 1 MiB.
Recurrence editing after creation, HTML event bodies, calendar attachments,
relative recurrence patterns, and arbitrary online-meeting providers are not
silently approximated. `--json` returns the same stable preview/result schema
used by MCP.

## Update one event version

Use the exact ID and change key from `calendar list`:

```console
corresync calendar update \
  --event-id 'opaque-event-id' \
  --change-key 'opaque-change-key' \
  --subject 'Updated design review' \
  --start 2026-07-20T10:00:00+01:00 \
  --end 2026-07-20T11:00:00+01:00
```

The closed patch accepts subject, plain-text body, start plus end and optional
Exchange time zone, location, all-day status, reminder, and full replacement of
both required and optional attendee lists.
Use `--clear-subject`, `--clear-body`, or `--clear-location` for an explicit
clear. `--body-file` reads replacement content from a file or stdin. Start and
end must be supplied together and remain a positive interval of at most 31
days. Use `--all-day true|false`, `--reminder-minutes`, or
`--disable-reminder`. Attendees are changed only when `--replace-attendees` is
present; then repeat `--required-attendee` and `--optional-attendee`, or provide
neither to clear both lists. There is no arbitrary field URI or JSON update
surface.

The first call always previews and changes nothing. Repeat the exact command
with `--approve` to submit the specialized `UpdateCalendarEvent` action once
with the listed ID/change key and default event scope. A stale key fails closed.
Outlook controls meeting-update notifications, so attendee replacement and
other meeting changes may notify people. List the calendar afterward when
Outlook omits a refreshed change key.

## Cancel one event version

```console
corresync calendar cancel \
  --event-id 'opaque-event-id' \
  --change-key 'opaque-change-key'
```

The first call is a destructive preview only. Repeat it with `--approve` to
call `DeleteItem` once, move that exact event version to Deleted Items, and ask
Outlook to send meeting cancellations to all attendees. A stale change key
fails closed. Recurrence-series editing is not exposed; select only an event ID
whose scope you understand from Outlook.

If update or cancellation has an unknown outcome, inspect the calendar and
Deleted Items before another action. Preparing and committing another preview
could duplicate notifications or target a newer version.

## MCP

```console
corresync mcp serve
corresync mcp setup codex
corresync mcp setup codex --dry-run
corresync mcp setup claude-code
corresync mcp setup claude-code --scope project
corresync mcp setup github-copilot
corresync mcp setup gemini-cli --scope user
corresync mcp setup qwen-code
corresync mcp setup qoder --scope user
corresync mcp config codex
corresync mcp config claude-code
corresync mcp config github-copilot
corresync mcp config gemini-cli
corresync mcp config qwen-code
corresync mcp config qoder
corresync mcp config kimi-code
```

`setup` invokes the supported client's official MCP command without rewriting
unrelated configuration. New registrations default to `corresync` and the
success message reminds users to start a new agent session. `config` prints
native TOML or JSON for review, Kimi Code CLI, project configuration, and
advanced settings. The supported client set is Codex, Claude Code, GitHub
Copilot CLI, Gemini CLI, Qwen Code, Qoder, and Kimi Code CLI. See
[MCP integration](mcp.md) for details.

## Generate shell completion

```console
corresync completion install
corresync completion bash
corresync completion zsh
corresync completion fish
```

`completion install` detects Bash, Zsh, or Fish from `SHELL`; use `--shell` to
override it. The install is idempotent, refuses to replace a different regular
file without `--force`, and rejects symlink targets. It never appends to a shell
startup file.

The generated script invokes the installed `corresync` from `PATH` only while the
shell requests candidates. Completion is derived from the live CLI command
model and performs no Outlook operation.

## Machine-readable behavior

Commands with `--json` write one JSON value to stdout. Interactive progress and
diagnostics go to stderr. Exit code `0` means success, `1` means an operation or
diagnostic failed, and `2` means command-line usage was invalid. Errors do not
include authorization values or OWA response bodies. Human output uses color
only on an interactive terminal and honors `NO_COLOR` and `TERM=dumb`.
