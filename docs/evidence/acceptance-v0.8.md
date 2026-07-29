# v0.8 acceptance evidence

This record maps the acceptance criteria in the v0.8 roadmap issues to the
candidate implementation and deterministic tests. It is not live-provider,
native-platform, MCP-client, or distribution evidence. Those claims remain
`live-unobserved` until a content-free record is tied to the exact commit.

The issue state on GitHub is not changed by this record. Issue updates,
closure, release publication, and live testing remain separately authorized
actions.

## Issue 24: account discovery and lifecycle

- Explainable candidates and manual override:
  `internal/discovery/discovery_test.go` exercises credential-free DNS,
  well-known, Google, and Microsoft evidence.
  `cmd/corr/command_account_test.go` covers manual provider/endpoint selection.
- Distinct stable IDs and isolated profiles:
  `TestAccountAddCreatesDistinctStableIsolationKeysWithoutAuthentication` and
  `internal/accountstore/store_test.go` cover stable identity and state roots.
- Selection before authentication:
  the explicit Google, Graph, JMAP, IMAP, and CalDAV account-add tests prove
  that a route is persisted without starting authentication.
- Shared CLI/MCP use cases:
  `internal/application/account_test.go` owns lifecycle semantics, while
  `cmd/corr/daemon_mcp_backend_test.go` proves MCP uses the same typed
  preview/commit boundary.
- Capability, degradation, and provenance output:
  `cmd/corr/session_backend_auth_test.go` covers content-free per-service
  status and provider-qualified capability records.

## Issue 25: JMAP, IMAP/SMTP, and CalDAV

- Protocol reads, writes, errors, and degradation:
  `internal/provider/jmap/client_test.go`,
  `internal/provider/imapmail/client_test.go`, and
  `internal/provider/caldav/client_test.go` use synthetic TLS protocol
  fixtures, including malformed and ambiguous outcomes.
- Account isolation:
  `TestSessionBackendKeepsHybridProvidersAndAccountsIsolated` keeps route
  sessions separate, and
  `TestAddAccountAtomicallyRejectsCrossAccountCredentialReuse` prevents a
  credential handle from silently crossing account ownership.
- Provider-qualified identities:
  each adapter returns opaque encoded object IDs and application provenance;
  the contract suites resolve those IDs only through the originating adapter.
- Target-bound SMTP and CalDAV writes:
  `internal/application/mail_send_test.go` and the calendar create/update/
  cancel suites reject cross-account or cross-operation commit tokens.
  The protocol suites then assert the exact reviewed mailbox or collection
  path and condition.
- Shipped versus planned coverage:
  `docs/features.md`, `docs/protocol.md`, and `docs/compatibility.md`
  distinguish implemented synthetic contracts from live compatibility.
  POP3 remains reserved and unavailable.

## Issue 26: consent-safe Google providers

- Managed auto mode avoids third-party approval:
  `TestDiscoverKnownGoogleDoesNotAuthenticate` and
  `TestAccountAddAutoSelectsBrowserOwnedGoogleWithoutOAuth` choose the
  visible, read-only web route without starting OAuth.
- Explicit API consent is disclosed first:
  `cmd/corr/oauth_consent_test.go` computes and displays the service-minimal
  scope set, while explicit Google account-add tests prove no authorization
  occurs during configuration.
- Explainable and overridable routing:
  discovery returns evidence and an explicit-selection marker; the account
  command accepts independent mail and calendar provider overrides.
- Multiple Google accounts remain isolated:
  `cmd/corr/session_backend_auth_test.go` covers independent OAuth/browser
  routes and mixed-provider accounts.
- API and web capability separation:
  `internal/provider/googleapi/googleapi_test.go` covers write-capable REST
  contracts; `internal/provider/googleweb/googleweb_test.go` covers bounded
  browser-owned reads and proves every write fails without touching the
  driver.

## Issue 27: explicit Microsoft Graph

- Outlook Web remains the automatic Microsoft route:
  `TestDiscoverKnownMicrosoftOffersGraphOnlyByExplicitSelection` and
  `TestSelectAccountCandidateDoesNotAutoSelectExplicitConsent` prevent Graph
  from becoming an automatic OAuth fallback.
- Graph requires explicit selection or an existing grant:
  `TestAccountAddPersistsExplicitGraphPublicClientWithoutAuthorizing` records
  the selected public client before login, and the session backend opens only
  that configured route.
- Failed consent does not broaden permissions:
  `TestSessionBackendOnlyAuthorizesGraphForExplicitCLILogin` keeps
  authorization account-scoped and has no fallback path.
- Graph and Outlook Web remain isolated:
  session-backend hybrid/account tests preserve separate providers,
  capabilities, degradations, and provenance.
