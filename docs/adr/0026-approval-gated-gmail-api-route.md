# ADR 0026: Approval-gated Gmail API route

- Status: superseded in its OAuth availability decision by
  [ADR 0036](0036-user-owned-google-oauth-first.md); its Gmail API contract is retained
- Date: 2026-08-11
- Supersedes: the Gmail transport and Google-availability decisions in
  [ADR 0021](0021-google-mail-over-imap-smtp-xoauth2.md)
- Retains: the desktop-client credential boundary in
  [ADR 0022](0022-google-desktop-client-credential.md)

## Context

The current Google OAuth verification request is for the Gmail API
`gmail.modify` scope. That scope supports Corresync's bounded mail reads,
drafts, sends, label changes, read state, archive, and Trash operations without
the permanent-delete authority of `https://mail.google.com/`. Keeping an
IMAP/SMTP transport would require the broader scope and would make the reviewed
product behavior differ from the code intended to ship.

The adapter needs to be present in a release candidate so its synthetic
provider contract, packaging, documentation, and security boundary can be
reviewed as one candidate. Presence in a binary must not make Google OAuth
available before Google approves the production application. A hidden flag,
environment variable, user-supplied client, migrated configuration, or MCP call
would turn staged code into an unapproved production route.

## Decision

The `google` provider keeps its stable account and route identity, but Gmail
mail uses the pinned Gmail API v1 endpoint and requests
`https://www.googleapis.com/auth/gmail.modify` when the route is eventually
activated. Google Calendar continues to use its existing bounded API scopes;
mail and calendar may share one account-scoped grant.

The Gmail adapter implements only Corresync's typed mail ports:

- bounded list, search, selected-body, attachment, and label reads;
- draft, send, reply, reply-all, and forward;
- read/unread, archive, label movement, and move to or from Trash;
- version binding through Gmail message and history identifiers; and
- explicit unknown-outcome reporting after a confirmed partial mutation.

The adapter never calls Gmail's immediate permanent-delete method. The
provider-neutral permanent-delete operation fails locally and directs the user
to move the message to Trash. Push watches, durable Gmail history cursors, and
scheduled send are also reported as unavailable.

Until Google approves the production application, the release-owned constant
`rollout.GoogleOAuthApproved` is `false`. It has no environment, configuration,
CLI, MCP, or user-supplied-client override. The gate is enforced independently
at these boundaries:

1. Google is omitted from the providers that account setup may select.
2. Explicit Google mail or calendar account addition fails without persistence.
3. Existing or migrated Google routes fail before browser launch, OAuth grant
   lookup or creation, keyring access, and session activation. A mixed account
   is rejected as a whole rather than partially activating another route.
4. OAuth profile construction fails before a scope can be presented or an
   authorization request can begin.

Credential-free discovery may still identify Gmail or a Google-hosted domain.
It responds with a natural approval-pending message, states that no Google
sign-in started, and names routes available now. CLI and Pages copy may link to
Google's independent Workspace MCP Developer Preview, but must not imply that
it is the Corresync route.

Changing the gate to `true` is a separate reviewed production change after
approval. That change must re-run the complete verification and security
review, update public availability claims, and confirm the approved consent
screen exactly matches the shipped scopes. A stored grant with the former
`https://mail.google.com/` scope does not match the new scope set and therefore
requires fresh explicit authorization; it is never silently broadened or
reused as evidence of approval. Former IMAP message handles and cursors are not
translated into Gmail API identifiers and fail closed.

## Security consequences

The staged adapter is reachable only from package-level synthetic tests while
the gate is false. Production commands cannot request a Google scope, open a
Google sign-in page, read a Google grant, construct a Google API client, or send
Google traffic. Shipping source and machine code therefore does not exercise
the pending OAuth application.

When activated later, Gmail and Calendar requests remain local, account scoped,
and sent only through the provider-pinned Google API base using the existing
OS-keyring grant boundary. Configuration still cannot represent tokens,
passwords, cookies, arbitrary authorization headers, or alternate Google API
hosts. Provider payloads, MIME trees, pagination, identifiers, headers, bodies,
attachments, recipients, and write outcomes are bounded and validated before
they cross application ports.

## Evidence

Default tests use synthetic TLS Gmail, Calendar, and OAuth endpoints. They
cover the exact staged scope, all four approval gates, no partial activation,
bounded pagination and MIME traversal, identity binding, attachment refetch,
native Gmail mutations, no permanent-delete request, and partial-write outcome
handling. The Gmail API route remains live-unobserved; the older IMAP/SMTP live
observation is historical evidence for that retired transport and cannot be
used as evidence for this route. Live mailbox tests remain explicit opt-ins and
outside the default test command and CI.
