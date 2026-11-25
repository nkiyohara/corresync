package main

import (
	"context"

	"github.com/nkiyohara/corresync/internal/domain"
)

// activateAccount takes activationMu and activates one account, as the CLI
// login and the MCP-approved sign-in do.
func (backend *sessionBackend) activateAccount(
	ctx context.Context,
	accountID domain.AccountID,
) error {
	backend.activationMu.Lock()
	defer backend.activationMu.Unlock()
	_, err := backend.activateAccountHeld(ctx, accountID)
	return err
}
