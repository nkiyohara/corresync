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
corr settings
corr setup
corr setup you@example.com --alias personal
corr config init
corr config path
corr config validate
corr config show
corr config get policy.max_recipients
corr config set policy.max_recipients 25
corr config edit
```

Interactive `corr setup` is the user-first path. It creates a provider-neutral
configuration when absent, performs credential-free discovery, explains and
previews selectable mail/calendar routes, and adds an account only after
confirmation. Authentication and provider verification remain separate
choices. `corr setup ADDRESS` keeps the deterministic, non-interactive contract
for scripts; it never prompts or authenticates. `config init` is the lower-level
alternative and creates a valid configuration with zero accounts and no
selected provider.

Provider route changes belong to the account lifecycle:

```console
corr account discover reader@example.invalid
corr account list
corr account show work
corr account add reader@example.invalid --help
corr account rename work primary
corr account remove old --new-default primary --approve
```

`corr settings` is the guided path for everyday changes. Its arrow-key form is
organized around configured accounts, updates, safety, and browser sign-in.
The top-level Accounts category opens a second level containing **Add account**
and the configured account list. Selecting an account opens graphical or
terminal sign-in, rename, default, and removal actions for that account; Back
returns to the account list. Removal shows the local-state impact, defaults to
cancellation, and asks for a replacement before deleting the current default.
The form stops and restarts a running session owner around account
configuration changes. Every choice explains its effect and displays the exact
direct command. Set `CORRESYNC_ACCESSIBLE=true` for line-oriented screen-reader
prompts and type `:cancel` to leave an input step safely. The remaining
commands are stable direct forms for scripts and advanced use.

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
  --mail-provider microsoft-owa \
  --origin https://outlook.cloud.microsoft

# Coming soon: Gmail API and Google Calendar (disabled pending OAuth approval)
corr account add reader@example.invalid \
  --alias personal \
  --mail-provider google \
  --calendar-provider google \
  --oauth-client-id synthetic-public-client \
  --oauth-redirect-uri http://127.0.0.1:0 \
  --authorization-key personal-google \
  --approve-oauth

# IMAP/SMTP mail plus CalDAV calendar
corr account add reader@example.invalid \
  --alias standards \
  --mail-provider imap-smtp \
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

# Calendar-only Graph route with a dynamically allocated loopback port
corr account add reader@example.invalid \
  --alias calendar-only \
  --mail-provider none \
  --calendar-provider microsoft-graph \
  --calendar-oauth-client-id synthetic-public-client \
  --calendar-oauth-redirect-uri http://127.0.0.1:0/callback \
  --calendar-authorization-key calendar-graph \
  --approve-calendar-oauth

# Coming soon: an explicit task-only route needs no email discovery
corr account add \
  --alias tasks \
  --mail-provider none \
  --calendar-provider none \
  --task-provider todoist
```

Mail and calendar providers are independent. Calendar-specific OAuth flags
default to their shared `--oauth-*` values; set them explicitly when the two
services use different providers or grants. `--provider` remains a compatibility
alias for `--mail-provider`. No account command accepts a password or token.
Approved removal discloses and purges account-local state plus an unshared
Corresync-owned OAuth grant; it never deletes an external standards credential.

The Google example documents the future route shape. In this RC it returns an
approval-pending message, persists nothing, and starts no browser, keyring, or
Google API work. Outlook Web, Graph, JMAP, IMAP/SMTP, and CalDAV routes remain
available as described above. The task-only example likewise documents the
stable route shape, but all task providers currently return an unavailable
message before discovery, authentication, or configuration writes.

## Authentication and doctor

```console
corr auth login --account work
corr auth status
corr auth logout --account work
corr auth logout

corr doctor
corr doctor --online --account work
corr doctor --online --connection-only --account personal
```

`auth login` displays the exact OAuth scope set before any provider page can
open, then invokes the route's browser/keyring/helper authentication. Targeted
logout preserves every other account and the daemon; logout without an account
closes the entire local session owner.
`--terminal` is an optional Outlook-Web-only browser relay and requires an
interactive TTY. The staged `google` route is disabled pending production OAuth
approval and stops before sign-in or network access. After a separate activation
release it will use normal-browser OAuth and pinned Gmail/Calendar APIs; it will
never automate Google sign-in. `auth status` is content-free.

