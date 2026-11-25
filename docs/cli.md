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

Interactive `corr setup` starts with one question: the email address. It
creates the settings file silently when needed, looks up how to connect
without signing in, and lists the ways it found as **Recommended** and **Also
possible**. When it finds nothing, or you pick **Something else**, it offers the
connections you can set up by hand (IMAP/SMTP, JMAP, CalDAV, Outlook Web,
Microsoft Graph, and Google), each with what it will ask for. A connection that
needs something prepared first — your own Microsoft app registration, a Google
Desktop OAuth client, or a saved password — is explained with a link to its
guide before any question, with **Use another way to connect** and **Stop here
and come back later**. When discovery found Outlook Web for the same address,
the Microsoft Graph explanation also offers it, because it needs nothing
prepared. Nothing signs in automatically and Microsoft Graph is never chosen for
you.

After the accounts, setup asks two optional questions — install tab completion
for your shell, and connect an AI agent it found — each answered with one key.
The summary names the settings file once and ends with the next command, such
as `corr auth login --account work`. Answered questions leave a one-line record
(`Account: ana@example.com`), and a cancelled question is erased. Re-running
setup skips finished work. `corr setup ADDRESS` adds one account without
prompting, for scripts and AI agents; it never signs in, and its `--json`
output is unchanged. `config init` only creates an empty settings file.

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
and the configured account list. Selecting an account opens its services,
**Sign in** (`corr auth login`: a browser on this computer, or for Outlook Web
on a computer without a display, a login view you open over SSH), **Sign in
with text only** (`--terminal`), rename, default, and removal actions; Back
returns to the account list. Removal shows the local-state impact, defaults to
cancellation, and asks for a replacement before deleting the current default
when another account will remain. The final account can be removed and leaves
an empty configuration.
The form stops and restarts a running daemon around account changes. Every
choice explains its effect and shows the matching direct command. Set
`CORRESYNC_ACCESSIBLE=true` for line-oriented screen-reader prompts (their help
text is printed too) and type `:cancel` to leave an input step safely. The
remaining commands are stable direct forms for scripts and advanced use.

`account discover` signs in to nothing. It lists the ways it found to connect,
best first, with the evidence (DNS and well-known lookups) and what each needs
to sign in. `account add` needs the connection chosen with flags when it cannot
be picked automatically, generates a stable account ID, and leaves sign-in and
monitoring off. `corr account add --help` groups its flags by connection type.

Examples:

```console
# Outlook Web
corr account add reader@example.invalid \
  --alias work \
  --mail-provider microsoft-owa \
  --origin https://outlook.cloud.microsoft

# Gmail API and Google Calendar with your own Desktop OAuth client
corr auth google-client import ~/Downloads/client_secret.json \
  --key personal-google-client
corr account add reader@example.invalid \
  --alias personal \
  --mail-provider google \
  --calendar-provider google \
  --oauth-client-id YOUR_CLIENT_ID.apps.googleusercontent.com \
  --oauth-redirect-uri http://127.0.0.1:0 \
  --authorization-key personal-google \
  --approve-oauth \
  --oauth-client-secret-key personal-google-client \
  --approve-oauth-client-secret

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

# Microsoft To Do task-only route needs no email discovery
corr account add reader@example.invalid \
  --alias tasks \
  --task-provider microsoft-graph \
  --microsoft-cloud global \
  --task-oauth-client-id synthetic-public-client \
  --task-oauth-redirect-uri http://127.0.0.1:0/callback \
  --task-authorization-key tasks-graph \
  --approve-task-oauth \
  --task-read-only

# Todoist task-only route also uses explicit public-client OAuth
corr account add reader@example.invalid \
  --alias todoist \
  --task-provider todoist \
  --task-oauth-client-id synthetic-public-client \
  --task-oauth-redirect-uri http://127.0.0.1:53684/callback \
  --task-authorization-key tasks-todoist \
  --approve-task-oauth \
  --task-read-only

# TickTick task-only route uses a separate confidential-client secret handle
corr account add \
  --alias ticktick \
  --task-provider ticktick \
  --task-oauth-client-id synthetic-confidential-client \
  --task-oauth-redirect-uri http://127.0.0.1:53685/callback \
  --task-authorization-key tasks-ticktick-grant \
  --task-oauth-secret-key tasks-ticktick-client-secret \
  --approve-task-oauth \
  --approve-task-oauth-secret \
  --task-read-only

# CalDAV VTODO task-only route; no email address or discovery required
corr account add \
  --alias tasks \
  --task-provider caldav \
  --task-caldav-endpoint https://dav.example.invalid/ \
  --task-list-path /dav/tasks/work/ \
  --task-username reader@example.invalid \
  --task-credential-key tasks-caldav \
  --approve-task-credential

# Google Tasks uses a separate task-only OAuth grant and client handle
corr auth google-client import ~/Downloads/tasks_client_secret.json \
  --key google-tasks-client
corr account add reader@example.invalid \
  --alias google-tasks \
  --task-provider google-tasks \
  --task-oauth-client-id synthetic-public-client \
  --task-oauth-redirect-uri http://127.0.0.1:0 \
  --task-authorization-key tasks-google \
  --approve-task-oauth \
  --task-oauth-secret-key google-tasks-client \
  --approve-task-oauth-secret \
  --task-read-only
```

