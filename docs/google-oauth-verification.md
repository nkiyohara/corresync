# Google OAuth production and verification runbook

This runbook prepares Corresync's official Google desktop OAuth application for
production. It records the project state, public URLs, requested scopes,
justifications, architecture evidence, and submission checks in one place. It
does not assert that Google has approved the application or waived a security
assessment; the Google verification team makes those decisions.

## Project and current status

| Item | Value |
| --- | --- |
| Google Cloud project name | `Corresync` |
| Google Cloud project ID | `strong-surfer-504009-i0` |
| Cloud project | Created |
| Google Auth Platform branding | Configured; External / Testing |
| Gmail API | Enabled |
| Google Calendar API | Enabled |
| Desktop OAuth client | Created as `Corresync desktop` |
| Data access scopes | Registered exactly as listed below |
| Authorized domain and public URLs | Configured and live on `corresync.org` |
| Synthetic test account | Authorized and live-observed |
| Google verification submission | Not submitted |

Do not describe the Google route as generally available until the production
OAuth identity is configured, the public policy URLs are live, and Google has
approved the requested access. Testing with named test users is not production
approval and remains subject to Google's test-user limits and warnings.

## Canonical public identity

Use these exact, non-redirecting HTTPS URLs in the Google Auth Platform:

| Google field | Canonical value |
| --- | --- |
| Application name | `Corresync` |
| Homepage | `https://corresync.org/` |
| Privacy Policy | `https://corresync.org/privacy.html` |
| Terms of Service | `https://corresync.org/terms.html` |
| Authorized domain | `corresync.org` |

The homepage identifies Corresync, describes the mail/calendar and MCP
functionality, explains direct local Google data handling, and visibly links
the Privacy Policy and Terms. The Privacy Policy is on the same domain and
explicitly covers access, use, local storage, disclosure, retention, deletion,
and Google Limited Use.

Before entering these URLs:

1. Deploy the Pages artifact from `main`.
2. Open all three URLs in a private browser window and confirm HTTP 200 with no
   login or cross-domain redirect.
3. Confirm that the homepage source contains the exact Search Console tag:

   ```html
   <meta
     name="google-site-verification"
     content="du6yQYCD4HROJoMhBnPxnbcntabW8RFRJbfZrRcVcic"
   >
   ```

4. In Search Console, verify the URL-prefix property
   `https://corresync.org/` using the Google account that is
   also an Owner of the Cloud project. Keep the tag published:
   Search Console periodically rechecks it.
5. Confirm that Google Auth Platform accepts `corresync.org` as the
   authorized domain and recognizes the project owner's Search Console
   verification. If it does not, stop and move all three canonical URLs to one
   custom domain controlled through DNS rather than submitting inconsistent
   URLs.

The user support email and developer contact email must be real, monitored
addresses controlled by the maintainer. They are intentionally not invented or
committed here. Set them in Google Cloud and keep them current before
submission.

## Enable only the required APIs

In project `strong-surfer-504009-i0`:

1. Enable **Gmail API**. Although Gmail mail transport uses IMAP/SMTP XOAUTH2
   rather than Gmail REST, Google classifies and verifies the requested Gmail
   OAuth scope through the Gmail API product.
2. Enable **Google Calendar API**.
3. Do not enable unrelated Workspace APIs for future use.

Google may require acceptance of current API terms in the Cloud Console. The
project maintainer must review and accept those terms as the authorized project
owner.

## Configure branding and audience

In Google Auth Platform:

1. Set the application name to `Corresync`.
2. Select a monitored user support email.
3. Use the Corresync project mark only; do not use a Google, Gmail, Calendar,
   or Meet trademark as the application icon.
4. Enter the exact homepage, Privacy Policy, and Terms URLs above.
5. Add `corresync.org` as the only authorized domain used by these
   fields.
6. Choose the **External** audience for a public open-source desktop client.
7. Add a monitored developer contact email.
8. While the application is in Testing, list only explicit test accounts and
   do not market the connection as production-ready.

Publish the branding before beginning sensitive/restricted-scope verification.
Changing the name, icon, homepage, Privacy Policy URL, Terms URL, or authorized
domain during review can trigger another brand review.

## Create the desktop public client

Create an OAuth client with application type **Desktop app** and a clear name
such as `Corresync desktop`. A native application cannot keep an embedded
client credential confidential, but Google's generated Desktop client may
still require that value at the token endpoint. Corresync persists only:

- the public client ID;
- `http://127.0.0.1:0` as the native loopback redirect model;
- an opaque OS-keyring authorization handle; and
- the user's explicit OAuth consent bit.

