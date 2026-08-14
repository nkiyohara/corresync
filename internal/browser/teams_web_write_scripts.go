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
  const messageNode = id => Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
    .find(node => (node.getAttribute("data-message-id") || node.getAttribute("data-mid")) === id);
  const existing = new Set(Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
    .map(node => node.getAttribute("data-message-id") || node.getAttribute("data-mid")).filter(Boolean));
  if (action === "send" && targetId) {
    const target = await waitFor(() => messageNode(targetId));
    const reply = target && target.querySelector("[data-tid='reply-button'], [data-tid='message-reply']");
    if (!reply) return {state: "before_write"};
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
    more.click();
    const edit = await waitFor(() => document.querySelector(
      "[data-tid='edit-message'], [data-tid='message-action-edit']"
    ));
    if (!edit) return {state: "before_write"};
    edit.click();
  }
  const composer = await waitFor(() => document.querySelector(
    "[data-tid='ckeditor'][contenteditable='true'], [data-tid='message-editor'][contenteditable='true'], [contenteditable='true'][data-tid*='compose']"
  ));
  if (!composer) return {state: "before_write"};
  composer.focus();
  document.execCommand("selectAll", false, null);
  document.execCommand("insertText", false, content);
  for (const mention of mentions || []) {
    const label = String(mention.displayName || mention.id || "");
    if (!label) return {state: "before_write"};
    document.execCommand("insertText", false, " @" + label);
    const option = await waitFor(() => Array.from(document.querySelectorAll(
      "[data-tid='people-picker-result'], [data-tid='mention-suggestion']"
    )).find(node => (node.getAttribute("data-object-id") || node.getAttribute("data-user-id")) === mention.id));
    if (!option) return {state: "before_write"};
    option.click();
  }
  const button = await waitFor(() => action === "edit"
    ? document.querySelector("[data-tid='save-edited-message'], [data-tid='edit-message-save']")
    : document.querySelector("[data-tid='send-message'], button[data-tid='sendMessage']"));
  if (!button || button.disabled) return {state: "before_write"};
  button.click();
  if (action === "edit") {
    const confirmed = await waitFor(() => {
      const node = messageNode(selectedId);
      if (!node) return false;
      const body = node.querySelector("[data-tid='message-body'], [data-tid='chat-pane-message-body'], [data-tid='post-message-content']");
      return body && String(body.innerText || "").trim() === String(content || "").trim();
    });
    return confirmed ? {state: "confirmed", messageId: selectedId} : {state: "unknown"};
  }
  const created = await waitFor(() => Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
    .find(node => {
      const id = node.getAttribute("data-message-id") || node.getAttribute("data-mid");
      const author = node.querySelector("[data-author-id], [data-user-id], [data-tid='message-author-name']");
      const observedActor = author && (author.getAttribute("data-author-id") || author.getAttribute("data-user-id"));
      const body = node.querySelector("[data-tid='message-body'], [data-tid='chat-pane-message-body'], [data-tid='post-message-content']");
      const observedText = String(body && body.innerText || "").trim();
      return id && !existing.has(id) && observedActor === actorId && observedText.includes(String(content || "").trim());
    }));
  if (!created) return {state: "unknown"};
  return {state: "confirmed", messageId: created.getAttribute("data-message-id") || created.getAttribute("data-mid")};
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
  const messageNode = () => Array.from(document.querySelectorAll("[data-message-id], [data-mid]"))
    .find(node => (node.getAttribute("data-message-id") || node.getAttribute("data-mid")) === messageId);
  const target = await waitFor(messageNode);
  if (!target) return {state: "before_write"};
  target.dispatchEvent(new MouseEvent("mouseenter", {bubbles: true}));
  if (action === "reaction") {
    let reactionButton = target.querySelector("[data-tid='reaction-button'], [data-tid='message-reactions']");
    if (!reactionButton) {
      const more = await waitFor(() => target.querySelector(
        "[data-tid='message-actions-more-button'], [data-tid='message-action-more-options']"
      ));
      if (!more) return {state: "before_write"};
      more.click();
      reactionButton = await waitFor(() => document.querySelector("[data-tid='reaction-button'], [data-tid='message-reactions']"));
    }
    if (!reactionButton) return {state: "before_write"};
    reactionButton.click();
    const choice = await waitFor(() => document.querySelector(
      "[data-tid='reaction-" + reaction + "'], [data-reaction-type='" + reaction + "']"
    ));
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
  more.click();
  const removeButton = await waitFor(() => document.querySelector(
    "[data-tid='delete-message'], [data-tid='message-action-delete']"
  ));
  if (!removeButton) return {state: "before_write"};
  removeButton.click();
  const confirm = await waitFor(() => document.querySelector(
    "[data-tid='confirm-delete'], [data-tid='delete-message-confirm']"
  ));
  if (confirm) confirm.click();
  const deleted = await waitFor(() => {
    const node = messageNode();
    return !node || node.getAttribute("data-deleted") === "true" || !!node.querySelector("[data-tid='deleted-message']");
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
  team.dispatchEvent(new MouseEvent("mouseenter", {bubbles: true}));
  const menu = team.querySelector("[data-tid='team-more-options'], [data-tid='team-menu-button']");
  if (!menu) return {state: "before_write"};
  menu.click();
  const add = await waitFor(() => document.querySelector("[data-tid='add-channel'], [data-tid='create-channel']"));
  if (!add) return {state: "before_write"};
  add.click();
  const nameInput = await waitFor(() => document.querySelector("input[data-tid='channel-name-input']"));
  const topicInput = document.querySelector("textarea[data-tid='channel-description-input']");
  const visibilityInput = document.querySelector("[data-tid='channel-privacy-select']");
  if (!nameInput || !visibilityInput) return {state: "before_write"};
  const setValue = (node, value) => {
    node.focus();
    node.value = value;
    node.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: value}));
    node.dispatchEvent(new Event("change", {bubbles: true}));
  };
  setValue(nameInput, name);
  if (topicInput) setValue(topicInput, topic);
  visibilityInput.click();
  const choice = await waitFor(() => document.querySelector(
    "[data-tid='channel-privacy-" + visibility + "']"
  ));
  if (!choice) return {state: "before_write"};
  choice.click();
  const submit = document.querySelector("[data-tid='create-channel-submit']");
  if (!submit || submit.disabled) return {state: "before_write"};
  submit.click();
  const created = await waitFor(() => Array.from(document.querySelectorAll("[data-channel-id]"))
    .find(node => String(node.getAttribute("aria-label") || node.textContent || "").trim() === String(name || "").trim()));
  const channelId = created && created.getAttribute("data-channel-id");
  return channelId ? {state: "confirmed", channelId} : {state: "unknown"};
}`

const teamsMembershipScript = `async (action, memberId, role) => {
  const sleep = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 80; attempt++) {
      const value = predicate();
      if (value) return value;
      await sleep(100);
    }
    return null;
  };
  const manage = document.querySelector("[data-tid='view-members'], [data-tid='manage-channel-members']");
  if (!manage) return {state: "before_write"};
  manage.click();
  if (action === "add") {
    const add = await waitFor(() => document.querySelector("[data-tid='add-member']"));
    if (!add) return {state: "before_write"};
    add.click();
    const input = await waitFor(() => document.querySelector("input[data-tid='member-picker-input']"));
    if (!input) return {state: "before_write"};
    input.focus();
    input.value = memberId;
    input.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: memberId}));
    const option = await waitFor(() => Array.from(document.querySelectorAll("[data-tid='member-picker-result']"))
      .find(node => (node.getAttribute("data-object-id") || node.getAttribute("data-user-id")) === memberId));
    if (!option) return {state: "before_write"};
    option.click();
    if (role === "owner") {
      const roleControl = document.querySelector("[data-tid='member-role-select']");
      if (!roleControl) return {state: "before_write"};
      roleControl.click();
      const owner = await waitFor(() => document.querySelector("[data-tid='member-role-owner']"));
      if (!owner) return {state: "before_write"};
      owner.click();
    }
    const submit = document.querySelector("[data-tid='add-member-submit']");
    if (!submit || submit.disabled) return {state: "before_write"};
    submit.click();
  } else {
    const member = await waitFor(() => Array.from(document.querySelectorAll("[data-member-id], [data-user-id]"))
      .find(node => (node.getAttribute("data-member-id") || node.getAttribute("data-user-id")) === memberId));
    if (!member) return {state: "before_write"};
    const menu = member.querySelector("[data-tid='member-more-options']");
    if (!menu) return {state: "before_write"};
    menu.click();
    const remove = await waitFor(() => document.querySelector("[data-tid='remove-member']"));
    if (!remove) return {state: "before_write"};
    remove.click();
    const confirm = await waitFor(() => document.querySelector("[data-tid='remove-member-confirm']"));
    if (confirm) confirm.click();
  }
  const confirmed = await waitFor(() => {
    const present = Array.from(document.querySelectorAll("[data-member-id], [data-user-id]"))
      .some(node => (node.getAttribute("data-member-id") || node.getAttribute("data-user-id")) === memberId);
    return action === "add" ? present : !present;
  });
  return confirmed ? {state: "confirmed"} : {state: "unknown"};
}`
