# Live evidence index

Live compatibility requires a content-free record tied to the exact commit
that was exercised. Passing fixtures, cross-compilation, or an observation of
an earlier build does not promote a current implementation to “observed.”

## v0.9 marker

No route that ships in v0.9 has a commit-bound live observation yet. The one
committed record, the
[macOS arm64 Google observation](google-macos-arm64-2026-07-30.md), covers the
retired Google IMAP/SMTP transport. It is historical context and is not
evidence for the current `google` API route. Every row in the index below
stays **live-unobserved** until a record made with its harness or procedure is
committed here against the exact tested revision. Native-platform IPC
implementations and MCP clients also remain live-unobserved.

Published distribution evidence remains bound to its exact tag and workflow;
it does not promote provider or native-platform rows to live-observed.
Historical Outlook Web, local terminal, macOS IPC, Codex, and Claude notes did
not capture an exact commit and therefore remain non-evidentiary context.

The [v0.8 acceptance map](acceptance-v0.8.md) links the v0.8 roadmap criteria
to code and deterministic tests. It does not promote any row to live-observed.

## Harness index

Each shipped route has one opt-in harness or a written manual procedure. A
harness is a Go test that compiles only with `-tags live` and skips unless its
route is confirmed by name. Default tests, the race run, `task verify`, commit
hooks, and CI never set the tag for a test run or any `CORRESYNC_LIVE_`
variable; the guard test in `internal/livetest` fails if one does. CI and
`task verify` compile the harnesses with `go vet -tags live ./...` but never
run them.

Every harness needs the four common variables below. The table lists only
what each route adds.

