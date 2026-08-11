import catalog from "../catalog.json" with { type: "json" };

const SCHEMA_VERSION = 1;
const DNS_ENDPOINT = "https://cloudflare-dns.com/dns-query";
const MAX_REQUEST_BYTES = 512;
const MAX_DNS_BYTES = 64 * 1024;
const MAX_DNS_ANSWERS = 64;
const MAX_RESPONSE_BYTES = 32 * 1024;
const DNS_TIMEOUT_MS = 4_500;
const DNS_TYPE_CODES = Object.freeze({ CNAME: 5, MX: 15, SRV: 33 });
const ALLOWED_ORIGINS = new Set([
  "https://corresync.org",
  "http://127.0.0.1:4173",
  "http://localhost:4173",
]);
const DNS_QUERIES = Object.freeze([
  { key: "mx", name: domain => domain, type: "MX" },
  { key: "autodiscover", name: domain => `autodiscover.${domain}`, type: "CNAME" },
  { key: "imaps", name: domain => `_imaps._tcp.${domain}`, type: "SRV" },
  { key: "submission", name: domain => `_submission._tcp.${domain}`, type: "SRV" },
  { key: "caldavs", name: domain => `_caldavs._tcp.${domain}`, type: "SRV" },
  { key: "jmap", name: domain => `_jmap._tcp.${domain}`, type: "SRV" },
]);

if (catalog.schemaVersion !== SCHEMA_VERSION) {
  throw new Error("discovery catalog schema is unsupported");
}

export default {
  async fetch(request, env) {
    return handleRequest(request, env, fetch);
  },
};

export async function handleRequest(request, env, fetcher = fetch) {
  const origin = request.headers.get("Origin") || "";
  if (!ALLOWED_ORIGINS.has(origin)) {
    return errorResponse("origin_not_allowed", 403, "");
  }
  const url = new URL(request.url);
  if (url.hostname !== "discover.corresync.org" || url.pathname !== "/v1/check" || url.search) {
    return errorResponse("not_found", 404, origin);
  }
  if (request.method === "OPTIONS") {
    return new Response(null, {
      status: 204,
      headers: responseHeaders(origin, {
        "Access-Control-Allow-Headers": "Content-Type",
        "Access-Control-Allow-Methods": "POST, OPTIONS",
        "Access-Control-Max-Age": "600",
      }),
    });
  }
  if (request.method !== "POST") {
    return errorResponse("method_not_allowed", 405, origin, { Allow: "POST, OPTIONS" });
  }
  if (!request.headers.get("Content-Type")?.toLowerCase().startsWith("application/json")) {
    return errorResponse("invalid_domain", 400, origin);
  }
  if (!env?.RATE_LIMITER?.limit) {
    return errorResponse("resolver_unavailable", 503, origin);
  }
  const rate = await env.RATE_LIMITER.limit({ key: "compatibility-check:v1" });
  if (!rate?.success) {
    return errorResponse("rate_limited", 429, origin, { "Retry-After": "60" });
  }

  let payload;
  try {
    const raw = await readBoundedText(request.body, MAX_REQUEST_BYTES);
    payload = JSON.parse(raw);
  } catch {
    return errorResponse("invalid_domain", 400, origin);
  }
  if (!isPlainObject(payload) || Object.keys(payload).length !== 1 ||
    typeof payload.domain !== "string") {
    return errorResponse("invalid_domain", 400, origin);
  }
  const domain = normalizeDomain(payload.domain);
  if (!domain) {
    return errorResponse("invalid_domain", 400, origin);
  }

  let result;
  try {
    result = await discoverDomain(domain, fetcher, request.signal);
  } catch {
    return errorResponse("resolver_unavailable", 503, origin);
  }
  return jsonResponse(result, 200, origin);
}

