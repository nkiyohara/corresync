(function (root, factory) {
  "use strict";

  const checker = factory(root);
  if (typeof module === "object" && module.exports) {
    module.exports = checker;
    return;
  }
  root.CorresyncCompatibilityChecker = checker;
  if (root.document) {
    checker.enhance(root.document, root.fetch.bind(root));
  }
}(typeof globalThis === "object" ? globalThis : this, function (root) {
  "use strict";

  const endpoint = "https://discover.corresync.org/v1/check";
  const maximumResponseBytes = 32 * 1024;
  const routeNames = Object.freeze({
    "microsoft-owa": "Outlook Web",
    "microsoft-graph": "Microsoft Graph",
    google: "Gmail and Google Calendar",
    jmap: "JMAP",
    "imap-smtp": "IMAP / SMTP",
    caldav: "CalDAV",
  });
  const routeStatus = Object.freeze({
    available: "Available now",
    additional_setup: "Available with setup",
    not_available: "Coming soon",
  });
  const familyNames = Object.freeze({
    "microsoft-consumer": "Outlook.com, Hotmail, or Live",
    "microsoft-365": "Microsoft 365 / Exchange Online (commonly used through Outlook)",
    "google-consumer": "Gmail",
    "google-workspace": "Google Workspace",
    standards: "Standards-based provider",
    unknown: "Provider not identified",
  });
  const confidenceNames = Object.freeze({
    high: "High confidence",
    medium: "Medium confidence",
    low: "Low confidence",
    unknown: "Confidence unknown",
  });
  const signInNames = Object.freeze({
    provider_browser: "A dedicated visible browser owns sign-in",
    public_oauth: "System-browser OAuth; the grant stays in the OS keyring",
    external_credential: "OS keyring or an explicitly approved credential helper",
    disabled: "Disabled; no sign-in can start",
  });
  const nextLinks = Object.freeze({
    "providers/google": { href: "#google", label: "Read about Google support" },
    "providers/route-cards": { href: "#route-cards", label: "Compare provider routes" },
    "providers/microsoft-owa": { href: "#microsoft-owa", label: "See Outlook Web" },
    "providers/microsoft-graph": { href: "#microsoft-graph", label: "See Microsoft Graph" },
    "providers/jmap": { href: "#jmap", label: "See JMAP" },
    "providers/imap-smtp": { href: "#imap-smtp", label: "See IMAP and SMTP" },
    "providers/caldav": { href: "#caldav", label: "See CalDAV" },
    "getting-started/install": { href: "getting-started.html#step-install", label: "Install Corresync" },
    "getting-started/sign-in": { href: "getting-started.html#sign-in", label: "See the setup steps" },
  });
  const evidenceNames = Object.freeze({
    known_domain: "Known provider domain",
    mx_provider: "Mail exchange",
    autodiscover_provider: "Autodiscover record",
    srv_imaps: "Secure IMAP service",
    srv_submission: "Mail submission service",
    srv_caldavs: "CalDAV service",
    srv_jmap: "JMAP service",
  });
  const statusCopy = Object.freeze({
    verified: {
      glyph: "✓",
      title: "This address looks ready for Corresync.",
      body: "Public records match a route that is available now. Sign-in and organization policy still decide what the account can do.",
    },
    likely: {
      glyph: "≈",
      title: "This address looks compatible.",
      body: "Public records point to a Corresync route, but the provider and your organization confirm the final capabilities at sign-in.",
    },
    additional_setup: {
      glyph: "+",
      title: "This address may work with a little setup.",
      body: "The domain publishes a standards-based or explicitly configured route. You will need to confirm its endpoint and credential requirements locally.",
    },
    conflict: {
      glyph: "?",
      title: "We found more than one possible route.",
      body: "That can be normal for custom domains. Compare the routes below and choose the one your account administrator supports.",
    },
    not_available: {
      glyph: "…",
      title: "Gmail and Google Calendar are coming soon.",
      body: "The Gmail and Calendar API integration is built and included in RC releases, but it is completely disabled while production OAuth approval is pending. No Google sign-in can start. Until approval, Google’s official Workspace MCP servers are the available interim option.",
    },
    unknown: {
      glyph: "?",
      title: "We could not identify this provider yet.",
      body: "That does not mean the address is unsupported. Public DNS may not advertise enough information, so local discovery or a provider-supplied endpoint is the next step.",
    },
  });

  function normalizeDomain(value) {
    if (typeof value !== "string" || value === "" || value !== value.trim() ||
      value.length > 253 || value.includes("@") || /[\s\u0000-\u001f/:?#\[\]]/.test(value)) {
      return "";
    }
    let hostname;
    try {
      hostname = new URL(`https://${value.toLowerCase()}`).hostname;
    } catch (_) {
      return "";
    }
    if (!hostname.includes(".") || /^\d+(?:\.\d+){3}$/.test(hostname) || hostname.includes(":")) {
      return "";
    }
    const labels = hostname.split(".");
    if (labels.some(label => label.length < 1 || label.length > 63 ||
      !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label))) {
      return "";
    }
    return hostname;
  }

  function normalizeEmailDomain(value) {
    if (typeof value !== "string" || value !== value.trim() || /\s/.test(value)) {
      return "";
    }
    const separator = value.lastIndexOf("@");
    if (separator < 1 || separator !== value.indexOf("@") || separator === value.length - 1) {
      return "";
    }
    return normalizeDomain(value.slice(separator + 1));
  }

  function buildLookupRequest(domain) {
    const normalized = normalizeDomain(domain);
    if (!normalized) {
      throw new TypeError("a normalized domain is required");
    }
    return [endpoint, {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify({ domain: normalized }),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
    }];
  }

  function enhance(document, fetcher) {
    const form = document.getElementById("compatibility-form");
    if (!form || typeof fetcher !== "function") {
      return;
    }
    const input = document.getElementById("compatibility-email");
    const button = document.getElementById("compatibility-submit");
    const validation = document.getElementById("compatibility-validation");
    const live = document.getElementById("compatibility-live");
    const result = document.getElementById("compatibility-result");
    let activeController;
    let requestSequence = 0;
    form.addEventListener("submit", async event => {
      event.preventDefault();
      const domain = normalizeEmailDomain(input.value);
      if (!domain) {
        validation.hidden = false;
        input.setAttribute("aria-invalid", "true");
        input.focus();
        return;
      }
      input.value = "";
      input.removeAttribute("aria-invalid");
      validation.hidden = true;
      result.hidden = true;
      button.disabled = true;
      activeController?.abort();
      const controller = new AbortController();
      activeController = controller;
      const sequence = ++requestSequence;
      live.textContent = "Checking the domain’s public provider records…";
      const slowTimer = root.setTimeout(() => {
        if (sequence === requestSequence) {
          live.textContent = "Still checking. Some DNS providers take a little longer to answer.";
        }
      }, 2_500);
      const timeoutTimer = root.setTimeout(() => controller.abort(), 8_000);
      try {
        const [url, options] = buildLookupRequest(domain);
        const response = await fetcher(url, { ...options, signal: controller.signal });
        const payload = await readResponse(response);
        if (!response.ok) {
          throw new CheckerError(errorMessage(payload?.error?.code));
        }
        renderResult(document, result, validateResult(payload));
        result.hidden = false;
        result.focus();
        live.textContent = "Compatibility result ready.";
      } catch (error) {
        if (sequence !== requestSequence) {
          return;
        }
        const message = error instanceof CheckerError
          ? error.message
          : error?.name === "AbortError"
            ? "The check timed out. Nothing was stored; please try again."
            : "The compatibility checker is unavailable right now. You can still run local discovery after installing Corresync.";
        renderUnavailable(document, result, message);
        result.hidden = false;
        result.focus();
        live.textContent = "The compatibility check could not finish.";
      } finally {
        root.clearTimeout(slowTimer);
        root.clearTimeout(timeoutTimer);
        if (sequence === requestSequence) {
          button.disabled = false;
          activeController = undefined;
        }
      }
    });
    button.disabled = false;
  }

  async function readResponse(response) {
    const declaredLength = Number(response.headers.get("Content-Length") || "0");
    if (!Number.isFinite(declaredLength) || declaredLength < 0 ||
      declaredLength > maximumResponseBytes || !response.body) {
      throw new CheckerError("The lookup service returned an unexpected response.");
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder("utf-8", { fatal: true });
    let bytes = 0;
    let raw = "";
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        bytes += value.byteLength;
        if (bytes > maximumResponseBytes) {
          throw new CheckerError("The lookup service returned an unexpected response.");
        }
        raw += decoder.decode(value, { stream: true });
      }
      raw += decoder.decode();
      return JSON.parse(raw);
    } catch (_) {
      throw new CheckerError("The lookup service returned an unexpected response.");
    } finally {
      reader.releaseLock();
    }
  }

  function validateResult(value) {
    if (!plainObject(value) || value.schemaVersion !== 1 ||
      normalizeDomain(value.normalizedDomain) !== value.normalizedDomain ||
      !plainObject(value.classification) || typeof value.classification.conflict !== "boolean" ||
      !Object.hasOwn(familyNames, value.classification.variant) ||
      !Object.hasOwn(confidenceNames, value.classification.confidence) ||
      !["verified", "likely", "additional_setup", "not_available", "unknown"].includes(
        value.classification.status,
      ) || !Array.isArray(value.routes) || value.routes.length > 8 ||
      !Array.isArray(value.caveats) || value.caveats.length > 8 ||
      !Array.isArray(value.evidence) || value.evidence.length > 16 ||
      !Array.isArray(value.next) || value.next.length > 3) {
      throw new CheckerError("The lookup service returned an unexpected response.");
    }
    for (const route of value.routes) {
      if (!plainObject(route) || !Object.hasOwn(routeNames, route.provider) ||
        !Object.hasOwn(routeStatus, route.status) || !plainObject(route.capabilities) ||
        !safeText(route.capabilities.mail, 200) || !safeText(route.capabilities.calendar, 200) ||
        !plainObject(route.signIn) || !Object.hasOwn(signInNames, route.signIn.owner) ||
        !Array.isArray(route.requirements) || route.requirements.length > 6 ||
        !Array.isArray(route.caveats) || route.caveats.length > 6 ||
        !route.requirements.every(item => safeText(item, 200)) ||
        !route.caveats.every(item => safeText(item, 200))) {
        throw new CheckerError("The lookup service returned an unexpected response.");
      }
    }
    if (!value.caveats.every(item => safeText(item, 240)) ||
      !value.evidence.every(item => plainObject(item) && Object.hasOwn(evidenceNames, item.category)) ||
      !value.next.every(item => plainObject(item) && Object.hasOwn(nextLinks, item.target))) {
      throw new CheckerError("The lookup service returned an unexpected response.");
    }
    return value;
  }

  function renderResult(document, result, value) {
    clear(result);
    const status = value.classification.conflict
      ? statusCopy.conflict
      : statusCopy[value.classification.status];
    result.append(
      element(document, "p", "checker-result-status", `${status.glyph} ${statusLabel(value)}`),
      element(document, "h3", "checker-result-title", status.title),
      element(
        document,
        "p",
        "checker-result-family",
        `${familyNames[value.classification.variant]} · ${confidenceNames[value.classification.confidence]}`,
      ),
      element(document, "p", "checker-result-body", status.body),
    );
    if (value.routes.length > 0) {
      const heading = element(document, "h4", "checker-subtitle", "Routes to consider");
      const list = element(document, "div", "checker-route-list");
      for (const route of value.routes) {
        const item = element(document, "section", "checker-route");
        const headingRow = element(document, "div", "checker-route-heading");
        headingRow.append(
          element(document, "h5", "", routeNames[route.provider]),
          element(document, "span", "chip", routeStatus[route.status]),
        );
        const facts = document.createElement("dl");
        appendFact(document, facts, "Mail", route.capabilities.mail);
        appendFact(document, facts, "Calendar", route.capabilities.calendar);
        appendFact(document, facts, "Sign-in", signInNames[route.signIn.owner]);
        item.append(headingRow, facts);
        if (route.requirements.length > 0) {
          item.append(element(document, "p", "checker-route-note", route.requirements.join(" ")));
        }
        if (route.caveats.length > 0) {
          item.append(element(document, "p", "checker-route-note", route.caveats.join(" ")));
        }
        list.append(item);
      }
      result.append(heading, list);
    }
    result.append(element(
      document,
      "p",
      "checker-isolation",
      "Add this account separately when you are ready. Its identity and authorization stay isolated from your other work or personal accounts, while combined searches and agendas keep the source account visible.",
    ));
    if (value.caveats.length > 0 || value.evidence.length > 0) {
      const details = document.createElement("details");
      details.className = "checker-details";
      details.append(element(document, "summary", "", "Technical details and limits"));
      if (value.evidence.length > 0) {
        details.append(element(
          document,
          "p",
          "",
          `Public signals: ${value.evidence.map(item => evidenceNames[item.category]).join(", ")}.`,
        ));
      }
      for (const caveat of value.caveats) {
        details.append(element(document, "p", "", caveat));
      }
      result.append(details);
    }
    appendNextLinks(document, result, value.next);
  }

  function renderUnavailable(document, result, message) {
    clear(result);
    result.append(
      element(document, "p", "checker-result-status", "! Check unavailable"),
      element(document, "h3", "checker-result-title", "We could not complete the check."),
      element(document, "p", "checker-result-body", message),
    );
    appendNextLinks(document, result, [
      { target: "getting-started/install" },
      { target: "providers/route-cards" },
    ]);
  }

  function appendNextLinks(document, result, targets) {
    const actions = element(document, "div", "checker-result-actions");
    for (const item of targets) {
      const definition = nextLinks[item.target];
      const link = element(document, "a", "button secondary", definition.label);
      link.href = definition.href;
      actions.append(link);
    }
    result.append(actions);
  }

  function statusLabel(value) {
    if (value.classification.conflict) return "More than one route found";
    return {
      verified: "Available now",
      likely: "Likely compatible",
      additional_setup: "Additional setup",
      not_available: "Coming soon",
      unknown: "Not identified",
    }[value.classification.status];
  }

  function errorMessage(code) {
    if (code === "rate_limited") return "The checker is busy. Please wait a minute and try again.";
    if (code === "invalid_domain") return "The lookup service did not accept that domain.";
    return "The compatibility checker is unavailable right now. You can still run local discovery after installing Corresync.";
  }

  function element(document, tag, className, content) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (content !== undefined) node.textContent = content;
    return node;
  }

  function appendFact(document, list, label, value) {
    const row = document.createElement("div");
    row.append(
      element(document, "dt", "", label),
      element(document, "dd", "", value),
    );
    list.append(row);
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function safeText(value, maximum) {
    return typeof value === "string" && value.length > 0 && value.length <= maximum &&
      !/[\r\n\u0000]/.test(value);
  }

  function plainObject(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value) &&
      Object.getPrototypeOf(value) === Object.prototype;
  }

  class CheckerError extends Error {}

  return Object.freeze({ buildLookupRequest, enhance, normalizeDomain, normalizeEmailDomain });
}));
