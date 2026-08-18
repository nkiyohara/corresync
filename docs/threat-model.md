# Threat model

Corresync handles private mail, calendar, task, and messaging data and the
authority to act as an interactively authenticated user. It is a local
single-user tool, not a remote gateway or tenant administration service.

## Assets

- browser sessions, OAuth grants, and credential-helper results;
- mail and chat messages, channels, threads, reactions, attachments,
  recipients, events, attendees, meeting links, task lists, reminders, notes,
  recurrence, and linked sources;
- authority to send mail, change mailbox state, alter meetings, or mutate tasks;
- stable account identities, provider cursors, import staging, event queues,
  monitor configuration, and local notification/runner destinations;
- configuration, content-free audit records, approval tokens, daemon
  credentials, and privacy-preserving error records;
- release artifacts, checksums, SBOMs, and update provenance;
- website-checker input domains, fixed resolver responses, and the integrity of
  public compatibility classifications.

## Trust boundaries

- providers and identity systems are external services;
- MCP hosts, models, plugins, scripts, notification viewers, and local runners
  are untrusted callers;
- mail, calendar, task, import, and event-queue values are attacker-controlled
  data and may contain prompt injection;
- another local user or a process outside the selected user boundary is
  untrusted;
- imported files can be malformed, oversized, symlinked, replaced, or crafted
  to escape their selected root;
- clipboard tools, browsers, issue trackers, and remote runner destinations
  cross an explicit disclosure boundary;
- OS credential prompts and user-approved credential helpers are external
  secret owners; Corresync may pass a reviewed handle but must not proxy their
  secret input;
- CI logs, fixtures, feedback reports, and public issues must be safe to
  publish;
- Agent-host executables, applications, and configuration footprints are
  external local state. Detection may reveal a private installation path but
  cannot execute an agent or read its configuration or conversations.
- GitHub Pages, Cloudflare Workers, and Cloudflare's public DNS resolver are
  external infrastructure for the optional domain-only website checker;
  ordinary connection metadata is visible to those operators;
- an explicitly selected upstream task MCP is an external provider boundary,
  never an extension of Corresync's trusted local MCP server.
- a selected self-hosted Mattermost origin, its DNS answers, TLS endpoint,
  REST responses, and WebSocket events are one untrusted provider boundary;
  self-hosting does not make a private or special-use destination safe.

## Public compatibility-checker controls

- Derive the domain in the browser, clear the full address after validation,
  and send only one normalized domain in a bounded JSON `POST` body. Never put
  the address or domain in a query string, browser history, cookie, persistent
  storage, analytics event, application log, or cache.
- Accept only the fixed production origin, host, path, method, content type,
  object shape, and normalized non-IP domain. Rate-limit with one global key so
  application code does not inspect or retain a visitor IP.
- Query only the fixed Cloudflare DNS-over-HTTPS origin with bounded names,
  types, time, answers, and bytes. Reject redirects. Never fetch a user-derived
  host, provider API, well-known URL, private address, or DNS answer target.
- Return only a versioned closed schema with typed classifications, signal
  categories, route IDs, and next-step IDs. Return no raw DNS record or free
  URL; browser code maps IDs to reviewed static text and uses text-only DOM
  construction.
- Use `no-store`, no Cache API or storage binding, and disable Workers
  observability and invocation logs. The checker never authenticates, adds an
  account, probes consent, or claims that DNS evidence guarantees capability.

## Local agent-host detection controls

- Keep the declarative catalog separate from the filesystem detector and from
  mutation-capable lifecycle adapters. Detection is always read-only.
- Treat inherited `PATH` as primary evidence. Probe only a fixed, validated,
  bounded set of common manager directories, application paths, and config
  footprints; never source a profile or run a login shell implicitly.
- Never execute a detected binary, enumerate arbitrary directories, read a
  host config file, inspect credentials, or scan conversation/session state.
- Resolve symlinks, require an executable regular file where applicable, cap
  search-path bytes/directories, bound total hosts, concurrency, and duration,
  and expose typed partial failure instead of silently returning an empty set.
