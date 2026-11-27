# ADR 0039: User-approved sign-in from MCP

- Status: Accepted
- Date: 2026-11-25
- Related: ADR 0002, ADR 0003, ADR 0012, ADR 0020, ADR 0036, ADR 0038, issue #45

## Amendment for issue #234 (2026-11-27)

For first sign-in and after a connection change, a daemon-owned desktop
confirmation can now verify the destination. The original CLI-only decision
below remains the fallback on hosts without a supported dialog. The existing
MCP form is still required. Its one-use ticket binds the account ID, alias and
route digest. After acceptance the daemon shows the reviewed account, address
and service destinations in a separate plain-text OS dialog, with cancel as
the default. A positive local answer gates browser and credential access.
A client fabricating an MCP acceptance can show this dialog but cannot answer
it through MCP. The timeout and logout cancel the dialog as well as the login.
Only completed authentication records the reviewed route digest. Failures use
the existing cooldown. Supported helpers are Zenity or X11 xmessage on Linux,
osascript on macOS and Windows PowerShell's Windows Forms dialog on Windows.

## Context

Until now an MCP client could never start a sign-in. When a session was
missing, every tool returned the content-free authentication action with
`corr auth login --account ALIAS`, and the person had to switch to a terminal
on the Corresync host. Issue #45 asked for capability-aware recovery: where the
connected client supports MCP elicitation, the person should be able to approve
the same sign-in from the client they are already using.

Elicitation lets the server, not the model, ask the person a question through
the client's own interface. Form mode returns `accept`, `decline`, or `cancel`.
From protocol revision 2026-07-28 the question travels as an `input_required`
tool result with an opaque `requestState`; earlier revisions use a server
`elicitation/create` request during the tool call. The Go SDK in use (v1.8.0)
supports both, so no protocol extension is needed.

## Decision

MCP can request a sign-in; it starts only after the user approves it in the MCP
client.

The exact boundary:

- One tool, `account_sign_in`, takes exactly one argument: a configured account
  alias. It is the only tool that can cause an elicitation. Read tools still
  never start, open, or focus a sign-in; their authentication action now also
  names the tool in the additive `nextAction.mcpTool` field.
- The tool is available only for an account whose current connection settings
  have completed at least one sign-in that a person started from the CLI
  (`corr auth login`, including its terminal and forwarded-view modes). When
  such a sign-in completes, the daemon records the account ID and a SHA-256
  over the account's enabled connection settings: address, providers, route
  kinds, origins, endpoints, OAuth client IDs, clouds and tenants, and
  credential handles. If there is no record, or the settings changed since,
  through MCP or the CLI, the tool returns `unavailable` with reason
  `cli_sign_in_required` and the CLI command, without prompting.
- The Corresync MCP server writes the whole prompt from configuration and
  fixed text: the configured alias, the configured address, and for each
  released and enabled service its provider display name and where approving
  signs in. A dedicated-browser route names its origin host, an OAuth route its
  authorization host and client ID, and a stored-credential route its server
  host. Each host is on its own line (`Sign-in page: outlook.office.com`). The
  wording differs per route kind: Corresync's dedicated browser window, the
  provider's authorization page in the person's browser, or "Corresync will
  read the saved credential from your keyring; your system may ask for
  permission". Tool arguments, provider data, and model text never reach it.
  Unknown aliases fail before any prompt. A host that is not a lowercase ASCII
  DNS name (IDNA names only in `xn--` form), an address that is not plain
  ASCII, an alias with characters other than letters, ASCII digits, `.`, `_`,
  `-`, `@`, and `+`, or an alias mixing Latin, Cyrillic, or Greek letters is
  never displayed: the tool returns `unavailable` with the CLI command.
- The form requests no data: an empty object schema. `accept` is approval.
  `decline` and `cancel` are refusals. An error, an unknown action, a missing
  answer, a client that did not declare form elicitation, or a host without a
  display all produce today's behaviour: a content-free result naming
  `corr auth login --account ALIAS`, and nothing starts.
- On approval the daemon starts the same visible sign-in `corr auth login`
  starts on that host: the account's dedicated browser profile at the
  configured Outlook Web origin, or the provider's own OAuth page in the system
  browser. Passwords, MFA, and consent stay in that browser. No credential,
  code, cookie, or token passes through MCP, the tool result, the prompt, or
  the audit log.
