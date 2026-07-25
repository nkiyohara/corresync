# Manual test checklist

Use this runbook to test one published release on another computer.
It deliberately separates installation, read-only Outlook access, MCP clients,
and consequential writes so a tester can stop at the authorized boundary.

> [!WARNING]
> Use only a device, mailbox, messages, calendar, and recipients you are
> authorized to test. Complete SSO, MFA, Conditional Access, and organization
> notices only in the visible browser or the explicit text-only browser relay.
> Never put a password or MFA code in a shell command, pipe, issue, chat, or
> test report. Never copy a cookie, bearer value, canary, browser profile, or
> raw OWA response outside the dedicated session.

## Test scope

Record `PASS`, `FAIL`, or `SKIP` for each applicable gate. A failure at one
gate does not authorize bypassing an operating-system or organization control.

| Gate | Required for read-only evidence | Separate authorization required |
| --- | --- | --- |
| Release download and checksum | Yes | No |
| Native launch, config, local IPC | Yes | No |
| Stable-release update check | Yes | No |
| Interactive browser login and online doctor | Yes | Mailbox read access |
| CLI folder, mail, and calendar reads | Recommended | Mailbox read access |
| Codex and Claude Code MCP reads | Per installed client | Mailbox read access |
| Save-only draft | No | Mailbox write access |
| Read-state or message move | No | Write access; harmless message |
| New-message send | No | Controlled recipient; send approval |
| Calendar create/update/cancel | No | Controlled calendar; write approval |

Do not test attendee invitations, meeting updates, or cancellations during the
first pass. They can notify other people and require a separately controlled
attendee set.

## 1. Prepare the computer

Prerequisites:

- a supported native target: macOS, Linux, or Windows on amd64 or arm64;
- Google Chrome, Chromium, or Microsoft Edge;
- GitHub CLI or a web browser for the public release download;
- Cosign for keyless checksum provenance verification;
- an authorized Outlook Web mailbox;
- Codex CLI and/or Claude Code only when testing that MCP client.

Prefer a disposable operating-system user or a computer without an existing
`owa-bridge` profile. Do not run two releases against the same config
and daemon state at once.

Record locally, without committing the result file:

```text
Release:
Commit:
Observation date:
OS and version:
Architecture:
Browser and version:
Deployment class: Microsoft 365 work/school | Outlook.com | other
Install surface: Homebrew | WinGet | Scoop | archive | deb | RPM | APK
Codex version or SKIP:
Claude Code version or SKIP:
```

Do not record a tenant name, mailbox address, account ID, message or event ID,
request ID, subject, recipient, body, token, or screenshot.

## 2. Download the release

The current supported release is `v0.6.0`. Change both variables together when
testing another version.

### macOS or Linux download

Run from a new empty directory:

```console
VERSION=v0.6.0
RELEASE=0.6.0
mkdir owa-test-assets
gh release download "$VERSION" \
  --repo nkiyohara/owa-bridge \
  --dir owa-test-assets
cd owa-test-assets
```

### Windows PowerShell download

Run from a new empty directory:

```powershell
$Version = "v0.6.0"
$Release = "0.6.0"
New-Item -ItemType Directory -Path owa-test-assets | Out-Null
gh release download $Version `
  --repo nkiyohara/owa-bridge `
  --dir owa-test-assets
Set-Location owa-test-assets
```

Expected inventory: 41 files consisting of `checksums.txt`, its Sigstore
bundle, seven archives, six native Linux packages, and 26 SBOM documents. No
filename may contain `~`.

## 3. Verify every downloaded asset

Do this before extracting an archive or invoking a privileged package manager.

### Linux

```console
sha256sum --check checksums.txt
```

### macOS

```console
shasum -a 256 -c checksums.txt
```

### Windows checksum in PowerShell

```powershell
Get-Content .\checksums.txt | ForEach-Object {
  if ($_ -notmatch '^([0-9a-f]{64})\s+(.+)$') {
    throw "Malformed checksum line: $_"
  }
  $Expected = $Matches[1]
  $Name = $Matches[2].Trim()
  if (-not (Test-Path -LiteralPath $Name)) {
    throw "Missing release asset: $Name"
  }
  $Actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Name).Hash.ToLowerInvariant()
  if ($Actual -ne $Expected) {
    throw "Checksum mismatch: $Name"
  }
}
Write-Host "All release checksums passed."
```

Expected result: all 36 entries report success. Stop immediately on a missing
file or mismatch.

Verify that the checksum manifest came from the tagged release workflow:

