import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  discoverDomain,
  handleRequest,
  normalizeDomain,
} from "../src/worker.mjs";

const endpoint = "https://discover.corresync.org/v1/check";
const origin = "https://corresync.org";

test("Microsoft MX and Autodiscover produce a high-confidence family with policy caveats", async () => {
  const result = await discoverDomain("example.test", dnsFixture({
    MX: ["0 tenant.mail.protection.outlook.com."],
    CNAME: ["autodiscover.outlook.com."],
    SRV: ["0 1 443 service.example.test."],
  }));
  assert.deepEqual(result.classification, {
    family: "microsoft",
    displayName: "Microsoft 365 / Outlook",
    variant: "microsoft-365",
    confidence: "high",
    status: "verified",
    conflict: false,
    summary: "Public records match a supported Microsoft account family.",
  });
  assert.deepEqual(result.routes.map(route => route.provider), [
    "microsoft-owa",
    "microsoft-graph",
    "jmap",
    "imap-smtp",
    "caldav",
  ]);
  assert.match(result.caveats.join(" "), /Organization policy/);
  assert.doesNotMatch(JSON.stringify(result), /tenant\.mail\.protection/);
});

test("Google families are identified while the generated approval gate stays closed", async () => {
  for (const records of [
    {},
    { SRV: ["0 1 443 service.gmail.com."] },
  ]) {
    const result = await discoverDomain("gmail.com", dnsFixture(records));
    assert.equal(result.classification.family, "google");
    assert.equal(result.classification.status, "not_available");
    assert.equal(result.routes.length, 1);
    assert.equal(result.routes[0].provider, "google");
    assert.equal(result.routes[0].status, "not_available");
  }

  const catalog = JSON.parse(await readFile(
    new URL("../catalog.json", import.meta.url),
    "utf8",
  ));
  assert.equal(catalog.googleOAuthApproved, false);
});

test("standards signals return useful setup guidance without guessing a brand", async () => {
  const result = await discoverDomain("standards.test", dnsFixture({
    SRV: ["0 0 993 mail.standards.test."],
  }));
  assert.equal(result.classification.family, "standards");
  assert.equal(result.classification.status, "additional_setup");
  assert.deepEqual(result.routes.map(route => route.provider), [
    "jmap",
    "imap-smtp",
    "caldav",
  ]);
});

test("conflicting provider evidence remains explicit", async () => {
  const result = await discoverDomain("gmail.com", dnsFixture({
    MX: ["0 tenant.mail.protection.outlook.com."],
  }));
  assert.equal(result.classification.conflict, true);
  assert.equal(result.classification.family, "google");
  assert.deepEqual(result.routes.map(route => route.provider), [
    "microsoft-owa",
    "microsoft-graph",
    "google",
  ]);
});

test("unknown domains are not called unsupported", async () => {
  const result = await discoverDomain("unknown.test", dnsFixture({}));
  assert.equal(result.classification.status, "unknown");
  assert.equal(result.routes.length, 0);
  assert.match(result.classification.summary, /No recognized/);
});

test("the public endpoint accepts only one normalized domain in a POST body", async () => {
  let DNSCalls = 0;
  const fetcher = async request => {
    DNSCalls++;
    assert.equal(new URL(request).origin, "https://cloudflare-dns.com");
    return dnsResponse([]);
  };
  for (const body of [
    { domain: "reader@example.com" },
    { domain: "example.com", address: "reader@example.com" },
    { domain: "127.0.0.1" },
    { domain: "localhost" },
    { domain: "example.com/path" },
  ]) {
    const response = await handleRequest(checkRequest(body), rateEnv(), fetcher);
    assert.equal(response.status, 400);
  }
  assert.equal(DNSCalls, 0);

  assert.equal(normalizeDomain("EXAMPLE.com"), "example.com");
  assert.equal(normalizeDomain("reader@example.com"), "");
});

test("CORS, methods, paths, request bounds, and rate limits fail closed", async () => {
  const noFetch = async () => assert.fail("rejected request reached DNS");
  assert.equal((await handleRequest(checkRequest(
    { domain: "example.com" },
    { Origin: "https://attacker.example" },
  ), rateEnv(), noFetch)).status, 403);
  assert.equal((await handleRequest(new Request(endpoint + "?domain=example.com", {
    method: "POST",
    headers: { Origin: origin, "Content-Type": "application/json" },
    body: JSON.stringify({ domain: "example.com" }),
  }), rateEnv(), noFetch)).status, 404);
  assert.equal((await handleRequest(new Request(endpoint, {
    method: "GET", headers: { Origin: origin },
  }), rateEnv(), noFetch)).status, 405);
  assert.equal((await handleRequest(new Request(endpoint, {
    method: "POST",
    headers: { Origin: origin, "Content-Type": "application/json" },
    body: `{"domain":"${"a".repeat(600)}"}`,
  }), rateEnv(), noFetch)).status, 400);
  const limited = await handleRequest(
    checkRequest({ domain: "example.com" }),
    rateEnv(false),
    noFetch,
  );
  assert.equal(limited.status, 429);
  assert.equal(limited.headers.get("Retry-After"), "60");
});

