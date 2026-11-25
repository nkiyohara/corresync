package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	runtimepkg "runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

// helpExample is one copy-pasteable example in a command group's help. The
// command line is kept as words so a test can run it through the parser.
type helpExample struct {
	title string
	words []string // after "corr"
}

func (example helpExample) command() string {
	return shellCommand(append([]string{commandName}, example.words...)...)
}

// helpWithExamples joins a group's explanation with its examples. Kong wraps
// paragraphs and keeps indented lines as they are, so commands never wrap.
func helpWithExamples(text string, examples []helpExample) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(text))
	builder.WriteString("\n\nExamples:")
	for _, example := range examples {
		if example.title == "" {
			// A follow-on command shares the previous example's title.
			builder.WriteString("\n    " + example.command())
			continue
		}
		builder.WriteString("\n\n" + example.title + "\n\n    " + example.command())
	}
	return builder.String()
}

// approvalCommand returns the command line that repeats command with
// --approve added, for the last line of a preview. command is a pointer to the
// Kong command struct that ran; path is its command path without "corr".
//
// It only saves retyping: an approved run builds its preview again from the
// arguments actually typed and prints it before carrying it out.
func (app *runtime) approvalCommand(command any, path ...string) string {
	if command := printableCommand(app.approvalWords(command, path...)...); command != "" {
		return command
	}
	return "run the same command again with --approve added"
}

func (app *runtime) approvalWords(command any, path ...string) []string {
	words := append([]string{commandName}, path...)
	flags, positionals := commandLineWords(command)
	words = append(words, flags...)
	if app != nil && app.configPath != "" {
		environment := ""
		if app.lookupEnv != nil {
			environment, _ = app.lookupEnv("CORRESYNC_CONFIG")
		}
		if environment != app.configPath {
			words = append(words, flagWords("config", app.configPath)...)
		}
	}
	words = append(words, "--approve")
	for _, positional := range positionals {
		if strings.HasPrefix(positional, "-") {
			words = append(words, "--")
			break
		}
	}
	return append(words, positionals...)
}

// printableCommand is shellCommand for a command shown on screen. It returns
// "" when a word holds a control character or invalid UTF-8, which must never
// reach the terminal; callers then leave the suggestion out.
func printableCommand(words ...string) string {
	for _, word := range words {
		if !utf8.ValidString(word) || strings.IndexFunc(word, unicode.IsControl) >= 0 {
			return ""
		}
	}
	return shellCommand(words...)
}

// commandLineWords renders the flags and positional arguments of a Kong
// command struct that differ from their defaults. --approve and --json are
// left out.
func commandLineWords(command any) (flags, positionals []string) {
	value := reflect.Indirect(reflect.ValueOf(command))
	if value.Kind() != reflect.Struct {
		return nil, nil
	}
	for index := range value.NumField() {
		field := value.Type().Field(index)
		if !field.IsExported() || hasTag(field, "cmd") {
			continue
		}
		name := field.Tag.Get("name")
		if name == "" {
			name = kebabCase(field.Name)
		}
		if name == "approve" || name == "json" {
			continue
		}
		rendered, present := renderFlagValue(value.Field(index), field.Tag.Get("default"))
		switch {
		case !present:
		case hasTag(field, "arg"):
			positionals = append(positionals, rendered...)
		case rendered == nil:
			flags = append(flags, "--"+name)
		default:
			for _, item := range rendered {
				flags = append(flags, flagWords(name, item)...)
			}
		}
	}
	return flags, positionals
}

// renderFlagValue returns the values to print for one field, nil for a set
// boolean flag, and present=false when the field holds its default.
func renderFlagValue(field reflect.Value, fallback string) (values []string, present bool) {
	if duration, ok := field.Interface().(time.Duration); ok {
		parsed, err := time.ParseDuration(fallback)
		if err != nil {
			parsed = 0
		}
		return []string{duration.String()}, duration != parsed
	}
	// Command structs use only these kinds; anything else is left out.
	kind := field.Kind()
	if kind == reflect.Bool {
		return nil, field.Bool() && fallback != "true"
	}
	if kind == reflect.String {
		text := field.String()
		return []string{text}, text != "" && text != fallback
	}
	if kind == reflect.Int || kind == reflect.Int64 {
		text := strconv.FormatInt(field.Int(), 10)
		if fallback == "" {
			fallback = "0"
		}
		return []string{text}, text != fallback
	}
	if kind == reflect.Pointer {
		if field.IsNil() {
			return nil, false
		}
		return []string{fmt.Sprint(field.Elem().Interface())}, true
	}
	if kind == reflect.Slice {
		for item := range field.Len() {
			values = append(values, fmt.Sprint(field.Index(item).Interface()))
		}
		return values, len(values) > 0
	}
	return nil, false
}

func flagWords(name, value string) []string {
	if strings.HasPrefix(value, "-") {
		return []string{"--" + name + "=" + value}
	}
	return []string{"--" + name, value}
}

func hasTag(field reflect.StructField, key string) bool {
	_, present := field.Tag.Lookup(key)
	return present
}

// kebabCase matches Kong's default flag name for a field: TimeZone becomes
// time-zone and MessageID becomes message-id.
func kebabCase(name string) string {
	var builder strings.Builder
	runes := []rune(name)
	for index, character := range runes {
		if unicode.IsUpper(character) && index > 0 &&
			(unicode.IsLower(runes[index-1]) ||
				index+1 < len(runes) && unicode.IsLower(runes[index+1])) {
			builder.WriteByte('-')
		}
		builder.WriteRune(unicode.ToLower(character))
	}
	return builder.String()
}

