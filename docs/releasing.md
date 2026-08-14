# Release engineering

Corresync releases are rehearsed, inventory-checked, and signed before
publication. A tag does not turn deterministic provider coverage into live
compatibility evidence.

## Release lines

`main` is the integration branch for the next minor release. While v0.9 is in
development, `release/0.8` is the protected maintenance branch for v0.8 stable
and preview releases. Short-lived feature branches target `main`; only bug,
security, compatibility, documentation, and release-engineering fixes for
already shipped behavior target `release/0.8`.

This is a finite transition, not a second long-term support line. Publish v0.8
releases from the maintenance branch while v0.9 develops concurrently on
`main`. Once v0.9.0 is published as stable, freeze `release/0.8`, stop routine
v0.8 releases, and update `SECURITY.md` so users are directed to v0.9. All new
development and releases then continue from `main` on the v0.9 line. The
release workflow enforces this cutoff: after the stable `v0.9.0` tag exists, it
rejects every new `v0.8.*` tag. Release lines earlier than v0.8 are closed.

Develop a fix against the oldest affected supported line. After it is reviewed
and merged there, forward-port the exact change to `main` through a separate
pull request, normally with `git cherry-pick -x`. Never merge `main` into a
maintenance branch, and do not add a new command, MCP tool, schema, provider,
or capability to v0.8.

The release workflow binds each tag to its protected source branch:

| Tag line | Required source branch |
| --- | --- |
| `v0.8.*` | `release/0.8` |
| `v0.9.*` and later | `main` |

Tags are immutable. A maintenance release is therefore built from the exact
reviewed maintenance commit without pulling unfinished next-minor work into the
binary. Public Pages continue to deploy from `main`; any unreleased feature on
the site must be explicitly described as upcoming and must not be presented as
stable or live-observed.

## Before the release

Confirm that:

- the [versioning policy](adr/0020-public-and-local-versioning.md) classifies
  every public, durable, and daemon-protocol change, and the oldest supported
  fixtures pass against the candidate;
- every included issue has implementation, synthetic tests, and public docs;
- `CHANGELOG.md`, `SECURITY.md`, and the compatibility matrix describe the
  intended SemVer line;
- the public homepage, Privacy Policy, Terms of Use, and any provider consent
  notice still match the shipped data flows and requested OAuth scopes;
- release archives and packages contain the project license and generated
  third-party license materials, and license/SBOM checks pass;
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
- one platform-universal MCPB containing the six verified primary binaries;
- one MCP Registry `server.json` bound to the MCPB's exact version and SHA-256;
- SPDX JSON and CycloneDX JSON SBOMs for every archive, package, and MCPB;
- one SHA-256 checksum manifest;
- license and third-party license material;
- README, changelog, security policy, installation/MCP guides, and migration
  guide;
- `corr(1)`, Bash/Zsh/Fish completion, Agent Skill, generated Codex/OpenAI and
  Claude/Copilot/VS Code plugins, Gemini extension, Kiro Power, config-only
  integration metadata, generated publication-channel matrix, and marketplace
  catalogs;
- source-building Homebrew plus Scoop and WinGet manifests.

During the finite v0.8–v0.9 command transition, binary archives and packages
also contain `corresync`, built from exactly the same package, source commit,
version metadata, and flags as `corr`. There is no separate compatibility
manual or completion.

The release hook renders every integration package from the canonical bundle
specification using the exact stable or preview tag. Checked-in manifests are
reviewable source snapshots, not the release version input. The verifier
rejects missing/extra inventory, unsafe archive paths, wrong
binary names, MCPB path or launcher drift, a bundled binary that differs from
its verified release input, a registry manifest not bound to that MCPB hash,
asset names GitHub could rewrite, mismatched versions, non-reproducible
metadata, missing licenses, incomplete SBOMs, and stale
documentation/package payloads.

## Publish a version

1. Merge the narrow, reviewed change through the protected source branch for
   the intended release line.
2. Confirm the matching source-branch CI run is green.
3. Repeat the local clean-checkout rehearsal.
4. Create an annotated stable tag `vX.Y.Z`, or a preview tag
   `vX.Y.Z-{alpha,beta,rc}.N`, at that exact source-branch commit.
5. Push only the tag.
6. Monitor release verification, MCP Registry publication, and package-catalog
   jobs.
7. Download the published assets and independently verify checksum and
   Sigstore provenance using [install.md](install.md).
8. Confirm the release is not advertised beyond its recorded compatibility
   evidence.

