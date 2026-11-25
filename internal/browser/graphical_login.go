package browser

import (
	"context"
	"errors"
	"net/url"
	"runtime"
	"time"

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
	if err := prepareGraphicalPage(operation); err != nil {
		if !errors.Is(err, errGraphicalFrameNotReady) {
			return application.GraphicalLoginFrame{}, err
		}
		// The page is mid-navigation. Nothing has been dispatched yet.
		if action.Type == "refresh" {
			return application.GraphicalLoginFrame{}, nil
		}
		return application.GraphicalLoginFrame{}, errors.New("the login page was still loading, so the input was not sent; try again")
	}
	var err error
	switch action.Type {
	case "text":
		_, err = chromedp.Call(operation, input.InsertText, input.InsertTextParams{Text: action.Text})
	case "key":
		modifiers := int64(0)
		if action.Shift {
			modifiers = 8
		}
		key := action.Key
		// Select-all is Command+A on macOS and Ctrl+A elsewhere. The editing
		// command is sent with the key because macOS Chromium does not derive
		// it from a synthetic key event.
		var commands []string
		if key == "SelectAll" {
			key = "a"
			modifiers = 2
			if runtime.GOOS == "darwin" {
				modifiers = 4
			}
			commands = []string{"selectAll"}
		}
		// Enter needs its character: without it Chromium raises no keypress and
		// a sign-in form is never submitted. Other keys act on key-down alone.
		text := ""
		if action.Key == "Enter" {
			text = "\r"
		}
		_, err = chromedp.Call(operation, input.DispatchKeyEvent, input.DispatchKeyEventParams{Type: input.DispatchKeyEventTypeKeyDown, Key: key, Text: text, Modifiers: modifiers, WindowsVirtualKeyCode: graphicalKeyCode(action.Key), Commands: commands})
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
	return browser.captureGraphicalFrame(operation, action.Type != "refresh")
}

const (
	graphicalFrameAttempts = 4
	graphicalFrameRetry    = 150 * time.Millisecond
	// Chromium never answers a screenshot request whose page is replaced by a
	// navigation, so each attempt is bounded. A normal capture takes well
	// under a second.
	graphicalFrameTimeout = 1500 * time.Millisecond
)

var errGraphicalFrameNotReady = errors.New("login page is still loading")

// prepareGraphicalPage confirms, before any input, that the page is HTTPS and
// sizes its viewport. Both requests are bounded like a screenshot: a page that
// is being replaced may never answer them.
func prepareGraphicalPage(operation context.Context) error {
	attempt, cancel := context.WithTimeout(operation, graphicalFrameTimeout)
	defer cancel()
	var location string
	if err := chromedp.Do(attempt, storeAction(chromedp.Location(), &location)); err != nil {
		if attempt.Err() != nil && operation.Err() == nil {
			return errGraphicalFrameNotReady
		}
		return errors.New("login browser is unavailable")
	}
	target, err := url.Parse(location)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return errors.New("graphical login requires an HTTPS page")
	}
	if _, err := chromedp.Call(attempt, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
		Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight, DeviceScaleFactor: 1,
	}); err != nil {
		if attempt.Err() != nil && operation.Err() == nil {
			return errGraphicalFrameNotReady
		}
		return errors.New("cannot size login viewport")
	}
	return nil
}

// captureGraphicalFrame returns the viewport only when it and its origin
// label describe the same HTTPS page. Sign-in pages redirect on load and
// navigate right after input, so a capture that overlaps a navigation is
// retried briefly. An empty frame with a nil error means "no stable frame
// yet": after input the caller always gets that instead of an error, because
// the input was delivered exactly once and the next refresh shows the page.
func (browser *Browser) captureGraphicalFrame(operation context.Context, afterInput bool) (application.GraphicalLoginFrame, error) {
	var failure error
	for attempt := range graphicalFrameAttempts {
		if attempt > 0 {
			select {
			case <-operation.Done():
				return application.GraphicalLoginFrame{}, operation.Err()
			case <-time.After(graphicalFrameRetry):
			}
		}
		frame, err := captureGraphicalFrameOnce(operation)
		if err == nil {
			return frame, nil
		}
		if operation.Err() != nil {
			return application.GraphicalLoginFrame{}, operation.Err()
		}
		if errors.Is(err, errGraphicalFrameNotReady) {
			failure = nil
		} else {
			failure = err
		}
	}
	if afterInput {
		return application.GraphicalLoginFrame{}, nil
	}
	return application.GraphicalLoginFrame{}, failure
}

func captureGraphicalFrameOnce(operation context.Context) (application.GraphicalLoginFrame, error) {
	attempt, cancel := context.WithTimeout(operation, graphicalFrameTimeout)
	defer cancel()
	// A step cut short by the attempt bound means the page is mid-navigation.
	bounded := func(err error, otherwise string) error {
		if attempt.Err() != nil {
			return errGraphicalFrameNotReady
		}
		if err != nil {
			return errors.New(otherwise)
		}
		return nil
	}
	var location string
	if err := bounded(chromedp.Do(attempt, storeAction(chromedp.Location(), &location)), "login location unavailable"); err != nil {
		return application.GraphicalLoginFrame{}, err
	}
	target, err := url.Parse(location)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return application.GraphicalLoginFrame{}, errors.New("the sign-in page is not an HTTPS page; check this host's network access and start the login again")
	}
	quality := int64(70)
	shot, err := chromedp.Call(attempt, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: &quality})
	if err := bounded(err, "login screenshot is unavailable"); err != nil {
		return application.GraphicalLoginFrame{}, err
	}
	if len(shot.Data) == 0 {
		return application.GraphicalLoginFrame{}, errors.New("login screenshot is unavailable")
	}
	if len(shot.Data) > application.MaxGraphicalLoginFrameBytes {
		return application.GraphicalLoginFrame{}, errors.New("login screenshot exceeds its bound")
	}
	var after string
	if err := bounded(chromedp.Do(attempt, storeAction(chromedp.Location(), &after)), "login location unavailable"); err != nil {
		return application.GraphicalLoginFrame{}, err
	}
	if after != location {
		return application.GraphicalLoginFrame{}, errGraphicalFrameNotReady
	}
	return application.GraphicalLoginFrame{JPEG: shot.Data, Origin: target.Scheme + "://" + target.Host, Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight}, nil
}

func graphicalKeyCode(key string) int64 {
	return map[string]int64{"Enter": 13, "Tab": 9, "Backspace": 8, "Delete": 46, "Escape": 27, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Home": 36, "End": 35, "PageUp": 33, "PageDown": 34, "SelectAll": 65}[key]
}
