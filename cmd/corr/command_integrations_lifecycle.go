package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
)

type integrationTargetFlags struct {
	Hosts       []string `arg:"" optional:"" name:"host" help:"Host IDs or compatibility aliases."`
	Name        string   `default:"corresync" help:"Client-side MCP server name."`
	Executable  string   `type:"path" help:"Corresync executable path; defaults to this process."`
	Scope       string   `default:"user" enum:"local,project,user,workspace" help:"Host configuration scope."`
	ProjectPath string   `name:"project" type:"path" help:"Explicit project root for project/workspace/local scope."`
}

type integrationMutationFlags struct {
	integrationTargetFlags
	JSON bool `help:"Write the preview as JSON without applying it."`
	Yes  bool `help:"Apply the displayed plan without an interactive confirmation."`
}

type integrationsPlanCommand struct {
	integrationTargetFlags
	JSON bool `help:"Write machine-readable JSON."`
}

type integrationsSetupCommand struct{ integrationMutationFlags }
type integrationsRepairCommand struct{ integrationMutationFlags }
type integrationsRemoveCommand struct{ integrationMutationFlags }

type integrationsDoctorCommand struct {
	integrationTargetFlags
	JSON bool `help:"Write machine-readable JSON."`
}

type integrationPlanReport struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Plans         []integrationlifecycle.Plan `json:"plans"`
}

type integrationDoctorItem struct {
	Host        agenthost.ID                    `json:"host"`
	DisplayName string                          `json:"displayName"`
	Inspection  integrationlifecycle.Inspection `json:"inspection"`
}

type integrationDoctorReport struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Hosts         []integrationDoctorItem `json:"hosts"`
}

type integrationApplyReport struct {
	SchemaVersion int                           `json:"schemaVersion"`
	Plans         []integrationlifecycle.Plan   `json:"plans"`
	Results       []integrationlifecycle.Result `json:"results"`
}

func (command *integrationsPlanCommand) Run(app *runtime) error {
	engine, requests, err := prepareIntegrationLifecycle(app, command.integrationTargetFlags, integrationlifecycle.OperationSetup, true)
	if err != nil {
		return err
	}
	plans, err := planIntegrations(app.context, engine, requests)
	if err != nil {
		return err
	}
	report := integrationPlanReport{SchemaVersion: integrationlifecycle.SchemaVersion, Plans: plans}
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	return writeIntegrationPlans(app, plans, false)
}

func (command *integrationsSetupCommand) Run(app *runtime) error {
	return runIntegrationMutation(app, command.integrationMutationFlags, integrationlifecycle.OperationSetup)
}

func (command *integrationsRepairCommand) Run(app *runtime) error {
	return runIntegrationMutation(app, command.integrationMutationFlags, integrationlifecycle.OperationRepair)
}

func (command *integrationsRemoveCommand) Run(app *runtime) error {
	return runIntegrationMutation(app, command.integrationMutationFlags, integrationlifecycle.OperationRemove)
}

