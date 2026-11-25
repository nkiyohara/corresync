"use strict";
// The login view. It shows pictures of the remote sign-in page and relays
// clicks, pointer movement, text, a fixed set of keys, and scrolling. It
// stores nothing but the pairing values in this tab's session storage and
// never logs input or pictures.
const $ = id => document.getElementById(id);
const screen = $("screen"), keys = $("keys"), stage = $("stage"), statusBox = $("status");
const storageKey = "corresync-login-" + location.port;
let token = location.hash.slice(1) || sessionStorage.getItem(storageKey) || "";
if (location.hash) history.replaceState(null, "", location.pathname);
if (/^[a-f0-9]{64}$/.test(token)) sessionStorage.setItem(storageKey, token);
let controller = sessionStorage.getItem(storageKey + "-controller");
if (!controller) { controller = Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, "0")).join(""); sessionStorage.setItem(storageKey + "-controller", controller); }

const textLimit = 4096;          // bytes of one text action, as the server allows
const fastPoll = 250, idlePoll = 1000, fastPeriod = 4000;
const moveInterval = 200;        // at most five pointer moves a second
const readsBeforeGivingUp = 5;

let pending = [], busy = false, done = false, failed = false, composing = false, refreshFailures = 0;
let shown = false, frameId = 0, expiresAt = 0, activeUntil = 0;
let pointer = null, lastMoved = null, lastMoveAt = -Infinity, moveTimer = 0;

const states = {
 starting: ["info", "Starting", "Starting the remote browser. This can take a few seconds."],
 loading: ["info", "Loading", "Loading the sign-in page…"],
 ready: ["ok", "Ready", "Click the picture, then type as you would on the page itself."],
 typing: ["ok", "Ready", "Typing goes to the remote page."],
 unknown: ["warn", "Check the screen", "The last input may not have arrived. Press Refresh view and look at the screen before you continue."],
 toolarge: ["warn", "Check the screen", "Input came faster or longer than can be sent, so it was dropped. Press Refresh view and look at the screen before you continue."],
 reconnecting: ["warn", "Reconnecting", "Lost contact with the remote screen. Trying again…"],
 unreachable: ["error", "Disconnected", "Cannot reach the login view. Check that the SSH tunnel and the corr command are still running, then press Refresh view."],
 elsewhere: ["error", "Open in another tab", "Another browser tab is using this link. Continue there, or cancel and run the corr command again."],
 signedin: ["ok", "Signed in", "Sign-in is complete. You can close this tab; the session stays on the server."],
 cancelled: ["info", "Cancelled", "The login was cancelled. Run the corr command again to start over."],
 expired: ["error", "Expired", "This link has expired or is no longer valid. Run the corr command again for a new link."],
 startfailed: ["error", "Could not start", "The login could not start. The terminal running corr shows why."],
 ended: ["error", "Ended", "This login has ended. The terminal running corr shows why."],
 badlink: ["error", "Incomplete link", "Open the complete login link printed by corr."],
};
let state = "";
// The status region is live, so it changes only when the state does.
function show(name) {
 if (name === state) return;
 state = name;
 const [tone, title, text] = states[name];
 statusBox.dataset.tone = tone; statusBox.dataset.state = name;
 $("state-title").textContent = title; $("state-text").textContent = text; $("note").textContent = "";
 $("paste").disabled = !canType(); $("refresh").disabled = done; $("cancel").disabled = done;
}
function note(text) { $("note").textContent = text; }
function canType() { return shown && !done && !failed; }
function readyState() { return !shown ? "loading" : document.activeElement === keys ? "typing" : "ready"; }
function setLoading(on) { $("loading").hidden = !(on && shown); }
function poke() { activeUntil = performance.now() + fastPeriod; }

