package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
	"github.com/nkiyohara/corresync/internal/localipc"
)

const setupInspectionTimeout = 750 * time.Millisecond

type setupCompletionState string

const (
	setupCompletionUnavailable setupCompletionState = "unavailable"
	setupCompletionAbsent      setupCompletionState = "absent"
	setupCompletionCurrent     setupCompletionState = "current"
	setupCompletionConflict    setupCompletionState = "conflict"
)

type setupCompletionInspection struct {
	shell string
	path  string
	state setupCompletionState
}

type setupPreflight struct {
	configPath     string
	createdConfig  bool
	accounts       application.AccountCatalog
	sessions       map[string]application.SessionStatus
	sessionProblem string
	completion     setupCompletionInspection
	agents         agenthost.Report
	agentProblem   string
}

type setupIntegrationOutcome struct {
	selected bool
	results  []integrationlifecycle.Result
}

// runGuidedAccountSetup is `corr setup` without an address. It starts with
// the question the person came for (the email address); tab completion and
// AI agents are short optional steps after the accounts, and the summary at
// the end names the one command to run next.
func runGuidedAccountSetup(app *runtime, createdConfig bool) error {
	preflight, err := inspectSetupPreflight(app)
	if err != nil {
		return err
	}
	preflight.createdConfig = createdConfig
	if err := writeOnboardingWelcome(app, preflight.accounts.Accounts); err != nil {
		return err
	}

	progress, err := runOnboardingAccountPhase(app, createdConfig)
	if err != nil {
		if createdConfig {
			view := newConsoleView(app, app.stdout, true)
			_, _ = view.printf(
				"\n   %s\n",
				view.muted("Settings were saved in a new file: "+sanitizeCell(preflight.configPath, 2048)),
			)
		}
		return err
	}
	if progress.cancelled {
		if len(preflight.accounts.Accounts) > 0 && progress.added == 0 {
			return writeOnboardingPaused(app)
		}
		return nil
	}
	if !progress.proceed {
		return nil
	}

	// Account setup may have started or replaced the daemon, so derive the
	// final summary from current local state rather than the opening view.
	preflight, err = refreshSetupAccountState(app, preflight)
	if err != nil {
		return err
	}
	completionProblem := offerSetupCompletion(app, &preflight.completion)
	inspectSetupAgents(app, &preflight)
	integrations, integrationErr := runSetupAgentStep(app, preflight.agents)
	if err := writeSetupSummary(
		app,
		preflight,
		integrations,
		completionProblem,
		integrationErr,
	); err != nil {
		return errors.Join(completionProblem, integrationErr, err)
	}
	return errors.Join(completionProblem, integrationErr)
}

func inspectSetupPreflight(app *runtime) (setupPreflight, error) {
	configPath, err := app.resolvedConfigPath()
	if err != nil {
		return setupPreflight{}, err
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return setupPreflight{}, err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return setupPreflight{}, err
	}
	preflight := setupPreflight{
		configPath: configPath,
		accounts:   catalog,
		completion: inspectSetupCompletion(app),
	}
	preflight.sessions, preflight.sessionProblem = inspectSetupSessions(
		app,
		configPath,
		catalog,
	)
	return preflight, nil
}

// inspectSetupAgents looks for AI agents on this computer. It runs only after
// the accounts are done, so a slow lookup never delays the first question.
func inspectSetupAgents(app *runtime, preflight *setupPreflight) {
	report, err := app.detectAgentHosts(app.context, agenthost.Request{})
	preflight.agents = report
	if err != nil {
		preflight.agentProblem = err.Error()
	}
	if report.Failure != nil && preflight.agentProblem == "" {
		preflight.agentProblem = report.Failure.Code
	}
}

func refreshSetupAccountState(
	app *runtime,
	preflight setupPreflight,
) (setupPreflight, error) {
	accounts, _, err := app.accountServices()
	if err != nil {
		return setupPreflight{}, err
	}
	preflight.accounts, err = accounts.List(app.context)
	if err != nil {
		return setupPreflight{}, err
	}
	preflight.sessions, preflight.sessionProblem = inspectSetupSessions(
		app,
		preflight.configPath,
		preflight.accounts,
	)
	preflight.completion = inspectSetupCompletion(app)
	return preflight, nil
}

