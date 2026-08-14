package browser

const teamsObservationScript = `(() => {
  const one = selectors => {
    for (const selector of selectors) {
      const node = document.querySelector(selector);
      if (node) return node;
    }
    return null;
  };
  const shell = one([
    "[data-tid='app-layout-area--main']",
    "[data-tid='teams-shell-main']",
    "#app-mount [role='main']"
  ]);
  if (!shell) return {state: document.readyState === "complete" ? "unknown" : "loading"};
  const profile = one([
    "[data-tid='me-control-avatar-trigger']",
    "[data-tid='me-control']",
    "button[data-tid*='profile']"
  ]);
  const tenant = one([
    "[data-tenant-id]",
    "[data-tid='tenant-switcher'][data-id]",
    "[data-tid='tenant-switcher'] [data-id]"
  ]);
  const label = String(profile && (profile.getAttribute("aria-label") || profile.getAttribute("title")) || "");
  const email = label.match(/[A-Z0-9.!#$%&'*+/=?^_{|}~-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i);
  const actorId = String(
    profile && (profile.getAttribute("data-object-id") || profile.getAttribute("data-user-id")) ||
    (email && email[0]) || ""
  ).slice(0, 1025);
  const workspaceId = String(
    tenant && (tenant.getAttribute("data-tenant-id") || tenant.getAttribute("data-id")) ||
    new URL(location.href).searchParams.get("tenantId") || ""
  ).slice(0, 1025);
  const exists = selectors => !!one(selectors);
  const list = exists([
    "[data-tid='app-bar-chat']", "[data-tid='app-bar-teams']",
    "[data-tid='chat-list']", "[data-tid='team-channel-list']"
  ]);
  const history = list && exists([
    "[data-tid='chat-pane-list']", "[data-tid='channel-posts-container']",
    "[data-tid='message-pane']", "[data-tid='app-bar-chat']"
  ]);
  const search = exists([
    "[data-tid='search-input']", "input[data-tid='search-box-input']",
    "input[role='combobox'][data-tid*='search']"
  ]);
  const send = exists([
    "[data-tid='ckeditor']", "[data-tid='send-message']",
    "[data-tid='app-bar-chat']"
  ]);
  const messageMenu = exists([
    "[data-tid='message-actions-more-button']",
    "[data-tid='message-action-more-options']"
  ]);
  return {
    state: "ready", workspaceId, actorId,
    displayName: label.replace(actorId, "").replace(/[(),]/g, " ").replace(/\s+/g, " ").trim().slice(0, 1025),
    list, history, search, send,
    edit: messageMenu,
    delete: messageMenu,
    reactions: messageMenu || exists(["[data-tid='reaction-button']"]),
    create: exists(["[data-tid='new-chat-button']", "[data-tid='join-or-create-team']"]),
    membership: exists(["[data-tid='view-members']", "[data-tid='manage-channel-members']"])
  };
})()`

