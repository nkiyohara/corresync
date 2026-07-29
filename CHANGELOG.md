# Changelog

All notable user-facing changes are recorded here. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Installation and user documentation

- Add a no-sudo Linux installer at
  `https://nkiyohara.github.io/corresync/install.sh`. It selects the latest
  stable amd64/arm64 archive, requires the exact checksum entry, verifies the
  release workflow identity when `cosign` is available, validates candidate
  version/OS/architecture, and atomically installs `corr` plus the finite-window
  `corresync` compatibility command in a user-owned directory.
- Repair direct v0.7-to-v0.8 command migration: running `corresync update` now
  installs a missing sibling `corr` from the same verified release, including
  when the compatibility binary is already at the latest stable version.
- Rebuild README and the multipage site around an MCP-first user journey with
  copyable per-OS installation, internal getting-started/features/provider/
  safety guides, accessible command copying, responsive presentation, unique
  search metadata, an exact sitemap, and an original social preview card.
- Add pinned `shfmt` and ShellCheck formatting/lint gates plus deterministic
  installer tests for fresh install, idempotent PATH setup, v0.7 recovery,
  custom paths, and fail-closed checksum handling.

## 0.8.0 - 2026-07-29

### Accounts and providers

- Add atomic account discovery/add/rename/remove with stable opaque identities,
  explicit mail/calendar routes, account-local state, and credential-free
  discovery.
- Add idempotent account-targeted logout that drains in-flight operations,
  cancels account-local monitors and previews, closes only that account's
  provider sessions, and preserves every other account and the daemon.
- Keep independently authorized mail and calendar providers isolated within
  each account and across accounts, including hybrid Google/Graph routes with
  service-minimal OAuth scopes and provider-qualified result provenance.
- Offer Microsoft Graph as a credential-free discovery candidate for known
  Microsoft domains and Microsoft-hosted MX records, while requiring explicit
  selection before any OAuth authorization begins.
- Add a read-only MCP `account_status` tool that exposes content-free runtime
  state, separate mail/calendar providers, observed capabilities, and typed
  degradations without opening authentication or reading credentials.
- Add a bounded read-only Google Web adapter plus write-capable Google API,
  Microsoft Graph, JMAP, IMAP/SMTP, and CalDAV adapters alongside Outlook Web,
  with typed capabilities, visible degradations, synthetic contracts, explicit
  selection, and no automatic provider fallback.
- Make calendar create/update/cancel reviews carry the selected route's typed
  attendee-notification and cancellation-disposition semantics instead of
  inheriting Outlook-specific behavior.
- Complete Gmail API read-state, label/folder movement, Trash movement, and
  permanent deletion after exact source-version revalidation. The permanent
  deletion contract requires an explicit `https://mail.google.com/` grant and
  therefore forces fresh consent instead of silently reusing an older grant.
- Complete Graph reply, reply-all, forward, and message move through
  source-version revalidation followed by the provider's typed action and
  response-draft flow.
- Preserve provider-derived Reply-To semantics and remove reply-all duplicates
  across To/Cc for Gmail, Graph, JMAP, IMAP/SMTP, and Outlook Web.
- Assemble Graph file attachments through the created draft's attachment
  collection for new, reply, reply-all, and forward flows, refresh the final
  draft identity before returning it, and report any partial draft as an
  outcome requiring reconciliation rather than risking a duplicate send.
- Report Gmail Trash-to-label moves and Graph/JMAP draft-then-submit failures
  as partial outcomes requiring reconciliation after any confirmed first
  stage, and never retry them automatically.
- Traverse Microsoft Graph mail-folder hierarchies through bounded child-folder
  collections and origin-checked provider continuation URLs.
- Add Microsoft Graph v1.0 permanent message deletion after exact source-version
  revalidation, using the delegated account's confirmed immutable user identity
  and a single outcome-safe provider action.
- Expand CalDAV recurrence safely whether a server returns expanded instances
  or an unexpanded recurrence master, with unique instance identities and
  bounded local expansion.
- Keep CalDAV recurring-instance updates and cancellation scoped through
  `RECURRENCE-ID` exceptions and master `EXDATE` updates instead of mutating or
  deleting the complete series.
- Detect RFC 6638 server-managed CalDAV scheduling and protect attendee
  invitation, update, and cancellation writes with schedule-tag conditions;
  unsupported servers now fail before silently changing an attendee event.