- The tool does not wait for sign-in. It returns `started` (with the login
  expiry), `already_signed_in`, `declined`, `unavailable`, or `in_progress` as
  soon as that outcome is known. The daemon owns the started sign-in under the
  existing browser login timeout, stores the session when the browser sign-in
  completes, and closes the browser when it expires. `account_status` reports
  the account as `pending` / `authentication_pending` until then.

### Daemon protocol

Protocol 32 adds two private methods. `login.mcp.request` reserves at most one
approval prompt per account, bound to the requesting MCP caller, for five
minutes, and returns an unguessable one-use ticket with the alias, address,
and per-service route kind, host, and OAuth client ID.
It never launches anything. `login.mcp.resolve` consumes that ticket with the
observed decision. Only the decision `approved_by_user_elicitation` can start a
sign-in, and only this MCP server's elicitation path sends it. The daemon
accepts both methods only from the `mcp` caller surface; the ordinary `login`,
`login.terminal`, and `logout` methods still refuse MCP callers. For a
2026-07-28 client the ticket is carried in `requestState` sealed with
AES-256-GCM under a key that exists only in that MCP server process; a forged,
replayed, tampered, or cross-account state starts nothing.

### Guard rails

- At most one pending approval prompt and one running interactive sign-in per
  account. A request while either exists, including a sign-in started by
  `corr auth login`, a terminal relay, or the forwarded login view, returns
  `in_progress` without prompting.
- After `decline` or `cancel`, the same account cannot be prompted from any MCP
  client for a fixed ten minutes; requests return `declined`. The same ten
  minutes apply, with `unavailable`, whenever a prompt was shown and did not
  end in `accept` (a client error, a call cancelled or timed out while the
  prompt was open, or an approval left unanswered until it expired) and when
  an MCP-started sign-in fails, expires, or is cancelled. Only a client
  without form elicitation, or a failure before any prompt, is exempt. The
  person can always run `corr auth login` themselves.
- Cool-downs and CLI verification records persist in the daemon's private
  state directory (`sign-in/cooldowns.json` and
  `sign-in/cli-verified-routes.json`, directory `0700`, files `0600`), so the
  daemon restart that follows every configuration commit cannot clear them.
  Expired cool-downs are pruned on load; records of removed accounts are
  deleted on load. An unreadable or malformed cool-down file cools down every
  account for the full period (`sign_in_state_unavailable`) and writes a
  content-free audit event; an unreadable record file verifies nothing.
- An MCP-started sign-in holds the daemon's activation lock until it ends. A
  CLI sign-in, terminal relay, or logout that would wait behind it returns at
  once naming the account and the time the sign-in ends.
  `corr auth logout --account ALIAS` for that account cancels it: the browser
  closes, the state clears, and the logout proceeds.
- Every request, prompt, approval, decline, cancellation, prompt failure, start,
  completion, failure, and expiry is written to the local audit log as the
  content-free `account.sign_in` operation with a bounded reason code, the
  opaque account ID, and the MCP caller.
- Logout, `--clear-browser`, the terminal relay, the forwarded login view,
  account creation and changes beyond the existing previews, monitoring,
  credential enrollment, and everything else MCP could not do before remain
  outside MCP.

### The CLI sign-in that verifies a destination

Because `cli_sign_in_required` sends the person to `corr auth login`, that
command must show where it goes. Every mode prints, before anything opens and
from the same server-derived description as the MCP prompt, the account, its
address, and each service's host, OAuth client ID, or credential server and
port. When the current settings have no CLI verification record, it adds that
this is the first sign-in with these settings and that the page should be
checked. The CLI learns that through the private `login.verified` query, which
answers one boolean for one account and only to the CLI surface. The command
does not stop to ask: the maintainer decided that an ordinary sign-in must not
need an extra confirmation, so the notice is the control and the person can
cancel with Ctrl-C or close the browser. With `--host` the notice reaches the
person's computer through the forwarded SSH standard error. For MCP the
`cli_sign_in_required` guidance tells the agent that the command shows the page
and that the person must check it, never that the page is safe. The remaining
risk is stated plainly: a person who runs the command without reading the
notice, on an account whose connection a model changed, can still sign in at
that changed page; the browser's address bar and the provider's own page are
then the last check.