Mail and calendar providers are independent. Calendar-specific OAuth flags
default to their shared `--oauth-*` values; set them explicitly when the two
services use different providers or grants. `--provider` remains a compatibility
alias for `--mail-provider`. No account command accepts a password or token.
Approved removal discloses and purges account-local state plus an unshared
Corresync-owned OAuth grant; it never deletes an external standards credential.

The Google examples require a Desktop client in a Cloud project you control.
Account addition persists only reviewed references and starts no browser,
keyring read, or Google API work. `corr auth login` is the separate explicit
authorization boundary. Follow [Google OAuth setup](google-oauth-setup.md).
The import command refuses to overwrite an existing handle. Use `--replace`
only for an intentional rotation of that same Google client credential, never
for a handle used by another provider or standards credential.
All Google adapters remain synthetic-contract covered and live-unobserved; the
other unavailable task provider IDs remain unavailable.

## Authentication and doctor

```console
corr auth login --account work
corr auth status
corr auth logout --account work
corr auth logout --account work --clear-browser
corr auth logout

corr doctor
corr doctor --online --account work
corr doctor --online --connection-only --account personal
```

`auth login` displays the exact OAuth scope set before any provider page can
open, then invokes the route's browser/keyring/helper authentication. Targeted
logout preserves every other account and the daemon; logout without an account
stops the local daemon, which ends every sign-in. These operations keep saved browser
sign-in, so a later explicit login can reopen the mailbox without a password.
Use `corr auth logout --account work --clear-browser` to also remove only that
account's dedicated Outlook/Teams browser storage. It requires an explicit
account, waits for account operations and browsers to close, and refuses busy
or linked profiles. Other accounts, caches, and OS-keyring entries remain.
OS or provider SSO can still sign you in; this is not remote token revocation.
Authentication observation failures (for example, a service request on a
mismatched exact origin) are reported in terminal and graphical login instead
of remaining pending. A displayed page origin alone does not prove the service
request origin; review the error and configured route before changing it.
An Outlook Web login keeps its dedicated visible window open because that
window owns the account session; account logout or daemon shutdown closes it.
If its private profile still has a provider session, a later explicit login may
finish without another password, MFA, or consent prompt.
`--terminal` is an optional Outlook-Web-only browser relay and requires an
interactive TTY. Google uses normal-browser OAuth and pinned Gmail/Calendar
APIs; it never automates Google sign-in. Its client credential must already be
available through the reviewed external handle. `auth status` is content-free
and prints the mail, calendar, and task state separately. A signed-out or
expired route shows
the exact local `corr auth login --account ALIAS` action; the command does not
run that action automatically.

While another `corr auth login --web`, `--host`, or `--terminal` is waiting for
you, `auth status` shows that sign-in for its account, for example
`Sign-in in progress (forwarded login view), expires in 3 min`, and `--json`
adds `pendingSignIn` with its kind and expiry (see [JSON output](json.md)).
Nothing else about that sign-in is reported. Finish it where it started, or run
`corr auth login --account ALIAS` again to replace it. A sign-in in a browser
window on this computer is not listed.