- Add recurrence replacement and removal to the shared CLI/MCP calendar update
  contract and implement it for Outlook Web, Google Calendar, Microsoft Graph,
  and CalDAV. Recurrence removal no longer requires an unrelated start/end
  rewrite.
- Add provider-neutral online-meeting creation: Microsoft routes provision
  Teams, while Google Calendar requests a unique Google Meet conference only
  after observing `hangoutsMeet` support. Keep the v0.7 Teams-only input as a
  compatibility alias that fails on non-Microsoft routes.
- Preserve short bounded Google Web result snapshots in cross-account mail
  search instead of misclassifying their honest non-terminal marker as a
  provider failure.
- Fail closed when Gmail or Google Calendar exposes neither recognized semantic
  rows nor a structural empty-state marker, and collect every UTC date in a
  bounded multi-day Google Web agenda window with deterministic deduplication.
- Discover bounded Outlook Web calendar hierarchies through the existing typed
  `FindFolder` action, filter exact calendar folder classes, expose observed
  effective rights, and keep the distinguished calendar as the stable default.
- Save successful SMTP submissions to the discovered IMAP Sent mailbox and
  return its resolvable message identity; fail before SMTP when no Sent mailbox
  exists, and report an unknown partial outcome if post-submit append fails.
- Add a targeted IMAP move fallback using UID COPY, deleted marking, and UID
  EXPUNGE only when UIDPLUS makes it safe for the selected message.
- Treat IMAP APPEND/STORE confirmation failures and JMAP attachment-upload
  followed by draft rejection as partial outcomes requiring reconciliation.
- Add interactive public-client OAuth with PKCE and OS-keyring grants for
  Google/Graph, plus approved external credential handles for standards
  providers. No password, token, helper output, or client secret enters config.

### Read, import, and monitoring workflows

- Add isolated cross-account mail search and agenda projections with stable
  ordering, provenance, global bounds, and explicit partial failures.
- Add read-only, identity-bound local import scanning/staging for recognized
  exports, archives, Maildir, and Thunderbird profiles, plus safe account-local
  purge.
- Add opt-in monitoring modes (`off -> notify -> queue -> agent`) with durable
  recovery/deduplication, quiet hours, bounds, loop prevention, rate limits,
  circuit breaking, direct no-shell runners, and separate remote-egress
  approval.
- Add read-only MCP monitor/event resources and tools; monitoring setup, runner
  consent, egress approval, and queue purge remain CLI-only.

### Safety and experience

- Make `corr` the primary command while keeping an identical `corresync`
  compatibility entry for the finite v0.8–v0.9 transition. Completion detection
  installs one idempotent `corr` file and never appends shell startup lines.
- Authenticate and pin Unix runtime directories, locks, sockets, identities,
  and peer UIDs before transmitting the rotating daemon bearer; reject
  symlinks, wrong types/owners/modes, squatters, and replacement races.
- Authenticate the Windows named-pipe owner, server process SID, protected
  DACL, and credential-file owner before transmitting the daemon bearer, and
  explicitly assign both IPC objects to the current user.
- Bind account lifecycle commits to previewed opaque identities even if aliases
  change, including the replacement default during removal; reject alias/ID
  ambiguity, and require explicit local CLI login after an MCP account addition.
- Restrict JMAP API/upload/download URLs to the configured session origin,
  require absolute credential-helper executables, drop malformed inherited IMAP
  reply headers, reject LF-only IMAP control lines, and give each response to
  the pinned IMAP parser once through a forward-only, CPU-bounded capture that
  bounds literals individually and per operation across implicit TLS and
  STARTTLS.
- Route MCP account add/rename/remove through effect policy and content-free
  prepare/commit/execution audit; show exact credential handles in add review
  and reject cross-account handle reuse before preview and atomic persistence.
- Keep attacker-controlled notification metadata behind native argument
  boundaries, escape Linux notification markup, reject NUL metadata, time-bound
  notification utilities, persist notification events in a delivery-bound
  outbox, advance provider cursors monotonically across deferrals and failures,
  drain pending deliveries before new scan commits, expire terminal events
  without evicting pending data or their dedup identities, bound matched-only
  deduplication with safe oldest-first eviction, paginate recovery by returned
  item count, distinguish provider-attested mailbox end from incomplete bounded
  recovery, preserve valid cursors across empty listings and ACKs that race
  delivery completion, and reject Windows `notify` setup until Corresync has a
  registered AppUserModelID.
