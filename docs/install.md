# Install and verify

The package is Corresync and the primary executable is `corr`. Releases in the
finite v0.8–v0.9 command-transition window also contain the identical
`corresync` compatibility executable; scripts and new documentation should use
`corr`.

## Install now

### Linux: one command, no sudo

```console
curl -LsSf https://nkiyohara.github.io/corresync/install.sh | sh
```

The standalone installer supports Linux amd64 and arm64. It:

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
curl -LsSf https://nkiyohara.github.io/corresync/install.sh | less
```

The Pages script is a mutable bootstrap delivered over HTTPS. For a
commit-bound, high-assurance installation, download a tagged release and
perform the checksum and Sigstore verification below before extraction.

Pin a version, choose a different absolute destination, or leave shell profiles
unchanged:

```console
CORRESYNC_VERSION=v0.8.0 \
  CORRESYNC_NO_PATH_UPDATE=1 \
  CORRESYNC_INSTALL_DIR="$HOME/bin" \
  sh -c 'curl -LsSf https://nkiyohara.github.io/corresync/install.sh | sh'
```

### macOS

```console
brew install nkiyohara/corresync/corresync
corr --version
```

### Windows

```console
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync
corr --version
```

## Release targets

<!-- markdownlint-disable MD013 -->
| Operating system | Architectures | Artifacts |
| --- | --- | --- |
| macOS | amd64, arm64 | `.tar.gz` |
| Linux | amd64, arm64 | `.tar.gz`, `.deb`, `.rpm`, `.apk` |
| Windows | amd64, arm64 | `.zip` |
<!-- markdownlint-enable MD013 -->

Every archive includes the license, security policy, changelog, essential
guides, `corr(1)`, Bash/Zsh/Fish completion, plugin metadata, the Agent Skill,
and required third-party licenses. Each archive and Linux package has SPDX JSON
and CycloneDX JSON SBOMs.

## Package managers

```console
# Homebrew on macOS or Linux
brew install nkiyohara/corresync/corresync

# Scoop on Windows
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync

# WinGet after its manifest has passed Microsoft review
winget install --id nkiyohara.Corresync --exact
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

macOS binaries are not yet notarized and Windows binaries are not yet
Authenticode-signed. Do not disable Gatekeeper, SmartScreen, or organization
policy to run them. If platform signing is required, build from the verified
tag or wait for a signed distribution.

## First run

```console
corr config init
corr config validate
corr account list
corr doctor
```

The default account uses Outlook Web. Sign in visibly:

```console
corr auth login
corr auth status
corr doctor --online
```

The browser owns sign-in, MFA, Conditional Access, and session cookies.
Corresync never asks for a password or copies an authorization header into its
configuration. Online doctor reuses that authenticated session and never starts
login or OAuth itself.

For another provider:

```console
corr account discover reader@example.invalid
corr account add --help
```

Discovery is credential-free. Review its candidates and choose one explicitly.
Google API and Microsoft Graph require a registered public OAuth client and an
OS-keyring authorization handle. Managed accounts can explicitly choose the
read-only `google-web` route and authenticate in a visible browser without an
OAuth client. JMAP, IMAP/SMTP, and CalDAV use an OS-keyring entry or an
explicitly approved credential helper. See
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

An interactive released CLI may perform a quiet public stable-release check,
cached locally for 24 hours. It sends the Corresync version as a user agent and
no account, tenant, config, mailbox, or machine identifier. It is disabled for
MCP, daemon, completion, feedback, pipes, and JSON output.

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
never replaced by Corresync. A direct install verifies release identity,
checksums, version, OS, and architecture before replacement and retains a
rollback copy beside the executable. Background checks never modify a binary.

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
