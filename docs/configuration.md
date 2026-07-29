# Configuration

Corresync uses strict, secret-free TOML:

- Linux: `$XDG_CONFIG_HOME/corresync/config.toml`, normally
  `~/.config/corresync/config.toml`;
- macOS: `~/Library/Application Support/corresync/config.toml`;
- Windows: `%AppData%\corresync\config.toml`.

Use `CORRESYNC_CONFIG` or global `--config` for an explicit file. The directory
is protected and the file is atomically written with owner-only permissions
where supported.

## Prefer lifecycle commands

```console
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

Discovery is read-only and credential-free. Adding a route never
authenticates. The account receives a generated opaque ID, monitoring remains
off, and login occurs only through `corr auth login --account ALIAS`.

Rename preserves the stable ID and every account-local state tree. Remove
requires approval and deletes Corresync-owned profile, import, cursor, queue,
and unshared Corresync-owned OAuth grant state. External standards credentials
remain in their keyring/helper. Removing the default account requires
`--new-default`.

## Schema v3

Schema v3 separates mail and calendar routes. A minimal Outlook Web
configuration looks like:

```toml
version = 3
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
disable_automatic_checks = false
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
| mail | `google-web` | `mail.google_web` |
| mail | `google-api` | `mail.google_api` |
| mail | `microsoft-graph` | `mail.microsoft_graph` |
| mail | `jmap` | `mail.jmap` |
| mail | `imap-smtp` | `mail.imap_smtp` |
| calendar | `microsoft-owa` | `calendar.outlook_web` |
| calendar | `google-web` | `calendar.google_web` |
| calendar | `google-api` | `calendar.google_api` |
| calendar | `microsoft-graph` | `calendar.microsoft_graph` |
| calendar | `caldav` | `calendar.caldav` |
<!-- markdownlint-enable MD013 -->

The payload must match the provider exactly. Google or Graph mail and calendar
routes may share one identical OAuth route. An IMAP/SMTP mail route can be
paired with a CalDAV calendar route.

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

## OAuth routes

Google API and Microsoft Graph are explicit BYO public-client integrations:

```console
corr account add reader@example.invalid \
  --alias personal \
  --mail-provider google-api \
  --calendar-provider google-api \
  --oauth-client-id synthetic-public-client \
  --oauth-redirect-uri http://127.0.0.1:8765/callback \
  --authorization-key personal-google \
  --approve-oauth
```

The redirect must be an allowed loopback `http://127.0.0.1` URI. Port `0`
selects an available ephemeral port for public-client registrations that permit
native-app loopback ports; otherwise configure an explicitly registered port.
Before a provider page can open, `corr auth login` prints the exact service-
derived scope set. The flow validates state and grants belong to the OS keyring.
There is no client-secret field and no automatic Graph or Google selection. Use
only a client registration you are authorized to operate.

Mail and calendar routes can also use distinct OAuth providers and grants.
Prefix the calendar settings with `calendar-`, for example
`--calendar-oauth-client-id` and `--calendar-authorization-key`. A
calendar-only account uses `--mail-provider none`; `--calendar-provider none`
creates a mail-only account.

## Google Web routing

Managed Google accounts may use a browser-owned, read-only route without a
public OAuth client:

```console
corr account add reader@example.invalid \
  --alias managed \
  --mail-provider google-web \
  --calendar-provider google-web \
  --origin https://mail.google.com \
  --calendar-origin https://calendar.google.com
```

Only the exact provider-owned origins above are accepted. Authentication and
identity confirmation remain inside one visible, stable-ID-isolated browser
profile. The adapter reads a bounded semantic snapshot of Gmail and Google
Calendar; it exposes incomplete pagination as a degradation and implements no
mail or calendar writes. Credential-free discovery may rank this route for a
managed Workspace address, but never opens a browser or requests OAuth/admin
consent.

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

Schema v1 and v2 files are read into schema v3 in memory during migration.
Automatic migration preserves the original rollback copy and never migrates
IPC credentials.
