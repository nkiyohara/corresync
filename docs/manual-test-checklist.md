# Manual test checklist

Use this runbook for a published candidate on an authorized computer and
account. Installation, authentication, reads, writes, imports, monitoring, and
remote egress are separate gates. `SKIP` is the correct result when a gate was
not explicitly authorized.

> [!WARNING]
> Never put a password, MFA value, cookie, token, authorization code, canary,
> credential-helper result, approval token, mailbox/calendar content, or private
> source path into a shell history, report, issue, chat, screenshot, or fixture.
> Do not bypass Gatekeeper, SmartScreen, identity policy, consent, or
> organization controls to make a test pass.

## Evidence header

Record locally:

```text
Release and commit:
Observation date:
OS and architecture:
Browser/keyring family and version:
Provider ID and deployment class:
Install surface:
MCP client/version or SKIP:
```

Do not record account alias/ID/address, tenant/endpoint, message/event/folder
identifier, subject, recipient, attendee, body, query, join URL, source path,
request ID, runner arguments, or any authorization material.

## Gate map

<!-- markdownlint-disable MD013 -->
| Gate | Minimum authority | Consequence |
| --- | --- | --- |
| Verify/download/launch | Local files | None outside test directory |
| Config/offline doctor/IPC | Local app state | Starts/stops local owner |
| Account discovery | Public DNS/well-known network | No authentication |
| Login/online doctor | Selected account read access | Interactive provider session |
| Metadata/body/attachment reads | Selected account read access | Private data returned locally |
| Cross-account projections | Every selected account | Private aggregate returned locally |
| MCP reads | Same account access plus local client | Private data reaches MCP host/model |
| Import scan | Selected local export | Private data enters local staging |
| Draft/organization | Mailbox write access | Remote mailbox mutation |
| Send/calendar change | Controlled targets | External notification/write |
| Monitoring notify/queue | Ongoing account read access | Persistent local collection |
| Agent runner/remote egress | Separate runner/egress consent | Automated disclosure/execution |
<!-- markdownlint-enable MD013 -->

## 1. Verify release assets

Download into a new empty directory:

```console
VERSION=vX.Y.Z
RELEASE="${VERSION#v}"
mkdir corresync-test-assets
gh release download "$VERSION" \
  --repo nkiyohara/corresync \
  --dir corresync-test-assets
cd corresync-test-assets
```

Before extraction or package installation:

```console
# Linux
sha256sum --check checksums.txt

# macOS
shasum -a 256 --check checksums.txt
```

Verify exact workflow provenance:

```console
WORKFLOW_ID="https://github.com/nkiyohara/corresync/"
WORKFLOW_ID="${WORKFLOW_ID}.github/workflows/release.yml@refs/tags/${VERSION}"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "$WORKFLOW_ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

Confirm the inventory matches [release engineering](releasing.md): archives,
Linux packages, source archive, per-artifact SPDX/CycloneDX SBOMs, checksum
manifest, and signature bundle. Stop on any missing, extra, or mismatched file.

## 2. Launch the native artifact

Extract the matching archive into a new directory and put it first on `PATH`.
For example:

```console
mkdir ../corresync-under-test
tar -xzf "corresync_${RELEASE}_linux_amd64.tar.gz" \
  -C ../corresync-under-test
export PATH="$(cd ../corresync-under-test && pwd):$PATH"

