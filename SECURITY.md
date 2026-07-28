# Security policy

Corresync handles private mailbox/calendar data and the authority of
interactive browser, OAuth, and standards-provider sessions. Please report
suspected vulnerabilities privately and share the least data needed to
reproduce them.

## Supported versions

| Version | Security support |
| --- | --- |
| 0.7.x | Supported |
| 0.6.2 | Migration bridge only |
| Earlier releases | Unsupported |

The latest stable release is available from
[GitHub Releases](https://github.com/nkiyohara/corresync/releases/latest).
The v0.6.2 bridge exists only to establish trust for the coordinated v0.7
rename; it does not receive general feature or compatibility fixes.

## Report a vulnerability

Use GitHub
[private vulnerability reporting](https://github.com/nkiyohara/corresync/security/advisories/new).
Do not disclose vulnerability details in a public issue, discussion, pull
request, commit, or email thread.

An initial report should include:

- the affected Corresync version and installation method;
- operating system and architecture;
- the security boundary crossed and realistic impact;
- minimal reproduction steps using synthetic data;
- whether the issue requires another local user, an MCP caller, a malicious
  mailbox item, or a compromised release source.

Never include live tokens, cookies, authorization headers, canaries, message or
calendar contents, personal or corporate data, browser profiles, screenshots,
raw Outlook payloads, or reusable approval tokens. Maintainers may request a
smaller synthetic reproducer through the private advisory.

Maintainers will acknowledge the report, reproduce it without live user data,
assess supported releases, and coordinate remediation and disclosure. A public
advisory or release note will not expose reporter data or private mailbox
material.

## Security boundary

Corresync operates only with accounts the local signed-in human already
controls. It does not bypass authentication, tenant policy, MFA, Conditional
Access, disabled services, mailbox permissions, or administrator consent.

The Outlook Web route does not require a third-party Microsoft Graph
application. Google API and Microsoft Graph routes require an explicitly
selected public OAuth client. JMAP, IMAP/SMTP, and CalDAV routes use only an
OS-keyring entry or approved helper reference. None of these models is an
authorization bypass: the provider must already permit the requested operation.

Expected controls include:

- no password, cookie, OAuth grant, helper output, or refresh-token fields in
  configuration, CLI, MCP, audit, or feedback;
- no TLS interception or unattended credential login;
- endpoint-authenticated local IPC and MCP over stdio rather than an ambient
  network service;
- caller-, account-, target-, payload-, expiry-, and single-use-bound approval
  tokens;
- metadata-first reads and explicit sensitive-content access;
- no automatic retry after an ambiguous write;
- monitoring off by default, with separate consent for collection, runner
  execution, content release, and remote egress;
- allowlisted local feedback that prints before any copy/save/browser action
  and never submits automatically;
- signed checksum provenance, per-artifact SBOMs, secret scanning, dependency
  checks, and pinned workflow actions.

Read the complete [threat model](docs/threat-model.md),
[authentication design](docs/authentication.md), and
[release verification guide](docs/install.md#verify-checksums-and-provenance).

## Out of scope for security claims

Corresync is not a multi-user server, remote MCP gateway, hosted relay, endpoint
security product, tenant administration tool, or credential recovery system.
A deployment that exposes its local IPC or MCP process remotely needs an
independent security design and is not covered by the project defaults.

Messages, attachments, calendar fields, import files, event-queue values, and
links are untrusted external input. Agents, scripts, and monitor runners must
not execute instructions found inside them.
