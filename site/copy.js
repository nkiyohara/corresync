/* Corresync site enhancement: copy buttons on command blocks.
   Progressive and dependency-free. Without JavaScript (or without a secure
   clipboard), blocks stay plain, selectable text and nothing changes.
   No inline handlers, no network requests, no third-party code. */
(function () {
  "use strict";

  if (
    !document.body ||
    typeof document.querySelectorAll !== "function" ||
    !("clipboard" in navigator) ||
    typeof navigator.clipboard.writeText !== "function"
  ) {
    return;
  }

  var live = document.createElement("p");
  live.className = "visually-hidden";
  live.setAttribute("aria-live", "polite");
  document.body.appendChild(live);

  var blocks = document.querySelectorAll("[data-copy]");
  Array.prototype.forEach.call(blocks, function (block) {
    var code = block.querySelector("pre > code");
    if (!code) {
      return;
    }

    var label = block.getAttribute("data-copy-label") || "command";
    var button = document.createElement("button");
    var timer = null;
    button.type = "button";
    button.className = "copy-button";
    /* The accessible name stays constant; state changes are announced
       through the live region instead of by renaming the control. */
    button.setAttribute("aria-label", "Copy " + label + " to clipboard");
    button.textContent = "Copy";

    function settle(text, announcement, delay) {
      button.textContent = text;
      live.textContent = announcement;
      if (timer) {
        window.clearTimeout(timer);
      }
      timer = window.setTimeout(function () {
        button.textContent = "Copy";
      }, delay);
    }

    function fail() {
      settle(
        "Copy failed",
        "Automatic copy failed. Select the text and copy it manually.",
        3000
      );
    }

    button.addEventListener("click", function () {
      /* No trailing newline: pasting into a terminal must never
         run the command by itself. */
      var text = code.textContent.trim();
      try {
        navigator.clipboard.writeText(text).then(function () {
          settle("Copied", "Copied " + label + " to clipboard.", 2000);
        }, fail);
      } catch (error) {
        fail();
      }
    });

    block.classList.add("has-copy");
    block.appendChild(button);
  });
})();
