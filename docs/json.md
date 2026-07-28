# JSON output contract

`--json` and MCP structured content serialize the same application result
types. Field names use lower camel case. Optional scalar fields are omitted
when Outlook does not return a value; result arrays are empty arrays rather
than `null`. Empty nested address metadata can appear as `{}`. Mailbox content
and identifiers in the examples below are synthetic.

## Read results

<!-- markdownlint-disable MD013 -->

| Command or tool | Top-level fields | Item fields |
| --- | --- | --- |
| Mail folders | `folders`, `totalFolders`, `includesLastItem` | `id`, optional `changeKey`, `parentId`, `displayName`, `class`, `distinguishedId`; `childFolderCount`, `totalItemCount`, `unreadItemCount` |
| Mail list and search | `messages`, `totalItemsInView`, `includesLastItem` | `id`, optional `changeKey`, `subject`, `from`, `receivedAt`, `importance`; `isRead`, `hasAttachments` |
| Mail attachment | `status`, `attachment` | metadata plus `contentBase64`, bounded to 2 MiB decoded |
| Calendar list | `events`, `start`, `end` | `id`, optional `changeKey`, `subject`, `location`, `organizer`, `myResponse`, `freeBusy`; `start`, `end`, `isAllDay`, `isOnlineMeeting`, `isOrganizer`, `isCancelled` |

<!-- markdownlint-enable MD013 -->

`from` and `organizer` contain optional `name` and `address` fields. Calendar
list results exclude bodies, attendee lists, attachments, and meeting join
URLs.

An immediate body result has this shape:

```json
{
  "status": "completed",
  "body": {
    "id": "message-1",
    "changeKey": "change-2",
    "text": "Synthetic plain-text body",
    "attachments": [
      {
        "id": "attachment-1",
        "kind": "file",
        "name": "fixture.txt",
        "contentType": "text/plain",
        "size": 17,
        "isInline": false
      }
    ]
  }
}
```

## Preview results

A gated operation first returns `status: "approval_required"`, an operation-
specific `review`, and a `preview`:

```json
{
  "status": "approval_required",
  "review": {
    "to": ["reader@example.invalid"],
    "subject": "Synthetic message",
    "bodyPreview": "Synthetic body",
    "bodyBytes": 14,
    "bodySha256": "24a225060015d36ac2507b199f561043ed5374faada4fb75c880c19017f40038",
    "bodyFormat": "text",
    "composeMode": "new"
  },
  "preview": {
    "token": "REDACTED",
    "expiresAt": "2026-07-18T12:02:00Z",
    "operation": {
      "name": "mail.send",
      "effect": "external_write",
      "account": "work",
      "digest": "1e6887a57c5e7f647590cc3beef1be2f3c1f3e2ff018e80ccfeea349652b7184"
    }
  }
}
```

The token is a secret, one-time capability. Do not log or persist it. CLI
`--approve` regenerates and consumes the in-process preview from the same exact
arguments; MCP passes the returned token only to the matching commit tool.

Reviews bind the complete input while bounding displayed content:

<!-- markdownlint-disable MD013 -->

| Operation | Review fields |
| --- | --- |
| Draft or send | optional recipients, subject, body preview, reference identity, attachments; required body size/hash, `bodyFormat`, `composeMode`; attachment content is represented by size and SHA-256 |
| Mail hard delete | `messageId`, `changeKey`, `deleteType` |
| Mail move | `messageId`, `changeKey`, `destination` |
| Read-state update | `messageId`, `changeKey`, `state` |
| Calendar create | calendar, optional subject/body/location/attendees/reminder/recurrence, start/end/time zone, all-day and invitation/Teams flags, body size/hash |
| Calendar update | identity, only supplied patch fields, optional reminder and attendee replacement, bounded body review, `meetingUpdateMode` |
| Calendar cancellation | `eventId`, `changeKey`, `cancellationMode`, `deleteType` |

<!-- markdownlint-enable MD013 -->

## Commit results

<!-- markdownlint-disable MD013 -->

| Operation | Success status | Result object and fields |
| --- | --- | --- |
| Body read | `completed` | `body`: `id`, optional `changeKey`, `text`, optional attachment metadata |
| Attachment read | `completed` | `attachment`: metadata and `contentBase64` |
| Draft save | `completed` | `draft`: `id`, optional `changeKey` |
| Send | `sent` | `sent`: optional `id`, `changeKey` |
| Mail move | `completed` | `moved`: optional `id`, `changeKey` |
| Read-state update | `completed` | `updated`: optional `id`, `changeKey`; required `state` |
| Mail hard delete | `deleted` | `deleted`: `id` |
| Calendar create | `created` | `created`: `id`, optional `changeKey`, required `isOnlineMeeting`, optional `onlineMeetingProvider`, `onlineMeetingJoinUrl` |
| Calendar update | `updated` | `updated`: optional `id`, `changeKey` |
| Calendar cancellation | `cancelled` | `cancelled`: `id` |

<!-- markdownlint-enable MD013 -->

Write success objects also include the exact bounded `review`. Empty optional
identities do not mean failure: Outlook sometimes confirms a write without
returning a refreshed item identity. A transport failure after submission is
reported as an unknown outcome instead of being converted into success or
automatically retried.

## Update status

`corresync update check --json` returns `status`, `currentVersion`,
`updateAvailable`, and `cached`; successful comparisons also include
`latestVersion`, `releaseUrl`, and `checkedAt`.
`status` is `current`, `available`, `development`, or `unavailable`. This
content-free result is separate from the Outlook application result types.
`installMethod` and `upgrade` appear only when `status` is `available`.

`corresync update --json` returns the explicit action result. It always includes
`status`, `currentVersion`, `updated`, and `installMethod`. A direct successful
replacement uses `status: "updated"` and also returns `previousVersion`,
`latestVersion`, `releaseUrl`, `archive`, `backupPath`, and the completed
`verification` checks. A managed installation uses
`status: "action_required"` with the exact external `command`; `updated`
remains false.
Automatic human notices are never appended to JSON output.

## Local CLI state

`corresync auth status --json` is deliberately content-free:

```json
{
  "daemonVersion": "0.7.0",
  "processId": 4242,
  "accounts": [
    {
      "account": "acc_0123456789abcdef0123456789abcdef",
      "alias": "work",
      "provider": "microsoft-owa",
      "state": "authenticated",
      "authenticated": true,
      "capturedAt": "2026-07-28T12:00:00Z",
      "capabilities": {
        "mail": true,
        "calendar": true,
        "folders": true,
        "labels": false,
        "push": false,
        "freeBusy": false,
        "onlineMeeting": "teams",
        "incrementalSync": false,
        "scheduledSend": false,
        "sharedMailboxes": true,
        "sharedCalendars": false,
        "attachmentReads": true,
        "attachmentWrites": true
      }
    }
  ]
}
```

State is `authenticated`, `pending`, or `signed_out`; `capturedAt` exists only
for an authenticated session. `corresync auth logout --json` returns
`{"loggedOut":true,"scope":"all"}` after the owner has exited.

`corresync config show --json` serializes the validated secret-free configuration
with lower-camel field names. `config get --json` returns `key` and typed
`value`; `config set --json` additionally returns `updated: true`. Config
initialization, validation, editing, and path inspection similarly emit one
small action object. `corresync version --json` returns version, commit, build time,
Go version, operating system, and architecture.

## Compatibility policy

These are stable adapter contracts, not raw Outlook payloads. OWA wire changes
are normalized behind the transport boundary and represented by synthetic
contract fixtures. Additive fields can appear in a future minor release;
renames or semantic changes require a versioned compatibility decision.
