# ADR 0025: Explicit public error feedback through the user's GitHub CLI

- Status: accepted
- Date: 2026-08-03
- Amends: the automatic-feedback decision in
  [ADR 0017](0017-privacy-preserving-feedback.md)

## Context

Corresync already replaces one local last-error record and can generate a
manually reviewed feedback report from a closed allowlist. It never stores raw
errors or argument values. That flow is safe but relies on a user noticing the
command, reviewing a report, and filing an issue manually. Early preview users
may reasonably choose to report a reproducible command failure automatically.

GitHub does not permit anonymous issue creation. Shipping a project credential,
accepting a token in Corresync configuration, or introducing a hosted reporting
relay would violate the secret-free configuration and local-first boundaries.
Public issue submission also has a stronger disclosure consequence than an
ordinary update check: the user's GitHub identity and the submitted fields are
public and retained outside the device.

Masking arbitrary error strings after collection cannot prove that a new kind
of identifier, credential, path, or message content was removed. Automatic
submission therefore needs a smaller schema than manually reviewed feedback,
not a broader redaction filter.

## Decision

Automatic error feedback is default-off and can be enabled only by the signed-in
human through the interactive CLI or an explicit local `corr config set
feedback.auto_submit true` command. The interactive flow displays the public
destination, GitHub-identity consequence, complete included field categories,
complete excluded field categories, and de-duplication behavior before asking
for affirmative consent. MCP may display the boolean setting but cannot enable
or disable it.

Enabling verifies that the external GitHub CLI (`gh`) is signed in to
`github.com`. Corresync never reads, accepts, copies, logs, or persists the
GitHub credential. On an eligible interactive CLI command failure, Corresync
invokes the fixed `gh issue create` operation for the fixed public Corresync
repository. There is no shell, configurable destination, label, template,
arbitrary argument, hosted relay, ambient listener, or telemetry service.

The automatic report is newly constructed from this closed allowlist:

- validated Corresync version, commit, build date, Go version, and OS/CPU;
- enumerated installation method;
- a deterministic content-free error fingerprint;
- the static parsed command path, recognized flag names without values, and
  fixed error classes.

The schema has no field for a raw error, positional or flag value, config path,
environment value, account alias/address/ID, provider route, credential,
authorization, audit event, mail, calendar, attachment, import, monitor event,
or runner data. Invalid dynamic input fails closed rather than being reflected.
The public issue body states this boundary and the machine-readable report
marks its opt-in automatic destination.

Automatic submission runs only when stderr is an interactive terminal. It does
not run for MCP stdio, daemon-hosted tool failures, completion, machine-output,
configuration-management, scripts with redirected stderr, canceled commands,
or the feedback command itself. A private, content-free marker permits at most
one submission attempt for each sanitized build/error fingerprint. The marker
contains no time, issue URL/content, or GitHub identity. Submission has a short
deadline; failure is reported with a fixed local message and never changes the
original command's error or exit code.

Disabling `feedback.auto_submit` stops future attempts. It does not delete an
already public GitHub issue or the user's GitHub-side records. The manual
`corr feedback` review/copy/save/open flow remains available independently.

## Consequences

- A user can deliberately trade a small, documented set of public build and
  failure metadata for lower-friction preview feedback.
- Mail/calendar data and raw diagnostic strings never enter the automatic
  reporting pipeline, so privacy does not depend on best-effort masking.
- GitHub CLI installation, authentication, availability, repository permission,
  and GitHub service availability are external prerequisites.
- The GitHub username and issue are public. Privacy and Terms pages must state
  categories, purpose, recipient, retention, withdrawal, and deletion limits.
- Expected or repeated errors cannot produce an unbounded issue storm for one
  build/fingerprint, but a failed first attempt is not retried automatically.
- Corresync still has no analytics collection, crash dump upload, reporting
  relay, persistent device identifier, or automatic feedback by default.
