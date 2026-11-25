# ADR 0039: User-approved sign-in from MCP

- Status: Accepted
- Date: 2026-11-25
- Related: ADR 0002, ADR 0003, ADR 0012, ADR 0020, ADR 0036, ADR 0038, issue #45

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
- The Corresync MCP server writes the whole prompt. It is built only from the
  configured alias, fixed provider display names, and service names, plus fixed
  text. Tool arguments, provider data, and model text never reach it. Unknown
  aliases fail before any prompt. Aliases containing characters other than
  letters, digits, `.`, `_`, `-`, `@`, and `+` are not quoted into a prompt;
  the tool returns `unavailable` with the CLI command instead.
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
minutes, and returns an unguessable one-use ticket with the alias and routes.
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
  client for a fixed ten minutes; requests return `declined`. After an
  MCP-started sign-in fails or expires unfinished, the same ten minutes apply
  and requests return `unavailable`. The person can always run
  `corr auth login` themselves.
- Every request, prompt, approval, decline, cancellation, prompt failure, start,
  completion, failure, and expiry is written to the local audit log as the
  content-free `account.sign_in` operation with a bounded reason code, the
  opaque account ID, and the MCP caller.
- Logout, `--clear-browser`, the terminal relay, the forwarded login view,
  account creation and changes beyond the existing previews, monitoring,
  credential enrollment, and everything else MCP could not do before remain
  outside MCP.

## Threat analysis

**Prompt injection asking for sign-in.** Mail, calendar, or chat content can
tell a model to call `account_sign_in`. The call can at most show the
server-authored prompt for a configured account; it cannot change the prompt,
choose a URL, or read the result of the sign-in. The person still has to accept
and then complete sign-in in the browser. An injected agent gains no data it
could not already reach through an existing session.

**Consent fatigue.** One pending prompt per account, the ten-minute cool-down
after a refusal or unfinished sign-in, the `declined`/`unavailable` results
that tell agents to stop, and the Skill's no-retry rule bound how often a
person is asked. The prompt says declining is safe and names the CLI command.

**A malicious local MCP client lying about approval.** The MCP client is the
channel that relays the person's answer. A client, or any process that can act
as the person's MCP host, can answer `accept` without asking. This control
does not prove a human clicked anything; it proves only that the answer came
through Corresync's own elicitation path in this MCP server process. The MCP
server and the daemon are the same local, single-user trust domain, so a local
process able to impersonate the MCP server could already reach the daemon
socket. The concrete controls are that only this code path sends the approved
decision, tickets are single-use and caller-bound, the browser still requires
the person's own credentials and MFA, and every step is audited. A lying client
can open a sign-in window; it cannot sign in, read the credentials, or skip the
provider's own consent.

**Phishing through an unexpected browser window.** The prompt names the alias
and provider before anything opens, and the window opens only after acceptance.
For Outlook Web it is Corresync's dedicated profile at the configured origin
only, exactly as `corr auth login` opens it; OAuth routes open only the fixed
provider authorization endpoints already used by the CLI. No URL is accepted
from the model, the client, or the prompt answer. A person who did not expect a
window can close it; the sign-in then expires and the cool-down applies.

## Consequences

People using an elicitation-capable client on a desktop session can recover a
missing sign-in without leaving the client. Clients without elicitation, and
people who decline, see the same CLI command as before.

This first version covers hosts where the browser can be shown: non-Linux hosts,
and Linux hosts with `DISPLAY` or `WAYLAND_DISPLAY`. On a host without a
display nothing starts and the tool returns `unavailable` with the CLI command,
because the forwarded login view prints a private pairing link that must not be
handed to a model. The CLI's OAuth scope notice is printed only by the CLI; the
provider's consent page remains authoritative for MCP-started OAuth.

Out of scope: a headless or forwarded sign-in from MCP, URL-mode elicitation,
logout or browser clearing from MCP, account creation or route changes beyond
the existing previews, and any credential entry in the client.
