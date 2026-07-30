# Google live observation on macOS arm64

Commit: `b07378d5bdc31b5eee94a4de761f059c8b45c4e4`

Release candidate or local build: `0.8.4-verification`

Observation date: 2026-07-30

OS and architecture: macOS 26.5.2, arm64

Browser/keyring family and version: Codex in-app Browser with bundled Chromium
(browser build not exposed), macOS Keychain on macOS 26.5.2

Provider ID and broad deployment class: `google`, external Testing audience,
consumer test account

Capability or operation: browser-owned Authorization Code with PKCE and
loopback callback; Gmail XOAUTH2 selectable-folder discovery and bounded text
search; Google Calendar selectable-calendar discovery and bounded event list

Content-free result stage: token exchange completed; eight selectable mail
folders returned while a nonselectable hierarchy container was ignored; one
bounded mail-search result returned; two calendars returned including a
subscribed calendar whose provider ID contains `#`; one bounded event-list
result returned

No mail or calendar mutation was exercised. Write capabilities remain
deterministic-only evidence.
