# Repository instructions

## Scope

Build Corresync, a local-first, provider-neutral mail and calendar CLI and MCP
server for accounts the signed-in human already controls. Mail and calendar are
in scope across providers, including a Teams join link provisioned as a property
of one calendar event. See
[ADR 0008](docs/adr/0008-provider-neutral-product-scope.md) for the accepted
scope and [ADR 0011](docs/adr/0011-coordinated-corresync-rename.md) for the
rename.

Scope is not capability. The released binary implements exactly one provider
adapter, Outlook Web. Never document a provider or capability as available
before it has synthetic fixture contract tests and a documented opt-in live
observation.

Teams chat, channels, calls, recordings, and meeting lifecycle management stay
out of scope, exactly as decided in
[ADR 0005](docs/adr/0005-calendar-hosted-teams-links.md). Hosted relays,
multi-user servers, unattended credential login, TLS interception, tenant-wide
or administrative access, and any bypass of authentication, MFA, Conditional
Access, a disabled service, or a permission the user does not already have are
permanently out of scope. Microsoft Graph is never an implicit dependency, an
automatic fallback, or a capability probe; it is used only on explicit user
selection or with an authorization the user already granted.

## Architecture invariants

- Dependencies point inward: adapters and transports may depend on application
  ports; the domain must not import them. Provider adapters translate and hold
  no policy.
- CLI and MCP must call the same typed application use cases. No provider gets
  an escape hatch, an arbitrary action, or a generic property API.
- Where a web adapter is used, authentication is interactive and browser-owned.
  Never request or persist a password, app-specific password, OAuth token,
  cookie, or refresh token in core, and never introduce TLS interception. The
  configuration schema cannot represent a secret; a standards provider that
  needs one reads it through the consent-gated external credential port in
  [ADR 0012](docs/adr/0012-credential-free-discovery-and-explicit-selection.md).
- Discovery is credential-free, requires valid TLS, and never triggers an
  administrator-consent or admin-review flow. Automatic selection never starts a
  Graph or managed Google authorization.
- Accounts are isolated: opaque stable identity independent of address and
  alias, with separate sessions, profiles, cursors, caches, and audit context.
  Reads may aggregate; every write resolves one exact account and container
  first.
- Consequential writes use the server-enforced preview/commit protocol, and the
  approval token stays bound to the exact previewed account, target, and
  payload.
- Capabilities are observed per account after sign-in, never inferred from
  branding. Degradation is reported explicitly and never normalized away.
- Import scanning is read-only and never reuses another application's secrets.
  Monitoring, agent dispatch, and data egress are separate opt-ins that default
  to disabled, and MCP can never enable them.
- MCP annotations describe effects but never replace core policy checks.
- Live mailbox tests are opt-in and cannot run in the default test command or CI.
- Fixtures are synthetic and contain no credentials or personal data.

## Working agreement

- Keep commits narrow and use Conventional Commit messages.
- Update an ADR when changing an accepted architectural decision.
- Run `mise exec -- task verify` before committing.
- Never weaken a security invariant to make a test pass.