- Graph is optional:
  `docs/authentication.md`, `docs/configuration.md`, `docs/features.md`, and
  `docs/protocol.md` document it as an explicit provider route.

## Issue 28: read-only import staging

- Deterministic synthetic plans:
  `internal/importstage/scanner_test.go` covers MBOX, Maildir, EML, ICS, VCF,
  proprietary decision gates, and Thunderbird hints using temporary fixtures.
- Fidelity and provenance:
  the scanner contracts retain raw messages, dates, flags, iCalendar identity,
  contact identity, source provenance, and visible conflict/degradation data.
- Idempotence:
  `TestMBOXScanRetainsMetadataAndIsIdempotent` proves repeat scans identify
  duplicates instead of creating copies.
- No implicit local profile access:
  `TestImportScanRequiresPrivacyApprovalBeforeLocalAccess`, symlink/traversal
  tests, and the application import policy require an explicit approved source.
- No remote mutation:
  the import application port exposes scan, plan, and account-local purge only;
  no upload or provider writer is reachable.

## Issue 29: cross-account projections

- Stable ordering, global paging, provenance, and partial failure:
  `internal/application/projection_test.go` exercises multiple synthetic
  providers and explicit account status.
- Read-only CLI/MCP parity:
  `cmd/corr/command_projection_test.go` and
  `internal/mcpserver/server_test.go` route search/agenda through the shared
  projection service.
- No broadcast mutation:
  `TestMutationCLIHasNoAllAccountsFlag` and
  `TestMutationToolsHaveNoAllAccountsInput` prove the mutation surfaces have
  no all-account selector.
- Original calendar semantics:
  `TestAgendaProjectionNormalizesDisplayAndRetainsOriginalSemantics` preserves
  original zone and floating-time fields while normalizing display order.
- Per-account capability differences:
  projection account statuses retain provider, capability, degradation,
  completeness, and content-free failure records.

## Issue 30: monitoring and local dispatch

- Disabled-by-default consent:
  `internal/config/config_test.go` and migration acceptance tests keep monitor,
  runner, and remote egress consent off until explicitly enabled.
- Recovery and identifiable duplicates:
  `internal/application/monitor_engine_test.go` and
  `internal/eventqueue/store_test.go` cover cursor recovery, bounded overflow,
  durable pending events, deduplication, and restart-safe commits.
- Account isolation:
  `TestMonitorEnableAndDisableAreExplicitAndAccountScoped` and queue
  acknowledgement/purge tests operate on one stable account ID.
- Notify and queue without AI:
  notification delivery and durable queue tests run independently from the
  direct no-shell runner.
- Audited release and runner outcomes:
  monitor service and dispatch tests bind allowed fields, destination, runner
  result, delivery state, and acknowledgement.
- Explicit retain or purge:
  the monitor disable command requires a queue disposition and tests both
  account-scoped paths.

## Issue 31: authenticated local IPC

- Endpoint type, owner, and permission validation:
  `internal/localipc/platform_unix_test.go` covers symlinks, regular files,
  FIFOs, wrong owners, permissive directories, and socket ownership.
- Replacement-race protection:
  `TestDialContextRejectsSocketReplacementDuringConnect` and singleton-owner
  validation bind the connected endpoint before a bearer is transmitted.
- Listener checks remain:
  `internal/daemonapi/daemonapi_test.go` proves authentication occurs before
  request decoding and retains listener-side peer/auth checks.
- Platform isolation:
  the same package supplies Windows named-pipe contracts and is exercised by
  cross-build verification.
- No live mailbox dependency:
  all tests use local temporary endpoints and synthetic credentials.

## Issue 33: privacy-preserving feedback

- Local deterministic report:
  `internal/feedback/feedback_test.go` proves allowlisted, bounded generation
  without a network dependency.
- Sanitized last error:
  `TestErrorRecordNeverRetainsValuesOrRawError` and
  `TestRunRecordsOnlySanitizedLastError` retain only a bounded error class and
  redacted command shape.
- Review before external action:
  `cmd/corr/command_feedback_test.go` proves the complete report is printed
  before copy, save, or browser launch.
- No automatic submission:
  the GitHub action opens only a prefilled issue page; copy/save work without a
  GitHub account and save never overwrites.
- Secret and malformed-input resistance:
  allowlist, representative secret, malformed, oversized, symlink, and
  replace-not-append tests prevent sensitive or persistent diagnostic buildup.

## Verification boundary

The default verification command covers formatting, documentation lint, vet,
lint, shuffled tests, race tests, secret scanning, vulnerability reachability,
licenses, build, release configuration, and workflow static analysis:

```console
mise exec -- task verify
```

Opt-in live tests are excluded from that command and from CI.
