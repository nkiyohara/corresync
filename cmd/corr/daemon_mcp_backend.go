package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

// daemonMCPBackend forwards the MCP application boundary to the sole local
// session owner. Provider credentials never enter the MCP stdio process.
type daemonMCPBackend struct {
	*daemonapi.Client
	app             *runtime
	defaultAccount  domain.AccountID
	configuration   config.Config
	accounts        *application.AccountService
	discovery       *application.AccountDiscoveryService
	approvals       *approval.Store
	accountMu       sync.RWMutex
	mutationMu      sync.Mutex
	accountMutation func(
		context.Context,
		domain.Caller,
		func(context.Context) (application.AccountView, error),
	) (application.AccountView, error)
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
	approvals, err := approval.NewStore(approval.Options{})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &daemonMCPBackend{
		Client: client, app: app,
		defaultAccount: status.DefaultAccount, configuration: configuration,
		accounts: accounts, discovery: discoverer, approvals: approvals,
	}, nil
}

func (backend *daemonMCPBackend) DefaultAccount() domain.AccountID {
	_ = backend.refreshAccountSnapshot()
	backend.accountMu.RLock()
	defer backend.accountMu.RUnlock()
	return backend.defaultAccount
}

func (backend *daemonMCPBackend) refreshAccountSnapshot() error {
	if backend.app == nil {
		return nil
	}
	configuration, _, err := backend.app.loadConfig()
	if err != nil {
		return err
	}
	account, exists := configuration.Accounts[configuration.DefaultAccount]
	if !exists {
		return errors.New("configured default account disappeared")
	}
	backend.accountMu.Lock()
	backend.configuration = configuration
	backend.defaultAccount = account.ID
	backend.accountMu.Unlock()
	return nil
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

func (backend *daemonMCPBackend) PreviewAccountAdd(
	ctx context.Context,
	input application.AccountAddInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	review, err := backend.accounts.ReviewAdd(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	operation, err := domain.NewOperation(
		"account.add",
		domain.EffectReversibleWrite,
		backend.DefaultAccount(),
		input,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preview, err := backend.approvals.Issue(operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review, Preview: &preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountAdd(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.approvals.ConsumeFor(
		token,
		caller,
		"account.add",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var input application.AccountAddInput
	if err := operation.DecodePayload(&input); err != nil {
		return application.AccountChangeAccess{}, err
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.Add(callContext, input)
		},
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) PreviewAccountRename(
	ctx context.Context,
	input application.AccountRenameInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	review, err := backend.accounts.ReviewRename(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	normalized := input
	normalized.Account = string(review.Account)
	operation, err := domain.NewOperation(
		"account.rename",
		domain.EffectReversibleWrite,
		review.Account,
		normalized,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preview, err := backend.approvals.Issue(operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review, Preview: &preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountRename(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.approvals.ConsumeFor(
		token,
		caller,
		"account.rename",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var input application.AccountRenameInput
	if err := operation.DecodePayload(&input); err != nil {
		return application.AccountChangeAccess{}, err
	}
	if input.Account != string(operation.Account()) {
		return application.AccountChangeAccess{}, errors.New(
			"approved account rename no longer matches its account",
		)
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.Rename(callContext, input)
		},
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) PreviewAccountRemove(
	ctx context.Context,
	input application.AccountRemoveInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	review, err := backend.accounts.ReviewRemove(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	normalized := input
	normalized.Account = string(review.Account)
	if review.ReplacementAccount != "" {
		normalized.ReplacementDefault = string(review.ReplacementAccount)
	}
	operation, err := domain.NewOperation(
		"account.remove",
		domain.EffectDestructiveWrite,
		review.Account,
		normalized,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preview, err := backend.approvals.Issue(operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review, Preview: &preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountRemove(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.approvals.ConsumeFor(
		token,
		caller,
		"account.remove",
		domain.EffectDestructiveWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var input application.AccountRemoveInput
	if err := operation.DecodePayload(&input); err != nil {
		return application.AccountChangeAccess{}, err
	}
	if input.Account != string(operation.Account()) {
		return application.AccountChangeAccess{}, errors.New(
			"approved account removal no longer matches its account",
		)
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.Remove(callContext, input)
		},
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) executeAccountMutation(
	ctx context.Context,
	caller domain.Caller,
	change func(context.Context) (application.AccountView, error),
) (application.AccountView, error) {
	if backend.accountMutation != nil {
		return backend.accountMutation(ctx, caller, change)
	}
	return backend.commitAccountMutation(ctx, caller, change)
}

func (backend *daemonMCPBackend) commitAccountMutation(
	ctx context.Context,
	caller domain.Caller,
	change func(context.Context) (application.AccountView, error),
) (application.AccountView, error) {
	backend.mutationMu.Lock()
	defer backend.mutationMu.Unlock()
	if backend.app == nil || backend.Client == nil {
		return application.AccountView{}, errors.New(
			"account mutation coordinator is unavailable",
		)
	}
	configPath, err := backend.app.resolvedConfigPath()
	if err != nil {
		return application.AccountView{}, err
	}
	owner, err := backend.InspectOwner(ctx, caller)
	if err != nil {
		return application.AccountView{}, fmt.Errorf(
			"inspect session owner before account mutation: %w",
			err,
		)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(
		ctx,
		daemonControlTimeout,
	)
	shutdownErr := backend.ShutdownOwner(
		shutdownContext,
		caller,
		owner,
	)
	cancelShutdown()
	transitionContext, cancelTransition := context.WithTimeout(
		context.WithoutCancel(ctx),
		daemonReplacementTimeout,
	)
	defer cancelTransition()
	_, replaced, waitErr := waitForDaemonExit(
		transitionContext,
		backend.app,
		backend.Client,
		owner.Status(),
		daemonReplacementTimeout,
	)
	if waitErr != nil {
		return application.AccountView{}, errors.Join(
			fmt.Errorf("stop session owner for account mutation: %w", waitErr),
			shutdownErr,
		)
	}
	if replaced {
		return application.AccountView{}, errors.New(
			"session owner changed concurrently; review the account mutation again",
		)
	}

	var account application.AccountView
	changeErr := ctx.Err()
	if changeErr == nil {
		account, changeErr = change(ctx)
	}
	restartContext, cancelRestart := context.WithTimeout(
		context.WithoutCancel(ctx),
		daemonReplacementTimeout+daemonStartupTimeout,
	)
	defer cancelRestart()
	restartErr := backend.restartAfterAccountMutation(
		restartContext,
		configPath,
	)
	if changeErr != nil {
		return application.AccountView{}, errors.Join(changeErr, restartErr)
	}
	if restartErr != nil {
		return application.AccountView{}, fmt.Errorf(
			"account configuration changed but restart failed; inspect `corr account list` and restart the daemon: %w",
			restartErr,
		)
	}
	return account, nil
}

func (backend *daemonMCPBackend) restartAfterAccountMutation(
	ctx context.Context,
	configPath string,
) error {
	if err := backend.app.startDaemon(ctx, configPath); err != nil {
		return fmt.Errorf("start updated session owner: %w", err)
	}
	status, err := waitForDaemon(
		ctx,
		backend.app,
		backend.Client,
		daemonStartupTimeout,
	)
	if err != nil {
		return err
	}
	digest, err := config.Fingerprint(configPath)
	if err != nil {
		return err
	}
	if err := backend.app.validateDaemonStatus(status, digest); err != nil {
		return err
	}
	configuration, err := config.Load(configPath)
	if err != nil {
		return err
	}
	backend.accountMu.Lock()
	backend.configuration = configuration
	backend.defaultAccount = status.DefaultAccount
	backend.accountMu.Unlock()
	return nil
}

func (backend *daemonMCPBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	if err := backend.refreshAccountSnapshot(); err != nil {
		return "", err
	}
	backend.accountMu.RLock()
	defer backend.accountMu.RUnlock()
	_, account, err := backend.configuration.ResolveAccount(reference)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}
