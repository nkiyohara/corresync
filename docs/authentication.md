# Authentication

Authentication is interactive, account-scoped, and owned by the selected
provider surface. Account discovery and addition never authenticate.

```console
corr auth login --account work
corr auth status
corr auth logout
```

The daemon is the only authenticated session owner. CLI and MCP call it through
private local IPC and never receive provider credentials.

## Outlook Web

Outlook Web login opens a dedicated visible browser profile for the account.
SSO, MFA, Conditional Access, consent notices, and identity-provider redirects
remain in that browser. Corresync:

- never asks for or reads a password or MFA value;
- never performs TLS interception;
- accepts session material only for the exact configured final Outlook origin;
- keeps captured authorization in daemon memory;
- leaves browser-managed profile state inside the account's private local
  profile directory.

On Linux, visible login checks for an available X11 or Wayland session before
starting Chromium. If both `DISPLAY` and `WAYLAND_DISPLAY` are unset, Corresync
stops before launching the browser and prints the exact account-specific
`--terminal` command to use over SSH.

The browser profile is isolated by stable account ID, not mutable alias. Shared
or delegated mailbox routing reuses the signed-in user session and grants no
new permission.

### Terminal relay

```console
corr auth login --account work --terminal
```

The optional terminal relay is Outlook-Web-only. It starts a dedicated
headless browser and projects a bounded text view and numbered controls over
authenticated, caller-bound IPC. It accepts one interactive keystroke at a time
from a TTY, masks sensitive fields, and never returns complete form values.
After an activation or Enter submission, it waits briefly for authentication
or a changed page before rendering again. If the bounded view remains the same,
the CLI says so and offers `r` to refresh rather than appearing to ignore the
selection.

Piped input is rejected. CAPTCHA, passkeys, security keys, client
certificates, native dialogs, and graphical custom login may require the
visible browser. Do not use the relay to bypass organization policy.

## Google

Choose `google` explicitly with a desktop public OAuth client you are
authorized to use. Corresync opens the normal system browser for Google
authorization; it never automates the sign-in page. Gmail then uses only
XOAUTH2 over `imap.gmail.com:993` and `smtp.gmail.com:587`. Google Calendar and
Google Meet use the Calendar API with the same grant. No Gmail password, app
password, cookie, or Gmail REST transport is accepted.

Google's generated Desktop client may require its client credential at the
token endpoint. Provide `CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` only in the
local Corresync process environment. It is never a config, CLI, MCP, grant, or
browser-URL field.

Google Workspace administrators can restrict third-party OAuth, IMAP, and
Calendar API access. A rejected or unapproved route fails clearly and never
falls through to a password or another provider.

## Google and Microsoft Graph OAuth

These routes require an explicitly configured public OAuth client and a
registered loopback redirect:

```text
http://127.0.0.1:<registered-or-ephemeral-port>/<registered-path>
```

Login first prints the exact service-derived scopes. It then opens the provider
authorization page in a browser when a matching valid grant is absent, binds
the redirect to an unpredictable state value, and accepts only the configured
loopback path and actual bound port. Configured port `0` requests an available
ephemeral port. The same in-product notice links the public
[Privacy Policy](https://corresync.org/privacy.html) and
[Terms of Use](https://corresync.org/terms.html) before any
provider page can open. Corresync does not support a generic confidential
client, device-code unattended flow, password grant, or broad tenant
credential. The bounded Google Desktop client credential above is the sole
provider-specific exception; Microsoft Graph remains secret-free.

The resulting grant is stored by the operating-system keyring under the
configured local reference. The TOML contains only that reference and the
explicit consent bit. Scopes are selected from the configured mail/calendar
services; choosing Graph or Google is never an automatic fallback. Google mail
requests the provider-documented `https://mail.google.com/` scope required by
XOAUTH2 and fetches a fresh short-lived access token immediately before each
encrypted IMAP or SMTP authentication.

Use only an application registration and account you are authorized to use.

## JMAP, IMAP/SMTP, and CalDAV

Standards routes resolve a credential only from:

- the OS keyring service `corresync`; or
- one absolute, explicitly configured credential helper.

The helper receives a small JSON `get` request on stdin and is executed
directly without a shell. Output and environment are bounded. Corresync does
not provide a password prompt, store helper output, or use credentials during
discovery.

All remote endpoints require encrypted transport. IMAP/SMTP support implicit
TLS or STARTTLS according to the explicit route. IMAP response literals are
bounded individually and in aggregate from the first greeting, including after
STARTTLS. A forward-only capture gives each complete response to the same pinned
go-imap parser exactly once before releasing its exact bytes to the client.
Parser-recognized literal payload reads are bounded separately from control
syntax, parser CPU work has an internal deadline, status-text `{N}` cannot
desynchronize framing, and LF-only control lines are rejected. Reply
inheritance drops malformed external
Message-ID/References values and forwarding does not depend on them. CalDAV
and JMAP endpoints must be HTTPS; a JMAP session may advertise
API/upload/download URLs only on that exact HTTPS origin. Certificate
verification is never disabled.

## Session status and logout

`corr auth status` is content-free. It reports configured alias, provider,
authenticated/pending/signed-out state, captured time, normalized
capabilities, and explicit degradations. It returns no address, endpoint,
cookie, token, page content, or mailbox item.

`corr auth logout --account work` closes only that account's adapters, browser,
pending login, previews, and monitor after its in-flight operations finish.
Other accounts and the config-scoped daemon remain active. Repeating the command
is safe. Without `--account`, `corr auth logout` closes every account session,
rotates/removes IPC credentials, and exits the owner.

Neither form deletes provider keyring entries or user-owned credential-helper
data. Approved account removal deletes an unshared OAuth grant only when
Corresync owns that grant; shared grants and external standards credentials are
retained.

## Local IPC authentication

The daemon exposes no TCP port. MCP uses stdio. On Unix:

1. a suitable private `XDG_RUNTIME_DIR` is preferred;
2. otherwise a current-user-specific private temporary directory is used;
3. the listener owns an owner-only singleton lock and Unix socket;
4. before any bearer is sent, the client opens the runtime directory without
   following symlinks, validates type/owner/mode, pins directory and socket
   identities, validates the active singleton lock, connects, verifies peer
   UID, and rechecks the pinned identities;
5. symlinks, regular files, FIFOs, wrong ownership, permissive directories,
   socket squatting, and connection-time replacement fail closed.

The legacy migration client uses the same authenticated connection path.
Windows named pipes reject remote clients. Before any bearer is sent, the
client verifies the pipe owner, protected non-null DACL, server process ID, and
server process SID against the current user; the credential file receives the
same owner/DACL validation.

The local bearer is random, owner-only, rotated with daemon ownership, reloaded
for each operation, and sent only after transport authentication succeeds.
Server-side bearer, caller, protocol, config-digest, concurrency, request-size,
and effect-policy checks remain in force.

## Online diagnostics

`corr doctor` is local and does not authenticate. `corr doctor --online` is the
explicit opt-in provider compatibility check for an already authenticated
session. Run `corr auth login --account ALIAS` first. Doctor reports the exact
configured OAuth scopes, never starts OAuth or opens a provider login, and
requests only bounded folder, mail, and calendar metadata contracts. It must
not be part of default tests or CI.

For shareable support material, use `corr feedback --last-error`. It records
only generalized error classes and a redacted command shape; it never copies
authentication values or raw doctor errors.
