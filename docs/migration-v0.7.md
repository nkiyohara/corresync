# Migrate from the v0.6 Outlook bridge

Corresync 0.7 coordinated the product/repository/package rename. The next
release makes `corr` the primary interactive command and upgrades configuration
to schema v3 with explicit mail/calendar provider routes.

Old names in this guide are migration inputs only. They are not current command
or directory aliases.

## Cross the release-asset rename safely

A direct v0.6.1 updater looks only for an old archive name in the 0.7 release
and therefore reports a missing asset. Published release assets are immutable;
the fix is the v0.6.2 bridge release, whose updater trusts both exact workflow
identities and understands the canonical Corresync archive.

Upgrade/install v0.6.2 first, then run its historical update command:

```console
owa update
```

Package-manager users may replace the former package directly:

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
3. converts schema v1/v2 accounts to schema v3 mail/calendar routes;
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

Pages is `https://nkiyohara.github.io/corresync/`. There is deliberately no
Pages redirect; update bookmarks.

## Rollback

Stop Corresync before rollback:

```console
corr daemon stop
```

The original v0.6 config remains unchanged, but browser profiles were moved
rather than duplicated. Rollback can require signing in again. Never copy a
live browser profile between state trees or run two daemon generations against
the same explicit state directory.
