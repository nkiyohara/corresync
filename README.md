# Corresync

<!-- markdownlint-disable MD013 MD033 -->
<p align="center">
  <a href="https://corresync.org/">
    <img src="site/corresync-mark.svg" width="144" height="144" alt="Corresync: two correspondence flows around one local core">
  </a>
</p>

<p align="center">
  <strong>A local CLI and MCP server for mail, calendars, and tasks.</strong><br>
  It runs on your computer and works across providers.
</p>

<p align="center">
  <a href="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml"><img alt="CI status" src="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/nkiyohara/corresync/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/nkiyohara/corresync?display_name=tag&sort=semver"></a>
  <a href="go.mod"><img alt="Go 1.27" src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white"></a>
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

`corr` is a command-line tool and a local
[Model Context Protocol](https://modelcontextprotocol.io/) (MCP) server that
reaches all your mail, calendar, and task accounts while keeping them separate.

Gmail, Google Calendar, and Google Tasks use a Desktop OAuth client in a
Google Cloud project you control. Corresync-managed Google OAuth is dormant.
Tasks also work with Microsoft To Do, Todoist, TickTick, and CalDAV VTODO, each
a route you select yourself. Other task providers are unavailable until their
contracts are implemented.

- Search mail and list one agenda across accounts. Each result shows which
  account and provider it came from.
- Use one task format across providers as their adapters become available.
  Each task records where it came from.
- Save private, per-account mail searches and relative calendar windows. Each
  run reads live from the provider and creates no local mailbox or calendar
  copy.
- Run the same operations from the CLI, as stable JSON, or through an AI agent.
- Sign in through a visible browser window, an OAuth flow with a public client
  you choose, or a local credential store you approve.
- Preview sends, moves, deletes, and other writes before they run. If a
  write's outcome is unknown, Corresync stops for you to check instead of
  retrying.

## Example

```console
$ corr mail search --all-accounts \
    --query 'subject:"Quarterly plan"' --limit 3
● work · microsoft-owa   Ana Ruiz   Plan review
· personal · jmap       Finance    Plan receipt

$ corr agenda list --all-accounts \
    --start 2026-07-29T00:00:00Z \
    --end 2026-07-30T00:00:00Z
```

Connect an agent to the same local server:

```console
corr integrations detect
corr integrations plan codex claude-code
corr integrations setup codex claude-code
# Phase A also includes github-copilot, gemini-cli, qwen-code, qoder, kimi-code
```

Then ask it something like:

```text
Check my inboxes, calendars, and task lists and summarize what needs attention today.
```

If one provider fails, the output reports a partial failure and still shows
the results that succeeded. Writes always target exactly one account.

## Providers

Mail, calendar, and tasks each have their own route. For example, an account
can use IMAP/SMTP for mail and CalDAV for its calendar, and pick a task route
separately.

<!-- markdownlint-disable MD013 -->
| Route | Mail | Calendar | Tasks | Authentication |
| --- | --- | --- | --- | --- |
| Outlook Web | Typed reads and writes | Selectable calendars; provider-supported Teams link | — | Dedicated visible browser profile |
| Google (your OAuth client) | Gmail API reads and writes; no permanent delete | Selectable calendars; Google Meet when advertised | Google Tasks through a separate task-only grant | Your Desktop OAuth client; client credential and grants in the OS keyring |
| Microsoft Graph | Typed reads and writes | Selectable calendars; typed Teams-link creation | Microsoft To Do | Your authorized public OAuth client; OS-keyring grant |
| Todoist | — | — | Typed Todoist operations | Your authorized public OAuth client; OS-keyring grant |
| TickTick | — | — | Typed TickTick operations | Your authorized confidential OAuth client; external secret handle and OS-keyring grant |
| JMAP | Typed mail operations | — | — | OS keyring or approved credential helper |
| IMAP / SMTP | IMAP read/manage and SMTP draft/send | — | — | OS keyring or approved credential helper |
| CalDAV | — | Typed calendar operations and conditional scheduling | VTODO lists, search, sync, and reviewed writes | OS keyring or approved credential helper |
<!-- markdownlint-enable MD013 -->

Discovery reads DNS, well-known, and provider metadata without credentials. It
never signs in or adds an account. Corresync never falls back to Microsoft
Graph or your own Google authorization; you must choose them. Google discovery
can guide you through creating the Desktop client, but no Google sign-in starts
until you run `corr auth login`. See the
[annotated Google setup guide](docs/google-oauth-setup.md).

iCloud has a guided preset over the standard IMAP/SMTP and CalDAV adapters.
`corr setup` recognizes documented iCloud Mail address families, or a domain
with the complete verified set of Apple SRV endpoints. It reviews mail and
calendar as one account and uses one external credential handle by default.
After the account is added, you can have it open Apple's app-password page and
the OS credential prompt. The `corr` process never reads the app-specific
password.

Every available v0.9 route above has synthetic provider-contract and
application tests. The user-owned Google API route is available but
live-unobserved. Other provider and platform implementations stay
**live-unobserved** until an authorized observation record with no account
content is tied to the exact commit. Live-unobserved means the code passes
synthetic tests, but no real-account check is on record for that commit. Read
the [compatibility evidence](docs/compatibility.md) before you connect a
sensitive account.

## Set up Corresync

The setup buttons on the Corresync website open your desktop agent with a
request already filled in. The request installs Corresync or updates an existing
install. Nothing is sent until you review it and send it yourself. Manual
commands, package managers, and downloads are below.

[**Set up with ChatGPT**](https://corresync.org/#setup-chatgpt) ·
[**Set up with Claude**](https://corresync.org/#setup-claude)

Recommended: the standalone CLI. New setups turn on verified automatic updates.
If Corresync is already installed, setup keeps your package manager,
configuration and update setting unless you choose to change them.

You need the ChatGPT Codex or Claude Code desktop app with local access to this
computer. Approve each permission prompt when it appears. You don't need
administrator rights. Sign in to your accounts and complete MFA yourself. Never
paste passwords or codes into the chat.

If you're on a phone, open this page on the computer you want to set up or copy
the instructions to it.

<!-- markdownlint-disable MD033 -->
<details>
<summary>Optional: manual installation and later setup steps</summary>
<!-- markdownlint-enable MD033 -->

For a guided page, follow
[getting started on the website](https://corresync.org/getting-started.html).

### 1. Install

The standalone installers are the quickest option on every platform. They pick
the latest stable release, verify the archive and the binary, install for the
current user without admin rights, and do not touch account setup.

#### macOS and Linux

```console
curl -LsSf https://corresync.org/install.sh | sh
corr --version
```

#### Windows PowerShell

```powershell
powershell -NoProfile -Command "irm https://corresync.org/install.ps1 | iex"
corr --version
```

You can read [`install.sh`](https://corresync.org/install.sh) or
[`install.ps1`](https://corresync.org/install.ps1) before running it. Package
managers are fully supported too:

```console
# Homebrew · macOS or Linux
brew install nkiyohara/corresync/corresync

# WinGet · Windows
winget install --id nkiyohara.Corresync --exact

# Scoop · Windows
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
```

Direct archives, native Linux packages, version pinning, checksums, and
Sigstore provenance are in the [installation guide](docs/install.md). The
[guided website](https://corresync.org/getting-started.html#step-install) shows
the same commands for people who don't use GitHub much.

Claude Desktop users can also install the platform-universal `.mcpb` from the
[latest release](https://github.com/nkiyohara/corresync/releases/latest).
It runs the same verified `corr mcp serve` binary locally over stdio. It does
not deploy Corresync or relay mailbox data. Install the CLI first to add
accounts and sign in (steps below), then open or drag the `.mcpb` into Claude
Desktop.

### Updates

Interactive commands do a quiet, cached release check at startup. If an update
exists, Corresync prints the command for your install: `corr update` for the
standalone build, or the matching Homebrew, Scoop, or WinGet command. It never
changes package-managed files.

Stable releases are the default. Standalone users who want signed release
candidates can choose the preview channel in:

```console
corr settings
```

For scripts, the same change is:

```console
corr config set updates.channel preview
corr update
```

To switch back, run `corr config set updates.channel stable`. Corresync never
downgrades the running binary. Preview releases are not published to
package-manager catalogs.

New standalone configurations install verified updates automatically. Existing
configurations keep their current setting. To turn it on:

```console
corr config set updates.auto_install true
```

Automatic installation never runs during MCP tool calls, configuration changes,
daemon work, JSON output, pipes, or other non-interactive use. Bare `corr` also
checks when interactive. After verifying an update, the updater restarts the
session owner (the local process that holds your sign-in sessions) and runs
your command with the new executable. Session owners from 0.9.2 also
exit after something else replaces the executable, once accepted operations
finish. To turn installation off, run
`corr config set updates.auto_install false`.

### 2. Add and sign in to your account

```console
corr setup
```

`corr setup` reads your local state to see what is already done, so you can
resume it. It can install shell completion at a user-local path it shows you.
It then discovers routes without credentials, previews the mail, calendar, and
task routes, and adds the account only after you confirm. Signing in, or
allowing access to an external credential, is a separate choice. The same run
can add more accounts, select several detected agent hosts, preview their local
integration plans, apply them after one confirmation, and check each result.
Running it again skips what already works and offers missing or repairable
steps.

After you choose a connection route, the wizard shows a checklist of services.
For a Microsoft account, Outlook Mail and Calendar are checked by default, and
you can change each one. Microsoft To Do is optional and needs its own Graph
authorization. Teams messaging can be selected as a preview, through Teams Web
or a Graph authorization you grant. Mattermost is also available as a preview.
These previews lack complete live compatibility evidence. Discovery only shows
which routes exist; it does not prove that a checked service is enabled for
the account.

`corr settings` opens the same account wizard in an arrow-key form. Use it to
sign in to, rename, select, or remove accounts, and to manage updates, safety,
and browser sign-in. Its Setup guide entry resumes the full `corr setup` flow.
The top-level Accounts category holds Add account and your accounts; selecting
one shows only its actions. Removal shows the local data it deletes and asks
you to confirm; removing the default first asks which account replaces it.
Each choice also shows the equivalent command. Set `CORRESYNC_ACCESSIBLE=true`
for line-by-line screen-reader prompts, and type `:cancel` to leave an input
step safely. To rename directly:

```console
corr account rename personal work
```

For scripts, use the non-interactive form:

```console
corr setup you@example.com --alias personal
corr auth login --account personal
corr doctor --account personal
```

`corr setup ADDRESS` writes the same secret-free local configuration, runs
discovery without credentials, and adds only a first-party route that can be
selected automatically. It never opens a sign-in page.

If no route can be selected safely, or you want an API or standards route, look
at the discovery results and choose the provider settings yourself:

```console
corr account discover reader@example.invalid
corr account add reader@example.invalid --help
```

For Gmail, Google Calendar, and Google Tasks, create a Desktop OAuth client in
a Google Cloud project you control. Guided setup checks the client JSON you
download and stores the credential generated from it in the OS keyring. The
[Google OAuth setup guide](docs/google-oauth-setup.md) covers the current Cloud
Console screens, audience choices, exact APIs and scopes, import, and login.

Before you install, you can use the optional
[provider compatibility checker](https://corresync.org/providers.html#check)
to classify public evidence for an address. Your browser sends only the
domain after the `@`, in a request body. It never sends the part before the
`@`, stores the address, or starts sign-in. The service uses one fixed DNS
resolver and returns no raw DNS records. You can skip it and run
`corr account discover ADDRESS` locally.

Adding an account does not sign you in. OAuth routes need a client registration
you are authorized to use. Google also needs a separate external handle for its
generated Desktop client credential. Standards routes use a keyring entry or an
approved helper reference. Passwords and tokens never go into `config.toml`.
See [account and provider configuration](docs/configuration.md). For Outlook
Web, the later `auth login` opens a dedicated, visible browser profile. That
window holds the session until account logout or daemon shutdown. SSO, MFA,
Conditional Access, and organization notices stay in the provider's own sign-in
flow.

### 3. Connect an agent

```console
corr mcp setup codex
```

Run `corr mcp --help` for Claude Code, GitHub Copilot CLI, Gemini CLI, Qwen
Code, Qoder, Kimi Code CLI, and generic stdio clients. Corresync exposes 45
narrow tools and two read-only monitor resources. There is no HTTP, SSE, or
remote MCP endpoint, and no hosted relay.

</details>

## Previewing writes

Writes such as sending mail use a server-enforced `preview -> commit`
protocol. The first command shows the normalized account, provider, target,
recipients, content digest, and version preconditions, and changes nothing.

```console
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file -
```

Check every field, then run the same command again with `--approve`:

```console
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file - \
    --approve
```

Approval is short-lived, single-use, and tied to the caller, account,
provider, target, payload, and effect. Changing any reviewed field invalidates
it. Over MCP, preview and commit are separate tools.

[See the complete safety model](https://corresync.org/safety.html).
The [Privacy Policy](https://corresync.org/privacy.html) covers Google data
access, local storage, disclosure, retention, and removal. The
[Terms of Use](https://corresync.org/terms.html) keep the software rights
that Apache-2.0 grants.

## Other features

- **Cross-account views:** size-limited mail search and agenda results keep
  the original account, provider, calendar, time zone, and partial failures.
- **Read-only import staging:** inspect an approved local archive, Maildir
  tree, or supported export without uploading or changing it.
- **Opt-in monitoring:** step up from `off` to local notifications, a durable
  queue, and finally one approved runner that uses no shell. Sending data off
  your computer needs separate consent.
- **Feedback reports:** build a report locally from an allowlist, review it,
  then copy, save, or open a prefilled GitHub page. A separate default-off
  setting can file an even smaller allowlist-only public issue through your
  signed-in `gh` client after an interactive failure.

Corresync treats mailbox, calendar, import, and event-queue content as private,
untrusted data. That content can never run a command or start an agent.

## Limits

- Gmail, Google Calendar, and Google Tasks need your own Google Desktop OAuth
  client. Corresync-managed Google OAuth is disabled. The user-owned route has
  synthetic contract tests and is live-unobserved.
- Microsoft Graph needs your own authorized public-client registration.
  Corresync ships no token relay.
- Windows desktop notifications are unavailable because Corresync does not
  install an AppUserModelID. Queue and approved-runner modes still work.
- Provider meeting links are requested only when the selected calendar route
  reports native support.
- Cross-compilation and synthetic fixtures do not prove how native browsers,
  keyrings, IPC, providers, package managers, Gatekeeper, or SmartScreen
  behave.
- Out of scope: Teams calls, recordings, and meeting lifecycle management;
  tenant-wide access; unattended login; TLS interception; arbitrary provider
  actions; automatic telemetry; and raw crash upload.

[features.md](docs/features.md) lists each provider's actions and gaps.

## Architecture

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

The session owner has no TCP listener. On Unix, before a client
sends the local bearer token, it checks and pins the private runtime directory
(which does not depend on environment variables), the singleton lock, the
socket, and the peer UID. On Windows, clients first check the protected named
pipe, its owner and DACL, the server process, and the SID.

See the [architecture](docs/architecture.md),
[authentication model](docs/authentication.md), and
[threat model](docs/threat-model.md) for details.

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
| Connect Google with your own Desktop client | [Google OAuth setup](docs/google-oauth-setup.md) |
| Review the dormant managed-client plan | [Google verification runbook](docs/google-oauth-verification.md) |
| Review every guide | [Documentation map](docs/README.md) |

If you are upgrading from a version before v0.7, see the
[historical migration guide](docs/migration-v0.7.md). `corr` is the main
command. The product, package, repository, configuration roots, plugin, and MCP
server are still named Corresync.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md). Default
tests and CI use only synthetic fixtures:

```console
mise exec -- task verify
```

Please report vulnerabilities through
[GitHub private vulnerability reporting](SECURITY.md), not in a public issue.

For remote terminals, see [graphical login over SSH](docs/remote-login.md).
