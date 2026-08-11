# ADR 0008: Provider-neutral product scope

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-08-11

## Context

The project shipped as a single-provider Outlook Web bridge. Its safety model
is deliberately narrow: one browser-owned session, one typed use-case core, and
a server-enforced preview/commit boundary shared by CLI and MCP.

Users hold more than one mail and calendar account, and the reason this project
exists generalizes beyond Microsoft. Organizations frequently permit the
first-party web client while blocking third-party API applications;
universities, hosted providers, and self-hosted deployments expose standards
such as JMAP, IMAP, SMTP Submission, and CalDAV; consumer and managed accounts
of the same provider behave differently.

Issue #20 proposes evolving the product accordingly. The risk is not the extra
providers. It is that a second provider becomes an excuse for a second safety
model, a lowest-common-denominator data model, or documentation that describes
an intended product as though it already existed.

## Decision

Adopt a provider-neutral product scope. Corresync is a local-first CLI and MCP
server for mail and calendar accounts that the signed-in human already
controls, with multiple accounts and multiple providers as first-class
concerns. The name and its migration are decided in
[ADR 0011](0011-coordinated-corresync-rename.md).

One safety model covers every provider. The invariants that apply to Outlook
Web today apply unchanged to every adapter added later: dependencies point
inward, CLI and MCP call the same typed application use cases, consequential
writes use the server-enforced preview/commit protocol, live mailbox tests are
opt-in, and fixtures are synthetic.

Calendar scope includes organizer scheduling semantics, not calendar-object
storage alone. When an event has attendees, create, material update, attendee
replacement, and cancellation must use the selected provider's supported
invitation/update/cancellation path. An adapter must discover scheduling
capability where the protocol requires it, disclose the exact effects in the
preview, and fail before changing the event when it cannot guarantee the
reviewed notification behavior. It must never report a storage-only delete as
an attendee cancellation.

The following remain permanently out of scope rather than merely
unimplemented:

- hosted relays, multi-user servers, and any project-operated credential or
  mailbox intermediary;
- tenant-wide, administrative, or delegated access beyond what the signed-in
  user already holds;
- unattended credential login and TLS interception;
- bypassing authentication, MFA, Conditional Access, a disabled service, or any
  permission the user does not already have;
- silently reading or copying passwords, tokens, or cookies that belong to
  another application;
- Teams chat, channels, calls, recordings, and meeting lifecycle management. A
  provider-native Teams or Google Meet join link provisioned as a property of
  one calendar event remains calendar scope exactly as decided in
  [ADR 0005](0005-calendar-hosted-teams-links.md).

The domain-only public compatibility checker accepted by
[ADR 0027](0027-domain-only-public-compatibility-checker.md) is not a relay or
mailbox intermediary. It receives no address local part, credential, provider
data, or authentication authority and can query only one fixed public DNS
resolver. That narrow service does not broaden the hosted product scope.

Scope is not capability. At the time of this decision, the current release
implemented exactly one provider adapter. A provider may be described as
live-compatible only after synthetic fixture contract tests and a documented
opt-in live observation; documentation must distinguish that claim from an
adapter implemented and deterministically tested only on a development commit.

## Consequences

Provider differences become a modelling problem instead of a scope problem.
[ADR 0009](0009-provider-capability-degradation-contracts.md) makes them
explicit rather than normalizing them away, and
[ADR 0010](0010-account-identity-and-isolation.md) makes the account the
routing and isolation boundary.

The Outlook Web adapter stays first-class and remains the default Microsoft
route. It is not a legacy path awaiting replacement by an API: for tenants that
block third-party applications it is the only route their users are permitted
to take.

The review surface grows with every adapter. That growth is bounded by
requiring each one to reuse the existing typed core instead of introducing a
parallel one, and by ADRs 0012 to 0014, which constrain onboarding, import, and
monitoring.

## Implementation amendment

Gmail-API/Google-Calendar-API, Microsoft Graph, JMAP, IMAP/SMTP, and CalDAV
adapters are implemented behind the shared application boundary. The Google
route is approval-gated by
[ADR 0026](0026-approval-gated-gmail-api-route.md). The former read-only
Google Web route is disabled before browser launch by
[ADR 0018](0018-disable-automated-google-web-sign-in.md); its legacy schema and
synthetic fixtures are not a runtime capability. Remaining route evidence is
deterministic-only until separately recorded in
[compatibility.md](../compatibility.md). This evidence difference is not a
capability fallback.
