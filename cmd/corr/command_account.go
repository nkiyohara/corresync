package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type accountCommand struct {
	Discover accountDiscoverCommand `cmd:"" help:"Find explainable provider candidates without authenticating."`
	List     accountListCommand     `cmd:"" help:"List configured account routes."`
	Show     accountShowCommand     `cmd:"" help:"Show one configured account route."`
	Add      accountAddCommand      `cmd:"" help:"Add an explicitly selected route without authenticating."`
	Rename   accountRenameCommand   `cmd:"" help:"Rename an account while preserving its stable identity."`
	Remove   accountRemoveCommand   `cmd:"" help:"Remove an account and its Corresync-owned local state."`
}

type accountDiscoverCommand struct {
	Address string `arg:"" help:"Bare email address to inspect without credentials."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountListCommand struct {
	JSON bool `help:"Write machine-readable JSON."`
}

type accountShowCommand struct {
	Account string `arg:"" help:"Account alias or stable opaque ID."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountAddCommand struct {
	Address  string `arg:"" help:"Bare email address for the account."`
	Alias    string `help:"Local alias; defaults to the address local part."`
	Provider string `help:"Explicit provider override."`
	Origin   string `help:"Explicit HTTPS provider origin override."`
	Mailbox  string `help:"Optional mailbox identity when it differs from the address."`
	Default  bool   `help:"Make this the default account."`
	JSON     bool   `help:"Write machine-readable JSON."`
}

type accountRenameCommand struct {
	Account string `arg:"" help:"Account alias or stable opaque ID."`
	Alias   string `arg:"" help:"New local alias."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

type accountRemoveCommand struct {
	Account    string `arg:"" help:"Account alias or stable opaque ID."`
	NewDefault string `help:"Replacement alias when removing the default account."`
	Approve    bool   `help:"Confirm deletion of Corresync-owned local state."`
	JSON       bool   `help:"Write machine-readable JSON."`
}

type accountAddResult struct {
	Selected application.ProviderCandidate `json:"selected"`
	Account  application.AccountView       `json:"account"`
}

func (command *accountDiscoverCommand) Run(app *runtime) error {
	_, discoverer, err := app.accountServices()
	if err != nil {
		return err
	}
	result, err := discoverer.Discover(app.context, command.Address)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	return writeDiscoveryResult(app, result)
}

func (command *accountListCommand) Run(app *runtime) error {
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, catalog)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf("%s  %s\n\n", view.info(), view.strong("Accounts")); err != nil {
		return err
	}
	for _, account := range catalog.Accounts {
		marker := " "
		if account.IsDefault {
			marker = "*"
		}
		address := account.Address
		if address == "" {
			address = "address not set"
		}
		if _, err := view.printf(
			"  %s %-16s %-18s %s\n",
			marker,
			sanitizeCell(account.Alias, 64),
			account.Provider,
			view.muted(sanitizeCell(address, 254)),
		); err != nil {
			return err
		}
	}
	return nil
}

func (command *accountShowCommand) Run(app *runtime) error {
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Show(app.context, command.Account)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, account)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %s\n  %-10s %t\n",
		view.info(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)),
		"ID", account.ID,
		"Address", sanitizeCell(account.Address, 254),
		"Provider", account.Provider,
		"Origin", sanitizeCell(account.Origin, 2048),
		"Mailbox", sanitizeCell(account.Mailbox, 254),
		"Default", account.IsDefault,
	)
	return err
}

func (command *accountAddCommand) Run(app *runtime) error {
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		return err
	}
	result, err := discoverer.Discover(app.context, command.Address)
	if err != nil {
		return err
	}
	selected, err := selectAccountCandidate(result, command.Provider, command.Origin)
	if err != nil {
		return err
	}
	alias := command.Alias
	if alias == "" {
		alias = command.Address[:strings.LastIndexByte(command.Address, '@')]
	}
	origin := command.Origin
	if origin == "" {
		origin = candidateEndpoint(selected, "origin")
	}
	if origin == "" {
		return errors.New(
			"selected provider has no discovered origin; pass --origin with an explicit HTTPS origin",
		)
	}
	account, err := accounts.Add(app.context, application.AccountAddInput{
		Alias: alias, Address: result.Address, Provider: selected.Provider,
		Origin: origin, Mailbox: command.Mailbox, Default: command.Default,
	})
	if err != nil {
		return err
	}
	output := accountAddResult{Selected: selected, Account: account}
	if command.JSON {
		return writeJSON(app.stdout, output)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n\n  %-14s %s\n  %-14s %d/100\n  %-14s %s\n  %-14s %s\n\n%s  %s\n",
		view.info(),
		view.strong("Selected provider route"),
		"Provider", selected.Provider,
		"Confidence", selected.Confidence,
		"Authentication", selected.Authentication,
		"Origin", sanitizeCell(origin, 2048),
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added; authentication has not started"),
	)
	return err
}

func (command *accountRenameCommand) Run(app *runtime) error {
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Rename(app.context, application.AccountRenameInput{
		Account: command.Account, NewAlias: command.Alias,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, account)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Account renamed to "+sanitizeCell(account.Alias, 64)),
		view.muted(string(account.ID)+" remains unchanged"),
	)
	return err
}

func (command *accountRemoveCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New(
			"account removal deletes Corresync-owned local state; review the account and rerun with --approve",
		)
	}
	if err := app.requireDaemonStopped(); err != nil {
		return err
	}
	accounts, _, err := app.accountServices()
	if err != nil {
		return err
	}
	account, err := accounts.Remove(app.context, application.AccountRemoveInput{
		Account: command.Account, ReplacementDefault: command.NewDefault,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, map[string]any{"removed": true, "account": account})
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n",
		view.success(),
		view.strong("Removed account "+sanitizeCell(account.Alias, 64)+" and its local state"),
	)
	return err
}

func selectAccountCandidate(
	result application.AccountDiscoveryResult,
	providerOverride string,
	originOverride string,
) (application.ProviderCandidate, error) {
	if providerOverride != "" {
		provider := domain.ProviderID(providerOverride)
		if err := provider.Validate(); err != nil {
			return application.ProviderCandidate{}, err
		}
		for _, candidate := range result.Candidates {
			if candidate.Provider == provider {
				if !candidate.Available {
					return application.ProviderCandidate{}, fmt.Errorf(
						"provider %q was discovered but is not available in this build",
						provider,
					)
				}
				return candidate, nil
			}
		}
		if provider != domain.ProviderMicrosoftOWA {
			return application.ProviderCandidate{}, fmt.Errorf(
				"provider %q is not available in this build",
				provider,
			)
		}
		if originOverride == "" {
			return application.ProviderCandidate{}, errors.New(
				"manual provider selection requires --origin",
			)
		}
		if _, err := url.ParseRequestURI(originOverride); err != nil {
			return application.ProviderCandidate{}, errors.New("--origin is not a valid URI")
		}
		return application.ProviderCandidate{
			Provider: domain.ProviderMicrosoftOWA, Confidence: 0,
			Authentication:            application.DiscoveryBrowserFirstParty,
			RequiresExplicitSelection: true,
			Available:                 true,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "origin", Value: originOverride},
			},
			Evidence: []application.DiscoveryEvidence{
				{Source: "manual_override", Detail: result.Domain},
			},
		}, nil
	}
	for _, candidate := range result.Candidates {
		if candidate.Available && !candidate.RequiresExplicitSelection {
			return candidate, nil
		}
	}
	return application.ProviderCandidate{}, errors.New(
		"no automatically selectable provider is available; inspect `corr account discover` and pass --provider with an explicit endpoint",
	)
}

func candidateEndpoint(candidate application.ProviderCandidate, kind string) string {
	for _, endpoint := range candidate.Endpoints {
		if endpoint.Kind == kind {
			return endpoint.Value
		}
	}
	return ""
}

func writeDiscoveryResult(app *runtime, result application.AccountDiscoveryResult) error {
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n   %s\n",
		view.info(),
		view.strong("Provider candidates"),
		view.muted(result.Address+" · evidence only; no authentication performed"),
	); err != nil {
		return err
	}
	if len(result.Candidates) == 0 {
		_, err := view.printf(
			"\n  No candidate was inferred. Manual --provider and --origin remain available.\n",
		)
		return err
	}
	for _, candidate := range result.Candidates {
		availability := "planned"
		if candidate.Available {
			availability = "available"
		}
		if _, err := view.printf(
			"\n  %-18s %3d/100 · %s · %s\n",
			candidate.Provider,
			candidate.Confidence,
			candidate.Authentication,
			availability,
		); err != nil {
			return err
		}
		for _, evidence := range candidate.Evidence {
			if _, err := view.printf(
				"    %-20s %s\n",
				evidence.Source,
				sanitizeCell(evidence.Detail, 512),
			); err != nil {
				return err
			}
		}
		for _, endpoint := range candidate.Endpoints {
			if _, err := view.printf(
				"    %-20s %s\n",
				endpoint.Kind,
				sanitizeCell(endpoint.Value, 2048),
			); err != nil {
				return err
			}
		}
	}
	return nil
}