export async function discoverDomain(domain, fetcher, outerSignal) {
  const settled = await Promise.allSettled(DNS_QUERIES.map(async query => ({
    key: query.key,
    answers: await queryDNS(query.name(domain), query.type, fetcher, outerSignal),
  })));
  const observations = new Map();
  let unavailable = 0;
  for (const result of settled) {
    if (result.status === "fulfilled") {
      observations.set(result.value.key, result.value.answers);
    } else {
      unavailable++;
    }
  }
  if (unavailable === DNS_QUERIES.length && !matchKnownDomain(domain)) {
    throw new Error("all DNS lookups were unavailable");
  }
  return classify(domain, observations, unavailable);
}

export function normalizeDomain(value) {
  if (typeof value !== "string" || value === "" || value !== value.trim() ||
    value.length > 253 || value.includes("@") || /[\s\u0000-\u001f/:?#\[\]]/.test(value)) {
    return "";
  }
  let hostname;
  try {
    hostname = new URL(`https://${value.toLowerCase()}`).hostname;
  } catch {
    return "";
  }
  if (hostname !== value.toLowerCase() || isIPAddress(hostname) || !hostname.includes(".")) {
    return "";
  }
  const labels = hostname.split(".");
  if (labels.some(label => label.length < 1 || label.length > 63 ||
    !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label))) {
    return "";
  }
  return hostname;
}

async function queryDNS(name, type, fetcher, outerSignal) {
  const url = new URL(DNS_ENDPOINT);
  url.searchParams.set("name", name);
  url.searchParams.set("type", type);
  const signals = [AbortSignal.timeout(DNS_TIMEOUT_MS)];
  if (outerSignal) {
    signals.push(outerSignal);
  }
  const response = await fetcher(url, {
    method: "GET",
    headers: { Accept: "application/dns-json" },
    // Workerd does not implement redirect "error". Manual mode returns the
    // redirect response without following it, and the checks below reject it.
    redirect: "manual",
    signal: AbortSignal.any(signals),
  });
  if (response.status >= 300 && response.status < 400) {
    throw new Error("DNS resolver redirected");
  }
  if (response.url && new URL(response.url).origin !== new URL(DNS_ENDPOINT).origin) {
    throw new Error("DNS response came from an unexpected origin");
  }
  if (!response.ok || !response.headers.get("Content-Type")?.toLowerCase().includes("json")) {
    throw new Error("DNS resolver failed");
  }
  const contentLength = Number(response.headers.get("Content-Length") || "0");
  if (contentLength > MAX_DNS_BYTES) {
    throw new Error("DNS response is oversized");
  }
  const raw = await readBoundedText(response.body, MAX_DNS_BYTES);
  const decoded = JSON.parse(raw);
  if (!isPlainObject(decoded) || !Number.isInteger(decoded.Status) || decoded.Status < 0 ||
    decoded.Status > 23) {
    throw new Error("DNS response is malformed");
  }
  if (decoded.Status !== 0 && decoded.Status !== 3) {
    throw new Error("DNS lookup was unavailable");
  }
  if (decoded.Answer === undefined) {
    return [];
  }
  if (!Array.isArray(decoded.Answer) || decoded.Answer.length > MAX_DNS_ANSWERS) {
    throw new Error("DNS answer collection is unbounded");
  }
  const expectedName = name.toLowerCase().replace(/\.$/, "");
  const answers = [];
  for (const answer of decoded.Answer) {
    if (!isPlainObject(answer) || typeof answer.name !== "string" ||
      !Number.isInteger(answer.type) || typeof answer.data !== "string" ||
      answer.name.length > 253 || answer.data.length > 2048 ||
      /[\r\n\u0000]/.test(answer.name) || /[\r\n\u0000]/.test(answer.data)) {
      throw new Error("DNS answer is malformed");
    }
    if (answer.type === DNS_TYPE_CODES[type] &&
      answer.name.toLowerCase().replace(/\.$/, "") === expectedName) {
      answers.push(answer.data.toLowerCase());
    }
  }
  return answers;
}