```console
WORKFLOW_ID="https://github.com/nkiyohara/owa-bridge/"
WORKFLOW_ID="${WORKFLOW_ID}.github/workflows/release.yml@refs/tags/${VERSION}"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "$WORKFLOW_ID" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

The certificate identity must equal the exact repository, workflow, and tag.

## 4. Launch the native archive

Archive testing is required even when a native Linux package will also be
tested. Select the filename matching the actual computer.

### macOS or Linux archive

Example for Linux amd64:

```console
ASSET="owa-bridge_${RELEASE}_linux_amd64.tar.gz"
mkdir ../owa-under-test
tar -xzf "$ASSET" -C ../owa-under-test
export PATH="$(cd ../owa-under-test && pwd):$PATH"
owa version --json
owa --help
```

Use `darwin_amd64`, `darwin_arm64`, `linux_amd64`, or `linux_arm64` as
appropriate. The version must equal the selected release and the commit must
match the tag in GitHub. On macOS, record a Gatekeeper failure rather than
removing quarantine metadata or weakening system policy.

### Windows archive in PowerShell

Example for Windows amd64:

```powershell
$Asset = "owa-bridge_${Release}_windows_amd64.zip"
New-Item -ItemType Directory -Path ..\owa-under-test | Out-Null
Expand-Archive -LiteralPath $Asset -DestinationPath ..\owa-under-test
$Owa = (Resolve-Path ..\owa-under-test\owa.exe).Path
& $Owa version --json
& $Owa --help
```

Use `windows_amd64` or `windows_arm64` as appropriate. Record a
SmartScreen or application-control failure; do not bypass organization policy.
In later PowerShell examples, replace `owa` with `& $Owa` when it is not on
`PATH`.

## 5. Optionally test a native Linux package

Review the matching package before using `sudo`. Install only one format on a
compatible disposable host:

```console
# Debian or Ubuntu
sudo apt install ./owa-bridge_0.6.0-1_amd64.deb
dpkg -L owa-bridge

# Fedora or another RPM-based distribution
sudo dnf install ./owa-bridge-0.6.0-1.x86_64.rpm
rpm -ql owa-bridge

# Alpine
sudo apk add ./owa-bridge_0.6.0-r1_x86_64.apk
apk info -L owa-bridge
```

Confirm that the package installs `owa`, the `owa(1)` manual, shell
completions, the project license, `third_party_licenses`, and the agent plugin
under `/usr/share/owa-bridge`. Then run `owa version --json`. Adjust only the
architecture suffix, not the release number, for arm64.

## 6. Initialize without Outlook access

These commands must not open a browser or contact Outlook. The offline doctor
may read bounded public release metadata unless update checks are disabled:

```console
owa config init
owa config path
owa config validate
owa --version
owa help auth
owa doctor --json
owa daemon start
owa daemon status --json
owa daemon stop
```

Expected results:

- the config contains no password, cookie, OAuth token, or authorization field;
- `config validate` succeeds;
- the offline doctor identifies the selected browser and local IPC readiness;
- daemon status reports the same config digest and release version;
- no TCP listening port is created.

If normal Outlook Web sign-in finishes on a service host other than the
generated `https://outlook.cloud.microsoft` origin, edit only
`accounts.<alias>.origin` to that final HTTPS origin with no path. Do not add an
identity-provider origin or a redirecting vanity host. Validate again and stop
the daemon after every config edit.

### 6.1 Verify update-check isolation

```console
owa update check
owa update check --json
owa update --json
```

The first command should report the current stable release or an
installation-specific upgrade action. The second output must be one valid JSON
object with no human `Update available:` line before or after it. Repeat it and
confirm `cached` is `true`; do not expect a second network request within 24
hours. Run the third command only from the release version under test: it must
return one unstyled action-result object and report `current` without changing
files. When rehearsing an older direct archive in a disposable directory, it
must instead verify provenance, update to the selected release, retain the old
binary as a rollback copy, and report `updated`. A package-managed rehearsal
must report `action_required` and the exact owner command without changing
files. Temporarily set `updates.disable_automatic_checks = true`, validate the
config, and confirm the offline doctor's `update` row is `skip`. Restore the
setting afterward. MCP and completion byte streams are covered by deterministic
tests and must never be modified for a startup notice.

## 7. Run the bounded read-only compatibility check

This is the first step that accesses a live mailbox. Confirm authorization
before continuing.

```console
owa auth login --json
owa auth status --json
owa doctor --online --json
owa daemon status --json
```

