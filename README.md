# Corresync

<!-- markdownlint-disable MD013 MD033 -->
<p align="center">
  <a href="https://corresync.org/">
    <img src="site/corresync-mark.svg" width="144" height="144" alt="Corresync: two correspondence flows around one local core">
  </a>
</p>

<p align="center">
  <strong>All your mail and calendars. One local MCP server and CLI.</strong><br>
  Provider-neutral, local-first tooling for AI agents, scripts, and you.
</p>

<p align="center">
  <a href="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml"><img alt="CI status" src="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/nkiyohara/corresync/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/nkiyohara/corresync?display_name=tag&sort=semver"></a>
  <a href="go.mod"><img alt="Go 1.26" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="Apache-2.0 license" src="https://img.shields.io/github/license/nkiyohara/corresync"></a>
  <a href="docs/install.md"><img alt="macOS, Linux, and Windows" src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-334155"></a>
  <a href="docs/mcp.md"><img alt="Model Context Protocol over stdio" src="https://img.shields.io/badge/MCP-stdio-6F5BD3"></a>
  <a href="docs/README.md"><img alt="Current documentation" src="https://img.shields.io/badge/docs-current-3F7AD6"></a>
  <a href="docs/architecture.md"><img alt="Local-first architecture" src="https://img.shields.io/badge/architecture-local--first-E0574A"></a>
</p>

<p align="center">
  <a href="https://corresync.org/">Website</a> ·
  <a href="https://corresync.org/getting-started.html">Getting started</a> ·
  <a href="https://corresync.org/providers.html">Providers</a> ·
  <a href="https://corresync.org/features.html">Features</a> ·
  <a href="https://corresync.org/safety.html">Safety</a> ·
  <a href="https://corresync.org/privacy.html">Privacy</a> ·
  <a href="https://corresync.org/terms.html">Terms</a> ·
  <a href="docs/README.md">Technical docs</a>
</p>
<!-- markdownlint-enable MD013 MD033 -->