async function readBoundedText(stream, maximum) {
  if (!stream) {
    return "";
  }
  const reader = stream.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let total = 0;
  let text = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      total += value.byteLength;
      if (total > maximum) {
        throw new Error("body exceeds the limit");
      }
      text += decoder.decode(value, { stream: true });
    }
    return text + decoder.decode();
  } finally {
    reader.releaseLock();
  }
}

function classify(domain, observations, unavailable) {
  const scores = new Map();
  const evidence = [];
  const known = matchKnownDomain(domain);
  if (known) {
    addScore(scores, known.id, 100);
    addEvidence(evidence, "known_domain", "Known provider domain matched");
  }
  for (const data of observations.get("mx") || []) {
    const host = parseMXHost(data);
    const family = matchSuffixFamily(host, "mailExchangeSuffixes");
    if (family) {
      addScore(scores, family.id, family.id.startsWith("google-") ? 75 : 60);
      addEvidence(evidence, "mx_provider", "Mail exchange matched a known provider");
    }
  }
  for (const data of observations.get("autodiscover") || []) {
    const family = matchSuffixFamily(trimDNSName(data), "autodiscoverCnameSuffixes");
    if (family) {
      addScore(scores, family.id, 45);
      addEvidence(evidence, "autodiscover_provider", "Autodiscover matched a known provider");
    }
  }
  const standards = {
    imaps: hasSRV(observations.get("imaps")),
    submission: hasSRV(observations.get("submission")),
    caldavs: hasSRV(observations.get("caldavs")),
    jmap: hasSRV(observations.get("jmap")),
  };
  for (const [key, observed] of Object.entries(standards)) {
    if (observed) {
      addEvidence(evidence, `srv_${key}`, "A standards service record was published");
    }
  }

  const grouped = groupedScores(scores);
  const standardsCount = Object.values(standards).filter(Boolean).length;
  if (standardsCount > 0 && grouped.size === 0) {
    grouped.set("standards", standardsCount * 30);
  }
  const ranked = [...grouped.entries()].sort((left, right) => right[1] - left[1]);
  const conflict = ranked.length > 1;
  const primary = ranked[0]?.[0] || "unknown";
  const routes = routesFor(primary, standards, conflict ? ranked.map(item => item[0]) : []);
  const classification = classificationFor(
    primary,
    strongestVariant(scores, primary),
    ranked[0]?.[1] || 0,
    conflict,
  );
  const caveats = [
    "Provider detection does not guarantee sign-in or permission.",
    "Organization policy, administrator consent, disabled protocols, app passwords, or a bridge may still be required.",
  ];
  if (unavailable > 0) {
    caveats.push("Some public DNS signals were unavailable during this lookup.");
  }
  return {
    schemaVersion: SCHEMA_VERSION,
    normalizedDomain: domain,
    classification,
    evidence: evidence.slice(0, 16),
    routes: routes.slice(0, 8),
    caveats,
    accountIsolation: {
      summary: "Each added account keeps separate authorization and identity while reads may aggregate with provenance.",
    },
    next: nextTargets(classification, routes),
  };
}

function classificationFor(group, variant, score, conflict) {
  if (conflict) {
    return {
      family: group,
      displayName: displayNameForGroup(group),
      variant,
      confidence: score >= 90 ? "high" : score >= 55 ? "medium" : "low",
      status: "likely",
      conflict: true,
      summary: "Public records point to more than one provider family.",
    };
  }
  if (group === "unknown") {
    return {
      family: "unknown", displayName: "Unknown provider", confidence: "unknown",
      variant: "unknown",
      status: "unknown", conflict: false,
      summary: "No recognized provider or standards signal was published.",
    };
  }
  if (group === "google") {
    const enabled = catalog.googleOAuthApproved === true;
    return {
      family: "google", displayName: displayNameForGroup(group),
      variant,
      confidence: score >= 90 ? "high" : "medium",
      status: enabled ? (score >= 90 ? "verified" : "likely") : "not_available",
      conflict: false,
      summary: enabled
        ? "Public records match the available Corresync Google route."
        : "The Corresync Google route is disabled while production OAuth approval is pending.",
    };
  }
  if (group === "standards") {
    return {
      family: "standards", displayName: "a standards-based provider",
      variant: "standards",
      confidence: score >= 60 ? "medium" : "low", status: "additional_setup",
      conflict: false,
      summary: "Use the published standards route with a credential you explicitly keep in the OS keyring or an approved helper.",
    };
  }
  return {
    family: group,
    displayName: displayNameForGroup(group),
    variant,
    confidence: score >= 90 ? "high" : "medium",
    status: score >= 90 ? "verified" : "likely",
    conflict: false,
    summary: "Public records match a supported Microsoft account family.",
  };
}

