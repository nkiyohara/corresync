# ADR 0011: Coordinated Corresync rename and compatibility window

- Status: accepted; primary-CLI and alias decisions superseded by
  [ADR 0016](0016-short-corr-command.md)
- Date: 2026-07-28

## Context

[ADR 0008](0008-provider-neutral-product-scope.md) accepts a provider-neutral
product, and `owa` names one provider's web client. The replacement name is
Corresync, from correspondence and synchronization.

Renaming this project is not a string substitution. It touches the source
repository, the GitHub Pages base path, the Go module path, the binary name,
release artifacts and their signatures, package-manager identifiers, MCP
registration, update metadata, and local configuration, state, cache,
browser-profile, and IPC paths.

Renaming those surfaces one release at a time produces states worse than either
name alone: a formula pointing at archives that no longer exist, an MCP client
registered to a missing binary, or two configuration trees whose different
absolute paths quietly start two session owners under
[ADR 0003](0003-authenticated-local-session-owner.md). GitHub's redirect after a
repository rename covers clone and issue URLs; it does not migrate package
managers, Pages URLs, update metadata, client configuration, or local paths.

Release provenance was prepared in advance.
[ADR 0007](0007-explicit-verified-self-update.md) already accepts exactly two
enumerated release-workflow identities, so an installed binary can verify and
apply the first renamed release instead of stranding direct installations.

## Decision

Execute the rename as one coordinated migration in a single release. The
canonical target names are:

| Surface | Target |
| --- | --- |
| Product | `Corresync` |
| MCP display name | `Corresync — Mail & Calendar` |
| Source repository | `nkiyohara/corresync` |
| GitHub Pages | `https://corresync.org/` |
| Go module | `github.com/nkiyohara/corresync` |
| Primary CLI | `corresync` |
| MCP connection name | `corresync` |
| MCP Registry ID | `io.github.nkiyohara/corresync` |
| Winget ID | `nkiyohara.Corresync` |
| Homebrew install target | `nkiyohara/corresync/corresync` |

One release carries every dependent surface: repository metadata and templates;
the Pages base path with its canonical URLs, sitemap, assets, and inbound links;
module and import paths, build metadata, user agent, protocol diagnostics, and
the update checker; release archives, checksums, SBOMs, and Sigstore bundles;
package-manager manifests and identifiers; service definitions, desktop entries,
completions, and manpages; and MCP server metadata, plugin and Skill manifests,
setup commands, and client configuration examples.

Local state migrates in the same release. Configuration, cache, and log
directories are copied into the Corresync locations and the legacy tree is left
intact so a downgrade remains possible. Browser profiles and any other session
material are moved rather than copied, because two readable copies of an
authenticated profile is a worse outcome than one.

Because the daemon namespace derives from absolute configuration and state
paths, migration explicitly stops the legacy session owner, draining active
calls and closing its browsers, before the Corresync owner starts. That stop
discards in-memory authorization and pending previews exactly as any version
replacement does. The relocated browser profile may still hold a valid browser
session, so the next command can often re-establish authorization without a full
interactive sign-in. That is an expected outcome, not a guarantee, and a
re-authentication prompt is a normal result of the migration.

The canonical release does not publish legacy command, directory, plugin,
Skill, completion, manpage, package, or MCP connection aliases. Compatibility
is instead a finite set of migration-only inputs: the old default config and
state locations, environment overrides, daemon namespace and request host,
package-install detection, release artifact fallback, and release-workflow
identity in ADR 0007. A legacy location is read only when its Corresync
counterpart is absent. These inputs are exact constants rather than a pattern
match, and they are not shown in ordinary help or generated configuration.

The v0.6.2 bridge release precedes the rename. It can verify both release
workflow identities and extract the canonical artifact, preventing a direct
v0.6 installation from becoming stranded when the repository and assets move.
The v0.7 migration guide is the only user-facing document that names the old
commands and paths.

The compatibility window includes the complete v0.7 and v0.8 release lines.
Migration-only legacy inputs and the old release-workflow identity may be
removed no earlier than v0.9.0, after at least one stable release has announced
their removal. Historical documentation and wire identifiers that still
describe the Outlook Web provider are not compatibility aliases and remain
when technically accurate.

The release must include an end-to-end upgrade test from the last `owa-bridge`
release to the first Corresync release, and one migration guide covering
repository redirects, Pages URLs, package-manager changes, configuration
migration, compatibility inputs, and rollback.

## Consequences

A single large release is harder to review and to revert than a sequence of
small ones, and it temporarily doubles the accepted provenance identities and
migration inputs. That is still better than a
multi-release window in which published artifacts, documentation, and the
installed binary disagree about the product's name.

Users who follow the migration guide keep their configuration and normally
keep their authenticated browser profile. Existing client registrations and
package identifiers must be replaced because no permanent public alias surface
is created.

The provider-neutral name does not by itself add a provider. What ships on the
day of the rename is the same Outlook Web capability under a new name, and
documentation must keep saying so; see
[ADR 0008](0008-provider-neutral-product-scope.md).
