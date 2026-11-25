package loginui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
)

func evaluate[T any](ctx context.Context, t *testing.T, expression string, options ...chromedp.EvaluateOption) T {
	t.Helper()
	var value T
	if err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		result, err := chromedp.Evaluate[T](expression, options...)(ctx, target)
		value = result
		return err
	})); err != nil {
		t.Fatalf("evaluate %s: %v", expression, err)
	}
	return value
}

const pageStatus = `document.getElementById("state-title").textContent + ": " + document.getElementById("state-text").textContent + " " + document.getElementById("note").textContent + " [" + document.getElementById("timer").textContent + "]"`

func waitFor(ctx context.Context, t *testing.T, what, expression string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !evaluate[bool](ctx, t, expression) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; status=%q", what, evaluate[string](ctx, t, pageStatus))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func syntheticJPEG(t *testing.T, shade uint8) []byte {
	t.Helper()
	picture := image.NewGray(image.Rect(0, 0, application.GraphicalLoginWidth, application.GraphicalLoginHeight))
	for index := range picture.Pix {
		picture.Pix[index] = shade
	}
	picture.SetGray(10, 10, color.Gray{Y: 255 - shade})
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, picture, nil); err != nil {
		t.Fatal(err)
	}
	return frame.Bytes()
}

// fakeRemote stands in for the daemon's browser. It records every input
// except screen reads and pointer moves, which are counted separately.
type fakeRemote struct {
	mu                    sync.Mutex
	frame                 []byte
	navigating            bool
	unreadable            bool
	failMove, failClick   bool
	authenticateOnRefresh bool
	failStart             bool
	inputs                []application.GraphicalLoginAction
	moves                 []time.Time
	moveAttempts          int
	reads                 int
}

func (remote *fakeRemote) exchange(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if action == nil && remote.failStart {
		return Result{}, errors.New("synthetic start failure")
	}
	switch {
	case action == nil:
	case action.Type == "refresh":
		if remote.unreadable {
			return Result{}, errors.New("synthetic expired session")
		}
		if remote.authenticateOnRefresh {
			return Result{Status: "authenticated"}, nil
		}
		remote.reads++
	case action.Type == "move":
		remote.moveAttempts++
		if remote.failMove {
			return Result{}, errors.New("synthetic move failure")
		}
		remote.moves = append(remote.moves, time.Now())
	case action.Type == "click" && remote.failClick:
		remote.inputs = append(remote.inputs, *action)
		return Result{}, errors.New("synthetic unknown input outcome")
	case action.Type == "cancel":
		return Result{Status: "cancelled"}, nil
	default:
		remote.inputs = append(remote.inputs, *action)
	}
	if remote.navigating {
		return Result{Status: "pending"}, nil
	}
	return Result{Status: "pending", Frame: &application.GraphicalLoginFrame{
		JPEG: remote.frame, Origin: "https://login.example",
		Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight,
	}}, nil
}

func (remote *fakeRemote) snapshot() (inputs []application.GraphicalLoginAction, moves []time.Time, reads, moveAttempts int) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return append([]application.GraphicalLoginAction(nil), remote.inputs...), append([]time.Time(nil), remote.moves...), remote.reads, remote.moveAttempts
}

func (remote *fakeRemote) set(change func(*fakeRemote)) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	change(remote)
}

// startPage serves the view for remote with the given lifetime and opens it
// in a fresh headless Chromium.
func startPage(t *testing.T, remote *fakeRemote, lifetime time.Duration) (context.Context, *Server, chan error) {
	t.Helper()
	executable, err := browser.ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true))
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	t.Cleanup(stop)
	ctx, closeBrowser := chromedp.NewContext(allocator)
	t.Cleanup(closeBrowser)
	// Behave like a focused window, as a person's browser is.
	if err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		_, err := chromedp.Call(ctx, emulation.SetFocusEmulationEnabled, emulation.SetFocusEmulationEnabledParams{Enabled: true})
		return err
	})); err != nil {
		t.Fatal(err)
	}
	// The browser is running before the lifetime starts.
	serverContext, cancel := context.WithTimeout(t.Context(), lifetime)
	t.Cleanup(cancel)
	server, err := New(serverContext, 0, remote.exchange)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run(serverContext) }()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL())); err != nil {
		t.Fatal(err)
	}
	return ctx, server, done
}