- Reject plain and percent-encoded dot path segments at the shared authorized
  REST boundary, with provider ID validation as a second guard, so opaque
  provider identifiers cannot be normalized into a different collection path.
- Add `corr feedback`, an allowlisted deterministic report with an optional
  bounded generalized last-error record. Generation is local; copy/save/open
  actions occur only after full display, and GitHub is never submitted
  automatically.
- Refresh CLI/MCP help, README, Pages, plugin/Skill, feature/evidence tables,
  architecture, security guidance, runbooks, and icon for the implemented
  multi-provider surface.

## 0.7.0 - 2026-07-28

### Corresync

- Rename the product, repository, Go module, executable, release artifacts,
  package catalogs, configuration and state roots, MCP identity, plugin, Skill,
  completions, manual, and Pages site to Corresync in one coordinated release.
- Keep old names only as exact migration inputs for v0.6 config, state, daemon,
  installation detection, release artifacts, and signed workflow identity; the
  canonical release publishes no legacy command or directory aliases.
- Add the v0.6.2 bridge path and a rollback-aware v0.7 migration guide. Config
  v1 is copied to schema v2 while the original remains byte-exact; browser
  profiles move into the stable account-ID namespace after the old daemon
  stops, and IPC credentials are never copied.

### Provider-neutral safety core

- Give every configured account a stable opaque ID and explicit provider ID,
  independent of its editable alias or address.
- Bind every consequential preview and commit digest to one account, provider,
  mailbox or calendar target, and expose capability and provenance metadata
  through the shared application, daemon, CLI, and MCP paths.
- Accept the provider-neutral scope, capability/degradation, account isolation,
  discovery, import, and monitoring architecture while clearly shipping only
  the Outlook Web adapter in this release.
- Advance the private authenticated daemon protocol to version 12 and isolate
  the v0.6 shutdown protocol behind a migration-only client.

### Experience and documentation

- Add `corresync completion install` with Bash, Zsh, and Fish detection,
  idempotent no-op behavior, explicit safe replacement, and symlink rejection.
- Redesign Pages as a static, accessible light/dark site with the Corresync
  icon system, one-core/two-interface mental model, explicit shipped-versus-
  planned scope, and a careful explanation of the no-Graph-app use case.
- Regenerate package manifests, release verification, completions, plugin
  metadata, manuals, examples, and security guidance for the canonical names.

## 0.6.2 - 2026-07-28

### Corresync update bridge

- Preserve verified direct updates across the coordinated repository rename by
  accepting only the exact tagged release-workflow identities for
  `nkiyohara/owa-bridge` and `nkiyohara/corresync`.
- Select signed Corresync release archives and their renamed executable while
  retaining exact legacy-archive support for the compatibility window.
- Keep the existing checksum, transparency-log, platform, version, and
  rollback checks unchanged; the migration uses a finite identity allowlist,
  never a wildcard.

## 0.6.1 - 2026-07-25

### Attachment compatibility

- Use the current Outlook Web `GetAttachment` JSON contract so bounded file
  attachment reads work against Microsoft 365 instead of returning HTTP 500.
- Pin the exact request attachment type in deterministic coverage and record
  the successful live metadata, body, and attachment-content observation.

## 0.6.0 - 2026-07-25

### Complete command-line lifecycle

- Add conventional `--version` and `-V` flags, `owa help <command>`, stable
  usage exit code `2`, and keep the detailed `version --json` build record.
- Add `owa auth login`, content-free `auth status`, and `auth logout`; retain
  top-level `owa login` as a hidden compatibility alias.
- Add validated `config show`, typed `config get` and `config set`, and safe
  `config edit` with atomic replacement only after strict TOML validation.

### Diagnostics and presentation

- Make offline doctor inspect an existing session owner without repairing it,
  report an absent owner as skipped, and fail clearly on an incompatible
  daemon protocol.
- Use one Lip Gloss visual language for root help, version, auth, config,
  doctor, daemon, login, and update output while preserving unstyled pipes and
  JSON and honoring `NO_COLOR` and `TERM=dumb`.
