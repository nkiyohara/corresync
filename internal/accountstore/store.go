// Package accountstore adapts secret-free local configuration and state to the
// account lifecycle application ports.
package accountstore

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

// Store persists account definitions in one explicit config file.
type Store struct {
	ConfigPath string
}

// ListAccounts returns a detached account catalog.
func (store Store) ListAccounts(context.Context) (application.AccountCatalog, error) {
	configuration, err := config.Load(store.ConfigPath)
	if err != nil {
		return application.AccountCatalog{}, err
	}
	accounts := make([]application.AccountView, 0, len(configuration.Accounts))
	for alias, account := range configuration.Accounts {
		web, _ := account.OutlookWeb()
		accounts = append(accounts, application.AccountView{
			ID: account.ID, Alias: alias, Address: account.Address,
			Provider: account.PrimaryProvider(), Origin: web.Origin, Mailbox: web.Mailbox,
			IsDefault: alias == configuration.DefaultAccount,
		})
	}
	return application.AccountCatalog{Accounts: accounts}, nil
}

// AddAccount atomically rejects stale alias/ID conflicts before saving.
func (store Store) AddAccount(ctx context.Context, account application.AccountView) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		if len(configuration.Accounts) >= 32 {
			return errors.New("at most 32 accounts are supported")
		}
		if _, exists := configuration.Accounts[account.Alias]; exists {
			return fmt.Errorf("account alias %q already exists", account.Alias)
		}
		for alias, existing := range configuration.Accounts {
			if existing.ID == account.ID {
				return fmt.Errorf("account ID already belongs to alias %q", alias)
			}
		}
		if account.Provider != domain.ProviderMicrosoftOWA {
			return fmt.Errorf("provider %q needs an explicit route definition", account.Provider)
		}
		web := config.OutlookWebRoute{Origin: account.Origin, Mailbox: account.Mailbox}
		configuration.Accounts[account.Alias] = config.Account{
			ID: account.ID, Address: account.Address,
			Mail: &config.MailRoute{
				Provider: domain.ProviderMicrosoftOWA, OutlookWeb: &web,
			},
			Calendar: &config.CalendarRoute{
				Provider: domain.ProviderMicrosoftOWA,
				OutlookWeb: &config.OutlookWebRoute{
					Origin: account.Origin, Mailbox: account.Mailbox,
				},
			},
		}
		if account.IsDefault {
			configuration.DefaultAccount = account.Alias
		}
		return nil
	})
}

// RenameAccount atomically changes only the mutable alias.
func (store Store) RenameAccount(
	ctx context.Context,
	accountID domain.AccountID,
	newAlias string,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		if _, exists := configuration.Accounts[newAlias]; exists {
			return fmt.Errorf("account alias %q already exists", newAlias)
		}
		oldAlias, account, exists := configuration.AccountByID(accountID)
		if !exists {
			return fmt.Errorf("account ID %q is not configured", accountID)
		}
		delete(configuration.Accounts, oldAlias)
		configuration.Accounts[newAlias] = account
		if configuration.DefaultAccount == oldAlias {
			configuration.DefaultAccount = newAlias
		}
		return nil
	})
}

// RemoveAccount atomically deletes one route and updates the default.
func (store Store) RemoveAccount(
	ctx context.Context,
	accountID domain.AccountID,
	replacementDefault string,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		alias, _, exists := configuration.AccountByID(accountID)
		if !exists {
			return fmt.Errorf("account ID %q is not configured", accountID)
		}
		if len(configuration.Accounts) == 1 {
			return errors.New("cannot remove the only configured account")
		}
		if alias == configuration.DefaultAccount {
			if replacementDefault == "" || replacementDefault == alias {
				return errors.New("removing the default account requires a different replacement")
			}
			if _, exists := configuration.Accounts[replacementDefault]; !exists {
				return fmt.Errorf(
					"replacement default account %q is not configured",
					replacementDefault,
				)
			}
			configuration.DefaultAccount = replacementDefault
		} else if replacementDefault != "" {
			return errors.New("replacement default is valid only for the default account")
		}
		delete(configuration.Accounts, alias)
		return nil
	})
}

// PurgeAccountState removes only Corresync-owned state derived from the stable
// opaque account ID. It refuses symlinked roots rather than following them.
func (Store) PurgeAccountState(_ context.Context, accountID domain.AccountID) error {
	if err := accountID.ValidateOpaque(); err != nil {
		return err
	}
	profile, err := paths.ProfileDir(accountID)
	if err != nil {
		return err
	}
	accountState, err := paths.AccountStateDir(accountID)
	if err != nil {
		return err
	}
	for _, target := range []string{profile, accountState} {
		if err := removeOwnedTree(target); err != nil {
			return err
		}
	}
	return nil
}

func removeOwnedTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect account state: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("account state path %q is not a directory", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove account state: %w", err)
	}
	return nil
}
