package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Submitting a sign-in form navigates while the frame is being captured. The
// input must be delivered once and the call must not fail: it returns either
// the settled page or no frame, and the next refresh shows the new page.
func TestGraphicalLoginSurvivesNavigationAfterInput(t *testing.T) {
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	submissions := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			submissions++
			_, _ = w.Write([]byte(`<html><body><p id="done">second page</p></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><form action="/next"><input autofocus name="field"></form></body></html>`))
	}))
	defer server.Close()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true), chromedp.Flag("ignore-certificate-errors", true)) // Synthetic TLS fixture only.
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL+"/login")); err != nil {
		t.Fatal(err)
	}
	handle := &Browser{context: ctx}
	for _, action := range []application.GraphicalLoginAction{{Type: "refresh"}, {Type: "click", X: 40, Y: 18}, {Type: "text", Text: "synthetic"}, {Type: "key", Key: "Enter"}} {
		frame, err := handle.GraphicalLogin(context.Background(), action)
		if err != nil {
			t.Fatalf("%s: %v", action.Type, err)
		}
		if len(frame.JPEG) != 0 && frame.Origin != server.URL {
			t.Fatalf("%s: frame labelled %q", action.Type, frame.Origin)
		}
	}
	var location string
	for range 50 {
		frame, err := handle.GraphicalLogin(context.Background(), application.GraphicalLoginAction{Type: "refresh"})
		if err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(ctx, storeAction(chromedp.Location(), &location)); err != nil {
			t.Fatal(err)
		}
		if len(frame.JPEG) != 0 && strings.HasPrefix(location, server.URL+"/next") {
			break
		}
	}
	if !strings.HasPrefix(location, server.URL+"/next") || submissions != 1 {
		t.Fatalf("location=%q submissions=%d", location, submissions)
	}
}

// Editing keys must behave as they do on a local keyboard, because sign-in
// forms rely on Enter to submit and on Backspace or select-all to correct input.
func TestGraphicalLoginEditingKeys(t *testing.T) {
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><input autofocus id="field"></body></html>`))
	}))
	defer server.Close()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true), chromedp.Flag("ignore-certificate-errors", true)) // Synthetic TLS fixture only.
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL+"/login")); err != nil {
		t.Fatal(err)
	}
	handle := &Browser{context: ctx}
	for _, step := range []struct {
		action application.GraphicalLoginAction
		want   string
	}{
		{application.GraphicalLoginAction{Type: "click", X: 40, Y: 18}, ""},
		{application.GraphicalLoginAction{Type: "text", Text: "abcd"}, "abcd"},
		{application.GraphicalLoginAction{Type: "key", Key: "Backspace"}, "abc"},
		{application.GraphicalLoginAction{Type: "key", Key: "Home"}, "abc"},
		{application.GraphicalLoginAction{Type: "key", Key: "Delete"}, "bc"},
		{application.GraphicalLoginAction{Type: "key", Key: "End"}, "bc"},
		{application.GraphicalLoginAction{Type: "key", Key: "ArrowLeft"}, "bc"},
		{application.GraphicalLoginAction{Type: "text", Text: "x"}, "bxc"},
		{application.GraphicalLoginAction{Type: "key", Key: "SelectAll"}, "bxc"},
		{application.GraphicalLoginAction{Type: "text", Text: "z"}, "z"},
	} {
		if _, err := handle.GraphicalLogin(context.Background(), step.action); err != nil {
			t.Fatalf("%+v: %v", step.action, err)
		}
		var value string
		if err := chromedp.Do(ctx, evaluateInto(`document.getElementById('field').value`, &value)); err != nil {
			t.Fatal(err)
		}
		if value != step.want {
			t.Fatalf("after %s %q: value = %q, want %q", step.action.Type, step.action.Key+step.action.Text, value, step.want)
		}
	}
}

// Hover menus open on pointer movement. A move must reach the page as a
// mouseover without pressing a button, so it never activates anything.
func TestGraphicalLoginMoveHoversWithoutClicking(t *testing.T) {
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body style="margin:0"><div id="menu" style="width:200px;height:200px"></div><script>
window.hovers=0;window.presses=0;
const menu=document.getElementById('menu');
menu.addEventListener('mouseover',()=>window.hovers++);
document.addEventListener('mousedown',()=>window.presses++);
document.addEventListener('click',()=>window.presses++);
</script></body></html>`))
	}))
	defer server.Close()
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(executable), chromedp.Flag("headless", true), chromedp.Flag("ignore-certificate-errors", true)) // Synthetic TLS fixture only.
	allocator, stop := chromedp.NewExecAllocator(t.Context(), options...)
	defer stop()
	ctx, closeBrowser := chromedp.NewContext(allocator)
	defer closeBrowser()
	if err := chromedp.Do(ctx, chromedp.Navigate(server.URL+"/login")); err != nil {
		t.Fatal(err)
	}
	handle := &Browser{context: ctx}
	for _, action := range []application.GraphicalLoginAction{{Type: "move", X: 600, Y: 600}, {Type: "move", X: 50, Y: 50}, {Type: "move", X: 60, Y: 60}} {
		frame, err := handle.GraphicalLogin(context.Background(), action)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Origin != server.URL || len(frame.JPEG) == 0 {
			t.Fatal("a move must return the bounded frame like other input")
		}
	}
	var hovers, presses int
	if err := chromedp.Do(ctx, evaluateInto(`window.hovers`, &hovers), evaluateInto(`window.presses`, &presses)); err != nil {
		t.Fatal(err)
	}
	if hovers != 1 || presses != 0 {
		t.Fatalf("hovers=%d presses=%d, want one hover and no press", hovers, presses)
	}
	if _, err := handle.GraphicalLogin(context.Background(), application.GraphicalLoginAction{Type: "move", X: 1, Y: 1, Text: "x"}); err == nil {
		t.Fatal("a move carrying text was accepted")
	}
}
