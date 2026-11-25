package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type integrationsCommand struct {
	Detect integrationsDetectCommand `cmd:"" help:"Find AI agents installed on this computer without running them."`
	List   integrationsListCommand   `cmd:"" help:"List the AI agents Corresync knows how to connect to."`
	Show   integrationsShowCommand   `cmd:"" help:"Show one AI agent and the ways Corresync connects to it."`
	Plan   integrationsPlanCommand   `cmd:"" help:"Show what setup would change for one or more agents."`
	Setup  integrationsSetupCommand  `cmd:"" help:"Connect Corresync to one or more agents and check the result."`
	Doctor integrationsDoctorCommand `cmd:"" help:"Check how Corresync is connected to agents, changing nothing."`
	Repair integrationsRepairCommand `cmd:"" help:"Fix outdated Corresync entries in an agent's settings."`
	Remove integrationsRemoveCommand `cmd:"" help:"Remove Corresync's entries from an agent's settings."`
}

func (*integrationsCommand) Help() string {
	return `An integration adds Corresync to an AI agent, such as Claude Code or Codex,
as an MCP server, and where the agent supports it, as a skill or plugin.
Setup, repair, and remove first show what they will change and ask before
changing anything; they touch only entries Corresync added. Each agent is
handled on its own, so a failure with one does not undo another.

Examples:
  corr integrations detect
  corr integrations plan claude-code
  corr integrations setup claude-code codex`
}

type integrationsDetectCommand struct {
	Refresh bool `help:"Look again instead of reusing the result from earlier in this run."`
	JSON    bool `help:"Write machine-readable JSON."`
}

type integrationsListCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type integrationsShowCommand struct {
	Host string `arg:"" name:"host" help:"Agent ID or alias, as shown by corr integrations list."`
	JSON bool   `help:"Write machine-readable JSON."`
}

type integrationCatalogReport struct {
	SchemaVersion int              `json:"schemaVersion"`
	Hosts         []agenthost.Host `json:"hosts"`
}

type integrationHostReport struct {
	SchemaVersion int            `json:"schemaVersion"`
	Host          agenthost.Host `json:"host"`
}

func (command *integrationsDetectCommand) Run(app *runtime) error {
	report, err := app.detectAgentHosts(app.context, agenthost.Request{Refresh: command.Refresh})
	if command.JSON {
		if writeErr := writeJSON(app.stdout, report); writeErr != nil {
			return writeErr
		}
		return err
	}
	if writeErr := writeAgentHostDetection(app, report); writeErr != nil {
		return writeErr
	}
	return err
}

