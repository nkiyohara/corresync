# Threat model

Corresync handles private mail/calendar data and the authority to act as an
interactively authenticated user. It is a local single-user tool, not a remote
gateway or tenant administration service.

## Assets

- browser sessions, OAuth grants, and credential-helper results;
- messages, attachments, recipients, events, attendees, and meeting links;
- authority to send mail, change mailbox state, or alter meetings;
- stable account identities, provider cursors, import staging, event queues,
  monitor configuration, and local notification/runner destinations;
- configuration, content-free audit records, approval tokens, daemon
  credentials, and privacy-preserving error records;
- release artifacts, checksums, SBOMs, and update provenance.
- website-checker input domains, fixed resolver responses, and the integrity of
  public compatibility classifications.

## Trust boundaries

- providers and identity systems are external services;
- MCP hosts, models, plugins, scripts, notification viewers, and local runners
  are untrusted callers;
- mail, calendar, import, and event-queue values are attacker-controlled data
  and may contain prompt injection;
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
  publish.
- GitHub Pages, Cloudflare Workers, and Cloudflare's public DNS resolver are
  external infrastructure for the optional domain-only website checker;
  ordinary connection metadata is visible to those operators.

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

## Authentication controls

- Never accept or persist passwords, cookies, canaries, bearer tokens, OAuth
  authorization codes, access tokens, refresh tokens, or client secrets in
  config, CLI, MCP, audit, feedback, or logs.
- A generated Google Desktop client credential may enter only through the
  inherited `CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` process environment. It is
  bounded, excluded from browser URLs and stored grants, and sent only to
  Google's fixed TLS token endpoint.
- The Outlook Web route uses visible interactive sign-in. OAuth routes use
  Authorization Code with PKCE for an explicitly selected public client.
- The staged Google route is release-gated before browser, OAuth, keyring,
  session, and network access until production approval. After activation it
  pins the Google API base and cannot represent a password, app password,
  alternate host, or arbitrary Google API transport.
- Standards credentials remain behind an OS-keyring entry or an explicitly
  approved helper reference.
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
  text, queries, credential references, runner arguments, and approval values.
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
- Teams chat, channels, calls, recordings, or meeting lifecycle management;
- remote MCP, hosted relay, multi-user daemon, or ambient network listener;
- raw crash upload, automatic issue submission without explicit local consent,
  or a telemetry/reporting relay;
- executing instructions found in messages, events, imports, or attachments.
- turning the optional public compatibility checker into an arbitrary DNS/HTTP
  proxy, provider probe, authentication surface, account service, or telemetry
  endpoint.

Report suspected vulnerabilities privately as described in
[SECURITY.md](../SECURITY.md).