const teamsConversationSnapshotScript = `async section => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const navSelectors = section === "chat"
    ? ["[data-tid='app-bar-chat']", "button[data-tid='app-bar-chat']"]
    : ["[data-tid='app-bar-teams']", "button[data-tid='app-bar-teams']"];
  let nav = null;
  for (const selector of navSelectors) {
    nav = document.querySelector(selector);
    if (nav) break;
  }
  if (!nav) return {state: "unknown", rows: []};
  nav.click();
  const listSelectors = section === "chat"
    ? ["[data-tid='chat-list']", "[data-tid='chat-list-container']"]
    : ["[data-tid='team-channel-list']", "[data-tid='teams-list']"];
  let container = null;
  for (let attempt = 0; attempt < 40 && !container; attempt++) {
    for (const selector of listSelectors) {
      container = document.querySelector(selector);
      if (container) break;
    }
    if (!container) await sleep(100);
  }
  if (!container) return {state: "unknown", rows: []};
  const clean = (value, maximum) => String(value || "")
    .replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim().slice(0, maximum);
  const rows = [];
  const seen = new Set();
  const nodes = Array.from(container.querySelectorAll(
    "a[href*='/l/chat/'], a[href*='/l/channel/'], a[href*='/l/team/'], [data-chat-id], [data-channel-id]"
  ));
  for (const node of nodes) {
    const href = node.href || (node.querySelector("a[href]") || {}).href || "";
    let chatId = clean(node.getAttribute("data-chat-id"), 1025);
    let teamId = clean(node.getAttribute("data-team-id") || node.getAttribute("data-group-id"), 1025);
    let channelId = clean(node.getAttribute("data-channel-id"), 1025);
    try {
      const target = new URL(href, location.origin);
      if (target.origin !== "https://teams.microsoft.com") continue;
      const parts = target.pathname.split("/").filter(Boolean).map(decodeURIComponent);
      if (parts[0] === "l" && parts[1] === "chat") chatId = chatId || clean(parts[2], 1025);
      if (parts[0] === "l" && (parts[1] === "channel" || parts[1] === "team")) {
        channelId = channelId || clean(parts[2], 1025);
        teamId = teamId || clean(target.searchParams.get("groupId"), 1025);
      }
    } catch (_) {
      continue;
    }
    const key = chatId + "\u0000" + teamId + "\u0000" + channelId;
    if ((!chatId && (!teamId || !channelId)) || seen.has(key)) continue;
    seen.add(key);
    const row = node.closest("[data-tid*='chat-list-item'], [data-tid*='channel-list-item'], li") || node;
    const type = clean(row.getAttribute("data-conversation-type"), 32).toLowerCase();
    const membership = clean(row.getAttribute("data-membership-type"), 32).toLowerCase();
    const count = Number(row.getAttribute("data-member-count"));
    const time = row.querySelector("time[datetime]");
    rows.push({
      chatId, teamId, channelId,
      kind: channelId ? "channel" : type === "oneonone" || type === "direct" ? "direct" : type === "meeting" ? "meeting" : "group",
      visibility: channelId ? membership === "private" ? "private" : membership === "shared" ? "shared" : "public" : "private",
      name: clean(row.getAttribute("aria-label") || (row.querySelector("[data-tid*='title']") || {}).textContent || row.textContent, 4097),
      topic: clean(row.getAttribute("data-topic"), 8193),
      memberCount: Number.isInteger(count) && count >= 0 ? count : 0,
      memberCountKnown: Number.isInteger(count) && count >= 0,
      lastActivityAt: clean(time && time.dateTime, 64)
    });
    if (rows.length > 256) return {state: "overflow", rows};
  }
  return {state: rows.length ? "rows" : "empty", rows};
}`

const teamsCurrentConversationScript = `(chatId, teamId, channelId) => {
  const main = document.querySelector(
    "[data-tid='chat-pane-header'], [data-tid='channel-header'], [data-tid='app-layout-area--main']"
  );
  if (!main) return {state: "unknown", rows: []};
  const clean = (value, maximum) => String(value || "")
    .replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim().slice(0, maximum);
  const type = clean(main.getAttribute("data-conversation-type"), 32).toLowerCase();
  const membership = clean(main.getAttribute("data-membership-type"), 32).toLowerCase();
  const count = Number(main.getAttribute("data-member-count"));
  const time = main.querySelector("time[datetime]");
  return {state: "rows", rows: [{
    chatId, teamId, channelId,
    kind: channelId ? "channel" : type === "oneonone" || type === "direct" ? "direct" : type === "meeting" ? "meeting" : "group",
    visibility: channelId ? membership === "private" ? "private" : membership === "shared" ? "shared" : "public" : "private",
    name: clean(main.getAttribute("aria-label") || (main.querySelector("[data-tid*='title']") || {}).textContent || main.textContent, 4097),
    topic: clean(main.getAttribute("data-topic"), 8193),
    memberCount: Number.isInteger(count) && count >= 0 ? count : 0,
    memberCountKnown: Number.isInteger(count) && count >= 0,
    lastActivityAt: clean(time && time.dateTime, 64)
  }]};
}`

