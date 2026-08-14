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

The Gmail, Google Calendar, and Google Tasks integrations are included in RC
builds but disabled while Corresync's production OAuth application awaits
Google approval. Gmail discovery explains that support is coming soon. Explicit
account addition, existing configurations, migrated grants, and mixed-provider
accounts all stop before a browser opens, the keyring is read, or Google traffic
is sent.

After approval, activation will be a separate reviewed release. The normal
system browser will own authorization; Corresync will never automate the
sign-in page. Gmail will use the pinned Gmail API, Google Calendar/Meet will use
the Calendar API, and Google Tasks will use the Tasks API with an account-scoped
OS-keyring grant. No Google password, app password, cookie, configurable host,
or unattended login is accepted.

Google's generated Desktop client may require its client credential at the
token endpoint. Provide `CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` only in the
local Corresync process environment. It is never a config, CLI, MCP, grant, or
browser-URL field.

Google Workspace administrators can restrict third-party OAuth and API access.
A rejected or unapproved route fails clearly and never
falls through to a password or another provider.

## Google, Microsoft Graph, and Todoist OAuth

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
provider-specific exception; Microsoft Graph and Todoist remain secret-free.

The resulting grant is stored by the operating-system keyring under the
configured local reference. The TOML contains only that reference and the
explicit consent bit. Scopes are selected from the configured mail/calendar/task
services; choosing Graph or Google is never an automatic fallback. Once the
production gate is separately enabled after approval, Google mail requests
only the provider-documented
`https://www.googleapis.com/auth/gmail.modify` scope. The pending RC does not
construct or display that scope through a production command and cannot begin
authorization.

Google Tasks uses a separate provider identity and authorization handle. It
requests `openid`, `email`, and exactly `tasks.readonly` or `tasks`; it never
reuses or expands the Gmail/Calendar grant. The verified OpenID email must
match the configured account. While the shared release gate is closed, task
setup and activation fail before any grant, keyring, browser, or API access.

Microsoft To Do requests `Tasks.Read` for a read-only route or
`Tasks.ReadWrite` for a writable route. The separate task approval is required
even when task settings reuse a mail/calendar public client. A stored grant is
reused only when its recorded scopes cover the complete selected service set;
otherwise login starts fresh interactive authorization. Global, GCC High, and
DoD derive fixed API/authority pairs from `microsoft_cloud`. China task routes
fail before keyring or browser access because the To Do API is unavailable.

Todoist requests `data:read` for a read-only route or
`data:read_write,data:delete` for a writable route. Its authorization and token
endpoints and API base are fixed. Current dynamic-registration and Client ID
Metadata documentation does not establish Corresync's production HTTP
`127.0.0.1` callback as a supported registered redirect, so setup requires an
explicit public client ID and never creates a hosted relay. Todoist rotates
refresh tokens; Corresync serializes refresh by grant across local processes,
reloads after taking the lock, and persists the replacement before release.
The configured loopback port must be the fixed port registered for that client;
the Todoist route does not substitute an ephemeral port.

Use only an application registration and account you are authorized to use.

## JMAP, IMAP/SMTP, and CalDAV

Standards routes resolve a credential only from:

- the OS keyring service `corresync`; or
- one absolute, explicitly configured credential helper.

The helper receives a small JSON `get` request on stdin and is executed
directly without a shell. Output and environment are bounded. Corresync does
not provide a password prompt, store helper output, or use credentials during
discovery.

For an iCloud preset, guided setup explains Apple's 2FA and app-specific
password prerequisite and can open the fixed Apple Account management page
only after an explicit choice. After the secret-free account is added, it can
hand the reviewed key to the OS-owned secure prompt: macOS `security`, Linux
Secret Service `secret-tool`, or Windows `cmdkey`. The OS process—not
`corr`—reads the app-specific password. No credential value appears in a
Corresync argument, environment variable, config file, output, or log. A user
who selected an approved helper uses that helper's own enrollment UI instead.
One iCloud credential handle is shared by Mail and Calendar by default; direct
advanced configuration still permits separate handles.

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

