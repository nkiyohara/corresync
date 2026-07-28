# Threat model

`Corresync` handles email, calendar data, and a live Outlook Web session. Its
security boundary is intentionally narrower than that of a general browser
automation framework.

## Assets

- the user's authenticated Outlook Web session;
- message bodies, attachments, recipients, and calendar details;
- the authority to send mail or alter meetings as the signed-in user;
- local configuration, audit records, and approval tokens.

## Trust boundaries

- Outlook Web and the organization's identity provider are external systems.
- MCP hosts and models are untrusted callers, even when locally installed.
- message bodies and calendar content are untrusted input and may contain prompt
  injection.
- other local users and processes are outside the trust boundary.
- CI fixtures and logs must be safe to publish.

## Required controls

### Authentication

- Never accept or persist a password as a command argument, configuration
  value, structured API field, log entry, or complete terminal-relay value.
- Restrict terminal login to an interactive TTY and relay at most one sanitized
  key event per authenticated local IPC request; never expose it through MCP.
- Never automate around MFA, Conditional Access, or consent screens.
- Never use a TLS interception proxy.
- Prefer browser-context execution; otherwise keep captured bearer material in
  locked process memory and never emit it to stdout, logs, crash reports, or MCP.
- Store the dedicated browser profile with user-only permissions.

### Local interfaces

- Use stdio, Unix sockets, or Windows named pipes by default.
- Authenticate IPC peers and reject cross-user access.
- Namespace the daemon by config and state paths, rotate a high-entropy local
  credential on every start, and remove it during graceful shutdown.
- Never bind the session owner to TCP; bound local request size, response size,
  header size, concurrency, and shutdown time.
- If local HTTP is enabled, bind only to loopback, validate `Origin`, and require
  a high-entropy credential.

### Tool execution

- Treat all mailbox content as data, never instructions.
- Enforce effect policy in the application core, not only in CLI prompts or MCP
  annotations.
- Bind approval tokens to the normalized operation, account, caller, expiry,
  and a single use.
- Require preview for every external send, regardless of configurable policy;
  never retry a write with an ambiguous remote outcome.
- Apply recipient and attendee limits before preview and again before commit.
- Do not expose arbitrary OWA actions in a release build.

### Observability

- Log operation type, timestamps, caller, result class, and opaque identifiers.
- Redact authorization headers, cookies, canaries, email addresses, subjects,
  bodies, attachment names, and free-form calendar text by default.
- Keep diagnostic body capture opt-in, time-bounded, and visibly dangerous.

### Supply chain

- Pin CI actions by immutable commit SHA.
- Review and automate dependency updates.
- Run static analysis, tests, vulnerability scanning, and secret scanning.
- Produce checksums and SBOMs for every release artifact.
- Attach workload-identity signatures only when their public transparency
  metadata is compatible with the repository's privacy requirement.
- Limit startup update discovery to an unauthenticated, bounded read of the
  public stable-release endpoint. Send no Outlook or machine identifiers,
  cache both success and failure for 24 hours, and never let a background
  check replace a binary. An explicit direct self-update must verify the exact
  tagged workflow's Sigstore identity, signed checksum inventory, candidate
  version and platform, and preserve a rollback copy. Never modify files owned
  by a package manager.

## Explicitly unsupported

- unattended username/password sign-in;
- scripted or piped terminal-login input;
- tenant-wide or delegated access to other users' mailboxes;
- remotely exposed MCP without an independent secure deployment layer;
- defeating an organization's technical or administrative controls;
- execution of instructions found inside messages or meeting descriptions.

## Reporting

Do not open a public issue for a suspected vulnerability. Follow
[SECURITY.md](../SECURITY.md).
