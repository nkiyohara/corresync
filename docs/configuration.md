# Configuration

Corresync uses strict, secret-free TOML:

- Linux: `$XDG_CONFIG_HOME/corresync/config.toml`, normally
  `~/.config/corresync/config.toml`;
- macOS: `~/Library/Application Support/corresync/config.toml`;
- Windows: `%AppData%\corresync\config.toml`.

Use `CORRESYNC_CONFIG` or global `--config` for an explicit file. The directory
is protected and the file is atomically written with owner-only permissions
where supported.

Supported schema versions, fail-safe defaults, migration, downgrade refusal,
and support-window rules are defined by the
[public and local versioning policy](adr/0020-public-and-local-versioning.md).

## Prefer lifecycle commands

```console
corr settings
corr setup you@example.com --alias personal
corr config init
corr config validate
corr config show
corr account discover reader@example.invalid
corr account add --help
corr account list
corr account show work
corr account rename work primary
corr account remove old --approve
```

Use `corr settings` when working interactively. It provides an arrow-key form
organized around accounts, updates, safety, and browser login. It can add an
account from the nested Accounts category, whose account list leads to either
login mode, rename, default, and removal actions for one account. Removing an
account requires an explicit local-state review; removing the default requires
selecting its replacement first, and the only configured account cannot be
removed. Each row shows its current value, plain-language effect, and
equivalent direct command. Set `CORRESYNC_ACCESSIBLE=true` for line-oriented
screen-reader prompts. Direct `account` and `config` commands remain available
for scripts and advanced configuration.

`setup` is the provider-neutral first-run path. It creates an empty,
secret-free configuration when needed, performs credential-free discovery,
and adds an automatically selectable first-party route. It does not
authenticate. `config init` creates only the empty configuration for users who
want to inspect candidates and select every route manually.

Discovery is read-only and credential-free. Adding a route never
authenticates. The account receives a generated opaque ID, monitoring remains
off, and login occurs only through `corr auth login --account ALIAS`.

Rename preserves the stable ID and every account-local state tree. Remove
requires approval and deletes Corresync-owned profile, import, cursor, queue,
and unshared Corresync-owned OAuth grant state. External standards credentials
remain in their keyring/helper. Removing the default account requires
`--new-default`.

## Schema v5

Schema v5 adds an explicit signed-release channel to the provider-neutral
schema introduced in v4. Existing v4 files migrate to `stable` without
changing check or automatic-install consent. A freshly
initialized provider-neutral configuration contains no account and has an
empty `default_account`. The first account added becomes the default:

```toml
version = 5
default_account = ""

[policy]
mode = "guarded"
preview_sensitive_reads = false
preview_reversible_writes = false
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m0s"

[updates]
channel = "stable"
disable_automatic_checks = false
auto_install = false
```

A configured Outlook Web account then looks like:

```toml
version = 5
default_account = "work"

[accounts.work]
id = "acc_0123456789abcdef0123456789abcdef"
address = "reader@example.invalid"

[accounts.work.mail]
provider = "microsoft-owa"

[accounts.work.mail.outlook_web]
origin = "https://outlook.cloud.microsoft"

[accounts.work.calendar]
provider = "microsoft-owa"

[accounts.work.calendar.outlook_web]
origin = "https://outlook.cloud.microsoft"

[policy]
mode = "guarded"
preview_sensitive_reads = false
preview_reversible_writes = false
max_recipients = 20
max_attendees = 50

[browser]
login_timeout = "5m0s"

[updates]
channel = "stable"
disable_automatic_checks = false
auto_install = false
```

Unknown fields, mismatched tagged-union payloads, unsupported providers,
credential-bearing URLs, non-TLS remote endpoints, duplicate account IDs,
invalid aliases, and out-of-range policy values are rejected.

Do not copy example IDs into multiple accounts. Let `corr account add` generate
them.

## Per-service routes