func inspectSetupCompletion(app *runtime) setupCompletionInspection {
	shell, err := detectCompletionShell(app.lookupEnv)
	if err != nil {
		return setupCompletionInspection{
			state: setupCompletionUnavailable,
		}
	}
	path, err := completionInstallPath(shell, app.lookupEnv)
	if err != nil {
		return setupCompletionInspection{
			shell: shell, state: setupCompletionUnavailable,
		}
	}
	inspection := setupCompletionInspection{shell: shell, path: path}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		inspection.state = setupCompletionAbsent
		return inspection
	case err != nil:
		inspection.state = setupCompletionConflict
		return inspection
	case !info.Mode().IsRegular():
		inspection.state = setupCompletionConflict
		return inspection
	}
	contents, err := readCompletionFile(path, info)
	if err != nil {
		inspection.state = setupCompletionConflict
		return inspection
	}
	if bytes.Equal(contents, []byte(completionScripts[shell])) {
		inspection.state = setupCompletionCurrent
		return inspection
	}
	inspection.state = setupCompletionConflict
	return inspection
}

func offerSetupCompletion(
	app *runtime,
	inspection *setupCompletionInspection,
) error {
	if inspection.state != setupCompletionAbsent {
		return nil
	}
	confirmed, err := runOnboardingConfirm(
		app,
		"Install tab completion for "+inspection.shell+"?",
		"Adds one file; remove it to uninstall: "+sanitizeCell(inspection.path, 2048),
		"Install",
		"Later",
	)
	if err != nil || !confirmed {
		return err
	}
	if err := (&completionInstallCommand{Shell: inspection.shell}).Run(app); err != nil {
		inspection.state = setupCompletionConflict
		return fmt.Errorf("install %s completion: %w", inspection.shell, err)
	}
	*inspection = inspectSetupCompletion(app)
	return nil
}

func inspectSetupSessions(
	app *runtime,
	configPath string,
	catalog application.AccountCatalog,
) (map[string]application.SessionStatus, string) {
	result := make(map[string]application.SessionStatus, len(catalog.Accounts))
	for _, account := range catalog.Accounts {
		result[account.Alias] = application.SessionStatus{
			Account: account.ID, Alias: account.Alias, State: "signed_out",
		}
	}
	if len(catalog.Accounts) == 0 {
		return result, ""
	}
	endpoint, err := app.endpoint(configPath)
	if err != nil {
		return result, "cannot resolve the local daemon"
	}
	// No daemon is the normal state right after setup: every account is
	// simply signed out, which is not a problem to report.
	if active, activeErr := localipc.EndpointActive(endpoint); activeErr == nil && !active {
		return result, ""
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return result, "cannot inspect the local daemon"
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(app.context, setupInspectionTimeout)
	defer cancel()
	owner, err := client.InspectOwner(ctx, app.caller())
	status := owner.Status()
	if err != nil {
		return result, "the running daemon uses an incompatible local protocol"
	}
	if status.ProcessID < 1 {
		return result, ""
	}
	fingerprint, err := config.Fingerprint(configPath)
	if err != nil || status.ConfigDigest != fingerprint ||
		status.ProtocolVersion != daemonapi.ProtocolVersion ||
		status.Version != app.info.Version {
		return result, "the running daemon does not match the current settings and version"
	}
	sessions, err := client.SessionStatus(ctx, app.caller())
	if err != nil {
		return result, "the running daemon could not report account status"
	}
	for _, session := range sessions.Accounts {
		result[session.Alias] = session
	}
	return result, ""
}

// setupDetectedLifecycleHosts lists the agents found on this computer that
// setup can both connect and inspect.
func setupDetectedLifecycleHosts(report agenthost.Report) []string {
	hosts := make([]string, 0, len(report.Hosts))
	for _, detection := range report.Hosts {
		if !detection.Host.Lifecycle.Setup || !detection.Host.Lifecycle.Inspect {
			continue
		}
		switch detection.Status {
		case agenthost.StatusConfirmed, agenthost.StatusProbable,
			agenthost.StatusSelectedMissing:
			hosts = append(hosts, string(detection.Host.ID))
		case agenthost.StatusNotFound, agenthost.StatusUnsupportedSurface:
		}
	}
	return hosts
}

func setupAgentFound(status agenthost.DetectionStatus) bool {
	return status == agenthost.StatusConfirmed || status == agenthost.StatusProbable
}

// runSetupAgentStep offers to connect AI agents as one optional question. The
// agent's settings change only after the person picks agents and approves the
// plan that follows.
func runSetupAgentStep(
	app *runtime,
	report agenthost.Report,
) (setupIntegrationOutcome, error) {
	found := make([]string, 0, len(report.Hosts))
	selectable := 0
	for _, detection := range report.Hosts {
		if !detection.Host.Lifecycle.Setup ||
			detection.Status == agenthost.StatusUnsupportedSurface {
			continue
		}
		selectable++
		if setupAgentFound(detection.Status) {
			found = append(found, sanitizeCell(detection.Host.DisplayName, 64))
		}
	}
	if selectable == 0 {
		return setupIntegrationOutcome{}, nil
	}
	description := "None found on this computer, but you can still pick one."
	if len(found) > 0 {
		description = "Found: " + strings.Join(found, ", ") + "."
	}
	description += " You review each change before it is made."
	connect, err := runOnboardingConfirm(
		app, "Connect an AI agent?", description, "Choose agents", "Later",
	)
	if err != nil || !connect {
		return setupIntegrationOutcome{}, err
	}
	return runSetupAgentSelection(app, report)
}

func runSetupAgentSelection(
	app *runtime,
	report agenthost.Report,
) (setupIntegrationOutcome, error) {
	options := make([]huh.Option[agenthost.ID], 0, len(report.Hosts))
	for _, detection := range report.Hosts {
		if !detection.Host.Lifecycle.Setup ||
			detection.Status == agenthost.StatusUnsupportedSurface {
			continue
		}
		label := detection.Host.DisplayName + " (not found on this computer)"
		if setupAgentFound(detection.Status) {
			label = detection.Host.DisplayName + " (found)"
		}
		options = append(options, huh.NewOption(label, detection.Host.ID).
			Selected(setupAgentFound(detection.Status)))
	}
	if len(options) == 0 {
		return setupIntegrationOutcome{}, nil
	}
	selected, submitted, err := runSettingsMultiSelect(
		app,
		"Which agents should use Corresync?",
		"Agents found on this computer are selected. Corresync never installs an agent.",
		options,
		nil,
	)
	if err != nil || !submitted {
		return setupIntegrationOutcome{}, err
	}
	outcome := setupIntegrationOutcome{selected: true}
	if len(selected) == 0 {
		return outcome, nil
	}
	hosts := make([]string, len(selected))
	for index, host := range selected {
		hosts[index] = string(host)
	}
	results, err := runIntegrationMutationWithResults(
		app,
		integrationMutationFlags{integrationTargetFlags: integrationTargetFlags{
			Hosts: hosts, Name: "corresync", Scope: string(agenthost.ScopeUser),
		}},
		integrationlifecycle.OperationSetup,
	)
	outcome.results = results
	return outcome, err
}

func writeOnboardingPaused(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n",
		view.info(),
		view.strong("Setup paused. Nothing was changed."),
		view.muted("Run corr setup again to continue."),
	)
	return err
}