corr version --json
corr --help
corresync version --json
```

The last command checks only the finite v0.8–v0.9 compatibility entry. Both
executables must report the same version/commit. New help/examples must use
`corr`.

On Windows, extract the matching zip and invoke `corr.exe`. On macOS/Windows,
record a platform-policy failure instead of weakening it.

When testing a Linux package, review its contents before privileged install.
Confirm `corr`, the compatibility entry in its supported window, `corr(1)`,
completion, plugin/Skill, license material, and essential docs are installed.

## 3. Offline and IPC checks

Use an isolated config/state root. These commands must not authenticate or
access a mailbox:

```console
corr config init
corr config path
corr config validate
corr account list
corr doctor --json
corr daemon start
corr daemon status --json
corr daemon stop
corr completion install
corr completion install
```

Pass criteria:

- config contains no password, token, cookie, grant, or client secret;
- schema/version/account routes validate;
- daemon status is content-free and reports protocol/version/config digest;
- no TCP listener appears;
- the second completion install is an exact no-op and no startup line is
  appended;
- a symlink or differently owned/permissive IPC path fails closed.

Do not create hostile IPC paths outside an isolated disposable user or test
directory.

## 4. Update and feedback isolation

```console
corr update check
corr update check --json
corr feedback
corr feedback --last-error
```

The update JSON is one unstyled object. A repeated bounded public check may use
the local cache. A package-managed binary prints its owner-specific update
action and is not replaced. A direct update verifies provenance/checksum before
replacement and retains rollback.

Feedback must print a deterministic allowlisted report with no network
request. Review it manually for private values. If testing an external action,
select exactly one:

```console
corr feedback --copy
corr feedback --save ./feedback-review.json
corr feedback --open-github
```

The report must appear before the action. Save must not overwrite. Opening
GitHub must not submit.

## 5. Discover and configure a route

Credential-free discovery:

```console
corr account discover reader@example.invalid
corr account add reader@example.invalid --help
```

Pass criteria:

- candidates explain evidence, confidence, auth type, and availability;
- no browser/keyring/helper opens during discovery;
- ambiguous evidence requires an explicit provider;
- unavailable routes and the reserved `pop3` provider cannot be selected.

Add only a route approved for the test. Use synthetic aliases and follow
[configuration.md](configuration.md). For standards routes, preload a
dedicated OS-keyring/helper reference out of band. For Google/Graph, use an
authorized public OAuth registration and approve the displayed scopes. Never
place a secret in a flag or config.

```console
corr account list
corr account show ALIAS
corr config validate
```

## 6. Authenticate and run bounded doctor

This is the first mailbox/calendar access:

```console
corr auth login --account ALIAS
corr auth status
corr doctor --online --account ALIAS --json
```

Complete SSO, MFA, consent, and policy only in the visible browser. A helper
must be an explicitly configured absolute executable invoked without a shell.

Pass criteria:

- status contains only local aliases, lifecycle state, capabilities, and
  degradations;
- online doctor reuses the existing session, shows configured OAuth scopes,
  performs bounded checks, and never opens another login/consent flow;
- no secret or provider response body reaches stdout/logs;
- no mailbox/calendar write occurs;
- unavailable behavior appears as a degradation, not a silent provider
  fallback.

The Outlook-Web-only `--terminal` relay is optional, requires a real TTY, and
must reject piped input. Do not record entered or rendered identity values.

For a separately authorized managed Google Web observation, use its opt-in
read-only harness from a source checkout:

```console
read -r -p "Authorized Google address: " CORRESYNC_LIVE_GOOGLE_ADDRESS
export CORRESYNC_LIVE_GOOGLE_ADDRESS
CORRESYNC_LIVE_CONFIRM=google-web-read-only \
CORRESYNC_LIVE_GOOGLE_PROFILE_DIR="$(mktemp -d)" \
mise exec -- go test -tags=live \
  -run TestLiveGoogleWebVisibleRead ./internal/provider/googleweb
unset CORRESYNC_LIVE_GOOGLE_ADDRESS
```

Verify the browser stays visible, both Google surfaces match the configured
identity, only bounded metadata is read, and no cookie/token/storage export or
write occurs. Use a dedicated profile and remove it only through a reviewed
local cleanup after the test. Record only the content-free evidence header.

## 7. Read-only CLI

Keep output on the test machine:

```console
corr mail folders --account ALIAS --json
corr mail list --account ALIAS --limit 5 --json
corr mail search --account ALIAS --query 'provider-specific query' \
  --limit 5 --json
corr calendar list --account ALIAS \
  --start 2026-07-28T09:00:00Z \
  --end 2026-07-28T10:00:00Z \
  --json
```

Optionally test one explicit body and bounded attachment without copying IDs
into the evidence memo. Verify list/search omit bodies/bytes and all terminal
control characters are safely rendered.

With two authorized accounts:

```console
corr mail search --all-accounts --query 'synthetic query' \
  --limit 10 --time-zone UTC --json
corr agenda list --all-accounts \
  --start 2026-07-28T09:00:00Z \
  --end 2026-07-28T10:00:00Z \
  --time-zone UTC --limit 10 --json