func (command *integrationsListCommand) Run(app *runtime) error {
	hosts := app.agentHosts.Hosts()
	if command.JSON {
		return writeJSON(app.stdout, integrationCatalogReport{SchemaVersion: 1, Hosts: hosts})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf("%s  %s\n\n", view.info(), view.strong("AI agents Corresync can connect to")); err != nil {
		return err
	}
	for _, host := range hosts {
		if _, err := fmt.Fprintf(
			app.stdout,
			"  %-22s %-20s %-18s %s\n",
			host.DisplayName,
			"("+string(host.ID)+")",
			supportLabel(host.Support),
			capabilitySummary(host),
		); err != nil {
			return err
		}
	}
	_, err := view.printf("\n%s\n", view.nextStep(shellCommand("corr", "integrations", "show", "HOST")))
	return err
}

func supportLabel(support agenthost.Support) string {
	switch support {
	case agenthost.SupportVerified:
		return "verified"
	case agenthost.SupportExperimental:
		return "experimental"
	case agenthost.SupportConfigOnly:
		return "configuration only"
	case agenthost.SupportCatalogOnly:
		return "listed only, no setup"
	default:
		return string(support)
	}
}

func (command *integrationsShowCommand) Run(app *runtime) error {
	host, ok := app.agentHosts.Lookup(command.Host)
	if !ok {
		return unknownAgentHostError(command.Host)
	}
	if command.JSON {
		return writeJSON(app.stdout, integrationHostReport{SchemaVersion: 1, Host: host})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	aliases := "none"
	if len(host.Aliases) > 0 {
		aliases = strings.Join(host.Aliases, ", ")
	}
	if _, err := view.printf(
		"%s  %s\n\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n",
		view.info(),
		view.strong(host.DisplayName),
		"ID", host.ID,
		"Aliases", aliases,
		"Surfaces", joinSurfaces(host.Surfaces),
		"Support", supportLabel(host.Support),
		"Connects through", capabilitySummary(host),
		"Detected by", joinEvidenceKinds(host.Detection.EvidenceKinds),
		"In a marketplace", yesNoLabel(host.Capabilities.MarketplaceSurface),
		"Published", yesNoLabel(host.Capabilities.Published),
		"corr can", lifecycleLabel(host.Lifecycle),
		"Documentation", host.DocumentationURL,
	); err != nil {
		return err
	}
	if !host.Lifecycle.Setup {
		return nil
	}
	_, err := view.printf("\n%s\n", view.nextStep(shellCommand("corr", "integrations", "plan", string(host.ID))))
	return err
}

func unknownAgentHostError(name string) error {
	return withHint(
		fmt.Errorf("corr does not know an AI agent called %q", name),
		shellCommand("corr", "integrations", "list"),
	)
}

func writeAgentHostDetection(app *runtime, report agenthost.Report) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n%s · %s/%s · %s\n\n",
		view.info(),
		view.strong("AI agents on this computer"),
		report.Context.Kind,
		report.Context.OS,
		report.Context.Arch,
		detectionCacheLabel(report.Cache),
	); err != nil {
		return err
	}
	next := ""
	for _, item := range report.Hosts {
		location := ""
		if len(item.Evidence) > 0 {
			location = " · " + item.Evidence[0].Source + " " + strconv.Quote(item.Evidence[0].Location)
		}
		if _, err := fmt.Fprintf(
			app.stdout,
			"  %-22s %-20s %-19s %-24s %-22s%s\n",
			item.Host.DisplayName,
			"("+string(item.Host.ID)+")",
			detectionStatusLabel(item.Status),
			connectionStatusLabel(item.ConnectionStatus),
			supportLabel(item.Host.Support),
			location,
		); err != nil {
			return err
		}
		if next == "" && item.Status == agenthost.StatusConfirmed && item.Host.Lifecycle.Setup {
			next = shellCommand("corr", "integrations", "plan", string(item.Host.ID))
		}
	}
	if len(report.Problems) > 0 {
		if _, err := fmt.Fprintf(app.stdout, "\nDetection finished with %d warning(s); some agents may be missing from the list.\n", len(report.Problems)); err != nil {
			return err
		}
	}
	if report.Failure != nil {
		_, err := fmt.Fprintf(app.stdout, "\nDetection did not finish (%s); the list above is incomplete.\n", report.Failure.Code)
		return err
	}
	if _, err := fmt.Fprintln(
		app.stdout,
		"\nDetection only looked for installed agents and ran none of them. It did not check whether Corresync is already connected; plan does that.",
	); err != nil {
		return err
	}
	if next == "" {
		return nil
	}
	_, err := view.printf("%s\n", view.nextStep(next))
	return err
}

func detectionStatusLabel(status agenthost.DetectionStatus) string {
	switch status {
	case agenthost.StatusConfirmed:
		return "installed"
	case agenthost.StatusProbable:
		return "probably installed"
	case agenthost.StatusSelectedMissing:
		return "not found"
	case agenthost.StatusNotFound:
		return "not installed"
	case agenthost.StatusUnsupportedSurface:
		return "not supported here"
	default:
		return string(status)
	}
}

func connectionStatusLabel(status agenthost.ConnectionStatus) string {
	switch status {
	case agenthost.ConnectionNotInspected:
		return "connection not checked"
	case agenthost.ConnectionNotDetected:
		return "-"
	case agenthost.ConnectionUnsupported:
		return "cannot be connected"
	default:
		return string(status)
	}
}

func detectionCacheLabel(cache agenthost.CacheStatus) string {
	switch cache {
	case agenthost.CacheFresh:
		return "just checked"
	case agenthost.CacheRefreshed:
		return "checked again"
	case agenthost.CacheHit:
		return "result from earlier in this run"
	default:
		return string(cache)
	}
}

func capabilitySummary(host agenthost.Host) string {
	items := make([]string, 0, 3)
	if host.Capabilities.LocalStdioMCP {
		items = append(items, "local MCP")
	}
	if host.Capabilities.AgentSkill {
		items = append(items, "Skill")
	}
	if host.Capabilities.NativePackage != "" {
		items = append(items, string(host.Capabilities.NativePackage))
	}
	if len(items) == 0 {
		return "catalog only"
	}
	return strings.Join(items, " + ")
}

func joinSurfaces(surfaces []agenthost.Surface) string {
	items := make([]string, len(surfaces))
	for index := range surfaces {
		items[index] = string(surfaces[index])
	}
	return strings.Join(items, ", ")
}

func joinEvidenceKinds(kinds []agenthost.EvidenceKind) string {
	if len(kinds) == 0 {
		return "none"
	}
	items := make([]string, len(kinds))
	for index := range kinds {
		items[index] = string(kinds[index])
	}
	return strings.Join(items, ", ")
}

func yesNoLabel(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func lifecycleLabel(lifecycle agenthost.Lifecycle) string {
	if lifecycle.Setup {
		return "set up, check, repair, and remove"
	}
	if lifecycle.AdapterID != "" {
		return "detect; setup is planned"
	}
	return "detect only"
}
