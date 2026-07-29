# Prior art and build decision

Reviewed on 2026-07-17. The comparison is pinned to repository commits so the
decision remains auditable as those projects evolve.

## Candidates

| Project | Reviewed revision | Strength | Constraint for this project |
| --- | --- | --- | --- |
| [Owl for Exchange](https://addons.thunderbird.net/thunderbird/addon/owl-for-exchange/) | 1.5.2 | Mature OWA integration with mail and calendar | Proprietary, no source license, Thunderbird-specific product |
| [johnkil/outlook-agent](https://github.com/johnkil/outlook-agent) | `bedf89f` | Broad Apache-2.0 CLI/MCP, policy gates, many tests, Graph/EWS/OWA backends | Graph-first and intentionally broad; includes persisted secret providers and a guarded raw-action escape hatch |
| [jlentink/outlook-web-mcp](https://github.com/jlentink/outlook-web-mcp) | `04d3509` | Focused OWA mail/calendar MCP with wide action coverage | No repository license; persists OAuth tokens and uses a local TLS interception login design |
| [okms/m365-corresync-cli](https://github.com/okms/m365-corresync-cli) | `e7bb9e8` | Focused calendar CLI and useful OWA contract fixtures | No repository license, no MCP, and plaintext token persistence |
| [ladifire-opensource/outlook.live.com_modules](https://github.com/ladifire-opensource/outlook.live.com_modules) | `fef0574` | Historical names and shapes from Outlook Web modules | No repository license and old generated/extracted source |

No-license repositories are useful public observations, but their code is not
available for copying or redistribution. Owl establishes that the OWA approach
is viable, but it is not a forkable open-source base.

## Decision: independent implementation

`johnkil/outlook-agent` is the only serious fork candidate with a compatible
license. It is also a different product: approximately 50,000 lines across
Graph, EWS, OWA, setup plugins, skills, raw actions, and enterprise rollout
machinery at the reviewed revision. Removing most of that surface would produce
a harder-to-review fork than a small OWA-only core.

`Corresync` therefore started independently and keeps these non-negotiable
invariants:

- a visible browser owns the Outlook session;
- cookies, bearer tokens, refresh tokens, canaries, and passwords are never
  persisted by the bridge;
- there is no TLS interception and no arbitrary raw-action tool;
- CLI and MCP are adapters over one typed application/policy/audit core;
- undocumented OWA contracts are isolated behind synthetic golden fixtures;
- each undocumented or standard protocol remains behind a typed adapter.

Public implementations are used as independent compatibility observations,
not as source to copy. A wire shape is accepted only after it agrees with more
than one observation or with an opt-in capture from the user's own browser
session. New fixtures contain synthetic values.

For new-message sending, the independent contract also follows Microsoft's EWS
documentation for
[`CreateItem`](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/createitem),
[`SavedItemFolderId`](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/saveditemfolderid),
`SendAndSaveCopy`, and the Sent Items distinguished folder. Public OWA
implementations were compared only as wire observations. The typed request,
safety flow, tests, and synthetic fixtures in this repository were written
independently.

For mailbox search, the typed contract follows Microsoft's
[`FindItem`](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/finditem-operation)
and
[`QueryStringType`](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/querystring-querystringtype)
documentation. The OWA-specific `QueryStringType:#Exchange`, paging, mail-list
shape, and search identity fields were independently compared with the pinned
historical Outlook Web modules. Only the observed wire vocabulary was used;
the implementation and synthetic fixture are original.

For moving mail, the one-item request follows Microsoft's
[`MoveItem` operation](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/moveitem-operation).
The OWA-specific `MoveItemJsonRequest:#Exchange`, `TargetFolderId`, and item ID
shapes agree across the pinned Owl and Outlook Web observations. The bridge adds
its own mandatory bounds, versioned change key, policy flow, unknown-outcome
handling, and synthetic fixtures.

The read/unread contract follows Microsoft's
[`UpdateItem` operation](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/updateitem-operation)
and Owl's pinned OWA `IsRead` observation. It deliberately uses
`NeverOverwrite` with a required change key instead of Owl's broad bulk
`AlwaysOverwrite` behavior, and it exposes no general update primitive.

Calendar creation follows Microsoft's
[`CreateItem` operation](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/createitem-operation)
and
[`CalendarItem`](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/calendaritem)
contracts for `SendMeetingInvitations`, start/end, attendees, location, and the
returned item identity. Microsoft's
[Outlook online-meeting guidance](https://learn.microsoft.com/en-us/graph/outlook-calendar-online-meetings)
also documents the bounded calendar semantics of `isOnlineMeeting` and a
provider supported by the parent calendar; the bridge does not use Graph. The
OWA-specific `CreateCalendarEvent` action, `CalendarItem:#Exchange`,
`EnhancedLocation`, attendee mailbox, time-zone, `IsOnlineMeeting`, and
`TeamsForBusiness` vocabulary agree with the pinned historical Outlook Web
modules and an authorized live observation. The bridge independently adds UTC
normalization, configured bounds, mandatory preview/commit even without
attendees, one-attempt transport, unknown-outcome handling, and synthetic
golden fixtures. It does not copy code from Owl or the unlicensed repositories.

Calendar update and cancellation follow Microsoft's
[`UpdateItem` operation](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/updateitem-operation)
and
[`DeleteItem` operation](https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/deleteitem-operation)
contracts. The OWA-specific `UpdateCalendarEvent` action, default event scope,
unqualified property names, time-zone ID fields, enhanced `Locations`, and
request envelope agree with the pinned historical Outlook Web modules and an
authorized live observation. The bridge narrows update to reviewed fields,
requires a change key, leaves attendee notification behavior to Outlook,
treats cancellation as destructive, and never retries either submission.

## Multi-provider amendment

On 2026-07-28 the project expanded the stable application boundary with
independent Google API, Microsoft Graph, JMAP, IMAP/SMTP, and CalDAV adapters.
This does not reverse the independent-implementation decision:

- each adapter was written against its published protocol documentation and
  synthetic fixtures;
- provider wire types remain outside the domain/application core;
- mail and calendar routes are explicitly selected and never fall back
  silently;
- public behavior is the typed application contract, not a copied provider
  client surface; and
- deterministic and live evidence remain labeled separately.

Graph is now an optional explicit adapter rather than an upstream architecture
dependency. The browser-owned Outlook Web route remains available for users
who cannot or do not want to register a Graph public client. No route embeds a
shared secret or operates through a hosted relay.

## When to reconsider

Reconsider an upstream or shared protocol package if an actively maintained,
compatibly licensed OWA project adopts the same browser-owned, non-persistent
credential boundary and a typed, raw-action-free API. Until then, independent
implementation is the cleaner security and maintenance choice.