`doctor` validates local config, starts and closes a blank sandboxed Chromium
target when the selected route needs a browser, checks IPC, daemon state, and
update policy. It performs no navigation or authentication. `--online` validates
only an already authenticated session; it never starts login or OAuth. Run
`auth login` first. The report includes the configured OAuth scope set and is
never run by default tests.
For standards routes, `--connection-only` reports when the active session last
established TLS and authorization, with Mail/Calendar capability state shown
separately. The status check makes no fresh authentication attempt and requests
no folder, message, event, contact, or attachment metadata.

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

Outlook Web accepts mail list pages of up to 25 items; request later pages with
`--offset`. Its mail time-zone value is an Exchange/Windows identifier such as
`GMT Standard Time`, not an IANA identifier such as `Europe/London`. Omit
`--time-zone` to use the UTC default. Unsupported values and provider-side
search failures return an actionable error instead of an opaque HTTP 500.

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

# Preview one existing saved draft without reconstructing it
corr mail send-draft \
  --account work \
  --draft-id opaque-draft-id \
  --draft-change-key opaque-change-key

# Send only that reviewed provider version
corr mail send-draft \
  --account work \
  --draft-id opaque-draft-id \
  --draft-change-key opaque-change-key \
  --approve
```

Compose supports new, reply, reply-all, and forward modes; text or HTML; and
bounded repeatable attachments. Review output includes normalized source
version, recipients, subject, body format/size/digest, and attachment
size/digest. It does not print the body.

A draft always uses save-only semantics. Every external send requires exact
commit. Changing any input after preview invalidates the approval. Existing-
draft send is advertised per authenticated account: Outlook Web uses the exact
Exchange ItemId and ChangeKey; IMAP/SMTP requires immutable UID content,
Drafts and Sent mailboxes, and UIDPLUS targeted cleanup. Routes without an
atomic reviewed-version precondition report an explicit degradation.

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
corr calendar folders --account work

corr calendar list \
  --account work \
  --calendar-id opaque-calendar-id \
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
    --online-meeting \
    --body-file -
```

`calendar folders` discovers bounded provider calendars and copyable opaque
IDs. Omit `--calendar-id` to use the provider's primary calendar.

Creation, update, and cancellation always use preview/commit:

```console
corr calendar update \
  --account work \
  --event-id opaque-event-id \
  --change-key opaque-change-key \
  --subject 'Revised review' \
  --start 2026-07-28T09:00:00Z \
  --end 2026-07-28T10:00:00Z \
  --recurrence weekly \
  --recurrence-day Tuesday \
  --recurrence-count 4

corr calendar cancel \
  --account work \
  --event-id opaque-event-id \
  --change-key opaque-change-key
```

Repeat the exact reviewed command with `--approve` to commit. Supported fields
include bounded subject/body, absolute start/end, time zone, location, all-day
state, reminder, closed recurrence creation/replacement/removal, and complete
required/optional attendee lists. Use `--recurrence none` with replacement
start/end boundaries to remove a series rule. Provider meeting-link creation
is accepted only when the authenticated calendar route reports a supported
capability.

`--online-meeting` requests the selected route's observed native provider
(Teams or Google Meet). The transitional `--teams-meeting` spelling requires a
Teams-capable route and fails on Google rather than silently changing meaning.

## Tasks

```console
corr tasks lists --account work
corr tasks list --account work --list-id opaque-list-id
corr tasks list --all-accounts --status needs_action
corr tasks get --account work --list-id opaque-list-id --task-id opaque-task-id
corr tasks search --account work --query synthetic
corr tasks sync --account work --list-id opaque-list-id --json
```

Without `--json`, `tasks sync` prints the next cursor as a complete JSON value
that can be saved verbatim and passed back with `--cursor-file`. Cursors are
opaque, account/provider/list scoped data and never authorization.

Create and update use the strict canonical JSON contract rather than a growing
provider-shaped flag dialect:

```console
corr tasks create --account work --file ./task-create.json
corr tasks create --account work --file ./task-create.json --approve
corr tasks update --account work --file ./task-update.json --approve

corr tasks complete --account work \
  --list-id opaque-list-id \
  --task-id opaque-task-id \
  --task-version opaque-version \
  --approve
```

The file must omit `account`; `--account` is the only routing selector. Unknown
JSON fields, trailing values, oversized documents, unsupported capabilities,
and mismatched replacement flags fail before provider access. Each write prints
the exact typed review. Notes are represented by a bounded preview, byte count,
and digest while the approval binds the complete content.