<!-- markdownlint-disable MD013 -->
| Route | Harness or procedure | Extra variables | What it records | Cleanup |
| --- | --- | --- | --- | --- |
| `outlook-web` | `cmd/corr/outlook_web_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_CALENDAR_NAME` | Sign-in; folder and Inbox counts; calendar and seven-day event counts. With writes: one draft and one calendar event. | The draft is deleted and the event cancelled, then each is looked up and must be gone. |
| `outlook-web-forwarded-login` | manual procedure: [forwarded login view](../manual-test-checklist.md#forwarded-login-view-observation) | None; needs a host without a display | Whether `corr auth login` completed through the forwarded view, then the online doctor's check names and results. | Log out and delete the live-only state directory. |
| `google` | `cmd/corr/google_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_CALENDAR_NAME` | Sign-in; Gmail folder and Inbox counts; calendar and event counts. With writes: one calendar event only. Gmail stays read-only because this route cannot permanently delete a draft. | The event is cancelled and must be gone. |
| `google-tasks` | `cmd/corr/google_tasks_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_TASK_LIST_NAME` | Sign-in; list count; task count of the first list. With writes: one task. | The task is deleted and must be gone. |
| `microsoft-graph` | `cmd/corr/microsoft_graph_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_CALENDAR_NAME` | Same as `outlook-web`, through Microsoft Graph. | Same as `outlook-web`. |
| `microsoft-todo` | `cmd/corr/microsoft_todo_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_TASK_LIST_NAME` | Same as `google-tasks`, through Microsoft Graph. | The task is deleted and must be gone. |
| `jmap` | `cmd/corr/jmap_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE` | Sign-in; folder and Inbox counts. With writes: one draft. | The draft is deleted and must be gone. |
| `imap-smtp` | `cmd/corr/imap_smtp_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE` | Same as `jmap`. Nothing is sent over SMTP. | The draft is deleted and must be gone. |
| `caldav` | `cmd/corr/caldav_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_CALENDAR_NAME` | Sign-in; calendar and seven-day event counts. With writes: one event. | The event is cancelled and must be gone. |
| `caldav-tasks` | `cmd/corr/caldav_tasks_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_TASK_LIST_NAME` | Same as `google-tasks`, for VTODO. | The task is deleted and must be gone. |
| `todoist` | `cmd/corr/todoist_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_TASK_LIST_NAME` | Same as `google-tasks`. | The task is deleted and must be gone. |
| `ticktick` | `cmd/corr/ticktick_live_test.go` | Writes: `CORRESYNC_LIVE_WRITE`, `CORRESYNC_LIVE_TASK_LIST_NAME` | Same as `google-tasks`. | The task is deleted and must be gone. |
| `teams-graph` | `cmd/corr/teams_graph_live_test.go` | None; read-only | Sign-in; conversation count; message count of the first conversation; whether either page was partial. | None: nothing is written. |
| `teams-web` | `cmd/corr/teams_web_live_test.go` | None; read-only | Same as `teams-graph`, through the Teams web browser. | None: nothing is written. |
| `mattermost` | `cmd/corr/mattermost_live_test.go` | None; read-only | Same as `teams-graph`. | None: nothing is written. |
<!-- markdownlint-enable MD013 -->

Messaging routes have no write. A posted message notifies other members and a
deleted one leaves a visible trace, so no messaging write can be undone.

The iCloud preset is the `imap-smtp` and `caldav` routes; run each harness on
its own alias. Three older adapter-level, read-only harnesses remain:
`internal/provider/graphapi/task_live_test.go`,
`internal/provider/todoist/todoist_live_test.go`, and
`internal/provider/ticktick/ticktick_live_test.go`. They are described in the
[manual test checklist](../manual-test-checklist.md). The Google Web harness in
`internal/provider/googleweb` belongs to the retired route.

## Opt-in variables

<!-- markdownlint-disable MD013 -->
| Variable | Required | Meaning |
| --- | --- | --- |
| `CORRESYNC_LIVE_CONFIRM` | Yes | The route name from the index, exactly. Any other value skips the harness. |
| `CORRESYNC_LIVE_ACCOUNT` | Yes | The account alias to use. Every active service of that alias must belong to the route. |
| `CORRESYNC_LIVE_CONFIG` | Yes | Absolute path of the config file that holds the alias. |
| `CORRESYNC_STATE_DIR` | Yes | Absolute path of a state directory used only for live runs, so browser profiles and session state stay apart from everyday use. |
| `CORRESYNC_LIVE_WRITE` | For writes | Exactly `ROUTE:ALIAS`, for example `caldav:live-caldav`. Without it the run is read-only. |
| `CORRESYNC_LIVE_CALENDAR_NAME` | For calendar writes | Display name of a calendar you set aside for tests. It must match exactly one editable calendar. |
| `CORRESYNC_LIVE_TASK_LIST_NAME` | For task writes | Display name of a task list you set aside for tests. It must match exactly one editable list. |
| `CORRESYNC_LIVE_RECORD` | No | Absolute path for the record file. The file must not exist. |
| `CORRESYNC_LIVE_DEPLOYMENT_CLASS` | No | One of `consumer`, `work-or-school`, `education`, `government-cloud`, `self-hosted`, `hosted-service`. |
| `CORRESYNC_LIVE_PLATFORM` | No | OS version, such as `macOS 26.5.2`. Letters, digits, spaces, and `._()+-` only. |
| `CORRESYNC_LIVE_BROWSER_KEYRING` | No | Browser and keyring family and version, with the same character limits. |
<!-- markdownlint-enable MD013 -->

The alias, config path, and target names steer the run but never appear in the
record.

## Making one observation

Use an account you control and are allowed to test with. For a write run, use
a calendar or task list you created for tests; mail drafts go to that
account's own Drafts folder and are never sent.

1. Check out the exact commit in a clean tree. The harness refuses to record
   when tracked files are modified.

   ```console
   git switch --detach COMMIT
   mise exec -- task build
   ```

2. Make a live-only config and state directory, and add one alias for the
   route with the binary you just built. Follow
   [configuration](../configuration.md) for the route's `account add` flags
   and credential handle.

   ```console
   export CORRESYNC_STATE_DIR="$HOME/corresync-live/state"
   LIVE_CONFIG="$HOME/corresync-live/config.toml"
   mkdir -p "$CORRESYNC_STATE_DIR"
   ./bin/corr --config "$LIVE_CONFIG" config init
   ./bin/corr --config "$LIVE_CONFIG" account add ADDRESS --help
   ```

   If a daemon is running for that config, stop it first:
   `./bin/corr --config "$LIVE_CONFIG" daemon stop`. The harness refuses to
   start next to one.

3. Run one harness. This example is a read-only Outlook Web run:

   ```console
   CORRESYNC_LIVE_CONFIRM=outlook-web \
   CORRESYNC_LIVE_ACCOUNT=live-owa \
   CORRESYNC_LIVE_CONFIG="$LIVE_CONFIG" \
   CORRESYNC_LIVE_RECORD="$HOME/corresync-live/outlook-web.md" \
   mise exec -- go test -tags live -count=1 -v ./cmd/corr \
     -run '^TestLiveOutlookWebRoute$'
   ```

   For another route, change `CORRESYNC_LIVE_CONFIRM`, the alias, and the
   test name from the index. For a write run, also set, for example,
   `CORRESYNC_LIVE_WRITE=outlook-web:live-owa` and
   `CORRESYNC_LIVE_CALENDAR_NAME="Corresync tests"`.

4. Sign in when asked. A browser-backed route opens its own visible browser;
   an OAuth route opens the system browser; a standards route asks the OS
   keyring or credential helper. Complete SSO, MFA, and consent there. The
   harness never types, reads, or stores a credential.

5. Read the record the test prints (and writes to `CORRESYNC_LIVE_RECORD`).
   Check that it holds nothing private, then commit it here under the
   suggested file name. A failed or degraded run is still a valid record.

6. Clean up when you no longer need the alias. Review the removal first, then
   approve it; this deletes Corresync-owned local state and any unshared
   Corresync-owned OAuth grant, and keeps external standards credentials:

   ```console
   ./bin/corr --config "$LIVE_CONFIG" account remove ALIAS
   ./bin/corr --config "$LIVE_CONFIG" account remove ALIAS --approve
   rm -r "$HOME/corresync-live"
   ```

   Remove a standards credential from the OS keyring or helper yourself, and
   revoke the OAuth consent at the provider if you do not want to keep it.

### When cleanup is not confirmed

Every synthetic item is titled `Corresync live harness` followed by eight hex
characters. After a write, the harness deletes or cancels the item and then
looks it up again. If the lookup still finds it, cannot finish, or the create
outcome was unknown, the record says `cleanup NOT CONFIRMED` and the test
fails with the exact title to look for. Remove that item by hand from the test
calendar, task list, or Drafts folder. The harness never retries a write on
its own; do not rerun a write until the item is gone.

## What a record contains

A harness records only:

- the commit, the date, the OS and architecture, and the optional operator
  fields above;
- the route name, the provider IDs, and whether writes were enabled;
- each step's fixed operation name, outcome (`pass`, `degraded`, `fail`,
  `unobserved`, or `skipped`), item count, and duration in milliseconds;