Supply the generated value only in the local session owner's
`CORRESYNC_GOOGLE_OAUTH_CLIENT_SECRET` process environment. Corresync bounds
it, excludes it from the browser authorization URL and stored grant, and sends
it only to Google's fixed TLS token endpoint. Never put it in configuration,
CLI or MCP input, source, release metadata, CI, issues, logs, support output, or
the verification recording.

The runtime opens Google's authorization endpoint in the normal system browser,
uses Authorization Code with PKCE and unpredictable state, binds a listener to
a random loopback port, and exchanges the code directly with Google's token
endpoint. It does not use an embedded browser, device-code flow, password,
application password, browser cookie, or client-secret configuration field.

Google's desktop-app documentation recommends a system browser and random
loopback port on macOS, Linux, and Windows. Confirm the generated Desktop client
accepts that flow. Do not create a Web application client or register a public
internet callback for the local desktop path.

The client ID is public by design, but publishing it changes who can present
the Corresync OAuth identity. Record how the official binary receives it, and
keep a kill/rotation plan. Rotate or disable an obsolete generated credential
after a replacement has completed an end-to-end token exchange.

## Register the exact data-access scopes

Register only these three scopes:

```text
https://mail.google.com/
https://www.googleapis.com/auth/calendar.calendarlist.readonly
https://www.googleapis.com/auth/calendar.events
```

Corresync derives the requested set from the enabled services:

- mail only requests `https://mail.google.com/`;
- calendar only requests the two Calendar scopes; and
- mail plus calendar requests all three in one account-scoped grant.

It never requests Google access during discovery, setup, MCP registration,
status, or an ordinary tool read. `corr auth login --account ALIAS` first prints
the exact derived scope set and opens Google only when no matching valid grant
is already in the OS keyring.

### Scope justifications for the verification form

Use the following factual descriptions, adjusting only for form length:

#### `https://mail.google.com/`

> Corresync is a local desktop mail client and MCP server. It uses Gmail's
> documented IMAP and SMTP XOAUTH2 endpoints to let the signed-in user list and
> search mailbox folders, read selected messages and attachments, compose and
> save drafts, send mail, change read state, and move or organize messages. IMAP
> and SMTP XOAUTH2 require the `https://mail.google.com/` scope; a narrower Gmail
> REST scope cannot authorize this standards-based transport. The Google route
> does not expose permanent message deletion. Mail data travels directly
> between the user's device and Google over TLS and is not transmitted to or
> stored on a Corresync-operated server.

#### `calendar.calendarlist.readonly`

> Corresync lists the calendars already available to the signed-in user so the
> user can identify and select the exact calendar for reads and writes. It reads
> calendar-list identifiers, names, access roles, and related metadata. It does
> not modify the user's calendar list.

#### `calendar.events`

> Corresync lets the signed-in user list and read events and, after an exact
> preview and separate approval, create, update, or cancel an event. Event data
> can include title, description, location, times, attendees, recurrence,
> reminders, status, and a provider-native Google Meet link requested as one
> event property. The scope is not used for Contacts, Drive, tenant
> administration, chat, recordings, or unattended meeting management.

These descriptions must continue to match `internal/oauthlocal.ProviderFor`,
the adapter surface, the public Privacy Policy, and the verification demo.
Changing a requested scope or use requires a policy and product review before
release and may require Google re-verification.

## Restricted-scope data architecture

`https://mail.google.com/` is a restricted scope. The submission must answer
the restricted-data questions precisely:

- Corresync is a user-installed local desktop CLI and MCP stdio server.
- The official project operates no hosted mailbox, calendar store, token relay,
  remote MCP endpoint, analytics collector, or telemetry backend.
- OAuth grants are stored in the user's OS keyring. Configuration holds only an
  opaque handle and public-client metadata.
- Gmail mail traffic goes directly from the local process to
  `imap.gmail.com:993` using implicit TLS and `smtp.gmail.com:587` using
  STARTTLS. Calendar traffic goes directly to Google's fixed Calendar API.
- There is no general persistent Gmail or Calendar content cache.
- Content-free audit records retain only bounded operation/security fields and
  opaque account and target/provider identifiers; they exclude addresses,
  recipients, subjects, bodies, attachment names, event text, queries,
  credential references, tokens, and approval values.
- An optional local monitoring queue is disabled by default and separately
  consented; it stores only selected notification fields, never bodies or
  attachments.
- MCP uses local stdio. Requested tool results can be supplied to a client or
  model selected by the user. Any remote model is a separate third party under
  the user's configuration and that party's terms.
- Corresync does not sell data, serve advertising, use data for credit,
  surveillance, or general-purpose model training, or permit ordinary
  maintainer access.

