# CLI guide

The primary executable is `corr`:

```console
corr --help
corr help account
corr --version
corr version --json
```

Use global `--config PATH` or `CORRESYNC_CONFIG` to select an isolated
configuration. Human output is styled only on an interactive terminal and
honors `NO_COLOR` and `TERM=dumb`. Supported `--json` commands emit one
unstyled machine-readable value.

## Configuration and accounts

```console
corr config init
corr config path
corr config validate
corr config show
corr config get policy.max_recipients
corr config set policy.max_recipients 25
corr config edit
```

Provider route changes belong to the account lifecycle:

```console
corr account discover reader@example.invalid
corr account list
corr account show work
corr account add reader@example.invalid --help
corr account rename work primary
corr account remove old --new-default primary --approve
```

`account discover` uses no credentials and performs no authentication. Its
ranked candidates explain DNS/well-known evidence, confidence, required auth,
and whether the provider is available. `account add` requires an explicit route
when evidence is ambiguous, generates a stable opaque account ID, and leaves
authentication and monitoring off.

Examples:

```console
# Outlook Web
corr account add reader@example.invalid \
  --alias work \
  --provider microsoft-owa \
  --origin https://outlook.cloud.microsoft

# Gmail and Google Calendar with an authorized public client
corr account add reader@example.invalid \
  --alias personal \
  --provider google-api \
  --calendar-provider google-api \
  --oauth-client-id synthetic-public-client \
  --oauth-redirect-uri http://127.0.0.1:8765/callback \
  --authorization-key personal-google \
  --approve-oauth

# IMAP/SMTP mail plus CalDAV calendar
corr account add reader@example.invalid \
  --alias standards \
  --provider imap-smtp \
  --calendar-provider caldav \
  --imap-host imap.example.invalid \
  --imap-port 993 \
  --imap-tls implicit \
  --smtp-host smtp.example.invalid \
  --smtp-port 587 \
  --smtp-tls starttls \
  --caldav-endpoint https://calendar.example.invalid/dav \
  --credential-key standards-mail \
  --calendar-credential-key standards-calendar \
  --approve-credential \
  --approve-calendar-credential
```

No account command accepts a password or token.

## Authentication and doctor

```console
corr auth login --account work
corr auth status
corr auth logout

corr doctor
corr doctor --online --account work
```

`auth login` invokes the route's browser/keyring/helper authentication.
`--terminal` is an experimental Outlook-Web-only browser relay and requires an
interactive TTY. `auth status` is content-free.

`doctor` validates local config, browser prerequisites, IPC, daemon state, and
update policy. `--online` is an explicit live compatibility check; it is never
run by default tests.

## Mail reads

```console
corr mail folders --account work
corr mail folders --parent inbox --traversal shallow

corr mail list --account work --folder inbox --limit 25
corr mail list --folder-id opaque-folder-id --json

corr mail search --account personal \
  --query 'subject:"Quarterly plan" from:reader' \
  --limit 25

corr mail body --account work --message-id opaque-message-id
corr mail attachment \
  --account work \
  --message-id opaque-message-id \
  --attachment-id opaque-attachment-id \
  --output ./synthetic-attachment.bin
```

Search syntax belongs to the selected provider. Gmail and Graph queries can
differ from Outlook AQS; the authenticated capability/degradation report makes
that visible. Lists return metadata only. Body and attachment operations are
explicit sensitive reads and may require a second call with `--approve`,
depending on policy.

Attachment output is bounded and never overwrites an existing path.

## Cross-account reads

```console
corr mail search --all-accounts \
  --query 'subject:"Quarterly plan"' \
  --limit 50 \
  --time-zone UTC

corr agenda list --all-accounts \
  --start 2026-07-28T00:00:00Z \
  --end 2026-07-29T00:00:00Z \
  --time-zone Europe/London \
  --limit 50
```

These are read-only fan-outs across isolated account services. Results are
normalized, deterministically sorted, globally bounded, and tagged with account
alias and provider provenance. Unsupported accounts and provider failures are
reported explicitly alongside successful results. No cross-account write
exists.

## Mail drafts and sends

```console
# Save-only draft
printf 'Synthetic draft.\n' | \
  corr mail draft \
    --account work \
    --to reader@example.invalid \
    --subject 'Draft example' \
    --body-file -

# Preview a send
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Send example' \
    --body-file -

# Commit only after reviewing the exact preview
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Send example' \
    --body-file - \
    --approve
```

Compose supports new, reply, reply-all, and forward modes; text or HTML; and
bounded repeatable attachments. Review output includes normalized source
version, recipients, subject, body format/size/digest, and attachment
size/digest. It does not print the body.

A draft always uses save-only semantics. Every external send requires exact
commit. Changing any input after preview invalidates the approval.

## Mail organization

```console
corr mail move \
  --account work \
  --message-id opaque-message-id \
  --change-key opaque-change-key \
  --destination archive

corr mail mark \
  --account work \
  --message-id opaque-message-id \
  --change-key opaque-change-key \
  --state read

corr mail delete \
  --account work \
  --message-id opaque-message-id \
  --change-key opaque-change-key
```