Each account may have mail, calendar, or both. Supported route payloads are:

<!-- markdownlint-disable MD013 -->
| Service | Provider | Nested table |
| --- | --- | --- |
| mail | `microsoft-owa` | `mail.outlook_web` |
| mail | `google` | `mail.google` |
| mail | `microsoft-graph` | `mail.microsoft_graph` |
| mail | `jmap` | `mail.jmap` |
| mail | `imap-smtp` | `mail.imap_smtp` |
| calendar | `microsoft-owa` | `calendar.outlook_web` |
| calendar | `google` | `calendar.google` |
| calendar | `microsoft-graph` | `calendar.microsoft_graph` |
| calendar | `caldav` | `calendar.caldav` |
<!-- markdownlint-enable MD013 -->

The payload must match the provider exactly. New Google mail-and-calendar
setup shares one OAuth public client and grant: mail has fixed Gmail IMAP/SMTP
endpoints while calendar pins the Google Calendar API base. A migrated
schema-v3 account with deliberately distinct Google clients remains separate.
Graph mail and calendar may share one identical API route. An independent
IMAP/SMTP mail route can be paired with a CalDAV calendar route.

Use `corr account add` for these combinations; it validates endpoint
discovery, explicit provider selection, required consent bits, and route
pairing before saving.

## External credentials

`config.toml` can hold only a credential reference:

```toml
[accounts.work.mail.imap_smtp.credential]
backend = "os-keyring"
key = "work-mail"
consent = true
```

The OS-keyring service name is `corresync`. Store the actual password or token
with your platform's keyring facility under the selected key. Corresync reads
it only while constructing the explicitly authenticated adapter, bounds it to
64 KiB, keeps it in memory, and overwrites its owned byte buffer on close.

An advanced installation can name one helper:

```toml
[credentials]
helper = ["/absolute/path/to/credential-helper", "get"]

[accounts.work.calendar.caldav.credential]
backend = "helper"
key = "work-calendar"
consent = true
```

An account-add review shows the exact backend/key handles before approval.
Corresync rejects a new account that attempts to reuse a handle already owned
by another configured account; mail and calendar routes belonging to the same
account may intentionally share one handle. Existing external credential
records remain owned by their keyring or helper and are never copied into this
file.

The executable is invoked directly without a shell. It receives one bounded
JSON line on stdin:

```json
{"version":1,"operation":"get","key":"work-calendar"}
```

It must return only the secret and an optional final newline on stdout.
Stderr is discarded, output is bounded, and the child environment is reduced
to a small platform allowlist. Helper arguments and reference keys are private
configuration—not suitable for support reports.

Corresync never stores a password, cookie, OAuth access/refresh token,
authorization header, or browser canary in TOML.

## Google OAuth route

Google is an explicit BYO desktop public-client integration:

```console
corr account add reader@example.invalid \
  --alias personal \
  --mail-provider google \
  --calendar-provider google \
  --oauth-client-id synthetic-public-client \
  --oauth-redirect-uri http://127.0.0.1:0 \
  --authorization-key personal-google \
  --approve-oauth
corr auth login --account personal
```

The redirect must be an allowed loopback `http://127.0.0.1` URI. Port `0`
selects an available ephemeral port for public-client registrations that permit
native-app loopback ports; otherwise configure an explicitly registered port.
Before a provider page can open, `corr auth login` prints the exact service-
derived scope set. The flow validates state and grants belong to the OS keyring.
There is no client-secret field and no automatic Google selection. Google's
generated Desktop client may require its client credential during token
exchange. Supply it only to the local Corresync process:

```console
CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET='generated-desktop-client-value' \
  corr auth login --account personal
```

Do not commit that value, put it in TOML or a CLI flag, or expose it through
MCP, logs, support output, or screen recordings. Corresync bounds the inherited
value, sends it only to Google's fixed TLS token endpoint, and never stores it
in the authorization URL or OS-keyring grant. Use only a client registration
you are authorized to operate.

