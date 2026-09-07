package browser

import (
	"context"
	"os"
	"testing"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

func TestMinimizeKeepsBrowserTargetAlive(t *testing.T) {
	// Opt-in desktop observation uses only a synthetic data page and a fresh
	// profile. Default tests remain headless and never contact a provider.
	headless := os.Getenv("CORRESYNC_TEST_VISIBLE_WINDOW") != "1"
	ctx := browserFixtureContext(t, headless)
	if err := chromedp.Run(ctx, chromedp.Navigate("data:text/html,<title>Corresync synthetic window test</title>")); err != nil {
		t.Fatal(err)
	}
	instance := &Browser{context: ctx}
	if err := instance.Minimize(t.Context()); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := chromedp.Run(ctx, chromedp.Title(&title), chromedp.ActionFunc(func(ctx context.Context) error {
		state := chromedp.FromContext(ctx)
		_, bounds, err := cdpbrowser.GetWindowForTarget().WithTargetID(state.Target.TargetID).Do(cdp.WithExecutor(ctx, state.Browser))
		if err == nil && bounds.WindowState != cdpbrowser.WindowStateMinimized {
			t.Errorf("window state = %s", bounds.WindowState)
		}
		return err
	})); err != nil {
		t.Fatal(err)
	}
	if title != "Corresync synthetic window test" {
		t.Fatalf("target did not survive minimization: %q", title)
	}
}
