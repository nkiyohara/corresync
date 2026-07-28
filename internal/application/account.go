package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
)

// AccountView is the secret-free account lifecycle contract shared by CLI and
// MCP. Alias is mutable; ID is the stable isolation boundary.
type AccountView struct {
	ID        domain.AccountID  `json:"id"`
	Alias     string            `json:"alias"`
	Address   string            `json:"address,omitempty"`
	Provider  domain.ProviderID `json:"provider"`
	Origin    string            `json:"origin"`
	Mailbox   string            `json:"mailbox,omitempty"`
	IsDefault bool              `json:"isDefault"`
}

// AccountCatalog is a deterministic snapshot of configured accounts.
type AccountCatalog struct {
	Accounts []AccountView `json:"accounts"`
}

// AccountAddInput explicitly selects the candidate to persist. Discovery never
// writes this configuration or starts authentication.
type AccountAddInput struct {
	Alias    string            `json:"alias"`
	Address  string            `json:"address"`
	Provider domain.ProviderID `json:"provider"`
	Origin   string            `json:"origin"`
	Mailbox  string            `json:"mailbox,omitempty"`
	Default  bool              `json:"default"`
}

// AccountRenameInput changes only the human-facing alias.
type AccountRenameInput struct {
	Account  string `json:"account"`
	NewAlias string `json:"newAlias"`
}

// AccountRemoveInput selects a replacement default when necessary.
type AccountRemoveInput struct {
	Account            string `json:"account"`
	ReplacementDefault string `json:"replacementDefault,omitempty"`
}

// AccountRepository persists secret-free account definitions. Implementations
// must perform each mutation atomically against the latest complete config.
type AccountRepository interface {
	ListAccounts(context.Context) (AccountCatalog, error)
	AddAccount(context.Context, AccountView) error
	RenameAccount(context.Context, domain.AccountID, string) error
	RemoveAccount(context.Context, domain.AccountID, string) error
}

// AccountStatePurger removes Corresync-owned per-account state. It never reads
// or removes credentials owned by another application.
type AccountStatePurger interface {
	PurgeAccountState(context.Context, domain.AccountID) error
}

type accountIDGenerator func() (domain.AccountID, error)

// AccountService owns account lifecycle validation and isolation semantics.
type AccountService struct {
	repository AccountRepository
	purger     AccountStatePurger
	available  map[domain.ProviderID]struct{}
	newID      accountIDGenerator
}

// NewAccountService creates the shared lifecycle use case.
func NewAccountService(
	repository AccountRepository,
	purger AccountStatePurger,
	available []domain.ProviderID,
) (*AccountService, error) {
	if repository == nil {
		return nil, errors.New("account repository is required")
	}
	if purger == nil {
		return nil, errors.New("account state purger is required")
	}
	providers := make(map[domain.ProviderID]struct{}, len(available))
	for _, provider := range available {
		if err := provider.Validate(); err != nil {
			return nil, err
		}
		providers[provider] = struct{}{}
	}
	return &AccountService{
		repository: repository,
		purger:     purger,
		available:  providers,
		newID:      domain.NewAccountID,
	}, nil
}

// List returns a stable alias-ordered account snapshot.
func (service *AccountService) List(ctx context.Context) (AccountCatalog, error) {
	catalog, err := service.repository.ListAccounts(ctx)
	if err != nil {
		return AccountCatalog{}, fmt.Errorf("list accounts: %w", err)
	}
	if err := validateAccountCatalog(catalog); err != nil {
		return AccountCatalog{}, fmt.Errorf("validate account repository: %w", err)
	}
	slices.SortFunc(catalog.Accounts, func(left, right AccountView) int {
		return strings.Compare(left.Alias, right.Alias)
	})
	return catalog, nil
}