func setClipboardPermission(ctx context.Context, t *testing.T, server *Server, setting cdpbrowser.PermissionSetting) {
	t.Helper()
	origin := "http://127.0.0.1:" + strconv.Itoa(server.Port())
	for _, name := range []string{"clipboard-read", "clipboard-write"} {
		if _, err := chromedp.CallBrowser(ctx, cdpbrowser.SetPermission, cdpbrowser.SetPermissionParams{
			Permission: &cdpbrowser.PermissionDescriptor{Name: name}, Setting: setting, Origin: origin,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func waitForInputs(t *testing.T, remote *fakeRemote, want ...application.GraphicalLoginAction) {
	t.Helper()
	eventually(t, "the inputs to arrive", func() bool { inputs, _, _, _ := remote.snapshot(); return len(inputs) >= len(want) })
	time.Sleep(300 * time.Millisecond) // Anything sent twice would arrive by now.
	inputs, _, _, _ := remote.snapshot()
	if len(inputs) != len(want) {
		t.Fatalf("inputs = %+v, want %+v", inputs, want)
	}
	for index, action := range want {
		got := inputs[index]
		// A click is scaled from the displayed picture, so allow rounding.
		if got.Type != action.Type || got.Text != action.Text || got.Key != action.Key ||
			got.X < action.X-2 || got.X > action.X+2 || got.Y < action.Y-2 || got.Y > action.Y+2 {
			t.Fatalf("input %d = %+v, want %+v", index, got, action)
		}
	}
	remote.set(func(remote *fakeRemote) { remote.inputs = nil })
}

const clickCenter = `(() => {
	const screen = document.getElementById("screen"), box = screen.getBoundingClientRect();
	screen.dispatchEvent(new MouseEvent("click", {clientX: box.left + box.width / 2, clientY: box.top + box.height / 2, bubbles: true}));
	return true;
})()`

var center = application.GraphicalLoginAction{Type: "click", X: application.GraphicalLoginWidth / 2, Y: application.GraphicalLoginHeight / 2}

// The page script is the only part a person touches, so drive it in a real
// browser: states and remaining time, unchanged and missing frames, input
// relayed exactly once, paste, rate-limited pointer moves, input that may not
// have arrived, and a remote login that ends.
func TestLoginPageRelaysInputAndReportsAnEndedLogin(t *testing.T) {
	remote := &fakeRemote{frame: syntheticJPEG(t, 40)}
	ctx, server, done := startPage(t, remote, 2*time.Minute)

	waitFor(ctx, t, "the first frame", `document.getElementById("screen").naturalWidth === 1280 && document.getElementById("state-title").textContent === "Ready"`)
	if hash := evaluate[string](ctx, t, `location.hash`); hash != "" {
		t.Fatalf("pairing link stayed in the address bar: %q", hash)
	}
	if origin := evaluate[string](ctx, t, `document.getElementById("origin").hidden ? "" : document.getElementById("origin").textContent.replace(/\s+/g, " ").trim()`); origin != "Signing in at login.example. Check this before you type a password." {
		t.Fatalf("origin = %q", origin)
	}
	if role := evaluate[string](ctx, t, `document.getElementById("status").getAttribute("role")`); role != "status" {
		t.Fatalf("status role = %q", role)
	}
	waitFor(ctx, t, "the remaining time", `!document.getElementById("timer").hidden`)
	if timer := evaluate[string](ctx, t, `document.getElementById("timer").textContent`); !regexp.MustCompile(`^Link expires in (1:[45]\d|2:00)$`).MatchString(timer) {
		t.Fatalf("timer = %q", timer)
	}

	// An unchanged screen is not sent again and the picture stays.
	_, _, reads, _ := remote.snapshot()
	source := evaluate[string](ctx, t, `document.getElementById("screen").src`)
	eventually(t, "more screen reads", func() bool { _, _, now, _ := remote.snapshot(); return now >= reads+3 })
	if evaluate[string](ctx, t, `document.getElementById("screen").dataset.frame`) != "1" ||
		evaluate[string](ctx, t, `document.getElementById("screen").src`) != source ||
		!evaluate[bool](ctx, t, `document.getElementById("loading").hidden`) {
		t.Fatal("an unchanged frame replaced the picture or showed loading")
	}
	remote.set(func(remote *fakeRemote) { remote.frame = syntheticJPEG(t, 200) })
	waitFor(ctx, t, "the changed frame", `document.getElementById("screen").dataset.frame === "2"`)
	// While the remote page navigates there is no frame: keep the picture.
	remote.set(func(remote *fakeRemote) { remote.navigating = true })
	waitFor(ctx, t, "the loading badge", `!document.getElementById("loading").hidden && document.getElementById("screen").hasAttribute("src") && document.getElementById("state-title").textContent === "Ready"`)
	remote.set(func(remote *fakeRemote) { remote.navigating = false })
	waitFor(ctx, t, "the loading badge to go", `document.getElementById("loading").hidden`)

	// Click, text and Enter arrive exactly once each; the click focuses typing.
	evaluate[bool](ctx, t, clickCenter)
	evaluate[bool](ctx, t, `(() => {
		const keys = document.getElementById("keys");
		keys.value = "synthetic 日本語"; keys.dispatchEvent(new Event("input"));
		keys.dispatchEvent(new KeyboardEvent("keydown", {key: "Enter"}));
		return true;
	})()`)
	waitForInputs(t, remote, center, application.GraphicalLoginAction{Type: "text", Text: "synthetic 日本語"}, application.GraphicalLoginAction{Type: "key", Key: "Enter"})
	waitFor(ctx, t, "the typing indicator", `document.getElementById("stage").classList.contains("typing") && document.getElementById("state-text").textContent === "Typing goes to the remote page."`)

	// Paste sends the clipboard as one text action, from the button or Ctrl/Cmd+V.
	setClipboardPermission(ctx, t, server, cdpbrowser.PermissionSettingGranted)
	evaluate[bool](ctx, t, `navigator.clipboard.writeText("synthetic paste").then(() => true)`, chromedp.EvalAwaitPromise)
	evaluate[bool](ctx, t, `(document.getElementById("paste").click(), true)`)
	waitForInputs(t, remote, application.GraphicalLoginAction{Type: "text", Text: "synthetic paste"})
	evaluate[bool](ctx, t, `(() => {
		const data = new DataTransfer(); data.setData("text/plain", "synthetic keyboard paste");
		document.getElementById("keys").dispatchEvent(new ClipboardEvent("paste", {clipboardData: data, bubbles: true, cancelable: true}));
		return true;
	})()`)
	waitForInputs(t, remote, application.GraphicalLoginAction{Type: "text", Text: "synthetic keyboard paste"})
	evaluate[bool](ctx, t, `(() => {
		const data = new DataTransfer(); data.setData("text/plain", "x".repeat(4097));
		document.dispatchEvent(new ClipboardEvent("paste", {clipboardData: data, bubbles: true, cancelable: true}));
		return true;
	})()`)
	waitFor(ctx, t, "the too-long paste note", `document.getElementById("note").textContent.includes("longer than 4 KB")`)
	setClipboardPermission(ctx, t, server, cdpbrowser.PermissionSettingDenied)
	evaluate[bool](ctx, t, `(document.getElementById("paste").click(), true)`)
	waitFor(ctx, t, "the clipboard refusal", `document.getElementById("note").textContent.startsWith("Your browser did not allow reading the clipboard.")`)
	if inputs, _, _, _ := remote.snapshot(); len(inputs) != 0 {
		t.Fatalf("a refused or too long paste sent input: %+v", inputs)
	}

	// Pointer moves are rate-limited and are not input that must not repeat.
	evaluate[bool](ctx, t, `(() => {
		const screen = document.getElementById("screen"), box = screen.getBoundingClientRect();
		let step = 0;
		const timer = setInterval(() => {
			screen.dispatchEvent(new PointerEvent("pointermove", {pointerType: "mouse", clientX: box.left + 10 + step * 3, clientY: box.top + 10 + step, bubbles: true}));
			if (++step === 60) clearInterval(timer);
		}, 20);
		return true;
	})()`)
	time.Sleep(2 * time.Second)
	inputs, moves, _, _ := remote.snapshot()
	if len(moves) < 3 || len(inputs) != 0 {
		t.Fatalf("moves=%d inputs=%+v", len(moves), inputs)
	}
	for index := 1; index < len(moves); index++ {
		if gap := moves[index].Sub(moves[index-1]); gap < 150*time.Millisecond {
			t.Fatalf("moves %d and %d only %v apart", index-1, index, gap)
		}
	}
	// A move that fails is dropped without blocking the next input.
	remote.set(func(remote *fakeRemote) { remote.failMove = true })
	_, _, _, attempts := remote.snapshot()
	evaluate[bool](ctx, t, `(() => {
		const screen = document.getElementById("screen"), box = screen.getBoundingClientRect();
		screen.dispatchEvent(new PointerEvent("pointermove", {pointerType: "mouse", clientX: box.left + 400, clientY: box.top + 300, bubbles: true}));
		return true;
	})()`)
	eventually(t, "the failing move", func() bool { _, _, _, now := remote.snapshot(); return now > attempts })
	time.Sleep(200 * time.Millisecond)
	if title := evaluate[string](ctx, t, `document.getElementById("state-title").textContent`); title != "Ready" {
		t.Fatalf("a failed move changed the state to %q", title)
	}
	evaluate[bool](ctx, t, `document.getElementById("screen").dispatchEvent(new PointerEvent("pointerleave")) || true`)
	remote.set(func(remote *fakeRemote) { remote.failMove = false })
	evaluate[bool](ctx, t, clickCenter)
	waitForInputs(t, remote, center)

	// Input whose outcome is unknown stops further input until the person
	// refreshes and looks at the screen.
	remote.set(func(remote *fakeRemote) { remote.failClick = true })
	evaluate[bool](ctx, t, clickCenter)
	waitFor(ctx, t, "the input-not-delivered state", `document.getElementById("state-title").textContent === "Check the screen" && document.getElementById("state-text").textContent.startsWith("The last input may not have arrived.")`)
	waitForInputs(t, remote, center)
	evaluate[bool](ctx, t, `(() => { const keys = document.getElementById("keys"); keys.value = "blocked"; keys.dispatchEvent(new Event("input")); return document.getElementById("paste").disabled; })()`)
	time.Sleep(300 * time.Millisecond)
	if inputs, _, _, _ := remote.snapshot(); len(inputs) != 0 {
		t.Fatalf("input was sent while the screen needed checking: %+v", inputs)
	}
	remote.set(func(remote *fakeRemote) { remote.failClick = false })
	evaluate[bool](ctx, t, `(document.getElementById("refresh").click(), true)`)
	waitFor(ctx, t, "ready after refresh", `document.getElementById("state-title").textContent === "Ready" && !document.getElementById("paste").disabled`)

	// The remote login goes away: the page says so and the server ends.
	remote.set(func(remote *fakeRemote) { remote.unreadable = true })
	waitFor(ctx, t, "the ended-login explanation", `document.getElementById("state-title").textContent === "Ended" && document.getElementById("state-text").textContent.startsWith("This login has ended.")`)
	if evaluate[bool](ctx, t, `document.getElementById("screen").hasAttribute("src") || sessionStorage.length !== 0`) {
		t.Fatal("an ended view kept the picture or the pairing values")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("server ended without a reason")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after the remote login ended")
	}
}

func TestLoginPageShowsHowTheLoginEnded(t *testing.T) {
	for _, test := range []struct {
		name     string
		lifetime time.Duration
		remote   func(*fakeRemote)
		title    string
		text     string
	}{
		{"signed in", time.Minute, func(remote *fakeRemote) { remote.authenticateOnRefresh = true }, "Signed in", "Sign-in is complete."},
		{"could not start", time.Minute, func(remote *fakeRemote) { remote.failStart = true }, "Could not start", "The login could not start."},
		// Long enough for a loaded machine to open the page first.
		{"expired", 8 * time.Second, func(*fakeRemote) {}, "Expired", "This link has expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := &fakeRemote{frame: syntheticJPEG(t, 90)}
			test.remote(remote)
			ctx, _, _ := startPage(t, remote, test.lifetime)
			// The first answer is the start itself; the next read signs in.
			waitFor(ctx, t, test.name, `document.getElementById("state-title").textContent === `+strconv.Quote(test.title)+
				` && document.getElementById("state-text").textContent.startsWith(`+strconv.Quote(test.text)+`)`)
			if !evaluate[bool](ctx, t, `document.getElementById("paste").disabled && document.getElementById("cancel").disabled && document.getElementById("timer").hidden`) {
				t.Fatal("an ended view still offers input")
			}
		})
	}
}
