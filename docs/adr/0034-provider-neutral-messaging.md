# ADR 0034: Add provider-neutral messaging behind evidence gates

- Status: accepted
- Date: 2026-08-14
- Amends: the communications exclusions in
  [ADR 0005](0005-calendar-hosted-teams-links.md) and
  [ADR 0008](0008-provider-neutral-product-scope.md)

## Context

Corresync already applies one local-first policy core to mail, calendar, and
tasks through CLI, JSON, MCP, and its authenticated local daemon. Messaging is
useful through the same surfaces, but provider contracts differ in actor
identity, conversation shape, retention, membership, authorization, event
delivery, and write outcomes. Treating those differences as arbitrary
provider actions would bypass the product's account isolation and
preview/commit guarantees.

The first release cohort has three independently selected providers:

- Microsoft Teams through delegated Microsoft Graph and through an
  interactive, browser-owned Teams Web session;
- Slack through supported Slack app APIs and provider-owned installation
  authorization; and
- Mattermost through its supported v4 REST API and WebSocket event contract.

Microsoft documents delegated chat and channel message operations as typed
Graph resources with operation-specific permissions. Slack documents its Web
API as HTTPS methods whose accessible conversations and actor attribution
depend on token type, installation, membership, and granted scopes. Mattermost
documents a bearer-authenticated v4 REST API and `/api/v4/websocket` event
stream. Those are distinct contracts; branding alone proves none of them.

## Decision

Add a provider-neutral messaging domain. CLI, MCP, and the daemon call the same
closed application use cases for conversations, messages, threads,
incremental synchronization, reactions, attachments, conversation creation,
and membership changes. No transport exposes an arbitrary action, URL,
selector, script, or provider property map.

One messaging route belongs to one existing opaque account and additionally
binds one stable workspace and actor. Every result and audit record exposes
the account, provider route, workspace, and actor mode. Actor modes are
`delegated_user`, `app`, or `unavailable`; they are never normalized into an
apparently equivalent sender.

Metadata-first reads exclude full message bodies and attachment bytes. An
explicit sensitive read obtains body or attachment content. Rich text, links,
mentions, filenames, reactions, and remote display names are bounded untrusted
data and never instructions or authorization.

Every consequential operation uses the server-enforced preview/commit
protocol. The immutable operation binds the exact account, workspace, actor,
conversation, reply target, item version, and canonical payload. A provider
timeout or malformed success response after a write is outcome-unknown and is
never retried automatically. Authentication recovery always requires a fresh
preview.

### Provider routes

Microsoft Graph is available only after explicit route selection and a grant
the user already approved. It is never a dependency, discovery probe,
fallback, or automatic authorization. Corresync requests only the delegated
permissions needed by the configured capability cohort and preserves tenant,
license, consent, Conditional Access, and permission failures.

Teams Web uses a dedicated visible browser profile owned by the signed-in
human. Corresync does not inspect or automate authentication pages and never
requests, persists, exports, or logs a password, token, cookie, authorization
header, or refresh token. Its adapter offers only closed semantic messaging
operations. DOM drift, browser policy, an unexpected origin, or an unsupported
accessible control fails closed. It does not reverse-engineer a private
protocol or expose browser primitives.

The released Teams capability manifest is the intersection proven by both
routes. Graph and Teams Web must pass the same conformance suite and separate
revision-bound live observations. If either route lacks one cohort operation,
that operation is unavailable on both routes; there is no route fallback.

Slack uses only supported Slack APIs. The selected workspace, installation,
token actor, granted scopes, channel membership, retention, distribution
status, and rate limits remain visible capability or degradation evidence.
Corresync does not automate Slack administration, import browser sessions, use
private APIs, impersonate another client, or operate as a self-bot.

Mattermost uses one exact user-approved HTTPS origin. Configuration stores an
external credential reference rather than a secret. Every connection rejects
cross-authority redirects, invalid TLS, private or special-use destinations,
DNS rebinding, oversized or over-compressed responses, and unbounded event
streams. WebSocket events invalidate account-local state; REST snapshots
recover ordering gaps. Credentials and authorization challenges never enter
logs, audit, fixtures, or model-visible output.

### Monitoring and release gates

Message synchronization is not notification consent. Monitoring, local
delivery, runner execution, and remote egress remain separate opt-ins under
[ADR 0014](0014-opt-in-monitoring-and-dispatch.md). MCP may inspect accepted
state but cannot enable or widen any of them.

Messaging code may merge incrementally, but configuration, discovery, CLI,
MCP, daemon dispatch, monitoring, and public support claims remain disabled
until one release manifest proves all of the following:

1. this ADR, closed application contracts, migrations, and documentation;
2. deterministic synthetic conformance, isolation, bounds, malformed-input,
   throttling, permission-loss, and ambiguous-write tests;
3. the Graph/Teams Web parity intersection and separate content-free live
   observations;
4. provider-specific live evidence for every advertised Slack and Mattermost
   route; and
5. a clean final security review with no unresolved critical, high, or medium
   finding.

Google Chat is deferred until the production Google OAuth approval boundary is
resolved. Matrix is deferred until an accepted ADR defines end-to-end
encryption, key ownership, local storage, backup, and recovery. Neither is a
fallback. Calls, recordings, presence surveillance, meeting lifecycle
control, tenant administration, bulk export, hosted relays, private protocols,
self-bots, and authentication or policy bypass remain out of scope.

## Consequences

The Teams chat and channel exclusions in ADR 0005 and ADR 0008 no longer apply
to the evidence-gated messaging domain defined here. Their calendar-meeting
link decision and every other permanent boundary remain in force. Calls,
recordings, presence, and meeting lifecycle management are still excluded.

Provider adapters remain translators. Product policy, isolation,
preview/commit binding, monitoring consent, and release enablement live in the
inward application and release-gate layers. A development symbol or fixture
does not become a support claim; unobserved remains unobserved.

## Primary contracts

- [Microsoft Graph Teams API overview](https://learn.microsoft.com/graph/api/resources/teams-api-overview)
- [Microsoft Graph chat message resource](https://learn.microsoft.com/graph/api/resources/chatmessage)
- [Slack Web API methods](https://docs.slack.dev/reference/methods/)
- [Slack conversations history](https://docs.slack.dev/reference/methods/conversations.history/)
- [Mattermost API documentation](https://developers.mattermost.com/api-documentation/)