test("DNS redirects, timeouts, malformed and oversized answers become unavailable", async () => {
  for (const fetcher of [
    async () => new Response("", { status: 302, headers: { Location: "https://attacker.test" } }),
    async () => { throw new DOMException("timed out", "AbortError"); },
    async () => dnsResponse([], { Status: "bad" }),
    async () => new Response("x".repeat(70 * 1024), {
      headers: { "Content-Type": "application/dns-json" },
    }),
  ]) {
    const response = await handleRequest(
      checkRequest({ domain: "unknown.test" }),
      rateEnv(),
      fetcher,
    );
    assert.equal(response.status, 503);
    assert.equal((await response.json()).error.code, "resolver_unavailable");
  }
});

test("DNS fetches use the redirect mode supported by the Workers runtime", async () => {
  const redirects = [];
  const result = await discoverDomain("unknown.test", async (request, init) => {
    redirects.push(init.redirect);
    return dnsResponse(
      [],
      {},
      new URL(request).searchParams.get("name"),
      { CNAME: 5, MX: 15, SRV: 33 }[new URL(request).searchParams.get("type")],
    );
  });
  assert.deepEqual(new Set(redirects), new Set(["manual"]));
  assert.equal(result.classification.status, "unknown");
});

test("private and rebinding-like DNS data can never become an outbound target", async () => {
  const targets = [];
  const result = await discoverDomain("unknown.test", async request => {
    const url = new URL(request);
    targets.push(url.origin);
    const type = url.searchParams.get("type");
    const answers = type === "MX"
      ? ["0 127.0.0.1."]
      : type === "CNAME" ? ["169.254.169.254."] : ["0 0 443 10.0.0.1."];
    return dnsResponse(
      answers,
      {},
      url.searchParams.get("name"),
      { CNAME: 5, MX: 15, SRV: 33 }[type],
    );
  });
  assert.deepEqual(new Set(targets), new Set(["https://cloudflare-dns.com"]));
  assert.equal(result.classification.status, "unknown");
  assert.doesNotMatch(JSON.stringify(result), /127\.0\.0\.1|169\.254|10\.0\.0\.1/);
});

test("successful responses are bounded, non-cacheable, and contain no mailbox local part", async () => {
  const response = await handleRequest(
    checkRequest({ domain: "gmail.com" }),
    rateEnv(),
    dnsFixture({}),
  );
  const body = await response.text();
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  assert.equal(response.headers.get("Access-Control-Allow-Origin"), origin);
  assert.ok(body.length < 32 * 1024);
  assert.doesNotMatch(body, /reader@/);
});

test("the Worker contains no application logging, cache, analytics, or storage path", async () => {
  const source = await readFile(new URL("../src/worker.mjs", import.meta.url), "utf8");
  for (const forbidden of [
    "console.",
    "caches.",
    "localStorage",
    "sessionStorage",
    "indexedDB",
    "sendBeacon",
    "AnalyticsEngine",
    "DurableObject",
  ]) {
    assert.equal(source.includes(forbidden), false, forbidden);
  }
});

test("the deployment remains bounded for the Workers Free plan", async () => {
  const config = JSON.parse(await readFile(
    new URL("../wrangler.jsonc", import.meta.url),
    "utf8",
  ));
  const cpuLimit = config.limits?.cpu_ms;
  assert.equal(
    cpuLimit === undefined || (
      Number.isInteger(cpuLimit) && cpuLimit > 0 && cpuLimit <= 10
    ),
    true,
  );
  assert.equal(config.observability.enabled, false);
  assert.equal(config.observability.logs.invocation_logs, false);
});

function checkRequest(body, headers = {}) {
  return new Request(endpoint, {
    method: "POST",
    headers: { Origin: origin, "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
}

function rateEnv(success = true) {
  return { RATE_LIMITER: { limit: async () => ({ success }) } };
}

function dnsFixture(records) {
  return async request => {
    const url = new URL(request);
    const type = url.searchParams.get("type");
    return dnsResponse(
      records[type] || [],
      {},
      url.searchParams.get("name"),
      { CNAME: 5, MX: 15, SRV: 33 }[type],
    );
  };
}

function dnsResponse(answers, overrides = {}, name = "synthetic.test", type = 1) {
  return new Response(JSON.stringify({
    Status: 0,
    Answer: answers.map(data => ({ name: `${name}.`, type, TTL: 60, data })),
    ...overrides,
  }), {
    status: 200,
    headers: { "Content-Type": "application/dns-json" },
  });
}