// writePreviewEnd finishes a preview shown on stdout with the command that
// carries it out.
func (app *runtime) writePreviewEnd(command any, path ...string) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.nextStep(app.approvalCommand(command, path...)))
	return err
}

// emptyResult is what a list prints when nothing matched: what was searched,
// and optionally a command to try next.
type emptyResult struct {
	message string
	next    string
}

func (app *runtime) writeEmpty(empty emptyResult) error {
	message := empty.message
	if message == "" {
		message = "Nothing found."
	}
	if _, err := fmt.Fprintln(app.stdout, wrapText(message, app.textWidth(app.stdout), "")); err != nil {
		return err
	}
	if empty.next == "" {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.nextStep(empty.next))
	return err
}

// quoteForSentence quotes a user-supplied value inside a sentence.
func quoteForSentence(value string) string {
	return strconv.Quote(sanitizeCell(value, 120))
}

// reviewField is one labelled line of a preview.
type reviewField struct {
	label string
	value string
}

// writeReviewFields prints labelled lines with the values lined up. An empty
// value prints as "(none)".
func writeReviewFields(writer io.Writer, fields []reviewField) error {
	width := 0
	for _, field := range fields {
		width = max(width, len(field.label))
	}
	for _, field := range fields {
		value := field.value
		if value == "" {
			value = "(none)"
		}
		if _, err := fmt.Fprintf(writer, "  %-*s  %s\n", width+1, field.label+":", value); err != nil {
			return err
		}
	}
	return nil
}

func previewYesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// resolveAccount resolves --account like app.account and, when the alias is
// unknown, says where to find the configured ones.
func resolveAccount(configuration config.Config, reference string) (string, domain.AccountID, error) {
	alias, account, err := configuration.ResolveAccount(reference)
	if err == nil {
		return alias, account.ID, nil
	}
	if len(configuration.Accounts) == 0 {
		return "", "", withHint(err, shellCommand(commandName, "setup"))
	}
	return "", "", withHint(err, shellCommand(commandName, "account", "list"))
}

// explainCommandError rewords a failure the local daemon reports in a form
// meant for programs. Only a sign-in request is recognised: it becomes a
// sentence naming the account and the `corr auth login` command to run. JSON
// runs keep the error unchanged.
func explainCommandError(err error, jsonOutput bool) error {
	if err == nil || jsonOutput {
		return err
	}
	action, ok := signInRequest(err)
	if !ok {
		return err
	}
	return withHint(
		&signInError{err: err, action: action},
		shellCommand(append([]string{action.NextAction.Command.Executable}, action.NextAction.Command.Args...)...),
	)
}

// signInRequest reports whether err is the daemon asking for a sign-in: the
// daemon sends the typed action as its error message.
func signInRequest(err error) (application.AuthenticationActionRequired, bool) {
	var remote *daemonapi.Error
	if !errors.As(err, &remote) {
		return application.AuthenticationActionRequired{}, false
	}
	var action application.AuthenticationActionRequired
	if json.Unmarshal([]byte(remote.Message), &action) != nil || action.Validate() != nil {
		return application.AuthenticationActionRequired{}, false
	}
	return action, true
}

// signInError is a sign-in request from the daemon shown as a sentence.
// errors.Is and errors.As still reach the daemon error.
type signInError struct {
	err    error
	action application.AuthenticationActionRequired
}

func (failure *signInError) Error() string {
	reason := map[application.AuthenticationReason]string{
		application.AuthenticationReasonNeverAuthenticated:  "it has not been signed in yet",
		application.AuthenticationReasonUserSignedOut:       "it was signed out",
		application.AuthenticationReasonInteractionPending:  "a sign-in is still in progress",
		application.AuthenticationReasonSessionExpired:      "the sign-in has expired",
		application.AuthenticationReasonGrantRevoked:        "access was revoked",
		application.AuthenticationReasonCredentialRejected:  "the provider rejected the sign-in",
		application.AuthenticationReasonInteractionRequired: "the provider asks you to sign in again",
	}[failure.action.Reason]
	return fmt.Sprintf(
		"account %s needs you to sign in before it can use %s (%s): %s",
		sanitizeCell(failure.action.Alias, 64),
		sanitizeCell(string(failure.action.Service), 32),
		sanitizeCell(string(failure.action.Provider), 64),
		reason,
	)
}

func (failure *signInError) Unwrap() error { return failure.err }

// Printed commands target a POSIX shell on Unix and PowerShell on Windows.
// Execution always uses the original typed argv, never this presentation text.
func formatCommand(name string, arguments []string) string {
	return formatCommandForOS(runtimepkg.GOOS, name, arguments)
}

func formatCommandForOS(goos, name string, arguments []string) string {
	parts := make([]string, 0, len(arguments)+2)
	if goos == "windows" {
		// PowerShell requires its call operator for a quoted executable path.
		parts = append(parts, "&")
	}
	parts = append(parts, quoteCommandArgumentForOS(goos, name))
	for _, argument := range arguments {
		parts = append(parts, quoteCommandArgumentForOS(goos, argument))
	}
	return strings.Join(parts, " ")
}

func quoteCommandArgumentForOS(goos, value string) string {
	if goos == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@%+=,-", r)
		return !safe
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
