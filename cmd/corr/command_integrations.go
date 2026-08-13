package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type integrationsCommand struct {
	Detect integrationsDetectCommand `cmd:"" help:"Detect installed local agent hosts without executing them."`
	List   integrationsListCommand   `cmd:"" help:"List the declarative agent-host catalog."`
	Show   integrationsShowCommand   `cmd:"" help:"Show one host and its supported integration surfaces."`
	Plan   integrationsPlanCommand   `cmd:"" help:"Inspect and preview setup for one or more hosts."`
	Setup  integrationsSetupCommand  `cmd:"" help:"Preview, confirm, apply, and verify one or more hosts."`
	Doctor integrationsDoctorCommand `cmd:"" help:"Inspect Corresync integration state without changing it."`
	Repair integrationsRepairCommand `cmd:"" help:"Preview and repair stale Corresync-owned fields."`
	Remove integrationsRemoveCommand `cmd:"" help:"Preview and remove only Corresync-owned entries."`
}

type integrationsDetectCommand struct {
	Refresh bool `help:"Bypass the in-process detection cache."`
	JSON    bool `help:"Write machine-readable JSON."`
}

type integrationsListCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type integrationsShowCommand struct {
	Host string `arg:"" name:"host" help:"Stable host ID or compatibility alias."`
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
	if _, err := view.printf("%s  %s\n\n", view.info(), view.strong("Agent host catalog")); err != nil {
		return err
	}
	for _, host := range hosts {
		if _, err := fmt.Fprintf(
			app.stdout,
			"  %-22s %-20s %-18s %s\n",
			host.DisplayName,
			"("+string(host.ID)+")",
			host.Support,
			capabilitySummary(host),
		); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(app.stdout, "\nRun `corr integrations show HOST` for exact surfaces and support status.")
	return err
}

func (command *integrationsShowCommand) Run(app *runtime) error {
	host, ok := app.agentHosts.Lookup(command.Host)
	if !ok {
		return fmt.Errorf("unknown agent host %q; run `corr integrations list`", command.Host)
	}
	if command.JSON {
		return writeJSON(app.stdout, integrationHostReport{SchemaVersion: 1, Host: host})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	aliases := "none"
	if len(host.Aliases) > 0 {
		aliases = strings.Join(host.Aliases, ", ")
	}
	_, err := view.printf(
		"%s  %s\n\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n  %-20s %s\n",
		view.info(),
		view.strong(host.DisplayName),
		"ID", host.ID,
		"Aliases", aliases,
		"Surfaces", joinSurfaces(host.Surfaces),
		"Support", host.Support,
		"Capabilities", capabilitySummary(host),
		"Detection evidence", joinEvidenceKinds(host.Detection.EvidenceKinds),
		"Marketplace surface", yesNoLabel(host.Capabilities.MarketplaceSurface),
		"Published", yesNoLabel(host.Capabilities.Published),
		"Lifecycle", lifecycleLabel(host.Lifecycle),
		"Documentation", host.DocumentationURL,
	)
	return err
}

func writeAgentHostDetection(app *runtime, report agenthost.Report) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n%s · %s/%s · %s\n\n",
		view.info(),
		view.strong("Local agent hosts"),
		report.Context.Kind,
		report.Context.OS,
		report.Context.Arch,
		report.Cache,
	); err != nil {
		return err
	}
	for _, item := range report.Hosts {
		location := ""
		if len(item.Evidence) > 0 {
			location = " · " + item.Evidence[0].Source + " " + strconv.Quote(item.Evidence[0].Location)
		}
		if _, err := fmt.Fprintf(
			app.stdout,
			"  %-22s %-20s %-19s %-16s %-16s%s\n",
			item.Host.DisplayName,
			"("+string(item.Host.ID)+")",
			item.Status,
			item.ConnectionStatus,
			item.Host.Support,
			location,
		); err != nil {
			return err
		}
	}
	if len(report.Problems) > 0 {
		if _, err := fmt.Fprintf(app.stdout, "\nDetection completed with %d bounded probe warning(s).\n", len(report.Problems)); err != nil {
			return err
		}
	}
	if report.Failure != nil {
		_, err := fmt.Fprintf(app.stdout, "\nDetection incomplete: %s.\n", report.Failure.Code)
		return err
	}
	_, err := fmt.Fprintln(
		app.stdout,
		"\nDetection is read-only. Connection state is not inspected until a lifecycle plan is explicitly requested.",
	)
	return err
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
		return "setup + inspect + verify + repair + remove"
	}
	if lifecycle.AdapterID != "" {
		return "planned adapter"
	}
	return "detect only"
}
