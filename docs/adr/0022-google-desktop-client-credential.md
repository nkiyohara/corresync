# ADR 0022: Google Desktop client credential at the process boundary

- Status: accepted
- Date: 2026-07-30

## Context

Corresync's Google route uses the installed-application Authorization Code flow
with PKCE, unpredictable state, a normal system browser, and an ephemeral IPv4
loopback callback. ADRs 0015 and 0021 treated the Desktop client as a public
client that would exchange an authorization code without a client secret.

An authorized live observation against the project's generated Google Desktop
client completed account selection and consent but Google's token endpoint
rejected the exchange with `client_secret is missing`. Repeating the exchange
with the credential generated for that same Desktop client succeeded. Google's
installed-app documentation describes this value as optional because native
applications cannot keep embedded client credentials confidential; whether it
is required is nevertheless a property of the generated client.

Ignoring the observed requirement leaves the official Google route unable to
complete authentication. Putting the value in TOML, CLI flags, MCP input,
stored grants, source, release metadata, logs, or an authorization URL would
unnecessarily widen its exposure and violate Corresync's configuration and
diagnostic boundaries.

## Decision

For Google only, the local session owner reads the generated Desktop client
credential from `CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` in its inherited process
environment.

- The value is required before a Google route can load, refresh, or create a
  grant. A missing value fails before keyring access or browser launch.
- The value is bounded to 4 KiB and rejects carriage return, line feed, and NUL.
- It is sent only in a TLS request to Google's fixed token endpoint. It is
  never added to the browser authorization URL.
- It is never represented by the configuration schema, CLI or MCP parameters,
  approval/audit/feedback records, the OS-keyring grant, fixtures, logs, or
  error text.
- Authorization Code with PKCE, state validation, exact redirect checks, scope
  derivation, account isolation, and the keyring grant boundary remain
  unchanged.
- Microsoft Graph continues to use a public-client exchange without a client
  secret. This decision does not create a generic confidential-client feature
  or a hosted token relay.

The environment is an explicit deployment boundary, not a claim of
confidentiality against the same local user who runs the installed
application. Packagers and operators must inject the value only into the
Corresync process and keep it out of shell history, service definitions with
broad read permissions, CI output, support bundles, screen recordings, and
public artifacts.

## Consequences

The official generated Google Desktop client can complete the token exchange
without weakening the local-only architecture or making configuration
secret-bearing. A BYO Google registration must use the credential generated
for that Desktop client. The normal browser still owns all Google sign-in,
MFA, account selection, warnings, and consent.

An environment variable can be observed by sufficiently privileged local
software and must be present again when a later token refresh requires client
authentication. That limitation is explicit and preferable to persisting the
credential in Corresync-owned files. A future packaging mechanism may improve
local injection, but it must preserve every exclusion above and requires a new
review if it changes the trust boundary.

## Evidence

Synthetic OAuth tests assert that the credential is present at the token
endpoint while absent from the authorization URL and stored grant. Tests also
cover missing, oversized, and control-character values. The opt-in live
observation used a dedicated synthetic Google account and synthetic
mail/calendar content; it is not part of default tests or CI.
