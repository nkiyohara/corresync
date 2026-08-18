package browser

const teamsComposeActionScript = `async (action, targetId, content, mentions, selectedId, actorId) => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 80; attempt++) {
      const value = predicate();
      if (value) return value;
      await sleep(100);
    }
    return null;
  };
  const messageNode = id => {
    const matches = Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
      .filter(node => (node.getAttribute("data-message-id") || node.getAttribute("data-mid")) === id);
    return matches.length === 1 ? matches[0] : null;
  };
  if (action !== "send" && action !== "edit") return {state: "before_write"};
  if (!String(content || "").trim()) return {state: "before_write"};
  let replyScope = null;
  if (action === "send" && targetId) {
    const target = await waitFor(() => messageNode(targetId));
    const reply = target && target.querySelector("[data-tid='reply-button'], [data-tid='message-reply']");
    if (!reply) return {state: "before_write"};
    replyScope = target.closest("[data-thread-id], [data-tid='thread'], [data-tid='channel-post']");
    if (!replyScope) return {state: "before_write"};
    reply.click();
  }
  if (action === "edit") {
    const target = await waitFor(() => messageNode(selectedId));
    if (!target) return {state: "before_write"};
    target.dispatchEvent(new MouseEvent("mouseenter", {bubbles: true}));
    const more = await waitFor(() => target.querySelector(
      "[data-tid='message-actions-more-button'], [data-tid='message-action-more-options']"
    ));
    if (!more) return {state: "before_write"};
    const visibleMenus = new Set(Array.from(document.querySelectorAll(
      "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
    )).filter(node => node.getClientRects().length > 0));
    more.click();
    const menu = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
    )).find(node => !visibleMenus.has(node) && node.getClientRects().length > 0 &&
      node.querySelector("[data-tid='edit-message'], [data-tid='message-action-edit']")));
    const edit = menu && menu.querySelector("[data-tid='edit-message'], [data-tid='message-action-edit']");
    if (!edit) return {state: "before_write"};
    edit.click();
  }
  const composerSelector = "[data-tid='ckeditor'][contenteditable='true'], [contenteditable='true'][data-tid*='compose']";
  let topLevelScope = null;
  const topLevelComposer = () => {
    const scopes = Array.from(document.querySelectorAll(
      "[data-tid='chat-pane'], [data-tid='channel-posts-container'], [data-tid='message-pane']"
    )).filter(node => node.getClientRects().length > 0);
    const candidates = scopes.flatMap(scope => Array.from(scope.querySelectorAll(composerSelector))
      .filter(node => !node.closest("[data-message-id], [data-mid]"))
      .map(node => ({scope, node})));
    if (candidates.length !== 1) return null;
    topLevelScope = candidates[0].scope;
    return candidates[0].node;
  };
  const composer = await waitFor(() => action === "edit"
    ? messageNode(selectedId) && messageNode(selectedId).querySelector(
      composerSelector + ", [data-tid='message-editor'][contenteditable='true']"
    )
    : replyScope ? replyScope.querySelector(composerSelector)
    : topLevelComposer());
  if (!composer) return {state: "before_write"};
  composer.focus();
  document.execCommand("selectAll", false, null);
  document.execCommand("insertText", false, content);
  for (const mention of mentions || []) {
    const label = String(mention.displayName || mention.id || "");
    if (!label) return {state: "before_write"};
    const visiblePickers = new Set(Array.from(document.querySelectorAll(
      "[data-tid='people-picker'], [data-tid='mention-picker'], [role='listbox']"
    )).filter(node => node.getClientRects().length > 0));
    document.execCommand("insertText", false, " @" + label);
    const picker = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='people-picker'], [data-tid='mention-picker'], [role='listbox']"
    )).find(node => !visiblePickers.has(node) && node.getClientRects().length > 0 &&
      Array.from(node.querySelectorAll("[data-tid='people-picker-result'], [data-tid='mention-suggestion']"))
        .some(option => (option.getAttribute("data-object-id") || option.getAttribute("data-user-id")) === mention.id)));
    const option = picker && Array.from(picker.querySelectorAll(
      "[data-tid='people-picker-result'], [data-tid='mention-suggestion']"
    )).find(node => (node.getAttribute("data-object-id") || node.getAttribute("data-user-id")) === mention.id);
    if (!option) return {state: "before_write"};
    option.click();
  }
  const composerContainer = composer.closest(
    "[data-tid='compose-box'], [data-tid='message-composer'], [data-tid='reply-composer']"
  );
  if (!composerContainer) return {state: "before_write"};
  const button = await waitFor(() => action === "edit"
    ? messageNode(selectedId) && messageNode(selectedId).querySelector("[data-tid='save-edited-message'], [data-tid='edit-message-save']")
    : (() => {
      const matches = Array.from(composerContainer.querySelectorAll(
        "[data-tid='send-message'], button[data-tid='sendMessage']"
      )).filter(node => node.closest(
        "[data-tid='compose-box'], [data-tid='message-composer'], [data-tid='reply-composer']"
      ) === composerContainer);
      return matches.length === 1 ? matches[0] : null;
    })());
  if (!button || button.disabled) return {state: "before_write"};
  const writeScope = action === "send" ? replyScope || topLevelScope : null;
  if (action === "send" && !writeScope) return {state: "before_write"};
  const existingNodes = action === "send"
    ? Array.from(writeScope.querySelectorAll("[data-message-id], [data-mid]")) : [];
  const existing = new Set(existingNodes
    .map(node => node.getAttribute("data-message-id") || node.getAttribute("data-mid")).filter(Boolean));
  const lastExisting = existingNodes.length ? existingNodes[existingNodes.length - 1] : null;
  button.click();
  if (action === "edit") {
    const expectedText = String(content || "") + (mentions || []).map(mention =>
      " @" + String(mention.displayName || mention.id || "")
    ).join("");
    const normalizedExpectedText = expectedText.replace(/\s+/g, " ").trim();
    const confirmed = await waitFor(() => {
      const node = messageNode(selectedId);
      if (!node) return false;
      const body = node.querySelector("[data-tid='message-body'], [data-tid='chat-pane-message-body'], [data-tid='post-message-content']");
      const editor = node.querySelector("[contenteditable='true'], [data-tid='save-edited-message'], [data-tid='edit-message-save']");
      return body && !editor && String(body.innerText || "").replace(/\s+/g, " ").trim() === normalizedExpectedText;
    });
    return confirmed ? {state: "confirmed", messageId: selectedId} : {state: "unknown"};
  }
  const expectedText = String(content || "") + (mentions || []).map(mention =>
    " @" + String(mention.displayName || mention.id || "")
  ).join("");
  const normalizedExpectedText = expectedText.replace(/\s+/g, " ").trim();
  const created = await waitFor(() => Array.from(writeScope.querySelectorAll("[data-message-id], [data-mid]"))
    .find(node => {
      const id = node.getAttribute("data-message-id") || node.getAttribute("data-mid");
      const header = node.querySelector(
        "[data-tid='message-header'], [data-tid='chat-pane-message-header'], [data-tid='post-message-header']"
      );
      const author = node.matches("[data-author-id], [data-user-id]") ? node
        : header && header.querySelector("[data-author-id], [data-user-id], [data-tid='message-author-name']");
      const observedActor = author && (author.getAttribute("data-author-id") || author.getAttribute("data-user-id"));
      const body = node.querySelector("[data-tid='message-body'], [data-tid='chat-pane-message-body'], [data-tid='post-message-content']");
      const observedText = String(body && body.innerText || "").replace(/\s+/g, " ").trim();
      const followsSnapshot = !lastExisting ||
        !!(lastExisting.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING);
      return id && !existing.has(id) && followsSnapshot && observedActor === actorId &&
        observedText === normalizedExpectedText;
    }));
  if (!created) return {state: "unknown"};
  const createdId = created.getAttribute("data-message-id") || created.getAttribute("data-mid");
  for (let observation = 0; observation < 5; observation++) {
    await sleep(200);
    const stable = messageNode(createdId);
    if (!stable || !writeScope.contains(stable) ||
      (stable.getAttribute("data-message-id") || stable.getAttribute("data-mid")) !== createdId) {
      return {state: "unknown"};
    }
  }
  return {state: "confirmed", messageId: createdId};
}`

