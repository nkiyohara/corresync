# Compatibility evidence

Compatibility is an evidence claim, not an inference from a fixture or a
provider brand. This page distinguishes deterministic v0.8 coverage from
authorized live observations.

## v0.8 evidence

<!-- markdownlint-disable MD013 -->
| Boundary | Deterministic evidence | Recorded live evidence | v0.8 status |
| --- | --- | --- | --- |
| CLI, stable JSON, configuration schema v10 | Unit, golden, migration, `NO_COLOR` | Historical local-terminal note, not commit-bound | Deterministic only; live-unobserved |
| Account lifecycle and credential-free discovery | Unit, DNS/well-known fixtures, atomic-store tests | Not run | Deterministic only |
| Public domain-only compatibility checker | Browser privacy/source checks; DNS, CORS, bounds, redirect, failure, and SSRF fixtures | Not run | Deterministic only; no provider authentication |
| Authenticated local IPC | Unix adversarial tests, Windows contracts, cross-build | Historical macOS arm64 note, not commit-bound | Deterministic only; live-unobserved |
| Outlook Web mail/calendar | Synthetic typed wire contracts | Historical Microsoft 365 notes, not commit-bound | Deterministic only; live-unobserved |
| Legacy Google Web adapter | Synthetic semantic-DOM contracts retained; runtime sign-in disabled before browser launch | Live sign-in rejected by Google on 2026-07-29 | Unsupported |
| Approval-gated Gmail and Google Calendar APIs/Google Meet field | Synthetic REST, OAuth-gate, and application integration contracts | Prior [IMAP/SMTP observation](evidence/google-macos-arm64-2026-07-30.md) does not cover the new route | Included but disabled; deterministic only; live-unobserved |
| Approval-gated Google Tasks API | Synthetic REST, identity, independent-scope, OAuth-gate, ETag, assigned-task, polling/reset, and application integration contracts | Not run | Included but disabled; deterministic only; live-unobserved |
| Microsoft Graph mail/calendar/Teams-link field | Synthetic REST and application integration contracts | Not run | Deterministic only |
| Microsoft Graph To Do | Synthetic OAuth, national-cloud, REST CRUD/delta, recurrence/time-zone, cursor-restart, and application integration contracts | Not run | Implemented; deterministic only; live-unobserved |
| Todoist tasks | Synthetic public-client OAuth, refresh rotation, API v1 REST/Sync CRUD, plan, time, temporary-ID, cursor/isolation, and application integration contracts | Not run | Implemented; deterministic only; live-unobserved |
| TickTick tasks | Synthetic confidential-client OAuth, transient-secret, REST CRUD/search, time, recurrence, assignment, permission, bounds, polling/tombstone, cursor/isolation, and application integration contracts | Not run | Implemented; deterministic only; live-unobserved |
| JMAP mail | Synthetic RFC 8620 session/query/write contracts | Not run | Deterministic only |
| IMAP/SMTP mail | Synthetic protocol/MIME/capability contracts | Not run | Deterministic only |
| CalDAV calendar | Synthetic WebDAV/iCalendar/conditional-write contracts | Not run | Deterministic only |
| CalDAV VTODO tasks | Synthetic RFC 5545 mapping, WebDAV ETag writes, RFC 6578 sync/reset, account/session isolation, and extension-preservation contracts | Not run | Implemented; deterministic only; live-unobserved |
| iCloud guided IMAP/SMTP + CalDAV preset | Synthetic discovery, account-review, OS-prompt, and content-free doctor contracts | Not run | Deterministic only; live-unobserved |
| Cross-account search and agenda | Isolation, ordering, bounds, partial-failure tests | Not run | Deterministic only |
| Canonical task model, CLI, MCP, IPC, and cross-account projection | Shared synthetic fixtures; isolation, cursor, bounds, capability, and preview/commit tests | Not run | Implemented; Microsoft Graph adapter enabled separately |
| Private saved mail/calendar queries and no-content-cache decision | Account isolation, strict bounded store, revision race, corrupt purge, CLI/MCP schema, live freshness, and resource-bound tests | Not run | Implemented; definitions only, provider results never persisted |
| Provider-neutral messaging model, Teams Graph/Web, Slack API, and Mattermost REST/WebSocket adapters, schema, and release gate | Shared synthetic fixtures; account/workspace/actor isolation, Graph/Web parity, role/scope capability observation, semantic DOM bounds, SSRF/DNS pinning, redirect/compression/event bounds, cursor binding, malformed result, and preview/commit tests | Not run | Included but disabled; provider live and surface evidence incomplete |
| Read-only import staging | Format, identity, traversal, symlink, bound tests | Not run | Deterministic only |
| Monitoring, queue, and local runner | Consent, recovery, dedup, loop, rate, circuit tests | Not run | Deterministic only |
| Redacted feedback | Allowlist, secret corpus, malformed/oversized, action-order tests | Historical local-terminal note, not commit-bound | Deterministic only; live-unobserved |
| Local agent-host detection | Catalog validation; Unix/Windows/macOS path, manager, app, symlink, cache, timeout, cancellation, and secret-free JSON fixtures | Not run | Deterministic only; live-unobserved |
| MCP clients | Native setup-plan and schema tests | Historical Codex/Claude notes, not commit-bound | Deterministic only; live-unobserved |
| Distribution | Archive/package/SBOM/inventory verification in candidate and tag workflows | Tagged release workflow and published assets provide release-bound evidence | Provider/platform observations remain separate |
<!-- markdownlint-enable MD013 -->