The workflow rejects a tag that is not reachable from the branch assigned to
its release line or does not match one of those two channel formats. GoReleaser
creates a draft, injects the version/commit/source date, builds with
`CGO_ENABLED=0` and `-trimpath`, then an isolated macOS keychain Developer
ID-signs all four Darwin executables with hardened runtime and secure timestamp.
Apple notarization must accept those exact binaries before the Darwin archives
are repacked. The MCPB, Darwin SBOMs, catalogs, and checksum inventory are then
rebuilt from the signed inputs. The release gate verifies archives, packages,
the MCPB, catalogs, licenses, checksums, and SBOMs before publication.

Stable and preview releases pass the same archive, license, SBOM, macOS
signing/notarization, checksum, and provenance gates. Only the verified
checksum manifest is signed. The GitHub Actions OIDC identity
is bound to the exact repository, release workflow, and tag. Any pre-publish
failure leaves at most a draft; it does not expose an unverified release as
latest.

Apple release credentials are repository-scoped Actions Secrets restored from
the maintainer's private password manager:

- `MACOS_SIGN_P12`, the base64-encoded encrypted Developer ID PKCS#12 bundle;
- `MACOS_SIGN_PASSWORD`, the PKCS#12 password;
- `MACOS_NOTARY_APPLE_ID`, `MACOS_NOTARY_PASSWORD`, and `MACOS_TEAM_ID`, used
  only to create an ephemeral keychain profile for `notarytool`.

Do not place these values in repository variables, workflow artifacts, release
assets, logs, caches, or local environment files. The release job imports them
only after source verification and deletes its temporary keychain even on
failure. Rotate notarization credentials independently where possible; verify a
replacement release path before revoking the prior Developer ID certificate.

## Official MCP Registry

Only a stable `vX.Y.Z` tag can publish to the official MCP Registry. Preview
and RC releases still build and verify `server.json` and the MCPB, but the
publication job is absent from their execution path. This keeps immutable
stable directory versions free of preview entries.

After `sign-release` makes the GitHub release public, the Registry job:

1. downloads `server.json`, the MCPB, both MCPB SBOMs, `checksums.txt`, and its
   Sigstore bundle from that public release;
2. verifies the checksum signature against the exact release workflow and tag
   identity, then checks every downloaded publication input against the signed
   inventory;
3. validates the exact downloaded `server.json` with the pinned
   `mcp-publisher` `v1.8.1`; its Linux archive is pinned by SHA-256 as well as
   version;
4. checks the immutable version endpoint first, so a retry skips an identical
   record but fails closed if an existing record differs;
5. when absent, exchanges GitHub Actions OIDC for the
   `io.github.nkiyohara/*` namespace authorization and publishes without a
   stored Registry token;
6. polls the fixed production Registry endpoint until the name, version,
   repository, MCPB URL and hash, `active` status, and `isLatest` flag exactly
   match the verified release;
7. uploads `mcp-registry-publication.json`, containing only the source commit,
   tag, target, public URLs, artifact hashes, status, and timestamps.

The job never rebuilds or replaces a release artifact. If publication or
post-verification fails, rerun only that failed job: it re-downloads and
re-verifies the same public assets. A pre-existing exact version is treated as
an idempotent retry; any immutable-version mismatch stops without publishing.
The generated [publication matrix](generated/publication-channels.md) remains
the canonical checklist for directory owners, supported local surfaces,
publication methods, visible versions, verification links, and reload
behavior. A source package is not an upstream marketplace listing.

## Package catalogs

Stable releases update the owned Homebrew and Scoop catalogs and submit WinGet
manifests only after the canonical GitHub release is public. Preview releases
remain GitHub prereleases and do not enter catalogs. Catalog jobs consume the
exact verified manifest bundle from
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

Homebrew builds the stable tagged source. Do not publish a Cask unless it
installs the exact signed and notarized release archive and passes a
quarantine-path Gatekeeper test. Never add instructions that weaken Gatekeeper
or SmartScreen.

## Repository controls

Keep these controls enabled:

- protected `main`, required pull request, and green checks;
- blocked force pushes and branch deletion;
- Dependabot alerts/security updates and private vulnerability reporting;
- least-privilege workflow permissions and immutable action SHA pins;
- `github-pages` deployment limited to the default branch.

The Pages site deploys at its canonical Corresync URL. It intentionally creates
no redirect from the former Pages path. Its user-facing HTML, one-line
macOS/Linux and Windows installers, social preview, internal links, structured
metadata, canonical URLs, Privacy Policy, Terms of Use, robots policy, and
sitemap are one reviewed artifact. Run
`go run ./tools/siteverify` before deployment.

## Failed or withdrawn release

Do not overwrite a published tag or replace its assets. Correct the defect in a
new SemVer release, mark the affected release appropriately, and document
security/compatibility impact without exposing private reporter or mailbox
data. Package catalogs must converge on the new immutable release rather than a
mutated old one.
