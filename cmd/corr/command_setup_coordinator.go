package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
)

const (
	setupInspectionTimeout            = 750 * time.Millisecond
	setupIntegrationInspectionTimeout = 3 * time.Second
)

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
	configPath        string
	accounts          application.AccountCatalog
	sessions          map[string]application.SessionStatus
	sessionProblem    string
	completion        setupCompletionInspection
	agents            agenthost.Report
	agentProblem      string
	integrationStates map[agenthost.ID]integrationlifecycle.State
}

type setupIntegrationOutcome struct {
	selected bool
	results  []integrationlifecycle.Result
}

func runGuidedAccountSetup(app *runtime, createdConfig bool) error {
	if err := writeOnboardingWelcome(app); err != nil {
		return err
	}
	preflight, err := inspectSetupPreflight(app)
	if err != nil {
		return err
	}
	if err := writeSetupPreflight(app, preflight); err != nil {
		return err
	}

	completionProblem := offerSetupCompletion(app, &preflight.completion)
	progress, err := runOnboardingAccountPhase(app, createdConfig)
	if err != nil {
		return errors.Join(completionProblem, err)
	}
	if progress.cancelled {
		if len(preflight.accounts.Accounts) > 0 && progress.added == 0 {
			return errors.Join(completionProblem, writeOnboardingPaused(app))
		}
		return completionProblem
	}
	if !progress.proceed {
		return completionProblem
	}

	// Account setup may have started or replaced the session owner, so derive
	// the final summary from current local state rather than the opening view.
	preflight, err = refreshSetupAccountState(app, preflight)
	if err != nil {
		return errors.Join(completionProblem, err)
	}
	integrations, integrationErr := runSetupAgentSelection(app, preflight.agents)
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
	preflight.agents, err = app.detectAgentHosts(
		app.context,
		agenthost.Request{},
	)
	if err != nil {
		preflight.agentProblem = err.Error()
	}
	if preflight.agents.Failure != nil && preflight.agentProblem == "" {
		preflight.agentProblem = preflight.agents.Failure.Code
	}
	if len(catalog.Accounts) > 0 {
		preflight.integrationStates = inspectSetupIntegrationStates(
			app,
			preflight.agents,
		)
	}
	return preflight, nil
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
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"%s  %s\n   %s\n   %s\n",
		view.info(),
		view.strong("Optional shell completion"),
		view.muted("Target: "+sanitizeCell(inspection.path, 2048)),
		view.muted("Remove that generated file to uninstall."),
	); err != nil {
		return err
	}
	confirmed, err := runOnboardingConfirm(
		app,
		"Install shell completion?",
		fmt.Sprintf(
			"Corresync will write only %s. Remove that generated file to uninstall.",
			sanitizeCell(inspection.path, 2048),
		),
		"Install "+inspection.shell+" completion",
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
		return result, "cannot resolve the local session owner"
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		return result, "cannot inspect the local session owner"
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(app.context, setupInspectionTimeout)
	defer cancel()
	owner, err := client.InspectOwner(ctx, app.caller())
	status := owner.Status()
	if err != nil {
		return result, "the running session owner uses an incompatible local protocol"
	}
	if status.ProcessID < 1 {
		return result, ""
	}
	fingerprint, err := config.Fingerprint(configPath)
	if err != nil || status.ConfigDigest != fingerprint ||
		status.ProtocolVersion != daemonapi.ProtocolVersion ||
		status.Version != app.info.Version {
		return result, "the running session owner does not match the current configuration and binary"
	}
	sessions, err := client.SessionStatus(ctx, app.caller())
	if err != nil {
		return result, "the running session owner could not report account status"
	}
	for _, session := range sessions.Accounts {
		result[session.Alias] = session
	}
	return result, ""
}

