# ADR 0006: Text-only browser login relay

- Status: accepted
- Date: 2026-07-18
- Amended: 2026-07-28 by
  [ADR 0016](0016-short-corr-command.md)

## Context

The browser-owned authentication decision in [ADR 0002](0002-interactive-browser-session.md)
assumes the user can see a browser window. A session owner running through SSH
often has no display server, but unattended credential submission remains out
of scope. Copying a browser profile or authorization material to another
machine would also widen the trust boundary.

## Decision

Keep visible browser login as the default and add an explicit experimental
`corr auth login --terminal` mode. The session-owning daemon launches the same
isolated Chromium profile headlessly and projects a bounded, sanitized view of
visible page text and interactive controls over authenticated local IPC.

The CLI requires an interactive TTY. It renders numbered controls and relays
activations or individual key events to the selected browser element. It does
not accept piped input, a username or password flag, a complete form value, or
arbitrary JavaScript. Sensitive fields are not echoed. Page paths, queries,
form values, selectors, cookies, and authorization material do not cross the
IPC boundary.

The user still completes every identity-provider decision, MFA challenge,
Conditional Access prompt, and organization notice. The relay does not attempt
to classify or bypass them. CAPTCHA, passkeys, security keys, client
certificates, native dialogs, and controls that cannot be represented through
the page DOM may require visible browser login.

## Consequences

SSH users can complete common text-based Microsoft sign-in flows without X11,
VNC, or a hosted browser surface. The persistent dedicated Chromium profile can
then satisfy later headless starts while its browser session remains valid.

Identity-provider text and transient keystrokes now cross the same owner-only,
credential-authenticated local IPC used by the CLI and daemon. They are never
logged, audited, persisted, exposed to MCP, or accepted from a non-interactive
stream. Terminal output is treated as untrusted text and control characters are
removed before rendering.

The text projection is intentionally less universal than a visual browser.
Unsupported authentication controls fail closed and retain the visible browser
path as the compatibility fallback. This remains interactive authentication,
not unattended login.