A prompt still open when the daemon restarts (for example after a
self-committed `settings_update`) is recorded in `cooldowns.json` and cools its
account down at the next start; a decline, cancel, or failed prompt that
arrives for an unknown or stale ticket still cools down the account it names.
A failed cool-down write leaves `cooldowns.failed`, so the next start cools
down every account until a write succeeds again. While an MCP-started sign-in
holds the activation lock, a running terminal or forwarded-view interaction for
another account can still be polled and cancelled, and every new CLI sign-in or
logout decides atomically against that lock instead of waiting silently.

## Threat analysis

**Prompt injection asking for sign-in.** Mail, calendar, or chat content can
tell a model to call `account_sign_in`. The call can at most show the
server-authored prompt for a configured account whose current settings a
person already signed in to from the CLI; it cannot change the prompt or read
the result of the sign-in. The person still has to accept
and then complete sign-in in the browser. An injected agent gains no data it
could not already reach through an existing session.

**Consent fatigue.** One pending prompt per account, the ten-minute cool-down
after a refusal or unfinished sign-in, the `declined`/`unavailable` results
that tell agents to stop, and the Skill's no-retry rule bound how often a
person is asked. The prompt says declining is safe and names the CLI command.

**A malicious local MCP client lying about approval.** The MCP client is the
channel that relays the person's answer. A client, or any process that can act
as the person's MCP host, can answer `accept` without asking. The sealed
request state and the one-use ticket prove that an answer belongs to this
server's request for this account; they do not prove that a person produced
it. A client that shows elicitation requests or `input_required` results to
the model, or lets the model fill the retried call, lets the model approve,
and neither the MCP server nor the daemon can detect that. Corresync does not
claim any particular client is safe in this respect. The MCP
server and the daemon are the same local, single-user trust domain, so a local
process able to impersonate the MCP server could already reach the daemon
socket. What still protects the person in that case: the sign-in can open only
the destination their own CLI sign-in already verified, passwords and MFA are
typed by a person in that browser, the cool-down bounds repetition, and every
step is in the audit log. Only this code path sends the approved decision, and
tickets are single-use and caller-bound. A lying client can open a sign-in
window at a verified destination; it cannot sign in, read the credentials, or
skip the provider's own consent.

**Phishing through an unexpected browser window.** Over MCP a caller can
complete its own `account_add`, `account_rename`, and `settings_update`
preview/commit pairs, and an Outlook Web origin may be any HTTPS host. Without
a further control a prompt-injected model could rename `work` away, add a new
`work` at a look-alike origin, and ask the person to approve "work". The
CLI-verification requirement closes this: an account added or changed by
anyone has no matching record until a person runs `corr auth login` for it, so
the tool refuses without prompting. Destinations therefore come only from
settings a person has already signed in to from the CLI; the model, the
client, and the prompt answer cannot supply or alter a URL. The prompt also
names the exact host, the OAuth client ID, and the address in plain ASCII
before anything opens, and the window opens only after acceptance. The
verifying CLI sign-in shows the same destination and, the first time, asks
the person to confirm it, so a look-alike added through MCP stops at that
question with its host visible. A person
who did not expect a window can close it; the sign-in then fails and the
cool-down applies.

## Consequences

People using an elicitation-capable client on a desktop session can recover a
missing sign-in without leaving the client. Clients without elicitation, and
people who decline, see the same CLI command as before.

An account must be signed in once from the CLI, and again after any change to
its connection settings, before `account_sign_in` works for it. Accounts
signed in before this change need one more CLI sign-in.

This first version covers hosts where the browser can be shown: non-Linux hosts,
and Linux hosts with `DISPLAY` or `WAYLAND_DISPLAY`. On a host without a
display nothing starts and the tool returns `unavailable` with the CLI command,
because the forwarded login view prints a private pairing link that must not be
handed to a model. The CLI's OAuth scope notice is printed only by the CLI; the
provider's consent page remains authoritative for MCP-started OAuth.

Out of scope: a headless or forwarded sign-in from MCP, URL-mode elicitation,
logout or browser clearing from MCP, account creation or route changes beyond
the existing previews, and any credential entry in the client.