- Advance authenticated local IPC to protocol version 11 for content-free
  session status and cover the expanded lifecycle with deterministic tests.
- Refresh the manual, shell completions, release documentation, and dependency
  metadata.

## 0.5.0 - 2026-07-24

### Verified self-update

- Make `owa update` the default update action: direct installations verify the
  exact tagged GitHub Actions Sigstore identity, signed checksum manifest,
  archive, version, OS, and architecture before rollback-capable replacement.
- Keep package-manager ownership intact and display the exact Homebrew, WinGet,
  Scoop, deb, RPM, or APK upgrade path instead of modifying managed files.
- Present compact styled status and progress on interactive terminals while
  preserving stable, unstyled JSON, MCP, daemon, completion, and piped output.
- Move the quiet cached update notice to CLI startup and consistently direct
  users to `owa update`.
- Remove misleading `upgrade` and `installMethod` fields from current
  `owa update check --json` results and test root-help descriptions against the
  executable command model.

## 0.4.2 - 2026-07-24

### Session owner upgrades

- Detect a session owner left running by an older installed release, drain it
  through the authenticated local control API, and start the current binary
  automatically before handling the requested command.
- Limit cross-version retries to read-only status inspection and graceful
  shutdown after the old daemon proves the original request was rejected.
  Mailbox and calendar operations remain strictly non-retried.
- Verify the exact config digest before automatic replacement so a policy edit
  still requires an explicit `owa daemon stop`.
- Bind replacement shutdown to the rotating credential observed during status
  inspection, preventing a delayed concurrent updater from stopping the new
  owner, and close the old browser before releasing its singleton lock.
- Preserve the final local IPC failure when a detached session owner does not
  become ready instead of reporting only a generic timeout.

## 0.4.1 - 2026-07-22

### Update checking

- Accept the full GitHub latest-release response after the release asset set
  grew beyond the former 64 KiB safety limit.
- Version the private update cache so this fix discards failure records written
  by affected builds instead of replaying them for 24 hours.

### Catalog publication

- Publish each stable release's verified Homebrew and Scoop manifests to their
  dedicated repositories automatically.
- Submit the same verified WinGet manifests for Microsoft's validation and
  review with a checksum-pinned WinGetCreate client.
- Keep catalog credentials least-privilege and skip every catalog for
  prereleases.

## 0.4.0 - 2026-07-22

### Agent discovery

- Add a portable Agent Skill that teaches compatible agents when to use
  Outlook mail and calendar tools, how to stay metadata-first, and how to keep
  reviewed writes explicit.
- Add a polished Codex plugin and repository marketplace plus a
  dual-compatible Claude Code plugin and marketplace.
- Expand MCP server instructions and the three metadata entry-tool
  descriptions with task-oriented discovery guidance.
- Rename the default client connection from `owa` to the clearer
  `outlook-web`; existing registrations and `--name owa` remain supported.

### Client support

- Support seven agent clients: Codex, Claude Code, GitHub Copilot CLI, Gemini
  CLI, Qwen Code, Qoder, and Kimi Code CLI.
- Add official CLI setup for GitHub Copilot CLI, Gemini CLI, Qwen Code, and
  Qoder alongside Codex and Claude Code.
- Add native configuration generators for GitHub Copilot CLI, Gemini CLI,
  Qwen Code, Qoder, and Kimi Code CLI.
- Make every successful setup print its verification command and remind users
  to start a new agent session before asking it to use Outlook.

### Documentation and website

- Rework the README and MCP manual around a three-step install, connect, and
  ask workflow, including Skill installation, migration, and troubleshooting.
- Redesign GitHub Pages with a responsive agent quickstart, supported-client
  overview, capability summary, and safety architecture.
- Include the agent plugin and both marketplace manifests in verified release
  archives and native Linux packages.

### Updates

- Add `owa update check` plus quiet, 24-hour-cached stable-release notices for
  human-facing interactive commands.
- Detect Homebrew, WinGet, Scoop, deb, RPM, APK, and direct installs and print
  the matching upgrade guidance without replacing a binary.
- Keep update notices out of MCP, completion, daemon, and JSON output; cache
  endpoint failure, support config and environment opt-out, and expose a
  non-failing update row in `owa doctor`.

## 0.3.2 - 2026-07-20

### Homebrew