- Scope cached evidence to one detector/runtime context. Keep local, SSH, and
  WSL context visible, and apply previously selected/missing state per request
  rather than persisting it as detection evidence.
- Report detection, connection, support maturity, and package capabilities as
  separate fields. No detected brand may imply that MCP, a Skill, or a native
  package is installed or enabled.
- Keep evidence paths out of public diagnostics. Structured local output never
  contains environment values, file contents, tokens, provider/account data,
  mail/calendar data, or agent conversations.

## Authentication controls

- Never accept or persist passwords, cookies, canaries, bearer tokens, OAuth
  authorization codes, access tokens, refresh tokens, or client secrets in
  config, CLI, MCP, audit, feedback, or logs.
- A generated Google Desktop client credential may enter only through the
  bounded installed-client JSON importer or an explicitly consented external
  helper. Only its external handle enters config. It is resolved for code
  exchange or refresh, excluded from browser URLs and stored grants, sent only
  to Google's fixed TLS token endpoint, and overwritten afterward.
- TickTick configuration contains only a separately consented external
  client-secret handle. A new authorization-code exchange resolves it
  transiently for HTTP Basic authentication to TickTick's fixed token endpoint;
  reuse of a valid grant does not open the external secret owner. The secret and
  any unexpected refresh token are never persisted by Corresync.
- The Outlook Web route uses visible interactive sign-in. Public OAuth routes
  use Authorization Code with PKCE. TickTick uses the provider's documented
  confidential Authorization Code contract without inventing PKCE or refresh.
- User-owned Google mail, calendar, and task routes are configured without
  authentication and may authorize only from explicit local CLI login. Each
  route pins its API base and cannot represent a password, app password,
  alternate host, or arbitrary Google API transport. Google Tasks uses an
  independent task-only scope set and authorization handle. The managed Google
  route remains release-disabled without a user override.
- Standards credentials remain behind an OS-keyring entry or an explicitly
  approved helper reference.
- Slack and Mattermost configuration stores only an account-isolated external
  authorization reference. Mattermost resolves and pins a bounded set of
  public DNS answers before the external bearer authorizer may touch a request;
  the adapter never accepts a token value or WebSocket auth challenge payload.
- Slack private-file reads map the selected fixed API base to one fixed file
  origin and use a separate exact-origin bearer capability. Redirects, query
  credentials, arbitrary hosts, and non-private-file paths fail before the
  authorization can leave its owner.
- Guided OS-keyring enrollment invokes only the fixed platform credential tool
  with a bounded reviewed handle and no password argument. The child owns the
  TTY prompt; `corr` receives only its exit status. MCP, JSON, pipes, and
  non-interactive setup cannot launch it.
- Account-add approval displays the exact external backend/key handles and
  rejects cross-account handle reuse before both preview and atomic commit.
- Discovery cannot access credentials, start consent, or probe administrator
  approval.
- Never automate around MFA, Conditional Access, consent, or provider policy.
- Never use TLS interception or silently downgrade TLS.
- iCloud custom-domain classification requires the exact Apple endpoint set
  from separate IMAPS, Submission, and CalDAV SRV evidence; a suffix or one DNS
  record cannot activate the preset.
- The text-only terminal browser relay requires an interactive TTY, sends at
  most one sanitized control event, and is never exposed through MCP.

## Integration-package controls

- Generate native wrappers from one closed public metadata schema and the
  reviewed Agent Skill. The schema cannot represent an account, credential,
  token, cookie, environment secret, private config value, or auto-approval.
- Thin packages invoke only `corr mcp serve` over local stdio and require the
  CLI on `PATH`. Never imply that hosted ChatGPT, Kiro Web, or a remote sandbox
  can reach local state.
- Keep the self-contained MCPB distinct from thin plugins. Bind its exact
  release binaries, version, launchers, licenses, checksums, SBOMs, and MCP
  Registry manifest in the release verifier.
- Treat host-specific configuration as external state. Generic bundle metadata
  is not permission to guess a host schema or overwrite user-authored files;
  lifecycle adapters own scoped inspection and previewed mutation.
