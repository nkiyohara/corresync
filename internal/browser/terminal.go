package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const (
	maximumTerminalControls = 64
	maximumTerminalText     = 8 << 10
)

var terminalControlIDPattern = regexp.MustCompile(`^control-[1-9][0-9]{0,2}$`)

// TerminalControl is one visible, interactive browser element that can be
// represented safely in a text-only client.
type TerminalControl struct {
	ID        string
	Kind      string
	Name      string
	Sensitive bool
	Disabled  bool
}

// TerminalView is a bounded accessibility-oriented projection of the current
// browser page. It deliberately omits the URL path, query, form values, and DOM.
type TerminalView struct {
	Origin   string
	Title    string
	Text     string
	Controls []TerminalControl
}

// TerminalActionKind is a closed browser interaction supported by the text
// relay. Key actions carry exactly one printable rune or one named control key.
type TerminalActionKind string

const (
	TerminalActivate TerminalActionKind = "activate"
	TerminalFocus    TerminalActionKind = "focus"
	TerminalKey      TerminalActionKind = "key"
)

// TerminalAction targets an element from the most recently rendered view.
type TerminalAction struct {
	Kind      TerminalActionKind
	ElementID string
	Key       string
}

type terminalSnapshot struct {
	Origin   string            `json:"origin"`
	Title    string            `json:"title"`
	Text     string            `json:"text"`
	Controls []terminalControl `json:"controls"`
}

type terminalControl struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Sensitive bool   `json:"sensitive"`
	Disabled  bool   `json:"disabled"`
}

// TerminalSnapshot returns a bounded text projection of the current page and
// labels its visible controls for a subsequent TerminalAct call.
func (browser *Browser) TerminalSnapshot(ctx context.Context) (TerminalView, error) {
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()

	var snapshot terminalSnapshot
	if err := chromedp.Run(operationContext, chromedp.Evaluate(terminalSnapshotScript, &snapshot)); err != nil {
		return TerminalView{}, fmt.Errorf("read terminal login page: %w", err)
	}
	return normalizeTerminalSnapshot(snapshot), nil
}

// TerminalAct relays one bounded user action to a control from the latest text
// projection. It never accepts a complete credential or arbitrary JavaScript.
func (browser *Browser) TerminalAct(ctx context.Context, action TerminalAction) error {
	if err := validateTerminalAction(action); err != nil {
		return err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()

	selector := `[data-corresync-terminal-control="` + action.ElementID + `"]`
	var run chromedp.Action
	switch action.Kind {
	case TerminalActivate:
		run = chromedp.Click(selector, chromedp.ByQuery)
	case TerminalFocus:
		run = chromedp.Focus(selector, chromedp.ByQuery)
	case TerminalKey:
		key := action.Key
		switch key {
		case "Enter":
			key = kb.Enter
		case "Backspace":
			key = kb.Backspace
		case "Tab":
			key = kb.Tab
		}
		run = chromedp.Tasks{
			chromedp.Focus(selector, chromedp.ByQuery),
			chromedp.KeyEvent(key),
		}
	default:
		return errors.New("unsupported terminal browser action")
	}
	if err := chromedp.Run(operationContext, run); err != nil {
		return fmt.Errorf("apply terminal login action: %w", err)
	}
	return nil
}

func terminalOperationContext(browserContext, callerContext context.Context) (context.Context, context.CancelFunc) {
	operationContext, cancel := context.WithCancel(browserContext)
	stop := context.AfterFunc(callerContext, cancel)
	return operationContext, func() {
		stop()
		cancel()
	}
}

func validateTerminalAction(action TerminalAction) error {
	if !terminalControlIDPattern.MatchString(action.ElementID) {
		return errors.New("invalid terminal control ID")
	}
	switch action.Kind {
	case TerminalActivate, TerminalFocus:
		if action.Key != "" {
			return errors.New("terminal activation and focus actions cannot carry a key")
		}
	case TerminalKey:
		if action.Key == "Enter" || action.Key == "Backspace" || action.Key == "Tab" {
			return nil
		}
		if utf8.RuneCountInString(action.Key) != 1 {
			return errors.New("terminal key action must carry exactly one rune")
		}
		key, _ := utf8.DecodeRuneInString(action.Key)
		if unicode.IsControl(key) || unicode.Is(unicode.Cf, key) {
			return errors.New("terminal key action contains an unsupported control character")
		}
	default:
		return errors.New("unsupported terminal browser action")
	}
	return nil
}

func normalizeTerminalSnapshot(snapshot terminalSnapshot) TerminalView {
	view := TerminalView{
		Origin: sanitizeTerminalOrigin(snapshot.Origin),
		Title:  sanitizeTerminalText(snapshot.Title, 160),
		Text:   sanitizeTerminalText(snapshot.Text, maximumTerminalText),
	}
	for _, control := range snapshot.Controls {
		if len(view.Controls) >= maximumTerminalControls ||
			!terminalControlIDPattern.MatchString(control.ID) {
			break
		}
		kind := control.Kind
		if kind != "input" && kind != "activate" {
			continue
		}
		name := sanitizeTerminalText(control.Name, 160)
		if name == "" {
			name = kind
		}
		view.Controls = append(view.Controls, TerminalControl{
			ID: control.ID, Kind: kind, Name: name,
			Sensitive: control.Sensitive, Disabled: control.Disabled,
		})
	}
	return view
}

func sanitizeTerminalOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	return "https://" + strings.ToLower(parsed.Host)
}