const teamsMessageMenuActionScript = `async (action, messageId, reaction, remove) => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 80; attempt++) {
      const value = predicate();
      if (value) return value;
      await sleep(100);
    }
    return null;
  };
  const messageNode = () => {
    const matches = Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
      .filter(node => (node.getAttribute("data-message-id") || node.getAttribute("data-mid")) === messageId);
    return matches.length === 1 ? matches[0] : null;
  };
  const target = await waitFor(messageNode);
  if (!target) return {state: "before_write"};
  if (action !== "reaction" && action !== "delete") return {state: "before_write"};
  target.dispatchEvent(new MouseEvent("mouseenter", {bubbles: true}));
  if (action === "reaction") {
    if (!["like", "heart", "laugh", "surprised", "sad", "angry"].includes(reaction)) {
      return {state: "before_write"};
    }
    let reactionButton = target.querySelector("[data-tid='reaction-button']");
    if (!reactionButton) {
      const more = await waitFor(() => target.querySelector(
        "[data-tid='message-actions-more-button'], [data-tid='message-action-more-options']"
      ));
      if (!more) return {state: "before_write"};
      const visibleMenus = new Set(Array.from(document.querySelectorAll(
        "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
      )).filter(node => node.getClientRects().length > 0));
      more.click();
      const menu = await waitFor(() => Array.from(document.querySelectorAll(
        "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
      )).find(node => !visibleMenus.has(node) && node.getClientRects().length > 0 &&
        node.querySelector("[data-tid='reaction-button']")));
      reactionButton = menu && menu.querySelector("[data-tid='reaction-button']");
    }
    if (!reactionButton) return {state: "before_write"};
    const visiblePickers = new Set(Array.from(document.querySelectorAll(
      "[data-tid='reaction-picker'], [data-tid='reactions-menu'], [role='menu']"
    )).filter(node => node.getClientRects().length > 0));
    reactionButton.click();
    const picker = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='reaction-picker'], [data-tid='reactions-menu'], [role='menu']"
    )).find(node => !visiblePickers.has(node) && node.getClientRects().length > 0 &&
      node.querySelector("[data-tid='reaction-" + reaction + "'], [data-reaction-type='" + reaction + "']")));
    const choice = picker && picker.querySelector(
      "[data-tid='reaction-" + reaction + "'], [data-reaction-type='" + reaction + "']"
    );
    if (!choice) return {state: "before_write"};
    const currentlySelected = choice.getAttribute("aria-pressed") === "true" || choice.getAttribute("data-reacted") === "true";
    if (currentlySelected === remove) choice.click();
    const confirmed = await waitFor(() => {
      const node = messageNode();
      if (!node) return false;
      const pill = Array.from(node.querySelectorAll("[data-reaction-type], [data-tid='reaction-pill']"))
        .find(item => (item.getAttribute("data-reaction-type") || "") === reaction);
      const selected = !!pill && (pill.getAttribute("aria-pressed") === "true" || pill.getAttribute("data-reacted") === "true");
      return remove ? !selected : selected;
    });
    return confirmed ? {state: "confirmed", messageId} : {state: "unknown"};
  }
  const more = await waitFor(() => target.querySelector(
    "[data-tid='message-actions-more-button'], [data-tid='message-action-more-options']"
  ));
  if (!more) return {state: "before_write"};
  const visibleMenus = new Set(Array.from(document.querySelectorAll(
    "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
  )).filter(node => node.getClientRects().length > 0));
  more.click();
  const menu = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='message-actions-menu'], [data-tid='message-action-menu'], [role='menu']"
  )).find(node => !visibleMenus.has(node) && node.getClientRects().length > 0 &&
    node.querySelector("[data-tid='delete-message'], [data-tid='message-action-delete']")));
  const removeButton = menu && menu.querySelector(
    "[data-tid='delete-message'], [data-tid='message-action-delete']"
  );
  if (!removeButton) return {state: "before_write"};
  const visibleDialogs = new Set(Array.from(document.querySelectorAll(
    "[data-tid='delete-message-dialog'], [role='dialog']"
  )).filter(node => node.getClientRects().length > 0));
  removeButton.click();
  const dialog = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='delete-message-dialog'], [role='dialog']"
  )).find(node => !visibleDialogs.has(node) && node.getClientRects().length > 0 &&
    node.querySelector("[data-tid='confirm-delete'], [data-tid='delete-message-confirm']")));
  const confirm = dialog && dialog.querySelector(
    "[data-tid='confirm-delete'], [data-tid='delete-message-confirm']"
  );
  if (!confirm) return {state: "before_write"};
  confirm.click();
  const deleted = await waitFor(() => {
    const node = messageNode();
    return !!node && (node.getAttribute("data-deleted") === "true" ||
      !!node.querySelector(":scope > [data-tid='deleted-message'], :scope > [data-tid='message-body'] > [data-tid='deleted-message']"));
  });
  return deleted ? {state: "confirmed", messageId} : {state: "unknown"};
}`