Before anything opens, every `corr auth login` (a browser on this computer,
`--web`, `--terminal`, and the helper behind `--host`) prints where it will sign
in, on standard error so `--json` output is unchanged:

```text
Signing in to work (ana@example.com)
  Outlook Web (mail, calendar): outlook.office.com
  Gmail / Google (calendar): accounts.google.com, OAuth client
  1234.apps.googleusercontent.com (opens in your browser)
  IMAP / SMTP (mail): imap.example.com:993, smtp.example.com:465 (uses the saved
  password from your keyring)
```

If the account's current connection settings have never been signed in to from
the CLI (a new account, or any change to its routes since), it adds "This is
the first sign-in with these connection settings. Check that the page above is
the one you expect." The sign-in then continues without a question; press
Ctrl-C if the page is not the one you expect. With `--host` the notice comes
from the remote host through SSH; check the hosts it prints, and the page
address the login view shows.

While a sign-in that an MCP client started after your approval is open, `auth
login` for any account and `auth logout` for other accounts stop at once and
name that account
and the time it ends; `corr auth logout --account ALIAS` for that account
cancels it and closes its browser.

A failed `auth login` names the account, keeps the reason the local daemon
gave, and suggests a next command: `corr doctor --account ALIAS` when the
browser or route could not start, or the same login again after a timeout or
an expired login view.

When a read reports `authentication_required`, `authentication_pending`, or
`reauthentication_required`, finish the human-owned login, verify the same
account/service with `corr auth status`, and retry that read once. Do not switch
accounts or providers silently. Never replay a send, move, delete, event/task
change, or another consequential write after login; create a fresh preview and
obtain fresh approval.

`doctor` validates local config, starts and closes a blank sandboxed Chromium
target when the selected route needs a browser, checks IPC, daemon state, and
update policy. It performs no navigation or authentication. `--online` validates
only an already authenticated session; it never starts login or OAuth. Run
`auth login` first. The report includes the configured OAuth scope set and is
never run by default tests.
For standards routes, `--connection-only` reports when the active session last
established TLS and authorization, with Mail/Calendar/Tasks capability state
shown separately. The status check makes no fresh authentication attempt and
requests no folder, message, event, contact, task, task-list, or attachment
metadata.

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
that visible. Lists show sender, subject, date, and flags (`U` unread, `A` has
attachments), not message text. When `preview_sensitive_reads` is on, `mail
body` and `mail attachment` first print a preview that ends with a `Next:`
line: the same command with `--approve` added.

Message IDs are in the last column of `mail list` and `mail search`. Change
keys, which `move`, `mark`, `delete`, and replies also need, are in their
`--json` output. An empty result says which folder and account were searched
and, for a search, suggests the same search across all accounts.

When an account needs to sign in again, the error names the account and the
service and ends with a `Try:` line such as `Try: corr auth login --account
work`. An unknown `--account` alias ends with `Try: corr account list`.

Outlook Web accepts mail list pages of up to 25 items; request later pages with
`--offset`. Its mail time-zone value is an Exchange/Windows identifier such as
`GMT Standard Time`, not an IANA identifier such as `Europe/London`. Omit
`--time-zone` to use the UTC default. Unsupported values and provider-side
search failures return an actionable error instead of an opaque HTTP 500.

`--output` must name a new file; an existing file is never overwritten.

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

These commands only read. Every row is labelled with its account and
provider, and the results are sorted the same way on every run. An account
whose results are limited is listed under the table as `Limited results from
…`, and one that could not be read as `Missing results from …`, followed by the
sign-in command when that is the cause; JSON reports the same in `accounts` and
`failures`. Mail search and task listing
read each available source before applying the global page, up to 5,000 matches
and 2 MiB per account. An account that exceeds either bound contributes a
failure and no partial prefix. Narrow the mail query or task status filter, or
read that account separately. No cross-account write exists.

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

