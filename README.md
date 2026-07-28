# Corresync

<!-- markdownlint-disable MD013 MD033 -->
<p align="center">
  <a href="https://nkiyohara.github.io/corresync/">
    <img src="site/corresync-mark.svg" width="128" height="128" alt="Corresync: two correspondence flows around one local core">
  </a>
</p>

<p align="center">
  <strong>Mail and calendar for your terminal and AI agents—across accounts and providers, with one local safety boundary.</strong>
</p>

<p align="center">
  <a href="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml"><img alt="CI status" src="https://github.com/nkiyohara/corresync/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/nkiyohara/corresync/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/nkiyohara/corresync?display_name=tag&sort=semver"></a>
  <a href="go.mod"><img alt="Go 1.26" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white"></a>
  <a href="LICENSE"><img alt="Apache-2.0 license" src="https://img.shields.io/github/license/nkiyohara/corresync"></a>
  <a href="docs/install.md"><img alt="macOS, Linux, and Windows" src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-334155"></a>
  <a href="docs/mcp.md"><img alt="Model Context Protocol server" src="https://img.shields.io/badge/MCP-stdio-6F5BD3"></a>
</p>

<p align="center">
  <a href="https://nkiyohara.github.io/corresync/">Website</a> ·
  <a href="docs/install.md">Install</a> ·
  <a href="docs/configuration.md">Accounts</a> ·
  <a href="docs/mcp.md">Connect an agent</a> ·
  <a href="docs/features.md">Feature matrix</a> ·
  <a href="docs/README.md">Documentation</a>
</p>
<!-- markdownlint-enable MD013 MD033 -->