// writeSetupSummary ends guided setup: what is ready, what still needs the
// person, the settings file (named once, here), and the one command to run
// next.
func writeSetupSummary(
	app *runtime,
	preflight setupPreflight,
	integrations setupIntegrationOutcome,
	completionProblem error,
	integrationProblem error,
) error {
	ready := make([]string, 0, len(preflight.accounts.Accounts)+len(integrations.results)+1)
	attention := make([]string, 0, len(preflight.accounts.Accounts)+len(integrations.results)+2)
	optional := make([]string, 0, 2)
	next := ""

	for _, account := range preflight.accounts.Accounts {
		session := preflight.sessions[account.Alias]
		route := setupAccountServices(account)
		switch {
		case session.Authenticated && len(session.Degradations) > 0:
			attention = append(attention, fmt.Sprintf(
				"Account %s · %d problem(s) found after sign-in; %s",
				account.Alias,
				len(session.Degradations),
				shellCommand("corr", "auth", "status", "--account", account.Alias),
			))
		case session.Authenticated:
			ready = append(ready, "Account "+account.Alias+" · "+route+" · signed in")
		default:
			ready = append(ready, "Account "+account.Alias+" · "+route)
			attention = append(attention, "Account "+account.Alias+" · not signed in yet")
			if next == "" {
				next = shellCommand("corr", "auth", "login", "--account", account.Alias)
			}
		}
	}

	switch preflight.completion.state {
	case setupCompletionCurrent:
		ready = append(ready, preflight.completion.shell+" tab completion · installed")
	case setupCompletionAbsent:
		optional = append(optional, "Tab completion · corr completion install --shell auto")
	case setupCompletionUnavailable:
		optional = append(optional, "Tab completion · corr completion install --help")
	case setupCompletionConflict:
		attention = append(attention, fmt.Sprintf(
			"Tab completion · a different file is at %s; check it, then run corr completion install --shell %s --force",
			preflight.completion.path,
			preflight.completion.shell,
		))
	}
	if completionProblem != nil && preflight.completion.state != setupCompletionConflict {
		attention = append(attention, "Tab completion · installation did not finish")
	}

	for _, result := range integrations.results {
		switch result.Status {
		case integrationlifecycle.ResultAppliedVerified,
			integrationlifecycle.ResultAlreadyCurrent:
			ready = append(ready, string(result.Host)+" · connected and checked")
		case integrationlifecycle.ResultReloadRequired:
			attention = append(attention, string(result.Host)+" · open a new session or reload the host")
		case integrationlifecycle.ResultAlreadyAbsent,
			integrationlifecycle.ResultSkipped:
			optional = append(optional, string(result.Host)+" · corr integrations setup "+string(result.Host))
		case integrationlifecycle.ResultBlocked,
			integrationlifecycle.ResultFailedPreserved,
			integrationlifecycle.ResultFailedChanged:
			attention = append(attention, string(result.Host)+" · "+result.Message)
		default:
			attention = append(attention, fmt.Sprintf(
				"%s · unknown integration result %q; corr integrations doctor %s",
				result.Host,
				result.Status,
				result.Host,
			))
		}
	}
	if !integrations.selected ||
		(len(integrations.results) == 0 && integrationProblem == nil) {
		optional = append(optional, "AI agent · corr integrations setup HOST")
	}
	if integrationProblem != nil && len(integrations.results) == 0 {
		attention = append(attention, "AI agent · run corr setup again or corr integrations setup HOST")
	}
	if preflight.sessionProblem != "" {
		attention = append(attention, "Sign-in status · "+preflight.sessionProblem+"; corr daemon status")
	}
	if preflight.agentProblem != "" {
		attention = append(attention, "AI agent search did not finish · corr integrations detect --refresh")
	}

	view := newConsoleView(app, app.stdout, true)
	width := app.textWidth(app.stdout)
	if _, err := view.printf("\n%s  %s\n", view.success(), view.strong("Setup summary")); err != nil {
		return err
	}
	for _, group := range []struct {
		title string
		items []string
	}{
		{title: "Ready", items: ready},
		{title: "Needs attention", items: attention},
		{title: "Later, if you want", items: optional},
	} {
		if len(group.items) == 0 {
			continue
		}
		if _, err := fmt.Fprintln(app.stdout, "\n  "+group.title); err != nil {
			return err
		}
		for _, item := range group.items {
			if _, err := fmt.Fprintln(
				app.stdout,
				"    - "+strings.TrimPrefix(wrapText(sanitizeCell(item, 2300), width, "      "), "      "),
			); err != nil {
				return err
			}
		}
	}
	settingsLine := "Settings file: " + sanitizeCell(preflight.configPath, 2048)
	if preflight.createdConfig {
		settingsLine = "Settings were saved in a new file: " + sanitizeCell(preflight.configPath, 2048)
	}
	lines := []string{
		"",
		wrapText(view.muted(settingsLine), width, "  "),
		wrapText(view.muted("Run corr setup again at any time to add an account or repair one."), width, "  "),
	}
	if next != "" {
		lines = append(lines, "", "  "+view.nextStep(next))
	}
	_, err := fmt.Fprintln(app.stdout, strings.Join(lines, "\n"))
	return err
}

// setupAccountServices names an account's services and how each connects,
// such as "Mail and Calendar (Outlook Web)" or "Mail (IMAP / SMTP)".
func setupAccountServices(account application.AccountView) string {
	type service struct{ name, provider string }
	services := make([]service, 0, 4)
	for _, route := range []struct {
		name  string
		route *application.AccountRouteView
	}{{"Mail", account.Mail}, {"Calendar", account.Calendar}, {"Tasks", account.Tasks}} {
		if route.route != nil {
			services = append(services, service{route.name, providerDisplayName(route.route.Provider)})
		}
	}
	if account.Messages != nil {
		services = append(services, service{"Messages", string(account.Messages.Provider)})
	}
	parts := make([]string, 0, len(services))
	for index := 0; index < len(services); index++ {
		names := []string{services[index].name}
		for index+1 < len(services) && services[index+1].provider == services[index].provider {
			index++
			names = append(names, services[index].name)
		}
		parts = append(parts, strings.Join(names, " and ")+" ("+services[index].provider+")")
	}
	if len(parts) == 0 {
		return "no services"
	}
	return strings.Join(parts, ", ")
}
