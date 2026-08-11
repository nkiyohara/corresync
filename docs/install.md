# Install and verify

The package is Corresync and the primary executable is `corr`. Releases in the
finite v0.8–v0.9 command-transition window also contain the identical
`corresync` compatibility executable; scripts and new documentation should use
`corr`.

## Install now

### macOS and Linux: one command, no sudo

```console
curl -LsSf https://corresync.org/install.sh | sh
```

The shell installer supports macOS and Linux on amd64 and arm64. It:

- resolves only a stable tag from the canonical GitHub repository;
- downloads the exact archive, checksum inventory, and Sigstore bundle over
  HTTPS;
- requires the archive's one exact SHA-256 entry;
- verifies the exact GitHub Actions workflow identity when `cosign` is already
  available, or reports clearly that provenance was not checked;
- bounds archive size, entry count, extraction, candidate output, and time;
- validates candidate version, operating system, and architecture;
- installs `corr` and the v0.8-v0.9 `corresync` compatibility entry in
  `~/.local/bin` through a rollback-capable same-filesystem transaction.

If `~/.local/bin` is not already on `PATH`, the installer adds one marked,
idempotent line to the current Bash, Zsh, or POSIX profile when that profile is
a regular user-owned file that is not group- or world-writable. It never
follows or edits a symlinked profile. Open a new terminal after a PATH update.

The installer does not create configuration, read a credential, sign in,
connect a provider, start a daemon, or register an MCP client.

Review the exact script before running it:

```console
curl -LsSf https://corresync.org/install.sh | less
```

Each Pages installer is a mutable bootstrap delivered over HTTPS. For a
commit-bound, high-assurance installation, download a tagged release and
perform the checksum and Sigstore verification below before extraction.

Pin a version, choose a different absolute destination, or leave shell profiles
unchanged:

```console
CORRESYNC_VERSION=v0.8.0 \
  CORRESYNC_NO_PATH_UPDATE=1 \
  CORRESYNC_INSTALL_DIR="$HOME/bin" \
  sh -c 'curl -LsSf https://corresync.org/install.sh | sh'
```

### Windows PowerShell: one command, no elevation

```powershell
powershell -NoProfile -Command "irm https://corresync.org/install.ps1 | iex"
```

The PowerShell installer supports Windows amd64 and arm64. It follows the same
stable-tag, checksum, optional Sigstore, candidate validation, and rollback
contract as the shell installer. Redirects remain HTTPS-only and limited to
the canonical repository and GitHub release-asset host. ZIP metadata, expanded
size, entry names, executable size, candidate output, and execution time are
bounded before installation.

The default destination is `%USERPROFILE%\.local\bin`. The directory and any
existing target must be local, current-user-owned, non-reparse paths without
broad write access. If necessary, the installer adds the directory once to the
current user's `PATH`; it never changes the machine `PATH` or requests
administrator rights. Open a new terminal after a PATH update.

Review the exact PowerShell script before running it:

```powershell
powershell -NoProfile -Command "irm https://corresync.org/install.ps1 | more"
```

Pin a version, select another local destination, or leave `PATH` unchanged:

```powershell
$env:CORRESYNC_VERSION = "v0.8.0"
$env:CORRESYNC_NO_PATH_UPDATE = "1"
$env:CORRESYNC_INSTALL_DIR = "$HOME\bin"
irm https://corresync.org/install.ps1 | iex
```

Neither standalone installer creates configuration, reads a credential, signs
in, connects a provider, starts a daemon, or registers an MCP client.

### Other installation methods

Use a package manager when you prefer it to own upgrades and removal:

```console
# Homebrew on macOS or Linux
brew install nkiyohara/corresync/corresync

# WinGet on Windows
winget install --id nkiyohara.Corresync --exact

# Scoop on Windows
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
```

Linux releases also include `.deb`, `.rpm`, and `.apk` packages. Every platform
has a direct archive for commit-bound manual verification. Package-manager
installs stay owned by that manager; Corresync reports its exact upgrade command
instead of replacing the managed executable.

## Release targets

<!-- markdownlint-disable MD013 -->
| Operating system | Architectures | Artifacts |
| --- | --- | --- |
| macOS | amd64, arm64 | `.tar.gz`, platform-universal `.mcpb` |
| Linux | amd64, arm64 | `.tar.gz`, `.deb`, `.rpm`, `.apk`, platform-universal `.mcpb` |
| Windows | amd64, arm64 | `.zip`, platform-universal `.mcpb` |
<!-- markdownlint-enable MD013 -->