Save-only draft composition supports new, reply, reply-all, and forward modes;
text or HTML; and repeatable attachments. A preview starts with what has not
happened yet (for example `Preview: this message has not been sent.`), then
lists `Will send: yes` or `no`, the mode, To, Cc, Bcc, subject, body format,
body size and SHA-256, the original message for a reply or forward, and each
attachment with its size and SHA-256, followed by the start of the body. Its
last line is `Next:` with the same command plus `--approve`. With `--approve`,
the same preview is printed on standard error before the change is made.

Choose `--mode` from the intended audience:

- `new` and `forward` address only the `--to`, `--cc`, and `--bcc` you supply.
- `reply` addresses only the source sender (or its Reply-To).
- `reply-all` addresses the sender plus the source's other visible To and Cc
  participants, excluding this account.

`reply` and `reply-all` derive recipients from the source message and reject
explicit recipients; the error ends with a `Try:` line that saves the same
draft with `--mode reply-all` and without `--to`, `--cc`, or `--bcc`. A saved
draft reports the To, Cc, and subject as the provider saved them, and ends
with the `corr mail send-draft` command for that draft version. When a `reply`
leaves out source To/Cc recipients, the output says how many and that
`reply-all` keeps them. A route that cannot report the saved addressing says
it is unknown rather than showing an empty list. Check the recipients before
sending the draft. See
[the JSON contract](json.md#mail) for the `addressing` field.

```console
# Reply to everyone on a synthetic thread, then check the resolved audience
printf 'Synthetic reply.\n' | \
  corr mail draft \
    --account work \
    --mode reply-all \
    --reference-message-id opaque-source-id \
    --reference-change-key opaque-source-change-key \
    --body-file -
```

Reply and reply-all must be saved as drafts before sending, so the send preview
can show the resolved recipients and enforce `policy.max_recipients`. A direct
`mail send --mode reply` or `reply-all` fails with a `Try:` line naming the
matching `corr mail draft` command. Create
one with `corr mail draft --mode reply-all` (or `reply`) and the exact
reference message ID/version, then use the returned draft ID/version with
`corr mail send-draft`. Review every recipient before approving. If the account
does not support exact-version saved-draft sending, review and send the saved
draft in the provider's own interface. Direct new-message and forward sends
continue to accept explicit recipients.

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

Move and mark show a preview first when `preview_reversible_writes` is on.
Permanent delete always shows a preview and needs `--approve`; its preview
ends with the command to run. Without `--message-id` or `--change-key`, the
error ends with `Try: corr mail list --json`. Provider degradations state
when an atomic version precondition is unavailable. Corresync never treats
that limitation as permission to retry an ambiguous request.

## Calendar

Start/end values identify absolute RFC3339 instants. Calendar boundaries use
the selected installed IANA or known Windows timezone, defaulting to UTC.
All-day start/end must be midnight in that zone, including updates that retain
an existing all-day setting; explicitly disable all-day for timed events.
Outlook and Graph reject repeated local clock times that their write formats
cannot distinguish. Specify UTC to preserve either instant in such an interval.

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

Each preview ends with a `Next:` line: the same command with `--approve`
added. Run it to carry out the change; the preview is printed again on
standard error first. Supported fields
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
and mismatched replacement flags fail before provider access. Each write
preview lists the action, account, provider, list, task, version, and any
limitation, then the full change as JSON, and ends with the `--approve`
command. Notes are represented by a bounded preview, byte count,
and digest while the approval binds the complete content.

Complete, reopen, and delete require the exact version returned by a read.
Every task write previews; `--approve` commits only that single-use review.
Microsoft To Do, Todoist, TickTick, and CalDAV VTODO support lists, list/get,
sync, and the typed write commands; Todoist applies its observed account-plan
constraints. Search is unavailable for Microsoft To Do and Todoist. TickTick
uses the documented bounded search endpoint, while CalDAV performs a bounded
local search over its selected VTODO collections. Other task adapters remain
unavailable until their provider issues ship. See [tasks.md](tasks.md).

## Private saved queries

Saved queries keep a bounded private definition for one exact account. They do
not save message or event results and do not run in the background.

```console
# Preview, then save the mail search.
corr queries save mail priority 'is:unread importance:high' --account work
corr queries save mail priority 'is:unread importance:high' \
  --account work --approve

# A calendar window is relative to each run, not frozen at save time.
corr queries save calendar next-week --account work \
  --start-offset 0s --window 168h --time-zone Europe/London --approve

corr queries list --account work
corr queries show priority --account work
corr queries run priority --account work --json
```

Every run calls the configured live mail or calendar route and reports
`source: live_provider`, `fetchedAt`, `cached: false`, and `stale: false` in
JSON. Provider unavailability is an error; an old page is never substituted.
Mail syntax remains provider-native. Calendar definitions use one typed
calendar plus a relative start and bounded window because Corresync does not
invent a calendar text-search contract.

Save and replacement show the complete definition before `--approve`. Delete
is revision-bound, and `queries purge` binds approval to the exact catalog
bytes so it can safely recover a bounded malformed catalog without deleting a
newer replacement. Account removal deletes saved definitions with that
account's state. Neither CLI nor MCP can turn a saved query into monitoring,
notifications, runner execution, authentication, or egress.

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

Report generation performs no network request. `--last-error` includes two
independent replace-in-place, bounded, owner-only records when available:

- the latest ordinary command failure, reduced to generalized error classes,
  a local hash ID, command/subcommand placeholders, and flag names;
- the latest process panic, reduced to public build metadata, UTC time, fixed
  CLI/daemon/MCP and boundary codes, a local hash ID, and at most 32 Corresync
  source symbols with line numbers.

The crash record contains no panic value, raw error, argument, source path,
account or provider identifier, request data, credential, approval value, or
mail/calendar/task/message content. Recording does not make the panic
recoverable: the top-level command exits nonzero, owned background goroutines
re-raise, and a daemon request panic stops every listener before the standard
HTTP server can recover that connection. The separate automatic public issue
path never includes crash records.

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

The local daemon is the background process that keeps your sign-ins and talks
to providers. Normal provider commands start the config-scoped daemon on
demand. It owns sessions and exposes authenticated local IPC only—never TCP. A
config digest change requires an explicit stop. A compatible old binary can be
drained and replaced without retrying an application operation.

`daemon status` reports an unavailable daemon without printing private state
paths or credential-file details. The human message says whether the daemon is
not running, is a different version of corr, refused the command, or did not
answer, followed by the reason code and a suggested command. Its JSON form uses
stable `ready` or `unavailable` states and a bounded reason; an authorization
mismatch remains distinct from an owner that is not running. `daemon stop` on
a daemon that is not running still exits with status 1 and says so.

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

`corr update check` asks the release endpoint each time it runs. When the
lookup fails it says why where it can tell (no network, GitHub's rate limit,
a timeout, or another `corr` checking at the same moment); `corr doctor` shows
the same reason for its cached check. Eligible
interactive CLI starts perform a bounded, 24-hour-cached check instead, which
also remembers a failed lookup, and show a short installation-specific notice.
New standalone configurations enable the verified replacement path by default;
existing preferences are preserved.
Enable it explicitly for an existing configuration:

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

After verified installation, the existing config-scoped local daemon restarts
and the requested interactive command runs with the updated executable. Owners
from 0.9.2 also detect external executable replacement and retire after admitted
requests finish. Disable installation with `corr config set
updates.auto_install false`. Package-managed binaries continue to receive only
their exact owner command. MCP, configuration
management, daemon, completion, feedback, JSON, pipes, and non-interactive paths
never trigger installation. Retirement drains admitted operations before
closing browser state.

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

### Attachment send limits (0.9.3)

Outlook Web draft/send accepts up to 20 MiB per file and 20 MiB combined,
with at most 10 files. Use the existing repeatable `--attach` option.
CLI and MCP share the same reviewed write contract; attachment content remains
bound to the exact approval. Other mail routes retain 2 MiB per file and
3 MiB combined and reject larger inputs before creating a remote draft.
Provider/tenant message-size limits still apply, including encoding overhead.
Attachment download limits are unchanged.