Historical Outlook Web notes were made on 2026-07-18, 2026-07-19, and
2026-07-25 using synthetic content and no third-party recipient. Those notes did
not record the exact commit, so they are context only and do not substantiate
v0.8. Google's retired IMAP/SMTP route has the bounded commit-bound observation
linked above; it is not evidence for the staged Gmail API route. No current
provider or native-platform boundary has a commit-bound live observation
for the v0.8 implementation. The explicit marker and required template live in
the [live evidence index](evidence/README.md).

Cross-compilation proves platform-specific code builds; it does not replace
native browser, keyring, IPC, Gatekeeper, SmartScreen, or package-manager
evidence.

## Provider claims

- `microsoft-owa`: mail and calendar are implemented; historical live notes
  exist, but v0.8 remains live-unobserved because those notes are not tied to
  its exact commit.
- `google-web`: the legacy parser and synthetic adapter contracts remain for
  safe handling of v0.8.0-v0.8.1 configuration, but runtime sign-in is
  unsupported and stops before browser launch. Google rejected the observed
  software-controlled browser sign-in; Corresync does not disguise automation.
- `google`: Gmail API mail, selectable Google calendars, and Google Meet
  creation after observed calendar capability are implemented behind an
  approval gate. RC builds reject account addition and activation before OAuth,
  keyring, browser, or API access. The route has synthetic contracts only and
  remains live-unobserved. The linked macOS observation covers the retired
  IMAP/SMTP transport and is historical context only.
- `google-tasks`: task-list discovery, typed task CRUD/state, subtasks,
  ordering, output-only source links, date-only due values, ETag conditions,
  and deletion-aware polling are implemented behind the same approval gate.
  The route uses an independent task-only grant, stops before OAuth while the
  gate is closed, and has synthetic evidence only.
- `microsoft-graph`: mail, selectable calendars, typed Teams-link creation, and
  Microsoft To Do are implemented with service-derived scopes and a BYO public
  OAuth client. Global, GCC High, and DoD use closed endpoint/authority pairs;
  China rejects To Do before OAuth. The route has no recorded live observation.
- `slack`: supported Web API translation is implemented with exact workspace
  and actor confirmation, observed installation scopes, bounded cursors and
  file reads, rate-limit preservation, and outcome-unknown writes. Attachment
  writes remain unavailable because upload and message creation are not one
  atomic reviewed commit. The route is release-disabled and live-unobserved.
- `mattermost`: supported v4 REST translation and an explicitly opened,
  content-free WebSocket invalidation stream are implemented for one selected
  public HTTPS origin and team. DNS answers are validated and pinned before
  authorization; redirects, private/special destinations, compressed
  responses, oversized content, replay, sequence gaps, and event floods fail
  safely. Capabilities come from the actor's returned system, team, and channel
  role definitions. REST sync uses reset snapshots, and WebSocket events only
  request an earlier snapshot. Attachment sends and ID-bound mention writes
  remain unavailable because the provider contracts cannot preserve one atomic
  reviewed payload. The route is release-disabled and live-unobserved.
- `todoist`: task lists, typed task CRUD/state, plan-sensitive metadata,
  reminders, exact provider recurrence, and sync-token changes are implemented
  through a BYO public client and PKCE. The API v1 route has synthetic evidence
  only and no recorded live observation.
- `ticktick`: task projects, Inbox, typed reads/search, create/update/complete/
  delete, recurrence, checklists, labels, ordering, one assignee, and bounded
  snapshot polling are implemented through a BYO confidential client. Its
  client secret stays behind a separate external-credential consent, no
  personal token is accepted, and the missing identity, refresh, atomic-write,
  reopen, reminder, and complete-pagination contracts remain explicit. The
  route has synthetic evidence only and no recorded live observation.
- `jmap`, `imap-smtp`, and `caldav`: implemented against synthetic standards
  contracts, with server-specific behavior exposed through capabilities and
  degradations. CalDAV has independent VEVENT calendar and VTODO task routes;
  VTODO remains live-unobserved on real servers.
