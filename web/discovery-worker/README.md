# Provider compatibility Worker

This Cloudflare Worker backs the optional checker on
<https://corresync.org/providers.html#check>. It receives one normalized domain
in a JSON `POST` body and queries only Cloudflare's fixed DNS-over-HTTPS origin.
It never receives an address local part, fetches a user-derived host, starts
authentication, or stores an application record.

The checked-in `catalog.json` is generated from the CLI's canonical discovery
families and rollout gates:

```console
mise exec -- task discovery:generate
mise exec -- task discovery:check
```

Deploy from this directory with the reviewed Wrangler version:

```console
WRANGLER_SEND_METRICS=false npx --yes wrangler@4.120.1 deploy
```

The operator must first authenticate Wrangler to the Cloudflare account that
owns `corresync.org`. The custom-domain route, disabled observability and
invocation logs, and rate-limit binding are declared in `wrangler.jsonc`.
The configuration intentionally omits `limits.cpu_ms`: the Workers Free plan
enforces its own CPU ceiling and rejects custom CPU limits as a paid-plan
feature. DNS request wait time does not consume CPU time. Confirm the current
[Workers pricing and limits][workers-pricing] before deployment. After
deployment, verify that responses use `Cache-Control: no-store`, reject an
unapproved `Origin`, and that the Cloudflare dashboard still reports Workers
Logs and invocation logs as disabled.

If the account moves to Workers Paid, restore an explicit `limits.cpu_ms` of no
more than 10 before deployment; the configuration test accepts that tighter
paid-plan declaration while rejecting a wider CPU ceiling.

Do not add user-derived HTTP probes, arbitrary resolver selection, query-string
input, provider APIs, secrets, storage bindings, analytics, or application
logging. Any expansion requires a new security review and an ADR amendment.

[workers-pricing]: https://developers.cloudflare.com/workers/platform/pricing/
