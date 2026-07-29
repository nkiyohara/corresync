# ADR 0008: Provider-neutral product scope

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-07-28

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
  join link provisioned as a property of one calendar event remains calendar
  scope exactly as decided in
  [ADR 0005](0005-calendar-hosted-teams-links.md).

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

The read-only Google Web, Google API, Microsoft Graph, JMAP, IMAP/SMTP, and
CalDAV adapters are now implemented behind the shared application boundary.
Their evidence is deterministic-only until separately recorded in
[compatibility.md](../compatibility.md). Outlook Web remains the only provider
with bounded live observations; this evidence difference is not a capability
fallback.