A CalDAV VTODO task route follows the same external-credential rule but has
its own explicit task consent, authenticated discovery, session, and
capability record. A calendar route never activates tasks merely because the
same principal exposes VTODO, and a task route never grants VEVENT access.

## Session status and logout

`corr auth status` is content-free. It reports each configured mail, calendar,
and task route independently as `signed_out`, `authentication_pending`,
`authenticated`, or `reauthentication_required`, with a bounded reason and an
exact local recovery action when inactive. The older top-level state and
`authenticated` boolean remain derived compatibility fields: they report an
active account when at least one configured service is active. Captured time,
normalized capabilities, and degradations describe only active service
leases. Status performs no provider request or surprise login and returns no
address, endpoint, cookie, token, page content, or mailbox item.

A definitive runtime rejection invalidates the owning adapter lease before the
operation returns. Services sharing that lease transition together; a hybrid
service using another provider remains active. Corresync stops the affected
mail monitor, removes affected previews, drains current users, closes owned
resources once, and returns a versioned action naming the real account alias.
An unknown write outcome is never relabeled as authentication failure and is
never automatically replayed.

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

1. every shell, MCP host, and desktop launcher derives the same short
   current-user-specific directory under `/tmp`, independent of
   `XDG_RUNTIME_DIR` and `TMPDIR`;
2. the listener owns an owner-only singleton lock and Unix socket;
3. before any bearer is sent, the client opens the runtime directory without
   following symlinks, validates type/owner/mode, pins directory and socket
   identities, validates the active singleton lock, connects, verifies peer
   UID, and rechecks the pinned identities;
4. symlinks, regular files, FIFOs, wrong ownership, permissive directories,
   socket squatting, and connection-time replacement fail closed.

One authenticated owner left at a v0.8.5-and-earlier runtime location is
drained before the canonical owner starts. Multiple active runtime locations
are reported as a split owner and neither is stopped automatically. The v0.6
legacy migration client uses the same authenticated connection path.
Windows named pipes reject remote clients. Before any bearer is sent, the
client verifies the pipe owner, protected non-null DACL, server process ID, and
server process SID against the current user; the credential file receives the
same owner/DACL validation.

The local bearer is random, owner-only, rotated with daemon ownership, reloaded
for each operation, and sent only after transport authentication succeeds.
Server-side bearer, caller, protocol, config-digest, concurrency, request-size,
and effect-policy checks remain in force.

## Online diagnostics

`corr doctor` is local and does not authenticate. For a browser-backed route it
starts a temporary headless blank target with the same sandbox-relevant launch
options as authentication, then closes it without navigation. A resolved
executable is not reported healthy unless Chromium can actually start.

On Linux, prefer a system-managed Chrome or Chromium whose sandbox and AppArmor
policy cover the executable's installed path. A browser copied into a user cache
may be executable but unable to create its sandbox. Configure the managed path
with `corr config set browser.executable /absolute/path/to/chrome`, stop the
daemon, and rerun `corr doctor --account ALIAS`. Corresync never adds or suggests
`--no-sandbox`; fix the package or host policy instead.

`corr doctor --online` is the explicit opt-in provider compatibility check for
an already authenticated session. Run `corr auth login --account ALIAS` first.
Doctor reports the exact configured OAuth scopes, never starts OAuth or opens a
provider login, and requests only bounded folder, mail, calendar, and task-list
metadata contracts. It must not be part of default tests or CI.

`corr doctor --online --connection-only` is the narrower authenticated-session
status check used by standards onboarding. It reports when TLS and authorization
were last established, shows Mail, Calendar, and Tasks independently, and
requests no folder, message, event, contact, task, task-list, or attachment
metadata. It never silently reauthenticates or claims a new network probe, so
run `corr auth login` first.

For shareable support material, use `corr feedback --last-error`. It records
only generalized error classes and a redacted command shape; it never copies
authentication values or raw doctor errors.