const teamsCreateChannelScript = `async (teamId, name, topic, visibility) => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 80; attempt++) {
      const value = predicate();
      if (value) return value;
      await sleep(100);
    }
    return null;
  };
  const team = await waitFor(() => Array.from(document.querySelectorAll("[data-team-id], [data-group-id]"))
    .find(node => (node.getAttribute("data-team-id") || node.getAttribute("data-group-id")) === teamId));
  if (!team) return {state: "before_write"};
  const teamScope = team.closest("[data-team-id], [data-group-id]") || team;
  if (!["public", "private"].includes(visibility)) return {state: "before_write"};
  const existing = new Set(Array.from(document.querySelectorAll("[data-channel-id]"))
    .map(node => node.getAttribute("data-channel-id")).filter(Boolean));
  team.dispatchEvent(new MouseEvent("mouseenter", {bubbles: true}));
  const menuButton = team.querySelector("[data-tid='team-more-options'], [data-tid='team-menu-button']");
  if (!menuButton) return {state: "before_write"};
  const visibleMenus = new Set(Array.from(document.querySelectorAll(
    "[data-tid='team-actions-menu'], [data-tid='team-menu'], [role='menu']"
  )).filter(node => node.getClientRects().length > 0));
  menuButton.click();
  const menu = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='team-actions-menu'], [data-tid='team-menu'], [role='menu']"
  )).find(node => !visibleMenus.has(node) && node.getClientRects().length > 0 &&
    node.querySelector("[data-tid='add-channel'], [data-tid='create-channel']")));
  const add = menu && menu.querySelector("[data-tid='add-channel'], [data-tid='create-channel']");
  if (!add) return {state: "before_write"};
  const visibleDialogs = new Set(Array.from(document.querySelectorAll(
    "[data-tid='create-channel-dialog'], [role='dialog']"
  )).filter(node => node.getClientRects().length > 0));
  add.click();
  const dialog = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='create-channel-dialog'], [role='dialog']"
  )).find(node => !visibleDialogs.has(node) && node.getClientRects().length > 0 &&
    node.querySelector("input[data-tid='channel-name-input']")));
  const nameInput = dialog && dialog.querySelector("input[data-tid='channel-name-input']");
  const topicInput = dialog && dialog.querySelector("textarea[data-tid='channel-description-input']");
  const visibilityInput = dialog && dialog.querySelector("[data-tid='channel-privacy-select']");
  if (!nameInput || !visibilityInput) return {state: "before_write"};
  const setValue = (node, value) => {
    node.focus();
    node.value = value;
    node.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: value}));
    node.dispatchEvent(new Event("change", {bubbles: true}));
  };
  setValue(nameInput, name);
  if (topicInput) setValue(topicInput, topic);
  const visiblePrivacyMenus = new Set(Array.from(document.querySelectorAll(
    "[data-tid='channel-privacy-menu'], [role='listbox'], [role='menu']"
  )).filter(node => node.getClientRects().length > 0));
  visibilityInput.click();
  const privacyMenu = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='channel-privacy-menu'], [role='listbox'], [role='menu']"
  )).find(node => !visiblePrivacyMenus.has(node) && node.getClientRects().length > 0 &&
    node.querySelector("[data-tid='channel-privacy-" + visibility + "']")));
  const choice = privacyMenu && privacyMenu.querySelector(
    "[data-tid='channel-privacy-" + visibility + "']"
  );
  if (!choice) return {state: "before_write"};
  choice.click();
  const submit = dialog.querySelector("[data-tid='create-channel-submit']");
  if (!submit || submit.disabled) return {state: "before_write"};
  submit.click();
  const created = await waitFor(() => Array.from(document.querySelectorAll("[data-channel-id]"))
    .find(node => !existing.has(node.getAttribute("data-channel-id")) &&
    teamScope.contains(node) &&
      String(node.getAttribute("aria-label") || node.textContent || "").trim() === String(name || "").trim()));
  const channelId = created && created.getAttribute("data-channel-id");
  return channelId ? {state: "confirmed", channelId} : {state: "unknown"};
}`

