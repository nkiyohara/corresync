// Command owa provides the CLI, daemon, and MCP entry point for owa-bridge.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	kongcompletion "github.com/jotaen/kong-completion"
	"golang.org/x/term"

	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
)

type cli struct {
	ConfigPath  string            `name:"config" type:"path" env:"OWA_CONFIG" help:"Path to config.toml."`
	VersionFlag kong.VersionFlag  `name:"version" short:"V" help:"Print version information and quit."`
	Version     versionCommand    `cmd:"" help:"Print version and build information."`
	Config      configCommand     `cmd:"" help:"Initialize and inspect configuration."`
	Doctor      doctorCommand     `cmd:"" help:"Diagnose local setup and opt-in OWA compatibility."`
	Auth        authCommand       `cmd:"" help:"Inspect and manage interactive sessions."`
	Login       loginCommand      `cmd:"" hidden:"" help:"Open the interactive Outlook Web sign-in."`
	Mail        mailCommand       `cmd:"" help:"Read and manage mail."`
	Calendar    calendarCommand   `cmd:"" help:"Read and manage calendar events."`
	Daemon      daemonCommand     `cmd:"" help:"Run and inspect the local session owner."`
	MCP         mcpCommand        `cmd:"" help:"Expose guarded Outlook tools over MCP."`
	Update      updateCommand     `cmd:"" help:"Install verified updates or show the package-manager command."`
	Completion  completionCommand `cmd:"" help:"Generate a shell completion script."`
}

type versionCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

func (command *versionCommand) Run(app *runtime) error {
	if command.JSON {
		encoder := json.NewEncoder(app.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(app.info)
	}

	if !app.interactiveStdout() {
		_, err := fmt.Fprintln(app.stdout, versionLine(app.info))
		return err
	}
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s · %s/%s\n",
		view.info(),
		view.strong("OWA Bridge"),
		"Version",
		app.info.Version,
		"Commit",
		app.info.Commit,
		"Built",
		app.info.BuildDate,
		"Runtime",
		app.info.GoVersion,
		app.info.OS,
		app.info.Arch,
	)
	return err
}

func run(executionContext context.Context, arguments []string, stdout, stderr io.Writer) int {
	arguments = normalizeHelpArguments(arguments)
	if !completionEnvironmentActive() && len(arguments) == 0 {
		arguments = []string{"--help"}
	}

	info := buildinfo.Current()
	var commandLine cli
	exitCode := -1
	parser, err := kong.New(
		&commandLine,
		kong.Name("owa"),
		kong.Description("Local-first Outlook Web mail and calendar."),
		kong.Help(compactHelpPrinter),
		kong.Vars{"version": versionLine(info)},
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	var completionErr error
	kongcompletion.Register(
		parser,
		kongcompletion.WithExitFunc(func(code int) { exitCode = code }),
		kongcompletion.WithErrorHandler(func(err error) { completionErr = err }),
	)
	if completionErr != nil {
		_, _ = fmt.Fprintf(stderr, "initialize shell completion: %v\n", completionErr)
		return 1
	}
	if exitCode >= 0 {
		return exitCode
	}

	parsed, err := parser.Parse(arguments)
	if exitCode >= 0 {
		return exitCode
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	app := newRuntime(executionContext, commandLine.ConfigPath, stdout, stderr, info)
	if shouldOfferAutomaticUpdateNotice(arguments) {
		app.maybeNotifyUpdate(executionContext)
	}
	if err := parsed.Run(app); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func compactHelpPrinter(options kong.HelpOptions, ctx *kong.Context) error {
	if ctx.Selected() != nil {
		return kong.DefaultHelpPrinter(options, ctx)
	}
	width := 0
	for _, command := range ctx.Model.Children {
		if !command.Hidden && len(command.Name) > width {
			width = len(command.Name)
		}
	}
	view := newConsoleViewWithEnvironment(ctx.Stdout, outputIsTerminal(ctx.Stdout), os.LookupEnv)
	var help strings.Builder
	_, _ = fmt.Fprintf(
		&help,
		"%s  %s\n%s\n\n",
		view.info(),
		view.strong("OWA Bridge"),
		strings.TrimSuffix(ctx.Model.Help, "."),
	)
	_, _ = fmt.Fprintf(&help, "Usage:\n  %s <command> [flags]\n\nCommands:\n", ctx.Model.Name)
	for _, command := range ctx.Model.Children {
		if command.Hidden {
			continue
		}
		name := view.command(fmt.Sprintf("%-*s", width, command.Name))
		_, _ = fmt.Fprintf(
			&help,
			"  %s %s\n",
			name,
			strings.TrimSuffix(command.Help, "."),
		)
	}
	_, _ = fmt.Fprintf(
		&help,
		"\nFlags:\n  -h, --help           Show help\n  -V, --version        Print version information\n      --config <path>  Use a specific config.toml\n\nRun %s for command-specific help.\n",
		view.command(ctx.Model.Name+" help <command>"),
	)
	_, err := io.WriteString(ctx.Stdout, help.String())
	return err
}

func versionLine(info buildinfo.Info) string {
	return fmt.Sprintf(
		"owa %s (commit %s, built %s, %s)",
		info.Version,
		info.Commit,
		info.BuildDate,
		info.GoVersion,
	)
}

func normalizeHelpArguments(arguments []string) []string {
	if len(arguments) == 0 || arguments[0] != "help" {
		return arguments
	}
	normalized := append([]string(nil), arguments[1:]...)
	return append(normalized, "--help")
}

func outputIsTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func completionEnvironmentActive() bool {
	return os.Getenv("COMP_LINE") != ""
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
