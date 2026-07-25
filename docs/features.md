# Feature and evidence matrix

The CLI and MCP server call the same typed application use cases. This matrix
is the public contract for the current release: a missing row is not available
through an arbitrary protocol escape hatch.

## Mail

<!-- markdownlint-disable MD013 -->

| Capability | CLI | MCP | Stable result | Live observation |
| --- | --- | --- | --- | --- |
| Discover folders | `owa mail folders` | `mail_list_folders` | `MailFolderPage` | Observed |
| List metadata | `owa mail list` | `mail_list` | `MailPage` | Observed |
| Search metadata with AQS | `owa mail search` | `mail_search` | `MailPage` | Observed |
| Read one plain-text body and attachment metadata | `owa mail body` | `mail_get_body` + commit when required | `MailBodyAccess` | Body observed; metadata contract-tested |
| Retrieve one bounded file attachment | `owa mail attachment` | `mail_get_attachment` + commit when required | `MailAttachmentAccess` | Observed |
| Save a text/HTML new, reply, reply-all, or forward draft | `owa mail draft` | `mail_create_draft` + commit when required | `MailDraftAccess` | New text observed; extensions contract-tested |
| Send a text/HTML new message, reply, reply-all, or forward | `owa mail send` + `--approve` | `mail_send` + `mail_send_commit` | `MailSendAccess` | New text self-send observed; extensions contract-tested |
| Add bounded file attachments to a draft or send | `--attachment` | `attachments` | Attachment hashes in `MailReview` | Contract-tested; live unobserved |
| Move one message version | `owa mail move` | `mail_move` + commit when required | `MailMoveAccess` | Observed, including restore |
| Set read or unread | `owa mail mark` | `mail_set_read_state` + commit when required | `MailReadStateAccess` | Observed, including restore |
| Permanently delete one exact message version | `owa mail delete` + `--approve` | `mail_delete` + `mail_delete_commit` | `MailDeleteAccess` | Contract-tested; live unobserved |

<!-- markdownlint-enable MD013 -->

Metadata listing excludes bodies and attachment content. A body read returns
bounded attachment metadata; content retrieval is a separate sensitive read
limited to 2 MiB. Draft creation uses `SaveOnly` and never sends. Every send
requires a separate exact commit and every write request is attempted once; a
successful Outlook response may omit a Sent Items identity, so `sent.id` and
`sent.changeKey` are optional.

## Calendar

<!-- markdownlint-disable MD013 -->

| Capability | CLI | MCP | Stable result | Live observation |
| --- | --- | --- | --- | --- |
| List bounded event metadata | `owa calendar list` | `calendar_list` | `CalendarPage` | Observed |
| Create an appointment or meeting | `owa calendar create` + `--approve` | `calendar_create` + `calendar_create_commit` | `CalendarCreateAccess` | Observed |
| Add a Teams join link at creation | `--teams-meeting` | `teamsMeeting: true` | `created.onlineMeetingJoinUrl` | Observed, self-attendee only |
| Create all-day events, reminders, and recurrence | create flags | `allDay`, `reminder`, `recurrence` | `CalendarCreateAccess` review | Contract-tested; live unobserved |
| Update supported fields | `owa calendar update` + `--approve` | `calendar_update` + `calendar_update_commit` | `CalendarUpdateAccess` | Observed |
| Replace reminders, all-day status, or attendee lists | update flags | typed update fields | `CalendarUpdateAccess` review | Contract-tested; live unobserved |
| Cancel one event version | `owa calendar cancel` + `--approve` | `calendar_cancel` + `calendar_cancel_commit` | `CalendarCancelAccess` | Observed |

<!-- markdownlint-enable MD013 -->

Creation supports subject, plain-text body, start, end, Exchange time-zone ID,
location, all-day status, reminders, daily/weekly/absolute-monthly/
absolute-yearly recurrence, required and optional attendees, and one closed
Teams option. Update is deliberately limited to subject, plain-text body, start
plus end and time zone, location, all-day status, reminder, and complete
replacement of both attendee lists. Cancellation moves the selected event
version to Deleted Items and asks Outlook to notify meeting attendees.

Outlook's calendar-view response may report `isOnlineMeeting: false` even for
an event whose creation response returned a Teams link. The authoritative
provisioning result is `calendar_create_commit.created`; bounded calendar lists
never return join URLs.

## Agent ergonomics and safety

- Every consequential operation has a review tool or CLI preview. Commit tools
  accept only a short-lived token bound to the originating process and exact
  operation digest.
- Tool names are narrow verbs, schemas reject unknown fields, and updates do
  not accept arbitrary property names or OWA actions.
- IDs and change keys are returned together wherever Outlook supplies them so
  an agent can perform stale-safe follow-up actions.
- Mail and calendar output is labelled private, untrusted external content.
  Message bodies and Teams join URLs carry the sensitive classification.
- Unknown write outcomes fail closed and are never retried automatically.

See the [JSON contract](json.md) for field-level output shapes and
[compatibility evidence](compatibility.md) for the limits of the live
observations.

## Shared and delegated mailboxes

An account alias can set a bare `mailbox` SMTP address. The session still signs
in interactively as the user, stays on the configured Outlook origin, and sends
explicit OWA mailbox-routing headers. This grants no permission: the signed-in
user must already be authorized for that mailbox in Outlook Web. Delegate and
folder-permission management are not exposed.

## Intentionally not implemented

The current release does not expose mailbox-rule mutation, delegate-permission
management, recurrence editing after event creation, item attachments,
arbitrary recurrence patterns, or a generic property update API. Updating
Inbox rules through EWS can remove client-only rules according to Microsoft's
[Inbox management guidance][inbox-guidance], so it is not treated as a safe
generic write. Teams chat, channels, calls, recordings, and meeting
lifecycle management are outside the project boundary. Microsoft Graph,
hosted relays, unattended login, and tenant-wide access are also out of scope.

These omissions are deliberate. They avoid weak approximations and keep the
review surface small; they are not coverage gaps to fill without a concrete use
case and a typed safety contract.

[inbox-guidance]: https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/inbox-management-and-ews-in-exchange
