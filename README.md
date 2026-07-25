# owa-bridge

Local-first Outlook Web mail and calendar for humans, scripts, and AI agents.

`owa-bridge` is a cross-platform CLI and MCP server that works through the
interactive Outlook Web session you already use. It needs no Microsoft Graph
app registration, hosted bridge, or captured password, so it fits environments
where Graph application access is unavailable.

[Website](https://nkiyohara.github.io/owa-bridge/) ·
[Latest release](https://github.com/nkiyohara/owa-bridge/releases/latest) ·
[Install](docs/install.md) · [MCP guide](docs/mcp.md) ·
[Feature matrix](docs/features.md) · [JSON contract](docs/json.md)

> [!WARNING]
> `owa-bridge` 0.6 is an early release over undocumented Outlook Web contracts.
> Use only an account you are authorized to access, review every write, and
> keep Outlook available for reconciliation after an unknown outcome.

## Install and sign in

```console
# macOS or Linux
brew install nkiyohara/owa-bridge/owa-bridge

# Windows
winget install --id nkiyohara.OWABridge --exact

# First run on every platform
owa config init
owa config validate
owa auth login
owa auth status
owa doctor --online
```

Scoop, direct downloads, checksum verification, Sigstore verification, and
Linux packages are covered in the [install guide](docs/install.md). Sign-in,
MFA, and Conditional Access stay inside a dedicated browser profile; the CLI
never asks for a password.

Released binaries quietly cache a public stable-release check for 24 hours and
show an update hint only on an interactive terminal. Run `owa update`: direct
installs verify signed provenance and update with a rollback copy, while
package-managed installs print their exact owner-specific command. Use
`owa update check` for read-only status; MCP, completion, pipes, and JSON output
never receive an automatic notice or terminal styling.

Run `owa --version` for the conventional one-line version, `owa version --json`
for build metadata, and `owa help <command>` for command-specific help.

## Connect an AI agent

Choose the client you use and run one command:

```console
owa mcp setup codex
# or: owa mcp setup claude-code
# or: owa mcp setup github-copilot
# or: owa mcp setup gemini-cli
# or: owa mcp setup qwen-code
# or: owa mcp setup qoder
```

Then start a new agent session and ask naturally:

```text
Check Outlook and summarize the messages that need my attention.
```

The default connection name is the readable `outlook-web`. Clear server
instructions and task-oriented tool descriptions help agents discover the
right mail or calendar tool without naming it explicitly.

For even stronger discovery, install the bundled Agent Skill. In Codex, ask:

```text
Install the owa-bridge skill from
https://github.com/nkiyohara/owa-bridge/tree/main/plugins/owa-bridge/skills/owa-bridge
using $skill-installer.
```

Claude Code can install the same Skill as a plugin:

```console
claude plugin marketplace add nkiyohara/owa-bridge
claude plugin install owa-bridge@owa-bridge
```

The [MCP guide](docs/mcp.md) covers all seven clients—Codex, Claude Code,
GitHub Copilot CLI, Gemini CLI, Qwen Code, Qoder, and Kimi Code CLI—plus native
configuration documents, project scopes, Skill installation, migration from
the former `owa` connection name, and troubleshooting.

## Use it directly

Metadata-first reads:

```console
owa mail folders
owa mail list --limit 25
owa mail search --query 'subject:"Quarterly plan" from:reader'
owa calendar list \
  --start 2026-07-20T00:00:00Z \
  --end 2026-07-21T00:00:00Z
```

Reviewed writes:

```console
printf 'Synthetic draft body.\n' | \
  owa mail draft \
    --to reader@example.invalid \
    --subject 'Draft example' \
    --body-file -

printf 'Synthetic agenda.\n' | \
  owa calendar create \
    --subject 'Design review' \
    --start 2026-07-20T09:00:00Z \
    --end 2026-07-20T10:00:00Z \
    --required-attendee reader@example.invalid \
    --teams-meeting \
    --body-file -
```

The first call shows the exact normalized review and changes nothing when
approval is required. Repeat the CLI command with `--approve` only after
checking every field. MCP keeps preview and commit as separate tools and binds
the token to the originating process.

## Why this exists

Microsoft Graph remains the right choice when an organization permits app
registration and consent. `owa-bridge` serves the narrower case where it does
not, without asking a user to defeat MFA, Conditional Access, or another
sign-in control.

```text
AI agents ───────── MCP stdio ─┐
                               ├── local IPC ── session owner ── Outlook Web
Humans and scripts ──── CLI ───┘                  │
                                          application / policy
```

The daemon owns the browser session. CLI and MCP calls enter the same typed
application core and the same policy boundary; authorization material never
enters an agent process.

## Design promises

- **Local first.** Mailbox data and authorization are not sent to a
  project-run cloud service.
- **Interactive authentication.** The browser completes normal SSO, MFA, and
  Conditional Access. The CLI never requests a password.
- **One core, two interfaces.** CLI and MCP execute the same typed use cases
  and return the same stable result shapes.
- **Safe by construction.** Reads are metadata-first. Sending and calendar
  changes use exact, caller-bound `preview -> commit` operations.
- **No automatic write retry.** Ambiguous outcomes fail closed to avoid
  duplicate mail, events, or invitations.
- **No ambient network listener.** MCP uses stdio; daemon IPC uses a Unix
  socket or Windows named pipe.

See the [architecture](docs/architecture.md),
[authentication design](docs/authentication.md),
[protocol boundary](docs/protocol.md), [threat model](docs/threat-model.md), and
[compatibility evidence](docs/compatibility.md).

## Current scope

Mail supports folder discovery, metadata list and AQS search, explicit body and
attachment reads, text or HTML drafts and sends, reply/reply-all/forward,
bounded attachments, versioned moves, read/unread updates, and reviewed
permanent deletion.

Calendar supports bounded metadata list, event creation with all-day,
reminder, recurrence, attendees, and optional Teams-link settings. Versioned
updates cover subject, body, time, location, all-day status, reminders, and
complete attendee replacement; cancellation moves an event to Deleted Items.

Explicit shared or delegated mailbox routing is available when the signed-in
user already has access in Outlook Web. Mailbox-rule mutation, recurrence
editing after creation, generic property mutation, delegate-permission
management, general Teams chat, Graph passthrough, hosted relays, unattended
login, and tenant-wide access are out of scope.

## Distribution and development

Releases contain macOS, Linux, and Windows binaries for amd64 and arm64, plus
deb, RPM, and APK packages. Every artifact is covered by SHA-256 checksums and
SPDX and CycloneDX SBOMs; the checksum manifest carries a keyless Sigstore
bundle. The binaries are not yet Apple-notarized or Windows
Authenticode-signed—do not weaken operating-system security controls to run
them.

The complete development toolchain is checksummed and pinned with `mise`:

```console
mise trust
mise install
mise exec -- task verify
mise exec -- task release:snapshot
```

Live mailbox tests are opt-in, never run in the default test command or CI, and
must use authorized data. See [CONTRIBUTING.md](CONTRIBUTING.md) and the
[manual test checklist](docs/manual-test-checklist.md).

## Responsible use

Use `owa-bridge` only with mailboxes you are authorized to access and according
to your organization's policies. The project does not bypass authentication or
grant permissions the signed-in user does not already have in Outlook Web.

`owa-bridge` is independent and is not affiliated with, endorsed by, or
sponsored by Microsoft. Microsoft, Outlook, Microsoft 365, and Teams are
trademarks of the Microsoft group of companies.

Apache-2.0. See [LICENSE](LICENSE).