For the default path, complete sign-in, MFA, notices, and Conditional Access in
the visible dedicated browser; do not type credentials at a shell prompt. The
online doctor may request at most one folder metadata row, one inbox metadata
row, and one one-hour calendar window, then discards them.

Pass criteria:

- `auth status` contains configured aliases and content-free state only;
- `session`, `folder_contract`, `mail_contract`, and `calendar_contract` pass;
- stdout contains no mailbox address, folder or item data, authorization, or
  OWA response body;
- no Outlook item changes.

For the optional SSH relay check, run `owa auth login --terminal` instead of
`owa auth login --json`. Enter browser-field keystrokes only while its
interactive relay is active; piped input is rejected. Record only whether the
flow passed, not identity-provider page text or entered values. Then run the
remaining JSON commands unchanged.

The JSON report is designed to be content-free, but review error strings for
local paths before sharing it.

## 8. Exercise read-only CLI commands

Keep all output on the test computer. Use small limits first:

```console
owa mail folders --json
owa mail list --limit 5 --json
owa mail search --query 'kind:email' --limit 5 --json
```

Choose one harmless message from the local output and test its bounded
plain-text body without copying its ID into the result memo:

```console
owa mail body --message-id 'opaque-message-id' --json
```

Choose an authorized one-hour calendar window and replace both example values:

```console
owa calendar list \
  --start 2026-07-20T09:00:00Z \
  --end 2026-07-20T10:00:00Z \
  --json
```

Pass criteria:

- folder discovery is bounded and returns no credentials;
- mail list and search return metadata, not bodies or attachment content;
- body output is plain text and at most 1 MiB;
- calendar output excludes bodies, attendees, attachments, and meeting links;
- terminal control characters do not affect the terminal.

Record only pass/fail and the failing command class. Do not record returned
names, counts, IDs, change keys, subjects, senders, times, or bodies.

## 9. Test Codex MCP

Skip this section when Codex is not installed. Review before registration:

```console
owa mcp setup codex --dry-run
owa mcp config codex
```

Register through the official client CLI and inspect the entry:

```console
owa mcp setup codex
codex mcp get outlook-web
```

In a new Codex session, ask it to perform only these read-only operations, one
at a time:

```text
Use only the owa mail_list_folders tool. Do not read message bodies or write.
Use only the owa mail_list tool with limit 5. Do not write.
Use only the owa calendar_list tool for this one-hour RFC3339 window. Do not write.
```

Pass criteria: Codex starts `owa mcp serve`, the first account call can reuse or
open the visible browser session, structured results return, and no Outlook
write or authorization material enters the client logs.

## 10. Test Claude Code MCP

Skip this section when Claude Code is not installed. Review before
registration:

```console
owa mcp setup claude-code --dry-run
owa mcp config claude-code
```

Register and inspect the entry:

```console
owa mcp setup claude-code --scope user
claude mcp get outlook-web
```

In a new Claude Code session, use the same three read-only prompts from the
Codex section. Pass criteria are identical. Do not copy MCP payloads containing
mailbox data into the result memo.

## 11. Optional write-compatibility gates

Stop here unless each operation class is separately authorized. Use synthetic
subjects and bodies, a harmless dedicated message, a controlled calendar, and
a recipient you control. Never use production recipients or attendees for a
first test.

Before reversible writes, set this in the generated config and restart the
daemon:

```toml
[policy]
preview_reversible_writes = true
```

```console
owa config validate
owa daemon stop
```

Every first call below must be treated as a preview. Review it, then repeat the
exact command with `--approve`. A write request is attempted once. If the CLI
reports an unknown outcome, do not retry; reconcile the corresponding Outlook
folder or calendar first.

### 11.1 Save-only draft

Use a controlled address and a synthetic body file:

```console
owa mail draft \
  --to tester-controlled@example.com \
  --subject 'owa-bridge synthetic draft' \
  --body-file ./synthetic-body.txt
```

After reviewing, repeat with `--approve`. Confirm exactly one new item in
Drafts and no item in Sent Items. On an unknown outcome, inspect Drafts before
another attempt.

### 11.2 Read-state update

Select one dedicated test message and use its current ID and change key:

```console
owa mail mark \
  --message-id 'opaque-test-message-id' \
  --change-key 'opaque-current-change-key' \
  --state read
```

Repeat with `--approve`, refresh the message list, and confirm only that one
property changed. Refresh the change key before any later operation.

### 11.3 Message move