`corr` brings isolated Outlook, Google, Microsoft 365, JMAP, IMAP/SMTP,
and CalDAV accounts into one terminal—and one local
[Model Context Protocol](https://modelcontextprotocol.io/) server.

- Search mail and build one agenda across accounts without collapsing their
  identity or provider provenance.
- Use the same typed operations from a human-friendly CLI, stable JSON, or an
  AI agent.
- Keep sign-in in a visible browser, an explicit public-client OAuth flow, or
  an approved local credential store.
- Review consequential effects before they happen. Unknown remote outcomes
  stop for reconciliation instead of being retried automatically.

## What it feels like

```console
$ corr mail search --all-accounts \
    --query 'subject:"Quarterly plan"' --limit 3
● work · microsoft-owa   Ana Ruiz   Plan review
· personal · google     Finance    Plan receipt

$ corr agenda list --all-accounts \
    --start 2026-07-29T00:00:00Z \
    --end 2026-07-30T00:00:00Z
```

Connect an agent to the same local core:

```console
corr mcp setup codex
# also: claude-code, github-copilot, gemini-cli, qwen-code, qoder
```

Then ask naturally:

```text
Check all my inboxes and calendars and summarize what needs attention today.
```

One failed provider becomes an explicit partial failure. Successful results
remain available, and writes still require one exact account.

## Choose the route that fits

Mail and calendar routes are selected independently. For example, an account
can pair IMAP/SMTP mail with a CalDAV calendar.

<!-- markdownlint-disable MD013 -->
| Route | Mail | Calendar | Authentication |
| --- | --- | --- | --- |
| Outlook Web | Typed reads and writes | Selectable calendars; provider-supported Teams link | Dedicated visible browser profile |
| Google (not yet available) | Gmail over IMAP/SMTP XOAUTH2 | Selectable calendars; Google Meet when advertised | Built; the official OAuth application is being prepared for verification |
| Microsoft Graph | Typed reads and writes | Selectable calendars; typed Teams-link creation | Your authorized public OAuth client; OS-keyring grant |
| JMAP | Typed mail operations | — | OS keyring or approved credential helper |
| IMAP / SMTP | IMAP read/manage and SMTP draft/send | — | OS keyring or approved credential helper |
| CalDAV | — | Typed calendar operations and conditional scheduling | OS keyring or approved credential helper |
<!-- markdownlint-enable MD013 -->

Discovery gathers DNS, well-known, and provider metadata without credentials.
It never authenticates or adds an account. Microsoft Graph and managed Google
authorization remain explicit choices and are never automatic fallbacks.
Google discovery does not add an account while Corresync prepares and verifies
its official Google OAuth application. For now, connect Gmail and Calendar through
Google's official
[Workspace MCP servers](https://developers.google.com/workspace/guides/configure-mcp-servers)
(Developer Preview).

Every v0.8 route above has synthetic provider-contract and application
coverage. The v0.8 provider and platform implementations remain
**live-unobserved** until an authorized, content-free observation is bound to
the exact commit. See [compatibility evidence](docs/compatibility.md) before
connecting a sensitive account.

## From install to a first read

Prefer a guided page? Follow
[getting started on the website](https://corresync.org/getting-started.html).

### 1. Install

Each block below is complete for its platform: it installs `corr` and then
verifies the installed version.

#### Linux

```console
curl -LsSf https://corresync.org/install.sh | sh
corr --version
```

#### macOS (Homebrew — also works on Linux)

```console
brew install nkiyohara/corresync/corresync
corr --version
```

#### Windows (Scoop)

```console
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
corr --version
```

Direct archives, native Linux packages, checksums, Sigstore provenance, and
current WinGet status are in the [installation guide](docs/install.md).

Claude Desktop users can also install the platform-universal `.mcpb` from the
[latest release](https://github.com/nkiyohara/corresync/releases/latest).
The bundle runs the same verified `corr mcp serve` binary locally over stdio;
it does not deploy Corresync or relay mailbox data. Install the CLI first for
the explicit account setup and sign-in steps below, then open or drag the
`.mcpb` into Claude Desktop.

### Stay current

Interactive commands make a quiet, cached release check at startup. If an
update exists, Corresync shows the exact command for your installation:
`corr update` for the standalone build, or the matching Homebrew, Scoop, or
WinGet command. Package-managed files are never changed by Corresync.

Stable releases are selected by default. Standalone users who want signed
release candidates can choose the preview channel interactively:

```console
corr settings
```

The equivalent copy-paste commands for scripts are:

```console
corr config set updates.channel preview
corr update
```

Switch back with `corr config set updates.channel stable`; Corresync never
downgrades the running binary. Preview releases do not enter package-manager
catalogs.

Standalone users can opt in to verified automatic installation:

```console
corr config set updates.auto_install true
```

It never runs during MCP tool calls, configuration changes, daemon work, JSON
output, pipes, or other non-interactive paths. The current command continues
normally and the new binary is active on the next `corr` start.

### 2. Add and sign in to your account

```console
corr setup you@example.com --alias personal
corr auth login --account personal
corr mail folders --account personal
corr calendar folders --account personal
```

Run `corr settings` to add, sign in to, rename, select, or remove accounts, and
to manage updates, safety, and browser sign-in from an arrow-key form. The
top-level Accounts category contains Add account and the configured account
list; selecting an account opens only actions for that account. Removal
previews the local data it deletes and asks for confirmation; removing the
default first asks which remaining account should replace it. Each choice also
displays the equivalent command, so the interactive flow teaches the direct
CLI.
Set `CORRESYNC_ACCESSIBLE=true` for line-oriented screen-reader prompts. The
direct rename form remains:

```console
corr account rename personal work
```

`setup` creates a provider-neutral, secret-free local configuration when
needed, performs credential-free discovery, and adds only an automatically
selectable first-party route. It never opens a sign-in page. Authentication is
a separate, account-specific action.

If no route can be selected safely—or if you want an API or standards route—
inspect the evidence and choose the exact provider settings:

```console
corr account discover reader@example.invalid
corr account add reader@example.invalid --help
```

For Gmail and Google Calendar, Corresync's OAuth application is still under
review. Until guided connection opens, use Google's official
[Workspace MCP setup](https://developers.google.com/workspace/guides/configure-mcp-servers)
with your agent. It is currently a Google Developer Preview and has its own
Google Cloud and OAuth setup requirements.

Account addition does not authenticate. OAuth routes require a public-client
registration you are authorized to use. Standards routes use a keyring entry
or explicitly approved helper reference. Passwords and tokens never enter
`config.toml`. See [account and provider configuration](docs/configuration.md).
The Outlook Web route opens a dedicated visible profile only during the later
`auth login`; SSO, MFA, Conditional Access, and organization notices remain
inside the provider-owned flow.

### 3. Connect an agent

```console
corr mcp setup codex
```

Use `corr mcp --help` for Claude Code, GitHub Copilot CLI, Gemini CLI, Qwen
Code, Qoder, Kimi Code CLI, and generic stdio clients. Corresync exposes 43
narrow tools and two read-only monitor resources; there is no HTTP, SSE,
remote MCP endpoint, or hosted relay.

## Nothing sends on the first attempt

Consequential writes use a server-enforced `preview -> commit` protocol. The
first command shows the normalized account, provider, target, recipients,
content digest, and version preconditions without performing the effect.

```console
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file -
```

After reviewing every field, repeat the exact command with approval:

```console
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file - \
    --approve
```

Approval is short-lived, single-use, and bound to the caller, account,
provider, target, payload, and effect. Changing any reviewed field invalidates
it. MCP keeps preview and commit as separate typed tools.

[See the complete safety model](https://corresync.org/safety.html).
Corresync's public [Privacy Policy](https://corresync.org/privacy.html)
explains Google data access, local storage, disclosure, retention, and removal;
the [Terms of Use](https://corresync.org/terms.html) preserve
the software rights granted by Apache-2.0.

## More than one inbox

- **Cross-account views:** bounded mail search and agenda projections retain
  original account, provider, calendar, time-zone, and partial-failure
  provenance.
- **Read-only import staging:** inspect an explicitly approved local archive,
  Maildir tree, or supported export without uploading or mutating the source.
- **Opt-in monitoring:** move deliberately from `off` to local notification,
  durable queueing, and finally one approved no-shell runner. Remote egress is
  a separate consent.
- **Privacy-preserving feedback:** generate an allowlisted report locally,
  review it, then explicitly copy, save, or open a prefilled GitHub page.
  Corresync never submits automatically.

Mailbox, calendar, import, and event-queue values are private, untrusted
external data. Their content is never authority to run a command or start an
agent.

## Honest edges

- Corresync is preparing its official Google OAuth application for
  verification. Guided Google connection is not offered yet; use Google's
  official Workspace MCP in the meantime.
- Microsoft Graph requires your own authorized public-client registration.
  Corresync ships no token relay.
- Windows desktop notification setup is unavailable because Corresync does
  not install an AppUserModelID; queue and approved runner modes remain
  available.
- Provider meeting links are requested only when the selected calendar route
  reports native support.
- Cross-compilation and synthetic fixtures do not prove native browser,
  keyring, IPC, provider, package-manager, Gatekeeper, or SmartScreen behavior.
- Teams chat, channels, calls, recordings, and meeting lifecycle management;
  tenant-wide access; unattended login; TLS interception; arbitrary provider
  actions; and automatic telemetry are outside scope.

The exact action matrix and typed provider degradations are in
[features.md](docs/features.md).

## One local safety boundary

```text
AI agents ───────── MCP over stdio ─┐
                                    ├── typed use cases + effect policy
Humans and scripts ──────── corr ───┘              │
                                                   │ authenticated local IPC
                                            session owner
                                            ├── browser-owned sessions
                                            ├── explicit OAuth + keyring
                                            └── standards adapters
```

The session owner exposes no TCP listener. On Unix, clients authenticate and
pin the private runtime directory, singleton lock, socket, and peer UID before
the local bearer can be sent. On Windows, clients verify the protected named
pipe, owner, DACL, server process, and SID first.

Read the [architecture](docs/architecture.md),
[authentication model](docs/authentication.md), and
[threat model](docs/threat-model.md) for the complete boundaries.

## Documentation

| I want to… | Start here |
| --- | --- |
| Install and verify a release | [Installation](docs/install.md) |
| Add accounts and choose routes | [Configuration](docs/configuration.md) |
| Understand browser, OAuth, and standards sign-in | [Authentication](docs/authentication.md) |
| Learn CLI commands | [CLI guide](docs/cli.md) |
| Connect an AI client | [MCP guide](docs/mcp.md) |
| Compare provider actions and degradations | [Feature matrix](docs/features.md) |
| Consume stable machine output | [JSON contract](docs/json.md) |
| Integrate versioned public/local contracts | [Versioning policy](docs/adr/0020-public-and-local-versioning.md) |
| Verify compatibility claims | [Evidence matrix](docs/compatibility.md) |
| Review privacy and Google data handling | [Privacy Policy](https://corresync.org/privacy.html) |
| Review use and open-source license terms | [Terms of Use](https://corresync.org/terms.html) |
| Prepare the official Google OAuth review | [Google verification runbook](docs/google-oauth-verification.md) |
| Review every guide | [Documentation map](docs/README.md) |

Users upgrading from versions before v0.7 can follow the
[historical migration guide](docs/migration-v0.7.md). `corr` is the primary
command. The product, package, repository, configuration roots, plugin, and MCP
server remain named Corresync.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md). Default
tests and CI use only synthetic fixtures:

```console
mise exec -- task verify
```

Please report vulnerabilities through
[GitHub private vulnerability reporting](SECURITY.md), never a public issue.
