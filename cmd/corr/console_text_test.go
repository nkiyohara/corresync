package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestWithHintKeepsTheWrappedError(t *testing.T) {
	t.Parallel()

	base := fmt.Errorf("read profile: %w", fs.ErrNotExist)
	err := withHint(base, "corr auth login --account work")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("errors.Is lost the wrapped error")
	}
	var pathErr *fs.PathError
	if !errors.As(withHint(&fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}, "fix it"), &pathErr) {
		t.Fatal("errors.As lost the wrapped error")
	}
	if err.Error() != base.Error() {
		t.Fatalf("Error() = %q, want %q", err.Error(), base.Error())
	}
	if hint, ok := errorHint(fmt.Errorf("outer: %w", err)); !ok || hint != "corr auth login --account work" {
		t.Fatalf("errorHint() = %q, %v", hint, ok)
	}
	if withHint(nil, "corr setup") != nil {
		t.Fatal("withHint(nil) is not nil")
	}
	if _, hinted := errorHint(withHint(base, "  ")); hinted {
		t.Fatal("withHint with an empty hint attached a hint")
	}
}

func TestPrintCommandError(t *testing.T) {
	t.Parallel()

	plain := errors.New("development builds cannot self-update")
	hinted := withHint(errors.New("account work is not signed in"), "corr auth login --account work")
	for _, test := range []struct {
		name     string
		err      error
		showHint bool
		want     string
	}{
		{"plain error is unchanged", plain, true, "development builds cannot self-update\n"},
		{"hint adds a Try line", hinted, true, "Account work is not signed in\nTry: corr auth login --account work\n"},
		{"json run hides the hint", hinted, false, "account work is not signed in\n"},
		{
			"mixed-case first word is kept",
			withHint(errors.New("iCloud refused the sign-in"), "try again\n  later"),
			true,
			"iCloud refused the sign-in\nTry: try again later\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			printCommandError(&output, test.err, test.showHint)
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestJSONOutputRequested(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		arguments []string
		want      bool
	}{
		{[]string{"update", "--json"}, true},
		{[]string{"doctor", "--json=true"}, true},
		{[]string{"doctor", "--json=false"}, false},
		{[]string{"mail", "list"}, false},
		{[]string{"mcp", "serve", "--", "--json"}, false},
	} {
		if got := jsonOutputRequested(test.arguments); got != test.want {
			t.Errorf("jsonOutputRequested(%q) = %v, want %v", test.arguments, got, test.want)
		}
	}
}

func TestRunPrintsCommandErrorsUnchanged(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	t.Setenv("CORRESYNC_CONFIG", filepath.Join(t.TempDir(), "missing", "config.toml"))

	for _, arguments := range [][]string{{"update"}, {"update", "--json"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), arguments, &stdout, &stderr); code != 1 {
			t.Fatalf("run(%q) code = %d, want 1", arguments, code)
		}
		if !strings.HasPrefix(stderr.String(), "development builds cannot self-update") ||
			strings.Contains(stderr.String(), "Try:") {
			t.Fatalf("run(%q) added a hint to an unhinted error: %q", arguments, stderr.String())
		}
	}
}

func TestCrashBoundaryPrintsANextStep(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())

	var stderr bytes.Buffer
	code := runWithCrashBoundary(
		context.Background(),
		nil,
		io.Discard,
		&stderr,
		buildinfo.Info{},
		func(context.Context, []string, io.Writer, io.Writer) int { panic("synthetic") },
	)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	want := "Corresync stopped unexpectedly\nTry: corr feedback --last-error to see the crash report and choose whether to share it\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestNextStepAndShellCommand(t *testing.T) {
	t.Parallel()

	if got := (consoleView{}).nextStep(shellCommand("corr", "auth", "login", "--account", "work")); got != "Next: corr auth login --account work" {
		t.Fatalf("nextStep() = %q", got)
	}
	for _, test := range []struct {
		words []string
		want  string
	}{
		{[]string{"corr", "account", "show", "ana@example.com"}, "corr account show ana@example.com"},
		{[]string{"corr", "account", "show", "my work"}, "corr account show 'my work'"},
		{[]string{"corr", "account", "show", "it's"}, `corr account show 'it'"'"'s'`},
		{[]string{"corr", "config", "set", "x", ""}, "corr config set x ''"},
		{[]string{"corr", "mail", "list", "--account", "$HOME"}, "corr mail list --account '$HOME'"},
	} {
		if got := shellCommand(test.words...); got != test.want {
			t.Errorf("shellCommand(%q) = %q, want %q", test.words, got, test.want)
		}
	}
}

