package browser

import (
	"context"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

// Minimize keeps the browser-owned session and its target alive while taking
// the dedicated sign-in window off the desktop. It never closes other windows.
func (browser *Browser) Minimize(ctx context.Context) error {
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	operationContext, timeout := context.WithTimeout(operationContext, 2*time.Second)
	defer timeout()
	return chromedp.Run(operationContext, chromedp.ActionFunc(func(ctx context.Context) error {
		state := chromedp.FromContext(ctx)
		executor := cdp.WithExecutor(ctx, state.Browser)
		window, bounds, err := cdpbrowser.GetWindowForTarget().WithTargetID(state.Target.TargetID).Do(executor)
		if err != nil {
			return err
		}
		if bounds.WindowState == cdpbrowser.WindowStateFullscreen || bounds.WindowState == cdpbrowser.WindowStateMaximized {
			if err := cdpbrowser.SetWindowBounds(window, &cdpbrowser.Bounds{WindowState: cdpbrowser.WindowStateNormal}).Do(executor); err != nil {
				return err
			}
		}
		return cdpbrowser.SetWindowBounds(window, &cdpbrowser.Bounds{WindowState: cdpbrowser.WindowStateMinimized}).Do(executor)
	}))
}
