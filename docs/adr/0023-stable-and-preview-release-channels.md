# ADR 0023: Stable and preview release channels

- Status: accepted
- Date: 2026-08-03

## Context

Corresync originally consumed only GitHub's latest stable release. That kept
package distribution simple but gave willing users no coherent way to test a
release candidate through the same verified self-update path. An informal
prerelease URL would bypass update caching, selection, and ownership guidance.

Release channels must not weaken provenance checks, silently broaden automatic
installation consent, mix a standalone executable with package-manager-owned
files, or turn a channel change into a downgrade.

## Decision

Expose exactly two channels through the secret-free configuration:

- `stable`, the default, selects published non-prerelease SemVer tags;
- `preview`, an explicit opt-in, selects the highest published stable or
  prerelease SemVer tag.

Preview tags use `vX.Y.Z-{alpha,beta,rc}.N`. Both channels use the same release
workflow, artifact inventory, Apple signing and notarization, SBOM and license
gates, signed checksum manifest, Sigstore workflow identity, archive checksum,
candidate version, operating-system, and architecture verification. Drafts,
malformed tags, and GitHub prerelease flags inconsistent with the tag are
ineligible.

Checks and verified direct self-update read `updates.channel`. Cache files are
isolated by channel. `updates.auto_install` remains separate, default-off
consent and never changes when the channel changes. Version comparison follows
SemVer precedence and every update path refuses a downgrade. Thus a preview
user can move to a newer final release, while switching back to stable waits if
the installed preview is newer than the current stable release.

Homebrew, Scoop, WinGet, deb, RPM, and APK catalogs remain stable-only. A
package-managed installation may report a preview release and its canonical
URL, but Corresync neither emits a package-manager command that cannot install
that version nor replaces the managed executable with a standalone artifact.

The configuration schema advances from v4 to v5. Migration sets the channel to
`stable` and preserves both automatic-check and automatic-install consent
exactly.

## Consequences

Early adopters get one reversible configuration switch and the same verified
update path as stable users. Stable remains unsurprising for every existing
user. Maintainers publish preview artifacts without mutating downstream
catalogs, and promotion to a final release remains an ordinary higher SemVer
selection.

GitHub's recent-release endpoint is larger than the stable latest-release
endpoint, so preview discovery reads at most four fixed pages of five releases.
Each page is independently size-bounded and the result remains capped at the
same 20 recent releases and cached for 24 hours. This avoids making one JSON
response grow with every SBOM-bearing release while retaining deterministic
SemVer selection across the bounded window. Preview is a testing channel, not
a nightly channel; arbitrary branch builds and unsigned development artifacts
remain ineligible.