func TestWrapTextNeverSplitsAWord(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		text   string
		width  int
		indent string
		want   string
	}{
		{
			name:  "path stays whole",
			text:  "Corresync saved your settings in /home/ana/.config/corresync/config.toml and will use them next time.",
			width: 40,
			want: "Corresync saved your settings in\n" +
				"/home/ana/.config/corresync/config.toml\n" +
				"and will use them next time.",
		},
		{
			name:   "indent counts toward the width",
			text:   "Sign in as ana@example.com with corr auth login --account work",
			width:  30,
			indent: "   ",
			want:   "   Sign in as ana@example.com\n   with corr auth login\n   --account work",
		},
		{
			name:  "url longer than the line",
			text:  "Open https://example.com/a/very/long/path/that/does/not/fit now",
			width: 20,
			want:  "Open\nhttps://example.com/a/very/long/path/that/does/not/fit\nnow",
		},
		{
			name:  "line breaks are kept",
			text:  "First line.\n\nSecond line.",
			width: 80,
			want:  "First line.\n\nSecond line.",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := wrapText(test.text, test.width, test.indent); got != test.want {
				t.Fatalf("wrapText() =\n%s\nwant\n%s", got, test.want)
			}
		})
	}
}

func TestWrapTextMeasuresVisibleWidth(t *testing.T) {
	t.Parallel()

	view := consoleView{color: true}
	text := view.command("corr setup") + " adds an account"
	if got := wrapText(text, 26, ""); strings.Contains(got, "\n") {
		t.Fatalf("styled text was wrapped early: %q", got)
	}
}

func TestTextWidth(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		width func(io.Writer) (int, error)
		want  int
	}{
		{"unknown", nil, 80},
		{"not a terminal", func(io.Writer) (int, error) { return 0, errors.New("not a terminal") }, 80},
		{"narrow", func(io.Writer) (int, error) { return 10, nil }, 24},
		{"normal", func(io.Writer) (int, error) { return 72, nil }, 72},
		{"wide", func(io.Writer) (int, error) { return 300, nil }, 100},
	} {
		app := &runtime{terminalWidth: test.width}
		if got := app.textWidth(io.Discard); got != test.want {
			t.Errorf("%s: textWidth() = %d, want %d", test.name, got, test.want)
		}
	}
}

func TestShellCommandQuotesForTheHostShell(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		goos  string
		words []string
		want  string
	}{
		{"linux", []string{"corr", "auth", "login", "--account", "work"}, "corr auth login --account work"},
		{"linux", []string{"corr", "account", "show", "it's mine"}, `corr account show 'it'"'"'s mine'`},
		{"windows", []string{"corr", "auth", "login", "--account", "work"}, "corr auth login --account work"},
		{"windows", []string{"corr", "account", "show", "it's mine"}, "corr account show 'it''s mine'"},
		{"windows", []string{"corr", "mail", "send", "--subject", ""}, "corr mail send --subject ''"},
	} {
		if got := shellCommandForOS(test.goos, test.words...); got != test.want {
			t.Errorf("shellCommandForOS(%s, %q) = %q, want %q", test.goos, test.words, got, test.want)
		}
	}
}
