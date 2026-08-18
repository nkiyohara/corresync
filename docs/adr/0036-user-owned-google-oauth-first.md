# ADR 0036: User-owned Google OAuth first

- Status: accepted
- Date: 2026-08-18
- Supersedes: the process-environment credential decision in
  [ADR 0022](0022-google-desktop-client-credential.md), the production approval
  gate in [ADR 0026](0026-approval-gated-gmail-api-route.md), and the Google
  Tasks approval gate in [ADR 0031](0031-approval-gated-google-tasks-route.md)

## Context

The Gmail, Google Calendar, and Google Tasks adapters are implemented against
synthetic contracts, but a Corresync-operated OAuth identity would make the
project responsible for Google's public-app verification and any applicable
security assessment. That cost and operational role are not required for the
local-first product: the signed-in human can instead create a Desktop OAuth
client in a Google Cloud project they control and authorize only their own
account.

The previously staged route used one release-wide approval gate and expected a
generated Desktop client credential in a process environment variable. That
made a future managed client possible, but it did not offer a safe, durable,
or approachable path for a user's own client. It also conflated two different
authorities: Corresync's decision to operate a managed OAuth identity and the
user's explicit decision to operate their own.

## Decision

Corresync separates the routes permanently:

- user-owned Google Desktop OAuth is available through the explicit `google`
  and `google-tasks` routes;
- Corresync-managed Google OAuth remains disabled by a release-owned constant
  with no environment, configuration, CLI, MCP, discovery, or fallback
  override;
- credential-free discovery may recommend user-owned setup but never creates a
  Cloud project, enables an API, opens OAuth, or submits an administrator
  request;
- setup and account addition only persist reviewed route metadata. OAuth starts
  later, only from an explicit local CLI login. MCP cannot initiate it.

The user supplies Google's downloaded `installed` client JSON. Corresync parses
at most 32 KiB, rejects unknown fields, Web-application clients, non-Google
authorization or token endpoints, malformed credentials, and non-loopback
redirects. The import writes only the generated client credential to the OS
keyring. Configuration contains the public client ID and a consented external
credential handle, never the credential value. An already-managed helper may
be selected instead of import under the existing external credential contract.

The client credential is resolved only for a code exchange or refresh, bounded
to 4 KiB, sent only to Google's fixed TLS token endpoint, and overwritten in
owned mutable memory afterward. It is absent from authorization URLs, stored
grants, config, CLI/MCP JSON, logs, feedback, audit output, and support text.
The downloaded source file remains user-owned and is never deleted implicitly.

Mail and Calendar may share one grant only when client ID, redirect, grant
handle, and client-credential handle match exactly. Google Tasks retains its
own provider identity and task-only grant; it may reference the same Desktop
client credential but cannot reuse or widen the mail/calendar grant. Every
credential binding remains account-isolated.

Configuration schema v11 adds the external Google client-credential reference.
Versions 3 through 10 cannot manufacture that new consent. A legacy Google
route therefore fails closed, leaves the file unchanged, and points to the
manual backup/removal/re-add recovery guide; non-Google routes migrate without
new authority.

The guide explains Google's current audience choices, test-user expiry,
unverified-app warning, organization policy, exact APIs, exact scopes, Desktop
client creation, secure import, login, revocation, and troubleshooting. Google
Cloud remains the user's administrative boundary. Corresync never requests a
password, service account, domain-wide delegation, administrator role, or
hosted callback.

## Consequences

People can use the Google adapters without Corresync operating or paying for a
public OAuth verification program. They do take responsibility for their Cloud
project, consent-screen identity, quota, test or production audience, policy
compliance, credential lifecycle, and any Workspace administrator approval.
Personal and internal-use exemptions, warnings, limits, and authorization
lifetime remain Google decisions and are reported rather than bypassed.

The setup has more steps than a managed client, so the guided wizard imports
the downloaded client into the OS keyring and the documentation mirrors the
current browser screens. A missing keyring value fails before a Google browser
or API request. Ordinary discovery, account reads before login, doctor, and MCP
calls never resolve it.

The adapters remain deterministic-only and live-unobserved until an authorized,
commit-bound observation is recorded. Enabling a future Corresync-managed
client requires a separate accepted decision, verified provider status,
reviewed release, updated public claims, and the same or stronger secret,
account, consent, and OAuth boundaries. It cannot silently replace or probe a
user-owned route.
