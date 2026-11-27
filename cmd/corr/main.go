// Command corr provides the Corresync CLI, daemon, and MCP entry point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	kongcompletion "github.com/jotaen/kong-completion"
	"golang.org/x/term"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/feedback"
	"github.com/nkiyohara/corresync/internal/panicguard"
	"github.com/nkiyohara/corresync/internal/paths"
	"github.com/nkiyohara/corresync/internal/updatecheck"
)

// cli is the root command. Each visible top-level command names its section of
// the root help with a helpgroup tag; rootHelpGroups lists the sections in the
// order they are shown. Fields are ordered as the root help lists them.
type cli struct {
	ConfigPath  string           `name:"config" type:"path" env:"CORRESYNC_CONFIG" help:"Path to config.toml."`
	VersionFlag kong.VersionFlag `name:"version" short:"V" help:"Print version information and quit."`

	Mail     mailCommand     `cmd:"" helpgroup:"everyday" help:"Read, search, send, and organize mail."`
	Calendar calendarCommand `cmd:"" helpgroup:"everyday" help:"List, create, change, and cancel calendar events."`
	Agenda   agendaCommand   `cmd:"" helpgroup:"everyday" help:"List events from all your calendar accounts together."`
	Tasks    tasksCommand    `cmd:"" helpgroup:"everyday" help:"List, create, update, and complete tasks."`
	Queries  queriesCommand  `cmd:"" helpgroup:"everyday" help:"Save mail and calendar searches and run them again."`
	Messages messagesCommand `cmd:"" hidden:"" help:"Read and manage provider-neutral messaging."`

	Setup    setupCommand    `cmd:"" helpgroup:"accounts" help:"Set up Corresync step by step, or add one account from a script."`
	Settings settingsCommand `cmd:"" helpgroup:"accounts" help:"Change accounts and everyday settings from a menu."`
	Account  accountCommand  `cmd:"" helpgroup:"accounts" help:"Find, add, rename, and remove accounts."`
	Auth     authCommand     `cmd:"" helpgroup:"accounts" help:"Sign in to accounts, check sign-in status, and sign out."`
	Login    loginCommand    `cmd:"" hidden:"" help:"Open the interactive provider sign-in."`

	MCP          mcpCommand          `cmd:"" helpgroup:"agents" help:"Run the MCP server, or add it to an AI agent."`
	Integrations integrationsCommand `cmd:"" helpgroup:"agents" help:"Find AI agents on this computer and connect Corresync to them."`

	Doctor     doctorCommand     `cmd:"" helpgroup:"maintenance" help:"Check your setup for problems."`
	Update     updateCommand     `cmd:"" helpgroup:"maintenance" help:"Install a verified update, or show the package manager command."`
	Config     configCommand     `cmd:"" helpgroup:"maintenance" help:"Show, check, and change the configuration file."`
	Completion completionCommand `cmd:"" helpgroup:"maintenance" help:"Print or install tab completion for your shell."`
	Feedback   feedbackCommand   `cmd:"" helpgroup:"maintenance" help:"Show a private diagnostics report and choose whether to share it."`
	Version    versionCommand    `cmd:"" helpgroup:"maintenance" help:"Print version and build information."`

	Monitor monitorCommand `cmd:"" helpgroup:"advanced" help:"Watch an account for new mail and notify you or an agent."`
	Events  eventsCommand  `cmd:"" helpgroup:"advanced" help:"List, acknowledge, and delete events found by monitoring."`
	Import  importCommand  `cmd:"" helpgroup:"advanced" help:"Read a local mail or calendar export into private staging."`
	Daemon  daemonCommand  `cmd:"" helpgroup:"advanced" help:"Start, stop, and check the local daemon."`
}

// rootHelpGroup is one section of the root help.
type rootHelpGroup struct {
	Key   string
	Title string
}

// rootHelpGroups lists the root help sections in display order. Every visible
// top-level command must name exactly one of these keys in its helpgroup tag.
var rootHelpGroups = []rootHelpGroup{
	{Key: "everyday", Title: "Everyday"},
	{Key: "accounts", Title: "Accounts and sign-in"},
	{Key: "agents", Title: "AI agents"},
	{Key: "maintenance", Title: "Maintenance"},
	{Key: "advanced", Title: "Advanced"},
}

const rootHelpGroupTag = "helpgroup"

type versionCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