function routesFor(primary, standards, groups) {
  const wanted = new Set(groups.length ? groups : [primary]);
  const hasStandards = Object.values(standards).some(Boolean);
  const routes = [];
  if (wanted.has("microsoft")) {
    routes.push(route(
      "microsoft-owa", "Outlook Web", "available",
      "Typed mail reads and writes", "Selectable calendars and provider-supported Teams links",
      "provider_browser", "A dedicated visible browser owns sign-in.", [],
      ["Organization policy and existing mailbox permissions still apply."],
    ));
    routes.push(route(
      "microsoft-graph", "Microsoft Graph", "additional_setup",
      "Typed mail reads and writes", "Selectable calendars and typed Teams-link creation",
      "public_oauth", "Your authorized OAuth public client; grant in the OS keyring.",
      ["Register or use a public OAuth client your organization authorizes."],
      ["Graph is explicit and never an automatic fallback."],
    ));
  }
  if (wanted.has("google")) {
    const enabled = catalog.googleOAuthApproved === true;
    routes.push(route(
      "google", "Gmail and Google Calendar", enabled ? "available" : "not_available",
      enabled ? "Typed Gmail API operations" : "Gmail API support is included but disabled",
      enabled
        ? "Selectable calendars and provider-supported Google Meet links"
        : "Google Calendar and Meet support is included but disabled",
      enabled ? "public_oauth" : "disabled",
      enabled
        ? "The normal browser owns OAuth; the grant stays in the OS keyring."
        : "Not active in this RC; no Google sign-in can start.",
      [],
      enabled
        ? ["Provider policy and existing mailbox permissions still apply."]
        : ["Production OAuth approval is pending.", "A separate reviewed release must enable the route."],
    ));
  }
  if (wanted.has("standards") || groups.length > 0 ||
    (primary !== "google" && hasStandards)) {
    if (standards.jmap) {
      routes.push(route(
        "jmap", "JMAP", "additional_setup", "Typed JMAP mail operations", "Not provided by this route",
        "external_credential", "OS keyring or approved credential helper.",
        ["Provide a credential handle after local setup confirms the endpoint."],
        ["Server capabilities determine available writes."],
      ));
    }
    if (standards.imaps || standards.submission) {
      const missing = [];
      if (!standards.imaps) missing.push("No IMAPS service record was observed.");
      if (!standards.submission) missing.push("No mail-submission service record was observed.");
      routes.push(route(
        "imap-smtp", "IMAP / SMTP", "additional_setup",
        "IMAP reads and organization; SMTP draft and send when both services are available",
        "Not provided by this route", "external_credential",
        "OS keyring or approved credential helper.",
        ["Confirm both encrypted endpoints and any provider-specific app-password or bridge requirement."],
        missing,
      ));
    }
    if (standards.caldavs) {
      routes.push(route(
        "caldav", "CalDAV", "additional_setup", "Not provided by this route",
        "Typed calendar operations and conditional scheduling",
        "external_credential", "OS keyring or approved credential helper.",
        ["Confirm the exact HTTPS calendar collection and credential requirement."],
        ["Attendee scheduling requires server-observed RFC 6638 support."],
      ));
    }
  }
  return routes;
}