const teamsMembershipScript = `async (action, memberId, role, chatId, teamId, channelId) => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 80; attempt++) {
      const value = predicate();
      if (value) return value;
      await sleep(100);
    }
    return null;
  };
  if (action !== "add" && action !== "remove") return {state: "before_write"};
  if (!["member", "owner", "guest"].includes(role) ||
    action === "add" && role === "guest") return {state: "before_write"};
  const surface = Array.from(document.querySelectorAll(
    "[data-tid='chat-pane-header'], [data-tid='channel-header']"
  )).find(node => {
    const identity = node.closest("[data-chat-id], [data-channel-id]") ||
      node.querySelector("[data-chat-id], [data-channel-id]") || node;
    const observedChat = identity.getAttribute("data-chat-id") || "";
    const observedTeam = identity.getAttribute("data-team-id") || identity.getAttribute("data-group-id") || "";
    const observedChannel = identity.getAttribute("data-channel-id") || "";
    return chatId ? observedChat === chatId && !observedTeam && !observedChannel
      : !observedChat && observedTeam === teamId && observedChannel === channelId;
  });
  if (!surface) return {state: "before_write"};
  const manage = surface.querySelector("[data-tid='view-members'], [data-tid='manage-channel-members']");
  if (!manage) return {state: "before_write"};
  const visibleRosters = new Set(Array.from(document.querySelectorAll(
    "[data-tid='members-list'], [data-tid='manage-members-dialog']"
  )).filter(node => node.getClientRects().length > 0));
  manage.click();
  let roster = await waitFor(() => Array.from(document.querySelectorAll(
    "[data-tid='members-list'], [data-tid='manage-members-dialog']"
  )).find(node => !visibleRosters.has(node) && node.getClientRects().length > 0));
  if (!roster) return {state: "before_write"};
  const currentRoster = () => {
    if (roster.isConnected && roster.getClientRects().length > 0) return roster;
    roster = Array.from(document.querySelectorAll(
      "[data-tid='members-list'], [data-tid='manage-members-dialog']"
    )).find(node => !visibleRosters.has(node) && node.getClientRects().length > 0) || null;
    return roster;
  };
  const memberNode = () => {
    const activeRoster = currentRoster();
    return activeRoster && Array.from(activeRoster.querySelectorAll(
      "[data-tid='member-row'][data-member-id], [data-tid='member-row'][data-user-id]"
    )).find(node => (node.getAttribute("data-member-id") || node.getAttribute("data-user-id")) === memberId);
  };
  let membershipPicker = null;
  const pickerIsVisible = () => membershipPicker && membershipPicker.isConnected &&
    membershipPicker.getClientRects().length > 0;
  if (action === "add") {
    const add = await waitFor(() => currentRoster() && currentRoster().querySelector("[data-tid='add-member']"));
    if (!add) return {state: "before_write"};
    const visiblePickers = new Set(Array.from(document.querySelectorAll(
      "[data-tid='member-picker'], [data-tid='people-picker-dialog'], [role='dialog']"
    )).filter(node => node.getClientRects().length > 0));
    add.click();
    membershipPicker = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='member-picker'], [data-tid='people-picker-dialog'], [role='dialog']"
    )).find(node => !visiblePickers.has(node) && node.getClientRects().length > 0 &&
      node.querySelector("input[data-tid='member-picker-input']")));
    const input = membershipPicker && membershipPicker.querySelector("input[data-tid='member-picker-input']");
    if (!input) return {state: "before_write"};
    input.focus();
    input.value = memberId;
    input.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: memberId}));
    const option = await waitFor(() => Array.from(membershipPicker.querySelectorAll("[data-tid='member-picker-result']"))
      .find(node => (node.getAttribute("data-object-id") || node.getAttribute("data-user-id")) === memberId));
    if (!option) return {state: "before_write"};
    option.click();
    if (role === "owner") {
      const roleControl = membershipPicker.querySelector("[data-tid='member-role-select']");
      if (!roleControl) return {state: "before_write"};
      roleControl.click();
      const owner = await waitFor(() => membershipPicker.querySelector("[data-tid='member-role-owner']"));
      if (!owner) return {state: "before_write"};
      owner.click();
    }
    const submit = membershipPicker.querySelector("[data-tid='add-member-submit']");
    if (!submit || submit.disabled) return {state: "before_write"};
    submit.click();
  } else {
    const member = await waitFor(memberNode);
    if (!member) return {state: "before_write"};
    const menu = member.querySelector("[data-tid='member-more-options']");
    if (!menu) return {state: "before_write"};
    const visibleMenus = new Set(Array.from(document.querySelectorAll(
      "[data-tid='member-actions-menu'], [role='menu']"
    )).filter(node => node.getClientRects().length > 0));
    menu.click();
    const memberMenu = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='member-actions-menu'], [role='menu']"
    )).find(node => !visibleMenus.has(node) && node.getClientRects().length > 0 &&
      node.querySelector("[data-tid='remove-member']")));
    const remove = memberMenu && memberMenu.querySelector("[data-tid='remove-member']");
    if (!remove) return {state: "before_write"};
    const visibleDialogs = new Set(Array.from(document.querySelectorAll(
      "[data-tid='remove-member-dialog'], [role='dialog']"
    )).filter(node => node.getClientRects().length > 0));
    remove.click();
    const dialog = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='remove-member-dialog'], [role='dialog']"
    )).find(node => !visibleDialogs.has(node) && node.getClientRects().length > 0 &&
      node.querySelector("[data-tid='remove-member-confirm']")));
    const confirm = dialog && dialog.querySelector("[data-tid='remove-member-confirm']");
    if (!confirm) return {state: "before_write"};
    confirm.click();
  }
  let stableObservations = 0;
  const confirmed = await waitFor(() => {
    const activeRoster = currentRoster();
    if (!activeRoster || action === "add" && pickerIsVisible()) {
      stableObservations = 0;
      return false;
    }
    const present = !!memberNode();
    const roleMatches = action !== "add" || role !== "owner" || present &&
      (memberNode().getAttribute("data-role") === "owner" ||
        !!memberNode().querySelector("[data-tid='member-role-owner']"));
    if ((action === "add" ? present : !present) && roleMatches) {
      stableObservations++;
      return stableObservations >= 5;
    }
    stableObservations = 0;
    return false;
  });
  return confirmed ? {state: "confirmed", memberId, action} : {state: "unknown"};
}`
