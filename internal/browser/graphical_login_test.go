package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/nkiyohara/corresync/internal/application"
)

func TestGraphicalLoginSyntheticBrowser(t *testing.T) {
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><input autofocus id="field"><script>window.pressed=false;document.querySelector('input').onkeydown=e=>{if(e.key==='Enter')window.pressed=true}</script></body></html>`))
	}))
	defer server.Close()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true), chromedp.Flag("ignore-certificate-errors", true)) // Synthetic TLS fixture only.
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL+"/login?secret=synthetic")); err != nil {
		t.Fatal(err)
	}
	handle := &Browser{context: ctx}
	for _, action := range []application.GraphicalLoginAction{{Type: "refresh"}, {Type: "click", X: 40, Y: 18}, {Type: "text", Text: "synthetic 日本語"}, {Type: "key", Key: "Enter"}} {
		frame, err := handle.GraphicalLogin(context.Background(), action)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Origin != server.URL || len(frame.JPEG) == 0 || frame.Width != 1280 || frame.Height != 900 {
			t.Fatal("invalid bounded frame")
		}
	}
	var value string
	var pressed bool
	if err := chromedp.Do(ctx, evaluateInto(`document.getElementById('field').value`, &value), evaluateInto(`window.pressed`, &pressed)); err != nil {
		t.Fatal(err)
	}
	if value != "synthetic 日本語" || !pressed {
		t.Fatalf("value=%q pressed=%v", value, pressed)
	}
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.GraphicalLogin(context.Background(), application.GraphicalLoginAction{Type: "refresh"}); err == nil {
		t.Fatal("non-HTTPS frame accepted")
	}
}
