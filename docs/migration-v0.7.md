# Migrate to Corresync 0.7

Corresync 0.7 is the coordinated rename release. The repository, executable,
Go module, release assets, package catalogs, configuration and state
directories, MCP identity, plugin, Skill, completions, and manual all use the
Corresync name.

The upgrade preserves account routing and normally preserves the dedicated
browser profile. It does not preserve old command or MCP registration names as
new public interfaces.

## Before the rename

Package-manager users can install the renamed package directly. A direct
v0.6.2 installation can use the built-in updater:

```console
owa update
```

Finish or abandon pending preview tokens before upgrading; daemon replacement
intentionally invalidates them.

## From v0.6.1

Do not run `owa update` directly from v0.6.1. That updater reads the latest
v0.7 release number but constructs the removed
`owa-bridge_0.7.0_<os>_<arch>` archive name. It also trusts only the exact
pre-rename Sigstore workflow identity, so adding an unsigned legacy-named asset
to v0.7 would still fail closed at provenance verification.

Install the signed v0.6.2 bridge first. For example, on Linux amd64:

```console
mkdir corresync-bridge
cd corresync-bridge

gh release download v0.6.2 \
  --repo nkiyohara/corresync \
  --pattern checksums.txt \
  --pattern checksums.txt.sigstore.json \
  --pattern owa-bridge_0.6.2_linux_amd64.tar.gz

WORKFLOW_ID="https://github.com/nkiyohara/owa-bridge/"
WORKFLOW_ID="${WORKFLOW_ID}.github/workflows/release.yml@refs/tags/v0.6.2"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "$WORKFLOW_ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

sha256sum --ignore-missing --check checksums.txt
tar -xzf owa-bridge_0.6.2_linux_amd64.tar.gz
./owa --version
```

Use `darwin_amd64`, `darwin_arm64`, `linux_arm64`, `windows_amd64.zip`, or
`windows_arm64.zip` for another release target. On macOS, replace
`sha256sum` with `shasum -a 256`.

After verification, update the extracted bridge copy. This leaves the installed
v0.6.1 executable untouched:

```console
./owa update
./owa --version
mv ./owa ./corresync
```

The first version check above must report v0.6.2; the second must report
Corresync v0.7. Its updater accepts the canonical archive and both exact
workflow identities. Move the resulting `corresync` executable to a private
directory on `PATH`, preserving the installed v0.6.1 file until first-run
migration succeeds. Then continue with
[local-data migration](#let-corresync-migrate-local-data).

Alternatively, verify and install the canonical v0.7 archive directly using
[install.md](install.md#direct-release-download). Corresync itself performs
the same config and state migration on first use; the bridge is required only
for an in-place update through the old command.

## Install the renamed package

Choose one installation owner:

```console
# Homebrew
brew uninstall owa-bridge
brew install nkiyohara/corresync/corresync

# Scoop
scoop uninstall owa-bridge
scoop bucket rm owa-bridge
scoop bucket add corresync https://github.com/nkiyohara/scoop-corresync
scoop install corresync/corresync

# WinGet
winget uninstall --id nkiyohara.OWABridge --exact
winget install --id nkiyohara.Corresync --exact

# Debian or Ubuntu
sudo apt remove owa-bridge

# Fedora or RHEL
sudo dnf remove owa-bridge

# Alpine
sudo apk del owa-bridge
```

Then install the Corresync `.deb`, `.rpm`, or `.apk` from the matching release
when using a native Linux package. For a direct archive, extract the new
release and put `corresync` or `corresync.exe` on `PATH`. If v0.6.2 updated an
existing executable in place, rename that file to the canonical executable
name after it exits.

Confirm that the canonical command resolves before removing any rollback copy:

```console
corresync --version
```

## Let Corresync migrate local data

Run a normal command without an explicit config path:

```console
corresync config validate
corresync auth status
```

On first use, Corresync:

1. reads the v0.6 version-1 config only when no Corresync config exists;
2. writes a version-2 config with a stable opaque ID and provider ID for every
   account, leaving the old config byte-for-byte unchanged;
3. stops the old authenticated session owner before touching browser state;
4. copies rollback-safe state such as content-free audit and update metadata;
5. never copies IPC credentials; and
6. moves each browser profile into the stable account-ID namespace so two
   readable authenticated copies do not exist.

The profile move is atomic and must stay on one filesystem. A fresh interactive
sign-in is a normal outcome if the browser or tenant invalidates the relocated
profile.

Explicit custom config or state paths are never guessed or moved. Continue to
pass `--config`, set `CORRESYNC_CONFIG`, or set `CORRESYNC_STATE_DIR` as
appropriate.

## Re-register MCP and completion

Existing MCP documents may contain both an old connection label and an absolute
path to the removed executable. Register the canonical connection again, verify
it, then remove the stale client entry:

```console
corresync mcp setup codex
corresync completion install
```

Replace `codex` with the client in use. The default MCP connection is
`corresync`; the Registry ID is `io.github.nkiyohara/corresync`. Re-running
completion installation is idempotent and does not append shell startup lines.

## Repository and Pages

The existing GitHub repository is renamed to
`https://github.com/nkiyohara/corresync`, preserving issues, pull requests,
stars, releases, and Git redirects. Update the local remote explicitly:

```console
git remote set-url origin https://github.com/nkiyohara/corresync.git
```

Pages moves to `https://nkiyohara.github.io/corresync/`. There is deliberately
no Pages redirect; update bookmarks and documentation links.

## Roll back

Stop Corresync before rollback:

```console
corresync daemon stop
```

The original v0.6 config remains unchanged, but authenticated browser profiles
were moved rather than duplicated. A rollback can therefore require signing in
again. Do not copy a live profile between the old and new state trees, and
never run both daemon generations against the same explicit state directory.