func (command *integrationsDoctorCommand) Run(app *runtime) error {
	if len(command.Hosts) == 0 {
		for _, host := range app.agentHosts.Hosts() {
			if host.Lifecycle.Inspect {
				command.Hosts = append(command.Hosts, string(host.ID))
			}
		}
	}
	engine, requests, err := prepareIntegrationLifecycle(app, command.integrationTargetFlags, integrationlifecycle.OperationSetup, false)
	if err != nil {
		return err
	}
	report := integrationDoctorReport{SchemaVersion: integrationlifecycle.SchemaVersion}
	for _, request := range requests {
		inspection, inspectErr := engine.Inspect(app.context, request)
		if inspectErr != nil {
			return inspectErr
		}
		host, _ := app.agentHosts.Lookup(string(request.Host))
		report.Hosts = append(report.Hosts, integrationDoctorItem{
			Host: request.Host, DisplayName: host.DisplayName, Inspection: inspection,
		})
	}
	if command.JSON {
		return writeJSON(app.stdout, report)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf("%s  %s\n\n", view.info(), view.strong("Corresync integration health")); err != nil {
		return err
	}
	for _, item := range report.Hosts {
		if _, err := fmt.Fprintf(app.stdout, "  %-22s %-18s %s\n", item.DisplayName, item.Inspection.State, item.Inspection.Detail); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(app.stdout, "\nInspection read only the named registration or documented configuration entry; it did not read conversations, credentials, mail, or calendar data.")
	return err
}

func runIntegrationMutation(app *runtime, flags integrationMutationFlags, operation integrationlifecycle.Operation) error {
	_, err := runIntegrationMutationWithResults(app, flags, operation)
	return err
}

func runIntegrationMutationWithResults(
	app *runtime,
	flags integrationMutationFlags,
	operation integrationlifecycle.Operation,
) ([]integrationlifecycle.Result, error) {
	if flags.JSON && flags.Yes {
		return nil, errors.New("--json is preview-only and cannot be combined with --yes")
	}
	engine, requests, err := prepareIntegrationLifecycle(app, flags.integrationTargetFlags, operation, true)
	if err != nil {
		return nil, err
	}
	plans, err := planIntegrations(app.context, engine, requests)
	if err != nil {
		return nil, err
	}
	if flags.JSON {
		return nil, writeJSON(app.stdout, integrationApplyReport{
			SchemaVersion: integrationlifecycle.SchemaVersion, Plans: plans,
		})
	}
	if err := writeIntegrationPlans(app, plans, true); err != nil {
		return nil, err
	}
	if !plansNeedMutation(plans) {
		results := resultsForNoopPlans(plans)
		if err := writeIntegrationResults(app, results); err != nil {
			return nil, err
		}
		return results, errors.Join(integrationResultErrors(results)...)
	}
	if !flags.Yes {
		if !app.interactiveInput() || !app.interactiveStdout() {
			return nil, errors.New("integration changes require an interactive confirmation or explicit --yes")
		}
		confirmed, confirmErr := confirmIntegrationPlans(app, operation, len(plans))
		if confirmErr != nil {
			return nil, confirmErr
		}
		if !confirmed {
			results := resultsForSkippedPlans(plans)
			return results, writeIntegrationResults(app, results)
		}
	}
	results := make([]integrationlifecycle.Result, 0, len(plans))
	var failures []error
	for index := range plans {
		result, applyErr := engine.Apply(app.context, requests[index], plans[index])
		results = append(results, result)
		if applyErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", requests[index].Host, applyErr))
		} else if resultErr := integrationResultError(result); resultErr != nil {
			failures = append(failures, resultErr)
		}
	}
	if err := writeIntegrationResults(app, results); err != nil {
		return nil, err
	}
	return results, errors.Join(failures...)
}

func integrationResultErrors(results []integrationlifecycle.Result) []error {
	failures := make([]error, 0, len(results))
	for _, result := range results {
		if err := integrationResultError(result); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
}

func integrationResultError(result integrationlifecycle.Result) error {
	switch result.Status {
	case integrationlifecycle.ResultAppliedVerified, integrationlifecycle.ResultReloadRequired,
		integrationlifecycle.ResultAlreadyCurrent, integrationlifecycle.ResultAlreadyAbsent:
		return nil
	case integrationlifecycle.ResultSkipped:
		return fmt.Errorf("%s: integration change was skipped", result.Host)
	case integrationlifecycle.ResultBlocked, integrationlifecycle.ResultFailedPreserved,
		integrationlifecycle.ResultFailedChanged:
		return fmt.Errorf("%s: %s", result.Host, result.Message)
	}
	return fmt.Errorf("%s: integration returned unknown status %q", result.Host, result.Status)
}

func prepareIntegrationLifecycle(
	app *runtime,
	flags integrationTargetFlags,
	operation integrationlifecycle.Operation,
	requireAccount bool,
) (integrationlifecycle.Engine, []integrationlifecycle.Request, error) {
	if len(flags.Hosts) == 0 {
		return integrationlifecycle.Engine{}, nil, errors.New("select at least one host; run `corr integrations list`")
	}
	var name, executable string
	var arguments []string
	var err error
	if requireAccount {
		name, executable, arguments, err = resolveMCPSetup(app, flags.Name, flags.Executable)
	} else {
		name, executable, arguments, err = resolveMCPClientConfig(app, flags.Name, flags.Executable)
		if err == nil {
			executable, err = verifyIntegrationExecutable(executable)
		}
	}
	if err != nil {
		return integrationlifecycle.Engine{}, nil, err
	}
	home, err := app.userHomeDir()
	if err != nil {
		return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve user home: %w", err)
	}
	home, err = canonicalIntegrationPath(home, true)
	if err != nil {
		return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve user home: %w", err)
	}
	configDirectory, err := app.userConfigDir()
	if err != nil {
		return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	configDirectory, err = canonicalIntegrationPath(configDirectory, false)
	if err != nil {
		return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	projectPath := flags.ProjectPath
	scope := agenthost.Scope(flags.Scope)
	if scope != agenthost.ScopeUser {
		if projectPath == "" {
			projectPath, err = app.workingDirectory()
			if err != nil {
				return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve project directory: %w", err)
			}
		}
		projectPath, err = canonicalIntegrationPath(projectPath, true)
		if err != nil {
			return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve project directory: %w", err)
		}
	}
	requests := make([]integrationlifecycle.Request, 0, len(flags.Hosts))
	seen := make(map[agenthost.ID]bool, len(flags.Hosts))
	for _, value := range flags.Hosts {
		host, ok := app.agentHosts.Lookup(value)
		if !ok {
			return integrationlifecycle.Engine{}, nil, fmt.Errorf("unknown agent host %q; run `corr integrations list`", value)
		}
		if seen[host.ID] {
			continue
		}
		seen[host.ID] = true
		requests = append(requests, integrationlifecycle.Request{
			Operation: operation, Host: host.ID, Scope: scope, ServerName: name,
			Executable: executable, Arguments: append([]string(nil), arguments...),
			ProjectDirectory: projectPath,
		})
	}
	environment := integrationlifecycle.Environment{
		HomeDirectory: filepath.Clean(home), ConfigDirectory: filepath.Clean(configDirectory), GOOS: app.info.OS,
	}
	bundleDirectory, err := app.integrationBundleDirectory(executable)
	if err != nil {
		return integrationlifecycle.Engine{}, nil, fmt.Errorf("resolve native integration packages: %w", err)
	}
	if bundleDirectory != "" {
		environment.BundleDirectory = bundleDirectory
		environment.ManagedDirectory = filepath.Join(environment.ConfigDirectory, "corresync", "integration-packages")
	}
	return integrationlifecycle.Engine{
		Catalog: app.agentHosts, Executor: runtimeIntegrationExecutor{app: app}, Environment: environment,
	}, requests, nil
}

func canonicalIntegrationPath(value string, requireExisting bool) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	candidate := absolute
	var suffix []string
	for {
		_, statErr := os.Lstat(candidate)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(candidate)
			if resolveErr != nil {
				return "", resolveErr
			}
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		if requireExisting && candidate == absolute {
			return "", statErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", statErr
		}
		suffix = append([]string{filepath.Base(candidate)}, suffix...)
		candidate = parent
	}
}

func findIntegrationBundleDirectory(executable string) (string, error) {
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(resolvedExecutable)
	prefix := filepath.Dir(directory)
	candidates := []string{
		directory,
		prefix,
		filepath.Join(prefix, "share", "corresync"),
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		valid := true
		for _, relative := range []string{
			"plugins/corresync/skills/corresync/SKILL.md",
			"plugins/corresync/.codex-plugin/plugin.json",
			"integrations/gemini-cli/corresync/gemini-extension.json",
		} {
			info, statErr := os.Lstat(filepath.Join(candidate, filepath.FromSlash(relative)))
			if statErr != nil || !info.Mode().IsRegular() || integrationlifecycle.IsSymlinkOrReparsePoint(info) {
				valid = false
				break
			}
		}
		if valid {
			resolved, resolveErr := filepath.EvalSymlinks(candidate)
			if resolveErr != nil {
				return "", resolveErr
			}
			return filepath.Clean(resolved), nil
		}
	}
	return "", nil
}

func verifyIntegrationExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("configured executable path is not clean and absolute: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Corresync executable %s: %w", path, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve Corresync executable %s: %w", path, err)
	}
	resolved = filepath.Clean(resolved)
	if err := verifyIntegrationExecutableParents(resolved); err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect Corresync executable %s: %w", resolved, err)
	}
	if !info.Mode().IsRegular() || integrationlifecycle.IsSymlinkOrReparsePoint(info) {
		return "", fmt.Errorf("configured executable is not a regular file: %s", resolved)
	}
	if !integrationlifecycle.OwnedByCurrentUserOrRoot(info) {
		return "", fmt.Errorf("configured executable is not owned by the current user or root: %s", resolved)
	}
	if integrationlifecycle.WritableByOtherUsers(info) || !integrationlifecycle.ExecutableByUser(info) {
		return "", fmt.Errorf("configured executable has unsafe write or execute permissions: %s", resolved)
	}
	return resolved, nil
}

func verifyIntegrationExecutableParents(path string) error {
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect Corresync executable parent %s: %w", directory, err)
		}
		if !info.IsDir() || integrationlifecycle.IsSymlinkOrReparsePoint(info) {
			return fmt.Errorf("corresync executable parent has an unsafe type: %s", directory)
		}
		if !integrationlifecycle.OwnedByCurrentUserOrRoot(info) {
			return fmt.Errorf("corresync executable parent is not owned by the current user or root: %s", directory)
		}
		if integrationlifecycle.WritableByOtherUsers(info) && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("corresync executable parent is writable by another user: %s", directory)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil
		}
	}
}