const teamsMessageSnapshotScript = `(sensitive, chatId, teamId, channelId, threadRootId, selectedId) => {
  const pane = document.querySelector(
    "[data-tid='chat-pane-list'], [data-tid='channel-posts-container'], [data-tid='message-pane'], [role='main']"
  );
  if (!pane) return {state: "unknown", rows: []};
  const clean = (value, maximum) => String(value || "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ").trim().slice(0, maximum);
  const nodes = Array.from(pane.querySelectorAll(
    "[data-message-id], [data-mid], [data-tid='chat-pane-message'], [data-tid='channel-post']"
  ));
  const rows = [];
  const seen = new Set();
  for (const node of nodes) {
    const id = clean(node.getAttribute("data-message-id") || node.getAttribute("data-mid"), 1025);
    if (!id || seen.has(id) || (selectedId && id !== selectedId)) continue;
    seen.add(id);
    const body = node.querySelector(
      "[data-tid='message-body'], [data-tid='chat-pane-message-body'], [data-tid='post-message-content']"
    );
    const author = node.querySelector("[data-author-id], [data-user-id], [data-tid='message-author-name']");
    const created = node.querySelector("time[datetime]");
    const attachments = Array.from(node.querySelectorAll("[data-attachment-id], [data-file-id]"));
    const links = sensitive ? Array.from((body || node).querySelectorAll("a[href]"))
      .slice(0, 257).map(link => ({url: String(link.href || "").slice(0, 8193), label: clean(link.textContent, 2049)})) : [];
    const mentions = sensitive ? Array.from((body || node).querySelectorAll("[data-tid='mention'], [data-mention-id]"))
      .slice(0, 257).map(mention => ({
        id: clean(mention.getAttribute("data-object-id") || mention.getAttribute("data-mention-id"), 1025),
        kind: mention.getAttribute("data-mention-type") === "channel" ? "channel" : "user",
        displayName: clean(mention.textContent, 1025)
      })).filter(mention => mention.id) : [];
    const reactions = sensitive ? Array.from(node.querySelectorAll("[data-tid='reaction-pill'], [data-reaction-type]"))
      .slice(0, 257).map(reaction => {
        const rawCount = Number(reaction.getAttribute("data-count"));
        return {
          name: clean(reaction.getAttribute("data-reaction-type") || reaction.getAttribute("aria-label"), 257),
          count: Number.isInteger(rawCount) && rawCount >= 0 ? rawCount : 0,
          countKnown: Number.isInteger(rawCount) && rawCount >= 0,
          reactedByActor: reaction.getAttribute("aria-pressed") === "true" || reaction.getAttribute("data-reacted") === "true"
        };
      }).filter(reaction => reaction.name) : [];
    const attachmentRows = sensitive ? attachments.slice(0, 257).map(attachment => ({
      id: clean(attachment.getAttribute("data-attachment-id") || attachment.getAttribute("data-file-id"), 1025),
      name: clean(attachment.getAttribute("data-file-name") || attachment.getAttribute("aria-label") || attachment.textContent, 4097),
      mediaType: clean(attachment.getAttribute("data-content-type"), 257),
      size: 0, sizeKnown: false, downloadable: false
    })).filter(attachment => attachment.id && attachment.name) : [];
    const text = clean(body && body.innerText, 1048577);
    const deleted = node.getAttribute("data-deleted") === "true" || !!node.querySelector("[data-tid='deleted-message']");
    rows.push({
      id, chatId, teamId, channelId,
      threadRootId: clean(node.getAttribute("data-parent-message-id") || threadRootId, 1025),
      authorId: clean(author && (author.getAttribute("data-author-id") || author.getAttribute("data-user-id")), 1025),
      authorName: clean(author && author.textContent, 1025),
      createdAt: clean(created && created.dateTime, 64),
      updatedAt: clean(node.getAttribute("data-edited-datetime"), 64),
      snippet: clean(text, 1025),
      content: sensitive ? text : "",
      format: "plain",
      replyCount: Math.max(0, Math.min(1000000, Number(node.getAttribute("data-reply-count")) || 0)),
      hasAttachments: attachments.length > 0,
      deleted,
      links, mentions, reactions, attachments: attachmentRows
    });
    if (rows.length > 256) return {state: "overflow", rows};
  }
  const empty = pane.querySelector("[data-tid='empty-state'], [data-tid='no-messages']");
  return {state: rows.length ? "rows" : empty ? "empty" : "unknown", rows};
}`