- Install the compiled executable as `owa` instead of inheriting the Formula
  name as the Go build output.

## 0.3.1 - 2026-07-20

### Package catalogs

- Add a source-building Homebrew Formula, a Scoop bucket manifest, and WinGet
  manifests bound to the verified release checksum inventory.
- Preserve catalog metadata separately from release publication so catalog
  updates cannot rebuild or replace a published artifact.
- Add a tagged source archive for Homebrew builds and document package-manager
  installation without asking users to bypass macOS Gatekeeper.

## 0.3.0 - 2026-07-20

### Mail composition and attachments

- Add reviewed reply, reply-all, and forward composition for drafts and sends,
  with exact source message IDs and change keys.
- Add text or HTML bodies plus bounded file attachments whose sizes and SHA-256
  digests are visible in the review.
- Return bounded attachment metadata with body reads and retrieve one explicit
  file attachment through a separate sensitive-read tool.
- Add mandatory destructive preview and commit for one exact message hard
  delete.

### Calendar fields

- Add all-day creation, explicit Exchange/Windows time-zone IDs, reminders,
  and bounded daily, weekly, absolute-monthly, and absolute-yearly recurrence.
- Add versioned updates for all-day status, reminders, and complete required/
  optional attendee-list replacement.

### Mailbox routing and public documentation

- Add explicit shared/delegated mailbox aliases for mailboxes the interactive
  Outlook Web user is already authorized to access.
- Advance local IPC to protocol version 10 and expand the MCP surface to 24
  typed tools.
- Clarify on GitHub Pages and in the README that owa-bridge is a local Outlook
  MCP that does not require a Microsoft Graph app registration or hosted relay.

### Compatibility limits

- Keep new OWA contracts marked deterministic-only until a separately
  authorized live observation is recorded.
- Continue to omit Inbox-rule mutation because Exchange warns that updating
  rules can remove client-only rules, as well as recurrence editing,
  delegate-permission management, and generic property mutation.

## 0.2.0 - 2026-07-19

### Authentication

- Add experimental `owa login --terminal` authentication for interactive SSH
  sessions without a display server.
- Project a bounded text view and numbered controls from a dedicated headless
  Chromium profile over caller-bound, authenticated local IPC.
- Relay one key at a time without accepting piped input or returning complete
  form values; mask sensitive browser fields and support refresh, back, and
  cancellation controls.
- Advance the local IPC protocol to version 8 for terminal-login requests,
  events, input, and cancellation.

### Compatibility evidence

- Observe headless Google Chrome reaching the Microsoft sign-in page on Linux
  amd64, rendering its text and controls, focusing the email field, returning
  with Escape, and cancelling cleanly.
- Keep full authentication, MFA, Conditional Access, and session capture marked
  unobserved because no credentials or MFA values were entered during the live
  check.

### Terminal authentication limits

- CAPTCHA, passkeys, security keys, client certificates, native dialogs, and
  custom graphical authentication may still require a visible browser.

## 0.1.0 - 2026-07-18

Initial public release.

### Mail

- Discover Outlook folders and list or AQS-search bounded message metadata.
- Read one explicit plain-text body through configurable sensitive-read review.
- Save plain-text drafts without sending and send new messages only after an
  exact, caller-bound preview.
- Move one exact message version and set its read or unread state.

### Calendar

- List bounded calendar windows without bodies, attendees, or join URLs.
- Create reviewed appointments and meetings with required and optional
  attendees.
- Ask Outlook to provision a Microsoft Teams join link at event creation.
- Update supported fields or cancel one exact event version with mandatory
  preview and commit.

### Runtime and distribution

- Share the same typed application use cases across the CLI and twenty MCP
  tools for Codex and Claude Code.
- Keep interactive Outlook authentication in a dedicated browser-owned session
  behind authenticated local IPC.
- Ship macOS, Linux, and Windows archives, Linux native packages, SHA-256
  checksums, SPDX and CycloneDX SBOMs, and a Sigstore checksum bundle.
- Deploy concise documentation through GitHub Pages.

### Known limits

- Outlook Web actions are undocumented and can drift between deployments.
- Binaries are not Apple-notarized or Windows Authenticode-signed.
- Reply, forward, HTML composition, attachments, permanent deletion,
  recurrence editing, attendee replacement, and general Teams access are not
  implemented.