- a fixed error class for a failed step, such as `timeout`, `http-403`,
  `policy-denied`, or `authentication-required:CODE`;
- the names of the capabilities observed after sign-in, and degradation
  feature codes with their lossy flag;
- the overall result and the cleanup state.

It never records message bodies or subjects, event or task titles, addresses,
aliases, display names, folder, calendar, list, or item IDs, tokens or other
credentials, provider error text, degradation reasons, or raw provider
responses. A skipped or unavailable service is recorded as `unobserved`, never
as passed.

## Record template

Add a separate Markdown file only after an authorized opt-in run. A harness
fills in every field; a manual procedure fills in at least the first eight.
Keep it content-free:

```text
Commit:
Release candidate or local build:
Observation date:
OS and architecture:
Browser/keyring family and version:
Provider ID and broad deployment class:
Capability or operation:
Content-free result stage:
Harness:
Scope:
Observed capabilities:
Reported degradations:
| Operation | Outcome | Items | Milliseconds | Note |
```

Do not record account aliases, IDs, addresses, tenant or endpoint names,
message/calendar identifiers, subjects, recipients, attendees, bodies,
queries, source paths, screenshots, provider payloads, credentials, approval
values, join URLs, request IDs, or runner arguments.

A record is linked from its entry in
[`internal/compatcatalog/catalog.json`](../../internal/compatcatalog/catalog.json),
which generates the tables in [`docs/compatibility.md`](../compatibility.md).
If the observed implementation surface changes after the recorded commit, that
entry stops being `live-observed` until the new commit is exercised.