- `apple-icloud`: a guided discovery family, not a provider adapter. It composes
  the existing `imap-smtp` and `caldav` routes with Apple's published endpoints
  and an external credential reference. It has synthetic coverage only and
  remains live-unobserved.
- task routes: the canonical application, CLI, JSON, IPC, MCP, cursor, and
  preview/commit contracts are implemented. Microsoft To Do, Todoist, TickTick,
  CalDAV VTODO, and the disabled Google Tasks route have deterministic provider
  contracts and remain live-unobserved. The other
  provider rows in the [Task contract](tasks.md) remain development targets,
  not provider evidence.

`pop3` is a reserved unavailable identifier. Its presence in discovery and
config validation does not constitute an adapter claim.

## Historical Outlook Web observation detail

Historically noted, but not a current compatibility claim:

- folder discovery, list/search/body, attachment metadata/content;
- single-message move and read/unread restoration;
- save-only draft and self-recipient new send;
- bounded primary-calendar list, create, update, cancel;
- Teams join URL returned from a reviewed Outlook event creation when the
  authenticated calendar advertises that capability;
- Codex discovery and a bounded calendar tool call.

Deterministic only:

- reply, reply-all, forward, HTML, and file-attachment composition;
- permanent message deletion and shared/delegated mailbox routing;
- all-day/reminder/recurrence creation and attendee replacement;
- the complete text-only terminal authentication flow.

Calendar list does not reliably return the join URL created with an event.
Treat the reviewed creation result as the provisioning result.

## Release targets

<!-- markdownlint-disable MD013 -->
| OS | amd64 | arm64 |
| --- | --- | --- |
| macOS | tar.gz | tar.gz |
| Linux | tar.gz, deb, RPM, APK | tar.gz, deb, RPM, APK |
| Windows | zip | zip |
<!-- markdownlint-enable MD013 -->

Release verification checks archives, native packages, both SBOM formats,
checksums, source/catalog manifests, completion, manual, plugin, Skill,
essential documentation, and an isolated Linux first run. During the finite
command transition, archives/packages carry `corr` plus the identical
`corresync` compatibility entry.

## Opt-in live workflow

Use only an account, device, source export, and recipient/attendee set you are
authorized to test. Prefer a dedicated test account and synthetic content.

1. Verify the selected release checksum and provenance.
2. Run `corr config validate`, `corr account list`, and `corr doctor`.
3. Review the selected provider route and required authentication.
4. Run `corr auth login --account ALIAS` and complete all controls visibly.
5. Run `corr doctor --online --connection-only --account ALIAS` first, then the
   broader metadata-contract check when that is part of the observation scope.
6. Exercise metadata-only reads before sensitive reads.
7. Exercise save-only draft before an external send.
8. For each write, review the exact preview, commit once, and reconcile remote
   state after any unknown outcome.
9. Stop the owner with `corr daemon stop`.

The online doctor is bounded and content-free in its output. It requires the
session established in step 4 and never initiates authentication or OAuth. It
does not prove mutation compatibility. Monitoring, remote egress, permanent
deletion, and calendar invitations require separate explicit authorization.
The `--connection-only` form reports when the active session last established
TLS and authorization. It makes no fresh authentication attempt, makes no
folder, message, event, contact, or attachment metadata request, and reports
Mail/Calendar/Tasks status independently. For a Microsoft To Do observation,
keep task output local and record only the operation and content-free result
stage. The build-tagged read-only harness in `internal/provider/graphapi`
requires an explicit confirmation value and never logs task content.

The former managed Google Web live harness is no longer an accepted observation
path. Do not use automation-hiding flags or browser fingerprint spoofing to
make the provider accept it. Record Google compatibility only through the
explicitly authorized `google` route.

See the [manual test checklist](manual-test-checklist.md) for provider and
platform recording templates.

## Recording evidence

A shareable observation contains only:

- exact `corr version --json` output;
- OS/architecture and browser or credential-store family/version;
- provider ID and broad deployment class;
- capability or operation name;
- content-free success/failure stage;
- observation date.

Do not include account aliases/IDs/addresses, tenant names, endpoint hostnames,
folder/message/event/attachment/request IDs, subjects, recipients, attendees,
bodies, queries, source paths, screenshots, provider payloads, cookies, tokens,
canaries, grants, credential references, approval values, or runner arguments.
Use `corr feedback --last-error` for a locally reviewable report.

## Drift policy

A live failure must be reproduced without sensitive data, represented by a
synthetic fixture, and fixed behind the typed provider boundary. Never add a
raw protocol escape hatch or commit captured private payloads. A provider that
loses a capability must report a degradation or unavailable operation rather
than silently falling back to another route.