const (
	commandName    = "corr"
	productTagline = "Mail, calendars, and tasks from the terminal and for AI agents."
)

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
		view.strong("Corresync"),
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
		app := newRuntime(executionContext, "", stdout, stderr, buildinfo.Current())
		if path := os.Getenv("CORRESYNC_CONFIG"); path != "" {
			app.configPath = path
		} else {
			app.configPath = os.Getenv("OWA_CONFIG")
		}
		result, updateErr := app.maybeHandleAutomaticUpdate(executionContext)
		if updateErr != nil {
			printCommandError(stderr, updateErr, true)
			return 1
		}
		if result.Status == updatecheck.InstallStatusUpdated {
			return app.runUpdatedCommand(executionContext, nil, result)
		}
		app.offerSkillUpdates()
		arguments = []string{"--help"}
	}

	info := buildinfo.Current()
	var commandLine cli
	exitCode := -1
	parser, err := kong.New(
		&commandLine,
		kong.Name(commandName),
		kong.Description(productTagline),
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
		printCommandError(stderr, err, !jsonOutputRequested(arguments))
		return 2
	}

	configPath := commandLine.ConfigPath
	if configPath == "" {
		// Read the exact v0.6 environment override only as a migration input.
		// It is deliberately absent from Corresync help and generated config.
		configPath = os.Getenv("OWA_CONFIG")
	}
	app := newRuntime(executionContext, configPath, stdout, stderr, info)
	if shouldHandleAutomaticUpdate(arguments) {
		result, updateErr := app.maybeHandleAutomaticUpdate(executionContext)
		if updateErr != nil {
			printCommandError(stderr, updateErr, !jsonOutputRequested(arguments))
			return 1
		}
		if result.Status == updatecheck.InstallStatusUpdated {
			return app.runUpdatedCommand(executionContext, arguments, result)
		}
		app.offerSkillUpdates()
	}
	if err := parsed.Run(app); err != nil {
		root := rootCommand(arguments)
		var record feedback.ErrorRecord
		if root != "feedback" {
			record = recordExecutionFailure(err, parsed.Command(), arguments)
		}
		printCommandError(stderr, err, !jsonOutputRequested(arguments))
		if root != "feedback" {
			app.maybeSubmitAutomaticFeedback(executionContext, root, record)
		}
		return 1
	}
	return 0
}

func recordExecutionFailure(
	err error,
	command string,
	arguments []string,
) feedback.ErrorRecord {
	record := feedback.NewErrorRecord(err, command, arguments)
	path, pathErr := paths.FeedbackErrorPath()
	if pathErr != nil {
		return record
	}
	_ = (feedback.Store{Path: path}).Save(record)
	return record
}

func compactHelpPrinter(options kong.HelpOptions, ctx *kong.Context) error {
	if ctx.Selected() != nil {
		return kong.DefaultHelpPrinter(options, ctx)
	}
	view := newConsoleViewWithEnvironment(ctx.Stdout, outputIsTerminal(ctx.Stdout), os.LookupEnv)
	_, err := io.WriteString(ctx.Stdout, rootHelp(ctx.Model, view))
	return err
}

// rootHelp renders the grouped command overview shown by `corr` and
// `corr --help`.
func rootHelp(model *kong.Application, view consoleView) string {
	sections := rootHelpSections(model.Children)
	name := model.Name
	width := 0
	for _, section := range sections {
		for _, command := range section.commands {
			width = max(width, len(command.Name))
		}
	}
	var help strings.Builder
	_, _ = fmt.Fprintf(
		&help,
		"%s  %s\n%s\n\nUsage:\n  %s <command> [flags]\n",
		view.info(),
		view.strong("Corresync"),
		strings.TrimSuffix(model.Help, "."),
		name,
	)
	for _, section := range sections {
		_, _ = fmt.Fprintf(&help, "\n%s:\n", section.title)
		for _, command := range section.commands {
			_, _ = fmt.Fprintf(
				&help,
				"  %s  %s\n",
				view.command(fmt.Sprintf("%-*s", width, command.Name)),
				strings.TrimSuffix(command.Help, "."),
			)
		}
	}
	help.WriteString("\nFlags:\n  -h, --help           Show help\n  -V, --version        Print version information\n      --config <path>  Use a specific config.toml\n\nStart:\n")
	startWidth := len(name + " help <command>")
	for _, start := range [][2]string{
		{name + " setup", "Add an account and sign in, step by step"},
		{name + " help <command>", "Show what one command can do"},
	} {
		_, _ = fmt.Fprintf(&help, "  %s  %s\n", view.command(fmt.Sprintf("%-*s", startWidth, start[0])), start[1])
	}
	return help.String()
}

type rootHelpSection struct {
	title    string
	commands []*kong.Node
}

