package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleViewColorsOnlyInteractiveTerminals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		interactive bool
		environment map[string]string
		wantColor   bool
	}{
		{name: "terminal", interactive: true, environment: map[string]string{"TERM": "xterm-256color"}, wantColor: true},
		{name: "pipe", interactive: false, environment: map[string]string{"TERM": "xterm-256color"}},
		{name: "no-color", interactive: true, environment: map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1"}},
		{name: "dumb-terminal", interactive: true, environment: map[string]string{"TERM": "dumb"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			view := newConsoleViewWithEnvironment(
				&output,
				test.interactive,
				func(name string) (string, bool) {
					value, exists := test.environment[name]
					return value, exists
				},
			)
			if view.color != test.wantColor {
				t.Fatalf("view.color = %t, want %t", view.color, test.wantColor)
			}
			_, _ = view.printf("%s %s", view.success(), view.strong("Ready"))
			hasANSI := strings.Contains(output.String(), "\x1b[")
			if !test.wantColor && hasANSI {
				t.Fatalf("uncolored output contains ANSI escapes: %q", output.String())
			}
		})
	}
}
