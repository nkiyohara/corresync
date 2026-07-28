package main

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

const (
	colorAccent  = "#5C7CFA"
	colorSuccess = "#04B575"
	colorWarning = "#E5A000"
	colorFailure = "#E5484D"
	colorMuted   = "#808080"
)

// consoleView styles human-facing terminal output without changing piped or
// machine-readable output.
type consoleView struct {
	writer      io.Writer
	interactive bool
	color       bool
}

func newConsoleView(app *runtime, writer io.Writer, interactive bool) consoleView {
	return newConsoleViewWithEnvironment(writer, interactive, app.lookupEnv)
}

func newConsoleViewWithEnvironment(
	writer io.Writer,
	interactive bool,
	lookupEnv func(string) (string, bool),
) consoleView {
	color := false
	if interactive {
		_, noColor := lookupEnv("NO_COLOR")
		term, _ := lookupEnv("TERM")
		color = !noColor && term != "dumb"
	}
	return consoleView{writer: writer, interactive: interactive, color: color}
}

func (view consoleView) printf(format string, values ...any) (int, error) {
	if view.color {
		return lipgloss.Fprintf(view.writer, format, values...)
	}
	return fmt.Fprintf(view.writer, format, values...)
}

func (view consoleView) render(value, color string, bold bool) string {
	if !view.color {
		return value
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if bold {
		style = style.Bold(true)
	}
	return style.Render(value)
}

func (view consoleView) strong(value string) string {
	if !view.color {
		return value
	}
	return lipgloss.NewStyle().Bold(true).Render(value)
}

func (view consoleView) muted(value string) string {
	return view.render(value, colorMuted, false)
}

func (view consoleView) command(value string) string {
	return view.render(value, colorAccent, true)
}

func (view consoleView) success() string {
	return view.render("✓", colorSuccess, true)
}

func (view consoleView) warning() string {
	return view.render("!", colorWarning, true)
}

func (view consoleView) failure() string {
	return view.render("✗", colorFailure, true)
}

func (view consoleView) info() string {
	return view.render("•", colorAccent, true)
}

func (view consoleView) accent() string {
	return view.render("↑", colorAccent, true)
}

func (view consoleView) status(status string) string {
	switch status {
	case "pass":
		return view.success()
	case "fail":
		return view.failure()
	case "warn":
		return view.warning()
	default:
		return view.muted("–")
	}
}
