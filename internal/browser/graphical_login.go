package browser

import (
	"context"
	"errors"
	"net/url"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/nkiyohara/corresync/internal/application"
)

func (browser *Browser) GraphicalLogin(ctx context.Context, action application.GraphicalLoginAction) (application.GraphicalLoginFrame, error) {
	if err := action.Validate(); err != nil {
		return application.GraphicalLoginFrame{}, err
	}
	browser.interactionMu.Lock()
	defer browser.interactionMu.Unlock()
	operation, cancel := terminalOperationContext(browser.context, ctx)
	defer cancel()
	var location string
	if err := chromedp.Do(operation, storeAction(chromedp.Location(), &location)); err != nil {
		return application.GraphicalLoginFrame{}, errors.New("login browser is unavailable")
	}
	target, err := url.Parse(location)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return application.GraphicalLoginFrame{}, errors.New("graphical login requires an HTTPS page")
	}
	if _, err := chromedp.Call(operation, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
		Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight, DeviceScaleFactor: 1,
	}); err != nil {
		return application.GraphicalLoginFrame{}, errors.New("cannot size login viewport")
	}
	switch action.Type {
	case "text":
		_, err = chromedp.Call(operation, input.InsertText, input.InsertTextParams{Text: action.Text})
	case "key":
		modifiers := int64(0)
		if action.Shift {
			modifiers = 8
		}
		key := action.Key
		if key == "SelectAll" {
			key = "a"
			modifiers = 2
		}
		_, err = chromedp.Call(operation, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: key, Modifiers: modifiers, WindowsVirtualKeyCode: graphicalKeyCode(action.Key)})
		if err == nil {
			_, err = chromedp.Call(operation, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyUp, Key: key, Modifiers: modifiers, WindowsVirtualKeyCode: graphicalKeyCode(action.Key)})
		}
	case "click":
		parameters := input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMousePressed, X: float64(action.X), Y: float64(action.Y), Button: input.MouseButtonLeft, ClickCount: 1}
		_, err = chromedp.Call(operation, input.DispatchMouseEvent, parameters)
		if err == nil {
			parameters.Type = input.DispatchMouseEventTypeMouseReleased
			_, err = chromedp.Call(operation, input.DispatchMouseEvent, parameters)
		}
	case "scroll":
		_, err = chromedp.Call(operation, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseWheel, X: float64(action.X), Y: float64(action.Y), DeltaY: float64(action.Delta)})
	}
	if err != nil {
		return application.GraphicalLoginFrame{}, errors.New("login input outcome is unknown; refresh the view without repeating the input")
	}
	if err := chromedp.Do(operation, storeAction(chromedp.Location(), &location)); err != nil {
		return application.GraphicalLoginFrame{}, errors.New("login location unavailable")
	}
	target, err = url.Parse(location)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return application.GraphicalLoginFrame{}, errors.New("graphical login requires an HTTPS page")
	}
	quality := int64(70)
	shot, err := chromedp.Call(operation, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: &quality})
	if err != nil || len(shot.Data) == 0 || len(shot.Data) > application.MaxGraphicalLoginFrameBytes {
		return application.GraphicalLoginFrame{}, errors.New("login screenshot is unavailable or exceeds its bound")
	}
	var after string
	if err := chromedp.Do(operation, storeAction(chromedp.Location(), &after)); err != nil || after != location {
		return application.GraphicalLoginFrame{}, errors.New("login page navigated; refresh the view")
	}
	return application.GraphicalLoginFrame{JPEG: shot.Data, Origin: target.Scheme + "://" + target.Host, Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight}, nil
}

func graphicalKeyCode(key string) int64 {
	return map[string]int64{"Enter": 13, "Tab": 9, "Backspace": 8, "Delete": 46, "Escape": 27, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34, "SelectAll": 65}[key]
}
