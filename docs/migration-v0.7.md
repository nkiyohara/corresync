# Migrate to Corresync 0.7

Corresync 0.7 is the coordinated rename release. The repository, executable,
Go module, release assets, package catalogs, configuration and state
directories, MCP identity, plugin, Skill, completions, and manual all use the
Corresync name.

The upgrade preserves account routing and normally preserves the dedicated
browser profile. It does not preserve old command or MCP registration names as
new public interfaces.

## Before the rename

Upgrade a direct v0.6 installation to v0.6.2 first. That bridge release trusts
both the old and Corresync release-workflow identities and recognizes the new
artifact name:

```console
owa update
```

Package-manager users can go directly to the matching Corresync package below.
Finish or abandon pending preview tokens before upgrading; daemon replacement
intentionally invalidates them.

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
