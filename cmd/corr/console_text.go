package main

import (
	"errors"
	"fmt"
	"io"
	runtimepkg "runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// hintError is a command failure that also tells the user what to do next.
type hintError struct {
	err  error
	hint string
}

func (failure *hintError) Error() string { return failure.err.Error() }

func (failure *hintError) Unwrap() error { return failure.err }

// withHint attaches a next step to err; use it where the failing code knows
// the fix, e.g. withHint(err, "corr auth login --account work").
//
// The hint is shown as a "Try:" line under the message. It can be a command
// or a short sentence. errors.Is and errors.As still see the wrapped error, and
// err.Error() is unchanged. A nil err stays nil and an empty hint adds nothing.
func withHint(err error, hint string) error {
	hint = strings.TrimSpace(hint)
	if err == nil || hint == "" {
		return err
	}
	return &hintError{err: err, hint: hint}
}

// errorHint returns the outermost hint attached with withHint, if any.
func errorHint(err error) (string, bool) {
	var failure *hintError
	if !errors.As(err, &failure) {
		return "", false
	}
	return failure.hint, true
}

// printCommandError reports a failed command on stderr; run() uses it for
// every failure, so commands return errors instead of printing them.
//
// An error without a hint, or any error when showHint is false (--json runs),
// is printed exactly as err.Error() plus a newline. An error with a hint is
// printed in sentence case (Go error strings stay lower case for the linters;
// the first letter is raised here) and followed by a "Try: …" line.
func printCommandError(writer io.Writer, err error, showHint bool) {
	if err == nil {
		return
	}
	hint, hinted := errorHint(err)
	if !showHint || !hinted {
		_, _ = fmt.Fprintln(writer, err)
		return
	}
	_, _ = fmt.Fprintf(writer, "%s\nTry: %s\n", sentenceCase(err.Error()), singleLine(hint))
}

// sentenceCase raises the first letter of message unless its first word is a
// name that is already mixed case, such as "iCloud".
func sentenceCase(message string) string {
	first, size := utf8.DecodeRuneInString(message)
	if !unicode.IsLower(first) {
		return message
	}
	word, _, _ := strings.Cut(message, " ")
	if strings.IndexFunc(word, unicode.IsUpper) >= 0 {
		return message
	}
	return string(unicode.ToUpper(first)) + message[size:]
}

// nextStep renders the "Next: …" line that ends a successful human-readable
// result, e.g. view.nextStep(shellCommand("corr", "auth", "login")).
func (view consoleView) nextStep(command string) string {
	return view.command("Next: " + command)
}

// shellCommand joins words into a command a user can paste into a shell,
// quoting only the words that need it: shellCommand("corr", "account", "show",
// alias). On Windows the quoting is PowerShell's.
func shellCommand(words ...string) string {
	return shellCommandForOS(runtimepkg.GOOS, words...)
}

func shellCommandForOS(goos string, words ...string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		switch {
		case word != "" && strings.IndexFunc(word, needsShellQuote) < 0:
			quoted = append(quoted, word)
		case goos == "windows":
			// PowerShell: single quotes are literal; a quote is doubled.
			quoted = append(quoted, "'"+strings.ReplaceAll(word, "'", "''")+"'")
		default:
			quoted = append(quoted, "'"+strings.ReplaceAll(word, "'", `'"'"'`)+"'")
		}
	}
	return strings.Join(quoted, " ")
}

func needsShellQuote(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z',
		character >= 'A' && character <= 'Z',
		character >= '0' && character <= '9':
		return false
	}
	return !strings.ContainsRune("-_./:=@+,%", character)
}

const (
	fallbackTextWidth = 80
	maximumTextWidth  = 100
	minimumTextWidth  = 24
)

// textWidth returns the column count to wrap human output written to writer:
// the terminal width when known, capped for readability, otherwise 80.
func (app *runtime) textWidth(writer io.Writer) int {
	if app == nil || app.terminalWidth == nil {
		return fallbackTextWidth
	}
	columns, err := app.terminalWidth(writer)
	if err != nil || columns <= 0 {
		return fallbackTextWidth
	}
	return min(max(columns, minimumTextWidth), maximumTextWidth)
}

// wrapText wraps prose to width columns and prefixes each line with indent;
// use it for any sentence that may not fit one terminal line.
//
// It breaks only at spaces, so a path, URL, email address, or command is never
// split; a word longer than the line gets a line of its own. Existing line
// breaks are kept. Widths are measured on the visible text, so styled words
// may be passed in.
func wrapText(text string, width int, indent string) string {
	available := max(width-lipgloss.Width(indent), 1)
	paragraphs := strings.Split(text, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line, lineWidth := words[0], lipgloss.Width(words[0])
		for _, word := range words[1:] {
			wordWidth := lipgloss.Width(word)
			if lineWidth+1+wordWidth > available {
				lines = append(lines, indent+line)
				line, lineWidth = word, wordWidth
				continue
			}
			line += " " + word
			lineWidth += 1 + wordWidth
		}
		lines = append(lines, indent+line)
	}
	return strings.Join(lines, "\n")
}

// jsonOutputRequested reports whether the command line asks for --json. Such
// runs keep their error output unchanged: no "Try:" line is added.
func jsonOutputRequested(arguments []string) bool {
	for _, argument := range arguments {
		switch argument {
		case "--":
			return false
		case "--json", "--json=true":
			return true
		}
	}
	return false
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
