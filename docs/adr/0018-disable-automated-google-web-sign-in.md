# ADR 0018: Disable automated Google Web sign-in

- Status: accepted
- Date: 2026-07-29

## Context

The v0.8.0 and v0.8.1 `google-web` route launched Gmail and Google Calendar in
a visible, isolated Chrome-family profile controlled through the browser
debugging protocol. It was intended as a bounded read-only alternative that
did not request Google API authorization.

A live personal Gmail sign-in on 2026-07-29 reached Google's “This browser or
app may not be secure” refusal. Google's account-help guidance lists browsers
controlled through software automation among sign-in surfaces it may block:
<https://support.google.com/accounts/answer/7675428>.

Masking `navigator.webdriver`, changing user-agent or launch flags, or adding a
browser-fingerprint “stealth” layer might make one observed challenge behave
differently. It would not create a provider-supported authentication contract.
It would deliberately hide the fact that software controls the browser in
order to get past an account-protection decision. That conflicts with the
permanent prohibition on bypassing authentication, MFA, Conditional Access, a
disabled service, or a permission the user does not have.

The supported Google API route is not a universal automatic replacement.
Consumer accounts require an explicitly authorized public OAuth client.
Workspace administrators can restrict third-party OAuth applications and
Gmail or Calendar API access:
<https://support.google.com/a/answer/7281227>. They can separately control
standards access such as IMAP:
<https://support.google.com/a/answer/105694>.

## Decision

Corresync does not attempt to conceal browser automation from Google.

Credential-free discovery for Gmail and Google-hosted MX records advertises
only `google-api`, labelled as requiring explicit OAuth selection. Automatic
setup does not choose it and does not open authorization. Explicit
`google-web` account selection is rejected with an actionable error.

Configuration keeps the legacy Google Web tagged-union shape so v0.8.0 and
v0.8.1 accounts can still be loaded, inspected, and removed safely. Activating
one of those accounts returns the same actionable error before a browser,
profile, remote connection, or provider adapter is opened. No automatic
migration invents an OAuth client or external credential.

Users may choose:

- `google-api`, with a public OAuth client they are authorized to operate and a
  grant stored through the Corresync OS-keyring port; or
- explicit IMAP/SMTP and CalDAV routes when the account and its administrator
  permit those services and the credential remains behind an approved external
  credential port.

If Google or a Workspace administrator blocks a selected route, Corresync
reports the refusal. It never falls through to another provider, initiates
administrator review from discovery, changes browser fingerprints, weakens
TLS, or asks for a password.

## Consequences

The advertised Google surface is smaller but matches an authentication path
with a documented provider contract. Personal Gmail setup needs explicit OAuth
client configuration until Corresync can lawfully ship a verified public
client. Workspace users may need their administrator to approve that client
and its requested scopes; some organizations will have no permitted Corresync
route.

The dormant semantic-DOM adapter and its synthetic fixtures may remain while
they help parse and test legacy state, but they are not product capability or
live evidence. Documentation, Pages, discovery, CLI selection, and session
activation must all state the same unsupported status.

A separately scoped ordinary-Chrome extension design may be evaluated in
[Issue 59](https://github.com/nkiyohara/corresync/issues/59). It does not
reactivate the route or weaken this decision until a new accepted ADR and its
security/evidence gates are complete.
