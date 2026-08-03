package browser

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestNormalizeTerminalSnapshotBoundsAndSanitizesPage(t *testing.T) {
	t.Parallel()

	controls := []terminalControl{
		{
			ID: "control-1", Kind: "input", Name: "Email\x1b[31m",
			InputType: "email", NativeInput: true,
		},
		{
			ID: "control-2", Kind: "input", Name: "Stay signed in",
			InputType: "checkbox", NativeInput: true, Checkable: true,
		},
		{
			ID: "control-3", Kind: "input", Name: "Continue",
			InputType: "submit", NativeInput: true,
		},
		{
			ID: "control-4", Kind: "activate", Name: "Remember this device",
			Checkable: true, Checked: true,
		},
		{ID: "invalid", Kind: "activate", Name: "Ignored"},
	}
	view := normalizeTerminalSnapshot(terminalSnapshot{
		Origin:   "https://LOGIN.EXAMPLE:443/path?token=secret",
		Title:    " Sign\x00 in ",
		Text:     "Microsoft\n\n  Continue   to Outlook\x1b[2J",
		Controls: controls,
	})
	if view.Origin != "https://login.example:443" {
		t.Fatalf("Origin = %q", view.Origin)
	}
	if strings.ContainsAny(view.Title+view.Text+view.Controls[0].Name, "\x00\x1b") {
		t.Fatalf("view retained terminal controls: %+v", view)
	}
	if view.Text != "Microsoft\nContinue to Outlook [2J" {
		t.Fatalf("Text = %q", view.Text)
	}
	if len(view.Controls) != 4 || view.Controls[0].ID != "control-1" {
		t.Fatalf("Controls = %+v", view.Controls)
	}
	if view.Controls[1].Kind != "activate" || view.Controls[1].Name != "Stay signed in [not checked]" {
		t.Fatalf("checkbox control = %+v", view.Controls[1])
	}
	if view.Controls[2].Kind != "activate" {
		t.Fatalf("submit control = %+v", view.Controls[2])
	}
	if view.Controls[3].Name != "Remember this device [checked]" {
		t.Fatalf("ARIA checkbox control = %+v", view.Controls[3])
	}
}

func TestTerminalSnapshotScriptClassifiesAndTogglesCheckboxes(t *testing.T) {
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium is unavailable: %v", err)
	}
	execOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	execOptions = append(execOptions, chromedp.ExecPath(executable), chromedp.Flag("headless", true))
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(t.Context(), execOptions...)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()

	html := `<html><head><title>Sign in</title></head><body>
		<input aria-label="Code" type="text">
		<label><input type="checkbox">Don't ask again</label>
		<input aria-label="Continue" type="submit">
		<div aria-checked="false" aria-label="Remember this device" role="checkbox" tabindex="0">Remember</div>
	</body></html>`
	dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
	var snapshot terminalSnapshot
	if err := chromedp.Run(
		browserContext,
		chromedp.Navigate(dataURL),
		chromedp.Evaluate(terminalSnapshotScript, &snapshot),
	); err != nil {
		t.Fatal(err)
	}
	view := normalizeTerminalSnapshot(snapshot)
	if len(view.Controls) != 4 {
		t.Fatalf("Controls = %+v", view.Controls)
	}
	if view.Controls[0].Kind != "input" || view.Controls[1].Kind != "activate" ||
		view.Controls[1].Name != "Don't ask again [not checked]" ||
		view.Controls[2].Kind != "activate" ||
		view.Controls[3].Name != "Remember this device [not checked]" {
		t.Fatalf("Controls = %+v", view.Controls)
	}

	if err := chromedp.Run(
		browserContext,
		chromedp.Click(`[data-corresync-terminal-control="control-2"]`, chromedp.ByQuery),
		chromedp.Evaluate(terminalSnapshotScript, &snapshot),
	); err != nil {
		t.Fatal(err)
	}
	view = normalizeTerminalSnapshot(snapshot)
	if view.Controls[1].Name != "Don't ask again [checked]" {
		t.Fatalf("toggled checkbox = %+v", view.Controls[1])
	}
}

func TestValidateTerminalAction(t *testing.T) {
	t.Parallel()

	valid := []TerminalAction{
		{Kind: TerminalActivate, ElementID: "control-1"},
		{Kind: TerminalFocus, ElementID: "control-64"},
		{Kind: TerminalKey, ElementID: "control-2", Key: "a"},
		{Kind: TerminalKey, ElementID: "control-2", Key: "Enter"},
	}
	for _, action := range valid {
		if err := validateTerminalAction(action); err != nil {
			t.Fatalf("validateTerminalAction(%+v) error = %v", action, err)
		}
	}

	invalid := []TerminalAction{
		{},
		{Kind: TerminalActivate, ElementID: "control-0"},
		{Kind: TerminalActivate, ElementID: "control-1", Key: "x"},
		{Kind: TerminalKey, ElementID: "control-1"},
		{Kind: TerminalKey, ElementID: "control-1", Key: "ab"},
		{Kind: TerminalKey, ElementID: "control-1", Key: "\x1b"},
	}
	for _, action := range invalid {
		if err := validateTerminalAction(action); err == nil {
			t.Fatalf("validateTerminalAction(%+v) unexpectedly succeeded", action)
		}
	}
}
