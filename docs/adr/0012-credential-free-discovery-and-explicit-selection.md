# ADR 0012: Credential-free discovery and explicit provider selection

The Google candidate described here is amended by
[ADR 0021](0021-google-mail-over-imap-smtp-xoauth2.md): it now advertises the
`google` route with fixed Gmail IMAP/SMTP endpoints and the Calendar API base.

- Status: accepted
- Date: 2026-07-28
- Amended: 2026-07-29

## Context

The intended onboarding path is a single email address. Inferring a provider
from an address is useful and unreliable: MX records describe inbound delivery
rather than the mailbox or calendar service, and hybrid Exchange, security
gateways, split delivery, forwarding, aliases, and separately hosted calendars
each break the inference.

Two automatic behaviors are actively harmful. A Microsoft Graph authorization
attempted merely to test whether Graph is available can raise an
administrator-approval request in the tenant, which turns a capability probe
into an administrative event the user never asked for. Managed Google Workspace
behaves similarly: third-party API access is usually gated by an administrator,
and an OAuth attempt can generate an admin-review request.

Standards providers create a different conflict. IMAP, SMTP Submission, and
CalDAV deployments frequently still authenticate with a password or an
app-specific password, while a core invariant of this project is that it never
requests or persists one and its configuration schema cannot represent one.

## Decision

### Fresh configuration is provider-neutral

A new configuration contains zero accounts, an empty default-account field,
and no provider route. Configuration initialization never manufactures an
Outlook placeholder or opens authentication. The user-first `corr setup
ADDRESS` command creates that empty local state when necessary, performs the
same credential-free discovery use case as `corr account discover`, and may
add only a candidate that the automatic-selection rules below permit.

Authentication remains a later, explicit, account-specific local CLI action.
The first added account becomes the default; later additions cannot change the
default unless the human requests it. Session-backed commands and MCP client
registration fail with setup guidance while the account catalog is empty.
Existing configurations are never rewritten to this empty state during an
upgrade.

### Discovery is credential-free

Discovery collects evidence without authenticating. It may recognize well-known
consumer domains, inspect MX and relevant SRV records, try standards
`well-known` endpoints for JMAP and CalDAV, and consider Autodiscover and
Thunderbird-style autoconfiguration. Each candidate carries a confidence score
and the evidence that produced it, retained for diagnostics without recording
message content or secrets.

Discovery never transmits a password, app-specific password, bearer token, or
cookie to a candidate endpoint. It requires valid TLS with no downgrade, no
certificate exception, and no interception. Domain inference is evidence and
never proof, so the resolved destination is shown before authentication, and a
manual override is always available and always sufficient on its own.

### Selection is explicit wherever consent is at stake

Automatic selection may choose only routes that authenticate through a
first-party interactive browser session or an authorization the user already
granted. It must never initiate a Microsoft Graph authorization, never initiate
a managed Google Workspace third-party API authorization, and never submit an
administrator review or approval request.

Microsoft therefore defaults to the authenticated Outlook Web adapter. Graph is
used only when a valid Graph authorization is already configured or the user
explicitly selects it. It is never an implicit dependency, an automatic
fallback, or a capability probe. Known Microsoft domains and
Microsoft-hosted MX evidence may advertise Graph as a separate discovery
candidate, but that candidate is always labelled as requiring explicit OAuth
selection; merely discovering it never opens a browser or reads a grant.

The former automatic `google-web` decision is superseded by
[ADR 0018](0018-disable-automated-google-web-sign-in.md). Google consumer
domains and Google-hosted Workspace MX evidence now produce only a
`google-api` provider candidate, and that candidate always requires explicit
selection. Discovery never starts its authorization.

Before an authorization browser opens, the CLI displays the exact mail and
calendar scopes. Submitting consent or an admin-review request always requires
an explicit human action. A blocked API grant never falls through to another
grant, and disabled Gmail, Calendar, or standards access fails clearly rather
than attempting to bypass organization policy. Consumer-versus-managed
inference remains evidence, not proof.

### Password-bearing providers use an external credential facility

The core still never requests or persists a password, and the configuration
schema still cannot represent a password, app-specific password, OAuth token,
cookie, or refresh token.

When a standards adapter requires a secret, it is obtained through a narrow
credential port with exactly two backends: an operating-system credential
facility, or an external credential-helper command that the user configures.
Configuration stores only the backend and its key reference. Every account
requires an explicit human consent step before either backend is used; the
secret is read on demand into the session owner's memory, is never written to
configuration, state, logs, audit records, or MCP output, and is never accepted
as a command-line flag or as the value of an inherited environment variable.

Neither backend is consulted during discovery, capability probing, or automatic
selection, and no MCP tool can read a secret, resolve a reference, trigger a
credential prompt, or authenticate. An MCP account-add preview may persist a
reviewed private handle reference, but its result explicitly requires a later
`corr auth login` invocation from the local CLI before the reference can be
used. The approval digest binds the complete input. Ordinary account reads omit
the private lookup key; the account-add approval review deliberately displays
the exact backend/key handle and rejects cross-account reuse so the human can
see where later login will resolve a secret. This is the single narrow
exception to "no secrets in core": the secret belongs to an external facility
that the human already trusts, and this project holds a reference to it rather
than a copy of it.

## Consequences

Onboarding becomes resolve-then-authenticate instead of one opaque step. A
tenant-blocked Graph path never surprises an administrator, and a managed
Workspace user is never made to file a request they did not intend to file.

Standards providers become usable without weakening the no-password invariant,
at the cost of an operating-system-specific credential backend, an explicit
consent step, and a helper contract to specify and test. A secret held by an
external facility is only as strong as that facility, which is a deliberate
trade against storing one in a file this project controls.

Discovery will sometimes be wrong. It is built to be explainable and
overridable rather than authoritative, so a wrong guess costs a manual
configuration instead of an unwanted authorization request. Corresync ships no
centrally held OAuth client secret; Google API and Graph use an explicitly
selected bring-your-own public-client registration as detailed in
[ADR 0015](0015-per-service-provider-routes.md).

Google Web no longer trades API completeness for consent safety because its
software-controlled browser cannot reliably reach the authenticated provider
surface. Users must explicitly select and authorize `google-api` or configure
a standards route that their account and administrator permit. Corresync does
not disguise automation or fall back around a blocked route.
