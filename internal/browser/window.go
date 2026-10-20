package browser

import (
	"context"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

// Minimize keeps the browser-owned session and its target alive while taking
// the dedicated sign-in window off the desktop. It never closes other windows.
func (browser *Browser) Minimize(ctx context.Context) error {
	operationContext, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	operationContext, timeout := context.WithTimeout(operationContext, 2*time.Second)
	defer timeout()
	return chromedp.Do(operationContext, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		state := chromedp.FromContext(ctx)
		window, err := chromedp.CallBrowser(ctx, cdpbrowser.GetWindowForTarget, cdpbrowser.GetWindowForTargetParams{TargetID: state.Target.TargetID})
		if err != nil {
			return err
		}
		if window.Bounds.WindowState == cdpbrowser.WindowStateFullscreen || window.Bounds.WindowState == cdpbrowser.WindowStateMaximized {
			if err := setWindowState(ctx, window.WindowID, cdpbrowser.WindowStateNormal); err != nil {
				return err
			}
		}
		return setWindowState(ctx, window.WindowID, cdpbrowser.WindowStateMinimized)
	}))
}

func setWindowState(ctx context.Context, window cdpbrowser.WindowID, state cdpbrowser.WindowState) error {
	_, err := chromedp.CallBrowser(ctx, cdpbrowser.SetWindowBounds, cdpbrowser.SetWindowBoundsParams{
		WindowID: window, Bounds: &cdpbrowser.Bounds{WindowState: state},
	})
	return err
}