const teamsSearchScript = `async query => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const input = document.querySelector(
    "[data-tid='search-input'], input[data-tid='search-box-input'], input[role='combobox'][data-tid*='search']"
  );
  if (!input) return {state: "unknown", rows: []};
  input.focus();
  input.value = query;
  input.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: query}));
  input.dispatchEvent(new KeyboardEvent("keydown", {bubbles: true, key: "Enter", code: "Enter"}));
  input.dispatchEvent(new KeyboardEvent("keyup", {bubbles: true, key: "Enter", code: "Enter"}));
  let pane = null;
  for (let attempt = 0; attempt < 50 && !pane; attempt++) {
    pane = document.querySelector("[data-tid='search-results'], [data-tid='search-messages-results']");
    if (!pane) await sleep(100);
  }
  if (!pane) return {state: "unknown", rows: []};
  const clean = (value, maximum) => String(value || "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ").trim().slice(0, maximum);
  const rows = [];
  for (const node of Array.from(pane.querySelectorAll("[data-message-id], [data-mid], [data-tid='search-result-message']"))) {
    const id = clean(node.getAttribute("data-message-id") || node.getAttribute("data-mid"), 1025);
    const link = node.querySelector("a[href*='/l/message/'], a[href*='/l/chat/'], a[href*='/l/channel/']");
    let chatId = clean(node.getAttribute("data-chat-id"), 1025);
    let teamId = clean(node.getAttribute("data-team-id") || node.getAttribute("data-group-id"), 1025);
    let channelId = clean(node.getAttribute("data-channel-id"), 1025);
    try {
      const target = new URL(link && link.href || "", location.origin);
      if (target.origin !== "https://teams.microsoft.com") continue;
      const parts = target.pathname.split("/").filter(Boolean).map(decodeURIComponent);
      if (parts[0] === "l" && parts[1] === "message") {
        const route = clean(parts[2], 1025);
        if (route.includes("@thread.v2") || route.includes("@unq.gbl.spaces")) chatId = chatId || route;
        else channelId = channelId || route;
        teamId = teamId || clean(target.searchParams.get("groupId"), 1025);
      }
    } catch (_) {
      continue;
    }
    const body = node.querySelector("[data-tid='message-body'], [data-tid='search-result-body']");
    const author = node.querySelector("[data-author-id], [data-user-id], [data-tid='message-author-name']");
    const created = node.querySelector("time[datetime]");
    if (!id || (!chatId && (!teamId || !channelId))) continue;
    rows.push({
      id, chatId, teamId, channelId,
      threadRootId: clean(node.getAttribute("data-parent-message-id"), 1025),
      authorId: clean(author && (author.getAttribute("data-author-id") || author.getAttribute("data-user-id")), 1025),
      authorName: clean(author && author.textContent, 1025),
      createdAt: clean(created && created.dateTime, 64), updatedAt: "",
      snippet: clean(body && body.innerText, 1025), content: "", format: "plain",
      replyCount: 0,
      hasAttachments: !!node.querySelector("[data-attachment-id], [data-file-id]"),
      deleted: false, links: [], mentions: [], reactions: [], attachments: []
    });
    if (rows.length > 256) return {state: "overflow", rows};
  }
  const empty = pane.querySelector("[data-tid='empty-state'], [data-tid='no-results']");
  return {state: rows.length ? "rows" : empty ? "empty" : "unknown", rows};
}`