function route(
  provider,
  displayName,
  status,
  mail,
  calendar,
  signInOwner,
  signInMethod,
  requirements,
  caveats,
) {
  return {
    provider, displayName, status,
    capabilities: { mail, calendar },
    signIn: { owner: signInOwner, method: signInMethod },
    requirements,
    evidenceState: "synthetic_contract_live_unobserved",
    caveats,
  };
}

function nextTargets(classification, routes) {
  if (classification.status === "not_available") {
    return [{ target: "providers/google" }, { target: "getting-started/sign-in" }];
  }
  if (classification.status === "unknown" || classification.conflict) {
    return [{ target: "providers/route-cards" }, { target: "getting-started/install" }];
  }
  const first = routes[0]?.provider;
  return [
    { target: "getting-started/install" },
    { target: first ? `providers/${first}` : "providers/route-cards" },
  ];
}

function matchKnownDomain(domain) {
  return catalog.families.find(family => family.knownDomains?.includes(domain));
}

function matchSuffixFamily(value, property) {
  if (!value) return undefined;
  return catalog.families.find(family => family[property]?.some(
    suffix => value === suffix || value.endsWith(`.${suffix}`),
  ));
}

function groupedScores(scores) {
  const grouped = new Map();
  for (const [family, score] of scores) {
    const group = family.startsWith("microsoft-")
      ? "microsoft"
      : family.startsWith("google-") ? "google" : family;
    grouped.set(group, (grouped.get(group) || 0) + score);
  }
  return grouped;
}

function strongestVariant(scores, group) {
  if (group === "unknown" || group === "standards") return group;
  const prefix = `${group}-`;
  return [...scores.entries()]
    .filter(([family]) => family.startsWith(prefix))
    .sort((left, right) => right[1] - left[1])[0]?.[0] || group;
}

function displayNameForGroup(group) {
  if (group === "microsoft") return "Microsoft 365 / Outlook";
  if (group === "google") return "Google Gmail / Workspace";
  return group;
}

function parseMXHost(data) {
  const fields = data.trim().split(/\s+/);
  return fields.length === 2 && /^\d+$/.test(fields[0]) ? trimDNSName(fields[1]) : "";
}

function hasSRV(values) {
  return (values || []).some(data => {
    const fields = data.trim().split(/\s+/);
    return fields.length === 4 && fields.slice(0, 3).every(value => /^\d+$/.test(value)) &&
      trimDNSName(fields[3]) !== "";
  });
}

function trimDNSName(value) {
  const result = value.toLowerCase().replace(/\.$/, "");
  return normalizeDomain(result);
}

function addScore(scores, family, value) {
  scores.set(family, (scores.get(family) || 0) + value);
}

function addEvidence(evidence, category, label) {
  if (!evidence.some(item => item.category === category)) {
    evidence.push({ category, label });
  }
}

function isIPAddress(value) {
  return /^\d+(?:\.\d+){3}$/.test(value) || value.includes(":");
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype;
}

function errorResponse(code, status, origin, extraHeaders = {}) {
  return jsonResponse({ schemaVersion: SCHEMA_VERSION, error: { code } }, status, origin, extraHeaders);
}

function jsonResponse(value, status, origin, extraHeaders = {}) {
  const body = JSON.stringify(value);
  if (body.length > MAX_RESPONSE_BYTES) {
    return errorResponse("resolver_unavailable", 503, origin);
  }
  return new Response(body, {
    status,
    headers: responseHeaders(origin, extraHeaders),
  });
}

function responseHeaders(origin, extra = {}) {
  const headers = {
    "Cache-Control": "no-store",
    "Content-Type": "application/json; charset=utf-8",
    "Referrer-Policy": "no-referrer",
    "X-Content-Type-Options": "nosniff",
    Vary: "Origin",
    ...extra,
  };
  if (ALLOWED_ORIGINS.has(origin)) {
    headers["Access-Control-Allow-Origin"] = origin;
  }
  return headers;
}
