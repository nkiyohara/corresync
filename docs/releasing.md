# Release engineering

Releases are reproducible, verified, and signed before publication. A tag alone
is not a support claim; [SECURITY.md](../SECURITY.md) defines supported version
lines and [compatibility evidence](compatibility.md) records live observations.

## Local rehearsal

Use the checksummed toolchain and build all six target archives, one tagged
source archive, six native Linux packages, and both SBOM formats without
contacting GitHub:

```console
mise install
mise exec -- task verify
mise exec -- task release:check
mise exec -- task release:snapshot
```

Inspect `dist/artifacts.json`, `dist/checksums.txt`, each archive and package,
and the SPDX and CycloneDX documents. A snapshot never publishes or signs
artifacts.

The verifier rejects incomplete inventories and asset names GitHub would
rewrite. The pinned license tool rejects incompatible or unknown linked
dependency licenses and materializes required third-party license content. The
release wrapper normalizes Syft timestamps, document IDs, and temporary paths
so SBOMs are reproducible from the source commit.

## Publishing a version

1. Confirm `mise exec -- task verify` and
   `mise exec -- task release:snapshot` pass on a clean checkout.
2. Confirm the matching `main` CI run is green.
3. Confirm `SECURITY.md`, compatibility evidence, and release notes match the
   intended SemVer support level.
4. Create an annotated tag such as `v0.1.0` on the exact `main` commit.
5. Push only that tag and monitor the release and package-catalog jobs.
6. Download the published assets and independently verify the checksum and
   Sigstore bundle using [install.md](install.md).

The workflow rejects a tag not reachable from `main`. GoReleaser first creates
a draft and injects the version, commit, and source commit date. It builds with
`CGO_ENABLED=0`, `-trimpath`, and no VCS path leakage; produces macOS, Linux,
and Windows archives for amd64 and arm64; and creates deb, RPM, and APK packages
without installing a system service.

While the release is still a draft, the workflow verifies every archive,
package, checksum, catalog manifest, license bundle, and SBOM. It then signs the
verified checksum manifest with the workflow's GitHub OIDC identity, attaches
the Sigstore bundle, and publishes the draft. Any earlier failure leaves only a
draft and never exposes an unverified release as latest.

## Repository gates

The public repository should keep these controls enabled:

- required pull request and green CI checks for changes to `main`;
- blocked force pushes and branch deletion;
- Dependabot alerts and security updates;
- GitHub private vulnerability reporting;
- least-privilege workflow permissions and immutable action SHA pins;
- the `github-pages` deployment environment limited to the default branch.

Repository rules protect collaboration, but the release workflow still checks
tag ancestry and artifact contents independently. Do not weaken a local,
protocol, security, or compatibility gate to publish on schedule.

## Release inventory

The verifier requires:

- six OS/architecture archives and one tagged source archive;
- six native Linux packages;
- SPDX JSON and CycloneDX JSON SBOMs for every archive and package;
- one SHA-256 manifest;
- license, README, security policy, installation and MCP guides, manual page,
  all three completion scripts, Agent Skill, dual client plugin manifests, and
  marketplace catalogs inside each applicable artifact;
- a source-building Homebrew Formula plus Scoop and WinGet manifests that
  reference the same verified source or binary artifacts.

The Sigstore bundle is attached after this inventory passes and therefore is
not itself listed inside `checksums.txt`.

## Package catalogs

Catalog publication runs only after a stable release is public. The release
job renders, verifies, and preserves all three formats as a short-lived Actions
artifact. Follow-up jobs consume that exact artifact: they push the Homebrew
Formula and Scoop manifest to their dedicated catalogs and submit the WinGet
manifests for upstream review. Prereleases never enter a package catalog.

Configure these repository secrets before publishing a stable tag:

- `HOMEBREW_TAP_DEPLOY_KEY`: private half of a write-enabled deploy key scoped
  only to `nkiyohara/homebrew-corresync`.
- `SCOOP_BUCKET_DEPLOY_KEY`: private half of a write-enabled deploy key scoped
  only to `nkiyohara/scoop-corresync`.
- `WINGET_CREATE_GITHUB_TOKEN`: dedicated classic GitHub token with only the
  `public_repo` scope. WinGetCreate does not support fine-grained tokens.

The owned catalogs update idempotently and run their own installation tests on
push. WinGet remains available only after Microsoft's validation and review of
the submitted pull request. A rerun skips submission when the same version's
pull request is already open. A later release stops instead of opening a second
pull request until the prior one is merged or closed. A catalog-publication
failure does not mutate or replace the already verified GitHub release; fix the
credential or upstream condition and rerun the failed job.

Homebrew intentionally builds the tagged source archive. Until macOS binaries
are signed and notarized, the project does not publish a binary Cask that would
require users to weaken Gatekeeper. None of the catalog paths may rebuild or
replace an existing GitHub release.