Complete, reopen, and delete require the exact version returned by a read.
Every task write previews; `--approve` commits only that single-use review.
Task adapters remain unavailable until their provider issues ship, even though
these stable commands and schemas are present. See [tasks.md](tasks.md).

## Read-only import staging

```console
corr import scan ./synthetic-export
corr import scan ./synthetic-export --format auto --approve-read --json
corr import purge --account work --approve
```

The first command performs no filesystem scan; it prints the privacy boundary
and asks you to rerun with consent. `--approve-read` binds read-only access to
that exact resolved path, reads it, and creates an upload-free account-local
staging plan in the same operation. Purge removes only the staging area, never
the source.

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

`notify` invokes `notify-send` on Linux or `osascript` on macOS with a
10-second bound and native argument separation. Windows rejects `notify`
before changing configuration because Corresync does not install the registered
AppUserModelID required for desktop toasts. Windows `queue` and `agent` modes
remain available. Notify events are first committed to the account-local outbox
and stay pending across quiet hours, debounce, rate limits, cancellation, or
adapter failure; later polls drain them without rewinding the provider cursor.

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

Purge clears both queued events and their private deduplication window. The
monitor also attempts pending notification/runner delivery before committing a
new scan, so a saturated pending queue can recover when its configured sink and
rate policy permit.

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

Automatic public issue submission is a separate, default-off choice:

```console
gh auth login --hostname github.com
corr config set feedback.auto_submit true
# disable at any time
corr config set feedback.auto_submit false
```

Enabling verifies the external GitHub CLI and displays the same consent
boundary available under `corr settings`. On an interactive command failure,
Corresync constructs a smaller report containing only validated build/platform
atoms, install method, command and flag names, a content-free fingerprint, and
fixed error classes, then invokes `gh issue create` for the public Corresync
repository. Corresync never reads or stores the GitHub token. Raw errors,
values, paths, account/provider data, credentials, mail, and calendar data are
not representable. MCP, `--json`, configuration management, redirected stderr,
cancellation, and non-interactive commands never submit. Each build/error
fingerprint is attempted once.

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

On Unix, every process derives the same endpoint regardless of
`XDG_RUNTIME_DIR` or `TMPDIR`. v0.8.6 can authentically drain one owner left at
an older runtime location. If an installation already has multiple owners at
old runtime locations, `corr daemon stop` pins each protected same-user
endpoint, sends normal termination to those exact processes, and waits for all
owners to exit. It never guesses between their diverged credentials, scans
unrelated processes, or deletes and copies an IPC token.

## MCP

Read-only local agent-host discovery is available before MCP registration:

```console
corr integrations detect
corr integrations detect --refresh
corr integrations list
corr integrations show codex
corr integrations detect --json
corr integrations plan codex claude-code
corr integrations setup codex claude-code
corr integrations doctor
corr integrations repair codex
corr integrations remove codex
```

The detector never executes an agent or reads a host configuration file. See
[integrations.md](integrations.md) for evidence, cache, context, support, and
privacy semantics, plus exact lifecycle scopes and safety behavior. Lifecycle
mutation commands preview first and require an interactive confirmation or
`--yes`. Their `--json` form emits a preview and never applies it.

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

Eligible interactive CLI starts perform a bounded, 24-hour-cached check and
show a short installation-specific notice. Direct installs may opt in to the
same verified replacement path:

```console
corr config set updates.auto_install true
```

The default update channel is `stable`. A standalone installation can follow
signed prereleases, including automatic installation when separately enabled:

```console
corr config set updates.channel preview
corr update check
corr update
```

Use `corr config set updates.channel stable` to return. Channel changes never
downgrade. Package-manager catalogs are stable-only, so a managed preview check
shows the direct release URL instead of an owner command.

The update becomes active on the next process start. Package-managed binaries
continue to receive only their exact owner command. MCP, configuration
management, daemon, completion, feedback, JSON, pipes, and non-interactive paths
never attempt an update, so an MCP tool call cannot be interrupted by
installation.

## Exit behavior

- `0`: command completed, including a preview that intentionally made no
  external change;
- `1`: runtime, policy, provider, validation, or explicit action failure;
- `2`: CLI usage or parse error.

Errors go to stderr. Stable JSON never receives styling or automatic update
notices. A failed non-feedback command records only the sanitized local
last-error shape for a later explicit `corr feedback --last-error`. When the
signed-in human separately enabled `feedback.auto_submit`, an eligible
interactive failure may also submit the smaller allowlist-only public issue
described above; the original exit code and error do not change.
