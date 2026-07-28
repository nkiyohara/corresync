# Compatibility evidence

Compatibility is an evidence claim, not an inference from a fixture or a
provider brand. This page distinguishes deterministic coverage on `main` from
authorized live observations.

## Current branch evidence

<!-- markdownlint-disable MD013 -->
| Boundary | Deterministic evidence | Recorded live evidence | Current branch status |
| --- | --- | --- | --- |
| CLI, stable JSON, configuration schema v3 | Unit, golden, migration, `NO_COLOR` | Historical local-terminal note, not commit-bound | Deterministic only; live-unobserved |
| Account lifecycle and credential-free discovery | Unit, DNS/well-known fixtures, atomic-store tests | Not run | Deterministic only |
| Authenticated local IPC | Unix adversarial tests, Windows contracts, cross-build | Historical macOS arm64 note, not commit-bound | Deterministic only; live-unobserved |
| Outlook Web mail/calendar | Synthetic typed wire contracts | Historical Microsoft 365 notes, not commit-bound | Deterministic only; live-unobserved |
| Google Web read-only mail/calendar | Synthetic semantic-DOM and application integration contracts; opt-in live harness compiles | Not run | Deterministic only |
| Google API mail/calendar | Synthetic REST and application integration contracts | Not run | Deterministic only |
| Microsoft Graph mail/calendar/Teams-link field | Synthetic REST and application integration contracts | Not run | Deterministic only |
| JMAP mail | Synthetic RFC 8620 session/query/write contracts | Not run | Deterministic only |
| IMAP/SMTP mail | Synthetic protocol/MIME/capability contracts | Not run | Deterministic only |
| CalDAV calendar | Synthetic WebDAV/iCalendar/conditional-write contracts | Not run | Deterministic only |
| Cross-account search and agenda | Isolation, ordering, bounds, partial-failure tests | Not run | Deterministic only |
| Read-only import staging | Format, identity, traversal, symlink, bound tests | Not run | Deterministic only |
| Monitoring, queue, and local runner | Consent, recovery, dedup, loop, rate, circuit tests | Not run | Deterministic only |
| Redacted feedback | Allowlist, secret corpus, malformed/oversized, action-order tests | Historical local-terminal note, not commit-bound | Deterministic only; live-unobserved |
| MCP clients | Native setup-plan and schema tests | Historical Codex/Claude notes, not commit-bound | Deterministic only; live-unobserved |
| Distribution | Archive/package/SBOM/inventory verification | v0.7.0 release at `ec868d8`; no post-v0.7 candidate | Published tag only; current branch unobserved |
<!-- markdownlint-enable MD013 -->

Historical Outlook Web notes were made on 2026-07-18, 2026-07-19, and
2026-07-25 using synthetic content and no third-party recipient. Those notes did
not record the exact commit, so they are context only and do not substantiate
this branch. No provider or platform has a commit-bound live observation for
the post-v0.7 implementation. The explicit marker and required template live
in the [live evidence index](evidence/README.md).

Cross-compilation proves platform-specific code builds; it does not replace
native browser, keyring, IPC, Gatekeeper, SmartScreen, or package-manager
evidence.

## Provider claims

- `microsoft-owa`: mail and calendar are implemented; historical live notes
  exist, but the current branch remains live-unobserved because those notes are
  not tied to an exact commit.
- `google-web`: bounded read-only Gmail and Calendar snapshots are implemented
  through an isolated visible browser session, but have no recorded live
  observation.
- `google-api`: Gmail and selectable Google calendars are implemented with a BYO
  public OAuth client, but have no recorded live observation.
- `microsoft-graph`: mail, selectable calendars, and typed Teams-link creation are
  implemented with a BYO public OAuth client, but have no recorded live
  observation.
- `jmap`, `imap-smtp`, and `caldav`: implemented against synthetic standards
  contracts, with server-specific behavior exposed through capabilities and
  degradations.

`pop3` is a reserved unavailable identifier. Its presence in discovery and
config validation does not constitute an adapter claim.

## Historical Outlook Web observation detail

Historically noted, but not a current compatibility claim:

- folder discovery, list/search/body, attachment metadata/content;
- single-message move and read/unread restoration;
- save-only draft and self-recipient new send;
- bounded primary-calendar list, create, update, cancel;
- Teams join URL returned from a reviewed event creation;
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
5. Run `corr doctor --online --account ALIAS`.
6. Exercise metadata-only reads before sensitive reads.
7. Exercise save-only draft before an external send.
8. For each write, review the exact preview, commit once, and reconcile remote
   state after any unknown outcome.
9. Stop the owner with `corr daemon stop`.

The online doctor is bounded and content-free in its output. It requires the
session established in step 4 and never initiates authentication or OAuth. It
does not prove mutation compatibility. Monitoring, remote egress, permanent
deletion, and calendar invitations require separate explicit authorization.

The managed Google Web adapter also has a separate opt-in, read-only harness.
It requires a visible browser profile and authenticates only inside that
browser:

```console
read -r -p "Authorized Google address: " CORRESYNC_LIVE_GOOGLE_ADDRESS
export CORRESYNC_LIVE_GOOGLE_ADDRESS
CORRESYNC_LIVE_CONFIRM=google-web-read-only \
CORRESYNC_LIVE_GOOGLE_PROFILE_DIR="$(mktemp -d)" \
mise exec -- go test -tags=live \
  -run TestLiveGoogleWebVisibleRead ./internal/provider/googleweb
unset CORRESYNC_LIVE_GOOGLE_ADDRESS
```

Set `CORRESYNC_LIVE_BROWSER_EXECUTABLE` only when browser auto-detection is not
appropriate. Use a dedicated profile directory and remove it only through a
reviewed local cleanup after the test. The harness accepts no password, token,
cookie, or storage export; it verifies the visible signed-in identity and
performs bounded mail/calendar reads only. It is excluded from default tests
and CI.

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