function clearSession() {
 sessionStorage.removeItem(storageKey); sessionStorage.removeItem(storageKey + "-controller");
 token = ""; keys.value = ""; pending = []; frameId = 0; shown = false;
 screen.removeAttribute("src"); delete screen.dataset.frame;
}
function end(name) {
 done = true; clearSession(); keys.blur();
 stage.hidden = true; $("focus-hint").hidden = true; $("origin").hidden = true; $("timer").hidden = true; $("loading").hidden = true;
 show(name);
}
function showOrigin(origin) {
 $("origin-host").textContent = origin.startsWith("https://") ? origin.slice(8) : origin;
 $("origin").hidden = false;
}
function tickTimer() {
 const timer = $("timer");
 if (!expiresAt || done) { timer.hidden = true; return; }
 const left = Math.max(0, Math.ceil((expiresAt - performance.now()) / 1000));
 timer.hidden = false;
 timer.textContent = "Link expires in " + Math.floor(left / 60) + ":" + String(left % 60).padStart(2, "0");
 timer.classList.toggle("low", left < 60);
 if (left === 0) end("expired");
}

// One input failed, or the view lost contact. Input is never repeated: what
// is still queued is dropped and the person looks at the screen first.
function lost(action, kind) {
 if (action.type === "move") return; // A pointer move that did not arrive is simply dropped.
 pending = []; keys.value = "";
 // Reading the screen changes nothing remotely, so the next poll retries it.
 if (action.type === "refresh" && ++refreshFailures < readsBeforeGivingUp) { show("reconnecting"); return; }
 failed = true;
 show(action.type === "refresh" || kind === "unreachable" ? "unreachable" : "unknown");
}

async function exchange(action) {
 const headers = {"Content-Type": "application/json", "Authorization": "Bearer " + token, "X-Login-Controller": controller};
 if (frameId) headers["X-Login-Frame"] = String(frameId);
 let response;
 try {
  response = await fetch("/action", {method: "POST", headers, body: JSON.stringify(action), cache: "no-store", signal: AbortSignal.timeout(28000)});
 } catch { lost(action, "unreachable"); return; }
 if (!response.ok) {
  const reason = response.headers.get("X-Login-State") || "";
  if (response.status === 401 || reason === "expired") { end("expired"); return; }
  if (reason === "start-failed") { end("startfailed"); return; }
  if (reason === "ended") { end("ended"); return; }
  if (reason === "elsewhere") { pending = []; failed = true; show("elsewhere"); return; }
  lost(action, "rejected");
  return;
 }
 let result;
 try { result = await response.json(); } catch { lost(action, "unknown"); return; }
 refreshFailures = 0;
 if (typeof result.expiresIn === "number" && result.expiresIn > 0) { expiresAt = performance.now() + result.expiresIn * 1000; tickTimer(); }
 if (result.status === "authenticated") { end("signedin"); return; }
 if (result.status === "cancelled") { end("cancelled"); return; }
 if (done) return;
 if (result.frame && result.frame.jpeg) {
  screen.src = "data:image/jpeg;base64," + result.frame.jpeg;
  frameId = result.frameId || 0; screen.dataset.frame = String(frameId);
  showOrigin(result.frame.origin); shown = true; setLoading(false);
 } else if (result.unchanged && shown) {
  setLoading(false); // Same picture as before; keep it.
 } else {
  setLoading(true);  // The remote page is navigating; keep the last picture.
 }
 if (action.type !== "move") failed = false;
 if (!failed) show(readyState());
}

async function send(action) {
 if (done) return;
 if (pending.length >= 32 || action.type === "text" && new TextEncoder().encode(action.text).length > textLimit) { pending = []; failed = true; keys.value = ""; show("toolarge"); return; }
 pending.push(action);
 if (busy) return;
 busy = true;
 while (pending.length && !done) await exchange(pending.shift());
 busy = false;
 scheduleMove();
}
function input(action) { if (!canType()) return; poke(); note(""); send(action); }

function coordinates(event) {
 const rect = screen.getBoundingClientRect();
 return {x: Math.min(1279, Math.max(0, Math.floor((event.clientX - rect.left) * 1280 / rect.width))), y: Math.min(899, Math.max(0, Math.floor((event.clientY - rect.top) * 900 / rect.height)))};
}