Reversible writes may preview according to policy. Permanent delete always
previews and requires `--approve`. Provider degradations state when an atomic
version precondition is unavailable. Corresync never treats that limitation as
permission to retry an ambiguous request.

## Calendar

```console
corr calendar list \
  --account work \
  --start 2026-07-28T09:00:00Z \
  --end 2026-07-28T17:00:00Z

printf 'Synthetic agenda.\n' | \
  corr calendar create \
    --account work \
    --subject 'Design review' \
    --start 2026-07-28T09:00:00Z \
    --end 2026-07-28T10:00:00Z \
    --time-zone UTC \
    --required-attendee reader@example.invalid \
    --body-file -
```

Creation, update, and cancellation always use preview/commit:

```console
corr calendar update \
  --account work \
  --event-id opaque-event-id \
  --change-key opaque-change-key \
  --subject 'Revised review'

corr calendar cancel \
  --account work \
  --event-id opaque-event-id \
  --change-key opaque-change-key
```

Repeat the exact reviewed command with `--approve` to commit. Supported fields
include bounded subject/body, absolute start/end, time zone, location, all-day
state, reminder, closed recurrence forms, and complete required/optional
attendee lists. Provider meeting-link creation is accepted only when the
authenticated calendar route reports a supported capability.

## Read-only import staging

```console
corr import scan ./synthetic-export
corr import scan ./synthetic-export --format auto --approve-read --json
corr import purge --account work --approve
```

The first scan identifies and bounds one explicit source without granting
content access. `--approve-read` binds read-only access to that exact path and
creates an upload-free account-local staging plan. Purge removes only the
staging area, never the source.

## Monitoring and events

Monitoring starts off and advances one level at a time:

```console
corr monitor enable --mode notify \
  --notification-field sender \
  --notification-field subject \
  --approve

corr monitor enable --mode queue --approve
corr monitor status
corr events list --state all
corr events acknowledge evt_0123456789abcdef0123456789abcdef
```

To enable a local agent runner:

```console
corr monitor enable --mode agent \
  --runner /absolute/path/to/runner \
  --runner-argument process-event \
  --runner-egress local \
  --runner-field sender \
  --runner-field subject \
  --approve
```

The runner is invoked directly, without a shell, and receives bounded JSON on
stdin. A remote egress declaration additionally requires
`--approve-remote-egress`. Filters, quiet hours, debounce, retention, hourly
rate limits, and timeouts are available through `corr monitor enable --help`.

Disable requires explicit queue treatment:

```console
corr monitor disable --retain-queue --approve
# or:
corr monitor disable --purge-queue --approve
```

`events acknowledge` is idempotent. Permanent queue deletion requires:

```console
corr events purge --account work --approve
```

## Privacy-preserving feedback

```console
corr feedback
corr feedback --last-error
corr feedback --copy
corr feedback --save ./corresync-feedback.json
corr feedback --open-github
```

The report is generated locally and printed in full before the selected action
runs. `--copy`, `--save`, and `--open-github` are mutually exclusive.
`--save` creates a new owner-only file and never overwrites. `--open-github`
launches a prefilled browser page, requires a GitHub account, and never submits.

Report generation performs no network request. The last-error record is a
replace-in-place, bounded, owner-only file containing only generalized error
classes, a local hash ID, command/subcommand placeholders, and flag names.

## Daemon

```console
corr daemon start
corr daemon status --json
corr daemon stop
```

Normal provider commands start the config-scoped daemon on demand. It owns
sessions and exposes authenticated local IPC only—never TCP. A config digest
change requires an explicit stop. A compatible old binary can be drained and
replaced without retrying an application operation.

## MCP

```console
corr mcp setup codex
corr mcp setup claude-code --scope user
corr mcp setup github-copilot
corr mcp setup gemini-cli --scope user
corr mcp setup qwen-code
corr mcp setup qoder --scope user

corr mcp config kimi-code
corr mcp serve
```

See [mcp.md](mcp.md) for exact client setup, tools, resources, and the bundled
Agent Skill.

## Completion

```console
corr completion install
corr completion bash
corr completion zsh
corr completion fish
```

Install detects Bash, Zsh, or Fish and is idempotent. It never appends repeated
startup-file lines. Use `--shell` to override and `--force` only after reviewing
a different existing regular file.

## Updates

```console
corr update check
corr update check --json
corr update
corr update --json
```

Package-managed installations print their owner command. Direct installs
verify Sigstore identity, checksum, version, OS, and architecture before a
rollback-capable replacement. No ambiguous update is retried.

## Exit behavior

- `0`: command completed, including a preview that intentionally made no
  external change;
- `1`: runtime, policy, provider, validation, or explicit action failure;
- `2`: CLI usage or parse error.

Errors go to stderr. Stable JSON never receives styling or automatic update
notices. A failed non-feedback command records only the sanitized local
last-error shape for a later explicit `corr feedback --last-error`.
