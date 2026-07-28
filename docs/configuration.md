# Configuration

Corresync uses strict TOML in the platform user configuration directory:

- Linux: `$XDG_CONFIG_HOME/corresync/config.toml`, normally
  `~/.config/corresync/config.toml`;
- macOS: `~/Library/Application Support/corresync/config.toml`;
- Windows: `%AppData%\corresync\config.toml`.

The application creates the project directory with owner-only permissions and
atomically replaces the file with mode `0600` where the operating system
supports Unix permissions.

```toml
version = 2
default_account = "work"

[accounts.work]
id = "acc_0123456789abcdef0123456789abcdef"
provider = "microsoft-owa"
origin = "https://outlook.cloud.microsoft"

# Optional second alias for a mailbox the same signed-in user can already
# access in Outlook Web. This is not a credential or permission grant.
[accounts.shared]
id = "acc_fedcba9876543210fedcba9876543210"
provider = "microsoft-owa"
origin = "https://outlook.cloud.microsoft"
mailbox = "shared@example.com"

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

Unknown fields, non-HTTPS origins, URL credentials, unsupported policy modes,
and out-of-range limits are errors. There is deliberately no unguarded-write
mode.

Use `corresync config show` for a concise validated summary or `--json` for the
full secret-free model. The following keys are available to `config get` and
`config set`:

- `default_account`;
- `accounts.<alias>.id`, `accounts.<alias>.provider`,
  `accounts.<alias>.address`, `accounts.<alias>.origin`, and
  `accounts.<alias>.mailbox`;
- `policy.mode`, `policy.preview_sensitive_reads`,
  `policy.preview_reversible_writes`, `policy.max_recipients`, and
  `policy.max_attendees`;
- `browser.executable` and `browser.login_timeout`;
- `updates.disable_automatic_checks`.

`version` and opaque account `id` values can be read but not changed through
`config set`. IDs are generated once and remain stable when a human-facing
alias or address changes. Values are parsed as their declared
boolean, integer, duration, or string type and the complete configuration is
validated before an atomic save. `config set` writes normalized TOML; use
`config edit` when comments or manual ordering should be retained.

`origin` is an exact authorization boundary, not a discovery hint or wildcard.
If a normal browser ends on a different Outlook host after sign-in, configure
that final HTTPS origin with no path. Do not add the identity-provider origin,
tenant vanity aliases that merely redirect elsewhere, or multiple origins in an
attempt to make capture succeed. Sovereign, hybrid, and on-premises deployments
must use the actual OWA service origin observed by an authorized user.

The currently shipped provider is `microsoft-owa`; other provider IDs are
reserved until their adapters and capability contracts ship. The configuration
schema cannot represent a password, OAuth token, cookie,
canary, or refresh token. Browser session material belongs to the dedicated
browser profile and the in-memory session owner, never this file.

`disable_automatic_checks = true` disables opportunistic stable-release checks
without disabling explicit `corresync update` or `corresync update check`
commands. Set `CORRESYNC_NO_UPDATE_CHECK=1` for a process-level override.
Checks read only the public latest-release metadata, are cached for 24 hours,
and never run through MCP, completion, or JSON notification output.

An optional account `mailbox` must be one bare SMTP address. It enables
explicit shared/delegated mailbox routing with OWA's anchor and explicit-logon
headers while retaining the configured origin and interactive browser session.
It does not grant access, add a delegate, or change folder permissions; Outlook
must already authorize the signed-in user. Keep separate aliases for the user's
own mailbox and each explicitly routed mailbox, and select one with `--account`.

The daemon publishes a SHA-256 digest of the exact secret-free config it loaded.
CLI and MCP compare it before every new connection. If only the executable or
private protocol version changed, the next command drains the authenticated old
owner and starts the current binary automatically. If the config digest
changed, the client fails closed instead; run `corresync daemon stop` and retry
to apply the edit and start a fresh owner with the new policy.