Corresync is a local-first CLI and
[Model Context Protocol](https://modelcontextprotocol.io/) server for mail and
calendar. Its command is `corr`. Configure several isolated accounts, choose a
mail and calendar provider for each, then use the same typed operations from a
terminal, script, or agent.

The development branch contains deterministic candidate implementations for:

- Outlook Web through a visible, browser-owned session;
- Gmail and Google Calendar through an explicitly configured public OAuth
  client;
- Microsoft 365 mail and calendar through explicitly configured Graph OAuth;
- JMAP mail;
- IMAP receive with SMTP submission;
- CalDAV calendar, including a CalDAV calendar paired with a different mail
  provider.

Only Outlook Web has a recorded live observation. Google, Graph, JMAP,
IMAP/SMTP, and CalDAV remain synthetic-only release candidates until their
documented opt-in live checks are completed; implementation is not a universal
provider-compatibility claim. The latest stable v0.7 release remains
Outlook-Web-only.

Corresync never guesses a privileged route, requests a password, or silently
falls back to Graph. Discovery is credential-free, selection is explicit, and
authentication starts only when you ask.

## Start here

### Install

```console
# macOS or Linux
brew install nkiyohara/corresync/corresync

# Windows with Scoop
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
```

Direct archives, Linux packages, WinGet status, checksums, Sigstore provenance,
and platform-signing limitations are covered in the
[installation guide](docs/install.md).

### Configure the first account

```console
corr config init
corr config validate
corr account list
corr auth login
```

The default configuration is an Outlook Web account. Its sign-in stays inside
a dedicated browser profile, preserving normal SSO, MFA, Conditional Access,
and organization notices.

To add another account, inspect candidates without credentials, review the
evidence, then select a route explicitly:

```console
corr account discover reader@example.invalid
corr account add reader@example.invalid \
  --alias personal \
  --provider google-api \
  --calendar-provider google-api \
  --oauth-client-id synthetic-public-client \
  --oauth-redirect-uri http://127.0.0.1:8765/callback \
  --authorization-key personal-google \
  --approve-oauth
corr auth login --account personal
```

OAuth providers require your own registered public-client configuration and
store grants in the OS keyring. Standards providers use an OS-keyring entry or
an explicitly approved credential helper; passwords and tokens never enter
`config.toml`. See [account and provider configuration](docs/configuration.md).

### Read across accounts

```console
corr mail list --account work --limit 20
corr mail search --all-accounts --query 'subject:"Quarterly plan"'
corr agenda list --all-accounts \
  --start 2026-07-28T00:00:00Z \
  --end 2026-07-29T00:00:00Z \
  --time-zone Europe/London
```

Cross-account results preserve account and provider provenance. One failed
provider becomes an explicit partial failure; it does not erase successful
results or collapse isolation boundaries.

### Connect an AI agent

```console
corr mcp setup codex
# or: claude-code, github-copilot, gemini-cli, qwen-code, or qoder
```

Kimi Code CLI and generic clients can use generated native configuration or the
plain `corr mcp serve` stdio command. Start a new agent session, then ask
naturally:

```text
Check all my inboxes and calendars and summarize what needs attention today.
```

The [MCP guide](docs/mcp.md) covers every supported client, the bundled Agent
Skill, resource templates, and the complete tool safety model.

## Safe writes

Consequential writes use a server-enforced `preview -> commit` protocol. A
preview normalizes the exact account, provider, target, recipients, content,
and version keys and returns a short-lived approval token. A commit succeeds
only for that same caller and digest.

```console
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file -

# Review every field, then repeat the exact command:
printf 'Synthetic body.\n' | \
  corr mail send \
    --account work \
    --to reader@example.invalid \
    --subject 'Review example' \
    --body-file - \
    --approve
```

Ambiguous write outcomes are never retried automatically. MCP keeps preview
and commit as separate tools. Tool annotations describe effects, but the same
application policy checks remain authoritative for CLI and MCP.

## Monitoring is off until you stage consent

Every existing and newly added account starts with monitoring `off`. Enabling
can advance only one boundary at a time:

```text
off → notify → queue → agent
```

- `notify` shows selected metadata through a local Linux or macOS desktop
  adapter;
- `queue` also creates a bounded, durable account-local event queue;
- `agent` may invoke one absolute executable directly, without a shell, using
  bounded JSON on stdin.

Remote runner egress requires a separate explicit approval. Polling starts only
after interactive authentication; quiet hours, debounce, hourly rate limits,
deduplication, retention, loop prevention, and a circuit breaker are enforced.
Windows currently rejects `notify` setup because Corresync does not install a
registered AppUserModelID; `queue` and `agent` remain available.

```console
corr monitor enable --mode notify \
  --notification-field sender \
  --notification-field subject \
  --approve
corr monitor status
corr events list
```

Mailbox data remains private, untrusted external content. An event, message, or
resource update is data—not permission to start a model turn or follow embedded
instructions.

## Local imports and feedback

Explicit local archives, Maildir trees, Thunderbird profiles, and supported
exports can be scanned into private, account-local, read-only staging:

```console
corr import scan ./synthetic-maildir --approve-read
```

Import never uploads or authenticates. Purging affects only Corresync-owned
staging.

For support, generate a deterministic redacted report locally:

```console
corr feedback
corr feedback --last-error
```

The complete report is printed before any action. `--copy` and `--save PATH`
work without GitHub. `--open-github` only opens a prefilled page after review;
it requires a GitHub account and never submits automatically. Report generation
performs no network request and never includes mailbox content, account IDs,
credentials, queries, or private paths.

## Architecture at a glance

```text
AI agents ───────── MCP over stdio ─┐
                                    ├── typed use cases + effect policy
Humans and scripts ──────── corr ───┘              │
                                                   │ authenticated local IPC
                                            session owner
                                            ├── browser session
                                            ├── OAuth/keyring
                                            └── standards adapters
```

The daemon owns all authenticated provider sessions. On Unix, clients validate
and pin the owner-only runtime directory, singleton lock, socket type,
ownership, permissions, peer UID, and socket identity before an IPC bearer can
be sent. Windows named pipes are restricted to SYSTEM and the current user.
There is no TCP listener or hosted relay.

## Project boundaries

Corresync includes typed mail and calendar operations, multiple accounts,
service-specific routes, read-only cross-account projections, local import
staging, opt-in monitoring, stable JSON, MCP stdio, completion, verified
updates, and content-free audit records.

It deliberately excludes Teams chat, channels, calls, recordings, and meeting
lifecycle management; tenant-wide access; unattended credential login; hosted
relays; TLS interception; arbitrary provider actions; and automatic telemetry.
Teams join links are supported only as a typed calendar-event property where
the selected calendar provider reports that capability.

Exact coverage and provider degradations are in the
[feature matrix](docs/features.md). Security assumptions are in the
[threat model](docs/threat-model.md).

## Command and migration names

`corr` is the primary command. The product, package, repository, config/state
roots, plugin, and MCP server remain named Corresync. Releases in the finite
v0.8–v0.9 compatibility window also include a `corresync` executable with the
same build; new examples use `corr`.

The former `owa` command and `owa-bridge` paths are migration inputs only and
are never canonical output. Users coming from v0.6 should follow the
[v0.7 migration guide](docs/migration-v0.7.md).

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md), the
[architecture](docs/architecture.md), and [AGENTS.md](AGENTS.md). Every default
test is synthetic. Run the complete local gate before committing:

```console
mise exec -- task verify
```

Please report vulnerabilities through
[GitHub private vulnerability reporting](SECURITY.md), not a public issue.
