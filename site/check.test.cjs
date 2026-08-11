const assert = require("node:assert/strict");
const { readFile } = require("node:fs/promises");
const test = require("node:test");

const {
  buildLookupRequest,
  normalizeDomain,
  normalizeEmailDomain,
} = require("./check.js");

test("the browser derives and sends only the domain", () => {
  const domain = normalizeEmailDomain("reader+private@Example.COM");
  assert.equal(domain, "example.com");
  const [url, request] = buildLookupRequest(domain);
  assert.equal(url, "https://discover.corresync.org/v1/check");
  assert.equal(request.method, "POST");
  assert.equal(request.credentials, "omit");
  assert.equal(request.cache, "no-store");
  assert.equal(request.redirect, "error");
  assert.equal(request.referrerPolicy, "no-referrer");
  assert.deepEqual(JSON.parse(request.body), { domain: "example.com" });
  assert.doesNotMatch(url + request.body, /reader|private|@/);
});

test("internationalized domains become normalized ASCII locally", () => {
  assert.equal(normalizeEmailDomain("reader@bücher.example"), "xn--bcher-kva.example");
  assert.equal(normalizeDomain("xn--bcher-kva.example"), "xn--bcher-kva.example");
});

test("incomplete addresses and unsafe domains never produce a request", () => {
  for (const value of [
    "reader",
    "@example.com",
    "reader@",
    "reader@@example.com",
    " reader@example.com",
    "reader @example.com",
    "reader@localhost",
    "reader@127.0.0.1",
    "reader@example.com/path",
  ]) {
    assert.equal(normalizeEmailDomain(value), "", value);
  }
  assert.throws(() => buildLookupRequest("reader@example.com"), TypeError);
});

test("the public script contains no persistence, URL, logging, or HTML injection sink", async () => {
  const source = await readFile(new URL("check.js", `file://${__dirname}/`), "utf8");
  for (const forbidden of [
    "localStorage",
    "sessionStorage",
    "document.cookie",
    "indexedDB",
    "sendBeacon",
    "location.search",
    "location.hash",
    "history.",
    "innerHTML",
    "outerHTML",
    "console.",
  ]) {
    assert.equal(source.includes(forbidden), false, forbidden);
  }
});

test("the form has no action and starts inert until the script attaches", async () => {
  const source = await readFile(new URL("providers.html", `file://${__dirname}/`), "utf8");
  const form = source.match(/<form\s+id="compatibility-form"[\s\S]*?<\/form>/)?.[0];
  assert.ok(form);
  assert.doesNotMatch(form, /\saction=/);
  assert.match(form, /id="compatibility-submit"[\s\S]*?disabled/);
  assert.match(source, /Only the domain leaves your browser/);
});
