# Install and verify

Corresync 0.7 is an early release over undocumented Outlook Web contracts.
Use only an authorized account and review the
[compatibility evidence](compatibility.md) before enabling writes.

## Release targets

Each release contains one native `corresync` executable plus the license,
security policy, manual, shell completions, and essential documentation.

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
brew install nkiyohara/corresync/corresync

# Windows with Scoop
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync

# Windows Package Manager
winget install --id nkiyohara.Corresync --exact
```

Homebrew builds the tagged source locally instead of downloading an
unnotarized macOS binary. Scoop and WinGet install the exact Windows release
archive recorded in the catalog. If a newly published version has not reached
a catalog yet, use the signed GitHub release directly.

### Direct release download

Use the release page in a browser or GitHub CLI. For example:

```console
VERSION=v0.7.0
mkdir corresync-release
gh release download "$VERSION" \
  --repo nkiyohara/corresync \
  --dir corresync-release
cd corresync-release
```

Choose the archive matching `darwin`, `linux`, or `windows` and `amd64` or
`arm64`. Extract it, place `corresync` or `corresync.exe` on `PATH`, and run
`corresync version --json` to record the version, source commit, build time, Go
version, operating system, and architecture. Use `corresync --version` for a
conventional one-line check.

On Linux, download the matching native package when preferred:

```console
gh release download "$VERSION" \
  --repo nkiyohara/corresync \
  --pattern '*.deb'
sudo apt install ./corresync_*.deb
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
WORKFLOW_ID="https://github.com/nkiyohara/corresync/"
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
corresync config init
corresync config validate
corresync doctor
```

Edit only the configured account alias and final HTTPS Outlook origin used
after sign-in. `corresync config edit` validates changes before replacing the
file; typed automation can use `corresync config get` and
`corresync config set`. `corresync auth login` opens a dedicated browser
profile. Sign-in, MFA, and Conditional Access remain inside that browser; the
CLI does not accept a password or persist an authorization header.

```console
corresync auth login
corresync auth status
corresync doctor --online
```

For an interactive SSH session without a display server, the experimental
`corresync auth login --terminal` command can relay ordinary text-based browser
controls through the TTY. CAPTCHA, passkeys, security keys, client
certificates, and native dialogs may still require visible login.

## Shell completion and manual

Homebrew metadata and native deb, RPM, and APK packages install `corresync(1)`
plus Bash, Zsh, and Fish completions into platform-standard locations. Archive
users can detect and install completion without repeatedly changing a shell
startup file:

```console
corresync completion install
```

The command recognizes Bash, Zsh, and Fish from `SHELL`, accepts
`--shell bash|zsh|fish` as an override, and is idempotent when the installed
file is already current. A different existing file is preserved unless
`--force` is explicit. Zsh prints one `fpath` instruction when its install
directory is not already active.

Generated scripts also remain available for temporary use:

```console
source <(corresync completion bash)
source <(corresync completion zsh)
corresync completion fish | source
```

Persist only the command appropriate for the current shell. Completion derives
commands, flags, and enum values from the same CLI model and does not contact
Outlook.

## Configure an MCP client

After initializing `corresync`, register the client you use with one command:

```console
corresync mcp setup codex
# or: corresync mcp setup claude-code
# or: corresync mcp setup github-copilot
# or: corresync mcp setup gemini-cli
# or: corresync mcp setup qwen-code
# or: corresync mcp setup qoder
```

Start a new agent session, then ask it to check Outlook without naming a tool.
Use `--dry-run` to inspect the exact process invocation first. Scope flags are
available for Claude Code, Gemini CLI, Qwen Code, and Qoder. Setup delegates to
the installed client's official command and does not rewrite unrelated
settings.

For offline review, Kimi Code CLI, project configuration, or advanced client
settings, print the client's native document:

```console
corresync mcp config codex
corresync mcp config claude-code
corresync mcp config github-copilot
corresync mcp config gemini-cli
corresync mcp config qwen-code
corresync mcp config qoder
corresync mcp config kimi-code
```

The default connection name is `corresync`. See [MCP integration](mcp.md) for
Agent Skill installation, verification commands, and troubleshooting. Read
[interactive authentication](authentication.md) before the first login. For
an existing v0.6 installation, follow the
[v0.7 migration guide](migration-v0.7.md).

## Stay current

Released binaries check the latest stable public release at interactive CLI
startup and display a quiet notice when an update is available. Apply the
appropriate update path with one command:

```console
corresync update
```

For check-only automation or diagnostics:

```console
corresync update check
corresync update check --json
```

The startup check is read-only. A success or failure is cached in the private
state directory for 24 hours. Network failure never fails an Outlook
operation, and automatic notices never enter MCP stdio, generated completions,
daemon output, pipes, or any command using `--json`.

The request is an unauthenticated `GET` for the repository's public latest
release metadata. It sends the Corresync version as its user agent and sends
no mailbox, account, tenant, configuration, or machine identifier. Disable
automatic checks while retaining the explicit command with either:

```toml
[updates]
disable_automatic_checks = true
```

```console
export CORRESYNC_NO_UPDATE_CHECK=1
```

When a newer stable version exists, the hint follows the detected installation
surface:

<!-- markdownlint-disable MD013 -->

| Installation | Suggested action |
| --- | --- |
| Homebrew | `brew upgrade nkiyohara/corresync/corresync` |
| WinGet | `winget upgrade --id nkiyohara.Corresync --exact` |
| Scoop | `scoop update corresync` |
| deb, RPM, APK | Download and verify the new native package, then install it with the matching package manager |
| Direct archive | `corresync update` |

<!-- markdownlint-enable MD013 -->

`corresync update` never modifies files owned by a package manager; it prints
the command above instead. For a direct archive it refreshes stable metadata,
verifies the exact release workflow's Sigstore identity, signed checksum,
candidate version, OS, and architecture, then performs a rollback-capable
replacement. The previous executable is retained beside the installation as
`corresync.backup-VERSION` or `corresync.exe.backup-VERSION`. Background checks
never replace a binary. The explicit direct path contacts the GitHub release
endpoints and Sigstore's public TUF service, sending only component user-agent
strings—never Outlook, account, tenant, configuration, or machine identity.

## Package catalogs

GitHub releases remain canonical. Every release renders and verifies a
source-building Homebrew Formula, Scoop manifest, and WinGet manifest from the
same checksum inventory. Dedicated catalog repositories consume those
manifests only after the release is public; they never rebuild or replace an
existing release artifact. WinGet updates additionally pass Microsoft's
upstream review.
