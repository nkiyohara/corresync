# ADR 0002: Interactive browser-owned authentication

- Status: accepted; extended by [ADR 0006](0006-text-terminal-browser-login.md)
- Date: 2026-07-17

## Context

Graph application access may be unavailable, while the user is authorized to
use Outlook Web. Direct credential submission fails modern SSO, MFA, and
Conditional Access requirements. TLS interception expands the trust boundary.

## Decision

Launch or attach to an isolated Chromium profile and let the user complete the
normal interactive flow. Do not request the password and do not intercept TLS.
Keep authorization material in the session owner and prefer execution inside
the browser security context when practical. After successful authentication,
minimize the dedicated window when supported while retaining the same browser
process and target. A window-manager failure must not discard authentication.
Do not restart into another profile or copy browser storage to hide the window.

## Consequences

Initial login requires either a visible browser or the explicit text-only
browser relay. Both approaches preserve the identity provider's controls but
depend on undocumented Outlook Web behavior. Protocol capability probes and
fixtures are mandatory.