```

Verify deterministic ordering, provider/account provenance, global bounds, and
explicit partial failures. No cross-account write must be available.

## 8. Read-only MCP

Inspect before registering:

```console
corr mcp setup codex --dry-run
corr mcp config codex
corr mcp setup codex
codex mcp get corresync
```

Replace `codex` with another supported client and its documented verification
command. Start a fresh client session and request:

```text
List at most five mail metadata rows from one account. Do not read bodies or write.
List one bounded calendar hour. Do not write.
Show monitor status only. Do not enable or change monitoring.
```

Pass criteria:

- client starts `corr mcp serve` over stdio;
- the MCP process receives no provider/session credential;
- tool results preserve provenance/degradations;
- content is treated as untrusted data;
- account add/rename/remove require the matching short-lived MCP preview token,
  and a commit restarts the session owner without starting authentication;
- account removal review discloses any unshared Corresync-owned OAuth grant
  deletion and retains external standards credentials;
- MCP cannot authenticate, configure monitoring/egress, scan imports, purge
  queues, update the binary, or submit feedback.

Do not share raw MCP frames.

## 9. Read-only import staging

Use a disposable synthetic export, never a production profile for a first
test:

```console
corr import scan ./synthetic-export
corr import scan ./synthetic-export --format auto --approve-read --json
corr import purge --account ALIAS --approve
```

The first call must perform no filesystem scan and must request
`--approve-read`. The approved call binds the exact resolved source identity,
reads it, and creates the bounded account-local plan in one operation. Verify
bounds and recognized-format counts locally, then verify purge removes only
account-local Corresync staging and never changes the source.

## 10. Consequential writes

Stop unless each operation and target is separately authorized. Use a
disposable message, controlled recipient, controlled calendar, and synthetic
body. Enable reversible-write previews for the test:

```toml
[policy]
preview_reversible_writes = true
```

Restart the daemon after validation. For each applicable operation:

1. run it without `--approve`;
2. review account/provider/target/version/recipients and content digests;
3. repeat the exact command once with `--approve`;
4. reconcile provider state;
5. never retry an unknown outcome.

Test in increasing consequence:

- save-only `corr mail draft` and confirm nothing was sent;
- `corr mail mark` then restore using the refreshed version;
- `corr mail move` then reconcile both folders;
- `corr mail send` to a recipient controlled by the tester;
- `corr calendar create`, update with the refreshed version, then cancel.

Do not add attendees in the first calendar test. A Teams-link test is permitted
only when the provider reports capability and the sole attendee is controlled
by the tester. Never include the returned join URL in evidence.

Permanent delete requires a disposable self-owned message and separate
authorization. Gmail least-privilege configuration should report it
unavailable.

## 11. Monitoring and dispatch

Use a dedicated account with synthetic incoming messages.

```console
corr monitor status --account ALIAS
corr monitor enable --account ALIAS --mode notify \
  --notification-field sender \
  --notification-field subject \
  --approve
corr events list --account ALIAS --state all
```

On Windows, the `notify` command must fail before changing configuration with
the registered-AppUserModelID explanation. Continue the queue/agent checks
without claiming desktop-notification compatibility.

Verify old/new/imported accounts begin `off`; collection starts only after
authentication and approval; Sent/Drafts/self messages are suppressed where
possible; restart recovery does not duplicate an event; notification values
are bounded and treated as untrusted. During quiet hours or rate limiting,
confirm the provider cursor remains advanced while notification events stay
pending, then confirm a later poll drains them once delivery is allowed.
With a synthetic adapter only, place the prior cursor beyond the 1000-message
recovery window and confirm the poll returns an explicit overflow, monitor
status increments the durable overflow counter/time, and the inspected window
still becomes the new bounded baseline. Do not perform this load test against a
live mailbox.

Queue mode requires a separate step. Test acknowledgement twice and confirm
idempotence. Agent mode requires a disposable absolute runner, direct execution
without a shell, bounded JSON stdin, hourly/circuit limits, and explicit field
allowlist. Remote egress requires a second separate approval.

Disable with an explicit queue decision:

```console
corr monitor disable --account ALIAS --retain-queue --approve
# or, only when authorized:
corr monitor disable --account ALIAS --purge-queue --approve
```

Confirm disabled monitoring collects nothing. Queue purge must remove only the
selected account's local events.

## 12. Finish and record

```console
corr daemon status --json
corr daemon stop
```

Record `PASS`, `FAIL`, or `SKIP`:

<!-- markdownlint-disable MD013 -->
| Gate | Result | Content-free note |
| --- | --- | --- |
| Checksum and Sigstore identity | | |
| Native archive/package and command compatibility | | |
| Config, completion, offline doctor, authenticated IPC | | |
| Update and feedback isolation | | |
| Discovery/account route | | |
| Interactive auth and online doctor | | |
| Single-account CLI reads | | |
| Cross-account projections | | |
| MCP reads | | |
| Import staging | | |
| Draft/mail organization/send | | |
| Calendar create/update/cancel | | |
| Monitor notify/queue | | |
| Agent runner/remote egress | | |
<!-- markdownlint-enable MD013 -->

A shareable report contains only the evidence header and content-free stages.
Use `corr feedback --last-error` and review it before attaching. Do not upload
config/state/cache, browser profiles, keyring/helper output, audit/event/import
files, raw command output, screenshots, or MCP transcripts.

## Failure handling

- Preserve the verified release binary and checksum.
- Record command class, exit code, and generalized failure stage only.
- Reconcile remote state before any action after an unknown outcome.
- Reduce provider drift or malformed-import behavior to a synthetic fixture.
- Do not bypass platform, provider, tenant, or organization controls.
- Stop the daemon before changing config, binary version, or test identity.
- Report security issues privately through [SECURITY.md](../SECURITY.md).
