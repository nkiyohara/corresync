# ADR 0007: Verified self-update with explicit automatic opt-in

- Status: accepted
- Date: 2026-07-24
- Amended: 2026-08-03

## Context

ADR-independent release checking originally stopped after comparing public
stable-release metadata. Package-manager users received an exact upgrade
command, while direct-archive users had to download, verify, extract, and
replace the executable manually. That manual path is secure when followed
carefully but is easy to perform incompletely. The original `owa update` name also
reasonably suggests a user-initiated update rather than a command group that
only contains `owa update check`.

Implicit replacement remains inappropriate. A normal provider command must not
gain local filesystem write authority merely because a public endpoint reports
a newer version. An explicit local opt-in can grant that authority to a direct
installation, but package-manager files must remain owned by their package
manager. Machine-readable and MCP paths must never receive terminal decoration,
update progress, or an update attempt.

## Decision

Keep startup update discovery read-only by default and retain the explicit
`corr update` action. It performs one fresh check of the configured release
channel, as refined by
[ADR 0023](0023-stable-and-preview-release-channels.md). Homebrew,
WinGet, Scoop, deb, RPM, and APK installations are never modified; startup
notices and the explicit command display the exact package-manager action for
stable releases. Preview availability displays the direct release URL because
package catalogs remain stable-only.

`updates.auto_install = true` is a separate, default-off consent for a direct
installation to apply an available verified release in that channel before an eligible
interactive CLI command. The check remains cached and bounded. Automatic
installation is excluded entirely from MCP, daemon, completion, feedback,
configuration management, machine-readable, piped, and non-interactive paths;
it cannot run between MCP tool calls or while one is in flight. Excluding
configuration management ensures that a command which revokes automatic-update
consent cannot itself trigger an update first. A failed automatic attempt
leaves the requested command available and prints only a short manual retry
instruction. The already-running process continues on its loaded image, so the
replacement takes effect on the next `corr` start.

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

Direct users get the short, memorable path `corr update` and can separately
consent to verified startup installation. Default checks retain no write
authority. Managed users receive their owner-specific stable command or a
preview release URL without risking mixed ownership, even if automatic
installation is enabled in configuration.

The release binary and dependency review surface grow because provenance
verification is now self-contained instead of requiring an external `cosign`
process. The normal verification, vulnerability, license, SBOM, and
reproducibility gates cover those dependencies.

An interrupted successful replacement can leave the documented backup file,
and Windows may retain an old executable while the current process exits.
Both are preferable to silently deleting the rollback copy. A running older
session owner is still drained by the authenticated version-replacement flow
on the next Outlook command, as defined by ADR 0003.
