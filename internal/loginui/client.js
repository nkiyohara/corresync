"use strict";
const screen = document.getElementById("screen"), keys = document.getElementById("keys"), status = document.getElementById("status");
const storageKey = "corresync-login-" + location.port;
let token = location.hash.slice(1) || sessionStorage.getItem(storageKey) || "";
if (location.hash) history.replaceState(null, "", location.pathname);
if (/^[a-f0-9]{64}$/.test(token)) sessionStorage.setItem(storageKey, token);
let controller = sessionStorage.getItem(storageKey + "-controller");
if (!controller) { controller = Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2,"0")).join(""); sessionStorage.setItem(storageKey + "-controller",controller); }
let pending = [], busy = false, done = false, failed = false, composing = false, refreshFailures = 0;
const unreachable = "Cannot reach the login view. Check that the SSH tunnel and the corr command are still running, then press Refresh view.";
function clearSession() { sessionStorage.removeItem(storageKey); sessionStorage.removeItem(storageKey + "-controller"); token = ""; keys.value = ""; pending = []; screen.removeAttribute("src"); }
async function send(action) {
 if (done) return;
 if (pending.length >= 32 || action.type === "text" && new TextEncoder().encode(action.text).length > 4096) { pending=[]; failed=true; keys.value=""; status.textContent="Input too large or queued too quickly. Refresh and check the remote screen before continuing."; return; }
 pending.push(action);
 if (busy) return;
 busy = true;
 while (pending.length && !done) {
  const current = pending.shift();
  try {
   const response = await fetch("/action", {method:"POST",headers:{"Content-Type":"application/json","Authorization":"Bearer " + token,"X-Login-Controller":controller},body:JSON.stringify(current),cache:"no-store",signal:AbortSignal.timeout(28000)});
   if (response.status === 401) { done = true; clearSession(); }
   if (!response.ok) { const reason = new Error((await response.text()).trim() || unreachable); reason.fromServer = true; throw reason; }
   const result = await response.json();
   refreshFailures = 0;
   if (result.status === "authenticated" || result.status === "cancelled") {
    done = true; clearSession(); status.textContent = result.status === "authenticated" ? "Sign-in complete. You can close this tab; the remote Corresync keeps the session." : "Login cancelled.";
   } else {
    if (result.frame && result.frame.jpeg) { screen.src = "data:image/jpeg;base64,"+result.frame.jpeg; document.getElementById("origin").textContent = "Remote page: " + result.frame.origin; }
    status.textContent = result.frame && result.frame.jpeg || screen.hasAttribute("src") ? "Complete sign-in in the remote screen." : "Loading the sign-in page…"; failed = false;
   }
  } catch (error) {
   pending = []; keys.value = "";
   const message = error.fromServer ? error.message : unreachable;
   // Reading the screen changes nothing remotely, so a few failed reads are retried by the next poll.
   if (!done && current.type === "refresh" && ++refreshFailures < 5) status.textContent = "Reconnecting to the remote screen…";
   else { failed = true; status.textContent = message; }
  }
 }
 busy = false;
}
function coordinates(event) { const rect=screen.getBoundingClientRect(); return {x:Math.min(1279,Math.max(0,Math.floor((event.clientX-rect.left)*1280/rect.width))),y:Math.min(899,Math.max(0,Math.floor((event.clientY-rect.top)*900/rect.height)))}; }
screen.addEventListener("click",event=>{ if(failed)return; send({type:"click",...coordinates(event)}); keys.focus({preventScroll:true}); });
screen.addEventListener("wheel",event=>{event.preventDefault(); if(!failed)send({type:"scroll",...coordinates(event),delta:Math.round(Math.max(-1000,Math.min(1000,event.deltaY)))});},{passive:false});
keys.addEventListener("compositionstart",()=>{composing=true;});
keys.addEventListener("compositionend",()=>{composing=false; const text=keys.value;keys.value="";if(text&&!failed)send({type:"text",text});});
keys.addEventListener("input",()=>{if(composing)return;const text=keys.value;keys.value="";if(text&&!failed)send({type:"text",text});});
keys.addEventListener("keydown",event=>{
 if(composing||event.isComposing||failed)return;
 if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==="a"){event.preventDefault();send({type:"key",key:"SelectAll"});return;}
 if(["Enter","Tab","Backspace","Delete","Escape","ArrowLeft","ArrowRight","ArrowUp","ArrowDown","Home","End","PageUp","PageDown"].includes(event.key)){event.preventDefault();send({type:"key",key:event.key,shift:event.shiftKey});}
});
document.getElementById("refresh").onclick=()=>send({type:"refresh"});
document.getElementById("cancel").onclick=()=>send({type:"cancel"});
if(!/^[a-f0-9]{64}$/.test(token)){done=true;status.textContent="Open the complete login link printed by Corresync.";}else{send({type:"refresh"});}
setInterval(()=>{if(!busy&&!done&&!failed)send({type:"refresh"});},1000);
