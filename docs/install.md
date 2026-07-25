# Install and verify

`owa-bridge` 0.6 is an early release over undocumented Outlook Web contracts.
Use only an authorized account and review the
[compatibility evidence](compatibility.md) before enabling writes.

## Release targets

Each release contains one native `owa` executable plus the license, security
policy, manual, shell completions, and essential documentation.

| Operating system | Architecture | Artifacts |
| --- | --- | --- |
| macOS | Intel, Apple silicon | `.tar.gz` |
| Linux | amd64, arm64 | `.tar.gz`, `.deb`, `.rpm`, `.apk` |
| Windows | amd64, arm64 | `.zip` |

Release assets include SHA-256 checksums and SPDX JSON and CycloneDX JSON SBOMs
for every archive and Linux package. Each archive and package includes the
third-party license material required by its linked dependencies.

## Download

Install from a package catalog when available:

```console
# macOS or Linux (source-building Formula)
brew install nkiyohara/owa-bridge/owa-bridge

# Windows with Scoop
scoop bucket add owa-bridge https://github.com/nkiyohara/scoop-owa-bridge
scoop install owa-bridge/owa-bridge

# Windows Package Manager
winget install --id nkiyohara.OWABridge --exact
```

Homebrew builds the tagged source locally instead of downloading an
unnotarized macOS binary. Scoop and WinGet install the exact Windows release
archive recorded in the catalog. If a newly published version has not reached
a catalog yet, use the signed GitHub release directly.

### Direct release download

Use the release page in a browser or GitHub CLI. For example:

```console
VERSION=v0.6.1
mkdir owa-release
gh release download "$VERSION" \
  --repo nkiyohara/owa-bridge \
  --dir owa-release
cd owa-release
```

Choose the archive matching `darwin`, `linux`, or `windows` and `amd64` or
`arm64`. Extract it, place `owa` or `owa.exe` on `PATH`, and run
`owa version --json` to record the version, source commit, build time, Go
version, operating system, and architecture. Use `owa --version` for a
conventional one-line check.

On Linux, download the matching native package when preferred:

```console
gh release download "$VERSION" \
  --repo nkiyohara/owa-bridge \
  --pattern '*.deb'
sudo apt install ./owa-bridge_*.deb
```

Use the matching `.rpm` with `dnf install` or `.apk` with `apk add`. Review and
verify the package before invoking a privileged package manager.

## Verify checksums and provenance

Verify downloaded release assets before extracting or installing them:

```console
# Linux
sha256sum --ignore-missing --check checksums.txt

# macOS
shasum -a 256 --check checksums.txt
```

The release workflow signs `checksums.txt` with GitHub Actions keyless Sigstore
identity after verifying the complete artifact inventory. Verify the bundle
against the exact repository workflow:

