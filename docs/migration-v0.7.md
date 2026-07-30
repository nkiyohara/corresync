# Migrate from the v0.6 Outlook bridge

Corresync 0.7 coordinated the product/repository/package rename. Corresync 0.8
makes `corr` the primary interactive command. Current builds load configuration
as schema v4 with explicit mail/calendar provider routes and the unified
`google` route.

Old names in this guide are migration inputs only. They are not current command
or directory aliases.

## Cross the release-asset rename safely

A direct v0.6.1 updater looks only for an old archive name in the 0.7 release
and therefore reports a missing asset. Published release assets are immutable;
the fix is the v0.6.2 bridge release, whose updater trusts both exact workflow
identities and understands the canonical Corresync archive.

Package-manager users can install the renamed package directly. A direct
v0.6.2 installation can use the built-in updater after finishing or abandoning
pending preview tokens:

```console
owa update
```

Daemon replacement intentionally invalidates pending previews.

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
[local-data migration](#local-data-migration).

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
```

For deb/RPM/APK, remove the former package and install the matching verified
Corresync package. Do not run two package owners for the same executable.

## Adopt the short command

The product, repository, package, MCP connection, plugin, config/state paths,
and release assets remain named Corresync. The primary command is now:

```console
corr --version
corr config validate
corr account list
```

If `corresync --version` reports v0.8 but the `corr` command is missing after a
direct in-place update, the old updater replaced only its own executable path.
On Linux, install both verified command entries with:

```console
curl -LsSf https://corresync.org/install.sh | sh
```

Updated direct installers can also repair the missing primary name without
changing version:

```console
corresync update
```

The repair downloads and verifies the same stable release before creating
`corr`. Package-manager installs and freshly extracted v0.8 archives already
contain both names.

The identical `corresync` executable remains in v0.8 and v0.9 releases only as
a script/update compatibility entry. New scripts, MCP registrations,
completions, examples, and automation should use `corr`. It may be removed no
earlier than v0.10.

There is no current `owa` command or provider-specific public directory alias.

## Local data migration

Run a normal command without an explicit config path:

```console
corr config validate
corr auth status
```

The migration:

1. reads the old config only when no canonical Corresync config exists;
2. preserves every stable opaque account ID;
3. converts schema v1/v2 accounts to schema v4 mail/calendar routes and
   converts schema-v3 `google-api` accounts to the current `google` route;
4. leaves the original config byte-for-byte unchanged;
5. stops the old authenticated session owner before browser-state changes;
6. copies only rollback-safe state such as content-free audit/update metadata;
7. never copies an IPC credential; and
8. moves each browser profile into its stable account-ID namespace so two
   readable authenticated copies do not exist.

A fresh interactive sign-in is normal after a browser-profile move. Explicit
custom config/state paths are never guessed or migrated; continue to pass
`--config`, `CORRESYNC_CONFIG`, or `CORRESYNC_STATE_DIR`.

After migration, review the explicit routes:

```console
corr account show work
corr doctor
corr auth login --account work
```

New provider routes, imports, and monitoring do not turn on automatically.
Monitoring remains `off` for every migrated account.

## Re-register MCP and completion

Client registrations often store an absolute executable path. Register the new
path, verify it in a fresh client session, and remove the stale entry:

```console
corr mcp setup codex
corr completion install
```

Replace `codex` with the client in use. The MCP connection name remains
`corresync` and the Registry ID remains
`io.github.nkiyohara/corresync`.

Completion installation writes one canonical `corr` file. Re-running is
idempotent and does not append startup lines. A different file is preserved
unless `--force` is explicit; symlinks are rejected.

## Repository and Pages

The canonical repository is:

```text
https://github.com/nkiyohara/corresync
```

Update a local remote explicitly:

```console
git remote set-url origin https://github.com/nkiyohara/corresync.git
```

Pages is `https://corresync.org/`. The former GitHub Pages project URL
redirects to this canonical origin, but bookmarks and published links should
still use the custom domain directly.

## Rollback

Stop Corresync before rollback:

```console
corr daemon stop
```

The original v0.6 config remains unchanged, but browser profiles were moved
rather than duplicated. Rollback can require signing in again. Never copy a
live browser profile between state trees or run two daemon generations against
the same explicit state directory.
