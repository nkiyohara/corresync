# ADR 0027: Domain-only public compatibility checker

- Status: accepted
- Date: 2026-08-11
- Amends: [ADR 0008](0008-provider-neutral-product-scope.md) and
  [ADR 0012](0012-credential-free-discovery-and-explicit-selection.md)

## Context

Credential-free discovery is useful before installation, but asking a new user
to install a CLI merely to learn whether their provider has a plausible route
is a poor first experience. A public Pages form can make that answer legible,
provided it does not turn an email address into hosted account data or create a
general discovery proxy.

The tempting implementation—send the full address to a service and let it
probe provider-supplied URLs—would cross both boundaries. It would disclose a
mailbox identifier and expose server-side request forgery, redirects, DNS
rebinding, private-network targeting, response amplification, and accidental
authentication surfaces.

## Decision

The Providers page may offer an optional compatibility checker with two
strictly separated components:

1. Browser code validates the address locally, derives a normalized ASCII
   domain, immediately clears the address field, and sends only
   `{"domain":"example.com"}` in a `POST` body to the fixed
   `discover.corresync.org/v1/check` endpoint. It does not put either value in a
   URL, log it, persist it, or start authentication.
2. A single-purpose Cloudflare Worker accepts only that versioned schema from
   the production Pages origin or the two fixed loopback development origins.
   It queries only Cloudflare's fixed DNS-over-HTTPS origin for bounded MX,
   Autodiscover CNAME, and relevant SRV records.
   It never fetches a user-derived origin, follows a redirect, accepts a
   credential, calls a provider API, or starts OAuth.

The Worker returns a closed, versioned result: provider family, confidence,
availability state, signal categories, typed route identifiers, bounded
caveats, and closed next-step targets. It never returns raw DNS records or a
server-supplied link. The browser maps those identifiers to reviewed static
copy and local links with text-only DOM construction.

Application caching and observability are disabled. Responses and browser
requests use `no-store`; the Worker uses no Cache API, database, durable
object, analytics binding, cookie, or application log. Cloudflare still
processes ordinary connection metadata such as an IP address as the network
operator. Invocation logs and Workers observability are disabled in the
deployment configuration. A rate-limit binding uses one global product key so
the application does not inspect or retain a visitor IP.

The public signal catalog and separate user-owned/managed Google route modes
are generated from the same Go declarations used by local discovery. CI fails
when the artifact is stale. Public inference remains evidence rather than
authorization or a capability guarantee. The CLI remains the fuller local
discovery path and may perform bounded, TLS-validated well-known discovery that
this public service deliberately omits.

## Consequences

The project operates one narrow hosted DNS classification service, but still
does not operate a mailbox, calendar, credential, OAuth, MCP, telemetry, or
provider-data relay. This is an explicit exception to the broad wording in ADR
0008, not permission to add arbitrary hosted probes or account processing.

The fixed resolver removes the SSRF and rebinding target rather than trying to
filter every possible address after resolution. Omitting public well-known
HTTP probes reduces classification breadth; unknown remains an honest result
and directs users to local discovery.

Availability depends on GitHub Pages, Cloudflare Workers, and public DNS. A
failure cannot block installation or local discovery. The form starts inert
until its script attaches, has no action fallback, and provides a no-JavaScript
local alternative so it cannot accidentally submit an address.