// Show resolves either a mutable alias or stable opaque ID.
func (service *AccountService) Show(
	ctx context.Context,
	reference string,
) (AccountView, error) {
	if reference == "" {
		return AccountView{}, errors.New("account reference is required")
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, account := range catalog.Accounts {
		if account.Alias == reference || string(account.ID) == reference {
			return account, nil
		}
	}
	return AccountView{}, fmt.Errorf("account %q is not configured", reference)
}

// Add persists one explicit provider selection without authenticating.
func (service *AccountService) Add(
	ctx context.Context,
	input AccountAddInput,
) (AccountView, error) {
	if err := domain.AccountAlias(input.Alias).Validate(); err != nil {
		return AccountView{}, err
	}
	normalizedAddress, _, err := normalizeDiscoveryAddress(input.Address)
	if err != nil {
		return AccountView{}, err
	}
	if err := input.Provider.Validate(); err != nil {
		return AccountView{}, err
	}
	if _, available := service.available[input.Provider]; !available {
		return AccountView{}, fmt.Errorf(
			"provider %q is not available in this build",
			input.Provider,
		)
	}
	if err := validateAccountOrigin(input.Origin); err != nil {
		return AccountView{}, err
	}
	if input.Mailbox != "" {
		if _, _, err := normalizeDiscoveryAddress(input.Mailbox); err != nil {
			return AccountView{}, errors.New("mailbox must be one bare email address")
		}
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, existing := range catalog.Accounts {
		if existing.Alias == input.Alias {
			return AccountView{}, fmt.Errorf("account alias %q already exists", input.Alias)
		}
	}
	accountID, err := service.newID()
	if err != nil {
		return AccountView{}, fmt.Errorf("generate account ID: %w", err)
	}
	account := AccountView{
		ID: accountID, Alias: input.Alias, Address: normalizedAddress,
		Provider: input.Provider, Origin: input.Origin, Mailbox: input.Mailbox,
		IsDefault: input.Default || len(catalog.Accounts) == 0,
	}
	if err := validateAccountView(account); err != nil {
		return AccountView{}, err
	}
	if err := service.repository.AddAccount(ctx, account); err != nil {
		return AccountView{}, fmt.Errorf("add account: %w", err)
	}
	return account, nil
}

// Rename preserves the opaque account identity and all account-owned state.
func (service *AccountService) Rename(
	ctx context.Context,
	input AccountRenameInput,
) (AccountView, error) {
	if err := domain.AccountAlias(input.NewAlias).Validate(); err != nil {
		return AccountView{}, err
	}
	account, err := service.Show(ctx, input.Account)
	if err != nil {
		return AccountView{}, err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	for _, existing := range catalog.Accounts {
		if existing.Alias == input.NewAlias && existing.ID != account.ID {
			return AccountView{}, fmt.Errorf("account alias %q already exists", input.NewAlias)
		}
	}
	if err := service.repository.RenameAccount(ctx, account.ID, input.NewAlias); err != nil {
		return AccountView{}, fmt.Errorf("rename account: %w", err)
	}
	account.Alias = input.NewAlias
	return account, nil
}

// Remove purges Corresync-owned state before deleting the configuration route.
// If the final config write fails, the account remains configured but requires
// a fresh authentication; readable session copies are never retained.
func (service *AccountService) Remove(
	ctx context.Context,
	input AccountRemoveInput,
) (AccountView, error) {
	account, err := service.Show(ctx, input.Account)
	if err != nil {
		return AccountView{}, err
	}
	catalog, err := service.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	if len(catalog.Accounts) == 1 {
		return AccountView{}, errors.New("cannot remove the only configured account")
	}
	replacement := input.ReplacementDefault
	if account.IsDefault {
		if replacement == "" {
			return AccountView{}, errors.New(
				"removing the default account requires --new-default",
			)
		}
		replacementAccount, resolveErr := service.Show(ctx, replacement)
		if resolveErr != nil {
			return AccountView{}, resolveErr
		}
		if replacementAccount.ID == account.ID {
			return AccountView{}, errors.New("replacement default must be a different account")
		}
		replacement = replacementAccount.Alias
	} else if replacement != "" {
		return AccountView{}, errors.New("--new-default is valid only when removing the default account")
	}
	if err := service.purger.PurgeAccountState(ctx, account.ID); err != nil {
		return AccountView{}, fmt.Errorf("purge account state: %w", err)
	}
	if err := service.repository.RemoveAccount(ctx, account.ID, replacement); err != nil {
		return AccountView{}, fmt.Errorf("remove account: %w", err)
	}
	return account, nil
}

func validateAccountCatalog(catalog AccountCatalog) error {
	if len(catalog.Accounts) == 0 || len(catalog.Accounts) > 32 {
		return errors.New("account catalog size is invalid")
	}
	ids := make(map[domain.AccountID]struct{}, len(catalog.Accounts))
	aliases := make(map[string]struct{}, len(catalog.Accounts))
	defaults := 0
	for _, account := range catalog.Accounts {
		if err := validateAccountView(account); err != nil {
			return err
		}
		if _, exists := ids[account.ID]; exists {
			return errors.New("account catalog contains a duplicate ID")
		}
		if _, exists := aliases[account.Alias]; exists {
			return errors.New("account catalog contains a duplicate alias")
		}
		ids[account.ID] = struct{}{}
		aliases[account.Alias] = struct{}{}
		if account.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		return errors.New("account catalog must contain exactly one default")
	}
	return nil
}

func validateAccountView(account AccountView) error {
	if err := account.ID.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(account.Alias).Validate(); err != nil {
		return err
	}
	if account.Address != "" {
		if _, _, err := normalizeDiscoveryAddress(account.Address); err != nil {
			return err
		}
	}
	if err := account.Provider.Validate(); err != nil {
		return err
	}
	if err := validateAccountOrigin(account.Origin); err != nil {
		return err
	}
	if account.Mailbox != "" {
		if _, _, err := normalizeDiscoveryAddress(account.Mailbox); err != nil {
			return errors.New("mailbox must be one bare email address")
		}
	}
	return nil
}

func validateAccountOrigin(raw string) error {
	origin, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse origin: %w", err)
	}
	if origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" ||
		origin.Path != "" && origin.Path != "/" {
		return errors.New("origin must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return nil
}
