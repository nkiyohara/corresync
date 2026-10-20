package browser

import (
	"context"
	"os"
	"testing"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestMinimizeKeepsBrowserTargetAlive(t *testing.T) {
	// Opt-in desktop observation uses only a synthetic data page and a fresh
	// profile. Default tests remain headless and never contact a provider.
	headless := os.Getenv("CORRESYNC_TEST_VISIBLE_WINDOW") != "1"
	ctx := browserFixtureContext(t, headless)
	if err := chromedp.Do(ctx, chromedp.Navigate("data:text/html,<title>Corresync synthetic window test</title>")); err != nil {
		t.Fatal(err)
	}
	instance := &Browser{context: ctx}
	if err := instance.Minimize(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Inspect lifetime through the browser executor while minimized. Renderer
	// scheduling in a minimized headless window is platform-dependent; restore
	// the same window before asking its page to execute JavaScript again.
	if err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		state := chromedp.FromContext(ctx)
		window, err := chromedp.CallBrowser(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{TargetID: state.Target.TargetID})
		if err != nil {
			return err
		}
		if window.Bounds.WindowState != cdpbrowser.WindowStateMinimized {
			t.Errorf("window state = %s", window.Bounds.WindowState)
		}
		info, err := chromedp.CallBrowser(ctx, target.GetTargetInfo, target.GetTargetInfoParams{TargetID: state.Target.TargetID})
		if err != nil {
			return err
		}
		if info.TargetInfo.TargetID != state.Target.TargetID || !info.TargetInfo.Attached {
			t.Errorf("minimized target identity or attachment changed: %+v", info)
		}
		return setWindowState(ctx, window.WindowID, cdpbrowser.WindowStateNormal)
	})); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := chromedp.Do(ctx, storeAction(chromedp.Title(), &title)); err != nil {
		t.Fatal(err)
	}
	if title != "Corresync synthetic window test" {
		t.Fatalf("target did not survive minimization: %q", title)
	}
}
