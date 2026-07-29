package browser

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// GoogleMailRow is a bounded semantic projection from the visible Gmail page.
// The browser retains all cookies and authorization material.
type GoogleMailRow struct {
	ID             string `json:"id"`
	Href           string `json:"href"`
	Text           string `json:"text"`
	Subject        string `json:"subject"`
	FromName       string `json:"fromName"`
	FromAddress    string `json:"fromAddress"`
	Unread         bool   `json:"unread"`
	HasAttachments bool   `json:"hasAttachments"`
}

// GoogleMailSnapshot distinguishes a recognized empty Gmail view from an
// unknown DOM shape. An empty row slice alone is never sufficient evidence.
type GoogleMailSnapshot struct {
	State string          `json:"state"`
	Rows  []GoogleMailRow `json:"rows"`
}

// GoogleCalendarRow is a bounded semantic projection from visible Calendar
// event nodes that expose machine-readable timestamps.
type GoogleCalendarRow struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location"`
}

// GoogleCalendarSnapshot distinguishes a recognized empty agenda structure
// from selector drift.
type GoogleCalendarSnapshot struct {
	State string              `json:"state"`
	Rows  []GoogleCalendarRow `json:"rows"`
}

// WaitForGoogleWeb navigates to and confirms every configured Google service.
// It reads only URL and ready-state metadata while the browser owns sign-in.
func (browser *Browser) WaitForGoogleWeb(
	ctx context.Context,
	expectedOrigins []string,
) error {
	if browser == nil {
		return errors.New("browser is required")
	}
	if browser.sessions != nil {
		return errors.New(
			"google Web requires a browser-owned session without authorization observation",
		)
	}
	expected := make([]string, 0, len(expectedOrigins))
	for _, raw := range expectedOrigins {
		parsed, err := url.Parse(raw)
		if err != nil {
			return errors.New("google Web origin is malformed")
		}
		origin := browserRequestOrigin(parsed)
		if _, allowed := browser.allowedOrigins[origin]; !allowed {
			return errors.New("google Web origin is outside the browser boundary")
		}
		expected = append(expected, origin)
	}
	if len(expected) == 0 {
		return errors.New("google Web origin is required")
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	for _, origin := range expected {
		if err := chromedp.Run(
			operationContext,
			chromedp.Navigate(googleWebLandingURL(origin)),
		); err != nil {
			return err
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		for {
			var location, ready string
			var applicationShell bool
			if err := chromedp.Run(
				operationContext,
				chromedp.Location(&location),
				chromedp.Evaluate(`document.readyState`, &ready),
				chromedp.Evaluate(
					`document.querySelector("[role='main']") !== null`,
					&applicationShell,
				),
			); err != nil {
				ticker.Stop()
				return err
			}
			target, _ := url.Parse(location)
			if browserRequestOrigin(target) == origin &&
				googleWebApplicationPath(origin, target.Path) &&
				ready == "complete" &&
				applicationShell {
				ticker.Stop()
				break
			}
			select {
			case <-operationContext.Done():
				ticker.Stop()
				return operationContext.Err()
			case <-ticker.C:
			}
		}
	}
	return nil
}

func googleWebApplicationPath(origin, path string) bool {
	switch origin {
	case "https://mail.google.com":
		return strings.HasPrefix(path, "/mail/u/")
	case "https://calendar.google.com":
		return strings.HasPrefix(path, "/calendar/u/")
	default:
		return false
	}
}

func googleWebLandingURL(origin string) string {
	switch origin {
	case "https://mail.google.com":
		return origin + "/mail/u/0/#inbox"
	case "https://calendar.google.com":
		return origin + "/calendar/u/0/r/agenda"
	default:
		return origin
	}
}

// GoogleIdentity returns only the account email exposed by Google's header
// account control. It never reads page content, cookies, or browser storage.
func (browser *Browser) GoogleIdentity(
	ctx context.Context,
	target string,
) (string, error) {
	if err := browser.validateGoogleTarget(target); err != nil {
		return "", err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	var address string
	if err := chromedp.Run(
		operationContext,
		chromedp.Navigate(target),
		chromedp.WaitReady("[role='main']", chromedp.ByQuery),
		chromedp.Evaluate(googleIdentityScript, &address),
	); err != nil {
		return "", err
	}
	if address == "" || len(address) > 320 ||
		strings.ContainsAny(address, "\r\n\x00") {
		return "", errors.New(
			"google application did not expose a bounded signed-in account identity",
		)
	}
	return address, nil
}

// GoogleMailRows navigates within an approved Gmail origin and extracts only
// visible row metadata. Static JavaScript cannot access cookies or storage.
func (browser *Browser) GoogleMailRows(
	ctx context.Context,
	target string,
) (GoogleMailSnapshot, error) {
	if err := browser.validateGoogleTarget(target); err != nil {
		return GoogleMailSnapshot{}, err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	var snapshot GoogleMailSnapshot
	if err := chromedp.Run(
		operationContext,
		chromedp.Navigate(target),
		chromedp.WaitReady("[role='main']", chromedp.ByQuery),
		chromedp.Evaluate(googleMailRowsScript, &snapshot),
	); err != nil {
		return GoogleMailSnapshot{}, err
	}
	if err := validateGoogleMailSnapshot(snapshot); err != nil {
		return GoogleMailSnapshot{}, err
	}
	return snapshot, nil
}

func validateGoogleMailSnapshot(snapshot GoogleMailSnapshot) error {
	if len(snapshot.Rows) > 500 {
		return errors.New("google Web mail page exceeds the configured limit")
	}
	switch snapshot.State {
	case "rows":
		if len(snapshot.Rows) == 0 {
			return errors.New("google Web mail row marker omitted rows")
		}
	case "empty":
		if len(snapshot.Rows) != 0 {
			return errors.New("google Web mail empty marker included rows")
		}
	default:
		return errors.New(
			"google Web mail DOM exposed neither semantic rows nor a recognized empty state",
		)
	}
	return nil
}

// GoogleMailBody reads bounded visible message text after navigating to one
// previously observed same-origin thread URL.
func (browser *Browser) GoogleMailBody(
	ctx context.Context,
	target string,
) (string, error) {
	if err := browser.validateGoogleTarget(target); err != nil {
		return "", err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	var body string
	if err := chromedp.Run(
		operationContext,
		chromedp.Navigate(target),
		chromedp.WaitReady("[role='main']", chromedp.ByQuery),
		chromedp.Evaluate(googleMailBodyScript, &body),
	); err != nil {
		return "", err
	}
	if len(body) > 1<<20 {
		return "", errors.New("google Web message body exceeds the configured limit")
	}
	return body, nil
}

// GoogleCalendarRows extracts visible event metadata only when the page
// exposes machine-readable start and end timestamps.
func (browser *Browser) GoogleCalendarRows(
	ctx context.Context,
	target string,
) (GoogleCalendarSnapshot, error) {
	if err := browser.validateGoogleTarget(target); err != nil {
		return GoogleCalendarSnapshot{}, err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	var snapshot GoogleCalendarSnapshot
	if err := chromedp.Run(
		operationContext,
		chromedp.Navigate(target),
		chromedp.WaitReady("[role='main']", chromedp.ByQuery),
		chromedp.Evaluate(googleCalendarRowsScript, &snapshot),
	); err != nil {
		return GoogleCalendarSnapshot{}, err
	}
	if err := validateGoogleCalendarSnapshot(snapshot); err != nil {
		return GoogleCalendarSnapshot{}, err
	}
	return snapshot, nil
}

func validateGoogleCalendarSnapshot(snapshot GoogleCalendarSnapshot) error {
	if len(snapshot.Rows) > 2500 {
		return errors.New("google Web calendar page exceeds the configured limit")
	}
	switch snapshot.State {
	case "rows":
		if len(snapshot.Rows) == 0 {
			return errors.New("google Web calendar row marker omitted rows")
		}
	case "empty":
		if len(snapshot.Rows) != 0 {
			return errors.New("google Web calendar empty marker included rows")
		}
	default:
		return errors.New(
			"google Web calendar DOM exposed neither semantic rows nor a recognized empty agenda",
		)
	}
	return nil
}

func (browser *Browser) validateGoogleTarget(raw string) error {
	if browser == nil || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("google Web target is malformed")
	}
	target, err := url.Parse(raw)
	if err != nil || target.User != nil {
		return errors.New("google Web target is malformed")
	}
	if _, allowed := browser.allowedOrigins[browserRequestOrigin(target)]; !allowed {
		return errors.New("google Web target escaped the approved origins")
	}
	return nil
}

const googleIdentityScript = `(() => {
  const controls = Array.from(document.querySelectorAll(
    "a[href*='accounts.google.com/SignOutOptions'], a[aria-label^='Google Account'], [data-ogsr-up] a[aria-label]"
  ));
  for (const control of controls) {
    const label = String(control.getAttribute("aria-label") || "");
    const match = label.match(/[A-Z0-9.!#$%&'*+/=?^_{|}~-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i);
    if (match) return match[0].slice(0, 321);
  }
  return "";
})()`

const googleMailRowsScript = `(() => {
  const clean = value => String(value || "").replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim();
  const nodes = Array.from(document.querySelectorAll("tr[data-legacy-thread-id], [role='main'] [data-thread-id]"));
  const seen = new Set();
  const result = [];
  for (const node of nodes) {
    const link = node.querySelector("a[href*='#']") || (node.matches("a[href*='#']") ? node : null);
    const href = link && link.href ? link.href : "";
    const id = clean(node.getAttribute("data-legacy-thread-id") || node.getAttribute("data-thread-id") || href.split("/").pop());
    if (!id || seen.has(id) || !href) continue;
    seen.add(id);
    const sender = node.querySelector("[email]");
    const text = clean(node.innerText).slice(0, 2048);
    const subject = clean(
      (node.querySelector(".bog") || node.querySelector(".y6 span[id]") || {}).textContent
    ).slice(0, 512);
    result.push({
      id,
      href,
      text,
      subject,
      fromName: clean(sender && (sender.getAttribute("name") || sender.textContent)).slice(0, 320),
      fromAddress: clean(sender && sender.getAttribute("email")).slice(0, 320),
      unread: node.classList.contains("zE") || node.getAttribute("aria-readonly") === "false",
      hasAttachments: !!node.querySelector("[data-tooltip*='ttach'], [title*='ttach']")
    });
  }
  if (result.length) return {state: "rows", rows: result};
  if (nodes.length) return {state: "invalid", rows: []};
  const main = document.querySelector("[role='main']");
  const empty = main && main.querySelector(".TC, [data-empty-state='true']");
  return {state: empty ? "empty" : "unknown", rows: []};
})()`

const googleMailBodyScript = `(() => {
  const nodes = Array.from(document.querySelectorAll("[data-message-id], [role='listitem']"));
  const values = nodes.map(node => String(node.innerText || "").trim()).filter(Boolean);
  const text = (values.length ? values : [String((document.querySelector("[role='main']") || document.body).innerText || "")]).join("\n\n");
  return text.slice(0, 1048577);
})()`

const googleCalendarRowsScript = `(() => {
  const clean = value => String(value || "").replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim();
  const instant = value => {
    if (!value) return "";
    if (/^\d{10,13}$/.test(value)) {
      const number = Number(value);
      return new Date(value.length === 10 ? number * 1000 : number).toISOString();
    }
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? "" : date.toISOString();
  };
  const nodes = Array.from(document.querySelectorAll("[data-eventid], [data-event-id]"));
  const seen = new Set();
  const result = [];
  for (const node of nodes) {
    const id = clean(node.getAttribute("data-eventid") || node.getAttribute("data-event-id"));
    if (!id || seen.has(id)) continue;
    const times = node.querySelectorAll("time[datetime]");
    const start = instant(node.getAttribute("data-start-time") || (times[0] && times[0].dateTime));
    const end = instant(node.getAttribute("data-end-time") || (times[1] && times[1].dateTime));
    if (!start || !end || start >= end) continue;
    seen.add(id);
    result.push({
      id,
      text: clean(node.getAttribute("aria-label") || node.innerText).slice(0, 2048),
      start,
      end,
      location: clean(node.getAttribute("data-location")).slice(0, 512)
    });
  }
  if (result.length) return {state: "rows", rows: result};
  if (nodes.length) return {state: "invalid", rows: []};
  const main = document.querySelector("[role='main']");
  const agenda = main && main.querySelector("[data-datekey], [data-date-key], [data-date]");
  return {state: agenda ? "empty" : "unknown", rows: []};
})()`
