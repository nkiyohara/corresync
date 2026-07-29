# Release engineering

Corresync releases are rehearsed, inventory-checked, and signed before
publication. A tag does not turn deterministic provider coverage into live
compatibility evidence.

## Before the release

Confirm that:

- every included issue has implementation, synthetic tests, and public docs;
- `CHANGELOG.md`, `SECURITY.md`, and the compatibility matrix describe the
  intended SemVer line;
- provider capabilities and degradations match CLI, MCP, and stable JSON;
- all accepted architecture changes have an ADR;
- the working tree contains no credentials, personal data, generated live
  captures, or unintended legacy names;
- the final security review has no unresolved high-confidence finding.

Use a clean checkout of the exact candidate commit:

```console
mise install
mise exec -- task verify
mise exec -- task release:check
GORELEASER_CURRENT_TAG=v0.8.0 \
GORELEASER_PREVIOUS_TAG=v0.7.0 \
  mise exec -- task release:snapshot
```

Live mailbox tests remain separate and opt-in. Do not add them to `task verify`
or CI to obtain a release signal.

## Inspect the snapshot

Inspect `dist/artifacts.json`, `dist/checksums.txt`, every archive/package, and
both SBOM formats. A snapshot never signs or publishes.

The two tag overrides make an untagged candidate use the exact intended SemVer
line. Change both values for later releases. Without them, GoReleaser derives a
snapshot version from the most recent local tag, which tests packaging but does
not prove the intended candidate version.

The expected release inventory is:

- six OS/architecture archives and one tagged source archive;
- six native Linux packages;
- SPDX JSON and CycloneDX JSON SBOMs for every archive and package;
- one SHA-256 checksum manifest;
- license and third-party license material;
- README, changelog, security policy, installation/MCP guides, and migration
  guide;
- `corr(1)`, Bash/Zsh/Fish completion, Agent Skill, plugin manifests, and
  marketplace catalogs;
- source-building Homebrew plus Scoop and WinGet manifests.

During the finite v0.8–v0.9 command transition, binary archives and packages
also contain `corresync`, built from exactly the same package, source commit,
version metadata, and flags as `corr`. There is no separate compatibility
manual or completion.

The verifier rejects missing/extra inventory, unsafe archive paths, wrong
binary names, asset names GitHub could rewrite, mismatched versions,
non-reproducible metadata, missing licenses, incomplete SBOMs, and stale
documentation/package payloads.

## Publish a version

1. Merge the narrow, reviewed change through the protected default branch.
2. Confirm the matching `main` CI run is green.
3. Repeat the local clean-checkout rehearsal.
4. Create an annotated tag such as `vX.Y.Z` at that exact `main` commit.
5. Push only the tag.
6. Monitor release verification and package-catalog jobs.
7. Download the published assets and independently verify checksum and
   Sigstore provenance using [install.md](install.md).
8. Confirm the release is not advertised beyond its recorded compatibility
   evidence.

The workflow rejects a tag that is not reachable from `main`. GoReleaser creates
a draft, injects the version/commit/source date, builds with `CGO_ENABLED=0` and
`-trimpath`, then verifies archives, packages, catalogs, licenses, checksums,
and SBOMs before publication.

Only the verified checksum manifest is signed. The GitHub Actions OIDC identity
is bound to the exact repository, release workflow, and tag. Any pre-publish
failure leaves at most a draft; it does not expose an unverified release as
latest.

## Package catalogs

Stable releases update the owned Homebrew and Scoop catalogs and submit WinGet
manifests only after the canonical GitHub release is public. Prereleases do not
enter catalogs. Catalog jobs consume the exact verified manifest bundle from
the release job and may not rebuild or replace release artifacts.

Required secrets:

- `HOMEBREW_TAP_DEPLOY_KEY`, scoped only to
  `nkiyohara/homebrew-corresync`;
- `SCOOP_BUCKET_DEPLOY_KEY`, scoped only to
  `nkiyohara/scoop-corresync`;
- `WINGET_CREATE_GITHUB_TOKEN`, a dedicated classic `public_repo` token for a
  machine account with no broader repository access.

Set `WINGET_PR_AUTHOR` to that machine account's exact login. Duplicate-PR
checks are scoped to the same author/version. A catalog failure does not mutate
the published release; repair the credential/upstream condition and rerun only
the failed job.

Homebrew builds the verified tagged source. Until native binaries are signed
and notarized, do not publish a Cask or instructions that weaken Gatekeeper or
SmartScreen.

## Repository controls

Keep these controls enabled:

- protected `main`, required pull request, and green checks;
- blocked force pushes and branch deletion;
- Dependabot alerts/security updates and private vulnerability reporting;
- least-privilege workflow permissions and immutable action SHA pins;
- `github-pages` deployment limited to the default branch.

The Pages site deploys at its canonical Corresync URL. It intentionally creates
no redirect from the former Pages path.

## Failed or withdrawn release

Do not overwrite a published tag or replace its assets. Correct the defect in a
new SemVer release, mark the affected release appropriately, and document
security/compatibility impact without exposing private reporter or mailbox
data. Package catalogs must converge on the new immutable release rather than a
mutated old one.
