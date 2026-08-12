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

  var messagesByLanguage = {
    ja: {
      defaultLabel: "コマンド",
      button: "コピー",
      buttonLabel: function (label) { return label + "をクリップボードにコピー"; },
      copied: "コピーしました",
      copiedAnnouncement: function (label) { return label + "をクリップボードにコピーしました。"; },
      failed: "コピーできませんでした",
      failedAnnouncement: "自動コピーに失敗しました。テキストを選択してコピーしてください。",
    },
    "zh-Hans": {
      defaultLabel: "命令",
      button: "复制",
      buttonLabel: function (label) { return "将" + label + "复制到剪贴板"; },
      copied: "已复制",
      copiedAnnouncement: function (label) { return label + "已复制到剪贴板。"; },
      failed: "复制失败",
      failedAnnouncement: "无法自动复制。请选中文本后手动复制。",
    },
    "zh-Hant": {
      defaultLabel: "指令",
      button: "複製",
      buttonLabel: function (label) { return "將" + label + "複製到剪貼簿"; },
      copied: "已複製",
      copiedAnnouncement: function (label) { return label + "已複製到剪貼簿。"; },
      failed: "複製失敗",
      failedAnnouncement: "無法自動複製。請選取文字後手動複製。",
    },
    ko: {
      defaultLabel: "명령",
      button: "복사",
      buttonLabel: function (label) { return "클립보드에 복사: " + label; },
      copied: "복사됨",
      copiedAnnouncement: function (label) { return "클립보드에 복사했습니다: " + label + "."; },
      failed: "복사 실패",
      failedAnnouncement: "자동으로 복사하지 못했습니다. 텍스트를 선택해 직접 복사하세요.",
    },
  };
  var language = document.documentElement.lang;
  var messages = Object.hasOwn(messagesByLanguage, language) ? messagesByLanguage[language] : {
    defaultLabel: "command",
    button: "Copy",
    buttonLabel: function (label) { return "Copy " + label + " to clipboard"; },
    copied: "Copied",
    copiedAnnouncement: function (label) { return "Copied " + label + " to clipboard."; },
    failed: "Copy failed",
    failedAnnouncement: "Automatic copy failed. Select the text and copy it manually.",
  };

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

    var label = block.getAttribute("data-copy-label") || messages.defaultLabel;
    var button = document.createElement("button");
    var timer = null;
    button.type = "button";
    button.className = "copy-button";
    /* The accessible name stays constant; state changes are announced
       through the live region instead of by renaming the control. */
    button.setAttribute("aria-label", messages.buttonLabel(label));
    button.textContent = messages.button;

    function settle(text, announcement, delay) {
      button.textContent = text;
      live.textContent = announcement;
      if (timer) {
        window.clearTimeout(timer);
      }
      timer = window.setTimeout(function () {
        button.textContent = messages.button;
      }, delay);
    }

    function fail() {
      settle(
        messages.failed,
        messages.failedAnnouncement,
        3000
      );
    }

    button.addEventListener("click", function () {
      /* No trailing newline: pasting into a terminal must never
         run the command by itself. */
      var text = code.textContent.trim();
      try {
        navigator.clipboard.writeText(text).then(function () {
          settle(messages.copied, messages.copiedAnnouncement(label), 2000);
        }, fail);
      } catch (error) {
        fail();
      }
    });

    block.classList.add("has-copy");
    block.appendChild(button);
  });
})();
