package main

import (
	"context"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

// daemonMCPBackend forwards the MCP application boundary to the sole local
// session owner. Outlook credentials never enter the MCP stdio process.
type daemonMCPBackend struct {
	*daemonapi.Client
	defaultAccount domain.AccountID
	configuration  config.Config
	accounts       *application.AccountService
	discovery      *application.AccountDiscoveryService
}

func newDaemonMCPBackend(app *runtime) (*daemonMCPBackend, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return nil, err
	}
	client, status, err := app.openDaemon(app.context)
	if err != nil {
		return nil, err
	}
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &daemonMCPBackend{
		Client: client, defaultAccount: status.DefaultAccount, configuration: configuration,
		accounts: accounts, discovery: discoverer,
	}, nil
}

func (backend *daemonMCPBackend) DefaultAccount() domain.AccountID {
	return backend.defaultAccount
}

func (backend *daemonMCPBackend) DiscoverAccounts(
	ctx context.Context,
	address string,
) (application.AccountDiscoveryResult, error) {
	return backend.discovery.Discover(ctx, address)
}

func (backend *daemonMCPBackend) ListAccounts(
	ctx context.Context,
) (application.AccountCatalog, error) {
	return backend.accounts.List(ctx)
}

func (backend *daemonMCPBackend) ShowAccount(
	ctx context.Context,
	reference string,
) (application.AccountView, error) {
	return backend.accounts.Show(ctx, reference)
}

func (backend *daemonMCPBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	_, account, err := backend.configuration.ResolveAccount(reference)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}
