const assert = require("node:assert/strict");
const { readFile } = require("node:fs/promises");
const test = require("node:test");

const {
  buildLookupRequest,
  messagesForLanguage,
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
    "reader@example.com\\evil.com",
    "reader@exa%6dple.com",
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

test("the copy enhancement stays local and falls back safely for unknown languages", async () => {
  const source = await readFile(new URL("copy.js", `file://${__dirname}/`), "utf8");
  for (const forbidden of [
    "fetch(",
    "XMLHttpRequest",
    "WebSocket",
    "EventSource",
    "sendBeacon",
    "innerHTML",
    "outerHTML",
  ]) {
    assert.equal(source.includes(forbidden), false, forbidden);
  }
  assert.match(source, /Object\.hasOwn\(messagesByLanguage, language\)/);
});

test("every localized form starts inert and sends no address through HTML", async () => {
  const pages = [
    ["providers.html", /Only the domain leaves your browser/, /src="check\.js" defer/],
    ["ja/providers.html", /ブラウザの外へ出るのはドメインだけ/, /src="\.\.\/check\.js" defer/],
    ["zh-cn/providers.html", /只有域名会离开浏览器/, /src="\.\.\/check\.js" defer/],
    ["zh-tw/providers.html", /只有網域會離開瀏覽器/, /src="\.\.\/check\.js" defer/],
    ["ko/providers.html", /브라우저 밖으로 나가는 정보는 도메인뿐입니다/, /src="\.\.\/check\.js" defer/],
  ];
  for (const [path, privacyCopy, scriptSource] of pages) {
    const source = await readFile(new URL(path, `file://${__dirname}/`), "utf8");
    const form = source.match(/<form\s+id="compatibility-form"[\s\S]*?<\/form>/)?.[0];
    assert.ok(form, path);
    assert.doesNotMatch(form, /\saction=/, path);
    assert.match(form, /id="compatibility-submit"[\s\S]*?disabled/, path);
    assert.match(source, privacyCopy, path);
    assert.match(source, scriptSource, path);
  }
});

test("the checker includes copy for every published language", async () => {
  const source = await readFile(new URL("check.js", `file://${__dirname}/`), "utf8");
  for (const selector of ["ja: japaneseCopy", '"zh-Hans": simplifiedChineseCopy', '"zh-Hant": traditionalChineseCopy', "ko: koreanCopy"]) {
    assert.ok(source.includes(selector), selector);
  }
});

test("checker messages localize the user-owned Google OAuth route", () => {
  const expectations = new Map([
    ["en", ["Checking the domain’s public provider records…", /Desktop OAuth client/]],
    ["ja", ["ドメインの公開情報からプロバイダーを確認しています…", /デスクトップOAuthクライアント/]],
    ["zh-Hans", ["正在通过域名的公共记录确认服务提供商…", /桌面 OAuth 客户端/]],
    ["zh-Hant", ["正在從網域的公開記錄確認服務供應商…", /桌面 OAuth 用戶端/]],
    ["ko", ["도메인의 공개 레코드에서 서비스 제공자를 확인하고 있습니다…", /데스크톱 OAuth 클라이언트/]],
  ]);
  for (const [language, [checking, googleSignIn]] of expectations) {
    const messages = messagesForLanguage(language);
    assert.equal(messages.checking, checking, language);
    assert.match(messages.signInNames.user_owned_oauth, googleSignIn, language);
  }
  assert.equal(messagesForLanguage("constructor"), messagesForLanguage("en"));
});

test("every localized page explains the user-owned Google OAuth route", async () => {
  const locales = new Map([
    ["ja", /自分|デスクトップ|クライアント/],
    ["zh-cn", /自己|桌面|客户端/],
    ["zh-tw", /自行|桌面|用戶端/],
    ["ko", /직접|데스크톱|클라이언트/],
  ]);
  const pages = [
    "index.html",
    "getting-started.html",
    "providers.html",
    "google-oauth.html",
    "features.html",
    "safety.html",
    "privacy.html",
    "terms.html",
  ];
  for (const [locale, ownership] of locales) {
    for (const page of pages) {
      const path = `${locale}/${page}`;
      const source = await readFile(new URL(path, `file://${__dirname}/`), "utf8");
      assert.match(source, /Google|Gmail/, path);
      assert.match(source, /OAuth/, path);
      assert.match(source, ownership, path);
    }
  }
});

test("localized setup guides keep the verified install and MCP commands", async () => {
  const pages = [
    "getting-started.html",
    "ja/getting-started.html",
    "zh-cn/getting-started.html",
    "zh-tw/getting-started.html",
    "ko/getting-started.html",
  ];
  const commands = [
    "curl -LsSf https://corresync.org/install.sh | sh",
    'irm https://corresync.org/install.ps1 | iex',
    "winget install --id nkiyohara.Corresync --exact",
    "brew install nkiyohara/corresync/corresync",
    "scoop install corresync/corresync",
    "corr --version",
    "corr mcp setup codex",
    "corr mcp setup claude-code",
    "corr mcp setup github-copilot",
    "corr mcp setup gemini-cli",
    "corr mcp setup qwen-code",
    "corr mcp setup qoder",
    "corr mcp config kimi-code",
    "corr mcp serve",
  ];
  for (const path of pages) {
    const source = await readFile(new URL(path, `file://${__dirname}/`), "utf8");
    for (const command of commands) {
      assert.ok(source.includes(command), `${path}: ${command}`);
    }
    assert.doesNotMatch(source, /corr mcp setup (?:cursor|vscode|opencode)/, path);
  }
});