Discover a harmless destination folder ID first, then preview one dedicated
test-message move:

```console
owa mail move \
  --message-id 'opaque-test-message-id' \
  --change-key 'opaque-current-change-key' \
  --destination-id 'opaque-test-folder-id'
```

Repeat with `--approve` and list both source and destination. Never retry an
unknown outcome without checking both folders.

### 11.4 Controlled self-send

Use only a recipient you control:

```console
owa mail send \
  --to tester-controlled@example.com \
  --subject 'owa-bridge synthetic self-send' \
  --body-file ./synthetic-body.txt
```

Mail send always previews. Review every recipient, subject, body preview, byte
count, and digest before repeating with `--approve`. Confirm one Sent Items
copy and one controlled delivery. On an unknown outcome, inspect Sent Items
before doing anything else.

### 11.5 Appointment without attendees

First test an appointment that cannot notify another person:

```console
owa calendar create \
  --subject 'owa-bridge synthetic appointment' \
  --start 2026-07-20T09:00:00Z \
  --end 2026-07-20T09:15:00Z \
  --body-file ./synthetic-body.txt
```

Repeat with `--approve`, list that window, and capture the ID and change key
only in the local terminal. Then preview an update:

```console
owa calendar update \
  --event-id 'opaque-test-event-id' \
  --change-key 'opaque-current-change-key' \
  --subject 'owa-bridge synthetic appointment updated'
```

Repeat with `--approve`, list again to obtain the refreshed change key, then
preview cancellation:

```console
owa calendar cancel \
  --event-id 'opaque-test-event-id' \
  --change-key 'opaque-refreshed-change-key'
```

Repeat with `--approve` only after verifying the exact event scope. Confirm the
event is absent from the original window. Do not add attendees during this
runbook.

### 11.6 Teams link with a controlled self attendee

Only when the organizer address is also a recipient you control, prepare a
second event with that address as the sole attendee:

```console
owa calendar create \
  --subject 'owa-bridge synthetic Teams appointment' \
  --start 2026-07-21T09:00:00Z \
  --end 2026-07-21T09:15:00Z \
  --required-attendee tester-controlled@example.com \
  --teams-meeting
```

Confirm the preview lists exactly one controlled attendee, invitations enabled,
and the Teams option enabled before repeating with `--approve`. A successful
JSON commit should contain one HTTPS `onlineMeetingJoinUrl` on a Microsoft Teams
domain. CalendarView may not reliably report its online-meeting flag; do not
expose or persist the join URL in shared evidence. Cancel the event with its
latest change key and confirm it leaves the original window. Never substitute a
third-party attendee merely to test delivery.

## 12. Stop and record content-free evidence

```console
owa daemon status --json
owa daemon stop
```

Complete this table locally:

| Gate                                      | Result | Content-free note |
| ----------------------------------------- | ------ | ----------------- |
| Checksum 39/39                            |        |                   |
| Native archive launch                     |        |                   |
| Native package, if applicable             |        |                   |
| Stable-release update check               |        |                   |
| Config and offline doctor                 |        |                   |
| Local IPC start/status/stop               |        |                   |
| Visible login                             |        |                   |
| Online doctor contracts                   |        |                   |
| CLI mail reads                            |        |                   |
| CLI calendar read                         |        |                   |
| Codex MCP or SKIP                         |        |                   |
| Claude Code MCP or SKIP                   |        |                   |
| Save-only draft or SKIP                   |        |                   |
| Read-state or SKIP                        |        |                   |
| Move or SKIP                              |        |                   |
| Controlled send or SKIP                   |        |                   |
| Calendar create/update/cancel or SKIP     |        |                   |

A shareable report contains only `owa version --json`, OS and architecture,
browser family and version, deployment class, observation date, and the
content-free pass/fail stages above. Review even those files before sharing.
Do not upload the dedicated browser profile, config directory, state directory,
audit log, screenshots, raw stdout from mailbox commands, or MCP transcripts.

## Failure handling

- Preserve the failing release binary and its verified checksum.
- Record the command class and exit code, not mailbox data.
- For protocol drift, reduce the failure to a synthetic fixture before adding
  it to the repository.
- For an unknown write outcome, reconcile Outlook state and do not retry.
- For Gatekeeper, SmartScreen, Conditional Access, or organization-policy
  rejection, record the boundary and do not bypass it.
- Stop the daemon before changing config, binary version, or test account.

The evidence policy and smaller bounded smoke test are documented in
[compatibility evidence](compatibility.md).