func sanitizeTerminalText(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if character == '\n' {
			return character
		}
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return ' '
		}
		return character
	}, value)
	lines := strings.Split(value, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			clean = append(clean, line)
		}
	}
	value = strings.Join(clean, "\n")
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum]) + "…"
	}
	return value
}

const terminalSnapshotScript = `(() => {
  const marker = "data-corresync-terminal-control";
  document.querySelectorAll("[" + marker + "]").forEach((node) => node.removeAttribute(marker));
  const visible = (node) => {
    const style = window.getComputedStyle(node);
    if (style.visibility === "hidden" || style.display === "none" || Number(style.opacity) === 0) return false;
    const rect = node.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  const clean = (value) => String(value || "").replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, " ").trim();
  const label = (node) => {
    const aria = clean(node.getAttribute("aria-label"));
    if (aria) return aria;
    if (node.labels && node.labels.length) {
      const joined = Array.from(node.labels).map((item) => clean(item.innerText || item.textContent)).filter(Boolean).join(" ");
      if (joined) return joined;
    }
    return clean(node.getAttribute("placeholder") || node.innerText || node.textContent || node.getAttribute("title") || node.getAttribute("name") || node.id);
  };
  const selector = [
    "input:not([type=hidden])", "textarea", "select", "button", "a[href]",
    "[role=button]", "[role=link]", "[role=textbox]", "[contenteditable=true]"
  ].join(",");
  const controls = [];
  const seen = new Set();
  for (const node of document.querySelectorAll(selector)) {
    if (controls.length >= 64 || seen.has(node) || !visible(node)) continue;
    seen.add(node);
    const tag = node.tagName.toLowerCase();
    const type = clean(node.getAttribute("type")).toLowerCase();
    const role = clean(node.getAttribute("role")).toLowerCase();
    const input = tag === "input" || tag === "textarea" || tag === "select" || role === "textbox" || node.isContentEditable;
    const id = "control-" + (controls.length + 1);
    node.setAttribute(marker, id);
    controls.push({
      id,
      kind: input ? "input" : "activate",
      name: label(node),
      sensitive: tag === "input" && type === "password",
      disabled: Boolean(node.disabled) || node.getAttribute("aria-disabled") === "true"
    });
  }
  const text = document.body ? document.body.innerText : "";
  return {origin: window.location.origin, title: document.title, text, controls};
})()`
