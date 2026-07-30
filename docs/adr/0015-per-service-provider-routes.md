# ADR 0015: Per-service provider routes and authorization handles

The Google transport and provider ID are amended by
[ADR 0021](0021-google-mail-over-imap-smtp-xoauth2.md).
Google's generated Desktop client credential is further amended by
[ADR 0022](0022-google-desktop-client-credential.md).

- Status: accepted
- Date: 2026-07-28

## Context

ADRs 0008 to 0012 make account identity, provider capability, credential-free
discovery, and external credentials first-class. The v0.7 configuration still
models one account as one provider and one web origin because Outlook Web
supplies both mail and calendar through one authenticated session.

That shape does not generalize. A standards account commonly uses IMAP and SMTP
Submission for mail while using CalDAV for calendar. JMAP may supply mail but
not a calendar implementation. A Google or Microsoft API may supply both
services through one grant while exposing different scopes and capability
evidence. Treating each protocol as a separate human account would duplicate
identity and make cross-account results misleading; inventing a composite
provider would hide which adapter performed an operation.

OAuth distribution also needs an explicit boundary. Corresync does not operate
a hosted relay or centrally held client secret, and automatic discovery cannot
initiate consent or administrator review. At the same time, an explicit Google
API or Microsoft Graph route needs a local public-client registration and a
durable authorization that is not stored in configuration.

## Decision

### One account may have separate mail and calendar routes

An account remains one opaque, locally generated identity with one human-facing
alias and optional address. It owns zero or one mail route and zero or one
calendar route. Each route names:

- one explicit provider adapter;
- only the typed, non-secret endpoints that adapter requires;
- an optional external authorization or credential-handle reference;
- service-specific discovery evidence and explicit-selection provenance.

The provider on a result is the provider of the route that produced it, not a
branding label copied from the account. Mail and calendar operations may
therefore carry different provider IDs while retaining the same account ID.

An adapter instance is scoped to exactly one account and one route. Browser
profiles, credential handles, cursors, caches, rate limits, queue state, and
backoff are never shared merely because two routes have the same provider or
endpoint. A provider registry in the outer runtime constructs typed mail and
calendar ports; the application and domain do not import the registry or an
adapter.

Google API and delegated Microsoft Graph are in-tree outer-adapter packages,
not separately versioned plugins. This keeps their typed port contracts,
target/precondition behavior, OAuth scope profiles, and synthetic fixtures in
the same verification boundary as Outlook Web and the standards adapters,
without allowing either provider into the application or domain dependency
graph.

### Configuration version 3 represents routes explicitly

Configuration version 3 replaces the single account `provider`, `origin`, and
`mailbox` fields with nested `mail` and `calendar` route tables. Version 2
Outlook Web accounts migrate losslessly to two Outlook Web routes that share the
same non-secret origin and explicit mailbox routing value. The opaque account
ID and profile namespace do not change.

Provider-specific endpoint fields remain closed and typed. Configuration cannot
hold a password, app-specific password, cookie, bearer token, OAuth
authorization code, access token, or refresh token. Unknown provider fields are
errors; there is no arbitrary options map.

Discovery produces candidates but never writes configuration. Account addition
shows the selected mail and calendar routes before saving them. CLI and MCP
account lifecycle entry points call the same typed use case; MCP changes use a
caller-bound preview/commit pair. Addition through either surface does not
authenticate or resolve a credential, and its review requires a later explicit
local CLI login. Manual endpoints are always available and pass the same strict
TLS and syntax validation.

### Secrets stay behind explicit local handles

Password-bearing standards routes follow ADR 0012: configuration stores only a
credential backend and key reference. The operating-system credential facility
or explicitly configured helper returns a secret on demand to the local session
owner after a prior human consent step. Discovery, MCP, account addition, and
capability probing cannot trigger that access; only explicit local CLI login
may activate the configured route. Credential-reference keys remain private
write input and are omitted from ordinary account route views. Account-add
review is the deliberate exception: it displays each exact service, provider,
backend, and key that approval will bind. A new account cannot reuse a
backend/key pair already owned by another Corresync account; the application
checks ownership before preview and the atomic configuration adapter checks it
again at commit. Mail and calendar routes within the same account may
intentionally share one handle. The complete input also remains bound by the
approval operation digest.

OAuth routes use Authorization Code with PKCE as a local public client and store
the resulting grant only in the operating-system credential facility. A client
ID and redirect URI are identifiers, not authorization material, and may be
configured. Corresync does not ship or request a centrally held client secret.
If a provider requires a client secret for the selected flow, that route is
unsupported unless a later provider-specific decision defines a bounded local
delivery mechanism. ADR 0022 defines that exception for a Google-generated
Desktop client credential; it does not change the Microsoft Graph rule.

The initial Google API distribution model is bring-your-own public client
registration. Selecting it is explicit and shows scopes before opening a
browser. Managed Workspace automatic discovery never starts that flow.
Microsoft Graph follows the same rule and is never an automatic Microsoft
fallback. An existing locally stored valid grant may be selected without
starting a new authorization.

Google and Microsoft web-session routes are separate adapters from their API
routes. A web route may be implemented on a development branch with synthetic
contract fixtures, but it ships in a stable release only after an opt-in live
observation. API support does not permit documentation to imply that a web
route exists, or vice versa.

## Consequences

The configuration schema and migration become more verbose, but they represent
common standards deployments without fake providers or duplicate accounts.
Application services remain unchanged: they receive one typed mail or calendar
port plus provenance for the selected route.

Provider factories, credential backends, and OAuth browser flows remain outer
adapters. This keeps dependencies pointing inward and prevents a new provider
from acquiring its own policy path.

Users of Google API or Microsoft Graph must create or select an appropriate
public-client registration unless a future separately reviewed distribution
model is accepted. That setup cost is preferable to embedding a shared secret,
operating a relay, or unexpectedly creating an administrator-review event.

Account removal must revoke or delete Corresync-owned local authorization
handles where possible and purge every route's state. It never attempts to
delete credentials owned by another application or revoke organization-wide
access.
