package main

import (
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/domain"
)

// daemonMCPBackend forwards the MCP application boundary to the sole local
// session owner. Outlook credentials never enter the MCP stdio process.
type daemonMCPBackend struct {
	*daemonapi.Client
	defaultAccount domain.AccountID
	configuration  config.Config
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
	return &daemonMCPBackend{
		Client: client, defaultAccount: status.DefaultAccount, configuration: configuration,
	}, nil
}

func (backend *daemonMCPBackend) DefaultAccount() domain.AccountID {
	return backend.defaultAccount
}

func (backend *daemonMCPBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	_, account, err := backend.configuration.ResolveAccount(reference)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}
