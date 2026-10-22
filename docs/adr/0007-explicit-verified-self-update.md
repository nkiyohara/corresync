# ADR 0007: Verified self-update and coordinated daemon replacement

- Status: accepted
- Date: 2026-07-24
- Amended: 2026-10-22

## Context

ADR-independent release checking originally stopped after comparing public
stable-release metadata. Package-manager users received an exact upgrade
command, while direct-archive users had to download, verify, extract, and
replace the executable manually. That manual path is secure when followed
carefully but is easy to perform incompletely. The original `owa update` name
also
reasonably suggests a user-initiated update rather than a command group that
only contains `owa update check`.

New standalone installations should receive verified security and maintenance
updates without requiring a separate opt-in. Existing saved preferences must
remain authoritative, and package-manager files must remain owned by their
package manager. Replacing only the executable leaves old CLI and daemon
images running, so installation must coordinate their lifecycle as well.

## Decision

From 0.9.2, newly generated configurations enable `updates.auto_install`.
Existing configurations retain their saved value; omitted fields in existing
files retain their historical false value. Before configuration exists, an
eligible interactive start uses the new default. `corr config set
updates.auto_install false` disables installation, and
`CORRESYNC_NO_UPDATE_CHECK=1` disables startup checking and installation.

Keep the explicit `corr update` action. Both paths use the configured channel
and the same verifier. Homebrew, WinGet, Scoop, deb, RPM, and APK installations
receive their package-manager command, never self-replacement. Preview
availability shows the direct release URL because catalogs remain stable-only.

Eligible interactive CLI starts, including bare `corr`, use the bounded,
24-hour-cached check. MCP, daemon, completion, feedback, configuration,
machine-readable, piped, and non-interactive paths never trigger installation.
Configuration commands therefore remain available to revoke consent.

An executable-scoped sidecar lock serializes direct installers. Only after
verification and permission checks may the updater stop the authenticated,
config-scoped owner, using a generation-bound shutdown request. It requires
confirmed release of the singleton lock before replacing the binary; ambiguity
or a timeout defers installation. Multiple owners require explicit repair.
An existing owner is restarted from the installed absolute path after either
successful replacement or rollback. Restarting never initiates provider login.
Automatic success runs the original command once in the updated executable,
with a one-process update-check opt-out, preserving streams and exit status.
It never executes the original operation on the old image afterward. Failure
before replacement permits the original command to continue; failure after
replacement stops the CLI with an explicit recovery error.

Daemons from 0.9.2 poll their original executable's identity every five seconds.
Replacement retires even owners belonging to other configuration files, without
enumerating unrelated processes. Retirement refuses new requests, drains
admitted requests, then closes browser state before releasing the singleton
listener. The next provider operation starts the current owner. The owner in
the updating command's configuration is restarted immediately. Existing
pre-0.9.2 owners retain their earlier shutdown behavior until replaced.
Stale CLI instances refuse to replace a newer-version owner or start an owner
from an executable generation that changed after their startup.

For a direct installation, either the explicit command or the opted-in startup
path may replace only the running regular file, never a symlink. It:

1. accepts only the exact selected GitHub release and matching OS/architecture
   archive from a bounded HTTPS asset allowlist;
2. downloads a size-bounded checksum manifest, Sigstore bundle, and archive
   into an owner-only temporary directory;
3. refreshes the public Sigstore trust root through TUF and verifies the bundle
   transparency entry, observer timestamp, embedded certificate-transparency
   proof, OIDC issuer, and exact tagged release-workflow identity;
4. verifies the archive SHA-256 from that signed manifest;
5. extracts only the bounded regular `corr` or `corr.exe` entry, with the
   identical `corresync` entry accepted during ADR 0016's finite command
   transition and the exact older entry accepted only during ADR 0011's
   repository migration window, then runs its content-free version report to
   require the exact release version, operating system, and architecture; and
6. replaces the executable with rollback support while preserving the prior
   version beside it as an explicit backup.

Development builds, downgrades, incomplete release inventories,
existing staging or backup paths, and every failed verification leave the
installed executable unchanged. The implementation uses the official
`sigstore-go` verifier, `minio/selfupdate` for cross-platform rollback, and
TTY-only Lip Gloss styling. `--json`, pipes, MCP, daemon, and completion
surfaces remain stable and unstyled.

During the coordinated repository rename, provenance verification accepts only
two enumerated exact workflow identities: `nkiyohara/owa-bridge` and
`nkiyohara/corresync`, each bound to the requested tag and
`.github/workflows/release.yml`. This finite migration allowlist is not a
repository-name pattern. The legacy identity may be removed only after the
documented compatibility window ends.

## Consequences

Direct users get verified automatic installation by default in new
configurations
and can turn it off. Existing users can opt in with `corr config set
updates.auto_install true`. Managed users retain their owner-specific update
command even with automatic installation enabled.

A busy owner can delay an update. In-flight operations are allowed to finish;
updates do not silently replay writes or restart provider authentication.
Pending previews and sessions may need to be re-established after retirement.

The release binary and dependency review surface grow because provenance
verification is now self-contained instead of requiring an external `cosign`
process. The normal verification, vulnerability, license, SBOM, and
reproducibility gates cover those dependencies.

An interrupted successful replacement can leave the documented backup file,
and Windows may retain an old executable while the current process exits.
Both are preferable to silently deleting the rollback copy. The authenticated
owner replacement flow remains config-scoped, as defined
by ADR 0003.
