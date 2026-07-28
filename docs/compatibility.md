# Compatibility evidence

Compatibility is an evidence claim, not an assumption derived from an OWA
payload fixture. This page distinguishes deterministic coverage from live
mailbox observations.

## Current evidence

<!-- markdownlint-disable MD013 -->

| Boundary | Deterministic | Live | Status |
| --- | --- | --- | --- |
| CLI help/version/config/auth presentation | Unit/golden/NO_COLOR | Local terminal | Deterministic only |
| Config, policy, audit | Unit/race | macOS arm64, Chrome | Observed |
| Local session owner | Unix/Windows contracts | macOS arm64 | Observed |
| Text-only SSH login relay | Unit/IPC/browser projection contracts | Linux amd64, Chrome, sign-in shell | Partial |
| Folder discovery, list/search/body | JSON contracts | Microsoft 365 work/school | Observed |
| Single-message move | Preview/commit + JSON | Move and restore | Observed |
| Single-message read/unread | Preview/commit + JSON | Read and restore unread | Observed |
| Draft and new send | Preview/commit contracts | Save-only and self-send | Observed |
| Reply/forward/HTML/file attachments | Typed validation + OWA JSON contracts | Not run | Deterministic only |
| Attachment metadata/content read | Bounded GetItem/GetAttachment contracts | Microsoft 365 work/school | Observed |
| Message hard delete | Destructive preview/commit + JSON | Not run | Deterministic only |
| Explicit shared/delegated mailbox routing | Config/header contracts | Not run | Deterministic only |
| Calendar list | Two JSON shapes | Primary calendar | Observed |
| Calendar create | Preview/commit + JSON | Appointment and self meeting | Observed |
| Teams link at calendar creation | Closed provider + JSON | Self-attendee meeting | Observed |
| Calendar update/cancel | Versioned preview/commit + JSON | Update and cleanup | Observed |
| All-day/reminder/recurrence/attendee replacement | Typed validation + OWA JSON contracts | Not run | Deterministic only |
| Codex MCP client | Native CLI registration | Empty calendar tool call | Observed |
| Claude Code MCP client | Native CLI registration | Stdio health check only | Partial |
| GitHub Copilot CLI MCP client | Native CLI plan + JSON schema | Not run | Deterministic only |
| Gemini CLI MCP client | Native CLI plan + JSON schema | Not run | Deterministic only |
| Qwen Code MCP client | Native CLI plan + JSON schema | Not run | Deterministic only |
| Qoder MCP client | Native CLI plan + JSON schema | Not run | Deterministic only |
| Kimi Code MCP client | Native JSON schema | Not run | Deterministic only |
| Distribution | 7 archives, 6 packages, 26 SBOMs | Local + release CI | Verified build |

<!-- markdownlint-enable MD013 -->

The live observations were made on 2026-07-18, 2026-07-19, and 2026-07-25 with
synthetic content and no third-party recipient. They show one authorized
environment working; they are not a universal tenant or protocol support claim.
`SECURITY.md` remains the source of truth for supported release versions.

On 2026-07-19, `corresync login --terminal` launched headless Google Chrome on Linux
amd64, rendered the Microsoft sign-in text and eight numbered controls, focused
the email field, returned to the control list with Escape, and cancelled
cleanly. No credential or MFA value was entered, so completion of
authentication, Conditional Access, and OWA session capture remains
unobserved. This is partial relay evidence, not an authentication compatibility
claim.

The distribution row means the verifier checked the complete artifact
inventory, checksums, archive and package contents, both SBOM formats, generated
package-manager manifests, and an isolated Linux first run. It does not imply
native macOS or Windows runtime evidence.

## Release targets

Build and archive coverage is mandatory for these targets:

| OS | amd64 | arm64 |
| --- | --- | --- |
| macOS | tar.gz | tar.gz |
| Linux | tar.gz, deb, RPM, APK | tar.gz, deb, RPM, APK |
| Windows | zip | zip |

Cross-compilation proves that platform-specific code builds; it does not replace
a native browser, IPC, install, or Gatekeeper/SmartScreen observation.

## Opt-in live smoke test

Use only a mailbox and device you are authorized to test. Prefer a dedicated
test mailbox or a harmless folder and never upload browser captures.
For native installation, CLI, Codex, Claude Code, and separately authorized
write gates on another computer, follow the
[manual test checklist](manual-test-checklist.md).

1. Verify the release checksum before extracting or installing it.
2. Set the account origin to the final Outlook origin shown after sign-in. Do
   not add identity-provider origins or URL paths.
3. Run `corresync doctor` and resolve every local failure.
4. Run `corresync doctor --online --json`, completing SSO, MFA, notices, and
   Conditional Access only in the visible browser.
5. Confirm that `session`, `folder_contract`, `mail_contract`, and
   `calendar_contract` pass.
6. Stop the daemon with `corresync daemon stop` when the observation is complete.

The online doctor requests at most one folder metadata row, one inbox metadata
row, and a one-hour calendar window, then discards the results. Its report
contains no folder, message, event, recipient, item count, authorization, or
OWA response body. Review error text for local paths before sharing even this
content-free report.

Write compatibility is a separate, explicit gate. Test save-only draft creation
before new-message sending. Test reply, forward, HTML, and attachments only
against a controlled message and self-recipient. For sending, use a controlled
recipient, review the exact preview, and inspect Drafts and Sent Items after any
unknown transport outcome.
Calendar mutations are not part of online doctor. Do not exercise them without
separate authorization, a controlled calendar and attendee set, and
reconciliation after an unknown outcome. All-day creation, recurrence creation,
reminder changes, and attendee replacement remain unobserved. Recurrence editing
is not implemented. Move, read-state updates, and hard deletion remain separate
write-compatibility observations after save-only draft creation; hard deletion
must use a disposable self-owned message.

The Teams observation used only the signed-in user as attendee. The specialized
create response returned `TeamsForBusiness` and an HTTPS join URL, after which
the event was cancelled. The later calendar-view response did not reliably set
`isOnlineMeeting`; clients must treat the create result as the provisioning
source of truth and must not expect list results to expose the join URL.

Codex discovered the server, selected `calendar_list`, supplied the requested
bounded window, consumed structured content, and returned the empty event
count. Claude Code's native registration and MCP connection health check
succeeded; an end-to-end model tool call was not recorded because the local
Claude OAuth session had expired before inference. That client-account state is
not treated as a server compatibility failure.

## Recording evidence

A useful compatibility observation contains only:

- exact `corresync version --json` output;
- operating system and architecture;
- browser family and version;
- deployment class, such as Microsoft 365 work/school or Outlook.com;
- success or the content-free `corresync doctor --online --json` failure stage;
- observation date.

Do not include tenant names, mailbox addresses, account IDs, message or event
IDs, request IDs, subjects, recipients, bodies, cookies, tokens, canaries,
browser profiles, screenshots, or raw protocol payloads.

## Protocol drift policy

OWA is undocumented and can change without a versioned public contract. A live
failure must be reproduced without sensitive data, represented by a new
synthetic fixture, and fixed behind the typed transport boundary. Never add an
arbitrary action passthrough or capture live payloads into the repository.