Every archive includes the license, security policy, changelog, essential
guides, `corr(1)`, Bash/Zsh/Fish completion, plugin metadata, the Agent Skill,
and required third-party licenses. The MCPB contains the exact same six primary
release binaries, chooses only the current OS/architecture from a fixed local
launcher, and includes the project and third-party licenses. Each archive,
Linux package, and MCPB has SPDX JSON and CycloneDX JSON SBOMs.

## Claude Desktop MCP Bundle

Install the CLI with one of the methods above, then configure and authenticate
the exact accounts you control:

```console
corr setup you@example.com --alias personal
corr auth login --account personal
```

Download `corresync_VERSION.mcpb` from the
[matching GitHub release](https://github.com/nkiyohara/corresync/releases),
verify it through `checksums.txt` and the Sigstore procedure below, then open
the file or drag it into Claude Desktop. The installation UI shows the bundle
identity before installing it for the current user.

MCPB changes distribution, not the trust boundary. Claude Desktop starts the
bundled `corr mcp serve` on the user's device over stdio. The bundle defines no
credential fields, HTTP transport, remote endpoint, hosted relay, or Docker
runtime. Account setup and authentication remain separate explicit CLI actions.

## Package managers

```console
# Homebrew on macOS or Linux
brew install nkiyohara/corresync/corresync

# WinGet on Windows
winget install --id nkiyohara.Corresync --exact

# Scoop on Windows
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
```

Homebrew builds the tagged source. Scoop and WinGet install checksum-pinned
Windows release archives. Catalog publication follows the canonical GitHub
release, so a new version can briefly be available only as a direct download.

## Direct download

```console
VERSION=vX.Y.Z
RELEASE="${VERSION#v}"
mkdir corresync-release
gh release download "$VERSION" \
  --repo nkiyohara/corresync \
  --dir corresync-release
cd corresync-release
```

Select `corresync_${RELEASE}_{darwin|linux}_{amd64|arm64}.tar.gz` or the
equivalent Windows `.zip`. Extract it, place `corr` or `corr.exe` on `PATH`,
then check:

```console
corr --version
corr version --json
```

Linux users can instead install the verified native package:

```console
# Debian or Ubuntu
sudo apt install ./corresync_*_amd64.deb

# Fedora or another RPM distribution
sudo dnf install ./corresync-*.x86_64.rpm

# Alpine
sudo apk add ./corresync_*-r1_x86_64.apk
```

Review a package before passing it to a privileged package manager.

## Verify checksums and provenance

Verify downloads before extraction or installation:

```console
# Linux
sha256sum --ignore-missing --check checksums.txt

# macOS
shasum -a 256 --check checksums.txt
```

The release workflow signs the verified `checksums.txt` with GitHub Actions
keyless Sigstore identity:

```console
WORKFLOW_ID="https://github.com/nkiyohara/corresync/"
WORKFLOW_ID="${WORKFLOW_ID}.github/workflows/release.yml@refs/tags/${VERSION}"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "$WORKFLOW_ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

The identity must match the repository, release workflow, and selected tag
exactly. Do not accept a wildcard identity.

The macOS binaries in v0.8.4 and later are signed with the Corresync
maintainer's Apple Developer ID, hardened runtime, and secure timestamp, and
Apple notarization must be accepted before the release can publish. Check an
extracted binary without changing Gatekeeper:

```console
codesign --verify --strict --verbose=2 corr
codesign --display --verbose=4 corr 2>&1 |
  grep -E 'Authority=Developer ID|TeamIdentifier=N2D7Q889MA'
```

Apple issues an online notarization ticket for each standalone command-line
binary; standalone executables cannot carry a stapled ticket. The signed
Darwin binaries in the direct archives and MCPB are byte-identical to the
notarized release inputs.

Windows binaries are not yet Authenticode-signed. Do not disable Gatekeeper,
SmartScreen, or organization policy to run a release.

## First run

```console
corr setup you@example.com --alias personal
corr auth login --account personal
corr auth status
corr doctor --account personal
```

Replace the example address and alias. `setup` creates a provider-neutral local
configuration, performs credential-free discovery, and adds an automatically
selectable first-party route. It never starts authentication. The following
`auth login` is explicit and account-specific.

The browser owns sign-in, MFA, Conditional Access, and session cookies.
Corresync never asks for a password or copies an authorization header into its
configuration. Online doctor reuses that authenticated session and never starts
login or OAuth itself.

If automatic selection is unavailable, or you want an API or standards route:

```console
corr account discover reader@example.invalid
corr account add reader@example.invalid --help
```

Discovery is credential-free. Review its candidates and choose one explicitly.
Microsoft Graph requires a registered public OAuth client and an OS-keyring
authorization handle. The Gmail and Google Calendar route is included but
disabled in RC builds while production OAuth approval is pending, so Google
account addition and sign-in stop before any credential or network access.
After a separate activation release, Gmail and Calendar will use pinned Google
APIs. Workspace administrators may still require approval or block access. A
generated Google Desktop client may also require its client credential in the
local `CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` process environment; never put it in
configuration or command arguments. JMAP, IMAP/SMTP, and CalDAV use an
OS-keyring entry or an explicitly approved credential helper. See
[configuration.md](configuration.md) and
[authentication.md](authentication.md).

## Completion and manual

Native Linux packages and Homebrew install `corr(1)` and completion in standard
locations. Archive users can install completion idempotently:

```console
corr completion install
```

The shell is detected from `SHELL`; use `--shell bash|zsh|fish` to override.
Re-running with current contents is a no-op. A different regular file is
preserved unless `--force` is explicit, and symlinks are rejected.

Temporary completion remains available:

```console
source <(corr completion bash)
source <(corr completion zsh)
corr completion fish | source
```

## Connect an MCP client

```console
corr mcp setup codex
# or: claude-code, github-copilot, gemini-cli, qwen-code, or qoder
```

Use `--dry-run` to inspect an official client registration command. Kimi Code
CLI and generic clients can use:

```console
corr mcp config kimi-code
corr mcp serve
```

See [mcp.md](mcp.md) for client scopes, verification, and the Agent Skill.

## Stay current

```console
corr update check
corr update
```

An interactive released CLI may perform a quiet public release check for the
configured `stable` or `preview` channel, cached locally for 24 hours. It sends
the Corresync version as a user agent and
no account, tenant, config, mailbox, or machine identifier. It is disabled for
MCP, configuration management, daemon, completion, feedback, pipes, and JSON
output.

Standalone installs can opt in to applying a verified update at the start of
an eligible interactive command:

```console
corr config set updates.auto_install true
```

Stable is the default channel. Standalone users can opt in to signed
prereleases and switch back without allowing a downgrade:

```console
corr config set updates.channel preview
corr update check
corr update
corr config set updates.channel stable
```

The current command continues in the process version that was already loaded;
the replacement is active on the next `corr` start. Automatic update failure
does not block the requested command.

Disable automatic checks while keeping explicit update commands:

```toml
[updates]
disable_automatic_checks = true
```

or:

```console
export CORRESYNC_NO_UPDATE_CHECK=1
```

Package-managed binaries print the exact owner-specific update command and are
never replaced by Corresync. Package catalogs remain stable-only; preview
availability points to the signed direct release instead of suggesting a
package-manager command. A direct install verifies release identity,
checksums, version, OS, and architecture before replacement and retains a
rollback copy beside the executable. The default startup check never modifies a
binary. Even with `auto_install` enabled, Corresync never invokes Homebrew,
Scoop, WinGet, APT, DNF, or APK.

If a direct v0.7 `corresync update` reached v0.8.0 but did not create the new
`corr` filename, either run the Linux standalone installer above or rerun:

```console
corresync update
```

An updater containing the migration repair verifies the exact current release,
installs the missing sibling `corr`, and leaves the existing compatibility
command unchanged. If `corr` already exists, it becomes the direct update
owner; use `corr update` so two independently updated executable paths cannot
drift.

The updater knows the former repository and archive names only as a finite
v0.6 migration input. New releases require canonical `corresync_*` assets.

## Upgrade from v0.6

Install the v0.6.2 bridge before crossing the repository and asset rename, then
follow [migration-v0.7.md](migration-v0.7.md). The current application never
uses old command, config, state, or IPC names as canonical output.