- Bind every lifecycle apply to the exact request and freshly re-derived typed
  plan. Execute official clients as executable plus argv with bounded time and
  output; never interpolate a shell program or retain raw host output.
- For documented file fallbacks, lock and fully parse a bounded document,
  reject unknown schema versions, unsafe owners/modes/types, symlinks and name
  conflicts, and compare the post-preview fingerprint before changing only the
  exact Corresync entry. Use a private same-filesystem temporary file, sync,
  atomic replacement, and one bounded permission-preserving recovery copy.
- On Windows, rely on the signed-in user's inherited filesystem ACLs rather
  than synthesized Unix owner/mode bits, reject all reparse points including
  directory junctions, and treat directory-sync durability as unavailable.
  Re-inspect actual state through `doctor` after a crash, require the previewed
  `repair` flow for any new write, and retain the bounded recovery copy.
- Stage native packages only from the installed, reviewed bundle tree into a
  private Corresync-owned directory. Strip the package's duplicate MCP
  declaration so the separately previewed absolute executable/config launch
  contract remains authoritative. Version and source hashes participate in
  inspect, repair, and removal.
- Install portable Skills only at documented locations. An ownership marker
  and per-host references prevent overwrite or removal of a user-authored or
  still-shared Skill. Never create host auto-approval, credential, token, or
  environment-secret fields.
- Verify only registration/package identity. Never use a mailbox, calendar, or
  provider authentication request as an integration health check. Treat host
  reload/new-session as an explicit remaining step.

## Local IPC controls

- Use an owner-only Unix socket or protected local Windows named pipe; never a
  TCP listener.
- Namespace the endpoint by config/state identity and hold a singleton lock.
- Authenticate the endpoint before transmitting the rotating local bearer.
- On Unix, reject untrusted runtime directories, symlinks, wrong types or
  owners, permissive modes, inactive locks, socket squatters, peer-UID
  mismatch, and directory/socket replacement races.
- On Windows, reject remote named-pipe clients and validate the pipe owner,
  protected non-null DACL, server process ID/SID, and credential-file owner
  before sending the bearer.
- Validate bearer, caller, protocol version, config digest, method, body size,
  result size, concurrency, and shutdown lifetime.
- Rotate the bearer on every owner start and remove only the current owner's
  credential during shutdown.

## Data and action controls

- Treat every provider value as data, never instructions.
- Keep provider protocols behind closed typed adapters; expose no arbitrary
  action, URL, header, method, property, command, or payload surface.
- Enforce effect policy in the application core for both CLI and MCP.
- Bind approval tokens to caller, account, provider, target, normalized
  payload, effect, expiry, and one use.
- Require preview/commit for external and destructive writes. Never retry a
  write whose remote outcome may be committed.
- Preserve account/provider provenance across projections and never implement a
  broadcast write.
- Bound recipients, attendees, query/results, time windows, bodies,
  attachments, imports, queues, runner input, and feedback records.
- Bind task cursors to one provider, account, list, and advertised mode. Treat
  local notifications as invalidation only; refetch through bounded reads.
- Treat saved query definitions as private untrusted state: isolate them by
  opaque account ID, store no provider results or credentials, bind every
  replacement/deletion/purge to the reviewed revision, and report every run as
  a live non-cached read. Never let a definition enable monitoring, a runner,
  notification delivery, authentication, or egress.
- Bind messaging cursors and writes to one opaque account, route, workspace,
  actor, conversation, thread, and version. Treat Mattermost WebSocket payloads
  as content-free invalidations only; deduplicate bounded sequences and recover
  every gap or reconnect through an anchored REST snapshot reset.
- For self-hosted messaging, require one credential-free HTTPS DNS origin,
  valid public DNS answers pinned for the transport lifetime, normal
  certificate verification, no proxy or redirect, identity encoding only, and
  bounded REST/file/event bodies. Reject mixed public/private answers, IP
  literals, DNS rebinding, alternate authorities, and compressed responses.
- Treat linked task sources as untrusted provenance, never authorization to
  follow a URL or copy, move, or mirror an object. Reject self-loops before any
  future workflow preview.