```console
WORKFLOW_ID="https://github.com/nkiyohara/owa-bridge/"
WORKFLOW_ID="${WORKFLOW_ID}.github/workflows/release.yml@refs/tags/${VERSION}"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "$WORKFLOW_ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

The binaries are not yet Apple-notarized or Windows Authenticode-signed. Do not
disable or weaken operating-system protection merely to run a download. If
local policy requires a platform signature, inspect and build from the tagged
source or wait for a future signed distribution.

## First run

Create and validate the secret-free local configuration before starting the
browser session owner:

```console
owa config init
owa config validate
owa doctor
```

Edit only the configured account alias and final HTTPS Outlook origin used
after sign-in. `owa config edit` validates changes before replacing the file;
typed automation can use `owa config get` and `owa config set`. `owa auth
login` opens a dedicated browser profile. Sign-in, MFA, and Conditional Access
remain inside that browser; the CLI does not accept a password or persist an
authorization header.

```console
owa auth login
owa auth status
owa doctor --online
```

For an interactive SSH session without a display server, the experimental
`owa auth login --terminal` command can relay ordinary text-based browser controls
through the TTY. CAPTCHA, passkeys, security keys, client certificates, and
native dialogs may still require visible login.

## Shell completion and manual

Homebrew metadata and native deb, RPM, and APK packages install `owa(1)` plus
Bash, Zsh, and Fish completions into platform-standard locations. Archive users
can activate a relocatable generated script directly:

```console
source <(owa completion bash)
source <(owa completion zsh)
owa completion fish | source
```

Persist only the command appropriate for the current shell. Completion derives
commands, flags, and enum values from the same CLI model and does not contact
Outlook.

## Configure an MCP client

After initializing `owa`, register the client you use with one command:

```console
owa mcp setup codex
# or: owa mcp setup claude-code
# or: owa mcp setup github-copilot
# or: owa mcp setup gemini-cli
# or: owa mcp setup qwen-code
# or: owa mcp setup qoder
```

Start a new agent session, then ask it to check Outlook without naming a tool.
Use `--dry-run` to inspect the exact process invocation first. Scope flags are
available for Claude Code, Gemini CLI, Qwen Code, and Qoder. Setup delegates to
the installed client's official command and does not rewrite unrelated
settings.

For offline review, Kimi Code CLI, project configuration, or advanced client
settings, print the client's native document:

```console
owa mcp config codex
owa mcp config claude-code
owa mcp config github-copilot
owa mcp config gemini-cli
owa mcp config qwen-code
owa mcp config qoder
owa mcp config kimi-code
```

The default connection name is `outlook-web`. See [MCP integration](mcp.md)
for Agent Skill installation, verification commands, migration from `owa`, and
troubleshooting. Read [interactive authentication](authentication.md) before
the first login.

## Stay current

Released binaries check the latest stable public release at interactive CLI
startup and display a quiet notice when an update is available. Apply the
appropriate update path with one command:

```console
owa update
```

For check-only automation or diagnostics:

```console
owa update check
owa update check --json
```

The startup check is read-only. A success or failure is cached in the private
state directory for 24 hours. Network failure never fails an Outlook
operation, and automatic notices never enter MCP stdio, generated completions,
daemon output, pipes, or any command using `--json`.

The request is an unauthenticated `GET` for the repository's public latest
release metadata. It sends the owa-bridge version as its user agent and sends
no mailbox, account, tenant, configuration, or machine identifier. Disable
automatic checks while retaining the explicit command with either:

```toml
[updates]
disable_automatic_checks = true
```

```console
export OWA_NO_UPDATE_CHECK=1
```

When a newer stable version exists, the hint follows the detected installation
surface:

<!-- markdownlint-disable MD013 -->

| Installation | Suggested action |
| --- | --- |
| Homebrew | `brew upgrade owa-bridge` |
| WinGet | `winget upgrade --id nkiyohara.OWABridge --exact` |
| Scoop | `scoop update owa-bridge` |
| deb, RPM, APK | Download and verify the new native package, then install it with the matching package manager |
| Direct archive | `owa update` |

<!-- markdownlint-enable MD013 -->

`owa update` never modifies files owned by a package manager; it prints the
command above instead. For a direct archive it refreshes stable metadata,
verifies the exact release workflow's Sigstore identity, signed checksum,
candidate version, OS, and architecture, then performs a rollback-capable
replacement. The previous executable is retained beside the installation as
`owa.backup-VERSION` or `owa.exe.backup-VERSION`. Background checks never
replace a binary. The explicit direct path contacts the GitHub release
endpoints and Sigstore's public TUF service, sending only component user-agent
strings—never Outlook, account, tenant, configuration, or machine identity.

## Package catalogs

GitHub releases remain canonical. Every release renders and verifies a
source-building Homebrew Formula, Scoop manifest, and WinGet manifest from the
same checksum inventory. Dedicated catalog repositories consume those
manifests only after the release is public; they never rebuild or replace an
existing release artifact. WinGet updates additionally pass Microsoft's
upstream review.