func inspectSetupIntegrationStates(
	app *runtime,
	report agenthost.Report,
) map[agenthost.ID]integrationlifecycle.State {
	hosts := setupDetectedLifecycleHosts(report)
	if len(hosts) == 0 {
		return nil
	}
	engine, requests, err := prepareIntegrationLifecycle(
		app,
		integrationTargetFlags{Hosts: hosts, Scope: string(agenthost.ScopeUser)},
		integrationlifecycle.OperationSetup,
		true,
	)
	if err != nil {
		return nil
	}
	states := make(map[agenthost.ID]integrationlifecycle.State, len(requests))
	for _, request := range requests {
		ctx, cancel := context.WithTimeout(
			app.context,
			setupIntegrationInspectionTimeout,
		)
		inspection, inspectErr := engine.Inspect(ctx, request)
		cancel()
		if inspectErr == nil {
			states[request.Host] = inspection.State
		}
	}
	return states
}

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
		label := fmt.Sprintf(
			"%s · %s · %s",
			detection.Host.DisplayName,
			detection.Status,
			capabilitySummary(detection.Host),
		)
		option := huh.NewOption(label, detection.Host.ID)
		if detection.Status == agenthost.StatusConfirmed ||
			detection.Status == agenthost.StatusProbable {
			option = option.Selected(true)
		}
		options = append(options, option)
	}
	if len(options) == 0 {
		return setupIntegrationOutcome{}, nil
	}
	selected, submitted, err := runSettingsMultiSelect(
		app,
		"Which agents do you use?",
		"Detected hosts are preselected. Known but missing hosts remain available; Corresync never installs an agent.",
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

func writeSetupPreflight(app *runtime, preflight setupPreflight) error {
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"%s  %s\n\n  %-18s %s\n  %-18s %s\n  %-18s %s\n",
		view.info(),
		view.strong("Setup preflight"),
		"Version", sanitizeCell(app.info.Version, 128),
		"Configuration", sanitizeCell(preflight.configPath, 2048),
		"Completion", completionPreflightLabel(preflight.completion),
	); err != nil {
		return err
	}
	if len(preflight.accounts.Accounts) == 0 {
		if _, err := fmt.Fprintln(app.stdout, "  Accounts           none configured"); err != nil {
			return err
		}
	} else {
		for _, account := range preflight.accounts.Accounts {
			session := preflight.sessions[account.Alias]
			if _, err := fmt.Fprintf(
				app.stdout,
				"  Account            %-16s %s · %s\n",
				sanitizeCell(account.Alias, 64),
				accountRouteLabel(account),
				session.State,
			); err != nil {
				return err
			}
		}
	}
	detected := 0
	for _, detection := range preflight.agents.Hosts {
		if detection.Status != agenthost.StatusConfirmed &&
			detection.Status != agenthost.StatusProbable &&
			detection.Status != agenthost.StatusSelectedMissing {
			continue
		}
		detected++
		connection := "connection not inspected"
		if state, ok := preflight.integrationStates[detection.Host.ID]; ok {
			connection = string(state)
		}
		if _, err := fmt.Fprintf(
			app.stdout,
			"  Agent              %-22s %s · %s\n",
			sanitizeCell(detection.Host.DisplayName, 64),
			detection.Status,
			connection,
		); err != nil {
			return err
		}
	}
	if detected == 0 {
		if _, err := fmt.Fprintln(app.stdout, "  Agents             none detected; known hosts remain selectable"); err != nil {
			return err
		}
	}
	if preflight.agentProblem != "" {
		if _, err := view.printf(
			"  %s  %s\n",
			view.warning(),
			view.muted("Agent detection was incomplete; corr integrations detect --refresh can retry."),
		); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(app.stdout)
	return err
}

func completionPreflightLabel(inspection setupCompletionInspection) string {
	if inspection.shell == "" {
		return string(inspection.state)
	}
	return inspection.shell + " · " + string(inspection.state)
}

func writeOnboardingPaused(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n",
		view.info(),
		view.strong("Setup paused"),
		view.command("Resume from current state: corr setup"),
	)
	return err
}

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

	for _, account := range preflight.accounts.Accounts {
		ready = append(ready, fmt.Sprintf(
			"Account %s · %s route configured",
			account.Alias,
			accountRouteLabel(account),
		))
		session := preflight.sessions[account.Alias]
		if session.Authenticated {
			if len(session.Degradations) > 0 {
				attention = append(attention, fmt.Sprintf(
					"Account %s · %d observed degradation(s); corr auth status --account %s",
					account.Alias,
					len(session.Degradations),
					shellSingleQuote(account.Alias),
				))
			} else {
				ready = append(ready, "Account "+account.Alias+" · authenticated")
			}
		} else {
			attention = append(attention, fmt.Sprintf(
				"Account %s · %s; corr auth login --account %s",
				account.Alias,
				session.State,
				shellSingleQuote(account.Alias),
			))
		}
	}

	switch preflight.completion.state {
	case setupCompletionCurrent:
		ready = append(ready, preflight.completion.shell+" completion · installed")
	case setupCompletionAbsent:
		optional = append(optional, "Shell completion · corr completion install --shell auto")
	case setupCompletionUnavailable:
		optional = append(optional, "Shell completion · corr completion install --help")
	case setupCompletionConflict:
		attention = append(attention, fmt.Sprintf(
			"Shell completion · review %s before corr completion install --shell %s --force",
			preflight.completion.path,
			preflight.completion.shell,
		))
	}
	if completionProblem != nil && preflight.completion.state != setupCompletionConflict {
		attention = append(attention, "Shell completion · installation did not complete")
	}

	for _, result := range integrations.results {
		switch result.Status {
		case integrationlifecycle.ResultAppliedVerified,
			integrationlifecycle.ResultAlreadyCurrent:
			ready = append(ready, string(result.Host)+" · connected and verified")
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
		optional = append(optional, "Agent connection · corr integrations setup HOST")
	}
	if integrationProblem != nil && len(integrations.results) == 0 {
		attention = append(attention, "Agent connection · rerun corr setup or corr integrations setup HOST")
	}
	if preflight.sessionProblem != "" {
		attention = append(attention, "Session status · corr daemon status")
	}
	if preflight.agentProblem != "" {
		attention = append(attention, "Agent detection · corr integrations detect --refresh")
	}

	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf("\n%s  %s\n", view.success(), view.strong("Setup summary")); err != nil {
		return err
	}
	for _, group := range []struct {
		title string
		items []string
	}{
		{title: "Ready", items: ready},
		{title: "Needs attention", items: attention},
		{title: "Optional later", items: optional},
	} {
		if _, err := fmt.Fprintln(app.stdout, "\n"+group.title); err != nil {
			return err
		}
		if len(group.items) == 0 {
			if _, err := fmt.Fprintln(app.stdout, "  none"); err != nil {
				return err
			}
			continue
		}
		for _, item := range group.items {
			if _, err := fmt.Fprintln(app.stdout, "  - "+sanitizeCell(item, 2300)); err != nil {
				return err
			}
		}
	}
	_, err := view.printf(
		"\n%s\n",
		view.command("Resume, repair, or add another account at any time: corr setup"),
	)
	return err
}