// Pointer movement opens hover menus. Only the latest position is kept: a
// move is sent at most five times a second, never queued behind other input,
// and dropped while anything else is being sent.
function scheduleMove() {
 if (moveTimer || !pointer || done) return;
 moveTimer = setTimeout(() => { moveTimer = 0; sendMove(); }, Math.max(0, lastMoveAt + moveInterval - performance.now()));
}
function sendMove() {
 if (!pointer || !canType() || busy || pending.length) return;
 if (lastMoved && lastMoved.x === pointer.x && lastMoved.y === pointer.y) return;
 lastMoveAt = performance.now(); lastMoved = pointer;
 send({type: "move", x: pointer.x, y: pointer.y});
}
screen.addEventListener("pointermove", event => { if (event.pointerType === "touch") return; pointer = coordinates(event); scheduleMove(); });
screen.addEventListener("pointerleave", () => { pointer = null; });

screen.addEventListener("click", event => {
 if (!canType()) return;
 const at = coordinates(event); lastMoved = at;
 input({type: "click", ...at});
 keys.focus({preventScroll: true});
});
screen.addEventListener("wheel", event => { event.preventDefault(); input({type: "scroll", ...coordinates(event), delta: Math.round(Math.max(-1000, Math.min(1000, event.deltaY)))}); }, {passive: false});
function typing(on) {
 stage.classList.toggle("typing", on);
 const hint = $("focus-hint");
 hint.classList.toggle("typing", on);
 hint.textContent = on ? "Typing goes to the remote page." : "Click the picture to type.";
 if (canType()) show(readyState());
}
keys.addEventListener("focus", () => typing(true));
keys.addEventListener("blur", () => typing(false));
keys.addEventListener("compositionstart", () => { composing = true; });
keys.addEventListener("compositionend", () => { composing = false; const text = keys.value; keys.value = ""; if (text) input({type: "text", text}); });
keys.addEventListener("input", () => { if (composing) return; const text = keys.value; keys.value = ""; if (text) input({type: "text", text}); });
keys.addEventListener("keydown", event => {
 if (composing || event.isComposing || !canType()) return;
 if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a") { event.preventDefault(); input({type: "key", key: "SelectAll"}); return; }
 if (["Enter", "Tab", "Backspace", "Delete", "Escape", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"].includes(event.key)) { event.preventDefault(); input({type: "key", key: event.key, shift: event.shiftKey}); }
});

// Paste sends the clipboard text as one text action, and only when the person
// asks for it: the Paste button or Ctrl/Cmd+V.
function pasteText(text) {
 if (!canType()) return;
 if (!text) { note("The clipboard has no text to paste."); return; }
 if (new TextEncoder().encode(text).length > textLimit) { note("The clipboard text is longer than 4 KB, so it was not sent."); return; }
 input({type: "text", text});
 keys.focus({preventScroll: true});
}
$("paste").addEventListener("click", async () => {
 if (!canType()) return;
 let text;
 try { text = await navigator.clipboard.readText(); } catch {
  note("Your browser did not allow reading the clipboard. Click the picture, then paste with Ctrl+V (⌘V on a Mac).");
  return;
 }
 pasteText(text);
});
document.addEventListener("paste", event => { event.preventDefault(); pasteText(event.clipboardData ? event.clipboardData.getData("text/plain") : ""); });

$("refresh").addEventListener("click", () => { note(""); send({type: "refresh"}); });
$("cancel").addEventListener("click", () => send({type: "cancel"}));

// Refresh faster for a few seconds after the person does something, so the
// result shows up quickly, and once a second otherwise.
function poll() {
 if (!busy && !done && !failed && !pending.length) send({type: "refresh"});
 if (!done) setTimeout(poll, performance.now() < activeUntil && !refreshFailures ? fastPoll : idlePoll);
}
if (!/^[a-f0-9]{64}$/.test(token)) { done = true; stage.hidden = true; $("focus-hint").hidden = true; show("badlink"); }
else { show("starting"); send({type: "refresh"}); setTimeout(poll, idlePoll); setInterval(tickTimer, 1000); }
