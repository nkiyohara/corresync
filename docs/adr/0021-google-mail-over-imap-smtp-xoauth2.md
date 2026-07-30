# ADR 0021: Google mail over IMAP/SMTP XOAUTH2

The generated Google Desktop client credential required during token exchange
is amended by [ADR 0022](0022-google-desktop-client-credential.md).

- Status: accepted
- Date: 2026-07-30

## Context

The first provider-neutral Google route used one OAuth grant for Gmail and
Google Calendar REST APIs. It followed the right browser-owned installed-app
authorization flow, but it made Gmail API access part of the mail transport and
presented the route publicly as `google-api`.

Desktop mail clients such as Thunderbird use a simpler provider contract:
the system browser owns Google OAuth, the client receives a refreshable grant,
and Gmail mail traffic uses SASL XOAUTH2 over encrypted IMAP and SMTP
Submission. This avoids password and app-password handling without automating
Google sign-in. Google Calendar and Google Meet still require the Calendar API.

The separate automated `google-web` route remains unsupported under
[ADR 0018](0018-disable-automated-google-web-sign-in.md). It is not a fallback
for API, OAuth, IMAP, or administrator-policy failures.

## Decision

Corresync exposes one current Google provider ID: `google`.

- Mail uses only `imap.gmail.com:993` with implicit TLS and
  `smtp.gmail.com:587` with STARTTLS.
- IMAP and SMTP authenticate only with SASL `XOAUTH2` and a fresh access token
  from the account-scoped OAuth grant. Google mail never accepts a password,
  app password, configurable host, or Gmail REST transport.
- Gmail SMTP stores accepted messages in Sent. Corresync confirms that
  server-created copy by its unique Message-ID and never appends a duplicate.
- Calendar uses the pinned `https://www.googleapis.com` Calendar API route.
  Google Meet remains a typed property of one reviewed calendar-event
  creation when the authenticated calendar advertises `hangoutsMeet`.
- New account setup shares one desktop public-client registration and one
  OS-keyring grant when mail and calendar are configured together. A valid
  schema-v3 account that deliberately used distinct clients remains distinct
  and each explicit grant receives only its service's scopes.
- OAuth opens the normal system browser, uses PKCE and an unpredictable state,
  and accepts the authorization code only on the configured IPv4 loopback path
  and actual bound port.
- The configured account address must be present. The Gmail XOAUTH2 username
  must match it case-insensitively, and Calendar separately verifies the
  authenticated primary-calendar identity.
- Discovery may advertise only fixed Gmail endpoints and the pinned Calendar
  API base. It remains credential-free and always marks Google OAuth as an
  explicit human selection.
- Schema v4 replaces `mail.google_api` with `mail.google` and
  `calendar.google_api` with `calendar.google`. A schema-v3 `google-api`
  account is migrated in memory without changing its stable account ID,
  public-client metadata, authorization key, policy, or source file.
- A matching legacy `google-api` OS-keyring grant may be reused because its
  required mail scope was already `https://mail.google.com/`. Any scope or
  public-client mismatch starts a fresh explicit authorization instead.

The remaining `internal/provider/googleapi` adapter is calendar-only.
Historical Google Web parser code may remain solely for bounded inspection,
removal, and synthetic regression coverage of older configuration. No current
route activates it.

## Security consequences

Gmail bearer tokens are requested immediately before an encrypted protocol
authentication exchange. The mail adapter owns a mutable copy, bounds it,
never logs it, and overwrites it after each attempt. Configuration and MCP
cannot represent OAuth tokens, refresh tokens, passwords, cookies, client
secrets, arbitrary authorization headers, or alternate Gmail hosts.

The full-mail scope is required by Google's documented XOAUTH2 protocol. It is
shown before browser authorization. Workspace policy may disable IMAP or
require administrator approval; Corresync reports that failure and does not
fall back to a web browser, password, another provider, or a broader grant.

Gmail labels are projected through IMAP mailboxes and may therefore be lossy.
The authenticated capability report states that degradation. IMAP/SMTP
mutation ambiguity continues to use the existing reconciliation contract and
is never retried automatically. Gmail's IMAP expunge behavior is configurable
per account, so the Google route disables Corresync's permanent-delete
operation rather than treating an observed `UIDPLUS` capability as proof of
hard-delete semantics.

## Evidence

Default tests use only synthetic TLS IMAP/SMTP servers, synthetic OAuth
endpoints, and synthetic Calendar API fixtures. They verify the exact XOAUTH2
initial response, a fresh token for every authentication, token-buffer erasure,
fixed endpoints, one shared grant, migration, account isolation, and
calendar-only REST behavior. Live mailbox tests remain explicit opt-ins and
outside the default test command and CI.
