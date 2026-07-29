# ADR 0005: Treat calendar-hosted online-meeting links as calendar scope

- Status: accepted
- Date: 2026-07-18
- Amended: 2026-07-29

## Context

An Outlook or Microsoft Graph event can provision a Teams meeting, and Google
Calendar can provision a Google Meet conference when the authenticated
calendar advertises `hangoutsMeet`. This is useful to both CLI users and MCP
agents, but describing the Microsoft case as general Teams integration would
blur the product boundary and imply unrelated chat, channel, call, recording,
or meeting-management capabilities.

Join URLs are also more sensitive than ordinary calendar-list metadata. Bulk
calendar reads must not expose every meeting link merely because one event can
be created as an online meeting.

## Decision

Calendar creation accepts a provider-neutral `onlineMeeting` boolean. The
mandatory preview binds the exact observed provider, currently `teams` or
`google-meet`. Microsoft routes map the request to the provider's closed Teams
fields. Google Calendar generates a unique conference request ID, requests
`hangoutsMeet` with `conferenceDataVersion=1`, never reuses conference data,
and performs only bounded read-after-write confirmation if generation remains
pending. The v0.7 `teamsMeeting` field remains a compatibility input and fails
unless the selected route reports Teams support. The commit result may return
the join URL for only the single event it just created.

Calendar list remains metadata-only and excludes join URLs. Corresync does not
expose arbitrary conference fields or any Teams chat, channel, call, recording,
or post-creation meeting-management operation.

## Consequences

Humans and agents get a simple preview/commit flow and can use the returned URL
without broadening calendar reads. Corresync depends on the signed-in calendar
advertising its native conference service and never falls back to another
provider. Wider Teams or Google Meet lifecycle features require a separate ADR
and must not be inferred from this calendar capability.