func planIntegrations(ctx context.Context, engine integrationlifecycle.Engine, requests []integrationlifecycle.Request) ([]integrationlifecycle.Plan, error) {
	plans := make([]integrationlifecycle.Plan, 0, len(requests))
	for _, request := range requests {
		plan, err := engine.Plan(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("plan %s integration: %w", request.Host, err)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func writeIntegrationPlans(app *runtime, plans []integrationlifecycle.Plan, mutation bool) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	title := "Integration plan"
	if len(plans) > 1 {
		title = "Integration plans"
	}
	if _, err := view.printf("%s  %s\n", view.info(), view.strong(title)); err != nil {
		return err
	}
	for _, plan := range plans {
		if _, err := fmt.Fprintf(app.stdout, "\n%s (%s) · %s · %s\n", plan.DisplayName, plan.Host, plan.Operation, plan.Previous.State); err != nil {
			return err
		}
		if plan.Previous.Path != "" {
			if _, err := fmt.Fprintf(app.stdout, "  target  %s\n", plan.Previous.Path); err != nil {
				return err
			}
		}
		if plan.Blocked {
			if _, err := fmt.Fprintf(app.stdout, "  blocked %s\n", plan.Reason); err != nil {
				return err
			}
			continue
		}
		if len(plan.Actions) == 0 {
			if _, err := fmt.Fprintln(app.stdout, "  change  none"); err != nil {
				return err
			}
		}
		for _, action := range plan.Actions {
			switch {
			case action.Command != nil:
				directory := ""
				if action.Command.WorkingDirectory != "" {
					directory = " (in " + action.Command.WorkingDirectory + ")"
				}
				if _, err := fmt.Fprintf(app.stdout, "  change  %s%s\n", formatCommand(action.Command.Executable, action.Command.Arguments), directory); err != nil {
					return err
				}
			case action.File != nil:
				format := "configuration"
				switch action.Kind {
				case integrationlifecycle.ActionJSON:
					format = "JSON/JSONC"
				case integrationlifecycle.ActionYAML:
					format = "YAML"
				case integrationlifecycle.ActionCommand, integrationlifecycle.ActionPackage, integrationlifecycle.ActionSkill:
				}
				normalization := "formatting may normalize"
				if action.Kind == integrationlifecycle.ActionJSON {
					normalization = "comments will be removed and formatting normalized"
				}
				if _, err := fmt.Fprintf(app.stdout, "  change  merge entry %q in %s (%s %s; recovery copy retained)\n", action.File.Entry, action.File.Path, format, normalization); err != nil {
					return err
				}
			case action.Package != nil:
				if _, err := fmt.Fprintf(app.stdout, "  change  stage %s package %s at %s from reviewed local assets\n", action.Package.Kind, action.Package.Version, action.Package.Target); err != nil {
					return err
				}
			}
		}
		if plan.ReloadRequired {
			if _, err := fmt.Fprintln(app.stdout, "  reload  new session or host reload required"); err != nil {
				return err
			}
		}
	}
	if mutation {
		_, err := fmt.Fprintln(app.stdout, "\nHosts are independent: a later failure does not undo an earlier verified host.")
		return err
	}
	return nil
}

func plansNeedMutation(plans []integrationlifecycle.Plan) bool {
	for _, plan := range plans {
		if !plan.Blocked && len(plan.Actions) > 0 {
			return true
		}
	}
	return false
}

func resultsForNoopPlans(plans []integrationlifecycle.Plan) []integrationlifecycle.Result {
	results := make([]integrationlifecycle.Result, 0, len(plans))
	for _, plan := range plans {
		status := integrationlifecycle.ResultAlreadyCurrent
		message := "The integration is already current."
		if plan.Blocked {
			status = integrationlifecycle.ResultBlocked
			message = plan.Reason
		} else if plan.Operation == integrationlifecycle.OperationRemove || plan.Previous.State == integrationlifecycle.StateAbsent {
			status = integrationlifecycle.ResultAlreadyAbsent
			message = "The integration is already absent."
		}
		results = append(results, integrationlifecycle.Result{Host: plan.Host, Status: status, Verified: !plan.Blocked, Message: message})
	}
	return results
}

func resultsForSkippedPlans(plans []integrationlifecycle.Plan) []integrationlifecycle.Result {
	results := make([]integrationlifecycle.Result, 0, len(plans))
	for _, plan := range plans {
		status := integrationlifecycle.ResultSkipped
		message := "The user skipped this integration change."
		if plan.Blocked {
			status = integrationlifecycle.ResultBlocked
			message = plan.Reason
		} else if len(plan.Actions) == 0 {
			status = integrationlifecycle.ResultAlreadyCurrent
			message = "The integration is already current."
		}
		results = append(results, integrationlifecycle.Result{Host: plan.Host, Status: status, Message: message})
	}
	return results
}

func confirmIntegrationPlans(app *runtime, operation integrationlifecycle.Operation, count int) (bool, error) {
	confirmed := false
	restoreFallback := prepareAccessibleFieldFallback(app, "n\n")
	defer restoreFallback()
	affirmative := "Apply changes"
	if operation == integrationlifecycle.OperationRemove {
		affirmative = "Remove integrations"
	}
	form := settingsForm(app, huh.NewConfirm().
		Title(fmt.Sprintf("Apply %s plan to %d host(s)?", operation, count)).
		Description("Only the displayed host registrations and Corresync-owned entries will change. Esc cancels.").
		Affirmative(affirmative).
		Negative("Cancel").
		Value(&confirmed))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("confirm integration changes: %w", err)
	}
	if err := app.context.Err(); err != nil {
		return false, err
	}
	if settingsInputExhausted(app) {
		return false, nil
	}
	return confirmed, nil
}

func writeIntegrationResults(app *runtime, results []integrationlifecycle.Result) error {
	if _, err := fmt.Fprintln(app.stdout, "\nIntegration results"); err != nil {
		return err
	}
	for _, result := range results {
		if _, err := fmt.Fprintf(app.stdout, "  %-20s %-34s %s\n", result.Host, result.Status, result.Message); err != nil {
			return err
		}
	}
	return nil
}

type runtimeIntegrationExecutor struct{ app *runtime }

func (executor runtimeIntegrationExecutor) Run(ctx context.Context, command integrationlifecycle.Command, limit int64) (integrationlifecycle.Execution, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var output boundedBuffer
	output.remaining = limit
	var err error
	if command.WorkingDirectory == "" {
		err = executor.app.runIntegrationCommand(bounded, &output, &output, command.Executable, command.Arguments...)
	} else {
		err = executor.app.runIntegrationDirectoryCommand(
			bounded, &output, &output, command.WorkingDirectory, command.Executable, command.Arguments...,
		)
	}
	execution := integrationlifecycle.Execution{Started: true, Output: output.Bytes(), Truncated: output.truncated}
	if err == nil {
		return execution, nil
	}
	if bounded.Err() != nil {
		return execution, bounded.Err()
	}
	var executableError *exec.Error
	if errors.As(err, &executableError) || errors.Is(err, os.ErrNotExist) {
		execution.Started = false
	}
	execution.ExitCode = 1
	return execution, nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int64
	truncated bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if buffer.remaining <= 0 {
		buffer.truncated = true
		return original, nil
	}
	if int64(len(value)) > buffer.remaining {
		buffer.truncated = true
		value = value[:buffer.remaining]
	}
	_, _ = buffer.buffer.Write(value)
	buffer.remaining -= int64(len(value))
	return original, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return append([]byte(nil), buffer.buffer.Bytes()...) }

var _ io.Writer = (*boundedBuffer)(nil)