Google states that applications which access restricted-scope data from or
through a third-party server may need an annual approved security assessment.
Describe the direct local architecture above and ask the verification team to
determine the applicable assessment requirement. Do not claim an exemption,
and do not move restricted data through hosted infrastructure without a new
architecture, privacy, security, and Google-policy review.

## Verification evidence and demo

Prepare one unedited end-to-end screen recording using a synthetic test account
and synthetic mail/calendar content. Keep the consent screen and browser
address bar visible. Show:

1. the installed Corresync version and the public homepage;
2. account configuration for Google mail and calendar with no credential value
   visible in configuration, commands, or output;
3. `corr auth login --account TEST_ALIAS`;
4. the exact three scopes printed before browser launch;
5. the Google account chooser and consent screen identifying `Corresync`;
6. successful return through the loopback callback;
7. a Gmail folder list, bounded search, one selected message read, draft/send
   preview and approval, and a reversible organization action;
8. calendar list and event list, then an event create preview and approval;
9. where the Privacy Policy and Terms are linked in the homepage;
10. `corr account remove TEST_ALIAS --approve`, followed by removing Corresync
    from the test account's Google Account connections.

Do not record a real mailbox, token, client secret, keyring contents, personal
address, or reusable approval token. If Google requests separate videos or a
specific form flow, follow the current reviewer instructions.

Use these feature-documentation links in the submission where the form permits:

- `https://corresync.org/providers.html#google`
- `https://corresync.org/safety.html`
- `https://corresync.org/privacy.html#google-data`

## Pre-submission gate

Do not submit until every item is true:

- [ ] Pages is deployed from the reviewed `main` revision.
- [ ] Homepage, Privacy Policy, and Terms return HTTP 200 without login or
      redirect and all three use the same canonical domain.
- [ ] Homepage visibly links the exact Privacy Policy and Terms URLs entered in
      Google Cloud.
- [ ] Search Console recognizes the authorized domain for a Cloud project Owner
      while the verification meta tag remains live.
- [ ] Branding, icon, user support email, developer contact, External audience,
      and authorized domain are complete.
- [ ] Gmail API and Google Calendar API are enabled; no unrelated API is enabled
      for this integration.
- [ ] One Desktop app client exists; its generated client credential is injected
      only into the local process, excluded from the recording and public
      artifacts, and not persisted by Corresync.
- [ ] The three scopes above—and no others—are registered.
- [ ] Scope justifications match the current code and Privacy Policy.
- [ ] The local-only restricted-data architecture is answered accurately.
- [ ] A synthetic end-to-end demo video covers authorization and each requested
      scope.
- [ ] `mise exec -- task verify` passes on the exact candidate revision.
- [ ] A maintainer has completed the security/privacy review checklist below.
- [ ] The submission explicitly asks Google to determine whether a security
      assessment is required for this local installed-app architecture.

Submit from the Google Auth Platform verification center only after this gate.
Monitor the developer contact address, answer reviewer questions with links and
reproducible synthetic evidence, and keep production branding and behavior
stable during review. Restricted-scope verification can take weeks; do not
promise a completion date.

## Ongoing compliance

- Keep the Privacy Policy, Terms, homepage, in-product consent notice, requested
  scopes, and actual behavior aligned.
- Provide notice and obtain consent before using Google user data for a new
  purpose.
- Request the smallest service-derived scope set and handle denied or revoked
  consent without fallback or bypass.
- Review any new MCP client, hosted component, monitoring field, data export,
  telemetry, or model integration before it can receive Google user data.
- Preserve Google Limited Use: no sale, advertising, credit/lending,
  surveillance, unrelated data enrichment, or general-purpose model training.
- Keep provider endpoints TLS-only, dependencies and release provenance
  reviewed, and vulnerabilities privately reportable.
- Reverify when Google requires it, including after material branding, domain,
  privacy, redirect, client, scope, or data-use changes.
- Retain proof of the submitted revision, screenshots, demo, scope
  justifications, reviewer correspondence, and final decision without retaining
  live user data or credentials.

## Primary Google references

- [Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy)
- [Google APIs Terms of Service](https://developers.google.com/terms)
- [OAuth app verification requirements](https://support.google.com/cloud/answer/13464321)
- [Manage OAuth app branding](https://support.google.com/cloud/answer/15549049)
- [Restricted-scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification)
- [OAuth 2.0 for desktop apps](https://developers.google.com/identity/protocols/oauth2/native-app)
- [Gmail OAuth scopes](https://developers.google.com/workspace/gmail/api/auth/scopes)
- [Manage Google Account connections](https://support.google.com/accounts/answer/13533235)
- [Search Console ownership verification](https://support.google.com/webmasters/answer/9008080)
