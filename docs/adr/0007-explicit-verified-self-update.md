# ADR 0007: Explicit verified self-update

- Status: accepted
- Date: 2026-07-24
- Amended: 2026-07-28

## Context

ADR-independent release checking originally stopped after comparing public
stable-release metadata. Package-manager users received an exact upgrade
command, while direct-archive users had to download, verify, extract, and
replace the executable manually. That manual path is secure when followed
carefully but is easy to perform incompletely. The original `owa update` name also
reasonably suggests a user-initiated update rather than a command group that
only contains `owa update check`.

Background replacement remains inappropriate. A normal Outlook command must
not gain local filesystem write authority merely because a public endpoint
reports a newer version. Package-manager files must remain owned by their
package manager, and machine-readable or MCP output must never receive terminal
decoration or update progress.

## Decision

Keep startup update discovery read-only and add an explicit `corr update`
action. It performs one fresh stable-release check. Homebrew, WinGet, Scoop,
deb, RPM, and APK installations are never modified; the command displays and
returns the exact package-manager action instead.

For a direct installation, `corr update` may replace only the running regular
file, never a symlink. It:

1. accepts only the exact stable GitHub release and matching OS/architecture
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

Development builds, prereleases, downgrades, incomplete release inventories,
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

Direct users get the short, memorable path `corr update` without giving
background checks write authority. Managed users can use the same entry point
to discover the owner-specific command without risking mixed ownership.

The release binary and dependency review surface grow because provenance
verification is now self-contained instead of requiring an external `cosign`
process. The normal verification, vulnerability, license, SBOM, and
reproducibility gates cover those dependencies.

An interrupted successful replacement can leave the documented backup file,
and Windows may retain an old executable while the current process exits.
Both are preferable to silently deleting the rollback copy. A running older
session owner is still drained by the authenticated version-replacement flow
on the next Outlook command, as defined by ADR 0003.