The normal system browser owns Google sign-in. Mail then authenticates with a
fresh access token over fixed `imap.gmail.com:993` implicit TLS and
`smtp.gmail.com:587` STARTTLS endpoints. Calendar uses the pinned Google
Calendar API. Passwords, app passwords, cookies, custom Gmail hosts, and Gmail
REST are not accepted by the Google route.

Microsoft Graph and hybrid accounts can use distinct OAuth providers and
grants. Prefix calendar settings with `calendar-`, for example
`--calendar-oauth-client-id` and `--calendar-authorization-key`. A
calendar-only account uses `--mail-provider none`; `--calendar-provider none`
creates a mail-only account.

## Outlook Web routing

`origin` is an exact authorization boundary, not a wildcard. Configure the
final HTTPS Outlook host used after normal sign-in, with no path. Do not use an
identity-provider URL or a vanity redirect.

An optional bare `mailbox` address routes a shared/delegated mailbox that the
same signed-in user is already allowed to access. It grants no permission and
does not manage delegates or folders.

## Monitoring

No monitor table means `off`. Enable one account through the CLI so consent
advances only one step and all bounds are validated:

```console
corr monitor enable --mode notify \
  --notification-field sender \
  --approve
```

Modes are `off`, `notify`, `queue`, and `agent`. Configuration may include
metadata filters, poll interval, debounce, retention, hourly release limit,
quiet hours, notification fields, or an absolute runner executable. Agent
mode's remote egress declaration requires `approve_remote = true`, which the
CLI writes only after `--approve-remote-egress`.

Desktop `notify` is available through `notify-send` on Linux and `osascript` on
macOS. Windows setup fails before configuration changes because Corresync does
not install a registered AppUserModelID; use `queue` or an explicitly
configured local `agent` runner there.

Old configs, imports, and account additions always default to off.

## Policy and updates

There is no unguarded-write mode. The `guarded` policy controls optional
previews for sensitive reads and reversible writes; destructive writes and
external sends retain mandatory review.

`updates.disable_automatic_checks = true` disables opportunistic public release
checks without disabling `corr update` or `corr update check`.
`CORRESYNC_NO_UPDATE_CHECK=1` provides a process override.

Interactive CLI starts check the cached release status for the configured
channel and show one short, installation-specific action when an update is
available. `stable` is the default. A direct installation can follow fully
verified prereleases with:

```console
corr config set updates.channel preview
```

Return to the stable channel with `corr config set updates.channel stable`.
Switching channels never causes a downgrade. Preview releases are not placed
in Homebrew, Scoop, WinGet, APT, DNF, or APK catalogs; a package-managed binary
can report a preview but will not mix ownership by installing it.

Opt in to verified automatic installation for a standalone/direct binary with:

```console
corr config set updates.auto_install true
```

or set `updates.auto_install = true` in TOML. Corresync never runs Homebrew,
Scoop, WinGet, or a system package manager: managed installations still show
their exact owner command. Automatic installation is default-off and never
runs on MCP, daemon, completion, feedback, JSON, piped, or non-interactive
paths. Configuration commands are also excluded so consent can always be
revoked before another update attempt. `disable_automatic_checks` and
`auto_install` cannot both be true.

## Config lifecycle

`corr config edit` uses `VISUAL`, `EDITOR`, or the platform default, then saves
only if strict validation succeeds. `config set` supports the documented
simple scalar keys and writes normalized TOML; use account lifecycle commands
for tagged provider routes.

The daemon publishes a digest of the exact config it loaded. CLI and MCP verify
it before use. A binary/protocol change can replace an authenticated old
daemon; a config digest change fails closed. Run:

```console
corr daemon stop
```

then retry so the new owner starts with the new policy.

Older schema files are migrated without changing stable account identity or
moving credentials into configuration. See the
[migration guide](migration-v0.7.md) for version-specific details.