// rootHelpSections groups the visible top-level commands in rootHelpGroups
// order. A visible command without a known group is listed under "Other" so it
// is never lost; TestRootHelpGroupsCoverEveryVisibleCommand rejects that case.
func rootHelpSections(commands []*kong.Node) []rootHelpSection {
	sections := make([]rootHelpSection, 0, len(rootHelpGroups)+1)
	listed := make(map[*kong.Node]bool, len(commands))
	for _, group := range rootHelpGroups {
		section := rootHelpSection{title: group.Title}
		for _, command := range commands {
			if !command.Hidden && rootHelpGroupKey(command) == group.Key {
				section.commands = append(section.commands, command)
				listed[command] = true
			}
		}
		if len(section.commands) > 0 {
			sections = append(sections, section)
		}
	}
	other := rootHelpSection{title: "Other"}
	for _, command := range commands {
		if !command.Hidden && !listed[command] {
			other.commands = append(other.commands, command)
		}
	}
	if len(other.commands) > 0 {
		sections = append(sections, other)
	}
	return sections
}

func rootHelpGroupKey(command *kong.Node) string {
	if command.Tag == nil {
		return ""
	}
	return command.Tag.Get(rootHelpGroupTag)
}

func versionLine(info buildinfo.Info) string {
	return fmt.Sprintf(
		commandName+" %s (commit %s, built %s, %s)",
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

func inputIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func completionEnvironmentActive() bool {
	return os.Getenv("COMP_LINE") != ""
}

// terminationSignals lists the signals that cancel the running command. A lost
// terminal or SSH connection (SIGHUP) cancels like Ctrl-C, so an unfinished
// login is closed instead of waiting for the daemon timeout. A process
// started under nohup keeps ignoring SIGHUP.
func terminationSignals() []os.Signal {
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	return signals
}

// oldCommandNameNotice returns the removal notice for the `corresync`
// compatibility command (ADR 0016), or nothing when the program was started
// as corr.
func oldCommandNameNotice(argv0 string) string {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(argv0)), ".exe")
	if name != "corresync" {
		return ""
	}
	return "corresync is the old name of this command and will be removed in 0.11.0. Use corr instead."
}

func main() {
	// Only a person at a terminal sees the notice; scripts, pipes, and MCP
	// hosts that still start the old name keep their output unchanged.
	if notice := oldCommandNameNotice(os.Args[0]); notice != "" &&
		outputIsTerminal(os.Stderr) && !completionEnvironmentActive() {
		_, _ = fmt.Fprintln(os.Stderr, notice)
	}
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	code := runWithCrashBoundary(
		ctx,
		os.Args[1:],
		os.Stdout,
		os.Stderr,
		buildinfo.Current(),
		run,
	)
	stop()
	os.Exit(code)
}

type cliExecutionRunner func(context.Context, []string, io.Writer, io.Writer) int

func runWithCrashBoundary(
	executionContext context.Context,
	arguments []string,
	stdout, stderr io.Writer,
	info buildinfo.Info,
	execute cliExecutionRunner,
) (code int) {
	role := crashProcessRole(arguments)
	recorder := panicguard.Recorder(func(boundary panicguard.Boundary, callers []uintptr) {
		recordProcessCrash(info, role, boundary, callers)
	})
	executionContext = panicguard.WithRecorder(executionContext, recorder)
	defer func() {
		if recovered := recover(); recovered != nil {
			panicguard.Record(executionContext, panicguard.BoundaryProcess)
			printCommandError(stderr, withHint(
				errors.New("corresync stopped unexpectedly"),
				"corr feedback --last-error to see the crash report and choose whether to share it",
			), true)
			code = 1
		}
	}()
	return execute(executionContext, arguments, stdout, stderr)
}

func recordProcessCrash(
	info buildinfo.Info,
	role string,
	boundary panicguard.Boundary,
	callers []uintptr,
) {
	path, err := paths.FeedbackCrashPath()
	if err != nil {
		return
	}
	record := feedback.NewCrashRecord(
		feedback.Build{
			Version: info.Version, Commit: info.Commit, BuildDate: info.BuildDate,
			GoVersion: info.GoVersion, Platform: info.OS + "/" + info.Arch,
		},
		role,
		string(boundary),
		time.Now(),
		callers,
	)
	_ = (feedback.CrashStore{Path: path}).Save(record)
}

func crashProcessRole(arguments []string) string {
	commands := make([]string, 0, 2)
	for index := 0; index < len(arguments) && len(commands) < 2; index++ {
		argument := arguments[index]
		if argument == "--config" {
			index++
			continue
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		commands = append(commands, argument)
	}
	if len(commands) == 2 && commands[0] == "daemon" && commands[1] == "serve" {
		return "daemon"
	}
	if len(commands) == 2 && commands[0] == "mcp" && commands[1] == "serve" {
		return "mcp"
	}
	return "cli"
}