- For an explicit upstream remote MCP provider, allow only reviewed typed task
  tools and schemas, bound every response and timeout, send no unrelated
  Corresync content, and never retry a side-effecting call automatically.
- Keep browser-owned and local OS automation sessions in their established
  owner/helper boundary. Never accept arbitrary script source from a caller.

## Monitoring and runner controls

- Monitoring defaults to `off`; upgrades and imports do not enable it.
- Consent advances separately through collection, queueing, agent execution,
  content inclusion, and remote egress.
- Exclude Sent and Drafts where possible, deduplicate events, rate-limit and
  debounce dispatch, enforce quiet hours, and stop through a circuit breaker.
- Invoke one absolute runner directly without a shell. Runner arguments are
  configuration, never content-derived.
- Automatically triggered agents are read-only by default. Mail/calendar
  content cannot widen filters, routes, tools, egress, or write authority.
- MCP may inspect monitor/event state and acknowledge a local event, but cannot
  enable monitoring, add a runner, approve egress, or purge the queue.

## Import controls

- Without `--approve-read`, import scan performs no filesystem scan and exits
  after displaying the privacy boundary.
- `--approve-read` authorizes reading the one resolved source and creating its
  bounded account-local staging plan in the same operation.
- Reject traversal, symlink escape, special files, replacement, and excessive
  nesting/count/size.
- Never authenticate, upload, send, modify, or delete the source.
- Purge removes only Corresync-owned account-local staging.

## Observability and feedback controls

- Audit only operation/effect, time, caller, account/provider provenance,
  bounded result class, and policy reason.
- Exclude authorization, addresses, subjects, bodies, attachment names, event
  text, task titles, notes, reminders, links, queries, task cursors, credential
  references, runner arguments, and approval values.
- Keep only one bounded, owner-only generalized last-error record; replacement
  is atomic and symlink-safe.
- Build feedback from an allowlist. Raw errors, arguments, paths, identifiers,
  content, environment values, and credentials are not accepted by the report
  schema.
- Generate and print the complete report locally before copy, save, or opening
  a browser. Keep that manual flow non-submitting.
- Keep automatic public issue submission default-off, interactive-CLI-only,
  externally authenticated by `gh`, and confined to a smaller closed schema.
  MCP cannot enable it. Never collect then heuristically mask a raw error.

## Supply-chain controls

- Pin CI actions by immutable commit SHA.
- Run formatting, tests, race detection, static analysis, secret scanning,
  vulnerability scanning, and linked-license checks.
- Build reproducible archives/packages with checksums and per-artifact SPDX and
  CycloneDX SBOMs.
- Verify a direct update's exact tag, platform, checksum, and GitHub Actions
  Sigstore identity; preserve rollback and never replace package-managed files.
- Keep automatic update checks bounded, unauthenticated, cached, and free of
  account or machine identifiers. Disable them in MCP, daemon, completion,
  feedback, pipes, and JSON output.
- Keep automatic installation default-off and direct-install-only. Reuse the
  complete verified self-update path, never invoke a package manager, never run
  on an MCP, configuration-management, or machine-output path, and let
  verification failure leave both the executable and requested provider
  operation unchanged.

## Explicitly unsupported

- unattended or password-based login;
- TLS interception or bypassing organizational controls;
- automatic provider fallback or tenant-wide/delegated authorization;
- generic provider actions or arbitrary protocol payloads;
- Teams calls, recordings, presence, or meeting lifecycle management;
- remote Corresync MCP transport, hosted relay, multi-user daemon, or ambient
  network listener; an explicit allowlisted upstream provider adapter does not
  expose Corresync remotely;
- raw crash upload, automatic issue submission without explicit local consent,
  or a telemetry/reporting relay;
- executing instructions found in messages, events, tasks, imports, or
  attachments;
- turning the optional public compatibility checker into an arbitrary DNS/HTTP
  proxy, provider probe, authentication surface, account service, or telemetry
  endpoint.

Report suspected vulnerabilities privately as described in
[SECURITY.md](../SECURITY.md).
