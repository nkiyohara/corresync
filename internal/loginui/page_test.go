package loginui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
)

func evaluate[T any](ctx context.Context, t *testing.T, expression string) T {
	t.Helper()
	var value T
	if err := chromedp.Do(ctx, chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		result, err := chromedp.Evaluate[T](expression)(ctx, target)
		value = result
		return err
	})); err != nil {
		t.Fatalf("evaluate %s: %v", expression, err)
	}
	return value
}

func waitFor(ctx context.Context, t *testing.T, what, expression string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !evaluate[bool](ctx, t, expression) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; status=%q", what, evaluate[string](ctx, t, `document.getElementById("status").textContent`))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The page script is the only part a person touches, so drive it in a real
// browser: it must show the frame, relay a click, text and Enter exactly
// once each, keep polling through a few unreadable screens, and stop with the
// server's explanation once the remote login is gone.
func TestLoginPageRelaysInputAndReportsAnEndedLogin(t *testing.T) {
	executable, err := browser.ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, image.NewGray(image.Rect(0, 0, application.GraphicalLoginWidth, application.GraphicalLoginHeight)), nil); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var inputs []application.GraphicalLoginAction
	unreadable := false
	server, err := New(t.Context(), 0, func(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if action != nil && action.Type != "refresh" {
			inputs = append(inputs, *action)
		}
		if unreadable && action != nil && action.Type == "refresh" {
			return Result{}, errors.New("synthetic expired session")
		}
		return Result{Status: "pending", Frame: &application.GraphicalLoginFrame{
			JPEG: frame.Bytes(), Origin: "https://login.example",
			Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight,
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run(t.Context()) }()

	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true))
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL())); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, "the first frame", `document.getElementById("screen").naturalWidth === 1280 && document.getElementById("origin").textContent === "Remote page: https://login.example"`)
	if hash := evaluate[string](ctx, t, `location.hash`); hash != "" {
		t.Fatalf("pairing link stayed in the address bar: %q", hash)
	}

	evaluate[bool](ctx, t, `(() => {
		const screen = document.getElementById("screen"), keys = document.getElementById("keys"), box = screen.getBoundingClientRect();
		screen.dispatchEvent(new MouseEvent("click", {clientX: box.left + box.width / 2, clientY: box.top + box.height / 2, bubbles: true}));
		keys.value = "synthetic 日本語"; keys.dispatchEvent(new Event("input"));
		keys.dispatchEvent(new KeyboardEvent("keydown", {key: "Enter"}));
		return true;
	})()`)
	want := []application.GraphicalLoginAction{
		{Type: "click", X: application.GraphicalLoginWidth / 2, Y: application.GraphicalLoginHeight / 2},
		{Type: "text", Text: "synthetic 日本語"},
		{Type: "key", Key: "Enter"},
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		received := append([]application.GraphicalLoginAction(nil), inputs...)
		mu.Unlock()
		if len(received) >= len(want) {
			if len(received) != len(want) {
				t.Fatalf("inputs repeated: %+v", received)
			}
			for index, action := range want {
				got := received[index]
				// The click is scaled from the displayed image, so allow rounding.
				if got.Type != action.Type || got.Text != action.Text || got.Key != action.Key ||
					got.X < action.X-2 || got.X > action.X+2 || got.Y < action.Y-2 || got.Y > action.Y+2 {
					t.Fatalf("input %d = %+v, want %+v", index, got, action)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inputs = %+v", received)
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	unreadable = true
	mu.Unlock()
	waitFor(ctx, t, "the ended-login explanation", `document.getElementById("status").textContent.startsWith("This login has ended.")`)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("server ended without a reason")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server kept running after the remote login ended")
	}
}
